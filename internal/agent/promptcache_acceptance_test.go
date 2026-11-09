package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phzietsman/hermes-agent/internal/config"
	"github.com/phzietsman/hermes-agent/internal/llm"
	"github.com/phzietsman/hermes-agent/internal/provider/anthropic"
	"github.com/phzietsman/hermes-agent/internal/session"
	"github.com/phzietsman/hermes-agent/internal/skills"
)

// wireMarkers counts cache_control objects anywhere in a request body.
func wireMarkers(v any) int {
	switch t := v.(type) {
	case map[string]any:
		n := 0
		for k, x := range t {
			if k == "cache_control" {
				n++
			}
			n += wireMarkers(x)
		}
		return n
	case []any:
		n := 0
		for _, x := range t {
			n += wireMarkers(x)
		}
		return n
	}
	return 0
}

type recordedCall struct {
	body   map[string]any
	system string // the raw JSON of the system field
}

func fakeAnthropic(t *testing.T) (*httptest.Server, func() []recordedCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []recordedCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]json.RawMessage
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Error(err)
		}
		var full map[string]any
		_ = json.Unmarshal(raw, &full)
		mu.Lock()
		calls = append(calls, recordedCall{body: full, system: string(body["system"])})
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, `{"id":"m","model":"claude-opus-4-5","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":5,"output_tokens":1}}`)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []recordedCall { mu.Lock(); defer mu.Unlock(); return append([]recordedCall(nil), calls...) }
}

// Acceptance (task 2.2c): a multi-turn conversation through a fake Anthropic server. The system prompt is
// built once and stored; every request carries identical system bytes; no request exceeds four breakpoints and
// a settled conversation uses exactly four; the stored transcript is never decorated.
func TestPromptCacheAcceptance(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	if err := config.Init(home); err != nil {
		t.Fatal(err)
	}
	st, err := session.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateSession(ctx, session.NewSession{ID: "20260315_081500_acc", Source: "cli", Model: "claude-opus-4-5"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(home, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := []llm.ToolDef{
		{Name: "terminal", Description: "run a command", Parameters: llm.Block{"type": "object", "properties": llm.Block{}}},
		{Name: "read_file", Description: "read a file", Parameters: llm.Block{"type": "object", "properties": llm.Block{}}},
	}
	input := func(now time.Time) PromptInput {
		return PromptInput{
			Home: home, SessionID: "20260315_081500_acc", Model: "claude-opus-4-5", Provider: "anthropic", Platform: "cli",
			Tools: map[string]bool{"terminal": true, "read_file": true}, Toolsets: map[string]bool{"terminal": true, "file": true},
			CWD: t.TempDir(), Now: now, Zone: time.UTC,
			// Differs per rebuild, so any rebuild shows up as changed system bytes.
			MemoryBlock: fmt.Sprintf("memory snapshot at minute %d", now.Minute()),
			SkillsIndex: SkillsIndexOptions{Settings: skills.LoadSettings(home, cfg, "cli"), Filter: skills.HostFilter()},
		}
	}

	srv, recorded := fakeAnthropic(t)
	client := &anthropic.Client{BaseURL: srv.URL, Auth: anthropic.APIKey("sk-ant-api-test")}

	var transcript []llm.Message
	userTurns := []string{"first question", "second question", "third question", "fourth question"}
	for i, q := range userTurns {
		// Each turn resolves the prompt the way the loop does. The clock moves on, which must not matter.
		sp, err := EnsureSystemPrompt(ctx, st, "20260315_081500_acc", func() PromptParts {
			return BuildSystemPromptParts(input(time.Date(2026, 3, 15, 9, 30+i, 0, 0, time.UTC)))
		})
		if err != nil {
			t.Fatal(err)
		}
		if (i == 0) == sp.Restored {
			t.Fatalf("turn %d: restored = %v", i, sp.Restored)
		}
		transcript = append(transcript, llm.Message{Role: llm.RoleUser, Content: llm.Text(q)})
		msgs := append([]llm.Message{{Role: llm.RoleSystem, Content: llm.Text(sp.Text)}}, transcript...)
		// After a restart only the stored bytes are known; the static prefix is then the stable tier rebuilt.
		static := sp.Static
		if static == "" {
			static = BuildSystemPromptParts(input(time.Date(2026, 3, 15, 9, 30, 0, 0, time.UTC))).Stable
		}
		plan := BuildPromptCachePlan(msgs, tools, CacheOptions{TTL: CacheTTL1h, StaticSystemPrefix: static, DirectNativeToolCache: true})
		req, err := anthropic.BuildRequest(anthropic.RequestOptions{Model: "claude-opus-4-5", Messages: plan.Messages, Tools: plan.Tools, MaxTokens: 1024})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Create(ctx, req, anthropic.CallOptions{NoStream: true}); err != nil {
			t.Fatal(err)
		}
		transcript = append(transcript, llm.Message{Role: llm.RoleAssistant, Content: llm.Text("ok")})
		// The stored transcript is request-local decorated only: never mutated.
		for _, m := range transcript {
			if b, _ := json.Marshal(m); strings.Contains(string(b), "cache_control") {
				t.Fatalf("turn %d: transcript was decorated: %s", i, b)
			}
		}
	}

	calls := recorded()
	if len(calls) != len(userTurns) {
		t.Fatalf("got %d requests", len(calls))
	}
	for i, c := range calls {
		if c.system != calls[0].system {
			t.Fatalf("request %d: system bytes differ from request 0\n%s\n%s", i, clip(c.system), clip(calls[0].system))
		}
		if n := wireMarkers(c.body); n > 4 {
			t.Fatalf("request %d: %d breakpoints", i, n)
		}
	}
	// system prefix + last tool + the last two completed turns.
	if n := wireMarkers(calls[len(calls)-1].body); n != 4 {
		t.Fatalf("settled conversation: %d breakpoints, want exactly 4", n)
	}
	// The system prompt carries its own marker on the static prefix only.
	var sys []map[string]any
	if err := json.Unmarshal([]byte(calls[0].system), &sys); err != nil {
		t.Fatal(err)
	}
	marked := 0
	for _, b := range sys {
		if _, ok := b["cache_control"]; ok {
			marked++
		}
	}
	if marked != 1 {
		t.Fatalf("system blocks carrying a marker = %d, want 1: %s", marked, clip(calls[0].system))
	}
}
