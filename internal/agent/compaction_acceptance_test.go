package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phzietsman/hermes-agent/internal/agent/prompttext"
	"github.com/phzietsman/hermes-agent/internal/llm"
	"github.com/phzietsman/hermes-agent/internal/session"
)

// recordSession writes k exchanges, each with a tool round, into the store the way a long working session
// would have: the user's request, the assistant's tool call, a bulky result, the answer.
func recordSession(t *testing.T, st *session.Store, id string, k int) {
	t.Helper()
	ctx := context.Background()
	add := func(m session.Message) {
		m.SessionID = id
		if _, err := st.AppendMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < k; i++ {
		cid := fmt.Sprintf("rec%d", i)
		calls, _ := json.Marshal([]llm.ToolCall{call(cid, "terminal", fmt.Sprintf(`{"command":"make step%d"}`, i))})
		add(session.Message{Role: "user", Content: fmt.Sprintf("request %d: please run step %d. %s", i, i, filler(15))})
		add(session.Message{Role: "assistant", ToolCalls: calls})
		add(session.Message{Role: "tool", ToolCallID: cid, ToolName: "terminal", Content: fmt.Sprintf(`{"output":%q,"exit_code":0}`, filler(130))})
		add(session.Message{Role: "assistant", Content: fmt.Sprintf("step %d finished; the build id is zebra-%d. %s", i, i, filler(10))})
	}
}

// Acceptance (task 2.4): a long recorded session crosses the threshold, is compacted under the same session id,
// keeps its alternation and tool pairs, archives rather than loses the old messages, rebuilds the system prompt
// once, and a restart reads the compacted transcript back.
func TestCompactionOnLongRecordedSession(t *testing.T) {
	ctx := context.Background()
	st, err := session.OpenPath(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const sid = "20260315_081500_long"
	if err := st.CreateSession(ctx, session.NewSession{ID: sid, Source: "cli", Model: "claude-opus-4-5"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetSystemPrompt(ctx, sid, "stable\n\nvolatile"); err != nil {
		t.Fatal(err)
	}
	recordSession(t, st, sid, 40)
	before, _ := st.Messages(ctx, sid, session.ListOptions{})
	if len(before) != 160 {
		t.Fatalf("recorded %d messages", len(before))
	}

	summarizer := &scripted{steps: []any{say("## Goal\nrun the build steps\n## Completed Actions\n1. ran make steps"), say("## Goal\nrun the build steps\n## Completed Actions\n1. ran make steps and a second round")}}
	model := &scripted{steps: []any{say("answer one"), say("answer two"), say("answer three"), say("answer four")}}
	comp := NewCompressor(CompressorConfig{
		ContextLength: 20_000, SessionID: sid, Summarize: ProviderSummarizer(summarizer),
	})
	if comp.Threshold() != 17_000 {
		t.Fatalf("threshold %d", comp.Threshold())
	}
	build := func() *Agent {
		return New(Config{
			Store: st, SessionID: sid, Provider: model, Model: "claude-opus-4-5", ContextLength: 20_000, Compactor: comp,
			Tools:  &fakeTools{defs: defs("terminal"), parallel: map[string]bool{}, run: func(context.Context, llm.ToolCall) (string, error) { return "ok", nil }},
			Prompt: func() PromptParts { return PromptParts{Stable: "stable", Volatile: "volatile"} },
		})
	}
	ag := build()
	res, err := ag.RunTurn(ctx, "what was the build id of step 3?")
	if err != nil || !res.Completed || res.FinalResponse != "answer one" {
		t.Fatalf("%+v %v", res, err)
	}

	// The summarizer was asked once; the model then saw the compacted transcript, not 160 messages.
	if len(summarizer.calls) != 1 {
		t.Fatalf("%d summary calls", len(summarizer.calls))
	}
	if p := contentText(summarizer.calls[0].Messages[0].Content); !strings.Contains(p, "request 20") || !strings.Contains(p, "TURNS TO SUMMARIZE:") {
		t.Fatalf("summary prompt did not carry the middle of the session: %q", clip(p))
	}
	req := model.calls[0].Messages
	if len(req) >= 110 {
		t.Fatalf("the model call still carried %d messages", len(req))
	}
	if err := CheckAlternation(outbound(req[1:], false)); err != nil && !strings.Contains(err.Error(), "unanswered") {
		t.Fatal(err)
	}
	if got := estimateMessages(req[1:]); got >= comp.Threshold() {
		t.Fatalf("compacted transcript is still %d tokens (threshold %d)", got, comp.Threshold())
	}
	system := contentText(req[0].Content)
	if strings.Count(system, prompttext.C.CompressionNote) != 1 || !strings.HasPrefix(system, "stable\n\nvolatile") {
		t.Fatalf("system prompt after compaction: %q", clip(system))
	}

	// Same session; old messages archived, not deleted; the summary is flagged; search still finds old facts.
	if s, _ := st.GetSession(ctx, sid); s.ID != sid || s.SystemPrompt != system {
		t.Fatalf("session row: %+v", s)
	}
	live, _ := st.Messages(ctx, sid, session.ListOptions{})
	all, _ := st.Messages(ctx, sid, session.ListOptions{IncludeCompacted: true})
	if len(live) >= 110 || len(all) != len(before)+1+len(live) {
		t.Fatalf("live %d, with archive %d (recorded %d plus this turn's user message)", len(live), len(all), len(before))
	}
	flagged := 0
	for _, m := range live {
		if m.CompressedSummary {
			flagged++
		}
	}
	if flagged != 1 {
		t.Fatalf("%d rows flagged as the summary", flagged)
	}
	hits, err := st.Search(ctx, session.SearchQuery{Query: "zebra-3"})
	if err != nil || len(hits) == 0 {
		t.Fatalf("the archived fact is gone from search: %v %v", hits, err)
	}
	if err := CheckAlternation(ag.History()); err != nil {
		t.Fatal(err)
	}

	// A restart reads the compacted transcript and the rebuilt prompt, and does not compact again.
	ag = build()
	res, err = ag.RunTurn(ctx, "and step 5?")
	if err != nil || res.FinalResponse != "answer two" || len(summarizer.calls) != 1 {
		t.Fatalf("%+v %v summaries=%d", res, err, len(summarizer.calls))
	}
	if got := contentText(model.calls[1].Messages[0].Content); got != system {
		t.Fatal("the system prompt changed on resume")
	}
	if len(model.calls[1].Messages) != len(live)+2 { // system + the live transcript + the new user message
		t.Fatalf("resumed request has %d messages, live had %d", len(model.calls[1].Messages), len(live))
	}

	// The session keeps growing: a second compaction updates the summary instead of stacking another.
	recordSession(t, st, sid, 30)
	ag = build()
	if _, err := ag.RunTurn(ctx, "one more"); err != nil {
		t.Fatal(err)
	}
	if len(summarizer.calls) != 2 {
		t.Fatalf("%d summary calls after the second growth", len(summarizer.calls))
	}
	if p := contentText(summarizer.calls[1].Messages[0].Content); !strings.Contains(p, "PREVIOUS SUMMARY:") || !strings.Contains(p, "ran make steps") {
		t.Fatalf("second summary did not build on the first: %q", clip(p))
	}
	summaries := 0
	for _, m := range ag.History() {
		if IsSummaryMessage(m) {
			summaries++
		}
	}
	if summaries != 1 {
		t.Fatalf("%d summaries in the transcript", summaries)
	}
	if got := contentText(model.calls[2].Messages[0].Content); strings.Count(got, prompttext.C.CompressionNote) != 1 {
		t.Fatal("the compaction note was stacked")
	}
	if err := CheckAlternation(ag.History()); err != nil {
		t.Fatal(err)
	}
}
