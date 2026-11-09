package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/phzietsman/hermes-agent/internal/memory/sam"
	"github.com/phzietsman/hermes-agent/internal/session"
)

const (
	entryDelimiter = "\n§\n"
	ledgerFile     = "agent-memory-import.json"
)

// ImportEntry is one item of the builtin memory files.
type ImportEntry struct {
	Source, Category, Text, Digest string
}

// ImportReport counts what an import did.
type ImportReport struct{ Planned, Written, Skipped, Failed int }

func entryDigest(kind, text string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + text))
	return hex.EncodeToString(sum[:])[:16]
}

// ReadEntries reads MEMORY.md (learned knowledge) and USER.md (who the user is) from <home>/memories, entries
// joined by "\n§\n" as the builtin store wrote them.
func ReadEntries(home string) []ImportEntry {
	var out []ImportEntry
	for _, f := range []struct{ source, file, category string }{{"memory", "MEMORY.md", "knowledge"}, {"user", "USER.md", "identity"}} {
		raw, err := os.ReadFile(filepath.Join(home, "memories", f.file))
		if err != nil {
			continue
		}
		for _, e := range strings.Split(string(raw), entryDelimiter) {
			if text := strings.TrimSpace(e); text != "" {
				out = append(out, ImportEntry{f.source, f.category, text, entryDigest(f.source, text)})
			}
		}
	}
	return out
}

// ledger remembers which digests were imported, so a second run writes nothing.
type ledger struct {
	path string
	done map[string]bool
}

func loadLedger(path string) *ledger {
	l := &ledger{path: path, done: map[string]bool{}}
	if raw, err := os.ReadFile(path); err == nil {
		var d struct {
			Digests []string `json:"digests"`
		}
		if json.Unmarshal(raw, &d) == nil {
			for _, x := range d.Digests {
				l.done[x] = true
			}
		}
	}
	return l
}

func (l *ledger) add(digest string) error {
	l.done[digest] = true
	ds := make([]string, 0, len(l.done))
	for d := range l.done {
		ds = append(ds, d)
	}
	sort.Strings(ds)
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(l.path), ".agent-memory-import-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := json.NewEncoder(tmp).Encode(map[string]any{"digests": ds}); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), l.path)
}

func preview(text string) string {
	one := strings.Join(strings.Fields(text), " ")
	if r := []rune(one); len(r) > 90 {
		return string(r[:89]) + "…"
	}
	return one
}

// ImportOptions tune an import.
type ImportOptions struct {
	DryRun bool
	// Rate pauses between writes.
	Rate time.Duration
	Out  func(string)
}

func (o ImportOptions) say(format string, a ...any) {
	if o.Out != nil {
		o.Out(fmt.Sprintf(format, a...))
	}
}

// ImportMemoryFiles writes MEMORY.md and USER.md into memory. Idempotent: each entry's digest is recorded as it
// is written, so an interrupted run resumes where it stopped.
func ImportMemoryFiles(ctx context.Context, c *sam.Client, home string, s *sam.Settings, o ImportOptions) ImportReport {
	var r ImportReport
	led := loadLedger(filepath.Join(home, ledgerFile))
	for _, e := range ReadEntries(home) {
		if led.done[e.Digest] {
			r.Skipped++
			continue
		}
		r.Planned++
		if o.DryRun {
			o.say("  would import [%s -> %s] %s", e.Source, e.Category, preview(e.Text))
			continue
		}
		_, err := c.Remember(ctx, e.Text, sam.RememberOptions{Category: e.Category, Scopes: s.DefaultScope,
			Labels: []string{"origin=import", "source=" + e.Source, "digest=" + e.Digest}})
		if err != nil {
			r.Failed++
			o.say("  ✗ [%s] %s: %v", e.Source, preview(e.Text), err)
			continue
		}
		if err := led.add(e.Digest); err != nil {
			r.Failed++
			o.say("  ✗ ledger not saved: %v", err)
			continue
		}
		r.Written++
		sleepCtx(ctx, o.Rate)
	}
	return r
}

func sleepCtx(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// ImportSessions backfills the user/assistant text of the most recent limit sessions.
func ImportSessions(ctx context.Context, c *sam.Client, store *session.Store, home string, s *sam.Settings, limit int, o ImportOptions) (ImportReport, error) {
	var r ImportReport
	led := loadLedger(filepath.Join(home, ledgerFile))
	rows, err := store.RecentSessions(ctx, limit, "")
	if err != nil {
		return r, err
	}
	for _, sess := range rows {
		msgs, err := store.Messages(ctx, sess.ID, session.ListOptions{})
		if err != nil {
			return r, err
		}
		var items []sam.Message
		h := newDigest()
		for _, m := range msgs {
			content := "None"
			if m.Content != "" {
				content = m.Content
			}
			h.add(m.Role, content)
			items = append(items, sam.Message{Role: m.Role, Content: m.Content})
		}
		items = direct(items)
		digest := h.sum()
		if len(items) == 0 {
			continue
		}
		if led.done[digest] {
			r.Skipped++
			continue
		}
		r.Planned++
		if o.DryRun {
			o.say("  would import session %s (%d messages)", sess.ID, len(items))
			continue
		}
		if _, err := c.RememberMany(ctx, items, "", s.DefaultScope, []string{"origin=import-session", "digest=" + digest, "session=" + sess.ID}); err != nil {
			r.Failed++
			o.say("  ✗ session %s: %v", sess.ID, err)
			continue
		}
		if err := led.add(digest); err != nil {
			r.Failed++
			continue
		}
		r.Written++
		sleepCtx(ctx, o.Rate)
	}
	return r, nil
}
