package approval

import (
	"regexp"
	"strings"
	"unicode"
)

// The shell scanners work on runes, as the Python originals work on code points, and never expand anything:
// they only follow quoting, escaping and substitution so detection can tell command words from data.

func isSpace(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }

func hasAt(t []rune, s string, i int) bool {
	sr := []rune(s)
	if i < 0 || i+len(sr) > len(t) {
		return false
	}
	for k, r := range sr {
		if t[i+k] != r {
			return false
		}
	}
	return true
}

type stepKind int

const (
	kChar stepKind = iota
	kEsc
	kQuote
	kSubst
	kComment
)

// step is one lexical step: [i, j) of the text, read in quote state quote (0, '\” or '"'). j is -1 for an
// unterminated substitution reported under stopUnterminated.
type step struct {
	kind  stepKind
	i, j  int
	quote rune
}

type scanOpts struct {
	subst            string // "u": substitutions unquoted, "q": inside double quotes
	brace            bool
	stopUnterminated bool
	naiveBacktick    bool
	comments         bool
}

// scanShell yields steps over t[start:end] until yield returns false. It is the one quote/escape state machine
// behind every detection scanner.
func scanShell(t []rune, start, end int, o scanOpts, yield func(step) bool) {
	n := end
	if n < 0 {
		n = len(t)
	}
	var quote rune
	i := start
	for i < n {
		ch := t[i]
		kind, j := kChar, i+1
		switch {
		case o.comments && quote == 0 && isCommentStart(t, i):
			kind = kComment
			j = indexRune(t, '\n', i, n)
			if j < 0 {
				j = n
			}
		case quote != '\'' && ch == '\\' && i+1 < n:
			kind, j = kEsc, i+2
		case (quote != 0 && ch == quote) || (quote == 0 && (ch == '\'' || ch == '"')):
			kind = kQuote
		default:
			key := "u"
			if quote != 0 {
				key = "q"
			}
			if strings.Contains(o.subst, key) && (ch == '`' || hasAt(t, "$(", i) || (o.brace && quote == 0 && hasAt(t, "${", i))) {
				close := -1
				switch {
				case ch == '`':
					if o.naiveBacktick {
						if k := indexRune(t, '`', i+1, len(t)); k >= 0 {
							close = k + 1
						}
					} else {
						close = scanBacktickEnd(t, i)
					}
				case t[i+1] == '(':
					close = scanDollarParenEnd(t, i)
				default:
					if k := indexRune(t, '}', i+2, len(t)); k >= 0 {
						close = k + 1
					}
				}
				if close >= 0 {
					kind, j = kSubst, close
				} else if o.stopUnterminated {
					yield(step{kSubst, i, -1, quote})
					return
				}
			}
		}
		if !yield(step{kind, i, j, quote}) {
			return
		}
		if kind == kQuote {
			if quote != 0 {
				quote = 0
			} else {
				quote = ch
			}
		}
		i = j
	}
}

func indexRune(t []rune, r rune, from, to int) int {
	for k := from; k < to && k < len(t); k++ {
		if t[k] == r {
			return k
		}
	}
	return -1
}

func isCommentStart(t []rune, i int) bool {
	return t[i] == '#' && (i == 0 || isSpace(t[i-1]) || strings.ContainsRune(";&|()<>", t[i-1]))
}

// scanDollarParenEnd returns the offset after the balanced $(...) that starts at start, or -1.
func scanDollarParenEnd(t []rune, start int) int {
	depth := 1
	result := -1
	scanShell(t, start+2, -1, scanOpts{}, func(s step) bool {
		if s.kind == kChar && s.quote == 0 {
			if hasAt(t, "$(", s.i) {
				depth++
			}
			if t[s.i] == ')' {
				depth--
			}
			if depth == 0 {
				result = s.i + 1
				return false
			}
		}
		return true
	})
	return result
}

// scanBacktickEnd returns the offset after the backtick closing the one at start, or -1. Backticks have no quote
// awareness: only a backslash escapes the next character.
func scanBacktickEnd(t []rune, start int) int {
	i := start + 1
	for i < len(t) {
		switch t[i] {
		case '`':
			return i + 1
		case '\\':
			if i+1 >= len(t) {
				return -1
			}
			i += 2
		default:
			i++
		}
	}
	return -1
}

func skipSpace(t []rune, pos int) int {
	for pos < len(t) && isSpace(t[pos]) {
		pos++
	}
	return pos
}

// readShellWord reads one word without executing expansions: (start, end, text).
func readShellWord(t []rune, pos int) (int, int, string) {
	start := skipSpace(t, pos)
	end := start
	scanShell(t, start, -1, scanOpts{subst: "u", brace: true}, func(s step) bool {
		if s.kind == kChar && s.quote == 0 && (isSpace(t[s.i]) || strings.ContainsRune(";&|<>()", t[s.i])) {
			return false
		}
		end = s.j
		return true
	})
	return start, end, string(t[start:end])
}

var (
	paramReplacementRe = regexp.MustCompile(`\$\{[^}/\s]+/[^}/]*/([^}]*)\}`)
	paramDefaultRe     = regexp.MustCompile(`\$\{[^}:}\s]+:-([^}]*)\}`)
	simpleLiteralRe    = regexp.MustCompile(`^[A-Za-z0-9_./:@%+=,-]+$`)
	echoFlagsRe        = regexp.MustCompile(`^-[nEe]+$`)
)

// literalCommandSubstitutionOutput resolves tiny literal substitutions (echo/printf of a plain word) without a shell.
func literalCommandSubstitutionOutput(script string) (string, bool) {
	tokens, err := shlexSplit(script, false)
	if err != nil || len(tokens) == 0 {
		return "", false
	}
	command, args := strings.ToLower(tokens[0]), tokens[1:]
	switch command {
	case "echo":
		for len(args) > 0 && echoFlagsRe.MatchString(args[0]) {
			args = args[1:]
		}
	case "printf":
	default:
		return "", false
	}
	if len(args) == 1 && simpleLiteralRe.MatchString(args[0]) {
		return args[0], true
	}
	if command == "printf" && len(args) == 2 && args[0] == "%s" && simpleLiteralRe.MatchString(args[1]) {
		return args[1], true
	}
	return "", false
}

func replaceSimpleCommandSubstitutions(word string) string {
	w := []rune(word)
	var out strings.Builder
	for i := 0; i < len(w); {
		opener := 0
		switch {
		case hasAt(w, "$(", i):
			opener = 2
		case w[i] == '`':
			opener = 1
		}
		end := -1
		if opener == 2 {
			end = scanDollarParenEnd(w, i)
		} else if opener == 1 {
			end = scanBacktickEnd(w, i)
		}
		if end >= 0 {
			if rep, ok := literalCommandSubstitutionOutput(string(w[i+opener : end-1])); ok {
				out.WriteString(rep)
				i = end
				continue
			}
		}
		out.WriteRune(w[i])
		i++
	}
	return out.String()
}

func replaceSimpleShellExpansions(word string) string {
	word = replaceSimpleCommandSubstitutions(word)
	word = paramReplacementRe.ReplaceAllString(word, "$1")
	return paramDefaultRe.ReplaceAllString(word, "$1")
}

func stripShellWordSyntax(word string) string {
	w := []rune(word)
	var out strings.Builder
	scanShell(w, 0, -1, scanOpts{}, func(s step) bool {
		switch s.kind {
		case kEsc:
			out.WriteRune(w[s.i+1])
		case kQuote:
		default:
			out.WriteRune(w[s.i])
		}
		return true
	})
	return out.String()
}

// deobfuscateWord approximates how shell syntax can spell a command word: quoting, escaping and simple literal
// substitutions collapse. It is narrow and never executes anything.
func deobfuscateWord(word string) string {
	for i := 0; i < 2; i++ {
		previous := word
		word = stripShellWordSyntax(replaceSimpleShellExpansions(word))
		if word == previous {
			break
		}
	}
	return word
}

var commandTransitions = map[string]bool{"if": true, "then": true, "else": true, "elif": true, "do": true, "while": true, "until": true, "!": true}

// commandStarts lists the offsets where a command begins: the start, after separators and openers, inside
// substitutions, and after the keywords that introduce a command.
func commandStarts(t []rune) []int {
	starts := []int{0}
	var scan func(start, end int)
	scan = func(start, end int) {
		skip := -1
		scanShell(t, start, end, scanOpts{subst: "uq", stopUnterminated: true, comments: true}, func(s step) bool {
			switch {
			case s.kind == kSubst:
				inner := s.i + 2
				if t[s.i] == '`' {
					inner = s.i + 1
				}
				starts = append(starts, inner)
				if s.j < 0 {
					scan(inner, end)
				} else {
					scan(inner, s.j-1)
				}
			case s.kind == kChar && s.quote == 0 && s.i != skip:
				c := t[s.i]
				switch {
				case c == '(' || c == ';' || c == '\n' ||
					(c == '{' && (s.i == 0 || isSpace(t[s.i-1]) || strings.ContainsRune("(;&|)", t[s.i-1]))):
					starts = append(starts, s.i+1)
				case c == '&' || c == '|':
					repeated := 0
					limit := end
					if limit < 0 {
						limit = len(t)
					}
					if s.i+1 < limit && t[s.i+1] == c {
						repeated = 1
						skip = s.i + 1
					}
					starts = append(starts, s.i+1+repeated)
				}
			}
			return true
		})
	}
	scan(0, len(t))
	seen := map[int]bool{}
	var out []int
	for idx := 0; idx < len(starts); idx++ {
		start := skipSpace(t, starts[idx])
		if start >= len(t) || seen[start] || isCommentStart(t, start) {
			continue
		}
		seen[start] = true
		out = append(out, start)
		_, end, word := readShellWord(t, start)
		if commandTransitions[word] {
			starts = append(starts, end)
		}
	}
	return out
}

// splice applies sorted, non-overlapping edits in one pass.
type edit struct {
	start, end int
	text       string
}

func splice(t []rune, edits []edit) string {
	var b strings.Builder
	prev := 0
	for _, e := range edits {
		b.WriteString(string(t[prev:e.start]))
		b.WriteString(e.text)
		prev = e.end
	}
	b.WriteString(string(t[prev:]))
	return b.String()
}

// markCommandStarts inserts marker before each real (quote-aware) command start, exposing subshell and
// brace-group openers to the anchored patterns without the quoted-prose false positives that adding them to the
// patterns would cause.
func markCommandStarts(command, marker string) string {
	t := []rune(command)
	var offsets []int
	for _, o := range commandStarts(t) {
		if o > 0 {
			offsets = append(offsets, o)
		}
	}
	if len(offsets) == 0 {
		return command
	}
	sortInts(offsets)
	edits := make([]edit, len(offsets))
	for i, o := range offsets {
		edits[i] = edit{o, o, marker}
	}
	return splice(t, edits)
}

func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j-1] > a[j]; j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}

// maskQuotedNewlines replaces raw newlines inside quotes with a space: a quoted newline is data, yet the
// command-position class treats every raw newline as a command start.
func maskQuotedNewlines(command string) string {
	if !strings.Contains(command, "\n") {
		return command
	}
	t := []rune(command)
	return maskQuotedNewlinesSpan(t, 0, len(t))
}

func maskQuotedNewlinesSpan(t []rune, start, end int) string {
	var out strings.Builder
	scanShell(t, start, end, scanOpts{subst: "q"}, func(s step) bool {
		switch {
		case s.kind == kSubst:
			bodyStart := s.i + 1
			if hasAt(t, "$(", s.i) {
				bodyStart = s.i + 2
			}
			bodyEnd := s.j - 1
			out.WriteString(string(t[s.i:bodyStart]))
			out.WriteString(maskQuotedNewlinesSpan(t, bodyStart, bodyEnd))
			out.WriteString(string(t[bodyEnd:s.j]))
		case s.quote != 0 && s.kind == kChar && t[s.i] == '\n':
			out.WriteByte(' ')
		default:
			out.WriteString(string(t[s.i:s.j]))
		}
		return true
	})
	return out.String()
}

// maskQuotedProse blanks quoted string content for positionless hardline matching. Quote characters stay; inside
// double quotes $(...) and backtick spans stay raw because the shell really executes them.
func maskQuotedProse(command string) string {
	t := []rune(command)
	var out strings.Builder
	scanShell(t, 0, -1, scanOpts{subst: "q", naiveBacktick: true}, func(s step) bool {
		if s.quote == 0 || s.kind == kQuote || s.kind == kSubst {
			out.WriteString(string(t[s.i:s.j]))
		} else {
			out.WriteString(strings.Repeat(" ", s.j-s.i))
		}
		return true
	})
	return out.String()
}

// shellCommandSegment bounds a command candidate to its command, preserving quoted argument bytes.
func shellCommandSegment(t []rune, start int) string {
	end := len(t)
	scanShell(t, start, -1, scanOpts{subst: "uq", brace: true, comments: true}, func(s step) bool {
		if s.kind == kComment || (s.kind == kChar && s.quote == 0 && strings.ContainsRune(";&|\n)`", t[s.i])) {
			end = s.i
			return false
		}
		return true
	})
	return strings.TrimFunc(string(t[start:end]), isSpace)
}

// topLevelSegments yields top-level command segments in one left-to-right pass.
func topLevelSegments(command string) []string {
	t := []rune(command)
	var out []string
	start := 0
	scanShell(t, 0, -1, scanOpts{comments: true}, func(s step) bool {
		if s.kind == kComment || (s.kind == kChar && s.quote == 0 && strings.ContainsRune(";&|\n", t[s.i])) {
			if start < s.i {
				out = append(out, string(t[start:s.i]))
			}
			start = s.j
		}
		return true
	})
	if start < len(t) {
		out = append(out, string(t[start:]))
	}
	return out
}
