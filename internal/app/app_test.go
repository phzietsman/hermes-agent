package app

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phzietsman/hermes-agent/internal/agent"
	"github.com/phzietsman/hermes-agent/internal/config"
	"github.com/phzietsman/hermes-agent/internal/llm"
)

type script struct {
	mu    sync.Mutex
	steps []llm.Response
	calls []agent.CallInput
}

func (s *script) Complete(_ context.Context, in agent.CallInput, _ func(string)) (llm.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.calls)
	s.calls = append(s.calls, in)
	if n >= len(s.steps) {
		t := "exhausted"
		return llm.Response{Content: &t, FinishReason: "stop"}, nil
	}
	return s.steps[n], nil
}

func say(t string) llm.Response {
	return llm.Response{Content: &t, FinishReason: "stop", StopReason: "end_turn"}
}

func TestNewSessionIDFormat(t *testing.T) {
	id := NewSessionID(time.Date(2026, 6, 1, 8, 5, 3, 0, time.UTC))
	if !regexp.MustCompile(`^20260601_080503_[0-9a-f]{6}$`).MatchString(id) {
		t.Errorf("id %q", id)
	}
	now := time.Now()
	if a, b := NewSessionID(now), NewSessionID(now); a == b {
		t.Error("ids collide")
	}
}

func TestDelegatedChildIsALeafInItsOwnSessionUnderTheParent(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	ctx := context.Background()
	home := t.TempDir()
	args, _ := json.Marshal(map[string]any{"tasks": []any{map[string]any{"goal": "count the files"}}})
	p := &script{steps: []llm.Response{
		{FinishReason: "tool_calls", StopReason: "tool_use", ToolCalls: []llm.ToolCall{{ID: "c1", Type: "function",
			Function: llm.ToolCallFunction{Name: "delegate_task", Arguments: string(args)}}}},
		say("There are 3 files."), // the child's answer
		say("The helper found 3 files."),
	}}
	rt, err := Open(config.WithHome(ctx, home), home, Options{Provider: p})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	s, err := rt.NewSession(ctx, SessionOptions{Source: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Agent.RunTurn(ctx, "delegate counting")
	if err != nil || !res.Completed || res.FinalResponse != "The helper found 3 files." {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	names := func(in agent.CallInput) map[string]bool {
		m := map[string]bool{}
		for _, d := range in.Tools {
			m[d.Name] = true
		}
		return m
	}
	if !names(p.calls[0])["delegate_task"] {
		t.Fatal("the parent should be offered delegate_task")
	}
	child := names(p.calls[1])
	if child["delegate_task"] || len(child) == 0 {
		t.Errorf("child tools %v: must be non-empty and lack delegate_task", child)
	}
	if sys := p.calls[1].Messages[0].Content.AsText(); !strings.Contains(sys, "focused subagent") {
		t.Errorf("child system prompt: %q", sys)
	}
	rows, err := rt.Store.RecentSessions(ctx, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, r := range rows {
		if r.Source == "delegate" && r.ParentSessionID == s.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("no delegate session under %s: %+v", s.ID, rows)
	}
}

func TestResumeKeepsTheStoredSystemPrompt(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	ctx := context.Background()
	home := t.TempDir()
	p := &script{steps: []llm.Response{say("a"), say("b")}}
	rt, err := Open(config.WithHome(ctx, home), home, Options{Provider: p})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	s1, _ := rt.NewSession(ctx, SessionOptions{})
	if _, err := s1.Agent.RunTurn(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	// Change what the prompt depends on, then resume: the stored prompt wins.
	if err := writeFile(home+"/SOUL.md", "You are a pirate."); err != nil {
		t.Fatal(err)
	}
	s2, _ := rt.NewSession(ctx, SessionOptions{ID: s1.ID})
	if _, err := s2.Agent.RunTurn(ctx, "two"); err != nil {
		t.Fatal(err)
	}
	if p.calls[0].Messages[0].Content.AsText() != p.calls[1].Messages[0].Content.AsText() {
		t.Error("resume rebuilt the system prompt")
	}
}

func writeFile(path, s string) error { return os.WriteFile(path, []byte(s), 0o644) }
