package service

import (
	"strings"
	"testing"
)

func TestIsPublicURLReachable(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"", false},
		{"http://localhost", false},
		{"http://localhost:8080", false},
		{"https://localhost", false},
		{"http://127.0.0.1", false},
		{"http://127.0.0.1:8080", false},
		{"http://[::1]", false},
		{"http://0.0.0.0:8080", false},
		{"https://email.mycompany.com", true},
		{"http://senddock.example.com", true},
		{"https://app.senddock.dev", true},
		{"not-a-url", false},
	}

	for _, tc := range cases {
		t.Run(tc.url, func(t *testing.T) {
			got := IsPublicURLReachable(tc.url)
			if got != tc.want {
				t.Errorf("IsPublicURLReachable(%q) = %v, want %v", tc.url, got, tc.want)
			}
		})
	}
}

// The footer is added at send time to both renditions of the message: a text part without
// the unsubscribe link the HTML carries would be a compliance gap, so the pair is checked
// together.
func TestEnsureUnsubscribeFooter(t *testing.T) {
	t.Run("no placeholder appends footer to both parts", func(t *testing.T) {
		got := ensureUnsubscribeFooter(bodyParts{html: "<p>Hello</p>", text: "Hello"})

		if !strings.Contains(got.html, "{{unsubscribe_url}}") {
			t.Error("expected footer with unsubscribe placeholder appended to the html part")
		}
		if !strings.HasPrefix(got.html, "<p>Hello</p>") {
			t.Error("original body should still be at the start")
		}
		if !strings.Contains(got.text, "Unsubscribe: {{unsubscribe_url}}") {
			t.Errorf("text part gained no unsubscribe link: %q", got.text)
		}
		if !strings.HasPrefix(got.text, "Hello") {
			t.Errorf("text part should keep its content first: %q", got.text)
		}
	})

	t.Run("placeholder present is left alone", func(t *testing.T) {
		original := bodyParts{
			html: `<p>Hi <a href="{{unsubscribe_url}}">unsub</a></p>`,
			text: `Hi unsub ({{unsubscribe_url}})`,
		}
		if got := ensureUnsubscribeFooter(original); got != original {
			t.Errorf("parts with a placeholder should not be modified, got %+v", got)
		}
	})

	t.Run("inserts before closing body tag", func(t *testing.T) {
		got := ensureUnsubscribeFooter(bodyParts{html: "<html><body><p>Hi</p></body></html>", text: "Hi"})

		if !strings.Contains(got.html, "{{unsubscribe_url}}") {
			t.Errorf("expected unsubscribe placeholder in result, got %q", got.html)
		}
		if !strings.HasSuffix(got.html, "</body></html>") {
			t.Errorf("closing tags should remain at the end, got %q", got.html)
		}
		footerStart := strings.Index(got.html, "<hr")
		bodyClose := strings.LastIndex(got.html, "</body>")
		if footerStart == -1 || footerStart > bodyClose {
			t.Errorf("footer must be inserted before </body>, footerStart=%d bodyClose=%d", footerStart, bodyClose)
		}
	})

	t.Run("a caller with no text part is not given one", func(t *testing.T) {
		got := ensureUnsubscribeFooter(bodyParts{html: "<p>Hi</p>"})

		if got.text != "" {
			t.Errorf("text part = %q, want it left empty", got.text)
		}
		if !strings.Contains(got.html, "{{unsubscribe_url}}") {
			t.Error("the html part should still gain the footer")
		}
	})
}

type staticPublicURL string

func (s staticPublicURL) PublicURL() string { return string(s) }

func TestSignUnsubVerifyRoundTrip(t *testing.T) {
	s := &EmailService{settings: staticPublicURL("https://example.com"), encSecret: "test-secret"}
	pid := "11111111-1111-1111-1111-111111111111"
	sid := "22222222-2222-2222-2222-222222222222"

	token := s.signUnsub(pid, sid)
	if token == "" {
		t.Fatal("expected non-empty token")
	}

	if !s.verifyUnsubToken(pid, sid, token) {
		t.Error("freshly signed token should verify")
	}

	if s.verifyUnsubToken(pid, sid, token+"x") {
		t.Error("tampered token must not verify")
	}

	if s.verifyUnsubToken(pid, "different-sid", token) {
		t.Error("token bound to a different subscriber must not verify")
	}

	otherSecret := &EmailService{settings: staticPublicURL("https://example.com"), encSecret: "other-secret"}
	if otherSecret.verifyUnsubToken(pid, sid, token) {
		t.Error("token signed with one secret must not verify under another")
	}
}

func TestUnsubURLIncludesSignedToken(t *testing.T) {
	s := &EmailService{settings: staticPublicURL("https://email.example.com"), encSecret: "k"}
	pid := "abcd"
	sid := "efgh"

	url := s.unsubURL(pid, sid)
	if !strings.HasPrefix(url, "https://email.example.com/unsubscribe/abcd/efgh?t=") {
		t.Errorf("unexpected URL shape: %s", url)
	}
}
