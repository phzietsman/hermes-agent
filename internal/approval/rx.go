package approval

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dlclark/regexp2"
)

// matchTimeout bounds one pattern match. The patterns are written to avoid catastrophic backtracking, but a
// regex that cannot answer in time is treated as a match, never as a pass: detection fails closed.
const matchTimeout = 2 * time.Second

// rx is a compiled detection pattern. Go's RE2 has no lookaround or atomic groups, which the Python patterns
// use, so these run on a backtracking engine with the same case-insensitive, dot-matches-newline flags.
type rx struct{ re *regexp2.Regexp }

var unicodeEscapeRe = regexp.MustCompile(`\\U([0-9A-Fa-f]{8})`)

// translate rewrites Python regex syntax the .NET-style engine spells differently: named groups, possessive
// quantifiers (rewritten as atomic groups) and \UXXXXXXXX escapes.
func translate(p string) string {
	p = strings.ReplaceAll(p, "(?P<", "(?<")
	p = strings.ReplaceAll(p, `\S++`, `(?>\S+)`)
	p = strings.ReplaceAll(p, `\s++`, `(?>\s+)`)
	return unicodeEscapeRe.ReplaceAllStringFunc(p, func(m string) string {
		n, _ := strconv.ParseUint(m[2:], 16, 32)
		return regexp.QuoteMeta(string(rune(n)))
	})
}

func mustCompile(p string) rx {
	re, err := regexp2.Compile(translate(p), regexp2.IgnoreCase|regexp2.Singleline)
	if err != nil {
		panic(fmt.Sprintf("approval: pattern %q: %v", p, err))
	}
	re.MatchTimeout = matchTimeout
	return rx{re}
}

// search reports whether the pattern matches anywhere in s. A timeout counts as a match.
func (r rx) search(s string) bool {
	ok, err := r.re.MatchString(s)
	return ok || err != nil
}
