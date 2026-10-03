package service

import (
	"html"
	"regexp"
	"strings"
)

// htmlToText renders an HTML body as plain text, for the text/plain part of a message.
// A template that carries no text_body of its own gets one this way when it is saved, and
// a caller sending raw HTML gets the same treatment at send time, so a message always has
// a text rendition to ship alongside the HTML.
//
// This is deliberately a small tag-stripper rather than a full HTML parser: the input is
// email HTML produced by the template editor, and the output only has to be readable and
// keep the links the HTML was pointing at.

var (
	// Elements whose contents are not prose and must not leak into the text part.
	htmlToTextDropBlocks = []*regexp.Regexp{
		regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`),
		regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`),
		regexp.MustCompile(`(?is)<head[^>]*>.*?</head>`),
		regexp.MustCompile(`(?s)<!--.*?-->`),
	}
	htmlToTextAnchor = regexp.MustCompile(`(?is)<a\s[^>]*href\s*=\s*["']([^"']*)["'][^>]*>(.*?)</a>`)
	htmlToTextBreak  = regexp.MustCompile(`(?i)<(?:br|hr)[^>]*>`)
	htmlToTextEnd    = regexp.MustCompile(`(?i)</(?:p|div|tr|h[1-6]|li|blockquote|table|ul|ol|section|article|header|footer)[^>]*>`)
	htmlToTextTag    = regexp.MustCompile(`(?s)<[^>]*>`)
	htmlToTextSpaces = regexp.MustCompile(`[^\S\n]+`)
	htmlToTextIndent = regexp.MustCompile(` *\n *`)
	htmlToTextBlanks = regexp.MustCompile(`\n{2,}`)
)

func htmlToText(body string) string {
	body = strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\r", "\n")

	for _, drop := range htmlToTextDropBlocks {
		body = drop.ReplaceAllString(body, "")
	}

	body = htmlToTextAnchor.ReplaceAllStringFunc(body, htmlToTextAnchorLabel)
	body = htmlToTextBreak.ReplaceAllString(body, "\n")
	body = htmlToTextEnd.ReplaceAllString(body, "\n")
	body = htmlToTextTag.ReplaceAllString(body, "")

	// Non-breaking spaces arrive as U+00A0, which would survive the whitespace collapse
	// and show up as a stray character in a plain-text client.
	body = strings.ReplaceAll(html.UnescapeString(body), "\u00a0", " ")
	body = htmlToTextSpaces.ReplaceAllString(body, " ")
	body = htmlToTextIndent.ReplaceAllString(body, "\n")
	body = htmlToTextBlanks.ReplaceAllString(body, "\n\n")

	return strings.TrimSpace(body)
}

// htmlToTextAnchorLabel keeps the destination of a link next to its text. Without it the
// text part of a message whose HTML says "click here" would lose the only thing it was
// pointing at, and the unsubscribe link with it. The destination is wrapped in
// parentheses rather than angle brackets, which the tag stripper would remove as markup.
func htmlToTextAnchorLabel(anchor string) string {
	groups := htmlToTextAnchor.FindStringSubmatch(anchor)
	if len(groups) != 3 {
		return anchor
	}

	href := strings.TrimSpace(groups[1])
	label := strings.TrimSpace(html.UnescapeString(htmlToTextTag.ReplaceAllString(groups[2], "")))

	switch {
	case href == "":
		return label
	case label == "" || label == href:
		return href
	default:
		return label + " (" + href + ")"
	}
}
