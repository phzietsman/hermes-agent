package approval

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ---- The command's alternative projections -------------------------------------------------

// detectionVariants yields the projections of a command the patterns are run against: the normalized,
// grep-safe text; payloads of program-bearing options (bash -c, env -S, ...); the same text with command starts
// marked; a faithful variant normalized after marking starts on the raw text (so an escaped quote cannot flip
// quote parity); and variants with each command word deobfuscated.
func (d Detector) detectionVariants(command string) []string {
	normalized := d.normalize(maskQuotedNewlines(command))
	grepSafe, _ := d.grepSafeVariant(normalized)
	seen := map[string]bool{grepSafe: true}
	out := []string{grepSafe}
	fresh := func(v string) bool {
		if v == "" || seen[v] {
			return false
		}
		seen[v] = true
		return true
	}
	pending := []string{normalized}
	for len(pending) > 0 {
		cur := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		for _, f := range executionFlagFindings(cur) {
			if f.payload == "" || !fresh(f.payload) {
				continue
			}
			out = append(out, f.payload)
			if marked := markCommandStarts(f.payload, "\n"); marked != f.payload && fresh(marked) {
				out = append(out, marked)
			}
			pending = append(pending, f.payload)
		}
	}
	if marked := markCommandStarts(grepSafe, "\n"); marked != grepSafe && fresh(marked) {
		out = append(out, marked)
	}
	if faithful := d.normalize(markCommandStarts(maskQuotedNewlines(command), " \n")); fresh(faithful) {
		out = append(out, faithful)
	}
	nt := []rune(normalized)
	var spans []edit
	for _, w := range commandWordSpans(nt) {
		if deob := deobfuscateWord(w.word); deob != "" && deob != w.word {
			spans = append(spans, edit{w.start, w.end, deob})
		}
	}
	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i].start != spans[j].start {
			return spans[i].start < spans[j].start
		}
		return spans[i].end < spans[j].end
	})
	for len(spans) > 0 {
		var applied, carry []edit
		cursor := 0
		for _, sp := range spans {
			if sp.start < cursor {
				carry = append(carry, sp)
			} else {
				applied = append(applied, sp)
				cursor = sp.end
			}
		}
		if v := splice(nt, applied); fresh(v) {
			out = append(out, v)
		}
		spans = carry
	}
	return out
}

// DenyVariants adds executable projections for deny rules: the normalized command words with their tails, so a
// rule on "git push" sees it however the executable was spelled.
func (d Detector) DenyVariants(command string) []string {
	out := d.detectionVariants(command)
	pending, seen := []string{command}, map[string]bool{}
	for len(pending) > 0 {
		source := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if seen[source] {
			continue
		}
		seen[source] = true
		st := []rune(source)
		for _, w := range commandWordSpans(st) {
			segment := shellCommandSegment(st, w.start)
			executable := deobfuscateWord(w.word)
			tail := string([]rune(segment)[min(w.end-w.start, len([]rune(segment))):])
			tail = collapseUnquotedSpace(tail)
			names := []string{executable}
			if b := path.Base(executable); b != executable {
				names = append(names, b)
			}
			for _, name := range names {
				candidate := name + tail
				out = append(out, candidate, d.normalize(candidate))
			}
			if path.Base(executable) == "env" {
				if tokens, err := shlexSplit(segment, true); err == nil && len(tokens) > 0 {
					if payload, ok := envSplitPayload(tokens); ok && payload != "" {
						pending = append(pending, payload)
					}
				}
			}
		}
		for _, f := range executionFlagFindings(source) {
			if f.payload != "" {
				pending = append(pending, f.payload)
			}
		}
	}
	return out
}

func collapseUnquotedSpace(tail string) string {
	t := []rune(tail)
	var parts []string
	scanShell(t, 0, -1, scanOpts{}, func(s step) bool {
		if s.kind == kChar && s.quote == 0 && isSpace(t[s.i]) {
			if len(parts) == 0 || parts[len(parts)-1] != " " {
				parts = append(parts, " ")
			}
		} else {
			parts = append(parts, string(t[s.i:s.j]))
		}
		return true
	})
	return strings.Join(parts, "")
}

// ---- grep patterns are data, not commands --------------------------------------------------

type token struct {
	value      string
	start, end int
	inert      bool
}

func backtickEndFrom(seg []rune, i int) int {
	j := indexRune(seg, '`', i+1, len(seg))
	for j != -1 && seg[j-1] == '\\' {
		j = indexRune(seg, '`', j+1, len(seg))
	}
	return j
}

// shellTokensWithSpans returns shell words with their source spans, or ok=false on malformed quoting. It lexes
// to the end of the simple command starting at start.
func shellTokensWithSpans(seg []rune, start int) (tokens []token, ok bool) {
	var value []rune
	tokenStart := -1
	var quote rune
	depth := 0
	inBacktick := false
	malformed := false
	flush := func(end int) {
		raw := string(seg[tokenStart:end])
		inert := (strings.HasPrefix(raw, "'") && strings.HasSuffix(raw, "'")) || (strings.Contains(raw, "='") && strings.HasSuffix(raw, "'"))
		tokens = append(tokens, token{string(value), tokenStart, end, inert})
	}
	endAt := len(seg)
	scanShell(seg, start, -1, scanOpts{}, func(s step) bool {
		ch := seg[s.i]
		if s.kind == kChar && quote == 0 {
			if isSpace(ch) && ch != '\n' {
				if tokenStart >= 0 {
					flush(s.i)
					value, tokenStart = nil, -1
				}
				return true
			}
			switch {
			case hasAt(seg, "$(", s.i):
				depth++
			case ch == '`':
				if inBacktick {
					inBacktick = false
				} else if depth == 0 && backtickEndFrom(seg, s.i) < 0 {
					endAt = s.i
					return false
				} else {
					inBacktick = true
				}
			case ch == ')':
				if depth == 0 {
					endAt = s.i
					return false
				}
				depth--
			case strings.ContainsRune(";|&\n", ch):
				endAt = s.i
				return false
			}
		}
		if tokenStart < 0 {
			tokenStart = s.i
		}
		switch {
		case s.kind == kQuote:
			if quote != 0 {
				quote = 0
			} else {
				quote = ch
			}
		case s.kind == kEsc:
			value = append(value, seg[s.i+1])
		case ch == '\\' && quote == 0:
			malformed = true
			return false
		default:
			value = append(value, ch)
		}
		return true
	})
	if malformed || quote != 0 {
		return nil, false
	}
	if tokenStart >= 0 {
		flush(endAt)
	}
	return tokens, true
}

var grepOptionsWithArg = set("--after-context", "--before-context", "--binary-files", "--context", "--directories", "--devices", "--exclude",
	"--exclude-dir", "--exclude-from", "--include", "--label", "--max-count", "--regexp", "--file")
var grepShortWithArg = set("A", "B", "C", "D", "d", "e", "f", "m")

type spanRange struct{ start, end int }

// quotedGrepPatternSpans locates quoted grep -P operands: they are data, so detection blanks them. On an
// ambiguous or malformed grep parse it reports malformed and the caller keeps the original command.
func quotedGrepPatternSpans(command string) ([]spanRange, bool) {
	var spans []spanRange
	cr := []rune(command)
	offset := 0
	for _, segment := range topLevelSegments(command) {
		sr := []rune(segment)
		segmentAt := indexSub(cr, sr, offset)
		offset = segmentAt + len(sr)
		for _, w := range commandWordSpans(sr) {
			if n := baseLower(deobfuscateWord(w.word)); n != "grep" && n != "egrep" {
				continue
			}
			tokens, ok := shellTokensWithSpans(sr, w.start)
			if !ok {
				return nil, true
			}
			args := tokens[1:]
			var patternIdx []int
			pcre, explicit := false, false
			operand := -1
			i, options := 0, true
			for i < len(args) {
				tk := args[i].value
				switch {
				case options && tk == "--":
					options = false
				case options && strings.HasPrefix(tk, "--"):
					opt, _, hasEq := strings.Cut(tk, "=")
					pcre = pcre || opt == "--perl-regexp"
					explicit = explicit || opt == "--regexp" || opt == "--file"
					takesNext := grepOptionsWithArg[opt] && !hasEq
					if takesNext && i+1 >= len(args) {
						return nil, true
					}
					if opt == "--regexp" {
						if takesNext {
							patternIdx = append(patternIdx, i+1)
						} else {
							patternIdx = append(patternIdx, i)
						}
					}
					if takesNext {
						i++
					}
				case options && strings.HasPrefix(tk, "-") && tk != "-":
					chars := []rune(tk[1:])
					for j, ch := range chars {
						pcre = pcre || ch == 'P'
						explicit = explicit || ch == 'e' || ch == 'f'
						if grepShortWithArg[string(ch)] {
							attached := j+1 < len(chars)
							if !attached && i+1 >= len(args) {
								return nil, true
							}
							if ch == 'e' {
								if attached {
									patternIdx = append(patternIdx, i)
								} else {
									patternIdx = append(patternIdx, i+1)
								}
							}
							if !attached {
								i++
							}
							break
						}
					}
				case operand < 0:
					operand = i
				}
				i++
			}
			if !explicit {
				if operand < 0 {
					return nil, pcre
				}
				patternIdx = append(patternIdx, operand)
			}
			if pcre {
				for _, pi := range patternIdx {
					if pi < len(args) && args[pi].inert {
						spans = append(spans, spanRange{segmentAt + args[pi].start, segmentAt + args[pi].end})
					}
				}
			}
		}
	}
	return spans, false
}

func indexSub(hay, needle []rune, from int) int {
	if from < 0 {
		from = 0
	}
	for i := from; i+len(needle) <= len(hay); i++ {
		match := true
		for k := range needle {
			if hay[i+k] != needle[k] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func (d Detector) grepSafeVariant(command string) (string, bool) {
	spans, malformed := quotedGrepPatternSpans(command)
	if malformed || len(spans) == 0 {
		return command, malformed
	}
	cr := []rune(command)
	edits := make([]edit, len(spans))
	for i, s := range spans {
		edits[i] = edit{s.start, s.end, strings.Repeat(" ", s.end-s.start)}
	}
	return splice(cr, edits), false
}

// ---- execution flags: interpreters, shells and read-only tools that run programs ------------

type finding struct {
	description string
	payload     string
}

var interpreterNameRes = []struct {
	family string
	re     *regexp.Regexp
}{
	{"python", regexp.MustCompile(`^(?:python[23]?(?:\.\d+)*)$`)}, {"node", regexp.MustCompile(`^(?:node(?:js)?)$`)},
	{"perl", regexp.MustCompile(`^(?:perl[0-9]*(?:\.\d+)*)$`)}, {"ruby", regexp.MustCompile(`^(?:ruby[0-9.]*)$`)}, {"php", regexp.MustCompile(`^(?:php)$`)},
	{"powershell", regexp.MustCompile(`^(?:powershell|pwsh)$`)}, {"bun", regexp.MustCompile(`^(?:bun)$`)}, {"deno", regexp.MustCompile(`^(?:deno)$`)},
}

var interpreterExecFlags = map[string]map[string]bool{
	"python": set("-c"), "node": set("-e", "--eval", "-p", "--print"), "perl": set("-e", "--eval"), "ruby": set("-e"),
	"php": set("-r"), "powershell": set("-command", "-c", "-file", "-f"), "bun": set("-e", "--eval"), "deno": set("eval", "-e", "--eval"),
}

var interpreterWithArg = map[string]map[string]bool{
	"python": set("-W", "-X", "--check-hash-based-pycs"),
	"node": set("-C", "--conditions", "--cpu-prof-dir", "--diagnostic-dir", "--icu-data-dir", "--import", "--loader",
		"--openssl-config", "--require", "--title"),
	"perl": set("-0", "-F", "-I", "-M", "-m", "-x"), "ruby": set("-C", "-E", "-F", "-I", "-K", "-r"), "php": set("-c", "-d", "-z"),
	"powershell": set("-configurationname", "-custompipename", "-executionpolicy", "-inputformat", "-outputformat",
		"-settingsfile", "-version", "-windowstyle", "-workingdirectory"),
	"bun": set("--config", "--cwd", "--env-file", "--preload", "--require"), "deno": set("-L", "--log-level"),
}

var readToolExecFlags = map[string]map[string]bool{
	"sort": set("--compress-program"), "rg": set("--pre", "--hostname-bin"), "ag": set("--pager"), "man": set("--pager", "--html", "-P", "-H"),
}

var readToolLongWithArg = map[string]map[string]bool{
	"rg": set("--after-context", "--before-context", "--color", "--colors", "--context", "--context-separator",
		"--dfa-size-limit", "--encoding", "--engine", "--field-context-separator", "--field-match-separator",
		"--file", "--generate", "--glob", "--hostname-bin", "--hyperlink-format", "--iglob", "--ignore-file",
		"--max-columns", "--max-count", "--max-depth", "--max-filesize", "--path-separator", "--pre", "--pre-glob",
		"--regex-size-limit", "--regexp", "--replace", "--sort", "--sortr", "--threads", "--type", "--type-add",
		"--type-clear", "--type-not"),
	"sort": set("--batch-size", "--buffer-size", "--compress-program", "--field-separator", "--files0-from", "--key",
		"--output", "--parallel", "--random-source", "--sort", "--temporary-directory"),
	"man": set("--config-file", "--encoding", "--extension", "--locale", "--manpath", "--pager", "--preprocessor",
		"--prompt", "--recode", "--sections", "--systems"),
	"ag": set("--ackmate-dir-filter", "--color-line-number", "--color-match", "--color-path", "--depth",
		"--filename-pattern", "--file-search-regex", "--ignore", "--ignore-dir", "--max-count", "--pager",
		"--path-to-ignore", "--width", "--workers"),
}

var readToolShortWithArg = map[string]string{"rg": "efEmjgdtTABCMr", "sort": "koStT", "man": "CRLmMSserEPp", "ag": "gGmpW"}

var bashOptionsWithArg = set("-O", "+O", "-o", "+o", "--init-file", "--rcfile")

const bashShortOptionLetters = "ilrsDcabefhkmnptuvxBCEHPTOo"

func interpreterFamily(executable string) string {
	name := baseLower(executable)
	for _, f := range interpreterNameRes {
		if f.re.MatchString(name) {
			return f.family
		}
	}
	return ""
}

func shellSegmentTokens(segment string, start int) ([]string, bool) {
	toks, err := shlexSplit(string([]rune(segment)[start:]), true)
	return toks, err == nil
}

func interpreterExecFlag(family string, args []string) string {
	flags, withArg := interpreterExecFlags[family], interpreterWithArg[family]
	powershell := family == "powershell"
	skipValue := false
	for _, tk := range args {
		if skipValue {
			skipValue = false
			continue
		}
		if tk == "--" || (!powershell && !strings.HasPrefix(tk, "-")) {
			if family == "deno" && strings.ToLower(tk) == "eval" {
				return "eval"
			}
			break
		}
		opt, _, hasEq := strings.Cut(tk, "=")
		comparable := opt
		if powershell {
			comparable = strings.ToLower(opt)
		}
		if flags[comparable] {
			return comparable
		}
		attached := false
		for short := range withArg {
			if strings.HasPrefix(short, "-") && !strings.HasPrefix(short, "--") && strings.HasPrefix(opt, short) && len(opt) > len(short) {
				attached = true
			}
		}
		if !powershell && !strings.HasPrefix(opt, "--") && len([]rune(opt)) > 2 && !attached {
			for _, ch := range opt[1:] {
				if flags["-"+string(ch)] {
					return "-" + string(ch)
				}
			}
		}
		skipValue = withArg[comparable] && !hasEq
	}
	return ""
}

// bashExecPayload reports whether Bash -c occurs and the command string it owns.
func bashExecPayload(args []string) (bool, string) {
	index := 0
	for index < len(args) {
		tk := args[index]
		if tk == "--" || !(strings.HasPrefix(tk, "-") || strings.HasPrefix(tk, "+")) {
			break
		}
		if bashOptionsWithArg[tk] {
			index += 2
			continue
		}
		chars := tk[1:]
		letters := true
		for _, c := range chars {
			if !strings.ContainsRune(bashShortOptionLetters, c) {
				letters = false
			}
		}
		if strings.HasPrefix(tk, "--") || !letters {
			index++
			continue
		}
		consumed := 0
		if strings.ContainsAny(chars, "Oo") {
			consumed = 1
		}
		if strings.Contains(chars, "c") {
			pi := index + 1 + consumed
			if pi < len(args) {
				return true, args[pi]
			}
			return true, ""
		}
		index += 1 + consumed
	}
	return false, ""
}

func readToolExecFlag(tool string, args []string) (string, string, bool) {
	flags := readToolExecFlags[tool]
	index := 0
	for index < len(args) {
		tk := args[index]
		if tk == "--" {
			break
		}
		opt, payload, hasEq := strings.Cut(tk, "=")
		havePayload := hasEq
		matched := ""
		if flags[opt] {
			matched = opt
		}
		if tool == "man" && (strings.HasPrefix(tk, "-P") || strings.HasPrefix(tk, "-H")) && len(tk) > 2 {
			matched, payload, havePayload = tk[:2], tk[2:], true
		}
		switch {
		case matched != "":
			if !havePayload && index+1 < len(args) {
				payload, havePayload = args[index+1], true
			}
			if payload != "" {
				return matched, payload, true
			}
			if havePayload && !strings.Contains(tk, "=") {
				index += 2
			} else {
				index++
			}
		case readToolLongWithArg[tool][opt] && !hasEq:
			index += 2
		case strings.HasPrefix(tk, "-") && !strings.HasPrefix(tk, "--") && len(tk) > 1:
			shorts := readToolShortWithArg[tool]
			owner := -1
			for k, ch := range []rune(tk)[1:] {
				if strings.ContainsRune(shorts, ch) {
					owner = k + 1
					break
				}
			}
			if owner == len([]rune(tk))-1 {
				index += 2
			} else {
				index++
			}
		default:
			index++
		}
	}
	return "", "", false
}

// executionFlagFindings reports scoped execution mechanisms (interpreter -e/-c, heredocs, shell -c, programs
// run by read-only tools) with the payloads they would run.
func executionFlagFindings(command string) []finding {
	var out []finding
	for _, segment := range topLevelSegments(command) {
		sr := []rune(segment)
		for _, w := range commandWordSpans(sr) {
			executable := deobfuscateWord(w.word)
			tokens, ok := shellSegmentTokens(segment, w.start)
			name := baseLower(executable)
			family := interpreterFamily(executable)
			if !ok {
				if family != "" || readToolExecFlags[name] != nil {
					out = append(out, finding{malformedExecDescription, ""})
				}
				continue
			}
			if len(tokens) == 0 {
				continue
			}
			args := tokens[1:]
			heredoc := false
			for _, tk := range args {
				if strings.HasPrefix(tk, "<<") {
					heredoc = true
				}
			}
			switch {
			case family != "" && interpreterExecFlag(family, args) != "":
				out = append(out, finding{"script execution via -e/-c flag", ""})
			case family != "" && heredoc:
				out = append(out, finding{"script execution via heredoc", ""})
			default:
				for _, s := range shellNames {
					if name == s {
						if found, payload := bashExecPayload(args); found {
							out = append(out, finding{"shell command via -c/-lc flag", payload})
						}
					}
				}
				if readToolExecFlags[name] != nil {
					if opt, prog, ok := readToolExecFlag(name, args); ok {
						out = append(out, finding{"arbitrary program execution via " + name + " " + opt, prog})
					}
				}
			}
		}
	}
	return out
}

var hermesTempScriptRe = regexp.MustCompile(`^hermes-(?:verify|ad-hoc)-[A-Za-z0-9_.-]+$`)

// isVerificationArtifactCleanup reports whether command only removes one Hermes ad-hoc temp script.
func isVerificationArtifactCleanup(command string) bool {
	argv, err := shlexSplit(command, false)
	if err != nil || len(argv) != 3 || argv[0] != "rm" || argv[1] != "-f" {
		return false
	}
	operand := argv[2]
	tmp := realpath(os.TempDir())
	base := path.Base(operand)
	return operand == filepath.Join(tmp, base) && realpath(path.Dir(operand)) == tmp && hermesTempScriptRe.MatchString(base)
}
