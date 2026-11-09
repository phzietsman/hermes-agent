package session

import "testing"

func TestReplaceLiveArchivesAndKeepsSessionID(t *testing.T) {
	s := open(t)
	mustSession(t, s, "a")
	mustSession(t, s, "other")
	for _, m := range []Message{{Role: "user", Content: "old question about zebras"}, {Role: "assistant", Content: "old answer"}} {
		m.SessionID = "a"
		if _, err := s.AppendMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AppendMessage(ctx, Message{SessionID: "other", Role: "user", Content: "untouched"}); err != nil {
		t.Fatal(err)
	}

	err := s.ReplaceLive(ctx, "a", []Message{
		{Role: "user", Content: "[summary] zebras discussed", CompressedSummary: true},
		{Role: "assistant", Content: "continuing"},
	})
	if err != nil {
		t.Fatal(err)
	}
	live, _ := s.Messages(ctx, "a", ListOptions{})
	if len(live) != 2 || !live[0].CompressedSummary || live[1].Content != "continuing" {
		t.Fatalf("live = %+v", live)
	}
	all, _ := s.Messages(ctx, "a", ListOptions{IncludeCompacted: true})
	if len(all) != 4 {
		t.Fatalf("with archived rows: %d", len(all))
	}
	archived := 0
	for _, m := range all {
		if m.Compacted && !m.Active {
			archived++
		}
	}
	if archived != 2 {
		t.Fatalf("%d archived", archived)
	}
	// Archived rows are still found by search, and another session is untouched.
	hits, err := s.Search(ctx, SearchQuery{Query: "zebras"})
	if err != nil || len(hits) == 0 {
		t.Fatalf("archived message not searchable: %v %v", hits, err)
	}
	if o, _ := s.Messages(ctx, "other", ListOptions{}); len(o) != 1 {
		t.Fatal("another session was modified")
	}
	if sess, _ := s.GetSession(ctx, "a"); sess.MessageCount != 2 {
		t.Fatalf("message_count = %d", sess.MessageCount)
	}
}

func TestReplaceLiveIsAtomic(t *testing.T) {
	s := open(t)
	mustSession(t, s, "a")
	if _, err := s.AppendMessage(ctx, Message{SessionID: "a", Role: "user", Content: "keep me"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceLive(ctx, "a", []Message{{Role: "user", Content: "ok"}, {Role: "", Content: "invalid"}}); err == nil {
		t.Fatal("want an error for a row without a role")
	}
	live, _ := s.Messages(ctx, "a", ListOptions{})
	if len(live) != 1 || live[0].Content != "keep me" {
		t.Fatalf("a failed replace changed the transcript: %+v", live)
	}
	if err := s.ReplaceLive(ctx, "missing", nil); err != ErrNotFound {
		t.Fatalf("err = %v", err)
	}
}

func TestRewriteSystemPrompt(t *testing.T) {
	s := open(t)
	mustSession(t, s, "a")
	if _, err := s.SetSystemPrompt(ctx, "a", "first"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.SetSystemPrompt(ctx, "a", "second"); ok {
		t.Fatal("SetSystemPrompt must stay write-once")
	}
	if err := s.RewriteSystemPrompt(ctx, "a", "rebuilt"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetSession(ctx, "a"); got.SystemPrompt != "rebuilt" || got.SystemPromptHash == "" {
		t.Fatalf("%+v", got)
	}
	if err := s.RewriteSystemPrompt(ctx, "missing", "x"); err != ErrNotFound {
		t.Fatalf("err = %v", err)
	}
}
