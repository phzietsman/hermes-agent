package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phzietsman/hermes-agent/internal/config"
	"github.com/phzietsman/hermes-agent/internal/learning"
	"github.com/phzietsman/hermes-agent/internal/llm"
	"github.com/phzietsman/hermes-agent/internal/session"
)

func tc(id, name string, args map[string]any) llm.ToolCall {
	b, _ := json.Marshal(args)
	return llm.ToolCall{ID: id, Type: "function", Function: llm.ToolCallFunction{Name: name, Arguments: string(b)}}
}

func memHome(t *testing.T) (string, *sync.Mutex, *[]string) {
	var mu sync.Mutex
	var reqs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		reqs = append(reqs, r.URL.Path+" "+string(b))
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/profile") {
			io.WriteString(w, `{"instructions":[],"preferences":[],"static":[]}`)
			return
		}
		io.WriteString(w, `{"hits":[]}`)
	}))
	t.Cleanup(srv.Close)
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "agent-memory.json"), []byte(`{"endpoint":"`+srv.URL+`","context":"c","max_retries":0}`), 0o600)
	os.WriteFile(filepath.Join(home, ".env"), []byte("AGENT_MEMORY_API_KEY=k\n"), 0o600)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("AGENT_MEMORY_API_KEY", "")
	return home, &mu, &reqs
}

func TestReviewForkSharesTheParentPrefixAndMayOnlySaveAndRead(t *testing.T) {
	home, mu, reqs := memHome(t)
	p := &script{steps: []llm.Response{
		say("parent answer"), // the parent's one turn
		{FinishReason: "tool_calls", StopReason: "tool_use", ToolCalls: []llm.ToolCall{
			tc("r1", "agent_memory_remember", map[string]any{"text": "user prefers Go", "kind": "preference"}),
			tc("r2", "terminal", map[string]any{"command": "echo hi"}),
			tc("r3", "skill_manage", map[string]any{"action": "delete", "name": "x"}),
			tc("r4", "agent_memory_forget", map[string]any{"query": "everything"}),
		}},
		say("Nothing more to save."),
	}}
	ctx := context.Background()
	rt, err := Open(config.WithHome(ctx, home), home, Options{Provider: p})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	s, _ := rt.NewSession(ctx, SessionOptions{})
	if _, err := s.Agent.RunTurn(ctx, "I always use Go for services"); err != nil {
		t.Fatal(err)
	}
	host := &reviewHost{rt: rt, parent: s.ID}
	out := host.run(ctx, s.Agent.History())
	if out != learning.Done {
		t.Fatalf("outcome %s", out)
	}

	parent, fork := p.calls[0], p.calls[1]
	if parent.Messages[0].Content.AsText() != fork.Messages[0].Content.AsText() {
		t.Error("the fork must read the parent's system prompt byte for byte (shared cache prefix)")
	}
	if len(parent.Tools) != len(fork.Tools) {
		t.Errorf("the fork advertises %d tools, the parent %d: the tools array must be identical", len(fork.Tools), len(parent.Tools))
	}
	last := fork.Messages[len(fork.Messages)-1].Content.AsText()
	if !strings.Contains(last, "Review the conversation above") || !strings.Contains(last, "agent_memory_remember") {
		t.Errorf("the review prompt was not the final user message: %.200s", last)
	}
	if fork.Messages[1].Content.AsText() != "I always use Go for services" {
		t.Errorf("the fork should replay the parent's conversation first: %q", fork.Messages[1].Content.AsText())
	}

	results := map[string]string{}
	for _, m := range p.calls[2].Messages {
		if m.Role == llm.RoleTool {
			results[m.ToolCallID] = m.Content.AsText()
		}
	}
	if !strings.Contains(results["r1"], "queued") {
		t.Errorf("remember should be allowed: %q", results["r1"])
	}
	for id, want := range map[string]string{"r2": "not available in a background review", "r3": "never deletes a skill", "r4": "not available in a background review"} {
		if !strings.Contains(results[id], want) {
			t.Errorf("%s: %q", id, results[id])
		}
	}

	// The write landed in memory, labelled as a review's; the fork itself synced nothing.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := strings.Join(*reqs, "\n")
		mu.Unlock()
		if strings.Contains(got, "origin=background_review") {
			if strings.Contains(got, "everything") {
				t.Error("a review forgot memory")
			}
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	joined := strings.Join(*reqs, "\n")
	mu.Unlock()
	if !strings.Contains(joined, "origin=background_review") {
		t.Errorf("the review's write never reached memory: %s", joined)
	}
	rows, _ := rt.Store.RecentSessions(ctx, 10, "")
	var found bool
	for _, r := range rows {
		if r.Source == "review" && r.ParentSessionID == s.ID {
			found = true
		}
	}
	if !found {
		t.Error("the fork has no session under its parent")
	}
}

func TestSessionEndQueuesAReviewForTheNextProcess(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	home := t.TempDir()
	p := &script{steps: []llm.Response{say("hi there")}}
	ctx := context.Background()
	rt, err := Open(config.WithHome(ctx, home), home, Options{Provider: p})
	if err != nil {
		t.Fatal(err)
	}
	s, _ := rt.NewSession(ctx, SessionOptions{})
	s.Agent.RunTurn(ctx, "hello")
	s.End(ctx)
	time.Sleep(100 * time.Millisecond)
	if len(p.calls) != 1 {
		t.Errorf("a one-shot process must not start a review at exit: %d model calls", len(p.calls))
	}
	rt.Close()

	// The queue outlives the process.
	st, err := session.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	q, _ := learning.NewSQLQueue(ctx, st.DB())
	j, _ := q.Claim(ctx, time.Now())
	if j == nil || j.SessionID != s.ID || j.Trigger != learning.TriggerSessionEnd {
		t.Fatalf("queued job %+v", j)
	}
}

func TestReadOnlySessionsAndDisabledConfigNeverReview(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "config.yaml"), []byte("auxiliary:\n  background_review:\n    enabled: false\n"), 0o644)
	ctx := context.Background()
	rt, err := Open(config.WithHome(ctx, home), home, Options{Provider: &script{}})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if s, _ := rt.NewSession(ctx, SessionOptions{}); s.learn != nil {
		t.Error("reviews disabled in config")
	}
	home2 := t.TempDir()
	rt2, _ := Open(config.WithHome(ctx, home2), home2, Options{Provider: &script{}})
	defer rt2.Close()
	if s, _ := rt2.NewSession(ctx, SessionOptions{ReadOnly: true}); s.learn != nil {
		t.Error("a read-only (cron/subagent) session must not review")
	}
	if s, _ := rt2.NewSession(ctx, SessionOptions{}); s.learn == nil {
		t.Error("a normal session reviews by default")
	}
}

func TestLearningSettingsFromConfig(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "config.yaml"), []byte("memory:\n  nudge_interval: 7\nlearning:\n  idle_seconds: 30\n  max_attempts: 2\n  review_on_boundary: false\n"), 0o644)
	ctx := context.Background()
	rt, _ := Open(config.WithHome(ctx, home), home, Options{Provider: &script{}})
	defer rt.Close()
	s := rt.learningSettings()
	if s.IterationInterval != 7 || s.IdleSeconds != 30 || s.MaxAttempts != 2 || s.ReviewOnBoundary || s.BackoffBase != 30*time.Second {
		t.Errorf("%+v", s)
	}
	os.WriteFile(filepath.Join(home, "config.yaml"), []byte("skills:\n  creation_nudge_interval: 4\nmemory:\n  nudge_interval: 7\nlearning:\n  iteration_interval: 0\n"), 0o644)
	rt2, _ := Open(config.WithHome(ctx, home), home, Options{Provider: &script{}})
	defer rt2.Close()
	if got := rt2.learningSettings().IterationInterval; got != 0 {
		t.Errorf("learning.iteration_interval overrides the aliases: %d", got)
	}
}
