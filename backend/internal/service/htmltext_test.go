package service

import (
	"strings"
	"testing"
)

func TestHTMLToTextStripsMarkupAndKeepsProse(t *testing.T) {
	got := htmlToText(`<html><head><title>ignored</title><style>.a{color:red}</style></head>
		<body><h1>Hello</h1><p>First line</p><p>Second line</p></body></html>`)

	want := "Hello\nFirst line\nSecond line"
	if got != want {
		t.Errorf("htmlToText =\n%q\nwant\n%q", got, want)
	}
}

// A text part that dropped the links would be missing the unsubscribe link the HTML
// carries, so the destination has to survive the conversion.
func TestHTMLToTextKeepsLinkDestinations(t *testing.T) {
	got := htmlToText(`<p>Don't want these? <a href="{{unsubscribe_url}}">Unsubscribe</a>.</p>`)

	if !strings.Contains(got, "Unsubscribe ({{unsubscribe_url}})") {
		t.Errorf("link destination was lost: %q", got)
	}
}

func TestHTMLToTextDoesNotRepeatALinkThatIsItsOwnText(t *testing.T) {
	got := htmlToText(`<a href="https://example.test/x">https://example.test/x</a>`)

	if got != "https://example.test/x" {
		t.Errorf("htmlToText = %q, want the bare url once", got)
	}
}

func TestHTMLToTextDropsNonProseContent(t *testing.T) {
	got := htmlToText(`<script>alert("x")</script><style>p{color:red}</style><!-- hidden --><p>Visible</p>`)

	if got != "Visible" {
		t.Errorf("htmlToText = %q, want only the visible prose", got)
	}
}

func TestHTMLToTextUnescapesEntitiesAndCollapsesWhitespace(t *testing.T) {
	got := htmlToText("<p>Tom&nbsp;&amp;&nbsp;Jerry</p>\n\n\n   <p>  spaced   out  </p>")

	want := "Tom & Jerry\n\nspaced out"
	if got != want {
		t.Errorf("htmlToText =\n%q\nwant\n%q", got, want)
	}
}

func TestHTMLToTextOnEmptyInput(t *testing.T) {
	if got := htmlToText(""); got != "" {
		t.Errorf("htmlToText(\"\") = %q, want empty", got)
	}
}

// The rendered text is what a plain-text client shows, so it must not contain a single
// tag or entity even when the markup nests and carries attributes.
func TestHTMLToTextLeavesNoMarkupBehind(t *testing.T) {
	got := htmlToText(`<table role="presentation"><tr><td style="padding:0"><span style="font:12px Arial">Cell &ndash; text</span></td></tr></table>`)

	if strings.ContainsAny(got, "<>") {
		t.Errorf("htmlToText left markup behind: %q", got)
	}
	if !strings.Contains(got, "Cell – text") {
		t.Errorf("htmlToText lost the content: %q", got)
	}
}
