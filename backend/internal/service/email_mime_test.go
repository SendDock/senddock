package service

import (
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
)

var mimeHeaderPrefixes = []string{
	"From:",
	"To:",
	"Subject:",
	"MIME-Version:",
	"Content-Type:",
}

// A subject is built from subscriber-supplied text, so a value carrying a line break
// must not be able to close the Subject line and start a header of its own.
func TestBuildMIMERejectsHeaderInjection(t *testing.T) {
	msg := string(buildMIME(
		"Sender <from@example.test>",
		"recipient@example.test",
		"Hello\r\nBcc: victim@example.test\r\nX-Injected: 1",
		"https://example.test/unsubscribe?t=abc\r\nBcc: victim2@example.test",
		bodyParts{html: "<p>body</p>"},
	))

	headerBlock, body, found := strings.Cut(msg, "\r\n\r\n")
	if !found {
		t.Fatalf("message has no header/body separator:\n%q", msg)
	}

	want := append(append([]string{}, mimeHeaderPrefixes...), "List-Unsubscribe:", "List-Unsubscribe-Post:")
	lines := strings.Split(headerBlock, "\r\n")
	if len(lines) != len(want) {
		t.Fatalf("message carries %d header lines, want %d — an injected header got through:\n%s", len(lines), len(want), headerBlock)
	}
	for i, prefix := range want {
		if !strings.HasPrefix(lines[i], prefix) {
			t.Errorf("header %d is %q, want a line starting with %q", i, lines[i], prefix)
		}
	}
	if body != "<p>body</p>" {
		t.Errorf("body = %q, want it untouched", body)
	}
}

// The fix strips line breaks rather than truncating, so the rest of the value survives
// as part of the same header.
func TestBuildMIMEKeepsInjectedTextInsideTheHeaderValue(t *testing.T) {
	msg := string(buildMIME("from@example.test", "to@example.test",
		"Hi\r\nBcc: victim@example.test", "", bodyParts{html: "<p>body</p>"}))

	headerBlock, _, _ := strings.Cut(msg, "\r\n\r\n")
	if !strings.Contains(headerBlock, "Subject: HiBcc: victim@example.test") {
		t.Fatalf("subject value was not preserved on one line:\n%s", headerBlock)
	}
}

// A message with no text rendition keeps the shape it has always had, so the plain-text
// part is an addition and never a change to what already worked.
func TestBuildMIMEKeepsTheOrdinaryShape(t *testing.T) {
	bare := string(buildMIME("from@example.test", "to@example.test", "Subject", "", bodyParts{html: "<p>hi</p>"}))
	headerBlock, body, found := strings.Cut(bare, "\r\n\r\n")
	if !found {
		t.Fatalf("no separator in:\n%q", bare)
	}
	if lines := strings.Split(headerBlock, "\r\n"); len(lines) != len(mimeHeaderPrefixes) {
		t.Fatalf("message without an unsubscribe URL carries %d headers, want %d:\n%s", len(lines), len(mimeHeaderPrefixes), headerBlock)
	}
	if !strings.Contains(headerBlock, "Content-Type: text/html; charset=UTF-8") {
		t.Errorf("single-part message should declare its html type:\n%s", headerBlock)
	}
	if body != "<p>hi</p>" {
		t.Errorf("body = %q", body)
	}
}

// A message with a text rendition has to be real, parseable MIME: the parts are read back
// with the standard library's parser rather than by matching strings, so a boundary that
// never closes or a part declared in the wrong order fails here.
func TestBuildMIMEWithTextIsParseableMultipart(t *testing.T) {
	raw := buildMIME("Sender <from@example.test>", "recipient@example.test", "Subject",
		"https://example.test/unsubscribe?t=abc",
		bodyParts{html: "<p>Hello</p>", text: "Hello"})

	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("the message is not valid mail: %v\n%s", err, raw)
	}

	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("Content-Type is not valid: %v", err)
	}
	if mediaType != "multipart/alternative" {
		t.Fatalf("Content-Type = %q, want multipart/alternative", mediaType)
	}
	if params["boundary"] == "" {
		t.Fatal("multipart message declares no boundary")
	}
	if got := msg.Header.Get("List-Unsubscribe"); got != "<https://example.test/unsubscribe?t=abc>" {
		t.Errorf("List-Unsubscribe = %q", got)
	}

	reader := multipart.NewReader(msg.Body, params["boundary"])
	var kinds []string
	var contents []string
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading part %d: %v", len(kinds), err)
		}
		body, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("reading part %d body: %v", len(kinds), err)
		}
		kinds = append(kinds, part.Header.Get("Content-Type"))
		contents = append(contents, string(body))
	}

	if len(kinds) != 2 {
		t.Fatalf("message carries %d parts, want 2: %v", len(kinds), kinds)
	}
	if !strings.HasPrefix(kinds[0], "text/plain") {
		t.Errorf("first part is %q, want the plain-text part first", kinds[0])
	}
	if !strings.HasPrefix(kinds[1], "text/html") {
		t.Errorf("second part is %q, want the html part last", kinds[1])
	}
	if contents[0] != "Hello" {
		t.Errorf("plain-text part = %q", contents[0])
	}
	if contents[1] != "<p>Hello</p>" {
		t.Errorf("html part = %q", contents[1])
	}
}

// A delimiter that occurs inside the content would cut a part in half, so the boundary has
// to be one the content does not contain.
func TestBuildMIMEBoundaryIsNotInTheContent(t *testing.T) {
	parts := bodyParts{html: "<p>=_senddock_alternative</p>", text: "=_senddock_alternative"}
	raw := string(buildMIME("from@example.test", "to@example.test", "Subject", "", parts))

	msg, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("the message is not valid mail: %v", err)
	}
	_, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("Content-Type is not valid: %v", err)
	}

	boundary := params["boundary"]
	headerEnd := strings.Index(raw, "\r\n\r\n") + len("\r\n\r\n")
	body := raw[headerEnd:]
	if strings.Count(body, boundary) != 3 {
		t.Fatalf("boundary %q appears %d times, want exactly the 3 delimiters", boundary, strings.Count(body, boundary))
	}
	if boundary == "=_senddock_alternative" {
		t.Fatal("the fallback boundary was used although it occurs in the content")
	}
}
