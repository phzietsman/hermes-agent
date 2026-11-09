package fuzzy

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Span is a half-open range of rune offsets.
type Span struct{ Start, End int }

// IdenticalStringsError is returned when old and new text are the same.
const IdenticalStringsError = "No edit was applied because old_string and new_string are identical. " +
	"Provide the existing text to replace in old_string and the changed " +
	"replacement text in new_string."

var unicodeMap = map[rune]string{
	'“': `"`, '”': `"`, '‘': "'", '’': "'", '—': "--", '–': "-", '…': "...", ' ': " ",
	'−': "-", ' ': " ", ' ': " ", ' ': " ", ' ': " ", ' ': " ", ' ': " ", ' ': " ",
	' ': " ", ' ': " ", ' ': " ", ' ': " ", ' ': " ", ' ': " ", '　': " ",
}

func unicodeNormalize(text string) string {
	var b strings.Builder
	for _, r := range text {
		if rep, ok := unicodeMap[r]; ok {
			b.WriteString(rep)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isPySpace(r rune) bool    { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }
func pyStrip(s string) string  { return strings.TrimFunc(s, isPySpace) }
func pyLstrip(s string) string { return strings.TrimLeftFunc(s, isPySpace) }
func rlen(s string) int        { return utf8.RuneCountInString(s) }

// ---- position helpers --------------------------------------------------------

func linePositions(lines []string, start, end, contentLen int) Span {
	startPos, endPos := 0, -1
	for _, l := range lines[:start] {
		startPos += rlen(l) + 1
	}
	for _, l := range lines[:end] {
		endPos += rlen(l) + 1
	}
	return Span{startPos, min(contentLen, endPos)}
}

func windowSpans(content string, lines []string, n int, accept func(int) bool) []Span {
	var out []Span
	cl := rlen(content)
	for i := 0; i < len(lines)-n+1; i++ {
		if accept(i) {
			out = append(out, linePositions(lines, i, i+n, cl))
		}
	}
	return out
}

func matchTransformedLines(content, pattern string, transform func([]string) []string) []Span {
	lines := strings.Split(content, "\n")
	norm := transform(strings.Split(pattern, "\n"))
	n := len(norm)
	return windowSpans(content, lines, n, func(i int) bool { return equalStrings(transform(lines[i:i+n]), norm) })
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func mapLines(f func(string) string) func([]string) []string {
	return func(ls []string) []string {
		out := make([]string, len(ls))
		for i, l := range ls {
			out[i] = f(l)
		}
		return out
	}
}

func stripBoundary(lines []string) []string {
	out := append([]string(nil), lines...)
	out[0] = pyStrip(out[0])
	if len(out) > 1 {
		out[len(out)-1] = pyStrip(out[len(out)-1])
	}
	return out
}

func buildOrigToNormMap(original string) []int {
	var result []int
	normPos := 0
	for _, r := range original {
		result = append(result, normPos)
		if rep, ok := unicodeMap[r]; ok {
			normPos += rlen(rep)
		} else {
			normPos++
		}
	}
	return append(result, normPos)
}

func normEndToOrig(origToNorm []int, origStart, normEnd int) int {
	origLen := len(origToNorm) - 1
	end := origStart
	for end < origLen && origToNorm[end] < normEnd {
		end++
	}
	return end
}

func mapPositionsNormToOrig(origToNorm []int, normMatches []Span) []Span {
	inverted := map[int]int{}
	for origPos, normPos := range origToNorm[:len(origToNorm)-1] {
		if _, ok := inverted[normPos]; !ok {
			inverted[normPos] = origPos
		}
	}
	var out []Span
	for _, m := range normMatches {
		if origStart, ok := inverted[m.Start]; ok {
			out = append(out, Span{origStart, normEndToOrig(origToNorm, origStart, m.End)})
		}
	}
	return out
}

func isSpaceTab(r rune) bool { return r == ' ' || r == '\t' }

// mapNormalizedPositions is the best-effort span mapping for [ \t]+ -> ' ' collapsing.
func mapNormalizedPositions(original, normalized string, normMatches []Span) []Span {
	orig, norm := []rune(original), []rune(normalized)
	var origToNorm []int
	oi, ni := 0, 0
	for oi < len(orig) && ni < len(norm) {
		switch {
		case orig[oi] == norm[ni]:
			origToNorm = append(origToNorm, ni)
			oi++
			ni++
		case isSpaceTab(orig[oi]) && norm[ni] == ' ':
			origToNorm = append(origToNorm, ni)
			oi++
			if oi < len(orig) && !isSpaceTab(orig[oi]) {
				ni++
			}
		default:
			origToNorm = append(origToNorm, ni)
			oi++
		}
	}
	for ; oi < len(orig); oi++ {
		origToNorm = append(origToNorm, len(norm))
	}
	normToOrigStart, normToOrigEnd := map[int]int{}, map[int]int{}
	for origPos, normPos := range origToNorm {
		if _, ok := normToOrigStart[normPos]; !ok {
			normToOrigStart[normPos] = origPos
		}
		normToOrigEnd[normPos] = origPos
	}
	var out []Span
	for _, m := range normMatches {
		var origStart int
		if s, ok := normToOrigStart[m.Start]; ok {
			origStart = s
		} else {
			origStart = -1
			for i, n := range origToNorm {
				if n >= m.Start {
					origStart = i
					break
				}
			}
		}
		var origEnd int
		if e, ok := normToOrigEnd[m.End-1]; ok {
			origEnd = e + 1
		} else {
			origEnd = origStart + (m.End - m.Start)
		}
		if m.End < len(norm) && norm[m.End-1] == ' ' {
			for origEnd < len(orig) && isSpaceTab(orig[origEnd]) {
				origEnd++
			}
		}
		out = append(out, Span{origStart, min(origEnd, len(orig))})
	}
	return out
}

// ---- strategies --------------------------------------------------------------

func strategyExact(content, pattern string) []Span {
	c, p := []rune(content), []rune(pattern)
	var out []Span
	if len(p) == 0 {
		return out
	}
	for i := 0; i+len(p) <= len(c); {
		if runesEqual(c[i:i+len(p)], p) {
			out = append(out, Span{i, i + len(p)})
			i += len(p)
		} else {
			i++
		}
	}
	return out
}

func runesEqual(a, b []rune) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func strategyLineTrimmed(content, pattern string) []Span {
	return matchTransformedLines(content, pattern, mapLines(pyStrip))
}

var spaceTabRe = regexp.MustCompile(`[ \t]+`)

func strategyWhitespaceNormalized(content, pattern string) []Span {
	norm := func(s string) string { return spaceTabRe.ReplaceAllString(s, " ") }
	cn := norm(content)
	matches := strategyExact(cn, norm(pattern))
	if len(matches) == 0 {
		return nil
	}
	return mapNormalizedPositions(content, cn, matches)
}

func strategyIndentationFlexible(content, pattern string) []Span {
	return matchTransformedLines(content, pattern, mapLines(pyLstrip))
}

func strategyEscapeNormalized(content, pattern string) []Span {
	un := strings.NewReplacer(`\n`, "\n", `\t`, "\t", `\r`, "\r").Replace(pattern)
	if un == pattern {
		return nil
	}
	return strategyExact(content, un)
}

func strategyTrimmedBoundary(content, pattern string) []Span {
	return matchTransformedLines(content, pattern, stripBoundary)
}

func strategyUnicodeNormalized(content, pattern string) []Span {
	np, nc := unicodeNormalize(pattern), unicodeNormalize(content)
	if nc == content && np == pattern {
		return nil
	}
	matches := strategyExact(nc, np)
	if len(matches) == 0 {
		matches = strategyLineTrimmed(nc, np)
	}
	if len(matches) == 0 {
		return nil
	}
	return mapPositionsNormToOrig(buildOrigToNormMap(content), matches)
}

func strategyBlockAnchor(content, pattern string) []Span {
	patternLines := strings.Split(unicodeNormalize(pattern), "\n")
	if len(patternLines) < 2 {
		return nil
	}
	first, last := pyStrip(patternLines[0]), pyStrip(patternLines[len(patternLines)-1])
	n := len(patternLines)
	normLines := strings.Split(unicodeNormalize(content), "\n")
	potential := map[int]bool{}
	for i := 0; i < len(normLines)-n+1; i++ {
		if pyStrip(normLines[i]) == first && pyStrip(normLines[i+n-1]) == last {
			potential[i] = true
		}
	}
	threshold := 0.70
	if len(potential) == 1 {
		threshold = 0.50
	}
	patternMiddle := strings.Join(patternLines[1:len(patternLines)-1], "\n")
	similar := func(i int) bool {
		if !potential[i] {
			return false
		}
		if n <= 2 {
			return true
		}
		contentMiddle := strings.Join(normLines[i+1:i+n-1], "\n")
		return NewSequenceMatcher(contentMiddle, patternMiddle).Ratio() >= threshold
	}
	return windowSpans(content, strings.Split(content, "\n"), n, similar)
}

func sim(a, b string) float64 {
	if a == b {
		return 1
	}
	return NewSequenceMatcher(a, b).Ratio()
}

func strategyContextAware(content, pattern string) []Span {
	pl := strings.Split(pattern, "\n")
	cl := strings.Split(content, "\n")
	n := len(pl)
	if n > len(cl) {
		return nil
	}
	firstPat, lastPat := pyStrip(pl[0]), pyStrip(pl[n-1])
	accept := func(i int) bool {
		block := cl[i : i+n]
		if sim(firstPat, pyStrip(block[0])) < 0.80 || sim(lastPat, pyStrip(block[n-1])) < 0.80 {
			return false
		}
		for k, p := range pl {
			if pyStrip(p) != "" && sim(pyStrip(p), pyStrip(block[k])) < 0.80 {
				return false
			}
		}
		return true
	}
	return windowSpans(content, cl, n, accept)
}

type strategy struct {
	name string
	fn   func(content, pattern string) []Span
}

var strategies = []strategy{
	{"exact", strategyExact}, {"line_trimmed", strategyLineTrimmed}, {"whitespace_normalized", strategyWhitespaceNormalized},
	{"indentation_flexible", strategyIndentationFlexible}, {"escape_normalized", strategyEscapeNormalized},
	{"trimmed_boundary", strategyTrimmedBoundary}, {"unicode_normalized", strategyUnicodeNormalized},
	{"block_anchor", strategyBlockAnchor}, {"context_aware", strategyContextAware},
}

var similarityStrategies = map[string]bool{"block_anchor": true, "context_aware": true}

// ---- orchestration -------------------------------------------------------------

// IsAlreadyApplied is true when the edit is already present: new text of at least 8 characters is there verbatim
// and the old text is gone, so a re-sent edit succeeds as a no-op.
func IsAlreadyApplied(content, oldString, newString string) bool {
	if newString == "" || rlen(pyStrip(newString)) < 8 || !strings.Contains(content, newString) {
		return false
	}
	return oldString == newString || !strings.Contains(content, oldString)
}

func matchedRegions(content string, matches []Span) string {
	r := []rune(content)
	var b strings.Builder
	for _, m := range matches {
		b.WriteString(string(r[m.Start:m.End]))
	}
	return b.String()
}

func formatMatchLocations(content string, matches []Span, cap int) string {
	r := []rune(content)
	var rows []string
	for _, m := range matches[:min(cap, len(matches))] {
		lineNo := 1
		lineStart := 0
		for i := 0; i < m.Start; i++ {
			if r[i] == '\n' {
				lineNo++
				lineStart = i + 1
			}
		}
		lineEnd := len(r)
		for i := lineStart; i < len(r); i++ {
			if r[i] == '\n' {
				lineEnd = i
				break
			}
		}
		snippet := pyStrip(string(r[lineStart:lineEnd]))
		if rlen(snippet) > 80 {
			snippet = firstRunes(snippet, 77) + "..."
		}
		rows = append(rows, fmt.Sprintf("  L%d: %s", lineNo, snippet))
	}
	if extra := len(matches) - cap; extra > 0 {
		rows = append(rows, fmt.Sprintf("  ... and %d more", extra))
	}
	return strings.Join(rows, "\n")
}

func firstRunes(s string, n int) string {
	if rlen(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// Result is the outcome of FindAndReplace.
type Result struct {
	Content  string
	Count    int
	Strategy string
	Error    string
}

// FindAndReplace applies old -> new through the strategy chain, most precise first. On failure the content is
// returned unchanged with Count 0 and an Error.
func FindAndReplace(content, oldString, newString string, replaceAll bool) Result {
	fail := func(msg string) Result { return Result{Content: content, Error: msg} }
	switch {
	case oldString == "":
		return fail("old_string is empty — nothing to match. Set old_string to the exact " +
			"existing text the replacement should replace (read the file first if " +
			"unsure). To create a new file or fully rewrite one, use write_file " +
			"instead. Do not re-send this call unchanged.")
	case pyStrip(oldString) == "":
		return fail("old_string is only whitespace — provide non-blank text to match. Set it " +
			"to the exact existing text the replacement should replace (read the file " +
			"first if unsure). Do not re-send this call unchanged.")
	case oldString == newString:
		return fail(IdenticalStringsError)
	}
	for _, st := range strategies {
		matches := st.fn(content, oldString)
		if len(matches) == 0 {
			continue
		}
		if len(matches) > 1 && !replaceAll {
			return fail(fmt.Sprintf("Found %d matches for old_string. Provide more context to make it unique, or use replace_all=True. Matches:\n%s",
				len(matches), formatMatchLocations(content, matches, 5)))
		}
		if replaceAll && len(matches) > 1 && similarityStrategies[st.name] {
			return fail(fmt.Sprintf("Found %d approximate matches via the '%s' strategy; replace_all only applies to exact "+
				"matches. Provide the precise text (whitespace included) so an "+
				"exact/line-trimmed match can be made.", len(matches), st.name))
		}
		if st.name != "exact" {
			if msg := detectEscapeDrift(content, matches, oldString, newString); msg != "" {
				return fail(msg)
			}
		}
		effective := maybeUnescapeNewString(newString, content, matches)
		old := ""
		if st.name != "exact" {
			old = oldString
		}
		var plan *unicodePlan
		if st.name == "unicode_normalized" {
			plan = newUnicodePlan(oldString, effective)
		}
		return Result{Content: applyReplacements(content, matches, effective, st.name != "exact", old, plan), Count: len(matches), Strategy: st.name}
	}
	return fail("Could not find a match for old_string in the file")
}

// ---- escape-drift guards ----------------------------------------------------------

func detectNewlineLiteralDrift(content string, matches []Span, oldString, newString string) string {
	if !strings.Contains(newString, `\n`) {
		return ""
	}
	oldLiterals := strings.Count(oldString, `\n`)
	r := []rune(content)
	for _, m := range matches {
		region := string(r[m.Start:m.End])
		if strings.Contains(region, "\n") && oldLiterals > strings.Count(region, `\n`) {
			return "Escape-drift detected: old_string contains more literal " +
				"'\\\\n' sequences than the matched region of the file, which " +
				"has real line breaks there instead. This is almost always a " +
				"tool-call serialization artifact where a line break got escaped " +
				"one extra time; new_string would write it as backslash + n. " +
				"Re-read the file with read_file and pass old_string/new_string " +
				"with actual line breaks, keeping only the '\\\\n' sequences " +
				"that appear literally in the file."
		}
	}
	return ""
}

func detectEscapeDrift(content string, matches []Span, oldString, newString string) string {
	hasQuote := strings.Contains(newString, `\'`) || strings.Contains(newString, `\"`)
	if !hasQuote && !strings.Contains(oldString, `\`) {
		return ""
	}
	regions := matchedRegions(content, matches)
	if hasQuote {
		for _, suspect := range []string{`\'`, `\"`} {
			if strings.Contains(newString, suspect) && strings.Contains(oldString, suspect) && !strings.Contains(regions, suspect) {
				plain := suspect[1:]
				return fmt.Sprintf("Escape-drift detected: old_string and new_string contain "+
					"the literal sequence %s but the matched region of "+
					"the file does not. This is almost always a tool-call "+
					"serialization artifact where an apostrophe or quote got "+
					"prefixed with a spurious backslash. Re-read the file with "+
					"read_file and pass old_string/new_string without "+
					"backslash-escaping %s characters.", pyRepr(suspect), pyRepr(plain))
			}
		}
	}
	if d := detectNewlineLiteralDrift(content, matches, oldString, newString); d != "" {
		return d
	}
	return detectBackslashDoubling(regions, oldString, newString)
}

// pyRepr renders s as Python's repr does for the two quote-bearing strings used in drift messages.
func pyRepr(s string) string {
	switch s {
	case `\'`:
		return `"\\'"`
	case `\"`:
		return `'\\"'`
	case "'":
		return `"'"`
	case `"`:
		return `'"'`
	}
	return "'" + s + "'"
}

var backslashRunRe = regexp.MustCompile(`\\+`)

func backslashRuns(s string) []int {
	var out []int
	for _, m := range backslashRunRe.FindAllString(s, -1) {
		out = append(out, len(m))
	}
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func detectBackslashDoubling(regions, oldString, newString string) string {
	oldRuns, fileRuns := backslashRuns(oldString), backslashRuns(regions)
	if len(oldRuns) == 0 || len(fileRuns) == 0 || len(oldRuns) != len(fileRuns) || equalInts(oldRuns, fileRuns) {
		return ""
	}
	for i := range oldRuns {
		if oldRuns[i] != fileRuns[i]*2 {
			return ""
		}
	}
	anyBig := false
	for _, f := range fileRuns {
		anyBig = anyBig || f >= 2
	}
	if !(anyBig || len(fileRuns) >= 2) || equalInts(backslashRuns(newString), fileRuns) {
		return ""
	}
	return "Escape-drift detected: every backslash run in old_string is exactly " +
		"twice as long as in the matched region of the file (e.g. the file " +
		"has `\\\\` where old_string has `\\\\\\\\`). The tool-call arguments " +
		"were JSON-escaped one extra time; applying new_string verbatim would " +
		"double every backslash in the file. Re-read the file with read_file " +
		"and resend old_string/new_string with the backslash counts exactly " +
		"as they appear in the file."
}

func maybeUnescapeNewString(newString, content string, matches []Span) string {
	if !strings.Contains(newString, `\t`) && !strings.Contains(newString, `\r`) {
		return newString
	}
	regions := matchedRegions(content, matches)
	for _, p := range [][2]string{{`\t`, "\t"}, {`\r`, "\r"}} {
		if strings.Contains(newString, p[0]) && strings.Contains(regions, p[1]) {
			newString = strings.ReplaceAll(newString, p[0], p[1])
		}
	}
	return newString
}

// ---- replacement shaping -----------------------------------------------------------

func leadingWhitespace(line string) string {
	return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
}

func firstMeaningfulLine(text string) (string, bool) {
	for _, l := range strings.Split(text, "\n") {
		if pyStrip(l) != "" {
			return l, true
		}
	}
	return "", false
}

func reindentReplacement(fileRegion, oldString, newString string) string {
	if newString == "" {
		return newString
	}
	oldFirst, ok1 := firstMeaningfulLine(oldString)
	fileFirst, ok2 := firstMeaningfulLine(fileRegion)
	if !ok1 || !ok2 {
		return newString
	}
	oldIndent, fileIndent := leadingWhitespace(oldFirst), leadingWhitespace(fileFirst)
	if oldIndent == fileIndent {
		return newString
	}
	lines := strings.Split(newString, "\n")
	for i, line := range lines {
		switch {
		case pyStrip(line) == "":
		case strings.HasPrefix(leadingWhitespace(line), oldIndent):
			lines[i] = fileIndent + line[len(oldIndent):]
		default:
			lines[i] = fileIndent + strings.TrimLeft(line, " \t")
		}
	}
	return strings.Join(lines, "\n")
}

type unicodePlan struct {
	normOld string
	opcodes []Opcode
	newText []rune
}

func newUnicodePlan(oldString, newString string) *unicodePlan {
	no := unicodeNormalize(oldString)
	return &unicodePlan{normOld: no, opcodes: NewSequenceMatcher(no, newString).Opcodes(), newText: []rune(newString)}
}

func preserveUnicodeInReplacement(fileRegion, newString string, plan *unicodePlan) string {
	if plan.normOld != unicodeNormalize(fileRegion) {
		return newString
	}
	m := buildOrigToNormMap(fileRegion)
	region := []rune(fileRegion)
	var out strings.Builder
	for _, op := range plan.opcodes {
		switch op.Tag {
		case "equal":
			origStart := sort.SearchInts(m, op.I1+1) - 1 // bisect_right(m, i1) - 1
			origEnd := normEndToOrig(m, origStart, op.I2)
			out.WriteString(string(region[origStart:origEnd]))
		case "delete":
		default:
			out.WriteString(string(plan.newText[op.J1:op.J2]))
		}
	}
	return out.String()
}

func applyReplacements(content string, matches []Span, newString string, reshape bool, oldString string, plan *unicodePlan) string {
	r := []rune(content)
	sorted := append([]Span(nil), matches...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Start > sorted[j].Start })
	result := r
	for _, m := range sorted {
		adjusted := newString
		if reshape {
			region := string(r[m.Start:m.End])
			if plan != nil {
				adjusted = preserveUnicodeInReplacement(region, adjusted, plan)
			}
			adjusted = reindentReplacement(region, oldString, adjusted)
		}
		next := append(append(append([]rune(nil), result[:m.Start]...), []rune(adjusted)...), result[m.End:]...)
		result = next
	}
	return string(result)
}

// ---- "did you mean?" diagnostics ----------------------------------------------------

func visualizeWhitespace(line string) string {
	stripped := strings.TrimLeft(line, " \t")
	prefix := line[:len(line)-len(stripped)]
	return strings.ReplaceAll(strings.ReplaceAll(prefix, "\t", "→"), " ", "·") + stripped
}

// splitLines is Python's str.splitlines (without keepends): it splits at \n, \r\n, \r and the other Unicode line
// boundaries, and a trailing terminator does not start a new line.
func splitLines(s string) []string {
	var lines []string
	var cur []rune
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		switch rs[i] {
		case '\n', '\v', '\f', '\x1c', '\x1d', '\x1e', '\u0085', ' ', ' ':
			lines = append(lines, string(cur))
			cur = cur[:0]
		case '\r':
			lines = append(lines, string(cur))
			cur = cur[:0]
			if i+1 < len(rs) && rs[i+1] == '\n' {
				i++
			}
		default:
			cur = append(cur, rs[i])
		}
	}
	if len(cur) > 0 {
		lines = append(lines, string(cur))
	}
	return lines
}

// FindClosestLines returns numbered snippets of the lines most similar to old's anchor line, or "".
func FindClosestLines(oldString, content string, contextLines, maxResults int) string {
	if oldString == "" || content == "" {
		return ""
	}
	oldLines, contentLines := splitLines(oldString), splitLines(content)
	if len(oldLines) == 0 || len(contentLines) == 0 {
		return ""
	}
	anchor := pyStrip(oldLines[0])
	if anchor == "" {
		for _, l := range oldLines {
			if pyStrip(l) != "" {
				anchor = pyStrip(l)
				break
			}
		}
	}
	if anchor == "" {
		return ""
	}
	type scored struct {
		score float64
		idx   int
	}
	var all []scored
	for i, line := range contentLines {
		if pyStrip(line) != "" {
			all = append(all, scored{NewSequenceMatcher(anchor, pyStrip(line)).Ratio(), i})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].score > all[j].score })
	var top []scored
	for _, s := range all {
		if s.score > 0.3 && len(top) < maxResults {
			top = append(top, s)
		}
	}
	if len(top) == 0 {
		return ""
	}
	var parts []string
	seen := map[[2]int]bool{}
	for _, t := range top {
		start := max(0, t.idx-contextLines)
		end := min(len(contentLines), t.idx+len(oldLines)+contextLines)
		if seen[[2]int{start, end}] {
			continue
		}
		seen[[2]int{start, end}] = true
		var rows []string
		for j := 0; j < end-start; j++ {
			rows = append(rows, fmt.Sprintf("%4d| %s", start+j+1, contentLines[start+j]))
		}
		parts = append(parts, strings.Join(rows, "\n"))
	}
	result := strings.Join(parts, "\n---\n")
	best := contentLines[top[0].idx]
	if pyStrip(best) == anchor && best != oldLines[0] {
		result += "\n\nWhitespace difference detected (→ = tab, · = space):\n" +
			"  file has: " + visualizeWhitespace(best) + "\n" +
			"  you sent: " + visualizeWhitespace(oldLines[0]) + "\n" +
			"Use the exact whitespace shown in 'file has'."
	}
	return result
}

// NoMatchHint is the "Did you mean..." suffix for a plain no-match error, "" for any other failure (ambiguous,
// escape-drift and identical errors also have no match but a hint would mislead).
func NoMatchHint(errMsg string, matchCount int, oldString, content string) string {
	if matchCount != 0 || errMsg == "" || !strings.HasPrefix(errMsg, "Could not find") {
		return ""
	}
	if hint := FindClosestLines(oldString, content, 2, 3); hint != "" {
		return "\n\nDid you mean one of these sections?\n" + hint
	}
	return ""
}
