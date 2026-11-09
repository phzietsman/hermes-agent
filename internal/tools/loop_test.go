package tools_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phzietsman/hermes-agent/internal/agent"
	"github.com/phzietsman/hermes-agent/internal/llm"
	"github.com/phzietsman/hermes-agent/internal/session"
	"github.com/phzietsman/hermes-agent/internal/tools"
)

type twoStep struct{ calls []agent.CallInput }

func (p *twoStep) Complete(_ context.Context, in agent.CallInput, _ func(string)) (llm.Response, error) {
	p.calls = append(p.calls, in)
	if len(p.calls) == 1 {
		return llm.Response{FinishReason: llm.FinishToolCalls, ToolCalls: []llm.ToolCall{
			{ID: "c1", Type: "function", Function: llm.ToolCallFunction{Name: "echo", Arguments: `{"n":"42","tags":"[\"a\"]"}`}}}}, nil
	}
	s := "done"
	return llm.Response{Content: &s, FinishReason: llm.FinishStop}, nil
}

// A toolbox drives the real agent loop: the model's string-typed arguments are coerced, the handler runs with
// the per-call environment, and the result goes back to the model.
func TestToolboxDrivesTheAgentLoop(t *testing.T) {
	st, err := session.OpenPath(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateSession(context.Background(), session.NewSession{ID: "s", Source: "cli", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	r := tools.NewRegistry()
	r.MustRegister(echoTool("echo", "core"))
	tb := r.Build(tools.Selection{})
	prov := &twoStep{}
	ag := agent.New(agent.Config{
		Store: st, SessionID: "s", Provider: prov, Tools: tb, Model: "m",
		Prompt: func() agent.PromptParts { return agent.PromptParts{Stable: "s"} },
	})
	res, err := ag.RunTurn(tools.WithEnv(context.Background(), tools.CallEnv{Home: "/home/x"}), "go")
	if err != nil || !res.Completed || res.FinalResponse != "done" {
		t.Fatalf("%+v %v", res, err)
	}
	second := prov.calls[1].Messages
	last := second[len(second)-1]
	if last.Role != llm.RoleTool || !strings.Contains(last.Content.AsText(), `"n":42`) || !strings.Contains(last.Content.AsText(), `"tags":["a"]`) ||
		!strings.Contains(last.Content.AsText(), `"home":"/home/x"`) {
		t.Fatalf("tool result sent back: %+v", last)
	}
	if len(prov.calls[0].Tools) != 1 || prov.calls[0].Tools[0].Name != "echo" {
		t.Fatalf("tools offered: %+v", prov.calls[0].Tools)
	}
}
