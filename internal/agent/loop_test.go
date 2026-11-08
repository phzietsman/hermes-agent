package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phzietsman/hermes-agent/internal/llm"
	"github.com/phzietsman/hermes-agent/internal/provider/anthropic"
	"github.com/phzietsman/hermes-agent/internal/session"
)

// scripted is a Provider that replays canned answers and records what it was asked.
type scripted struct {
	mu    sync.Mutex
	steps []any // llm.Response or error
	calls []CallInput
	hook  func(n int, in CallInput)
}

func (s *scripted) Complete(ctx context.Context, in CallInput, onText func(string)) (llm.Response, error) {
	s.mu.Lock()
	n := len(s.calls)
	s.calls = append(s.calls, CallInput{Messages: append([]llm.Message(nil), in.Messages...), Tools: in.Tools, MaxTokens: in.MaxTokens, StaticPrefix: in.StaticPrefix})
	if s.hook != nil {
		s.hook(n, in)
	}
	var step any
	if n < len(s.steps) {
		step = s.steps[n]
	}
	s.mu.Unlock()
	switch v := step.(type) {
	case error:
		return llm.Response{}, v
	case llm.Response:
		if onText != nil && v.Content != nil {
			onText(*v.Content)
		}
		if v.FinishReason == "" {
			v.FinishReason = llm.FinishStop
			if len(v.ToolCalls) > 0 {
				v.FinishReason = llm.FinishToolCalls
			}
		}
		return v, nil
	}
	return llm.Response{}, fmt.Errorf("script exhausted at call %d", n)
}

func say(s string) llm.Response { return llm.Response{Content: &s} }

func call(id, name, args string) llm.ToolCall {
	return llm.ToolCall{ID: id, Type: "function", Function: llm.ToolCallFunction{Name: name, Arguments: args}}
}

func use(calls ...llm.ToolCall) llm.Response { return llm.Response{ToolCalls: calls} }

// fakeTools runs named handlers.
type fakeTools struct {
	defs     []llm.ToolDef
	run      func(ctx context.Context, c llm.ToolCall) (string, error)
	parallel map[string]bool
}

func (f *fakeTools) Defs() []llm.ToolDef { return f.defs }
func (f *fakeTools) Run(ctx context.Context, c llm.ToolCall) (string, error) {
	return f.run(ctx, c)
}
func (f *fakeTools) ParallelSafe(n string) bool { return f.parallel[n] }

func defs(names ...string) []llm.ToolDef {
	var out []llm.ToolDef
	for _, n := range names {
		out = append(out, llm.ToolDef{Name: n, Description: n, Parameters: llm.Block{"type": "object", "properties": llm.Block{}}})
	}
	return out
}

type rig struct {
	t     *testing.T
	store *session.Store
	agent *Agent
	prov  *scripted
	tools *fakeTools
	cfg   Config
	slept []time.Duration
	stats []string
}

func newRig(t *testing.T, steps ...any) *rig {
	t.Helper()
	st, err := session.OpenPath(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.CreateSession(context.Background(), session.NewSession{ID: "s1", Source: "cli", Model: "claude-opus-4-5"}); err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, store: st, prov: &scripted{steps: steps},
		tools: &fakeTools{defs: defs("echo", "slow"), parallel: map[string]bool{"echo": true},
			run: func(ctx context.Context, c llm.ToolCall) (string, error) {
				return "ran " + c.Function.Name + " " + c.Function.Arguments, nil
			}}}
	r.cfg = Config{
		Store: st, SessionID: "s1", Provider: r.prov, Tools: r.tools, Model: "claude-opus-4-5", MaxTokens: 1000,
		Prompt: func() PromptParts { return PromptParts{Stable: "stable", Volatile: "volatile"} },
		Sleep:  func(_ context.Context, d time.Duration) error { r.slept = append(r.slept, d); return nil },
		Events: Events{Status: func(s string) { r.stats = append(r.stats, s) }},
	}
	r.agent = New(r.cfg)
	return r
}

// rebuild makes a fresh Agent over the same store, as a restart would.
func (r *rig) rebuild() { r.agent = New(r.cfg) }

func (r *rig) turn(text string) TurnResult {
	r.t.Helper()
	res, err := r.agent.RunTurn(context.Background(), text)
	if err != nil {
		r.t.Fatalf("RunTurn: %v", err)
	}
	return res
}

// checkShape asserts every request the model saw and the stored transcript keep the alternation contract.
func (r *rig) checkShape() {
	r.t.Helper()
	for i, c := range r.prov.calls {
		if err := CheckAlternation(outbound(c.Messages[1:], false)); err != nil && i < len(r.prov.calls) {
			// A request ends on the user or tool message the model must answer; only interior shape matters.
			if !strings.Contains(err.Error(), "unanswered") {
				r.t.Fatalf("request %d: %v", i, err)
			}
		}
	}
	if err := CheckAlternation(r.agent.History()); err != nil {
		r.t.Fatalf("history: %v", err)
	}
}

func (r *rig) rows() []session.Message {
	rows, err := r.store.Messages(context.Background(), "s1", session.ListOptions{})
	if err != nil {
		r.t.Fatal(err)
	}
	return rows
}

func TestPlainAnswer(t *testing.T) {
	r := newRig(t, say("hello"))
	var streamed []string
	r.cfg.Events.Text = func(s string) { streamed = append(streamed, s) }
	r.rebuild()
	res := r.turn("hi")
	if !res.Completed || res.FinalResponse != "hello" || res.APICalls != 1 {
		t.Fatalf("%+v", res)
	}
	if len(streamed) != 1 || streamed[0] != "hello" {
		t.Fatalf("streamed %v", streamed)
	}
	if got := r.prov.calls[0].Messages[0]; got.Role != llm.RoleSystem || got.Content.AsText() != "stable\n\nvolatile" {
		t.Fatalf("system message = %+v", got)
	}
	if r.prov.calls[0].StaticPrefix != "stable" {
		t.Fatalf("static prefix %q", r.prov.calls[0].StaticPrefix)
	}
	rows := r.rows()
	if len(rows) != 2 || rows[0].Role != "user" || rows[1].Role != "assistant" || rows[1].Content != "hello" {
		t.Fatalf("rows %+v", rows)
	}
	r.checkShape()
}

func TestToolRoundTripPersistsAndResumes(t *testing.T) {
	r := newRig(t,
		use(call("c1", "echo", `{"x":1}`), call("c2", "echo", `{"x":2}`)),
		say("done"),
		say("second answer"))
	res := r.turn("run it")
	if !res.Completed || res.FinalResponse != "done" || res.APICalls != 2 {
		t.Fatalf("%+v", res)
	}
	h := r.agent.History()
	if len(h) != 5 || h[1].Role != llm.RoleAssistant || len(h[1].ToolCalls) != 2 || h[2].ToolCallID != "c1" || h[3].ToolCallID != "c2" {
		t.Fatalf("history %+v", h)
	}
	if got := h[2].Content.AsText(); got != `ran echo {"x":1}` {
		t.Fatalf("result %q", got)
	}
	rows := r.rows()
	if len(rows) != 5 || rows[2].ToolName != "echo" || rows[2].ToolCallID != "c1" {
		t.Fatalf("rows %+v", rows)
	}
	r.checkShape()

	// A restart reloads the same transcript and the stored system prompt, and the conversation goes on.
	r.rebuild()
	res = r.turn("again")
	if res.FinalResponse != "second answer" {
		t.Fatalf("%+v", res)
	}
	last := r.prov.calls[2].Messages
	if last[0].Content.AsText() != "stable\n\nvolatile" || len(last) != 7 {
		t.Fatalf("resumed request has %d messages, system %q", len(last), last[0].Content.AsText())
	}
	if r.prov.calls[2].StaticPrefix != "stable" {
		t.Fatalf("static prefix after resume = %q", r.prov.calls[2].StaticPrefix)
	}
	r.checkShape()
}

func TestToolCallsRunInParallelWhenSafe(t *testing.T) {
	r := newRig(t, use(call("a", "echo", `{}`), call("b", "echo", `{}`), call("c", "echo", `{}`)), say("ok"))
	var running, peak atomic.Int32
	r.tools.run = func(ctx context.Context, c llm.ToolCall) (string, error) {
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		running.Add(-1)
		return "r-" + c.ID, nil
	}
	r.turn("go")
	if peak.Load() < 2 {
		t.Fatalf("calls did not overlap (peak %d)", peak.Load())
	}
	h := r.agent.History()
	for i, id := range []string{"a", "b", "c"} {
		if h[2+i].ToolCallID != id || h[2+i].Content.AsText() != "r-"+id {
			t.Fatalf("results out of order: %+v", h)
		}
	}
}

func TestUnsafeToolsRunSequentially(t *testing.T) {
	r := newRig(t, use(call("a", "echo", `{}`), call("b", "slow", `{}`)), say("ok"))
	var running, peak atomic.Int32
	r.tools.run = func(ctx context.Context, c llm.ToolCall) (string, error) {
		n := running.Add(1)
		if n > peak.Load() {
			peak.Store(n)
		}
		time.Sleep(10 * time.Millisecond)
		running.Add(-1)
		return "x", nil
	}
	r.turn("go")
	if peak.Load() != 1 {
		t.Fatalf("a batch with a non-parallel tool overlapped (peak %d)", peak.Load())
	}
}

func TestToolErrorsAndPanicsBecomeResults(t *testing.T) {
	r := newRig(t, use(call("a", "echo", `{}`), call("b", "slow", `{}`), call("c", "slow", `{}`)), say("ok"))
	r.tools.run = func(ctx context.Context, c llm.ToolCall) (string, error) {
		switch c.ID {
		case "a":
			return "", errors.New("disk full")
		case "b":
			panic("boom")
		}
		return "partial output", errors.New("exit 1")
	}
	r.turn("go")
	h := r.agent.History()
	if h[2].Content.AsText() != "Error: disk full" || !strings.Contains(h[3].Content.AsText(), "crashed: boom") || h[4].Content.AsText() != "partial output" {
		t.Fatalf("%q | %q | %q", h[2].Content.AsText(), h[3].Content.AsText(), h[4].Content.AsText())
	}
	r.checkShape()
}

func TestEmptyToolResultGetsPlaceholder(t *testing.T) {
	r := newRig(t, use(call("a", "echo", `{}`)), say("ok"))
	r.tools.run = func(context.Context, llm.ToolCall) (string, error) { return "", nil }
	r.turn("go")
	if got := r.agent.History()[2].Content.AsText(); got != "(no output)" {
		t.Fatalf("%q", got)
	}
}

func TestUnknownToolIsAnsweredThenRecovers(t *testing.T) {
	r := newRig(t, use(call("a", "nope", `{}`)), use(call("b", "echo", `{}`)), say("fine"))
	res := r.turn("go")
	if !res.Completed {
		t.Fatalf("%+v", res)
	}
	h := r.agent.History()
	if !strings.Contains(h[2].Content.AsText(), "Tool 'nope' does not exist. Available tools: echo, slow") {
		t.Fatalf("%q", h[2].Content.AsText())
	}
	r.checkShape()
}

func TestUnknownToolRepairedByCase(t *testing.T) {
	r := newRig(t, use(call("a", "Echo", `{}`)), say("fine"))
	r.turn("go")
	if got := r.agent.History()[1].ToolCalls[0].Function.Name; got != "echo" {
		t.Fatalf("name %q", got)
	}
}

func TestThreeUnknownToolsEndTheTurn(t *testing.T) {
	r := newRig(t, use(call("a", "nope", `{}`)), use(call("b", "nope", `{}`)), use(call("c", "nope", `{}`)))
	res := r.turn("go")
	if res.Completed || !res.Partial || res.FinalResponse != "Model generated invalid tool call: nope" {
		t.Fatalf("%+v", res)
	}
	r.checkShape()
}

func TestMixedBatchRunsTheValidCall(t *testing.T) {
	r := newRig(t, use(call("a", "nope", `{}`), call("b", "echo", `{}`)), say("ok"))
	r.turn("go")
	h := r.agent.History()
	if !strings.Contains(h[2].Content.AsText(), "does not exist") || h[3].Content.AsText() != "ran echo {}" {
		t.Fatalf("%q | %q", h[2].Content.AsText(), h[3].Content.AsText())
	}
	r.checkShape()
}

func TestEmptyArgumentsBecomeObject(t *testing.T) {
	r := newRig(t, use(call("a", "echo", "  ")), say("ok"))
	r.turn("go")
	if got := r.agent.History()[1].ToolCalls[0].Function.Arguments; got != "{}" {
		t.Fatalf("%q", got)
	}
}

func TestInvalidJSONRetriesThenAnswersWithErrors(t *testing.T) {
	bad := `{"a": 1,}`
	r := newRig(t, use(call("a", "echo", bad)), use(call("b", "echo", bad)), use(call("c", "echo", bad)), say("ok"))
	res := r.turn("go")
	if !res.Completed || res.APICalls != 4 {
		t.Fatalf("%+v", res)
	}
	h := r.agent.History()
	if len(h) != 4 || !strings.HasPrefix(h[2].Content.AsText(), "Error: Invalid JSON arguments.") {
		t.Fatalf("history %+v", h)
	}
	// The first two bad replies were never stored.
	if h[1].ToolCalls[0].ID != "c" {
		t.Fatalf("stored call %q", h[1].ToolCalls[0].ID)
	}
	// The broken arguments are repaired on the way out.
	out := outbound(h, false)
	if out[1].ToolCalls[0].Function.Arguments != `{"a": 1}` {
		t.Fatalf("outbound args %q", out[1].ToolCalls[0].Function.Arguments)
	}
	r.checkShape()
}

func TestTruncatedArgumentsAreRefused(t *testing.T) {
	r := newRig(t, use(call("a", "echo", `{"path": "/tmp/x", "content": "hel`)))
	res := r.turn("go")
	if res.Completed || res.FinalResponse != truncatedArgsText || r.agent.History()[1].Role != llm.RoleAssistant {
		t.Fatalf("%+v %+v", res, r.agent.History())
	}
	for _, m := range r.agent.History() {
		if len(m.ToolCalls) > 0 {
			t.Fatal("the truncated call must not be stored")
		}
	}
	r.checkShape()
}

func TestDuplicateCallIDsAreMadeUnique(t *testing.T) {
	r := newRig(t, use(call("call_0", "echo", `{}`)), say("ok"), use(call("call_0", "echo", `{}`)), say("ok2"))
	r.turn("one")
	r.turn("two")
	var ids []string
	for _, m := range r.agent.History() {
		for _, tc := range m.ToolCalls {
			ids = append(ids, tc.ID)
		}
	}
	if len(ids) != 2 || ids[0] == ids[1] {
		t.Fatalf("ids %v", ids)
	}
	r.checkShape()
}

func TestLengthContinuationStitchesOneAnswer(t *testing.T) {
	cut := say("first half")
	cut.FinishReason = llm.FinishLength
	r := newRig(t, cut, say("second half"))
	res := r.turn("write")
	if !res.Completed || res.FinalResponse != "first half\nsecond half" {
		t.Fatalf("%+v", res)
	}
	// The model was asked to continue, and neither the fragment nor the nudge was stored.
	second := r.prov.calls[1].Messages
	if n := len(second); n != 4 || second[3].Content.AsText() != continueNudge || second[2].Content.AsText() != "first half" {
		t.Fatalf("second request: %+v", second)
	}
	rows := r.rows()
	if len(rows) != 2 || rows[1].Content != "first half\nsecond half" {
		t.Fatalf("rows %+v", rows)
	}
	h := r.agent.History()
	if len(h) != 2 {
		t.Fatalf("history kept the trail: %+v", h)
	}
	r.checkShape()
}

func TestLengthContinuationCeiling(t *testing.T) {
	cut := func(s string) llm.Response { r := say(s); r.FinishReason = llm.FinishLength; return r }
	r := newRig(t, cut("a"), cut("b"), cut("c"), cut("d"), cut("e"))
	res := r.turn("write")
	if res.Completed || !res.Partial || res.FinalResponse != "a\nb\nc\nd\ne" {
		t.Fatalf("%+v", res)
	}
	if res.APICalls != 5 {
		t.Fatalf("api calls %d", res.APICalls)
	}
	r.checkShape()
}

func TestTruncatedToolCallRetriesWithLargerCap(t *testing.T) {
	cut := use(call("a", "echo", `{"x`))
	cut.FinishReason = llm.FinishLength
	r := newRig(t, cut, cut, use(call("a", "echo", `{}`)), say("ok"))
	r.turn("go")
	caps := []int{r.prov.calls[0].MaxTokens, r.prov.calls[1].MaxTokens, r.prov.calls[2].MaxTokens}
	if !(caps[0] == 1000 && caps[1] > caps[0] && caps[2] > caps[1]) {
		t.Fatalf("output caps %v", caps)
	}
	if r.prov.calls[3].MaxTokens != 1000 {
		t.Fatalf("cap did not reset after a complete reply: %d", r.prov.calls[3].MaxTokens)
	}
}

func TestTruncatedToolCallGivesUp(t *testing.T) {
	cut := use(call("a", "echo", `{"x`))
	cut.FinishReason = llm.FinishLength
	r := newRig(t, cut, cut, cut, cut, cut)
	res := r.turn("go")
	if res.Completed || res.FinalResponse != truncatedText || res.APICalls != 5 {
		t.Fatalf("%+v", res)
	}
	r.checkShape()
}

func TestThinkingOnlyTruncationRetriesWithoutNudge(t *testing.T) {
	cut := say("")
	cut.FinishReason = llm.FinishLength
	r := newRig(t, cut, say("answer"))
	res := r.turn("go")
	if !res.Completed || res.FinalResponse != "answer" {
		t.Fatalf("%+v", res)
	}
	if got := r.prov.calls[1].Messages; len(got) != 2 {
		t.Fatalf("a nudge was added after an empty cut-off: %+v", got)
	}
	if r.prov.calls[1].MaxTokens <= 1000 {
		t.Fatalf("cap not raised: %d", r.prov.calls[1].MaxTokens)
	}
}

func TestEmptyResponseRetriesThenSentinel(t *testing.T) {
	r := newRig(t, say(""), say("  "), say(""), say(""))
	res := r.turn("go")
	if res.Completed || res.FinalResponse != "(empty)" || res.APICalls != 4 || len(r.slept) != 3 {
		t.Fatalf("%+v slept=%v", res, r.slept)
	}
	if h := r.agent.History(); h[len(h)-1].Content.AsText() != "(empty)" {
		t.Fatalf("%+v", h)
	}
	r.checkShape()
}

func TestEmptyThenAnswer(t *testing.T) {
	r := newRig(t, say(""), say("fine"))
	if res := r.turn("go"); !res.Completed || res.FinalResponse != "fine" {
		t.Fatalf("%+v", res)
	}
}

func TestContentFilterWithoutTextEndsTurn(t *testing.T) {
	refusal := say("")
	refusal.FinishReason = llm.FinishContentFilter
	r := newRig(t, refusal)
	res := r.turn("go")
	if res.Completed || !strings.Contains(res.FinalResponse, "declined") || res.APICalls != 1 {
		t.Fatalf("%+v", res)
	}
	r.checkShape()
}

func apiErr(status int, typ, msg string) error {
	return &anthropic.APIError{Status: status, Type: typ, Message: msg, Body: fmt.Sprintf(`{"type":"error","error":{"type":%q,"message":%q}}`, typ, msg)}
}

func TestRetriesRateLimitWithBackoff(t *testing.T) {
	e := apiErr(429, "rate_limit_error", "slow down")
	e.(*anthropic.APIError).Header = http.Header{"Retry-After": {"7"}}
	r := newRig(t, e, apiErr(529, "overloaded_error", "busy"), say("ok"))
	res := r.turn("go")
	if !res.Completed || res.APICalls != 1 {
		t.Fatalf("%+v", res)
	}
	if len(r.slept) != 2 || r.slept[0] != 7*time.Second {
		t.Fatalf("waits %v", r.slept)
	}
}

func TestRetryBudgetThenFailure(t *testing.T) {
	e := apiErr(529, "overloaded_error", "busy")
	r := newRig(t, e, e, e, e, e)
	res := r.turn("go")
	if res.Completed || !strings.Contains(res.FinalResponse, "kept failing") || len(r.slept) != 3 {
		t.Fatalf("%+v slept=%v", res, r.slept)
	}
	r.checkShape()
}

func TestFatalErrorIsNotRetried(t *testing.T) {
	r := newRig(t, apiErr(400, "invalid_request_error", "bad field"), say("never"))
	res := r.turn("go")
	if res.Completed || len(r.prov.calls) != 1 || len(r.slept) != 0 || !strings.Contains(res.FinalResponse, "bad field") {
		t.Fatalf("%+v calls=%d", res, len(r.prov.calls))
	}
}

func TestAuthFailureRefreshesOnce(t *testing.T) {
	r := newRig(t, apiErr(401, "authentication_error", "expired"), say("ok"))
	refreshed := 0
	r.cfg.OnAuthError = func(context.Context, error) bool { refreshed++; return true }
	r.rebuild()
	if res := r.turn("go"); !res.Completed || refreshed != 1 {
		t.Fatalf("%+v refreshed=%d", res, refreshed)
	}
	r2 := newRig(t, apiErr(401, "authentication_error", "expired"), apiErr(401, "authentication_error", "expired"))
	r2.cfg.OnAuthError = func(context.Context, error) bool { return true }
	r2.rebuild()
	if res := r2.turn("go"); res.Completed || !strings.Contains(res.FinalResponse, "credentials") || len(r2.prov.calls) != 2 {
		t.Fatalf("%+v calls=%d", res, len(r2.prov.calls))
	}
}

type fakeCompactor struct {
	compressions int
	should       bool
	fail         bool
}

func (f *fakeCompactor) ShouldCompress(int) bool { return f.should }
func (f *fakeCompactor) Compress(_ context.Context, h []llm.Message) ([]llm.Message, error) {
	f.compressions++
	if f.fail {
		return nil, errors.New("summary failed")
	}
	return h[len(h)-1:], nil // keep only the latest message
}

func TestContextOverflowCompressesAndRetries(t *testing.T) {
	overflow := apiErr(400, "invalid_request_error", "prompt is too long: 250000 tokens > 200000 maximum")
	r := newRig(t, overflow, say("ok"))
	fc := &fakeCompactor{}
	r.cfg.Compactor = fc
	r.rebuild()
	res := r.turn("go")
	if !res.Completed || fc.compressions != 1 || len(r.prov.calls) != 2 {
		t.Fatalf("%+v compressions=%d", res, fc.compressions)
	}
}

func TestContextOverflowWithoutCompactorEndsTurn(t *testing.T) {
	overflow := apiErr(400, "invalid_request_error", "prompt is too long: 250000 tokens > 200000 maximum")
	r := newRig(t, overflow)
	res := r.turn("go")
	if res.Completed || res.FinalResponse != contextOverflowText {
		t.Fatalf("%+v", res)
	}
	r.checkShape()
}

func TestOverflowGivesUpAfterRepeatedCompressions(t *testing.T) {
	overflow := apiErr(400, "invalid_request_error", "prompt is too long: 250000 tokens > 200000 maximum")
	r := newRig(t, overflow, overflow, overflow, overflow, overflow)
	fc := &fakeCompactor{}
	r.cfg.Compactor = fc
	r.rebuild()
	res := r.turn("go")
	if res.Completed || fc.compressions != maxCompressAttempts || res.FinalResponse != contextOverflowText {
		t.Fatalf("%+v compressions=%d", res, fc.compressions)
	}
}

func TestCompactionAfterToolRoundWhenTriggered(t *testing.T) {
	r := newRig(t, use(call("a", "echo", `{}`)), say("ok"))
	fc := &fakeCompactor{should: true}
	r.cfg.Compactor = fc
	r.rebuild()
	r.turn("go")
	if fc.compressions != 1 {
		t.Fatalf("compressions %d", fc.compressions)
	}
}

func TestInterruptDuringToolsClosesTranscript(t *testing.T) {
	r := newRig(t, use(call("a", "slow", `{}`), call("b", "slow", `{}`)), say("after"))
	ctx, cancel := context.WithCancel(context.Background())
	r.tools.run = func(ctx context.Context, c llm.ToolCall) (string, error) {
		cancel()
		return "done", nil
	}
	res, err := r.agent.RunTurn(ctx, "go")
	if err != nil || !res.Interrupted || res.Completed {
		t.Fatalf("%+v %v", res, err)
	}
	h := r.agent.History()
	if h[len(h)-1].Role != llm.RoleAssistant || h[len(h)-1].Content.AsText() != interruptedPlaceholder {
		t.Fatalf("tail %+v", h[len(h)-1])
	}
	if got := h[3].Content.AsText(); !strings.Contains(got, "interrupted") {
		t.Fatalf("second call's result %q", got)
	}
	// The next turn works and still alternates.
	r.prov.steps = append(r.prov.steps, say("back"))
	res = r.turn("again")
	if !res.Completed {
		t.Fatalf("%+v", res)
	}
	r.checkShape()
}

func TestInterruptBeforeCall(t *testing.T) {
	r := newRig(t, say("never"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := r.agent.RunTurn(ctx, "go")
	if err != nil || !res.Interrupted || len(r.prov.calls) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if err := CheckAlternation(r.agent.History()); err != nil {
		t.Fatal(err)
	}
}

func TestCrashLeftToolTailIsClosedOnNextTurn(t *testing.T) {
	r := newRig(t, say("ok"))
	ctx := context.Background()
	for _, row := range []session.Message{
		{SessionID: "s1", Role: "user", Content: "do it"},
		{SessionID: "s1", Role: "assistant", ToolCalls: []byte(`[{"id":"a","type":"function","function":{"name":"echo","arguments":"{}"}}]`)},
		{SessionID: "s1", Role: "tool", ToolCallID: "a", Content: "result"},
	} {
		if _, err := r.store.AppendMessage(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	r.rebuild()
	r.turn("next")
	r.checkShape()
	h := r.agent.History()
	if h[3].Role != llm.RoleAssistant || h[4].Role != llm.RoleUser {
		t.Fatalf("%+v", h)
	}
}

func TestOutboundHealsBrokenPairs(t *testing.T) {
	h := []llm.Message{
		{Role: llm.RoleUser, Content: llm.Text("q")},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call("a", "echo", `{}`), call("b", "echo", `{}`)}},
		{Role: llm.RoleTool, ToolCallID: "a", Content: llm.Text("ra")},
		{Role: llm.RoleTool, ToolCallID: "zzz", Content: llm.Text("orphan")},
		{Role: llm.RoleTool, ToolCallID: "a", Content: llm.Text("duplicate")},
		{Role: llm.RoleAssistant, Content: llm.Text("done")},
	}
	out := outbound(h, false)
	if err := CheckAlternation(out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 5 || out[3].ToolCallID != "b" || out[3].Content.AsText() != missingToolResult {
		t.Fatalf("%+v", out)
	}
	if len(h) != 6 {
		t.Fatal("the stored transcript was modified")
	}
}

func TestBudgetExhaustionSummarisesWithoutTools(t *testing.T) {
	r := newRig(t, use(call("a", "echo", `{}`)), say("Summary: did one thing."))
	r.cfg.Budget = NewIterationBudget(1)
	r.rebuild()
	res := r.turn("go")
	if res.Completed || !res.Partial || res.FinalResponse != "Summary: did one thing." {
		t.Fatalf("%+v", res)
	}
	last := r.prov.calls[1]
	if len(last.Tools) != 0 {
		t.Fatal("the summary call offered tools")
	}
	tail := last.Messages[len(last.Messages)-1]
	if tail.Role != llm.RoleTool || !strings.Contains(tail.Content.AsText(), "iteration limit") {
		t.Fatalf("summary request tail %+v", tail)
	}
	for _, m := range r.agent.History() {
		if strings.Contains(contentText(m.Content), "iteration limit for this turn") {
			t.Fatal("the request-local note was stored")
		}
	}
	r.checkShape()
}

func TestBudgetExhaustionWhenSummaryFails(t *testing.T) {
	r := newRig(t, use(call("a", "echo", `{}`)), apiErr(400, "invalid_request_error", "bad"))
	r.cfg.Budget = NewIterationBudget(1)
	r.rebuild()
	res := r.turn("go")
	if res.Completed || !strings.Contains(res.FinalResponse, "iteration limit") {
		t.Fatalf("%+v", res)
	}
	r.checkShape()
}

func TestOneTurnAtATime(t *testing.T) {
	r := newRig(t, say("ok"))
	started, release := make(chan struct{}), make(chan struct{})
	r.prov.hook = func(int, CallInput) {
		close(started)
		<-release
	}
	done := make(chan TurnResult)
	go func() { res, _ := r.agent.RunTurn(context.Background(), "one"); done <- res }()
	<-started
	if _, err := r.agent.RunTurn(context.Background(), "two"); !errors.Is(err, ErrTurnInProgress) {
		t.Fatalf("err = %v", err)
	}
	close(release)
	if res := <-done; !res.Completed {
		t.Fatalf("%+v", res)
	}
}

func TestUsageIsAccumulatedAndStored(t *testing.T) {
	a, b := say(""), say("ok")
	a.ToolCalls = []llm.ToolCall{call("a", "echo", `{}`)}
	a.Usage = llm.Usage{InputTokens: 100, OutputTokens: 10, CacheReadTokens: 50}
	b.Usage = llm.Usage{InputTokens: 120, OutputTokens: 5, CacheWriteTokens: 7}
	r := newRig(t, a, b)
	res := r.turn("go")
	if res.Usage != (llm.Usage{InputTokens: 220, OutputTokens: 15, CacheReadTokens: 50, CacheWriteTokens: 7}) {
		t.Fatalf("%+v", res.Usage)
	}
	s, err := r.store.GetSession(context.Background(), "s1")
	if err != nil || s.InputTokens != 220 || s.APICallCount != 2 || s.CacheWriteTokens != 7 {
		t.Fatalf("%+v %v", s, err)
	}
}

func TestThinkingSignatureRejectionReplaysWithoutThinking(t *testing.T) {
	first := use(call("a", "echo", `{}`))
	first.ReasoningDetails = []llm.Block{{"type": "thinking", "thinking": "hmm", "signature": "sig"}}
	r := newRig(t, first, apiErr(400, "invalid_request_error", "messages.1.content.0: Invalid `signature` in `thinking` block"), say("ok"))
	res := r.turn("go")
	if !res.Completed {
		t.Fatalf("%+v", res)
	}
	if len(r.prov.calls[1].Messages[2].ReasoningDetails) == 0 {
		t.Fatal("thinking should be present on the request that was rejected")
	}
	if len(r.prov.calls[2].Messages[2].ReasoningDetails) != 0 {
		t.Fatal("thinking should be stripped on the retry")
	}
	if len(r.agent.History()[1].ReasoningDetails) == 0 {
		t.Fatal("the stored message must keep its thinking")
	}
}
