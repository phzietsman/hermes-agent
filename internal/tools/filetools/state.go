package filetools

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"sync"
)

func sha(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// fileView is what a session has seen of a file.
type fileView struct {
	mtimeNs   int64
	size      int64
	totalLine int
	covered   [][2]int // line ranges read, merged
	baseline  string   // sha256 of the whole content this session last read in full or wrote; "" when none
	blind     bool     // a patch changed the file without a prior full view
	lossy     bool     // some page was clamped or truncated, so the pages do not add up to the file
}

func (v *fileView) addRange(from, to int) {
	v.covered = append(v.covered, [2]int{from, to})
}

// fullyRead reports whether the recorded pages cover lines 1..totalLine.
func (v *fileView) fullyRead() bool {
	if v.totalLine <= 0 {
		return false
	}
	next := 1
	for changed := true; changed; {
		changed = false
		for _, r := range v.covered {
			if r[0] <= next && r[1] >= next {
				next = r[1] + 1
				changed = true
			}
		}
	}
	return next > v.totalLine
}

// sessionFiles tracks reads and writes for one session: the read-before-overwrite guard and repeated-read
// detection.
type sessionFiles struct {
	mu        sync.Mutex
	views     map[string]*fileView
	lastRead  map[string]int // identical consecutive reads, keyed by path|offset|limit
	lastKey   string
	patchFail map[string]int
}

var (
	sessionsMu sync.Mutex
	sessions   = map[string]*sessionFiles{}
)

func filesFor(session string) *sessionFiles {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	s := sessions[session]
	if s == nil {
		s = &sessionFiles{views: map[string]*fileView{}, lastRead: map[string]int{}, patchFail: map[string]int{}}
		sessions[session] = s
	}
	return s
}

// ForgetSession drops the file state of a finished session.
func ForgetSession(session string) {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	delete(sessions, session)
}

func stat(path string) (mtimeNs, size int64, ok bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, 0, false
	}
	return fi.ModTime().UnixNano(), fi.Size(), true
}

func (s *sessionFiles) view(resolved string) *fileView {
	v := s.views[resolved]
	if v == nil {
		v = &fileView{}
		s.views[resolved] = v
	}
	return v
}

// noteRead records a page that was read.
func (s *sessionFiles) noteRead(resolved string, offset, endLine, totalLines int, lossy bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.view(resolved)
	mt, sz, _ := stat(resolved)
	if v.mtimeNs != mt || v.size != sz {
		v.covered, v.lossy = nil, false // the file changed under us: earlier pages describe another version
	}
	v.lossy = v.lossy || lossy
	v.mtimeNs, v.size, v.totalLine = mt, sz, totalLines
	v.addRange(offset, endLine)
	if v.fullyRead() && !v.lossy {
		if data, err := os.ReadFile(resolved); err == nil {
			v.baseline, v.blind = sha(string(data)), false
		}
	}
}

// noteWrite records that this session wrote the whole content (or patched with a prior full view).
func (s *sessionFiles) noteWrite(resolved, content string, blind bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.view(resolved)
	mt, sz, _ := stat(resolved)
	v.mtimeNs, v.size = mt, sz
	v.covered = nil
	if blind {
		v.baseline, v.blind = "", true
		return
	}
	v.baseline, v.blind = sha(content), false
}

func (s *sessionFiles) hasBaseline(resolved string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.views[resolved]
	return v != nil && v.baseline != ""
}

// staleOverwrite is the reason a whole-file overwrite must not replace an existing file, or "". Net-new files
// pass; an existing file needs a full current view (read in full, or written by this session), and the disk must
// not have changed since.
func (s *sessionFiles) staleOverwrite(display, resolved string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(resolved)
	if err != nil {
		return "" // absent or unreadable: nothing to clobber
	}
	v := s.views[resolved]
	if v != nil && v.baseline != "" {
		if sha(string(data)) == v.baseline {
			return ""
		}
		return display + " was modified since you last read it (external edit or concurrent agent). Re-read the file before writing."
	}
	if v != nil && v.blind {
		return "Your patch changed this file without a full view of its current content. Read the current file " +
			"in full before a whole-file overwrite, or continue with targeted patches."
	}
	return resolved + " exists but this task has not seen its full current content " +
		"(never read, patched without a prior full read, or only a redacted/partial view). Read the " +
		"file — every page of it, if it needs offset/limit — or use patch for a " +
		"targeted edit; a stale conversation copy must not overwrite the current " +
		"disk content."
}

// staleWarning warns (without refusing) when a file changed since this session last read it.
func (s *sessionFiles) staleWarning(display, resolved string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.views[resolved]
	if v == nil || v.mtimeNs == 0 {
		return ""
	}
	if mt, _, ok := stat(resolved); ok && mt != v.mtimeNs {
		return display + " was modified since you last read it (external edit or concurrent agent). Re-read the file before editing."
	}
	return ""
}

// readRepeat counts consecutive identical reads of the same region.
func (s *sessionFiles) readRepeat(key string, changed bool) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if changed || s.lastKey != key {
		s.lastRead = map[string]int{}
	}
	s.lastKey = key
	s.lastRead[key]++
	return s.lastRead[key]
}

func (s *sessionFiles) recordPatchFailure(resolved string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.patchFail[resolved]++
	return s.patchFail[resolved]
}

func (s *sessionFiles) resetPatchFailures(resolved string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.patchFail, resolved)
}
