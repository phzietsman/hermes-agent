package webtools

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// DefaultCharLimit is the per-page budget sent to the model; larger pages are head+tail truncated and the
	// full text is stored on disk.
	DefaultCharLimit = 15000
	// maxStoredChars bounds the stored copy so a multi-MB page cannot write unbounded bytes on every extract.
	maxStoredChars = 2_000_000
	charLimitFloor = 2000
	charLimitCeil  = 500_000
)

// clampLimit applies the caller's char_limit (0 = unset) within [2k, 500k]: below 2k the footer dominates, and a
// typo must not blow up context.
func clampLimit(n int, set bool) int {
	if !set {
		return DefaultCharLimit
	}
	return max(charLimitFloor, min(n, charLimitCeil))
}

var (
	mdBase64   = regexp.MustCompile(`!\[([^\]]*)\]\(\s*data:image/[^;]+;base64,[A-Za-z0-9+/=\s]+\)`)
	parenBlob  = regexp.MustCompile(`\(\s*data:image/[^;]+;base64,[A-Za-z0-9+/=\s]+\)`)
	bareBase64 = regexp.MustCompile(`data:image/[^;]+;base64,[A-Za-z0-9+/=]+`)
)

// ConvertBase64Images replaces inline base64 image blobs (token bombs) with [IMAGE: alt] placeholders. Real
// http(s) image links are left alone so the agent can fetch them.
func ConvertBase64Images(text string) string {
	out := mdBase64.ReplaceAllStringFunc(text, func(m string) string {
		alt := strings.TrimSpace(mdBase64.FindStringSubmatch(m)[1])
		if alt == "" {
			return "[IMAGE]"
		}
		return "[IMAGE: " + alt + "]"
	})
	out = parenBlob.ReplaceAllString(out, "[IMAGE]")
	return bareBase64.ReplaceAllString(out, "[IMAGE]")
}

func commas(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// TruncateWithFooter returns the model text and whether it was truncated. Pages over limit become a ~75% head /
// ~25% tail window cut on line boundaries plus a footer saying how much is shown, where the full text is stored
// (stored, "" when it could not be) and the read_file call that pages the omitted middle.
func TruncateWithFooter(content string, limit int, stored func() string) (string, bool) {
	r := []rune(content)
	if len(r) <= limit {
		return content, false
	}
	headBudget := int(float64(limit) * 0.75)
	tailBudget := limit - headBudget
	head, tail := r[:headBudget], r[len(r)-tailBudget:]
	if nl := lastRune(head, '\n'); float64(nl) > float64(headBudget)*0.5 {
		head = head[:nl]
	}
	if nl := firstRune(tail, '\n'); nl >= 0 && float64(nl) < float64(tailBudget)*0.5 {
		tail = tail[nl+1:]
	}
	path := stored()
	bar := strings.Repeat("─", 8)
	footer := []string{"", bar + " [TRUNCATED] " + bar,
		fmt.Sprintf("Showing %s chars (head) + %s chars (tail) of %s total clean characters.", commas(len(head)), commas(len(tail)), commas(len(r)))}
	if path != "" {
		middle := strings.Count(string(head), "\n") + 2
		footer = append(footer, "Full text saved to: "+path,
			fmt.Sprintf(`To read the omitted middle: read_file path="%s" offset=%d limit=200  (the file is the complete page; raise/lower offset to page through it).`, path, middle))
	} else {
		footer = append(footer, "Full text could not be stored; re-run web_extract on a more specific URL or use browser_navigate for the complete page.")
	}
	footer = append(footer, strings.Repeat("─", 29))
	return string(head) + "\n\n[... middle omitted — see footer ...]\n\n" + string(tail) + "\n" + strings.Join(footer, "\n"), true
}

func lastRune(r []rune, c rune) int {
	for i := len(r) - 1; i >= 0; i-- {
		if r[i] == c {
			return i
		}
	}
	return -1
}

func firstRune(r []rune, c rune) int {
	for i, x := range r {
		if x == c {
			return i
		}
	}
	return -1
}

var slugBad = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// hostname reads the host of raw leniently, as Python's urlparse does (a malformed authority still yields a
// host), lower-cased, without userinfo, brackets or port.
func hostname(raw string) string {
	_, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return ""
	}
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	if i := strings.LastIndex(rest, "@"); i >= 0 {
		rest = rest[i+1:]
	}
	if strings.HasPrefix(rest, "[") {
		if i := strings.Index(rest, "]"); i >= 0 {
			return strings.ToLower(rest[1:i])
		}
	}
	if i := strings.LastIndex(rest, ":"); i >= 0 {
		rest = rest[:i]
	}
	return strings.ToLower(rest)
}

// hostSlug is a filesystem-safe hostname for cache filenames ("page" when hostless).
func hostSlug(raw string) string {
	host := hostname(raw)
	if host == "" {
		host = "page"
	}
	host = slugBad.ReplaceAllString(strings.ReplaceAll(host, ":", "_"), "-")
	if utf8.RuneCountInString(host) > 60 {
		host = string([]rune(host)[:60])
	}
	if host = strings.Trim(host, "-"); host == "" {
		return "page"
	}
	return host
}

// storeFullText writes the full page under <home>/cache/web and returns its path, or "" when it cannot (the
// truncated text is still returned). Same-URL re-extraction overwrites; a symlink at the path is replaced, not followed.
func storeFullText(home, pageURL, content string) string {
	if home == "" {
		return ""
	}
	if n := utf8.RuneCountInString(content); n > maxStoredChars {
		content = string([]rune(content)[:maxStoredChars]) +
			fmt.Sprintf("\n\n[... stored copy truncated at %s chars of %s; re-extract a more specific URL for the rest ...]", commas(maxStoredChars), commas(n))
	}
	dir := filepath.Join(home, "cache", "web")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(pageURL))
	path := filepath.Join(dir, hostSlug(pageURL)+"-"+hex.EncodeToString(sum[:])[:10]+".md")
	_ = os.Remove(path)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return ""
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		return ""
	}
	return path
}
