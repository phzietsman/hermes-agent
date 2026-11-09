package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/phzietsman/hermes-agent/internal/llm"
	"github.com/phzietsman/hermes-agent/internal/provider/anthropic"
	"github.com/phzietsman/hermes-agent/internal/session"
)

func sse(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		var head struct{ Type string }
		_ = json.Unmarshal([]byte(e), &head)
		fmt.Fprintf(&b, "event: %s\ndata: %s\n\n", head.Type, e)
	}
	return b.String()
}

func msgStart() string {
	return `{"type":"message_start","message":{"id":"m","model":"claude-opus-4-5","usage":{"input_tokens":50,"output_tokens":1,"cache_read_input_tokens":10}}}`
}

func textStream(text string) string {
	return sse(msgStart(),
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}`, text),
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`,
		`{"type":"message_stop"}`)
}

func toolStream(id, name, args string) string {
	return sse(msgStart(),
		fmt.Sprintf(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":%q,"name":%q,"input":{}}}`, id, name),
		fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":%q}}`, args),
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`,
		`{"type":"message_stop"}`)
}

// End to end through the real Anthropic client and a fake server: a tool-using conversation streams text,
// runs the tool, sends its result back, keeps the system bytes identical and stays within four breakpoints.
func TestLoopAgainstFakeAnthropicServer(t *testing.T) {
	var mu sync.Mutex
	var seen []map[string]any
	replies := []string{toolStream("toolu_1", "echo", `{"x":1}`), textStream("all done"), textStream("second answer")}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		n := len(seen)
		seen = append(seen, body)
		mu.Unlock()
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, replies[n])
	}))
	defer srv.Close()

	st, err := session.OpenPath(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateSession(context.Background(), session.NewSession{ID: "s1", Source: "cli", Model: "claude-opus-4-5"}); err != nil {
		t.Fatal(err)
	}
	tools := &fakeTools{defs: defs("echo", "slow"), parallel: map[string]bool{"echo": true},
		run: func(_ context.Context, c llm.ToolCall) (string, error) { return "echoed " + c.Function.Arguments, nil }}
	var streamed []string
	ag := New(Config{
		Store: st, SessionID: "s1", Tools: tools, Model: "claude-opus-4-5", MaxTokens: 2000,
		Provider: &AnthropicProvider{
			Client: &anthropic.Client{BaseURL: srv.URL, Auth: anthropic.APIKey("sk-ant-api-test")},
			Model:  "claude-opus-4-5", CacheTTL: CacheTTL5m, Direct: true,
		},
		Prompt: func() PromptParts { return PromptParts{Stable: "stable tier", Volatile: "volatile tier"} },
		Events: Events{Text: func(s string) { streamed = append(streamed, s) }},
	})
	res, err := ag.RunTurn(context.Background(), "please echo")
	if err != nil || !res.Completed || res.FinalResponse != "all done" || res.APICalls != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	if strings.Join(streamed, "|") != "all done" {
		t.Fatalf("streamed %v", streamed)
	}
	if res.Usage.InputTokens != 100 || res.Usage.CacheReadTokens != 20 {
		t.Fatalf("usage %+v", res.Usage)
	}
	res, err = ag.RunTurn(context.Background(), "and again")
	if err != nil || res.FinalResponse != "second answer" {
		t.Fatalf("%+v %v", res, err)
	}

	sys := func(i int) string { b, _ := json.Marshal(seen[i]["system"]); return string(b) }
	for i := range seen {
		if sys(i) != sys(0) {
			t.Fatalf("request %d: system bytes changed", i)
		}
		if n := wireMarkers(seen[i]); n > 4 {
			t.Fatalf("request %d has %d breakpoints", i, n)
		}
	}
	msgs := seen[1]["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	blocks := last["content"].([]any)
	first := blocks[0].(map[string]any)
	if last["role"] != "user" || first["type"] != "tool_result" || first["tool_use_id"] != "toolu_1" || !strings.Contains(fmt.Sprint(first["content"]), "echoed") {
		t.Fatalf("tool result was not sent back: %+v", last)
	}
	if len(seen[1]["tools"].([]any)) != 2 {
		t.Fatal("tools missing from the second request")
	}
}
