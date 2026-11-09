// Package approval decides whether a shell command may run: it classifies commands (a hardline floor that
// nothing bypasses, and dangerous patterns that need the user's say-so) and holds the once/session/always/deny
// decisions. It ports tools/approval_detection.py and the state half of tools/approval.py.
//
// Detection is deliberately a pile of small, quote-aware passes over several projections of the same command,
// because the shell can spell one command many ways (r\m, r”m, rm${IFS}-rf, quoting, wrappers, $(...)). A
// pattern that cannot answer in time counts as a match.
package approval

import (
	_ "embed"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"golang.org/x/text/unicode/norm"
)

//go:embed patterns.json
var patternsRaw []byte

type pattern struct {
	re          rx
	description string
}

var (
	hardlinePatterns      []pattern
	dangerousPatterns     []pattern
	maskedHardline        = map[string]bool{}
	maskedDangerous       = map[string]bool{}
	removedKeyAliasSource map[string]string
	rawDangerous          [][2]string
)

func init() {
	var p struct {
		Hardline            [][2]string       `json:"hardline"`
		Dangerous           [][2]string       `json:"dangerous"`
		QuoteMaskedHardline []string          `json:"quote_masked_hardline"`
		QuoteMaskedDanger   []string          `json:"quote_masked_dangerous"`
		RemovedAliases      map[string]string `json:"removed_aliases"`
	}
	if err := json.Unmarshal(patternsRaw, &p); err != nil {
		panic("approval: patterns.json is malformed: " + err.Error())
	}
	for _, h := range p.Hardline {
		hardlinePatterns = append(hardlinePatterns, pattern{mustCompile(h[0]), h[1]})
	}
	for _, d := range p.Dangerous {
		dangerousPatterns = append(dangerousPatterns, pattern{mustCompile(d[0]), d[1]})
	}
	for _, d := range p.QuoteMaskedHardline {
		maskedHardline[d] = true
	}
	for _, d := range p.QuoteMaskedDanger {
		maskedDangerous[d] = true
	}
	rawDangerous, removedKeyAliasSource = p.Dangerous, p.RemovedAliases
}

// Parser limits bound all work before normalization. Crossing them fails closed.
const (
	maxDetectionChars        = 128_000
	maxSeparatorFreeChars    = 4_096
	maxDetectionSegments     = 25_000
	parserLimitDescription   = "command parser limit exceeded"
	malformedExecDescription = "command parser limit or malformed executable payload"
)

func parserLimitExceeded(command string) bool {
	n := len([]rune(command))
	if n > maxDetectionChars {
		return true
	}
	if n > maxSeparatorFreeChars && !strings.ContainsAny(command, ";&|\n") {
		return true
	}
	count := 0
	for _, c := range ";&|\n" {
		count += strings.Count(command, string(c))
	}
	return count >= maxDetectionSegments
}

// Detector classifies commands. Home and HermesHome are folded to ~/ and ~/.hermes/ so the static patterns see
// /home/alice/.bashrc as ~/.bashrc. The zero value reads them from the environment.
type Detector struct {
	Home       string
	HermesHome string
	// SudoPasswordConfigured is true when SUDO_PASSWORD is set, in which case the sudo stdin guard stands down.
	SudoPasswordConfigured bool
}

// DetectorFromEnv builds a Detector from HOME, HERMES_HOME and SUDO_PASSWORD.
func DetectorFromEnv() Detector {
	home, _ := os.UserHomeDir()
	hh := os.Getenv("HERMES_HOME")
	if hh == "" && home != "" {
		hh = filepath.Join(home, ".hermes")
	}
	_, set := os.LookupEnv("SUDO_PASSWORD")
	return Detector{Home: home, HermesHome: hh, SudoPasswordConfigured: set}
}

var (
	ansiRe = regexp.MustCompile(`(?s)\x{1b}(?:\[[\x{30}-\x{3f}]*[\x{20}-\x{2f}]*[\x{40}-\x{7e}]|\][\s\S]*?(?:\x{07}|\x{1b}\\)|[PX^_][\s\S]*?(?:\x{1b}\\)|[\x{20}-\x{2f}]+[\x{30}-\x{7e}]|[\x{30}-\x{7e}])|\x{9b}[\x{30}-\x{3f}]*[\x{20}-\x{2f}]*[\x{40}-\x{7e}]|\x{9d}[\s\S]*?(?:\x{07}|\x{9c})|[\x{80}-\x{9f}]`)
	contRe = regexp.MustCompile(`\\\r?\n`)
	escRe  = regexp.MustCompile(`\\([^\n])`)
	emptyQ = regexp.MustCompile(`''|""`)
	ifsRe  = regexp.MustCompile(`\$\{IFS\b[^}]*\}|\$IFS\b`)
)

func stripANSI(s string) string {
	if !strings.ContainsAny(s, "\x1b\u0080\u0081\u0082\u0083\u0084\u0085\u0086\u0087\u0088\u0089\u008a\u008b\u008c\u008d\u008e\u008f\u0090\u0091\u0092\u0093\u0094\u0095\u0096\u0097\u0098\u0099\u009a\u009b\u009c\u009d\u009e\u009f") {
		return s
	}
	return ansiRe.ReplaceAllString(s, "")
}

// pathTokenStop ends a path token: shell metacharacters, quotes and whitespace.
const pathTokenStop = "\\s'\"`;|&<>()"

var (
	foldMu    sync.Mutex
	foldCache = map[string]*regexp.Regexp{}
)

func (d Detector) homeFoldRe(p string) *regexp.Regexp {
	foldMu.Lock()
	defer foldMu.Unlock()
	if re, ok := foldCache[p]; ok {
		return re
	}
	re := compileHomeFold(p)
	foldCache[p] = re
	return re
}

func compileHomeFold(p string) *regexp.Regexp {
	var comps []string
	for _, c := range strings.Split(p, "/") {
		if c != "" {
			comps = append(comps, regexp.QuoteMeta(c))
		}
	}
	if p == "" || len(comps) < 2 {
		return nil
	}
	return regexp.MustCompile("/*" + strings.Join(comps, "/+") + `(?P<tail>(?:/[^/` + pathTokenStop + `]*)+)`)
}

// foldHomePrefixes folds each resolved home prefix to replacement, longest first so a deeper home folds before a
// shorter overlapping one that would clobber it.
func (d Detector) foldHomePrefixes(command string, paths []string, replacement string) string {
	uniq := map[string]bool{}
	var list []string
	for _, p := range paths {
		if p != "" && !uniq[p] {
			uniq[p] = true
			list = append(list, p)
		}
	}
	for i := 1; i < len(list); i++ { // stable insertion sort by descending length
		for j := i; j > 0 && len(list[j-1]) < len(list[j]); j-- {
			list[j-1], list[j] = list[j], list[j-1]
		}
	}
	for _, p := range list {
		if re := d.homeFoldRe(p); re != nil {
			command = re.ReplaceAllString(command, replacement+"${tail}")
		}
	}
	return command
}

var (
	realpathMu    sync.Mutex
	realpathCache = map[string]string{}
)

// realpath resolves symlinks, remembering the answer: detection folds the same few home paths into every command
// variant, and resolving them costs a syscall per path component (slow on network and automounted homes).
func realpath(p string) string {
	realpathMu.Lock()
	defer realpathMu.Unlock()
	if r, ok := realpathCache[p]; ok {
		return r
	}
	r := p
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		r = resolved
	}
	realpathCache[p] = r
	return r
}

// normalize strips ANSI escapes, NULs, fullwidth forms, continuations, backslash escapes, empty quotes and $IFS so
// splicing tricks cannot bypass the patterns.
func (d Detector) normalize(command string) string {
	command = norm.NFKC.String(strings.ReplaceAll(stripANSI(command), "\x00", ""))
	command = contRe.ReplaceAllString(command, "")
	if d.HermesHome != "" {
		command = d.foldHomePrefixes(command, []string{d.HermesHome, realpath(d.HermesHome)}, "~/.hermes")
	}
	if d.Home != "" {
		command = d.foldHomePrefixes(command, []string{d.Home, realpath(d.Home), os.Getenv("HOME")}, "~")
	}
	command = escRe.ReplaceAllString(command, "$1")
	command = emptyQ.ReplaceAllString(command, "")
	return ifsRe.ReplaceAllString(command, " ")
}

var spaceRunRe = regexp.MustCompile(`(\s+)`)

// lowerPreservingFlags lowercases a variant while keeping dash-prefixed tokens byte for byte, so case-dependent
// flags keep their distinction.
func lowerPreservingFlags(command string) string {
	var b strings.Builder
	idx := spaceRunRe.FindAllStringIndex(command, -1)
	prev := 0
	emit := func(tok string) {
		if strings.HasPrefix(tok, "-") {
			b.WriteString(tok)
		} else {
			b.WriteString(strings.ToLower(tok))
		}
	}
	for _, m := range idx {
		emit(command[prev:m[0]])
		b.WriteString(command[m[0]:m[1]])
		prev = m[1]
	}
	emit(command[prev:])
	return b.String()
}

var shellNames = []string{"bash", "sh", "zsh", "ksh", "dash"}
var shellCarrierNames = map[string]bool{"eval": true, "sh": true, "bash": true, "zsh": true, "ksh": true, "dash": true, "source": true, ".": true}

func baseLower(p string) string { return strings.ToLower(path.Base(p)) }

// containsShellCarrier reports whether any command-position word is a shell-carrying command.
func containsShellCarrier(command string) bool {
	for _, w := range commandWordSpans([]rune(command)) {
		if shellCarrierNames[baseLower(deobfuscateWord(w.word))] {
			return true
		}
	}
	return false
}

var sudoStdinRe = mustCompile(`(?:^|[;&|` + "`" + `\n]|&&|\|\||\$\()\s*sudo\s+-S\b`)

// DetectHardline checks the unconditional floor: commands that never run, whatever the approval mode. It
// returns (blocked, description).
func (d Detector) DetectHardline(command string) (bool, string) {
	if parserLimitExceeded(command) {
		return true, parserLimitDescription
	}
	if _, malformed := d.grepSafeVariant(maskQuotedNewlines(command)); malformed {
		return true, malformedExecDescription
	}
	for _, variant := range d.detectionVariants(command) {
		lower := strings.ToLower(variant)
		masked := ""
		haveMasked := false
		for _, p := range hardlinePatterns {
			subject := lower
			if maskedHardline[p.description] {
				if !haveMasked {
					if containsShellCarrier(variant) {
						masked = lower
					} else {
						masked = strings.ToLower(maskQuotedProse(variant))
					}
					haveMasked = true
				}
				subject = masked
			}
			if p.re.search(subject) {
				return true, p.description
			}
		}
	}
	return false, ""
}

// DetectSudoStdin catches an explicit "sudo -S": without a configured SUDO_PASSWORD it is the model piping a
// guessed password through stdin.
func (d Detector) DetectSudoStdin(command string) (bool, string) {
	if !d.SudoPasswordConfigured && sudoStdinRe.search(strings.ToLower(d.normalize(command))) {
		return true, "sudo password guessing via stdin (sudo -S)"
	}
	return false, ""
}

// Verdict is the result of dangerous-command detection.
type Verdict struct {
	Dangerous bool
	// Key identifies the pattern for approvals; it is the description.
	Key         string
	Description string
}

// DetectDangerous checks the dangerous patterns, which need the user's approval rather than being forbidden.
func (d Detector) DetectDangerous(command string) Verdict {
	if parserLimitExceeded(command) {
		return Verdict{true, parserLimitDescription, parserLimitDescription}
	}
	if isVerificationArtifactCleanup(command) {
		return Verdict{}
	}
	for _, variant := range d.detectionVariants(command) {
		lower := lowerPreservingFlags(variant)
		masked := ""
		haveMasked := false
		for _, p := range dangerousPatterns {
			subject := lower
			if maskedDangerous[p.description] {
				if !haveMasked {
					masked = lowerPreservingFlags(maskQuotedProse(variant))
					haveMasked = true
				}
				subject = masked
			}
			if p.re.search(subject) {
				return Verdict{true, p.description, p.description}
			}
		}
	}
	for _, f := range executionFlagFindings(d.normalize(command)) {
		return Verdict{true, f.description, f.description}
	}
	return Verdict{}
}
