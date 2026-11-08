package session

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

var ctx = context.Background()

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func mustSession(t *testing.T, s *Store, id string) {
	t.Helper()
	if err := s.CreateSession(ctx, NewSession{ID: id, Source: "cli", Model: "m"}); err != nil {
		t.Fatal(err)
	}
}

func TestOpenIsIdempotentAndWAL(t *testing.T) {
	dir := t.TempDir()
	s1, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	mustSession(t, s1, "a")
	var mode string
	if err := s1.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, %v", mode, err)
	}
	var fk int
	if err := s1.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys = %d, %v", fk, err)
	}
	s1.Close()

	s2, err := Open(dir) // reopening must keep data and not duplicate the version row
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if _, err := s2.GetSession(ctx, "a"); err != nil {
		t.Fatalf("data lost on reopen: %v", err)
	}
	var n int
	if err := s2.db.QueryRow(`SELECT count(*) FROM schema_version`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("schema_version rows = %d, %v", n, err)
	}
}

func TestOpenRefusesForeignSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`CREATE TABLE schema_version (version INTEGER NOT NULL)`, `INSERT INTO schema_version VALUES (31)`} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	raw.Close()
	if _, err := OpenPath(path); !errors.Is(err, ErrForeignSchema) {
		t.Fatalf("err = %v, want ErrForeignSchema", err)
	}
}

func TestCreateAndGetSession(t *testing.T) {
	s := open(t)
	base := time.Unix(1_700_000_000, 0)
	s.now = func() time.Time { return base }

	if err := s.CreateSession(ctx, NewSession{ID: "s1", Source: "telegram", Model: "claude", Cwd: "/w", UserID: "u", ChatID: "c", ProfileName: "default"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSession(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "telegram" || got.Model != "claude" || got.Cwd != "/w" || got.UserID != "u" || got.ChatID != "c" || got.ProfileName != "default" {
		t.Fatalf("fields lost: %+v", got)
	}
	if got.StartedAt != float64(base.Unix()) || got.EndedAt != nil || got.Title != "" || got.ParentSessionID != "" {
		t.Fatalf("defaults wrong: %+v", got)
	}

	// Idempotent: a second create with different values leaves the first untouched.
	if err := s.CreateSession(ctx, NewSession{ID: "s1", Source: "cli", Model: "other"}); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetSession(ctx, "s1"); got.Source != "telegram" || got.Model != "claude" {
		t.Fatalf("second create overwrote the row: %+v", got)
	}

	if _, err := s.GetSession(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	for _, bad := range []NewSession{{Source: "cli"}, {ID: "x"}} {
		if err := s.CreateSession(ctx, bad); err == nil {
			t.Fatalf("CreateSession(%+v) should fail", bad)
		}
	}
	// A parent must exist (foreign key).
	if err := s.CreateSession(ctx, NewSession{ID: "child", Source: "cli", ParentSessionID: "missing"}); err == nil {
		t.Fatal("dangling parent accepted")
	}
	mustSession(t, s, "parent")
	if err := s.CreateSession(ctx, NewSession{ID: "child2", Source: "cli", ParentSessionID: "parent"}); err != nil {
		t.Fatal(err)
	}
}

func TestSystemPromptIsStoredOnce(t *testing.T) {
	s := open(t)
	mustSession(t, s, "a")
	mustSession(t, s, "b")

	set, err := s.SetSystemPrompt(ctx, "a", "You are Hermes.")
	if err != nil || !set {
		t.Fatalf("first set = %v, %v", set, err)
	}
	// A rebuilt prompt must never replace the stored one: that would break the prompt cache.
	set, err = s.SetSystemPrompt(ctx, "a", "You are Hermes. Now with new memories.")
	if err != nil || set {
		t.Fatalf("second set = %v, %v; want false, nil", set, err)
	}
	a, _ := s.GetSession(ctx, "a")
	if a.SystemPrompt != "You are Hermes." || a.SystemPromptHash == "" {
		t.Fatalf("prompt changed or hash missing: %+v", a)
	}

	// The same text on another session shares one system_prompts row.
	if _, err := s.SetSystemPrompt(ctx, "b", "You are Hermes."); err != nil {
		t.Fatal(err)
	}
	b, _ := s.GetSession(ctx, "b")
	if b.SystemPromptHash != a.SystemPromptHash {
		t.Fatal("same prompt, different hash")
	}
	var rows int
	if err := s.db.QueryRow(`SELECT count(*) FROM system_prompts`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("system_prompts rows = %d, %v", rows, err)
	}

	if _, err := s.SetSystemPrompt(ctx, "missing", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestEndSessionFirstReasonWinsAndReopen(t *testing.T) {
	s := open(t)
	mustSession(t, s, "a")
	if err := s.EndSession(ctx, "a", "compression"); err != nil {
		t.Fatal(err)
	}
	if err := s.EndSession(ctx, "a", "user_exit"); err != nil { // stale end must not clobber it
		t.Fatal(err)
	}
	got, _ := s.GetSession(ctx, "a")
	if got.EndReason != "compression" || got.EndedAt == nil {
		t.Fatalf("end state = %q, %v", got.EndReason, got.EndedAt)
	}
	if err := s.Reopen(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetSession(ctx, "a"); got.EndedAt != nil || got.EndReason != "" {
		t.Fatalf("reopen did not clear: %+v", got)
	}
	if err := s.EndSession(ctx, "a", "user_exit"); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetSession(ctx, "a"); got.EndReason != "user_exit" {
		t.Fatalf("re-end after reopen = %q", got.EndReason)
	}
}

func TestTitleAndUsage(t *testing.T) {
	s := open(t)
	mustSession(t, s, "a")
	if err := s.SetTitle(ctx, "a", "Plan the port"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTitle(ctx, "missing", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := s.AddUsage(ctx, "a", Usage{Input: 100, Output: 10, CacheRead: 50, CacheWrite: 5, Reasoning: 3}); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := s.GetSession(ctx, "a")
	if got.Title != "Plan the port" || got.InputTokens != 200 || got.OutputTokens != 20 || got.CacheReadTokens != 100 ||
		got.CacheWriteTokens != 10 || got.ReasoningTokens != 6 || got.APICallCount != 2 {
		t.Fatalf("accounting wrong: %+v", got)
	}
}

func TestConcurrentAppendsAreAllStored(t *testing.T) {
	s := open(t)
	mustSession(t, s, "a")
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				if _, err := s.AppendMessage(ctx, Message{SessionID: "a", Role: "user", Content: "hello"}); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	got, _ := s.GetSession(ctx, "a")
	msgs, _ := s.Messages(ctx, "a", ListOptions{})
	if got.MessageCount != 200 || len(msgs) != 200 {
		t.Fatalf("count = %d, rows = %d, want 200", got.MessageCount, len(msgs))
	}
}

func TestRecentSessionsNewestFirstSkipsExcludedAndEmpty(t *testing.T) {
	ctx := context.Background()
	st := open(t)
	for _, id := range []string{"a", "b", "c", "empty"} {
		if err := st.CreateSession(ctx, NewSession{ID: id, Source: "cli"}); err != nil {
			t.Fatal(err)
		}
		if id != "empty" {
			if _, err := st.AppendMessage(ctx, Message{SessionID: id, Role: "user", Content: "hi"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	got, err := st.RecentSessions(ctx, 10, "b")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range got {
		ids = append(ids, s.ID)
	}
	if len(ids) != 2 || ids[0] != "c" || ids[1] != "a" {
		t.Errorf("RecentSessions = %v, want [c a]", ids)
	}
}

func TestLatestBySessionKey(t *testing.T) {
	ctx := context.Background()
	st := open(t)
	st.CreateSession(ctx, NewSession{ID: "a", Source: "telegram", SessionKey: "telegram:1"})
	st.CreateSession(ctx, NewSession{ID: "b", Source: "telegram", SessionKey: "telegram:1"})
	st.CreateSession(ctx, NewSession{ID: "c", Source: "telegram", SessionKey: "telegram:2"})
	if id, err := st.LatestBySessionKey(ctx, "telegram:1"); err != nil || id != "b" {
		t.Errorf("%q %v", id, err)
	}
	if id, _ := st.LatestBySessionKey(ctx, "nope"); id != "" {
		t.Errorf("%q", id)
	}
}
