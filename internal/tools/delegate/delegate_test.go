package delegate

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phzietsman/hermes-agent/internal/agent"
	"github.com/phzietsman/hermes-agent/internal/llm"
	"github.com/phzietsman/hermes-agent/internal/session"
	"github.com/phzietsman/hermes-agent/internal/tools"
)

// echoProvider answers every call with "done: <last user message>", or blocks until cancelled.
type echoProvider struct {
	block   bool
	started chan struct{}
}

func (p *echoProvider) Complete(ctx context.Context, in agent.CallInput, _ func(string)) (llm.Response, error) {
	if p.block {
		p.started <- struct{}{}
		<-ctx.Done()
		return llm.Response{}, ctx.Err()
	}
	last := ""
	for _, m := range in.Messages {
		if m.Role == "user" {
			last = m.Content.AsText()
		}
	}
	s := "done: " + last
	return llm.Response{Content: &s, FinishReason: "stop", StopReason: "end_turn"}, nil
}

type harness struct {
	st      *session.Store
	mu      sync.Mutex
	systems []string
	parents []string
	n       int
}

func (h *harness) factory(p agent.Provider) Factory {
	return func(ctx context.Context, parent, system string) (*agent.Agent, error) {
		h.mu.Lock()
		h.n++
		id := "child" + string(rune('0'+h.n))
		h.systems = append(h.systems, system)
		h.parents = append(h.parents, parent)
		h.mu.Unlock()
		if err := h.st.CreateSession(ctx, session.NewSession{ID: id, Source: "delegate", ParentSessionID: parent}); err != nil {
			return nil, err
		}
		return agent.New(agent.Config{Store: h.st, SessionID: id, Provider: p, Model: "m",
			Prompt: func() agent.PromptParts { return agent.PromptParts{Stable: system} }}), nil
	}
}

func call(t *testing.T, f Factory, max int, args map[string]any, ctx context.Context) map[string]any {
	t.Helper()
	reg := tools.NewRegistry()
	Register(reg, f, max)
	b, _ := json.Marshal(args)
	ctx = tools.WithEnv(ctx, tools.CallEnv{SessionID: "parent", CWD: "/work/dir"})
	out, _ := reg.Build(tools.Selection{}).Run(ctx, llm.ToolCall{Function: llm.ToolCallFunction{Name: "delegate_task", Arguments: string(b)}})
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("not JSON: %q", out)
	}
	return m
}

func newHarness(t *testing.T) *harness {
	st, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.CreateSession(context.Background(), session.NewSession{ID: "parent", Source: "cli"}); err != nil {
		t.Fatal(err)
	}
	return &harness{st: st}
}

func TestChildrenRunInOwnSessionsAndReturnSummaries(t *testing.T) {
	h := newHarness(t)
	r := call(t, h.factory(&echoProvider{}), 3, map[string]any{"tasks": []any{
		map[string]any{"goal": "find A", "context": "repo at /x"},
		map[string]any{"goal": "find B"},
	}}, context.Background())
	res := r["results"].([]any)
	if len(res) != 2 {
		t.Fatalf("%v", r)
	}
	for i, want := range []string{"done: find A", "done: find B"} {
		x := res[i].(map[string]any)
		if x["status"] != "completed" || x["summary"] != want || x["task_index"] != float64(i) {
			t.Errorf("result %d = %v", i, x)
		}
	}
	for _, p := range h.parents {
		if p != "parent" {
			t.Errorf("child parent = %q", p)
		}
	}
	var withCtx, withDir int
	for _, s := range h.systems {
		if strings.Contains(s, "CONTEXT:\nrepo at /x") {
			withCtx++
		}
		if strings.Contains(s, "WORKSPACE PATH:\n/work/dir") && strings.Contains(s, "focused subagent") {
			withDir++
		}
	}
	if withCtx != 1 || withDir != 2 {
		t.Errorf("system prompts wrong (ctx=%d dir=%d): %q", withCtx, withDir, h.systems)
	}
}

func TestTaskLimitAndValidation(t *testing.T) {
	h := newHarness(t)
	f := h.factory(&echoProvider{})
	for name, args := range map[string]map[string]any{
		"too many":     {"tasks": []any{map[string]any{"goal": "a"}, map[string]any{"goal": "b"}, map[string]any{"goal": "c"}}},
		"none":         {},
		"empty goal":   {"tasks": []any{map[string]any{"goal": "  "}}},
		"goal missing": {"tasks": []any{map[string]any{"context": "x"}}},
	} {
		max := 2
		if name != "too many" {
			max = 3
		}
		r := call(t, f, max, args, context.Background())
		if r["error"] == nil {
			t.Errorf("%s: %v", name, r)
		}
	}
	if h.n != 0 {
		t.Error("a rejected call started subagents")
	}
	// The legacy single-goal shape still works.
	if r := call(t, f, 3, map[string]any{"goal": "solo"}, context.Background()); r["results"].([]any)[0].(map[string]any)["summary"] != "done: solo" {
		t.Errorf("%v", r)
	}
}

func TestFactoryFailureAndInterruptAreReportedPerChild(t *testing.T) {
	h := newHarness(t)
	bad := Factory(func(ctx context.Context, parent, system string) (*agent.Agent, error) {
		return nil, context.DeadlineExceeded
	})
	r := call(t, bad, 3, map[string]any{"tasks": []any{map[string]any{"goal": "g"}}}, context.Background())
	if x := r["results"].([]any)[0].(map[string]any); x["status"] != "failed" || !strings.Contains(x["error"].(string), "could not start") {
		t.Errorf("%v", x)
	}

	p := &echoProvider{block: true, started: make(chan struct{}, 2)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan map[string]any)
	go func() {
		done <- call(t, h.factory(p), 3, map[string]any{"tasks": []any{map[string]any{"goal": "a"}, map[string]any{"goal": "b"}}}, ctx)
	}()
	<-p.started
	<-p.started // both children are running at once
	cancel()
	select {
	case r := <-done:
		for _, x := range r["results"].([]any) {
			if x.(map[string]any)["status"] != "interrupted" {
				t.Errorf("%v", x)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelling the parent did not stop the children")
	}
}

func TestChildSelectionBlocksDelegationAndMemory(t *testing.T) {
	reg := tools.NewRegistry()
	noop := func(context.Context, tools.Args) (string, error) { return "", nil }
	obj := map[string]any{"type": "object"}
	for _, x := range []struct{ name, set string }{
		{"read_file", "file"}, {"delegate_task", "delegation"}, {"agent_memory_remember", "memory"},
		{"agent_memory_recall", "memory"}, {"cronjob", "cron"}, {"clarify", "clarify"}, {"terminal", "terminal"},
	} {
		reg.MustRegister(tools.Tool{Name: x.name, Toolset: x.set, Description: "d", Parameters: obj, Handler: noop})
	}
	parent := reg.Build(tools.Selection{})
	if !parent.Names()["agent_memory_remember"] || !parent.Names()["delegate_task"] {
		t.Fatal("the parent should have every tool")
	}
	child := reg.Build(ChildSelection(tools.Selection{}))
	got := child.Names()
	if len(got) != 2 || !got["read_file"] || !got["terminal"] {
		t.Errorf("child tools = %v, want only read_file and terminal", got)
	}
	// A parent restriction is kept, not widened.
	narrow := reg.Build(ChildSelection(tools.Selection{Toolsets: []string{"file", "memory"}}))
	if n := narrow.Names(); len(n) != 1 || !n["read_file"] {
		t.Errorf("narrowed child tools = %v", n)
	}
}
