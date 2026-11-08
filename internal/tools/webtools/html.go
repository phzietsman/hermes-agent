package webtools

import (
	"html"
	"regexp"
	"strings"
)

var (
	dropBlocks = regexp.MustCompile(`(?is)<(script|style|noscript|template|svg|head|title)\b.*?</(script|style|noscript|template|svg|head|title)\s*>`)
	comments   = regexp.MustCompile(`(?s)<!--.*?-->`)
	titleRe    = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	paraClose  = regexp.MustCompile(`(?i)</(p|h[1-6]|ul|ol|table|blockquote|pre|figure)\s*>`)
	breakTags  = regexp.MustCompile(`(?i)</?(p|div|section|article|header|footer|nav|main|aside|ul|ol|table|tr|h[1-6]|blockquote|pre|form|figure|br|hr)\b[^>]*>`)
	liOpen     = regexp.MustCompile(`(?i)<li\b[^>]*>`)
	anyTag     = regexp.MustCompile(`(?s)<[^>]*>`)
	hSpace     = regexp.MustCompile(`[ \t\f\v\x{a0}]+`)
)

// HTMLText reduces an HTML page to readable text and its title: scripts, styles and comments are dropped,
// block tags become line breaks, list items get a bullet, entities are decoded and whitespace collapsed.
func HTMLText(page string) (title, text string) {
	if m := titleRe.FindStringSubmatch(page); m != nil {
		title = strings.Join(strings.Fields(html.UnescapeString(anyTag.ReplaceAllString(m[1], ""))), " ")
	}
	page = strings.ReplaceAll(page, "\x00", "")
	page = comments.ReplaceAllString(page, "")
	page = dropBlocks.ReplaceAllString(page, "")
	page = liOpen.ReplaceAllString(page, "\n- ")
	page = paraClose.ReplaceAllString(page, "\n"+paraMark+"\n")
	page = breakTags.ReplaceAllString(page, "\n")
	page = html.UnescapeString(anyTag.ReplaceAllString(page, ""))
	var out []string
	blank := false
	for _, l := range strings.Split(page, "\n") {
		l = strings.TrimSpace(hSpace.ReplaceAllString(l, " "))
		switch {
		case l == paraMark:
			blank = true
		case l != "":
			if blank && len(out) > 0 {
				out = append(out, "")
			}
			blank = false
			out = append(out, l)
		}
	}
	return title, strings.Join(out, "\n")
}

// paraMark tags a paragraph boundary until blank lines are collapsed; it cannot occur in decoded text because
// the NUL is removed first.
const paraMark = "\x00"
