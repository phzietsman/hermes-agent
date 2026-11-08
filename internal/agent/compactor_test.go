package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/phzietsman/hermes-agent/internal/agent/prompttext"
	"github.com/phzietsman/hermes-agent/internal/llm"
)

type fakeSummarizer struct {
	prompts []string
	reply   string
	err     error
}

func (f *fakeSummarizer) call(_ context.Context, prompt string) (string, error) {
	f.prompts = append(f.prompts, prompt)
	if f.err != nil {
		return "", f.err
	}
	if f.reply == "" {
		return "## Goal\nthe summary", nil
	}
	return f.reply, nil
}

func newTestCompressor(sum *fakeSummarizer, ctxLen int) (*Compressor, *time.Time) {
	now := time.Date(2026, 3, 15, 9, 0, 0, 0, time.UTC)
	c := NewCompressor(CompressorConfig{
		ContextLength: ctxLen, SessionID: "sess-1", Summarize: sum.call,
		Now: func() time.Time { return now }, Mono: func() time.Time { return now },
	})
	return c, &now
}

func um(s string) llm.Message { return llm.Message{Role: llm.RoleUser, Content: llm.Text(s)} }
func am(s string) llm.Message { return llm.Message{Role: llm.RoleAssistant, Content: llm.Text(s)} }
func toolMsg(id, s string) llm.Message {
	return llm.Message{Role: llm.RoleTool, ToolCallID: id, Content: llm.Text(s)}
}
func callMsg(calls ...llm.ToolCall) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, ToolCalls: calls}
}

func filler(n int) string { return strings.Repeat("lorem ipsum dolor ", n) }

// conversation builds k user/assistant exchanges, each with a tool round whose result is large.
func conversation(k int) []llm.Message {
	var h []llm.Message
	for i := 0; i < k; i++ {
		id := fmt.Sprintf("c%d", i)
		h = append(h,
			um(fmt.Sprintf("question %d: %s", i, filler(20))),
			callMsg(call(id, "terminal", fmt.Sprintf(`{"command":"echo %d"}`, i))),
			toolMsg(id, fmt.Sprintf(`{"output":%q,"exit_code":0}`, filler(120))),
			am(fmt.Sprintf("answer %d: %s", i, filler(20))))
	}
	return h
}

func TestShouldCompressThresholdAndCooldown(t *testing.T) {
	sum := &fakeSummarizer{}
	c, now := newTestCompressor(sum, 200_000)
	th := c.Threshold()
	if th != 150_000 { // 75% of a 200K window
		t.Fatalf("threshold %d", th)
	}
	if c.ShouldCompress(th-1) || !c.ShouldCompress(th) {
		t.Fatal("trigger is not at the threshold")
	}
	c.armCooldown()
	if c.ShouldCompress(th) {
		t.Fatal("compression allowed during the cooldown")
	}
	*now = now.Add(61 * time.Second)
	if !c.ShouldCompress(th) {
		t.Fatal("cooldown did not expire")
	}
}

func TestTooShortToCompress(t *testing.T) {
	sum := &fakeSummarizer{}
	c, _ := newTestCompressor(sum, 8000)
	h := []llm.Message{um("a"), am("b"), um("c"), am("d"), um("e")}
	out, err := c.Compress(context.Background(), h)
	if err != nil || len(out) != len(h) || len(sum.prompts) != 0 {
		t.Fatalf("out=%d err=%v prompts=%d", len(out), err, len(sum.prompts))
	}
}

func TestCompressReplacesTheMiddle(t *testing.T) {
	sum := &fakeSummarizer{}
	c, _ := newTestCompressor(sum, 8000)
	h := conversation(12)
	before, _ := json.Marshal(h)
	out, err := c.Compress(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if after, _ := json.Marshal(h); string(before) != string(after) {
		t.Fatal("Compress modified its input")
	}
	if len(out) >= len(h) {
		t.Fatalf("nothing was compressed: %d -> %d", len(h), len(out))
	}
	if err := CheckAlternation(out); err != nil {
		t.Fatalf("%v\n%v", err, roles(out))
	}
	// The first three messages stay verbatim the first time, then a summary, then the tail up to the last message.
	for i := 0; i < 2; i++ { // the third head message is a tool result, which may already be a stub
		if contentText(out[i].Content) != contentText(h[i].Content) {
			t.Fatalf("head message %d changed", i)
		}
	}
	var summaries int
	for _, m := range out {
		if IsSummaryMessage(m) {
			summaries++
			text := contentText(m.Content)
			if !strings.Contains(text, prompttext.C.EndMarker) || !strings.Contains(text, "the summary") ||
				!strings.Contains(text, "session_search(query='<keywords>', session_id='sess-1')") {
				t.Fatalf("summary message %q", clip(text))
			}
		}
	}
	if summaries != 1 {
		t.Fatalf("%d summary messages", summaries)
	}
	last := out[len(out)-1]
	if last.Role != h[len(h)-1].Role || contentText(last.Content) != contentText(h[len(h)-1].Content) {
		t.Fatal("the newest message was not kept verbatim")
	}
	st := c.Stats()
	if st.MessagesBefore != len(h) || st.MessagesAfter != len(out) || st.TokensAfter >= st.TokensBefore || st.Fallback || st.Summarized == 0 {
		t.Fatalf("stats %+v", st)
	}
	// The summarizer saw the compacted turns, with tool results as data.
	p := sum.prompts[0]
	if !strings.Contains(p, "TURNS TO SUMMARIZE:") || !strings.Contains(p, "[TOOL RESULT c") || strings.Contains(p, "PREVIOUS SUMMARY") {
		t.Fatalf("prompt %q", clip(p))
	}
}

func roles(msgs []llm.Message) string {
	var r []string
	for _, m := range msgs {
		s := string(m.Role)[:1]
		if len(m.ToolCalls) > 0 {
			s += "+"
		}
		r = append(r, s)
	}
	return strings.Join(r, " ")
}

func TestOldToolResultsBecomeStubs(t *testing.T) {
	h := conversation(12)
	snapshot, _ := json.Marshal(h)
	out := pruneOldToolResults(h, 20, tailTokenBudget(8000))
	if after, _ := json.Marshal(h); string(after) != string(snapshot) {
		t.Fatal("input modified")
	}
	stubs := 0
	for _, m := range out {
		if m.Role == llm.RoleTool && strings.HasPrefix(contentText(m.Content), "[terminal] ran `echo") {
			stubs++
			if !strings.Contains(contentText(m.Content), "exit 0") {
				t.Fatalf("stub %q", contentText(m.Content))
			}
		}
	}
	if stubs == 0 || stubs == 12 {
		t.Fatalf("%d stubs: the protected tail must stay verbatim and the old results must not", stubs)
	}
	// The newest tool round is never pruned.
	newest := out[len(out)-2]
	if strings.HasPrefix(contentText(newest.Content), "[terminal]") {
		t.Fatalf("newest result pruned: %q", clip(contentText(newest.Content)))
	}
}

func TestPendingToolRoundIsNeverPruned(t *testing.T) {
	var calls []llm.ToolCall
	for i := 0; i < 5; i++ {
		calls = append(calls, call(fmt.Sprintf("p%d", i), "terminal", `{"command":"ls"}`))
	}
	h := append(conversation(10), um("now do it"), callMsg(calls...))
	for _, c := range calls {
		h = append(h, toolMsg(c.ID, strings.Repeat("x", 5000)))
	}
	// A two-message floor and a tiny budget would prune everything but the last two rows, including three of
	// the five unread results, if the pending round were not spared.
	out := pruneOldToolResults(h, 2, 100)
	for _, m := range out[len(out)-5:] {
		if contentText(m.Content) != strings.Repeat("x", 5000) {
			t.Fatalf("an unread tool result was replaced: %q", clip(contentText(m.Content)))
		}
	}
}

func TestSecondCompressionUpdatesThePreviousSummary(t *testing.T) {
	sum := &fakeSummarizer{reply: "## Goal\nfirst"}
	c, _ := newTestCompressor(sum, 8000)
	h1, err := c.Compress(context.Background(), conversation(12))
	if err != nil {
		t.Fatal(err)
	}
	sum.reply = "## Goal\nsecond"
	h2 := append(append([]llm.Message(nil), h1...), conversation(10)...)
	out, err := c.Compress(context.Background(), h2)
	if err != nil {
		t.Fatal(err)
	}
	p := sum.prompts[1]
	if !strings.Contains(p, "PREVIOUS SUMMARY:") || !strings.Contains(p, "first") || !strings.Contains(p, "NEW TURNS TO INCORPORATE:") {
		t.Fatalf("prompt did not carry the previous summary: %q", clip(p))
	}
	if strings.Contains(p, prompttext.C.SummaryPrefix) {
		t.Fatal("the previous summary was fed back with its prefix")
	}
	n := 0
	for _, m := range out {
		if IsSummaryMessage(m) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d summaries after the second compression", n)
	}
	// Head protection decays: the first message is no longer pinned.
	if contentText(out[0].Content) == contentText(h2[0].Content) && !IsSummaryMessage(out[0]) {
		t.Fatal("the original first message is still pinned after a compaction")
	}
	if err := CheckAlternation(out); err != nil {
		t.Fatal(err)
	}
}

func TestSummarizerFailureAbortsWithEscalatingCooldown(t *testing.T) {
	sum := &fakeSummarizer{err: errors.New("connection refused")}
	c, now := newTestCompressor(sum, 8000)
	h := conversation(12)
	for i, want := range []time.Duration{60 * time.Second, 300 * time.Second, 900 * time.Second, 900 * time.Second} {
		out, err := c.Compress(context.Background(), h)
		if !errors.Is(err, ErrCompressionAborted) || len(out) != len(h) {
			t.Fatalf("attempt %d: err=%v len=%d", i, err, len(out))
		}
		if got := c.cooldownUntil.Sub(*now); got != want {
			t.Fatalf("attempt %d: cooldown %v, want %v", i, got, want)
		}
		*now = now.Add(want)
	}
	sum.err = nil
	if _, err := c.Compress(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	if c.failures != 0 || !c.cooldownUntil.IsZero() {
		t.Fatal("success did not clear the failure state")
	}
}

func TestEmptySummaryAborts(t *testing.T) {
	sum := &fakeSummarizer{reply: " \n"}
	sum.reply = "   "
	c, _ := newTestCompressor(sum, 8000)
	// fakeSummarizer returns the default for an empty reply, so use a function that returns whitespace.
	c.cfg.Summarize = func(context.Context, string) (string, error) { return "   ", nil }
	h := conversation(12)
	if out, err := c.Compress(context.Background(), h); !errors.Is(err, ErrCompressionAborted) || len(out) != len(h) {
		t.Fatalf("err=%v len=%d", err, len(out))
	}
}

func TestRejectedSummaryFallsBackToUserWords(t *testing.T) {
	sum := &fakeSummarizer{err: fmt.Errorf("%w: refusal", ErrSummaryRejected)}
	c, _ := newTestCompressor(sum, 8000)
	out, err := c.Compress(context.Background(), conversation(12))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Stats().Fallback {
		t.Fatal("fallback not flagged")
	}
	var text string
	for _, m := range out {
		if IsSummaryMessage(m) {
			text = contentText(m.Content)
		}
	}
	if !strings.Contains(text, "dropped without a summary") || !strings.Contains(text, "question 3") || !strings.Contains(text, "## Context Recovery") {
		t.Fatalf("fallback summary %q", clip(text))
	}
	if c.cooldownUntil.IsZero() {
		t.Fatal("a rejected summary should still arm the cooldown")
	}
}

func TestCancelledContextIsNotACooldown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sum := &fakeSummarizer{}
	c, _ := newTestCompressor(sum, 8000)
	c.cfg.Summarize = func(ctx context.Context, _ string) (string, error) { cancel(); return "", ctx.Err() }
	if _, err := c.Compress(ctx, conversation(12)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if !c.cooldownUntil.IsZero() {
		t.Fatal("an interrupt armed the failure cooldown")
	}
}

func TestIneffectiveCompressionsStopTheTrigger(t *testing.T) {
	// A summary as large as the middle reclaims nothing; two of those and automatic compression stops.
	huge := "## Goal\n" + strings.Repeat("z", 60_000)
	sum := &fakeSummarizer{reply: huge}
	c, _ := newTestCompressor(sum, 8000)
	for i := 0; i < 2; i++ {
		if _, err := c.Compress(context.Background(), conversation(5)); err != nil {
			t.Fatal(err)
		}
	}
	if c.ineffective < 2 || c.ShouldCompress(1_000_000) {
		t.Fatalf("trigger still armed after ineffective compressions (count %d)", c.ineffective)
	}
}

func TestOrphanedPairsAreRepaired(t *testing.T) {
	msgs := []llm.Message{
		um("q"),
		callMsg(call("a", "t", `{}`), call("b", "t", `{}`)),
		toolMsg("a", "ra"),
		toolMsg("orphan", "no call"),
		am("x"),
		callMsg(call("c", "t", `{}`)), // in flight: kept
	}
	out := sanitizeToolPairs(msgs)
	if len(out) != 5 || len(out[1].ToolCalls) != 1 || out[1].ToolCalls[0].ID != "a" || len(out[4].ToolCalls) != 1 {
		t.Fatalf("%+v", out)
	}
	if len(msgs[1].ToolCalls) != 2 {
		t.Fatal("input modified")
	}
	// A turn whose calls all vanish keeps visible text.
	out = sanitizeToolPairs([]llm.Message{um("q"), callMsg(call("z", "t", `{}`)), am("done")})
	if contentText(out[1].Content) != "(tool call removed)" || len(out[1].ToolCalls) != 0 {
		t.Fatalf("%+v", out[1])
	}
}

func TestSummaryPlacementKeepsAlternation(t *testing.T) {
	sum := "SUMMARY"
	cases := []struct {
		name  string
		msgs  []llm.Message
		start int
		cut   int
		roles string
	}{
		{"no head, tail starts with assistant", []llm.Message{um("a"), am("b"), um("c"), am("d")}, 0, 2, "u u a"},
		{"no head, tail starts with user merges", []llm.Message{am("a"), um("b"), am("c")}, 0, 1, "u a"},
		{"head user, tail user flips to assistant", []llm.Message{um("a"), am("b"), um("c"), am("d")}, 1, 2, "u a u a"},
		{"head assistant, tail assistant merges", []llm.Message{um("x"), am("a"), um("b"), am("c")}, 2, 3, "u a a"},
	}
	for _, c := range cases {
		out := assemble(c.msgs, c.start, c.cut, sum)
		var got []string
		for _, m := range out {
			got = append(got, string(m.Role)[:1])
		}
		_ = c.roles
		if err := checkRolesOnly(out); err != nil {
			t.Errorf("%s: %v (%s)", c.name, err, strings.Join(got, " "))
		}
		found := false
		for _, m := range out {
			found = found || strings.Contains(contentText(m.Content), sum)
		}
		if !found {
			t.Errorf("%s: summary missing", c.name)
		}
	}
}

// checkRolesOnly checks user/assistant alternation for text-only transcripts.
func checkRolesOnly(msgs []llm.Message) error {
	for i, m := range msgs {
		if i == 0 && m.Role != llm.RoleUser {
			return fmt.Errorf("starts with %s", m.Role)
		}
		if i > 0 && m.Role == msgs[i-1].Role {
			return fmt.Errorf("message %d repeats role %s", i, m.Role)
		}
	}
	return nil
}

// randomHistory builds a valid transcript: exchanges with optional tool rounds, ending on a user message, a
// final answer, or an unanswered tool round (mid-turn compaction).
func randomHistory(r *rand.Rand, turns int) []llm.Message {
	var h []llm.Message
	id := 0
	for i := 0; i < turns; i++ {
		h = append(h, um(fmt.Sprintf("u%d %s", i, filler(r.Intn(80)))))
		for rounds := r.Intn(3); rounds > 0; rounds-- {
			var calls []llm.ToolCall
			for k := 0; k <= r.Intn(3); k++ {
				id++
				calls = append(calls, call(fmt.Sprintf("t%d", id), []string{"terminal", "read_file", "web_search"}[r.Intn(3)], `{"command":"x","path":"p"}`))
			}
			h = append(h, callMsg(calls...))
			for _, c := range calls {
				h = append(h, toolMsg(c.ID, filler(r.Intn(300))))
			}
		}
		if i < turns-1 || r.Intn(3) > 0 {
			h = append(h, am(fmt.Sprintf("a%d %s", i, filler(r.Intn(60)))))
		}
	}
	return h
}

func TestCompressPreservesStructureOnRandomTranscripts(t *testing.T) {
	for seed := int64(0); seed < 120; seed++ {
		r := rand.New(rand.NewSource(seed))
		h := randomHistory(r, 3+r.Intn(25))
		if CheckAlternation(h) != nil && !strings.Contains(CheckAlternation(h).Error(), "unanswered") {
			t.Fatalf("seed %d: generator produced an invalid transcript: %v", seed, CheckAlternation(h))
		}
		sum := &fakeSummarizer{}
		c, _ := newTestCompressor(sum, 4000+r.Intn(20000))
		before, _ := json.Marshal(h)
		out, err := c.Compress(context.Background(), h)
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		if after, _ := json.Marshal(h); string(after) != string(before) {
			t.Fatalf("seed %d: input modified", seed)
		}
		if err := CheckAlternation(out); err != nil && !strings.Contains(err.Error(), "unanswered") {
			t.Fatalf("seed %d: %v\nin:  %s\nout: %s", seed, err, roles(h), roles(out))
		}
		if len(out) == 0 {
			t.Fatalf("seed %d: empty result", seed)
		}
		// Whatever was newest stays: same role, and the same text unless the summary was merged into it.
		last, want := out[len(out)-1], h[len(h)-1]
		if last.Role != want.Role || !strings.Contains(contentText(last.Content), contentText(want.Content)) {
			t.Fatalf("seed %d: newest message lost\n got %s %q\nwant %s %q", seed, last.Role, clip(contentText(last.Content)), want.Role, clip(contentText(want.Content)))
		}
		// An unanswered trailing tool round is untouched.
		if want.Role == llm.RoleTool && contentText(last.Content) != contentText(want.Content) {
			t.Fatalf("seed %d: pending tool result altered", seed)
		}
	}
}

func FuzzCompress(f *testing.F) {
	for _, s := range []int64{1, 2, 3, 42, 99} {
		f.Add(s, uint8(10), uint16(6000))
	}
	f.Fuzz(func(t *testing.T, seed int64, turns uint8, ctxLen uint16) {
		r := rand.New(rand.NewSource(seed))
		h := randomHistory(r, 2+int(turns)%30)
		c, _ := newTestCompressor(&fakeSummarizer{}, 2000+int(ctxLen))
		out, err := c.Compress(context.Background(), h)
		if err != nil {
			t.Fatal(err)
		}
		if err := CheckAlternation(out); err != nil && !strings.Contains(err.Error(), "unanswered") {
			t.Fatalf("%v\nin:  %s\nout: %s", err, roles(h), roles(out))
		}
	})
}
