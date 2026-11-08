package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/phzietsman/hermes-agent/internal/app"
	"github.com/phzietsman/hermes-agent/internal/config"
	"github.com/phzietsman/hermes-agent/internal/llm"
)

type samServer struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []string
}

func (s *samServer) saw(substr string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.reqs {
		if strings.Contains(r, substr) {
			return true
		}
	}
	return false
}

func newSAM(t *testing.T) *samServer {
	s := &samServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.reqs = append(s.reqs, r.URL.Path+" "+string(b))
		s.mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/profile"):
			io.WriteString(w, `{"instructions":[{"description":"Always be concise"}],"preferences":[],"static":[]}`)
		case strings.HasSuffix(r.URL.Path, "/query"):
			io.WriteString(w, `{"hits":[{"id":"1","text":"the user likes green tea","score":0.91}]}`)
		default:
			io.WriteString(w, `{}`)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func memHome(t *testing.T, s *samServer) (context.Context, string) {
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "agent-memory.json"), []byte(`{"endpoint":"`+s.URL+`","context":"ctx","timeout_s":5,"prefetch_wait_s":1,"max_retries":0}`), 0o600)
	os.WriteFile(filepath.Join(home, ".env"), []byte("AGENT_MEMORY_API_KEY=mem-key\n"), 0o600)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("AGENT_MEMORY_API_KEY", "")
	return config.WithHome(context.Background(), home), home
}

func TestChatUsesMemoryEndToEnd(t *testing.T) {
	s := newSAM(t)
	ctx, home := memHome(t, s)
	p := &script{steps: []llm.Response{toolCall("c1", "agent_memory_remember", map[string]any{"text": "likes oat milk", "kind": "preference"}), say("Noted, and I remember tea.")}}
	var o, e bytes.Buffer
	code := run(ctx, []string{"chat", "-q", "what do I like to drink?"}, stdio{in: strings.NewReader(""), out: &o, err: &e}, app.Options{Provider: p})
	if code != 0 {
		t.Fatalf("code %d: %s", code, e.String())
	}
	first := p.calls[0]
	if sys := first.Messages[0].Content.AsText(); !strings.Contains(sys, "persistent long-term memory") || !strings.Contains(sys, "- Always be concise") {
		t.Errorf("the system prompt lacks the memory block and core profile: %q", sys)
	}
	if got := first.Messages[len(first.Messages)-1].Content.AsText(); !strings.Contains(got, "## Recalled memory") || !strings.Contains(got, "the user likes green tea (0.91)") {
		t.Errorf("recall not in the user message: %q", got)
	}
	names := map[string]bool{}
	for _, d := range first.Tools {
		names[d.Name] = true
	}
	for _, n := range []string{"agent_memory_recall", "agent_memory_remember", "agent_memory_forget", "agent_memory_reflect"} {
		if !names[n] {
			t.Errorf("tool %s not offered", n)
		}
	}
	// By exit the model's remember call, the turn and the transcript have reached the service, and it consolidated.
	for _, want := range []string{"likes oat milk", "origin=turn", "origin=session", "/consolidate"} {
		if !s.saw(want) {
			t.Errorf("service never saw %q: %v", want, s.reqs)
		}
	}
	if pend, _ := os.ReadFile(filepath.Join(home, "agent-memory-outbox.jsonl")); len(pend) != 0 {
		t.Errorf("outbox not drained: %s", pend)
	}
}

func TestChatWithoutMemoryConfigHasNoMemoryTools(t *testing.T) {
	ctx, _ := env(t)
	p := &script{steps: []llm.Response{say("ok")}}
	runCLI(t, ctx, p, "", "chat", "-q", "hi")
	for _, d := range p.calls[0].Tools {
		if strings.HasPrefix(d.Name, "agent_memory_") {
			t.Errorf("%s offered without memory configured", d.Name)
		}
	}
	if strings.Contains(p.calls[0].Messages[0].Content.AsText(), "long-term memory") {
		t.Error("memory guidance in the prompt without memory")
	}
}

func TestDelegatedChildrenGetNoMemoryTools(t *testing.T) {
	s := newSAM(t)
	ctx, _ := memHome(t, s)
	p := &script{steps: []llm.Response{
		toolCall("c1", "delegate_task", map[string]any{"tasks": []any{map[string]any{"goal": "look something up"}}}),
		say("child done"), say("all done"),
	}}
	var o, e bytes.Buffer
	run(ctx, []string{"chat", "-q", "delegate"}, stdio{in: strings.NewReader(""), out: &o, err: &e}, app.Options{Provider: p})
	child := p.calls[1]
	for _, d := range child.Tools {
		if strings.HasPrefix(d.Name, "agent_memory_") || d.Name == "delegate_task" {
			t.Errorf("child was offered %s", d.Name)
		}
	}
	if sys := child.Messages[0].Content.AsText(); strings.Contains(sys, "long-term memory") {
		t.Error("child prompt carries the memory block")
	}
}

func TestAgentMemoryCommands(t *testing.T) {
	s := newSAM(t)
	ctx, home := memHome(t, s)
	os.MkdirAll(filepath.Join(home, "memories"), 0o755)
	os.WriteFile(filepath.Join(home, "memories", "MEMORY.md"), []byte("a learned fact\n§\nanother"), 0o644)
	do := func(args ...string) (int, string) {
		var o, e bytes.Buffer
		code := run(ctx, append([]string{"agent_memory"}, args...), stdio{in: strings.NewReader(""), out: &o, err: &e}, app.Options{})
		return code, o.String() + e.String()
	}
	if code, out := do("status"); code != 0 || !strings.Contains(out, "context     : ctx") || !strings.Contains(out, "0 pending") {
		t.Errorf("status: %d %s", code, out)
	}
	if code, out := do("doctor"); code != 0 || !strings.Contains(out, "✓ whoami") || !strings.Contains(out, "✓ recall") {
		t.Errorf("doctor: %d %s", code, out)
	}
	if code, out := do("scopes-init"); code != 0 || !strings.Contains(out, "registered user/default") || !strings.Contains(out, "registered agent/hermes") {
		t.Errorf("scopes-init: %d %s", code, out)
	}
	if code, out := do("import", "--dry-run"); code != 0 || !strings.Contains(out, "would write 2") || s.saw("a learned fact") {
		t.Errorf("dry run: %d %s", code, out)
	}
	if code, out := do("import"); code != 0 || !strings.Contains(out, "wrote 2, already imported 0, failed 0") || !s.saw("a learned fact") {
		t.Errorf("import: %d %s", code, out)
	}
	if code, out := do("import"); code != 0 || !strings.Contains(out, "wrote 0, already imported 2") {
		t.Errorf("re-import: %d %s", code, out)
	}
	os.WriteFile(filepath.Join(home, "agent-memory-outbox.jsonl"),
		[]byte(`{"entry_id":"x","op":"remember","payload":{"text":"queued earlier"},"created_at":"2026-11-01T10:00:00Z","attempts":0}`+"\n"), 0o600)
	if code, out := do("replay"); code != 0 || !strings.Contains(out, "replayed 1/1; 0 left") || !s.saw("queued earlier") {
		t.Errorf("replay: %d %s", code, out)
	}
	if code, _ := do(); code != 2 {
		t.Errorf("no subcommand: %d", code)
	}
	if code, _ := do("bogus"); code != 2 {
		t.Errorf("bogus: %d", code)
	}
}

func TestAgentMemoryUnconfigured(t *testing.T) {
	ctx, _ := env(t)
	t.Setenv("AGENT_MEMORY_API_KEY", "")
	var o, e bytes.Buffer
	code := run(ctx, []string{"agent_memory", "status"}, stdio{in: strings.NewReader(""), out: &o, err: &e}, app.Options{})
	if code != 1 || !strings.Contains(o.String(), "configured  : no") {
		t.Errorf("%d %s", code, o.String())
	}
	o.Reset()
	if code := run(ctx, []string{"agent_memory", "doctor"}, stdio{in: strings.NewReader(""), out: &o, err: &e}, app.Options{}); code != 1 || !strings.Contains(o.String(), "not configured") {
		t.Errorf("%d %s", code, o.String())
	}
}
