// Package filetools implements the read_file, write_file and patch tools. It ports tools/file_tools*.py,
// tools/file_operations.py, tools/patch_parser.py and the file-safety rules of agent/file_safety.py for a local
// filesystem. Not ported: document extraction, image handling, linters and LSP diagnostics, remote backends and
// shared multi-agent file state.
package filetools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/phzietsman/hermes-agent/internal/fuzzy"
	"github.com/phzietsman/hermes-agent/internal/tools"
)

const (
	defaultReadLimit = 2000
	maxLineLength    = 2000
	maxReadChars     = 100_000
	largeFileHint    = 512_000
)

var binaryExtensions = map[string]bool{}

func init() {
	for _, e := range strings.Fields(`.png .jpg .jpeg .gif .bmp .ico .webp .tiff .tif .mp4 .mov .avi .mkv .webm .wmv .flv .m4v .mpeg .mpg
.mp3 .wav .ogg .flac .aac .m4a .wma .aiff .opus .zip .tar .gz .bz2 .7z .rar .xz .z .tgz .iso .exe .dll .so .dylib .bin .o .a .obj .lib
.app .msi .deb .rpm .doc .docx .xls .xlsx .ppt .pptx .odt .ods .odp .ttf .otf .woff .woff2 .eot .pyc .pyo .class .jar .war .ear .node
.wasm .rlib .sqlite .sqlite3 .db .mdb .idx .psd .ai .eps .sketch .fig .xd .blend .3ds .max .swf .fla .lockb .dat .data`) {
		binaryExtensions[e] = true
	}
}

// ---- result shaping ----------------------------------------------------------------

// obj builds a JSON object that keeps the key order of its fields: the order is part of the tool's output.
func obj(fields ...tools.Field) string { return tools.Result(fields...) }

func f(k string, v any) tools.Field { return tools.Field{Key: k, Value: v} }

func readResult(content string, totalLines, fileSize int, truncated bool, hint string, truncatedLines bool, errMsg string, notFound bool, similar []string, isBinary bool) []tools.Field {
	fields := []tools.Field{f("content", content), f("total_lines", totalLines), f("file_size", fileSize), f("truncated", truncated)}
	if truncatedLines {
		fields = append(fields, f("truncated_lines", true))
	}
	if hint != "" {
		fields = append(fields, f("hint", hint))
	}
	fields = append(fields, f("is_binary", isBinary), f("is_image", false))
	if errMsg != "" {
		fields = append(fields, f("error", errMsg))
	}
	fields = append(fields, f("not_found", notFound))
	if len(similar) > 0 {
		fields = append(fields, f("similar_files", similar))
	}
	return fields
}

// ---- paths -----------------------------------------------------------------------------

func expandTilde(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// resolve turns a model-supplied path into an absolute one (session cwd for relative paths), and also returns
// its symlink-resolved form for the guards.
func resolve(env tools.CallEnv, p string) (abs, real string) {
	p = expandTilde(p)
	if !filepath.IsAbs(p) {
		base := env.CWD
		if base == "" {
			base, _ = os.Getwd()
		}
		p = filepath.Join(base, p)
	}
	abs = filepath.Clean(p)
	return abs, realpath(abs)
}

// ---- read_file --------------------------------------------------------------------------

var conflictOpenRe = regexp.MustCompile(`(?m)^\s*\d+\|<<<<<<< `)
var conflictCloseRe = regexp.MustCompile(`(?m)^\s*\d+\|>>>>>>> `)

func countConflictBlocks(formatted string) int {
	opens := len(conflictOpenRe.FindAllString(formatted, -1))
	if opens == 0 {
		return 0
	}
	return min(opens, len(conflictCloseRe.FindAllString(formatted, -1)))
}

// isLikelyBinary is text iff the sample is valid UTF-8, allowing one incomplete multibyte sequence at the very end
// (the byte-boundary cut); NUL bytes or mid-stream invalid bytes are binary.
func isLikelyBinary(sample []byte) bool {
	if len(sample) == 0 {
		return false
	}
	if bytes.IndexByte(sample, 0) >= 0 {
		return true
	}
	if utf8.Valid(sample) {
		return false
	}
	for cut := 1; cut <= 3 && cut < len(sample); cut++ {
		if utf8.Valid(sample[:len(sample)-cut]) && !utf8.FullRune(sample[len(sample)-cut:]) {
			return false
		}
	}
	return true
}

var magicSignatures = []struct {
	prefix string
	name   string
}{
	{"\x89PNG\r\n\x1a\n", "PNG image data"}, {"\xff\xd8\xff", "JPEG image data"}, {"GIF87a", "GIF image data"}, {"GIF89a", "GIF image data"},
	{"RIFF", "RIFF container (WAV/AVI/WebP family)"}, {"%PDF-", "PDF document"}, {"PK\x03\x04", "ZIP archive (also docx/xlsx/jar/apk)"},
	{"PK\x05\x06", "ZIP archive (empty)"}, {"\x1f\x8b", "gzip compressed data"}, {"BZh", "bzip2 compressed data"},
	{"\xfd7zXZ\x00", "xz compressed data"}, {"7z\xbc\xaf\x27\x1c", "7-Zip archive"}, {"\x7fELF", "ELF executable"},
	{"MZ", "Windows PE executable"}, {"\xcf\xfa\xed\xfe", "Mach-O executable (64-bit)"}, {"\xca\xfe\xba\xbe", "Mach-O universal binary / Java class"},
	{"SQLite format 3\x00", "SQLite database"}, {"OggS", "Ogg container"}, {"fLaC", "FLAC audio"}, {"ID3", "MP3 audio (ID3 tag)"},
	{"\x00\x00\x00", "ISO media container (MP4/MOV family)"}, {"BM", "BMP image data"}, {"II*\x00", "TIFF image data (little-endian)"},
	{"MM\x00*", "TIFF image data (big-endian)"},
}

func describeBinary(sample []byte, size int) string {
	kind := "unknown binary"
	for _, m := range magicSignatures {
		if bytes.HasPrefix(sample, []byte(m.prefix)) {
			if strings.HasPrefix(m.name, "ISO media") && !(len(sample) >= 8 && string(sample[4:8]) == "ftyp") {
				continue
			}
			kind = m.name
			break
		}
	}
	var sz string
	switch {
	case size >= 1024*1024:
		sz = fmt.Sprintf("%.1f MB", float64(size)/(1024*1024))
	case size >= 1024:
		sz = fmt.Sprintf("%.1f KB", float64(size)/1024)
	default:
		sz = fmt.Sprintf("%d bytes", size)
	}
	return fmt.Sprintf("Binary file (%s, %s) — cannot display as text.", kind, sz)
}

type page struct {
	lines      []string
	total      int
	endsNL     bool
	sample     []byte
	fileSize   int
	notRegular bool
}

// readPage reads lines [offset, offset+limit-1], clamping each stored line to a byte budget so a one-line
// 400 MB file is never held whole, and counts the lines of the whole file.
func readPage(path string, offset, limit int) (page, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return page{}, err
	}
	if !fi.Mode().IsRegular() {
		return page{notRegular: true}, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return page{}, err
	}
	defer file.Close()
	p := page{fileSize: int(fi.Size())}
	p.sample = make([]byte, 1000)
	n, _ := io.ReadFull(file, p.sample)
	p.sample = p.sample[:n]
	if isLikelyBinary(p.sample) || binaryExtensions[strings.ToLower(filepath.Ext(path))] {
		return p, nil
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return p, err
	}
	clamp := 4*maxLineLength + 1
	end := offset + limit - 1
	lineno := 1
	var kept []byte
	havePartial := false
	var last byte
	buf := make([]byte, 1<<20)
	for {
		n, rerr := file.Read(buf)
		chunk := buf[:n]
		if n > 0 {
			last = chunk[n-1]
			pos := 0
			for pos < n {
				nl := bytes.IndexByte(chunk[pos:], '\n')
				inPage := lineno >= offset && lineno <= end
				if nl < 0 {
					if inPage && len(kept) < clamp {
						take := min(n-pos, clamp-len(kept))
						kept = append(kept, chunk[pos:pos+take]...)
					}
					havePartial = true
					break
				}
				if inPage {
					if len(kept) < clamp {
						take := min(nl, clamp-len(kept))
						kept = append(kept, chunk[pos:pos+take]...)
					}
					p.lines = append(p.lines, string(kept))
				}
				kept = kept[:0]
				havePartial = false
				p.total++
				lineno++
				pos += nl + 1
			}
		}
		if rerr != nil {
			break
		}
	}
	if havePartial && lineno >= offset && lineno <= end {
		p.lines = append(p.lines, string(kept))
	}
	p.endsNL = last == '\n'
	if p.fileSize > 0 && !p.endsNL {
		p.total++ // a final line without a newline is still a line
	}
	return p, nil
}

func addLineNumbers(lines []string, start int) string {
	rows := make([]string, len(lines))
	for i, l := range lines {
		if utf8.RuneCountInString(l) > maxLineLength {
			l = string([]rune(l)[:maxLineLength]) + "... [truncated]"
		}
		rows[i] = fmt.Sprintf("%d|%s", start+i, l)
	}
	return strings.Join(rows, "\n")
}

// suggestSimilar lists up to five names from the missing file's directory that look like it.
func suggestSimilar(path string) []string {
	dir, name := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	lowerName := strings.ToLower(name)
	ext := strings.ToLower(filepath.Ext(name))
	baseNoExt := strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))
	type scored struct {
		score int
		path  string
	}
	var out []scored
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for i, fn := range names {
		if i >= 50 {
			break
		}
		lf := strings.ToLower(fn)
		score := 0
		switch {
		case lf == lowerName:
			score = 100
		case strings.ToLower(strings.TrimSuffix(fn, filepath.Ext(fn))) == baseNoExt:
			score = 90
		case strings.HasPrefix(lf, lowerName) || strings.HasPrefix(lowerName, lf):
			score = 70
		case strings.Contains(lf, lowerName):
			score = 60
		case strings.Contains(lowerName, lf) && len([]rune(lf)) > 2:
			score = 40
		case ext != "" && strings.ToLower(filepath.Ext(fn)) == ext:
			common := 0
			seen := map[rune]bool{}
			for _, r := range lowerName {
				if strings.ContainsRune(lf, r) && !seen[r] {
					seen[r] = true
					common++
				}
			}
			if float64(common) >= float64(max(len([]rune(lowerName)), len([]rune(lf))))*0.4 {
				score = 30
			}
		}
		if score == 0 && fuzzy.NewSequenceMatcher(lowerName, lf).Ratio() >= 0.8 {
			score = 50
		}
		if score > 0 {
			out = append(out, scored{score, filepath.Join(dir, fn)})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].score > out[j].score })
	var paths []string
	for _, s := range out[:min(5, len(out))] {
		paths = append(paths, s.path)
	}
	return paths
}

func truncateToCharBudget(content string, maxChars int) (string, int) {
	if utf8.RuneCountInString(content) <= maxChars {
		return content, strings.Count(content, "\n") + 1
	}
	lines := strings.Split(content, "\n")
	var kept []string
	running := 0
	for _, l := range lines {
		add := utf8.RuneCountInString(l)
		if len(kept) > 0 {
			add++
		}
		if running+add > maxChars {
			break
		}
		kept = append(kept, l)
		running += add
	}
	if len(kept) == 0 {
		kept = append(kept, string([]rune(lines[0])[:min(maxChars, utf8.RuneCountInString(lines[0]))]))
	}
	return strings.Join(kept, "\n"), len(kept)
}

func readFile(env tools.CallEnv, g guard, args tools.Args) (string, error) {
	rawPath := args.String("path", "")
	offset := max(1, args.Int("offset", 1))
	limit := max(1, min(args.Int("limit", defaultReadLimit), defaultReadLimit))
	if isBlockedDevice(filepath.Clean(expandTilde(rawPath))) {
		return tools.Error(fmt.Sprintf("Cannot read '%s': this is a device file that would block or produce infinite output.", rawPath)), nil
	}
	abs, real := resolve(env, rawPath)
	if isBlockedDevice(real) {
		return tools.Error(fmt.Sprintf("Cannot read '%s': this is a device file that would block or produce infinite output.", rawPath)), nil
	}
	if msg := g.readBlocked(rawPath, real); msg != "" {
		return tools.Error(msg), nil
	}
	if ext := strings.ToLower(filepath.Ext(abs)); binaryExtensions[ext] {
		return tools.Error(fmt.Sprintf("Cannot read binary file '%s' (%s). Use vision_analyze for images, or terminal to inspect binary files.", rawPath, ext)), nil
	}
	fi, statErr := os.Lstat(abs)
	if statErr != nil {
		return obj(readResult("", 0, 0, false, "", false, "File not found: "+abs, true, suggestSimilar(abs), false)...), nil
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		if _, err := os.Stat(abs); err != nil { // dangling
			return obj(f("success", false), f("note", fmt.Sprintf("'%s' is a dangling symlink, not a regular file — no read was attempted.", rawPath))), nil
		}
	}
	files := filesFor(env.SessionID)
	pg, err := readPage(abs, offset, limit)
	if err != nil {
		return tools.Error(err.Error()), nil
	}
	if pg.notRegular {
		kind := "not a regular file"
		if st, e := os.Stat(abs); e == nil && st.IsDir() {
			kind = "a directory"
		}
		return obj(f("success", false), f("note", fmt.Sprintf("'%s' is %s, not a regular file — reading it would block indefinitely or return nothing useful, so no read was "+
			"attempted. Use terminal utilities if you need to interact with it.", rawPath, kind))), nil
	}
	if isLikelyBinary(pg.sample) {
		return obj(readResult("", 0, pg.fileSize, false, "", false, describeBinary(pg.sample, pg.fileSize), false, nil, true)...), nil
	}
	endLine := offset + limit - 1
	truncated := pg.total > endLine
	hint := ""
	if truncated {
		hint = fmt.Sprintf("Use offset=%d to continue reading (showing %d-%d of %d lines)", endLine+1, offset, endLine, pg.total)
	}
	switch {
	case pg.fileSize == 0:
		return obj(readResult("", 0, 0, false, "File is empty (0 bytes).", false, "", false, nil, false)...), nil
	case offset > pg.total && pg.total > 0:
		return obj(readResult("", pg.total, pg.fileSize, false, fmt.Sprintf("Note: offset %d is beyond the end of the file (%d lines total). Retry with offset <= %d.", offset, pg.total, pg.total), false, "", false, nil, false)...), nil
	}
	lines := pg.lines
	if offset == 1 && len(lines) > 0 {
		lines[0] = strings.TrimPrefix(lines[0], "\ufeff")
	}
	truncatedLines := false
	for _, l := range lines {
		if utf8.RuneCountInString(l) > maxLineLength {
			truncatedLines = true
		}
	}
	content := addLineNumbers(lines, offset)
	fields := readResult(content, pg.total, pg.fileSize, truncated, hint, truncatedLines, "", false, nil, false)
	extras := map[string]any{}
	if utf8.RuneCountInString(content) > maxReadChars {
		trimmed, kept := truncateToCharBudget(content, maxReadChars)
		next := offset + kept
		content = trimmed
		fields = readResult(trimmed, pg.total, pg.fileSize, true,
			fmt.Sprintf("Output truncated at the %s-char read budget after %d line(s) (showing lines %d-%d of %d). Use offset=%d to continue.",
				commas(maxReadChars), kept, offset, next-1, pg.total, next), truncatedLines, "", false, nil, false)
		fields = append(fields, f("truncated_by", "bytes"), f("next_offset", next))
		endLine = next - 1
	} else if pg.total > 0 {
		endLine = min(endLine, pg.total)
	}
	if n := countConflictBlocks(content); n > 0 {
		fields = append(fields, f("conflict_blocks", n), f("_hint", fmt.Sprintf("%d unresolved git merge-conflict block(s) (<<<<<<< / ======= / >>>>>>>) in this "+
			"range. Resolve them (keep one side or combine, delete the markers) before editing around them.", n)))
	} else if pg.fileSize > largeFileHint && limit > 200 && truncated {
		fields = append(fields, f("_hint", fmt.Sprintf("This file is large (%s bytes). Consider reading only the section you need with offset and limit to keep context usage efficient.", commas(pg.fileSize))))
	}
	_ = extras
	mt, _, _ := stat(abs)
	key := fmt.Sprintf("%s|%d|%d", real, offset, limit)
	count := files.readRepeat(key, mt == 0)
	files.noteRead(real, offset, endLine, pg.total, truncatedLines)
	if count >= 4 {
		return tools.Error(fmt.Sprintf("BLOCKED: You have read this exact file region %d times in a row. The content has NOT changed. You already have this information. "+
			"STOP re-reading and proceed with your task.", count), tools.Field{Key: "path", Value: rawPath}, tools.Field{Key: "already_read", Value: count}), nil
	}
	if count >= 3 {
		fields = append(fields, f("_warning", fmt.Sprintf("You have read this exact file region %d times consecutively. The content has not changed since your last read. "+
			"Use the information you already have. If you are stuck in a loop, stop reading and proceed with writing or responding.", count)))
	}
	return obj(fields...), nil
}

func commas(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// ---- writing ---------------------------------------------------------------------------

// atomicWrite writes content next to path and renames it over, keeping the mode of an existing file and writing
// through a symlink to its target.
func atomicWrite(path, content string) error {
	target := path
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		if r, err := filepath.EvalSymlinks(path); err == nil {
			target = r
		}
	}
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".hermes-tmp.*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // removed unless the rename below succeeded
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(target); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), target)
}

// osFS is the patch engine's view of the real filesystem.
type osFS struct{ env tools.CallEnv }

func (o *osFS) abs(p string) string { a, _ := resolve(o.env, p); return a }

func (o *osFS) ReadRaw(p string) (string, error) {
	path := o.abs(p)
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", &NotFoundError{p}
		}
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", errors.New("Cannot read '" + p + "': not a regular file (directory, dangling symlink, FIFO, socket, or device). Reading it could block indefinitely.")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("Failed to read file: " + err.Error())
	}
	if isLikelyBinary(data[:min(len(data), 1000)]) {
		return "", fmt.Errorf("%s", describeBinary(data[:min(len(data), 1000)], len(data)))
	}
	return strings.TrimPrefix(string(data), "\ufeff"), nil
}

func (o *osFS) Write(p, content string) error { return writeText(o.abs(p), content) }
func (o *osFS) Delete(p string) error         { return os.Remove(o.abs(p)) }
func (o *osFS) Move(src, dst string) error {
	d := o.abs(dst)
	if err := os.MkdirAll(filepath.Dir(d), 0o755); err != nil {
		return err
	}
	return os.Rename(o.abs(src), d)
}

// writeText writes text preserving the file's BOM and CRLF line endings (the model sends bare LF and read_file
// strips the BOM, so a round trip would otherwise change both).
func writeText(path, content string) error {
	if old, err := os.ReadFile(path); err == nil {
		hasBOM := bytes.HasPrefix(old, []byte("\ufeff"))
		if detectLineEnding(string(old[:min(len(old), 4096)])) == "\r\n" {
			content = normalizeLineEndings(content, "\r\n")
		}
		if hasBOM && !strings.HasPrefix(content, "\ufeff") {
			content = "\ufeff" + content
		}
	}
	return atomicWrite(path, content)
}

func detectLineEnding(sample string) string {
	crlf := strings.Count(sample, "\r\n")
	lf := strings.Count(sample, "\n") - crlf
	if crlf > 0 && crlf >= lf {
		return "\r\n"
	}
	return ""
}

func normalizeLineEndings(text, target string) string {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	if target == "\r\n" {
		return strings.ReplaceAll(text, "\n", "\r\n")
	}
	return text
}

// syntaxError is the fail-closed gate for structured formats: a write that does not parse is a corrupt write.
func syntaxError(ext, content string) string {
	switch ext {
	case ".json":
		var v any
		if err := json.Unmarshal([]byte(content), &v); err != nil {
			return err.Error()
		}
	}
	return ""
}

var lineNumberedRe = regexp.MustCompile(`^\s*(\d+)\|`)

// looksLikeReadOutput is true for content dominated by read_file's LINE_NUM|CONTENT display: writing it back
// would bake the line numbers into the file.
func looksLikeReadOutput(content string) bool {
	var lines []string
	for _, l := range strings.Split(content, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) < 2 {
		return false
	}
	var nums []int
	for _, l := range lines {
		if m := lineNumberedRe.FindStringSubmatch(l); m != nil {
			n := 0
			fmt.Sscan(m[1], &n)
			nums = append(nums, n)
		}
	}
	if len(nums) < 2 || float64(len(nums))/float64(len(lines)) < 0.6 {
		return false
	}
	consecutive := 0
	for i := 1; i < len(nums); i++ {
		if nums[i] == nums[i-1]+1 {
			consecutive++
		}
	}
	return consecutive >= len(nums)-1
}
