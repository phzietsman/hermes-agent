package anthropic

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/phzietsman/hermes-agent/internal/llm"
)

type buildCase struct {
	Model         string         `json:"model"`
	MaxTokens     *int           `json:"max_tokens"`
	Reasoning     map[string]any `json:"reasoning"`
	ToolChoice    *string        `json:"tool_choice"`
	OAuth         bool           `json:"oauth"`
	ContextLength *int           `json:"context_length"`
	Fast          bool           `json:"fast"`
	Tools         bool           `json:"tools"`
}

func (c buildCase) options(msgs []llm.Message, tools []llm.ToolDef) RequestOptions {
	o := RequestOptions{Model: c.Model, Messages: msgs, OAuth: c.OAuth, FastMode: c.Fast}
	if c.Tools {
		o.Tools = tools
	}
	if c.MaxTokens != nil {
		o.MaxTokens = *c.MaxTokens
	}
	if c.ContextLength != nil {
		o.ContextLength = *c.ContextLength
	}
	if c.ToolChoice != nil {
		o.ToolChoice = *c.ToolChoice
	}
	if len(c.Reasoning) > 0 {
		r := &Reasoning{}
		if v, ok := c.Reasoning["enabled"].(bool); ok {
			r.Enabled = &v
		}
		if v, ok := c.Reasoning["effort"].(string); ok {
			r.Effort = v
		}
		o.Reasoning = r
	}
	return o
}

func (c buildCase) String() string {
	return fmt.Sprintf("%s reasoning=%v max=%v ctx=%v choice=%v oauth=%v fast=%v tools=%v",
		c.Model, c.Reasoning, deref(c.MaxTokens), deref(c.ContextLength), derefS(c.ToolChoice), c.OAuth, c.Fast, c.Tools)
}

func deref(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func derefS(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func TestBuildRequestMatchesPython(t *testing.T) {
	var fx struct {
		Messages []llm.Message `json:"messages"`
		Tools    []llm.ToolDef `json:"tools"`
		Cases    []struct {
			Case            buildCase         `json:"case"`
			ExpectedBody    any               `json:"expected_body"`
			ExpectedHeaders map[string]string `json:"expected_headers"`
		} `json:"cases"`
	}
	readJSON(t, "testdata/build.json", &fx)
	if len(fx.Cases) < 250 {
		t.Fatalf("only %d build cases", len(fx.Cases))
	}
	failed := 0
	for _, c := range fx.Cases {
		req, err := BuildRequest(c.Case.options(fx.Messages, fx.Tools))
		if err != nil {
			t.Errorf("%s: %v", c.Case, err)
			continue
		}
		if got := normalize(t, req); !reflect.DeepEqual(got, c.ExpectedBody) {
			failed++
			if failed <= 3 {
				t.Errorf("%s: body differs\n--- python\n%s\n--- go\n%s", c.Case, pretty(c.ExpectedBody), pretty(got))
			}
		}
		wantHeaders := c.ExpectedHeaders
		if len(wantHeaders) == 0 {
			wantHeaders = nil
		}
		gotHeaders := req.ExtraHeaders
		if len(gotHeaders) == 0 {
			gotHeaders = nil
		}
		if !reflect.DeepEqual(gotHeaders, wantHeaders) {
			t.Errorf("%s: headers = %v, python %v", c.Case, gotHeaders, wantHeaders)
		}
	}
	if failed > 0 {
		t.Fatalf("%d of %d build cases differ from Python", failed, len(fx.Cases))
	}
}

func TestSupportsFastModeMatchesPython(t *testing.T) {
	var fx struct {
		Fast []struct {
			Model string `json:"model"`
			Want  bool   `json:"want"`
		} `json:"fast"`
	}
	readJSON(t, "testdata/build.json", &fx)
	if len(fx.Fast) == 0 {
		t.Fatal("no fast fixtures")
	}
	for _, c := range fx.Fast {
		if got := SupportsFastMode(c.Model); got != c.Want {
			t.Errorf("SupportsFastMode(%q) = %v, python %v", c.Model, got, c.Want)
		}
	}
}

func TestBetaHeader(t *testing.T) {
	if got, want := BetaHeader(false), "interleaved-thinking-2025-05-14,fine-grained-tool-streaming-2025-05-14"; got != want {
		t.Errorf("api key betas = %q", got)
	}
	if got := BetaHeader(true); !strings.HasSuffix(got, ",claude-code-20250219,oauth-2025-04-20") {
		t.Errorf("oauth betas = %q", got)
	}
}

func TestMaxOutputTokens(t *testing.T) {
	tests := []struct {
		model string
		want  int
	}{
		{"claude-opus-4-8", 128_000}, {"claude-opus-4.8", 128_000}, {"claude-opus-4-5-20251101", 64_000}, {"claude-opus-4-20250514", 32_000},
		{"claude-3-5-sonnet-20241022", 8_192}, {"claude-3-5-haiku", 8_192}, {"claude-3-7-sonnet", 128_000}, {"claude-haiku-4-5:fast", 64_000},
		{"claude-future-9", 128_000}, {"unknown", 128_000}, {"", 128_000},
	}
	for _, tc := range tests {
		if got := MaxOutputTokens(tc.model); got != tc.want {
			t.Errorf("MaxOutputTokens(%q) = %d, want %d", tc.model, got, tc.want)
		}
	}
}

func TestBuildRequestBufferToolInput(t *testing.T) {
	req, err := BuildRequest(RequestOptions{
		Model:    "claude-opus-4-5",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: llm.Text("hi")}},
		Tools:    []llm.ToolDef{{Name: "a"}, {Name: "b"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	req.BufferToolInput()
	for _, tool := range req.Tools {
		if tool["eager_input_streaming"] != false {
			t.Fatalf("tool %v not buffered", tool["name"])
		}
	}
	b, _ := json.Marshal(req)
	if !strings.Contains(string(b), `"eager_input_streaming":false`) {
		t.Fatalf("flag missing from the body: %s", b)
	}
}

func TestRequestBodyKeepsHTMLCharactersAndStreamFlag(t *testing.T) {
	req, err := BuildRequest(RequestOptions{
		Model:    "claude-opus-4-5",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: llm.Text("a < b && c > d")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	req.Stream = true
	b, err := req.Body()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "a < b && c > d") {
		t.Errorf("HTML characters must not be escaped: %s", b)
	}
	if !strings.Contains(string(b), `"stream":true`) {
		t.Errorf("stream flag missing: %s", b)
	}
}

func TestReplaceSlugBoundaries(t *testing.T) {
	tests := []struct{ in, want string }{
		{"the hermes-agent skill", "the claude-code skill"},
		{"hermes-agent.", "claude-code."},
		{"hermes-agent's docs", "claude-code's docs"},
		{"hermes-agent.nousresearch.com", "hermes-agent.nousresearch.com"},
		{"NousResearch/hermes-agent", "NousResearch/hermes-agent"},
		{"hermes-agent/venv", "hermes-agent/venv"},
		{"~/.hermes/hermes-agent/venv", "~/.hermes/hermes-agent/venv"},
		{"name='hermes-agent'", "name='hermes-agent'"},
		{`"hermes-agent"`, `"hermes-agent"`},
		{"user@hermes-agent", "user@hermes-agent"},
		{"hermes-agent-core", "hermes-agent-core"},
		{"my-hermes-agent", "my-hermes-agent"},
		{"hermes-agent hermes-agent", "claude-code claude-code"},
		{"xhermes-agent", "xhermes-agent"},
		{"hermes-agent", "claude-code"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := replaceSlug(tc.in); got != tc.want {
			t.Errorf("replaceSlug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestProseAliasBoundaries(t *testing.T) {
	tests := []struct{ in, want string }{
		{"use session_search now", "use chat_history_lookup now"},
		{"tools/session_search_tool.py", "tools/session_search_tool.py"},
		{"session_search_extra", "session_search_extra"},
		{"(session_search)", "(chat_history_lookup)"},
		{"unsession_search", "unsession_search"},
		{"éession_search", "éession_search"},
		{"memory stays memory", "memory stays memory"},
	}
	for _, tc := range tests {
		if got := applyOAuthProseAliases(tc.in); got != tc.want {
			t.Errorf("applyOAuthProseAliases(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestOAuthWireNamerNeverCollides(t *testing.T) {
	// A real tool already named context_notes owns mcp__context_notes, so memory must not alias onto it.
	tools := []llm.Block{{"name": "context_notes"}, {"name": "memory"}, {"name": "session_search"}}
	to := oauthWireNamer(tools)
	if got := to("memory"); got != "mcp__memory" {
		t.Errorf("memory = %q, want it left unaliased to avoid a duplicate wire name", got)
	}
	if got := to("session_search"); got != "mcp__chat_history_lookup" {
		t.Errorf("session_search = %q", got)
	}
	if got := to("context_notes"); got != "mcp__context_notes" {
		t.Errorf("context_notes = %q", got)
	}
	if got := to("mcp_srv_tool"); got != "mcp__srv_tool" {
		t.Errorf("mcp_srv_tool = %q", got)
	}
	if got := to("mcp__already"); got != "mcp__already" {
		t.Errorf("already prefixed must not double-prefix: %q", got)
	}
}

func FuzzBuildRequestInvariants(f *testing.F) {
	f.Add("claude-opus-4-5", "high", true, 0, 0, "", false, false)
	f.Add("claude-sonnet-5-5", "xhigh", false, 100, 50, "read_file", true, true)
	f.Add("claude-3-5-sonnet", "bogus", true, -1, 1, "none", true, false)
	f.Add("", "", false, 1<<30, 1<<30, "memory", false, true)
	f.Add("anthropic/Claude-Opus-4.8", "ultra", true, 5, 0, "required", true, true)
	f.Fuzz(func(t *testing.T, model, effort string, enabled bool, maxTokens, ctxLen int, choice string, oauth, fast bool) {
		o := RequestOptions{
			Model: model, MaxTokens: maxTokens, ContextLength: ctxLen, ToolChoice: choice, OAuth: oauth, FastMode: fast,
			Reasoning: &Reasoning{Enabled: &enabled, Effort: effort},
			Messages:  []llm.Message{{Role: llm.RoleSystem, Content: llm.Text("You are Hermes Agent")}, {Role: llm.RoleUser, Content: llm.Text("hi")}},
			Tools:     []llm.ToolDef{{Name: "read_file"}, {Name: "memory"}, {Name: "session_search"}},
		}
		req, err := BuildRequest(o)
		if err != nil {
			t.Fatalf("BuildRequest errored: %v", err)
		}
		if req.MaxTokens <= 0 {
			t.Fatalf("max_tokens = %d, the API rejects non-positive values", req.MaxTokens)
		}
		if o.ContextLength > 0 && req.MaxTokens > o.ContextLength && req.MaxTokens > 1 {
			// Thinking with a manual budget may raise max_tokens above the clamp; that is the Python behaviour.
			if req.Thinking["type"] != "enabled" {
				t.Fatalf("max_tokens %d exceeds the context length %d", req.MaxTokens, o.ContextLength)
			}
		}
		if req.ToolChoice != nil && len(req.Tools) == 0 {
			t.Fatal("tool_choice without tools")
		}
		body, err := req.Body()
		if err != nil || !json.Valid(body) {
			t.Fatalf("invalid body: %v", err)
		}
		if oauth {
			sys, ok := req.System.([]llm.Block)
			if !ok || len(sys) == 0 || sys[0]["text"] != claudeCodeSystemPrefix {
				t.Fatalf("OAuth request must lead with the Claude Code identity: %v", req.System)
			}
			for _, tool := range req.Tools {
				if name, _ := tool["name"].(string); !strings.HasPrefix(name, "mcp__") {
					t.Fatalf("OAuth tool name %q is not on the mcp__ wire form", name)
				}
			}
		}
		if req.Temperature != nil && *req.Temperature != 1 {
			t.Fatalf("temperature %d", *req.Temperature)
		}
	})
}
