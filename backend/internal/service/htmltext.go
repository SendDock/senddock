package service

import (
	"html"
	"regexp"
	"strings"
)

var (
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

	body = strings.ReplaceAll(html.UnescapeString(body), "\u00a0", " ")
	body = htmlToTextSpaces.ReplaceAllString(body, " ")
	body = htmlToTextIndent.ReplaceAllString(body, "\n")
	body = htmlToTextBlanks.ReplaceAllString(body, "\n\n")

	return strings.TrimSpace(body)
}

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
