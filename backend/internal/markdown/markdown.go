// Package markdown renders the listing's Markdown (an app's description,
// release notes) to HTML that is safe to put in a page: goldmark without raw
// HTML, then a bluemonday allow list. No script, no style attributes, no
// forms or inputs, no frames, no images (the page's CSP would block
// third-party images anyway, and the store never hot-links), links get
// rel="nofollow noreferrer noopener".
package markdown

import (
	"bytes"
	"regexp"
	"sync"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// MaxInput is the most Markdown rendered; the rest is cut.
const MaxInput = 64 << 10

var (
	once   sync.Once
	md     goldmark.Markdown
	policy *bluemonday.Policy
)

func setup() {
	md = goldmark.New(goldmark.WithExtensions(
		extension.NewTable(extension.WithTableCellAlignMethod(extension.TableCellAlignAttribute)),
		extension.Strikethrough, extension.Linkify))

	p := bluemonday.NewPolicy()
	p.AllowElements("p", "br", "hr", "h1", "h2", "h3", "h4", "h5", "h6",
		"ul", "ol", "li", "blockquote", "pre", "code", "em", "strong", "del", "s",
		"table", "thead", "tbody", "tr", "th", "td")
	p.AllowAttrs("href").OnElements("a")
	p.AllowElements("a")
	p.AllowURLSchemes("http", "https", "mailto")
	p.RequireParseableURLs(true)
	p.RequireNoFollowOnLinks(true)
	p.RequireNoReferrerOnLinks(true)
	p.AddTargetBlankToFullyQualifiedLinks(true)
	p.AllowAttrs("class").Matching(regexp.MustCompile(`^language-[A-Za-z0-9_+-]{1,32}$`)).OnElements("code")
	p.AllowAttrs("start").Matching(regexp.MustCompile(`^[0-9]{1,6}$`)).OnElements("ol")
	p.AllowAttrs("align").Matching(regexp.MustCompile(`^(left|right|center)$`)).OnElements("th", "td")
	policy = p
}

// Render turns Markdown into sanitized HTML. Empty input is "".
func Render(src string) string {
	once.Do(setup)
	if src == "" {
		return ""
	}
	if len(src) > MaxInput {
		src = src[:MaxInput]
	}
	var buf bytes.Buffer
	if err := md.Convert([]byte(src), &buf); err != nil {
		return policy.Sanitize(src)
	}
	return string(policy.SanitizeBytes(buf.Bytes()))
}
