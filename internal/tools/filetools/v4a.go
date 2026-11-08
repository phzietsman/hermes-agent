package filetools

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/phzietsman/hermes-agent/internal/fuzzy"
)

// FS is the file store the patch engine edits. ReadRaw returns the exact text; a missing path is a *NotFoundError.
type FS interface {
	ReadRaw(path string) (string, error)
	Write(path, content string) error
	Delete(path string) error
	Move(src, dst string) error
}

// NotFoundError says a path is absent, as opposed to a read that failed.
type NotFoundError struct{ Path string }

func (e *NotFoundError) Error() string { return "File not found: " + e.Path }

type opKind int

const (
	opAdd opKind = iota
	opUpdate
	opDelete
	opMove
)

type hunkLine struct {
	prefix  byte
	content string
}

type hunk struct {
	hint  string
	lines []hunkLine
}

type operation struct {
	kind    opKind
	path    string
	newPath string
	hunks   []hunk
}

var (
	beginMarker = regexp.MustCompile(`^\*\*\*\s*Begin\s+Patch\s*$`)
	endMarker   = regexp.MustCompile(`^\*\*\*\s*End\s+Patch\s*$`)
	opMarkers   = []struct {
		kind opKind
		re   *regexp.Regexp
	}{
		{opUpdate, regexp.MustCompile(`^\*\*\*\s*Update\s+File:\s*(.+)`)},
		{opAdd, regexp.MustCompile(`^\*\*\*\s*Add\s+File:\s*(.+)`)},
		{opDelete, regexp.MustCompile(`^\*\*\*\s*Delete\s+File:\s*(.+)`)},
		{opMove, regexp.MustCompile(`^\*\*\*\s*Move\s+File:\s*(.+?)\s*->\s*(.+)`)},
	}
	hintRe = regexp.MustCompile(`^@@\s*(.+?)\s*@@`)
)

func strip(s string) string { return strings.TrimFunc(s, isPySpace) }

// parseV4A parses a V4A patch into operations. An empty patch is no operations and no error.
func parseV4A(patch string) ([]operation, error) {
	lines := strings.Split(patch, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	start, end := -1, len(lines)
	for i, l := range lines {
		if beginMarker.MatchString(l) {
			start = i
		} else if endMarker.MatchString(l) {
			end = i
			break
		}
	}
	var ops []operation
	var cur *operation
	var curHunk *hunk
	flushHunk := func() {
		if cur != nil && curHunk != nil && len(curHunk.lines) > 0 {
			cur.hunks = append(cur.hunks, *curHunk)
		}
	}
	flush := func() {
		if cur != nil {
			flushHunk()
			ops = append(ops, *cur)
		}
	}
	for _, line := range lines[start+1 : end] {
		var matched *struct {
			kind opKind
			re   *regexp.Regexp
		}
		var m []string
		for i := range opMarkers {
			if mm := opMarkers[i].re.FindStringSubmatch(line); mm != nil {
				matched, m = &opMarkers[i], mm
				break
			}
		}
		switch {
		case matched != nil:
			flush()
			op := operation{kind: matched.kind, path: strip(m[1])}
			if matched.kind == opMove {
				op.newPath = strip(m[2])
			}
			cur = &op
			curHunk = nil
			if matched.kind == opAdd {
				curHunk = &hunk{}
			}
			if matched.kind == opDelete || matched.kind == opMove {
				ops = append(ops, op)
				cur = nil
			}
		case strings.HasPrefix(line, "@@"):
			if cur != nil {
				flushHunk()
				h := &hunk{}
				if hm := hintRe.FindStringSubmatch(line); hm != nil {
					h.hint = hm[1]
				}
				curHunk = h
			}
		case cur != nil && line != "":
			if curHunk == nil {
				curHunk = &hunk{}
			}
			switch line[0] {
			case '+', '-', ' ':
				curHunk.lines = append(curHunk.lines, hunkLine{line[0], line[1:]})
			default:
				if line[0] != '\\' {
					curHunk.lines = append(curHunk.lines, hunkLine{' ', line})
				}
			}
		}
	}
	flush()
	var errs []string
	for _, op := range ops {
		if op.path == "" {
			errs = append(errs, "Operation with empty file path")
		}
		if op.kind == opUpdate && len(op.hunks) == 0 {
			errs = append(errs, fmt.Sprintf("UPDATE %s: no hunks found", pyRepr(op.path)))
		}
		if op.kind == opMove && op.newPath == "" {
			errs = append(errs, fmt.Sprintf("MOVE %s: missing destination path (expected 'src -> dst')", pyRepr(op.path)))
		}
	}
	if len(errs) > 0 {
		return nil, errors.New("Parse error: " + strings.Join(errs, "; "))
	}
	return ops, nil
}

// pyRepr renders a string the way Python's repr does for ordinary paths.
func pyRepr(s string) string {
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"`
	}
	return "'" + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), "'", `\'`) + "'"
}

func countOccurrences(text, pat string) int {
	if pat == "" {
		return len([]rune(text)) + 1
	}
	n := 0
	for i := 0; i+len(pat) <= len(text); i++ {
		if strings.HasPrefix(text[i:], pat) {
			n++
		}
	}
	return n
}

func splitHunk(h hunk) (search, replace []string) {
	for _, l := range h.lines {
		if l.prefix != '+' {
			search = append(search, l.content)
		}
		if l.prefix != '-' {
			replace = append(replace, l.content)
		}
	}
	return
}

func hintAmbiguity(content, hint, tail string) (int, string) {
	n := countOccurrences(content, hint)
	if n > 1 {
		return n, fmt.Sprintf("context hint '%s' is ambiguous (%d occurrences)%s", hint, n, tail)
	}
	return n, ""
}

func equalLines(a, b []string) bool {
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

func isNotFound(err error) bool {
	_, ok := err.(*NotFoundError)
	return ok
}

// validate dry-runs every operation and returns the problems (none means safe to apply). Update hunks are
// simulated in order so later hunks see the content earlier ones leave.
func validate(ops []operation, fs FS) []string {
	var errs []string
	realChanges := 0
	pending := map[string]string{}
	removed := map[string]bool{}
	read := func(path string) (string, string) {
		if c, ok := pending[path]; ok {
			return c, ""
		}
		if removed[path] {
			return "", "file not found"
		}
		c, err := fs.ReadRaw(path)
		if err != nil {
			return "", err.Error()
		}
		return c, ""
	}
	occupied := func(path string) string {
		if _, ok := pending[path]; ok {
			return "exists"
		}
		if removed[path] {
			return ""
		}
		if _, err := fs.ReadRaw(path); err == nil {
			return "exists"
		} else if isNotFound(err) {
			return ""
		} else {
			return "could not confirm the path is free — " + err.Error()
		}
	}
	for _, op := range ops {
		switch op.kind {
		case opUpdate:
			simulated, readErr := read(op.path)
			if readErr != "" {
				errs = append(errs, op.path+": "+readErr)
				continue
			}
			for hi, h := range op.hunks {
				search, replace := splitHunk(h)
				if equalLines(search, replace) {
					for _, l := range h.lines {
						if l.prefix == '-' || l.prefix == '+' {
							realChanges++
							break
						}
					}
					continue
				}
				realChanges++
				if len(search) == 0 {
					if h.hint != "" {
						occ, amb := hintAmbiguity(simulated, h.hint, "")
						if occ == 0 {
							errs = append(errs, fmt.Sprintf("%s: addition-only hunk context hint '%s' not found", op.path, h.hint))
						} else if amb != "" {
							errs = append(errs, fmt.Sprintf("%s: addition-only hunk %s", op.path, amb))
						}
					}
					continue
				}
				pattern, replacement := strings.Join(search, "\n"), strings.Join(replace, "\n")
				r := fuzzy.FindAndReplace(simulated, pattern, replacement, false)
				if r.Count > 0 {
					simulated = r.Content
				} else if !fuzzy.IsAlreadyApplied(simulated, pattern, replacement) {
					label := "(no hint)"
					if h.hint != "" {
						label = "'" + h.hint + "'"
					}
					msg := fmt.Sprintf("%s: hunk %d %s not found", op.path, hi+1, label)
					if r.Error != "" {
						msg += " — " + r.Error
					}
					msg += fuzzy.NoMatchHint(r.Error, 0, pattern, simulated)
					errs = append(errs, msg)
				}
			}
			pending[op.path] = simulated
			continue
		}
		realChanges++
		switch op.kind {
		case opDelete:
			if _, e := read(op.path); e != "" {
				errs = append(errs, op.path+": file not found for deletion")
			} else {
				removed[op.path] = true
				delete(pending, op.path)
			}
		case opMove:
			if op.newPath == "" {
				errs = append(errs, op.path+": MOVE operation missing destination path")
				continue
			}
			srcContent, srcErr := read(op.path)
			if srcErr != "" {
				errs = append(errs, op.path+": source file not found for move")
			}
			switch taken := occupied(op.newPath); {
			case taken == "exists":
				errs = append(errs, op.newPath+": destination already exists — move would overwrite")
			case taken != "":
				errs = append(errs, op.newPath+": "+taken)
			case srcErr == "":
				pending[op.newPath] = srcContent
				removed[op.path] = true
				delete(pending, op.path)
			}
		case opAdd:
			switch taken := occupied(op.path); {
			case taken == "exists":
				errs = append(errs, op.path+": file already exists — use Update File, not Add File")
			case taken != "":
				errs = append(errs, op.path+": "+taken)
			default:
				delete(removed, op.path)
				var adds []string
				for _, h := range op.hunks {
					for _, l := range h.lines {
						if l.prefix == '+' {
							adds = append(adds, l.content)
						}
					}
				}
				pending[op.path] = strings.Join(adds, "\n")
			}
		}
	}
	if len(errs) == 0 && realChanges == 0 {
		errs = append(errs, "Patch contains no changes (only context lines were provided)")
	}
	return errs
}

// PatchResult is what a patch call reports.
type PatchResult struct {
	Success       bool
	Diff          string
	FilesModified []string
	FilesCreated  []string
	FilesDeleted  []string
	Error         string
	NoChange      bool
	Note          string
	// Writes records (path, sha256 of what was read, sha256 of what was written) for each file write.
	Writes []FileWrite
}

// FileWrite is one file write of a patch.
type FileWrite struct{ Path, ReadSHA, WrittenSHA string }

// ApplyV4A parses, validates and applies a V4A patch. Validation covers every operation before any file is
// touched, so a bad hunk leaves everything as it was.
func ApplyV4A(patch string, fs FS, sums Sums) PatchResult {
	ops, err := parseV4A(patch)
	if err != nil {
		return PatchResult{Error: "Failed to parse patch: " + err.Error()}
	}
	if errs := validate(ops, fs); len(errs) > 0 {
		return PatchResult{Error: "Patch validation failed (no files were modified):\n" + bullets(errs)}
	}
	var (
		errs  []string
		diffs []string
		res   PatchResult
	)
	for _, op := range ops {
		var (
			ok      bool
			payload string
			write   *FileWrite
			verb    string
			bucket  *[]string
		)
		func() {
			defer func() {
				if r := recover(); r != nil {
					ok, payload = false, fmt.Sprint(r)
				}
			}()
			switch op.kind {
			case opAdd:
				verb, bucket = "add", &res.FilesCreated
				ok, payload, write = applyAdd(op, fs, sums)
			case opDelete:
				verb, bucket = "delete", &res.FilesDeleted
				ok, payload, write = applyDelete(op, fs)
			case opMove:
				verb, bucket = "move", &res.FilesModified
				ok, payload, write = applyMove(op, fs)
			default:
				verb, bucket = "update", &res.FilesModified
				ok, payload, write = applyUpdate(op, fs, sums)
			}
		}()
		if !ok {
			errs = append(errs, fmt.Sprintf("Failed to %s %s: %s", verb, op.path, payload))
			continue
		}
		name := op.path
		if op.kind == opMove {
			name = op.path + " -> " + op.newPath
		}
		*bucket = append(*bucket, name)
		diffs = append(diffs, payload)
		if write != nil {
			res.Writes = append(res.Writes, *write)
		}
	}
	res.Success = len(errs) == 0
	if len(errs) > 0 {
		res.Error = "Apply phase failed (state may be inconsistent — run `git diff` to assess):\n" + bullets(errs)
	}
	res.Diff = strings.Join(diffs, "\n")
	return res
}

func bullets(errs []string) string {
	rows := make([]string, len(errs))
	for i, e := range errs {
		rows[i] = "  • " + e
	}
	return strings.Join(rows, "\n")
}

// Sums hashes text for the write records; nil means no hashing.
type Sums func(string) string

func (s Sums) of(text string) string {
	if s == nil {
		return ""
	}
	return s(text)
}

func splitKeepEnds(s string) []string {
	var out []string
	for len(s) > 0 {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			out = append(out, s)
			break
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
	}
	return out
}

func unifiedDiff(path, old string, new *string) string {
	to := "/dev/null"
	var newLines []string
	if new != nil {
		to = "b/" + path
		newLines = splitKeepEnds(*new)
	}
	return fuzzy.UnifiedDiff(splitKeepEnds(old), newLines, "a/"+path, to)
}

func applyAdd(op operation, fs FS, sums Sums) (bool, string, *FileWrite) {
	if _, err := fs.ReadRaw(op.path); err == nil {
		return false, op.path + ": file already exists — use Update File, not Add File", nil
	} else if !isNotFound(err) {
		return false, op.path + ": could not confirm the path is free — " + err.Error(), nil
	}
	var lines []string
	for _, h := range op.hunks {
		for _, l := range h.lines {
			if l.prefix == '+' {
				lines = append(lines, l.content)
			}
		}
	}
	content := strings.Join(lines, "\n")
	if err := fs.Write(op.path, content); err != nil {
		return false, err.Error(), nil
	}
	rows := make([]string, len(lines))
	for i, l := range lines {
		rows[i] = "+" + l
	}
	diff := "--- /dev/null\n+++ b/" + op.path + "\n" + strings.Join(rows, "\n")
	return true, diff, &FileWrite{op.path, "", sums.of(content)}
}

func applyDelete(op operation, fs FS) (bool, string, *FileWrite) {
	content, err := fs.ReadRaw(op.path)
	if err != nil {
		return false, "Cannot delete " + op.path + ": file not found", nil
	}
	if err := fs.Delete(op.path); err != nil {
		return false, err.Error(), nil
	}
	diff := unifiedDiff(op.path, content, nil)
	if diff == "" {
		diff = "# Deleted: " + op.path
	}
	return true, diff, nil
}

func applyMove(op operation, fs FS) (bool, string, *FileWrite) {
	if _, err := fs.ReadRaw(op.newPath); err == nil {
		return false, op.newPath + ": destination already exists — move would overwrite", nil
	} else if !isNotFound(err) {
		return false, op.newPath + ": could not confirm the destination is free — " + err.Error(), nil
	}
	if err := fs.Move(op.path, op.newPath); err != nil {
		return false, err.Error(), nil
	}
	return true, "# Moved: " + op.path + " -> " + op.newPath, nil
}

func insertAdditionOnly(content string, h hunk, insert string) (string, string) {
	if h.hint != "" {
		occ, amb := hintAmbiguity(content, h.hint, " — provide a more unique hint")
		if amb != "" {
			return "", "Addition-only hunk: " + amb
		}
		if occ == 1 {
			from := strings.Index(content, h.hint)
			eol := strings.Index(content[from:], "\n")
			if eol < 0 {
				return content + "\n" + insert, ""
			}
			eol += from
			return content[:eol+1] + insert + "\n" + content[eol+1:], ""
		}
	}
	return strings.TrimRight(content, "\n") + "\n" + insert + "\n", ""
}

func applyUpdate(op operation, fs FS, sums Sums) (bool, string, *FileWrite) {
	current, err := fs.ReadRaw(op.path)
	if err != nil {
		return false, "Cannot read file: " + err.Error(), nil
	}
	newContent := current
	for _, h := range op.hunks {
		search, replace := splitHunk(h)
		if len(search) > 0 && equalLines(search, replace) {
			continue
		}
		pattern, replacement := strings.Join(search, "\n"), strings.Join(replace, "\n")
		if len(search) == 0 {
			var e string
			newContent, e = insertAdditionOnly(newContent, h, replacement)
			if e != "" {
				return false, e, nil
			}
			continue
		}
		r := fuzzy.FindAndReplace(newContent, pattern, replacement, false)
		if r.Error == "" || r.Count > 0 {
			newContent = r.Content
			continue
		}
		errMsg := r.Error
		if h.hint != "" {
			if hp := strings.Index(newContent, h.hint); hp >= 0 {
				rs := []rune(newContent)
				hpr := len([]rune(newContent[:hp]))
				ws, we := max(0, hpr-500), min(len(rs), hpr+2000)
				w := fuzzy.FindAndReplace(string(rs[ws:we]), pattern, replacement, false)
				if w.Count > 0 {
					newContent = string(rs[:ws]) + w.Content + string(rs[we:])
					errMsg = ""
				} else {
					errMsg = w.Error
				}
			}
		}
		if errMsg != "" {
			if fuzzy.IsAlreadyApplied(newContent, pattern, replacement) {
				continue
			}
			return false, "Could not apply hunk: " + errMsg + fuzzy.NoMatchHint(errMsg, 0, pattern, newContent), nil
		}
	}
	if err := fs.Write(op.path, newContent); err != nil {
		return false, err.Error(), nil
	}
	return true, unifiedDiff(op.path, current, &newContent), &FileWrite{op.path, sums.of(current), sums.of(newContent)}
}
