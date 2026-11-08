package agent

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/phzietsman/hermes-agent/internal/threats"
)

// Project context files and the SOUL.md identity: discovery, injection scanning, truncation. It ports the
// context-file half of agent/prompt_builder.py.
//
// Not ported: the install-tree fallback guard (a Go binary has no source tree to mistake for a workspace),
// the Bot Mode legacy-protocol strip, profile distributions (so a SOUL.md is always the user's own), and
// the ensure_hermes_home call that seeded a default SOUL.md: the seeded text is byte-identical to the
// default identity, so an absent SOUL.md yields the same prompt without writing into the user's home.

const (
	// ContextFileMaxChars is the flat truncation limit when the model's window is unknown.
	ContextFileMaxChars = 20_000
	truncateHeadRatio   = 0.7
	truncateTailRatio   = 0.2
	// Dynamic cap (no explicit context_file_max_chars): a small slice of the window, since context files share
	// the cached prefix; small models stay at the floor.
	contextWindowFraction = 0.06
	contextDynamicCeiling = 500_000
	// charsPerToken is the rough characters-per-token ratio used for sizing.
	charsPerToken = 4
	// DefaultContextReadTimeout is intentionally short: network-backed filesystems (iCloud Drive, OneDrive,
	// NFS) can fault in an evicted file and block a cold read indefinitely, which would stall prompt assembly.
	DefaultContextReadTimeout = 5 * time.Second
)

// ContextOptions configures context-file loading.
type ContextOptions struct {
	// CWD is the project directory; empty uses the process working directory.
	CWD string
	// SkipSoul leaves SOUL.md out (it is already the identity slot).
	SkipSoul bool
	// ContextLength is the model's window in tokens; it scales the per-file cap. 0 means unknown.
	ContextLength int
	// MaxChars is an explicit per-file cap (config context_file_max_chars); 0 uses the dynamic cap.
	MaxChars int
	// Home is the profile home holding SOUL.md.
	Home string
	// ReadTimeout bounds each file read; 0 uses DefaultContextReadTimeout.
	ReadTimeout time.Duration
}

// ContextResult is the assembled project-context block and the truncation warnings to surface in chat.
type ContextResult struct {
	Prompt   string
	Warnings []string
}

type loader struct {
	o        ContextOptions
	warnings []string
}

func (l *loader) maxChars() int {
	if l.o.MaxChars > 0 {
		return l.o.MaxChars
	}
	if l.o.ContextLength <= 0 {
		return ContextFileMaxChars
	}
	budget := int(float64(l.o.ContextLength*charsPerToken) * contextWindowFraction)
	return max(ContextFileMaxChars, min(budget, contextDynamicCeiling))
}

// ---- Python-compatible string helpers ------------------------------------------------------------

func isPySpace(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }

// pyStrip is Python's str.strip(): it also trims the ASCII separators U+001C to U+001F.
func pyStrip(s string) string { return strings.TrimFunc(s, isPySpace) }

// splitLinesKeepEnds is Python's str.splitlines(keepends=True): it splits on \n, \r\n, \r, \v, \f, the
// separators U+001C to U+001E, U+0085, U+2028 and U+2029.
func splitLinesKeepEnds(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch r {
		case '\r':
			end := i + size
			if end < len(s) && s[end] == '\n' {
				end++
			}
			lines = append(lines, s[start:end])
			start, i = end, end
		case '\n', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
			end := i + size
			lines = append(lines, s[start:end])
			start, i = end, end
		default:
			i += size
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

var headingRe = regexp.MustCompile(`^#{1,6} \S`)

// omittedHeadings lists the markdown headings whose line starts inside runes [start, end), so a truncation
// marker can tell the agent what it lost. A # line inside a fenced code block is a comment, not a heading.
func omittedHeadings(content string, start, end, limit int) []string {
	var headings []string
	offset := 0
	fenced := false
	for _, line := range splitLinesKeepEnds(content) {
		stripped := pyStrip(line)
		if strings.HasPrefix(stripped, "```") || strings.HasPrefix(stripped, "~~~") {
			fenced = !fenced
		} else if !fenced && start <= offset && offset < end && headingRe.MatchString(stripped) {
			headings = append(headings, pyStrip(strings.TrimLeft(stripped, "#")))
		}
		offset += utf8.RuneCountInString(line)
	}
	if len(headings) > limit {
		return append(headings[:limit:limit], "...")
	}
	return headings
}

// truncateContent keeps the head and tail of an over-long file with a marker in the middle. readPath (default
// filename) is what the agent is told to read_file to recover the full content. queueWarning false is for
// bounded previews whose fixed cap no config key or model raises: the marker is then the only disclosure.
func (l *loader) truncateContent(content, filename string, readPath string, queueWarning bool) string {
	maxChars := l.maxChars()
	runes := []rune(content)
	if len(runes) <= maxChars {
		return content
	}
	if readPath == "" {
		readPath = filename
	}
	remedy := "trim the file, pin a larger context_file_max_chars, or use a larger-context model!"
	if !queueWarning {
		remedy = "the full file stays readable with read_file: " + readPath
	}
	msg := fmt.Sprintf("⚠️  Context file %s TRUNCATED: %d chars exceeds limit of %d — %s", filename, len(runes), maxChars, remedy)
	slog.Warn(msg)
	if queueWarning {
		l.warnings = append(l.warnings, msg)
	}
	head := int(float64(maxChars) * truncateHeadRatio)
	tail := int(float64(maxChars) * truncateTailRatio)
	omitted := omittedHeadings(content, head, len(runes)-tail, 15)
	sections := ""
	if len(omitted) > 0 {
		sections = " Omitted sections: " + strings.Join(omitted, "; ") + "."
	}
	marker := fmt.Sprintf("\n\n[...truncated %s: kept %d+%d of %d chars. The middle is omitted.%s If you need the full instructions, read the complete file with the read_file tool: %s]\n\n",
		filename, head, tail, len(runes), sections, readPath)
	return string(runes[:head]) + marker + string(runes[len(runes)-tail:])
}

// scanContext scans a context file for injection. Matches are BLOCKED: only the "context" scope runs (the
// strict-scope SSH-backdoor and persistence patterns are too aggressive for a cloned repo's docs), and
// blocking rather than warning is because the file would otherwise enter the prompt verbatim. userAuthored
// (the user's own SOUL.md) only warns: it sits in the same trust class as config.yaml, and a user who
// documents "ignore previous instructions" in their security guidance must not lose their identity file.
func scanContext(content, filename string, userAuthored bool) string {
	content = strings.TrimPrefix(content, "\ufeff") // a BOM is an editor artifact, not an injection
	findings, err := threats.Scan(content, threats.ScopeContext)
	if err != nil || len(findings) == 0 {
		return content
	}
	if userAuthored {
		slog.Warn("context file matched injection pattern(s); loaded anyway because it is the user's own file",
			"file", filename, "patterns", strings.Join(findings, ", "))
		return content
	}
	slog.Warn("context file blocked", "file", filename, "patterns", strings.Join(findings, ", "))
	return fmt.Sprintf("[BLOCKED: %s contained potential prompt injection (%s). Content not loaded.]", filename, strings.Join(findings, ", "))
}

// ---- reading ------------------------------------------------------------------------------------

// readText reads a file on its own goroutine so a slow file cannot stall prompt assembly. It returns ok=false
// on timeout (the orphaned read finishes on its own) or when the bytes are not valid UTF-8.
func (l *loader) readText(path string) (string, bool) {
	d := l.o.ReadTimeout
	if d <= 0 {
		d = DefaultContextReadTimeout
	}
	type result struct {
		b   []byte
		err error
	}
	ch := make(chan result, 1)
	go func() { b, err := os.ReadFile(path); ch <- result{b, err} }()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	select {
	case r := <-ch:
		if r.err != nil {
			return "", false
		}
		b := bytes.TrimPrefix(r.b, []byte{0xEF, 0xBB, 0xBF})
		if !utf8.Valid(b) {
			return "", false
		}
		return string(b), true
	case <-ctx.Done():
		slog.Warn("context file read timed out; skipping", "path", path, "timeout", d)
		return "", false
	}
}

// readContextFile is the stripped text of path; "" when missing, empty or unreadable.
func (l *loader) readContextFile(path string) string {
	if !existsOrDenied(path) {
		return ""
	}
	text, _ := l.readText(path)
	return pyStrip(text)
}

func existsOrDenied(p string) bool { _, err := os.Stat(p); return err == nil }

func isFileOrDenied(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

func isDirOrDenied(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func resolvePath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = filepath.Clean(p)
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		return r
	}
	return abs
}

// parents is p followed by each ancestor up to the root.
func parents(p string) []string {
	out := []string{p}
	for {
		next := filepath.Dir(p)
		if next == p {
			return out
		}
		out = append(out, next)
		p = next
	}
}

// findGitRoot is the nearest ancestor (or start itself) containing .git, else "". A parent the process may
// not stat counts as "no .git here", not a failure.
func findGitRoot(start string) string {
	for _, p := range parents(resolvePath(start)) {
		if existsOrDenied(filepath.Join(p, ".git")) {
			return p
		}
	}
	return ""
}

func isRelativeTo(path, base string) bool {
	rel, err := filepath.Rel(base, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// findHermesMD is the nearest .hermes.md or HERMES.md from cwd up to the git root. With no git root only cwd
// is checked: walking parents could pick up a file planted in /tmp, /home and the like.
func findHermesMD(cwd string) string {
	stop := findGitRoot(cwd)
	current := resolvePath(cwd)
	dirs := []string{current}
	if stop != "" {
		dirs = parents(current)
	}
	for _, d := range dirs {
		for _, n := range []string{".hermes.md", "HERMES.md"} {
			if isFileOrDenied(filepath.Join(d, n)) {
				return filepath.Join(d, n)
			}
		}
		if d == stop {
			return ""
		}
	}
	return ""
}

func stripYAMLFrontmatter(content string) string {
	content = strings.TrimLeft(content, "\ufeff")
	if !strings.HasPrefix(content, "---") {
		return content
	}
	end := strings.Index(content[3:], "\n---")
	if end == -1 {
		return content
	}
	end += 3
	if rest := strings.TrimLeft(content[end+4:], "\n"); rest != "" {
		return rest
	}
	return content
}

// ---- candidates ---------------------------------------------------------------------------------

type candidate struct {
	label, path, content string
}

func (l *loader) hermesMDCandidates(cwd string) []candidate {
	p := findHermesMD(cwd)
	if p == "" {
		return nil
	}
	label := filepath.Base(p)
	if isRelativeTo(p, cwd) {
		label, _ = filepath.Rel(cwd, p)
	}
	return []candidate{{label, p, l.readContextFile(p)}}
}

// agentsDirectoryChain is the directories to check for AGENTS.md: the git root first, cwd last (deeper takes
// precedence); cwd only without a root.
func agentsDirectoryChain(cwd string) []string {
	current := resolvePath(cwd)
	root := findGitRoot(current)
	if root == "" || root == current || !isRelativeTo(current, root) {
		return []string{current}
	}
	rel, _ := filepath.Rel(root, current)
	chain := []string{root}
	acc := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		acc = filepath.Join(acc, part)
		chain = append(chain, acc)
	}
	return chain
}

// agentsMDCandidates is the AGENTS.md chain from git root down to cwd. Per directory the first NON-EMPTY of
// AGENTS.override.md, AGENTS.md and agents.md wins; empty or unreadable files are listed but fall through.
func (l *loader) agentsMDCandidates(cwd string) []candidate {
	resolved := resolvePath(cwd)
	var found []candidate
	for _, dir := range agentsDirectoryChain(resolved) {
		for _, name := range []string{"AGENTS.override.md", "AGENTS.md", "agents.md"} {
			p := filepath.Join(dir, name)
			if !existsOrDenied(p) {
				continue
			}
			content := l.readContextFile(p)
			label := name
			if dir != resolved {
				label, _ = filepath.Rel(resolved, p)
			}
			found = append(found, candidate{label, p, content})
			if content != "" {
				break // the first name that matched wins per directory
			}
		}
	}
	return found
}

func (l *loader) claudeMDCandidates(cwd string) []candidate {
	var found []candidate
	for _, name := range []string{"CLAUDE.md", "claude.md"} {
		p := filepath.Join(cwd, name)
		if !existsOrDenied(p) {
			continue
		}
		content := l.readContextFile(p)
		found = append(found, candidate{name, p, content})
		if content != "" {
			break
		}
	}
	return found
}

func (l *loader) cursorRulesCandidates(cwd string) []candidate {
	type lp struct{ label, path string }
	cands := []lp{{".cursorrules", filepath.Join(cwd, ".cursorrules")}}
	dir := filepath.Join(cwd, ".cursor", "rules")
	if isDirOrDenied(dir) {
		matches, _ := filepath.Glob(filepath.Join(dir, "*.mdc"))
		sort.Strings(matches)
		for _, m := range matches {
			cands = append(cands, lp{".cursor/rules/" + filepath.Base(m), m})
		}
	}
	var out []candidate
	for _, c := range cands {
		if existsOrDenied(c.path) {
			out = append(out, candidate{c.label, c.path, l.readContextFile(c.path)})
		}
	}
	return out
}

// contextSection scans content, renders it as "## label", and caps it to the budget.
func (l *loader) contextSection(content, label, warnName, path string) string {
	body := "## " + label + "\n\n" + scanContext(content, label, false)
	return l.truncateContent(body, warnName, path, true)
}

func (l *loader) loadHermesMD(cwd string) string {
	for _, c := range l.hermesMDCandidates(cwd) {
		if c.content != "" {
			return l.contextSection(stripYAMLFrontmatter(c.content), c.label, ".hermes.md", c.path)
		}
	}
	return ""
}

// loadAgentsMD merges the directory chain. Each directory contributes its AGENTS.override.md, AGENTS.md or
// agents.md as its own provenance-labelled section; AGENTS.override.md wins so a developer can keep a
// personal, typically gitignored override beside the committed project instructions. Identical content met
// again further down the chain (copied or symlinked files) is deduplicated. With one match (the common case,
// and always the case outside a git repo) the output is the single-file form.
func (l *loader) loadAgentsMD(cwd string) string {
	var sections []string
	seen := map[string]bool{}
	for _, c := range l.agentsMDCandidates(cwd) {
		if c.content != "" && !seen[c.content] {
			seen[c.content] = true
			sections = append(sections, l.contextSection(c.content, c.label, c.label, c.path))
		}
	}
	switch len(sections) {
	case 0:
		return ""
	case 1:
		return sections[0]
	}
	// Per-file budgets applied above; the merged chain is capped too, so a deep monorepo cannot multiply the budget.
	return l.truncateContent(strings.Join(sections, "\n\n"), "AGENTS.md (directory chain)", filepath.Join(resolvePath(cwd), "AGENTS.md"), true)
}

func (l *loader) loadClaudeMD(cwd string) string {
	for _, c := range l.claudeMDCandidates(cwd) {
		if c.content != "" {
			return l.contextSection(c.content, c.label, "CLAUDE.md", c.path)
		}
	}
	return ""
}

func (l *loader) loadCursorRules(cwd string) string {
	var b strings.Builder
	for _, c := range l.cursorRulesCandidates(cwd) {
		if c.content != "" {
			fmt.Fprintf(&b, "## %s\n\n%s\n\n", c.label, scanContext(c.content, c.label, false))
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return l.truncateContent(b.String(), ".cursorrules", filepath.Join(cwd, ".cursorrules"), true)
}

func (l *loader) loadSoul() string {
	path := filepath.Join(l.o.Home, "SOUL.md")
	if !existsOrDenied(path) {
		return ""
	}
	text, ok := l.readText(path)
	content := pyStrip(text)
	if !ok || content == "" {
		return ""
	}
	return l.truncateContent(scanContext(content, "SOUL.md", true), "SOUL.md", path, true)
}

// LoadSoulMD is SOUL.md from the profile home (identity slot one), or "" when absent, empty or unreadable.
// It is the user's own file, so an injection-pattern hit only warns. Callers pass SkipSoul to
// BuildContextFiles so it is not injected twice.
func LoadSoulMD(o ContextOptions) ContextResult {
	l := &loader{o: o}
	s := l.loadSoul()
	return ContextResult{Prompt: s, Warnings: l.warnings}
}

// BuildContextFiles discovers and loads the project context files for the system prompt, each capped. Only
// ONE project-context type loads, first found wins: .hermes.md / HERMES.md (walking to the git root), then
// the AGENTS.md chain (git root to cwd), then CLAUDE.md (cwd), then .cursorrules and .cursor/rules/*.mdc
// (cwd). SOUL.md from the home is independent and always included unless SkipSoul.
func BuildContextFiles(o ContextOptions) ContextResult {
	cwd := o.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	cwd = resolvePath(cwd)
	l := &loader{o: o}
	var sections []string
	for _, load := range []func(string) string{l.loadHermesMD, l.loadAgentsMD, l.loadClaudeMD, l.loadCursorRules} {
		if s := load(cwd); s != "" {
			sections = append(sections, s)
			break
		}
	}
	if !o.SkipSoul {
		if s := l.loadSoul(); s != "" {
			sections = append(sections, s)
		}
	}
	if len(sections) == 0 {
		return ContextResult{Warnings: l.warnings}
	}
	return ContextResult{
		Prompt:   "# Project Context\n\nThe following project context files have been loaded and should be followed:\n\n" + strings.Join(sections, "\n"),
		Warnings: l.warnings,
	}
}
