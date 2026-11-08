package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/phzietsman/hermes-agent/internal/llm"
	"github.com/phzietsman/hermes-agent/internal/session"
)

type fakeMemory struct {
	recall   string
	prefetch []string
	synced   [][2]string
	pre      [][]llm.Message
}

func (f *fakeMemory) Prefetch(_ context.Context, q string) string {
	f.prefetch = append(f.prefetch, q)
	return f.recall
}
func (f *fakeMemory) SyncTurn(_ context.Context, u, a string) { f.synced = append(f.synced, [2]string{u, a}) }
func (f *fakeMemory) PreCompress(_ context.Context, h []llm.Message) {
	f.pre = append(f.pre, append([]llm.Message(nil), h...))
}

func lastUserText(in CallInput) string {
	for i := len(in.Messages) - 1; i >= 0; i-- {
		if in.Messages[i].Role == llm.RoleUser {
			return contentText(in.Messages[i].Content)
		}
	}
	return ""
}

func TestRecallJoinsTheUserMessageOfEveryCallInTheTurnButIsNeverStored(t *testing.T) {
	r := newRig(t, use(call("c1", "echo", `{}`)), llm.Response{Content: strp("done"), FinishReason: "stop"})
	mem := &fakeMemory{recall: "## Recalled memory\n- likes tea (0.90)"}
	r.cfg.Memory = mem
	r.rebuild()
	res := r.turn("what do I drink?")
	if !res.Completed {
		t.Fatalf("%+v", res)
	}
	if len(r.prov.calls) != 2 {
		t.Fatalf("%d calls", len(r.prov.calls))
	}
	want := "what do I drink?\n\n## Recalled memory\n- likes tea (0.90)"
	for i, c := range r.prov.calls {
		if got := lastUserText(c); got != want {
			t.Errorf("call %d user message %q", i, got)
		}
	}
	// The stored transcript, and so every later cached prefix, carries the plain text.
	rows, _ := r.store.Messages(context.Background(), "s1", session.ListOptions{})
	if rows[0].Role != "user" || rows[0].Content != "what do I drink?" {
		t.Errorf("stored user message %q", rows[0].Content)
	}
	for _, m := range r.agent.History() {
		if strings.Contains(contentText(m.Content), "Recalled memory") {
			t.Error("recall leaked into the history")
		}
	}
	if len(mem.prefetch) != 1 || mem.prefetch[0] != "what do I drink?" {
		t.Errorf("prefetch asked %v, want once per turn", mem.prefetch)
	}
	if len(mem.synced) != 1 || mem.synced[0] != [2]string{"what do I drink?", "done"} {
		t.Errorf("synced %v", mem.synced)
	}
}

func TestNoRecallLeavesTheRequestAlone(t *testing.T) {
	r := newRig(t, llm.Response{Content: strp("ok"), FinishReason: "stop"})
	r.cfg.Memory = &fakeMemory{}
	r.rebuild()
	r.turn("hello")
	if got := lastUserText(r.prov.calls[0]); got != "hello" {
		t.Errorf("%q", got)
	}
}

func TestOnlyCompletedTurnsAreSynced(t *testing.T) {
	r := newRig(t, llm.Response{FinishReason: "stop"}, llm.Response{FinishReason: "stop"}, llm.Response{FinishReason: "stop"}, llm.Response{FinishReason: "stop"})
	mem := &fakeMemory{}
	r.cfg.Memory = mem
	r.rebuild()
	r.turn("hi") // the model returns nothing, repeatedly: the turn fails
	if len(mem.synced) != 0 {
		t.Errorf("an unfinished turn was synced: %v", mem.synced)
	}
}

func strp(s string) *string { return &s }

type noopCompactor struct{}

func (noopCompactor) ShouldCompress(int) bool { return false }
func (noopCompactor) Compress(_ context.Context, h []llm.Message) ([]llm.Message, error) {
	return h, nil
}

func TestPreCompressSeesTheTranscriptBeforeItIsReplaced(t *testing.T) {
	r := newRig(t, llm.Response{Content: strp("answer"), FinishReason: "stop"})
	mem := &fakeMemory{}
	r.cfg.Memory, r.cfg.Compactor = mem, noopCompactor{}
	r.rebuild()
	r.turn("question")
	if err := r.agent.compress(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(mem.pre) != 1 || len(mem.pre[0]) != 2 || contentText(mem.pre[0][0].Content) != "question" {
		t.Errorf("PreCompress saw %v", mem.pre)
	}
}
