package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/phzietsman/hermes-agent/internal/agent"
	"github.com/phzietsman/hermes-agent/internal/app"
	"github.com/phzietsman/hermes-agent/internal/config"
	"github.com/phzietsman/hermes-agent/internal/llm"
)

// ---- a scripted provider ----

type script struct {
	mu    sync.Mutex
	steps []llm.Response
	calls []agent.CallInput
}

func (s *script) Complete(ctx context.Context, in agent.CallInput, onText func(string)) (llm.Response, error) {
	s.mu.Lock()
	n := len(s.calls)
	s.calls = append(s.calls, agent.CallInput{Messages: append([]llm.Message(nil), in.Messages...), Tools: in.Tools})
	s.mu.Unlock()
	if n >= len(s.steps) {
		t := "(script exhausted)"
		return llm.Response{Content: &t, FinishReason: "stop", StopReason: "end_turn"}, nil
	}
	r := s.steps[n]
	if onText != nil && r.Content != nil {
		onText(*r.Content)
	}
	return r, nil
}

func say(t string) llm.Response {
	return llm.Response{Content: &t, FinishReason: "stop", StopReason: "end_turn"}
}

func toolCall(id, name string, args map[string]any) llm.Response {
	b, _ := json.Marshal(args)
	return llm.Response{FinishReason: "tool_calls", StopReason: "tool_use",
		ToolCalls: []llm.ToolCall{{ID: id, Type: "function", Function: llm.ToolCallFunction{Name: name, Arguments: string(b)}}}}
}

func env(t *testing.T) (context.Context, string) {
	home := t.TempDir()
	t.Setenv("ANTHROPIC_API_KEY", "")
	return config.WithHome(context.Background(), home), home
}

func runCLI(t *testing.T, ctx context.Context, p agent.Provider, stdin string, args ...string) (code int, out, errOut string) {
	t.Helper()
	var o, e bytes.Buffer
	code = run(ctx, args, stdio{in: strings.NewReader(stdin), out: &o, err: &e}, app.Options{Provider: p})
	return code, o.String(), e.String()
}

func TestQueryRunsAToolUsingTurn(t *testing.T) {
	ctx, _ := env(t)
	file := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(file, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &script{steps: []llm.Response{toolCall("c1", "read_file", map[string]any{"path": file}), say("The file says beta.")}}
	code, out, errOut := runCLI(t, ctx, p, "", "chat", "-q", "what is in the file?")
	if code != 0 || !strings.Contains(out, "The file says beta.") {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	if !strings.Contains(errOut, "read_file") {
		t.Errorf("tool start not shown: %q", errOut)
	}
	// The second call carries the tool's result.
	last := p.calls[1].Messages[len(p.calls[1].Messages)-1]
	if last.Role != llm.RoleTool || !strings.Contains(last.Content.AsText(), "beta") {
		t.Errorf("tool result not fed back: %+v", last)
	}
	if len(p.calls[0].Tools) < 6 {
		t.Errorf("only %d tools offered", len(p.calls[0].Tools))
	}
}

func TestReplHandlesSlashCommandsAndSkills(t *testing.T) {
	ctx, home := env(t)
	dir := filepath.Join(home, "skills", "greeter")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	skill := "---\nname: greeter\ndescription: Greets people.\n---\nAlways greet in pirate speak.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skill), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &script{steps: []llm.Response{say("hello there"), say("Ahoy!")}}
	code, out, errOut := runCLI(t, ctx, p, "hi\n/skills\n/greeter to Bob\n/bogus\n/help\n/exit\n", "chat")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errOut)
	}
	if !strings.Contains(out, "hello there") || !strings.Contains(out, "Ahoy!") {
		t.Errorf("out %q", out)
	}
	for _, want := range []string{"/greeter", "Greets people.", "unknown command /bogus", "/yolo"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr lacks %q: %s", want, errOut)
		}
	}
	// The skill arrives as a user message, and the system prompt (cached prefix) is the same on both turns.
	users := p.calls[1].Messages
	lastUser := users[len(users)-1].Content.AsText()
	if !strings.Contains(lastUser, "pirate speak") || !strings.Contains(lastUser, "to Bob") {
		t.Errorf("skill not delivered as the user turn: %q", lastUser)
	}
	if p.calls[0].Messages[0].Content.AsText() != p.calls[1].Messages[0].Content.AsText() {
		t.Error("the system prompt changed between turns")
	}
}

func TestDangerousCommandAsksAndDenyKeepsTheFile(t *testing.T) {
	ctx, _ := env(t)
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &script{steps: []llm.Response{toolCall("c1", "terminal", map[string]any{"command": "rm -rf " + victim}), say("done")}}
	code, _, errOut := runCLI(t, ctx, p, "d\n", "chat", "-q", "clean up")
	if code != 0 || !strings.Contains(errOut, "Dangerous command") {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Error("a denied command ran")
	}
	last := p.calls[1].Messages[len(p.calls[1].Messages)-1].Content.AsText()
	if !strings.Contains(last, "NOT consented") && !strings.Contains(strings.ToLower(last), "denied") {
		t.Errorf("model was not told the command was refused: %q", last)
	}
}

func TestOnceApprovesAndYoloSkipsThePrompt(t *testing.T) {
	for _, tc := range []struct {
		name, stdin string
		args        []string
		prompted    bool
	}{
		{"once", "o\n", []string{"chat", "-q", "go"}, true},
		{"yolo flag", "", []string{"chat", "--yolo", "-q", "go"}, false},
	} {
		ctx, _ := env(t)
		victim := filepath.Join(t.TempDir(), "victim")
		os.WriteFile(victim, []byte("x"), 0o644)
		p := &script{steps: []llm.Response{toolCall("c1", "terminal", map[string]any{"command": "rm -rf " + victim}), say("done")}}
		_, _, errOut := runCLI(t, ctx, p, tc.stdin, tc.args...)
		if got := strings.Contains(errOut, "Dangerous command"); got != tc.prompted {
			t.Errorf("%s: prompted=%v", tc.name, got)
		}
		if _, err := os.Stat(victim); err == nil {
			t.Errorf("%s: approved command did not run", tc.name)
		}
	}
}

func TestHardlineBlocksEvenWithYolo(t *testing.T) {
	ctx, _ := env(t)
	p := &script{steps: []llm.Response{toolCall("c1", "terminal", map[string]any{"command": "rm -rf /"}), say("done")}}
	runCLI(t, ctx, p, "", "chat", "--yolo", "-q", "wipe")
	if last := p.calls[1].Messages[len(p.calls[1].Messages)-1].Content.AsText(); !strings.Contains(last, "BLOCKED (hardline)") {
		t.Errorf("hardline did not hold under yolo: %q", last)
	}
}

func TestNewSessionKeepsTheOldOneAndResumeContinues(t *testing.T) {
	ctx, home := env(t)
	p := &script{steps: []llm.Response{say("first"), say("second")}}
	_, _, errOut := runCLI(t, ctx, p, "one\n/new\ntwo\n/exit\n", "chat")
	ids := sessionIDs(t, errOut)
	if len(ids) != 2 || ids[0] == ids[1] {
		t.Fatalf("session ids %v in %q", ids, errOut)
	}
	// Resuming the first session replays its history to the model.
	p2 := &script{steps: []llm.Response{say("welcome back")}}
	var o, e bytes.Buffer
	code := run(config.WithHome(context.Background(), home), []string{"chat", "--resume", ids[0], "-q", "again"},
		stdio{in: strings.NewReader(""), out: &o, err: &e}, app.Options{Provider: p2})
	if code != 0 {
		t.Fatalf("resume: %d %s", code, e.String())
	}
	msgs := p2.calls[0].Messages
	var texts []string
	for _, m := range msgs {
		texts = append(texts, m.Content.AsText())
	}
	if joined := strings.Join(texts, "|"); !strings.Contains(joined, "one") || !strings.Contains(joined, "first") || !strings.Contains(joined, "again") {
		t.Errorf("history not replayed: %q", joined)
	}
}

func sessionIDs(t *testing.T, stderr string) []string {
	var ids []string
	for _, l := range strings.Split(stderr, "\n") {
		if rest, ok := strings.CutPrefix(l, "hermes — session "); ok {
			ids = append(ids, strings.Fields(rest)[0])
		}
		if rest, ok := strings.CutPrefix(l, "new session "); ok {
			ids = append(ids, strings.TrimSpace(rest))
		}
	}
	return ids
}

func TestUsageAndUnknownCommand(t *testing.T) {
	ctx, _ := env(t)
	if code, _, e := runCLI(t, ctx, nil, ""); code != 2 || !strings.Contains(e, "usage:") {
		t.Errorf("no args: %d %q", code, e)
	}
	if code, _, e := runCLI(t, ctx, nil, "", "bogus"); code != 2 || !strings.Contains(e, `unknown command "bogus"`) {
		t.Errorf("unknown: %d %q", code, e)
	}
	if code, _, _ := runCLI(t, ctx, nil, "", "help"); code != 0 {
		t.Errorf("help: %d", code)
	}
}

func TestMissingCredentialIsExplained(t *testing.T) {
	ctx, _ := env(t)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	var o, e bytes.Buffer
	code := run(ctx, []string{"chat", "-q", "hi"}, stdio{in: strings.NewReader(""), out: &o, err: &e}, app.Options{})
	if code != 1 || !strings.Contains(e.String(), "ANTHROPIC_API_KEY") {
		t.Errorf("code=%d err=%q", code, e.String())
	}
}

// ---- a fake Anthropic server ----

func sse(events ...[2]string) string {
	var b strings.Builder
	for _, e := range events {
		fmt.Fprintf(&b, "event: %s\ndata: %s\n\n", e[0], e[1])
	}
	return b.String()
}

func start(id string) [2]string {
	return [2]string{"message_start", `{"type":"message_start","message":{"id":"` + id + `","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":1}}}`}
}

func tail(reason string) [][2]string {
	return [][2]string{{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"` + reason + `","stop_sequence":null},"usage":{"output_tokens":5}}`},
		{"message_stop", `{"type":"message_stop"}`}}
}

func TestChatAgainstAFakeAnthropicServer(t *testing.T) {
	file := filepath.Join(t.TempDir(), "data.txt")
	os.WriteFile(file, []byte("the answer is 42\n"), 0o644)

	var mu sync.Mutex
	var bodies []map[string]any
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var m map[string]any
		json.Unmarshal(raw, &m)
		mu.Lock()
		bodies = append(bodies, m)
		keys = append(keys, r.Header.Get("x-api-key"))
		n := len(bodies)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		var ev [][2]string
		if n == 1 {
			args, _ := json.Marshal(map[string]any{"path": file})
			ev = append([][2]string{start("m1"),
				{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"read_file","input":{}}}`},
				{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":` + string(mustJSON(string(args))) + `}}`},
				{"content_block_stop", `{"type":"content_block_stop","index":0}`}}, tail("tool_use")...)
		} else {
			ev = append([][2]string{start("m2"),
				{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
				{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"It is 42."}}`},
				{"content_block_stop", `{"type":"content_block_stop","index":0}`}}, tail("end_turn")...)
		}
		io.WriteString(w, sse(ev...))
	}))
	defer srv.Close()

	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "config.yaml"), []byte("anthropic:\n  base_url: "+srv.URL+"\n"), 0o644)
	os.WriteFile(filepath.Join(home, ".env"), []byte("ANTHROPIC_API_KEY=sk-ant-api03-fake-key-for-tests\n"), 0o600)
	t.Setenv("ANTHROPIC_API_KEY", "")
	ctx := config.WithHome(context.Background(), home)

	var o, e bytes.Buffer
	code := run(ctx, []string{"chat", "-q", "what is in data.txt?"}, stdio{in: strings.NewReader(""), out: &o, err: &e}, app.Options{})
	if code != 0 || !strings.Contains(o.String(), "It is 42.") {
		t.Fatalf("code=%d out=%q err=%q", code, o.String(), e.String())
	}
	if len(bodies) != 2 {
		t.Fatalf("%d API calls", len(bodies))
	}
	for _, k := range keys {
		if k != "sk-ant-api03-fake-key-for-tests" {
			t.Errorf("api key header %q", k)
		}
	}
	// Prompt-cache invariants on the wire: the system prefix is byte-identical across the two calls and at most
	// four cache breakpoints are sent.
	s0, _ := json.Marshal(bodies[0]["system"])
	s1, _ := json.Marshal(bodies[1]["system"])
	if string(s0) != string(s1) {
		t.Error("system prompt differs between the two calls")
	}
	for i, b := range bodies {
		raw, _ := json.Marshal(b)
		if n := strings.Count(string(raw), `"cache_control"`); n > 4 || n == 0 {
			t.Errorf("call %d sent %d cache breakpoints", i, n)
		}
	}
	raw, _ := json.Marshal(bodies[1]["messages"])
	if !strings.Contains(string(raw), "tool_result") || !strings.Contains(string(raw), "the answer is 42") {
		t.Errorf("second call lacks the tool result: %s", raw)
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
