package sessiontools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/phzietsman/hermes-agent/internal/llm"
	"github.com/phzietsman/hermes-agent/internal/session"
	"github.com/phzietsman/hermes-agent/internal/tools"
)

type fx struct {
	t   *testing.T
	st  *session.Store
	tb  *tools.Toolbox
	ids map[string]int64
}

func newFx(t *testing.T) *fx {
	st, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	reg := tools.NewRegistry()
	Register(reg, st)
	return &fx{t: t, st: st, tb: reg.Build(tools.Selection{}), ids: map[string]int64{}}
}

func (f *fx) seed(id, title string, msgs ...string) {
	ctx := context.Background()
	if err := f.st.CreateSession(ctx, session.NewSession{ID: id, Source: "cli", ProfileName: "work"}); err != nil {
		f.t.Fatal(err)
	}
	if title != "" {
		if err := f.st.SetTitle(ctx, id, title); err != nil {
			f.t.Fatal(err)
		}
	}
	for i, c := range msgs {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		mid, err := f.st.AppendMessage(ctx, session.Message{SessionID: id, Role: role, Content: c})
		if err != nil {
			f.t.Fatal(err)
		}
		f.ids[id+"#"+string(rune('0'+i))] = mid
	}
}

func (f *fx) call(current string, args map[string]any) map[string]any {
	f.t.Helper()
	b, _ := json.Marshal(args)
	ctx := tools.WithEnv(context.Background(), tools.CallEnv{SessionID: current})
	out, _ := f.tb.Run(ctx, llm.ToolCall{Function: llm.ToolCallFunction{Name: "session_search", Arguments: string(b)}})
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		f.t.Fatalf("not JSON: %q", out)
	}
	return m
}

func TestDiscoverHydratesTopAndExcludesCurrent(t *testing.T) {
	f := newFx(t)
	f.seed("old1", "Docker talk", "configure docker networking", "use a bridge network", "thanks")
	f.seed("old2", "", "docker volumes question")
	f.seed("live", "", "docker docker docker")
	r := f.call("live", map[string]any{"query": "docker", "limit": 5})
	res := r["results"].([]any)
	if r["mode"] != "discover" || len(res) != 2 {
		t.Fatalf("%v", r)
	}
	for _, x := range res {
		if x.(map[string]any)["session_id"] == "live" {
			t.Error("current session must be excluded from recall")
		}
	}
	top := res[0].(map[string]any)
	if top["messages"] == nil || top["link"] != "@session:work/"+top["session_id"].(string) {
		t.Errorf("top result not hydrated or link wrong: %v", top)
	}
	if res[1].(map[string]any)["messages"] != nil {
		t.Error("adaptive detail hydrates only the top result")
	}
	if full := f.call("live", map[string]any{"query": "docker", "detail": "full"}); full["results"].([]any)[1].(map[string]any)["messages"] == nil {
		t.Error("detail=full hydrates every result")
	}
}

func TestDiscoverNoMatchAndBadBounds(t *testing.T) {
	f := newFx(t)
	f.seed("a", "", "hello world")
	if r := f.call("", map[string]any{"query": "zzzzqqq"}); r["count"] != float64(0) || r["message"] != "No matching sessions found." {
		t.Errorf("%v", r)
	}
	if r := f.call("", map[string]any{"query": "hello", "after": "yesterday-ish"}); r["success"] != false {
		t.Errorf("%v", r)
	}
	if r := f.call("", map[string]any{"query": "hello", "after": "7d"}); r["count"] != float64(1) {
		t.Errorf("a relative lower bound includes a session started now: %v", r)
	}
	if r := f.call("", map[string]any{"query": "hello", "before": "2001-01-01"}); r["count"] != float64(0) {
		t.Errorf("upper bound must exclude newer sessions: %v", r)
	}
}

func TestScrollAndRead(t *testing.T) {
	f := newFx(t)
	f.seed("s", "", "m0", "m1", "m2", "m3", "m4", "m5", "m6")
	anchor := f.ids["s#3"]
	r := f.call("", map[string]any{"session_id": "s", "around_message_id": anchor, "window": 1})
	msgs := r["messages"].([]any)
	if len(msgs) != 3 || msgs[1].(map[string]any)["content"] != "m3" || r["has_more_before"] != true || r["has_more_after"] != true {
		t.Fatalf("%v", r)
	}
	if r := f.call("", map[string]any{"session_id": "s", "around_message_id": 999999}); r["success"] != false {
		t.Errorf("unknown anchor: %v", r)
	}
	if r := f.call("", map[string]any{"session_id": "nope"}); r["success"] != false {
		t.Errorf("unknown session: %v", r)
	}
	if r := f.call("", map[string]any{"session_id": "s"}); r["mode"] != "read" || r["message_count"] != float64(7) {
		t.Errorf("%v", r)
	}
}

func TestReadCapsLongSessionsAndClipsMessages(t *testing.T) {
	f := newFx(t)
	many := make([]string, maxReadMsgs+5)
	for i := range many {
		many[i] = "x"
	}
	many[0] = strings.Repeat("y", msgChars+100)
	f.seed("big", "", many...)
	r := f.call("", map[string]any{"session_id": "big"})
	msgs := r["messages"].([]any)
	if len(msgs) != maxReadMsgs || r["note"] == nil {
		t.Fatalf("len=%d note=%v", len(msgs), r["note"])
	}
	if c := msgs[0].(map[string]any)["content"].(string); !strings.HasSuffix(c, "[truncated]") {
		t.Error("long message not clipped")
	}
}

func TestBrowseListsRecentExcludingCurrent(t *testing.T) {
	f := newFx(t)
	f.seed("a", "", "one")
	f.seed("b", "", "two")
	f.seed("cur", "", "three")
	r := f.call("cur", map[string]any{})
	if r["mode"] != "browse" || r["count"] != float64(2) {
		t.Errorf("%v", r)
	}
}

func TestArchivedRowsStaySearchableAndTokensAreRedacted(t *testing.T) {
	f := newFx(t)
	f.seed("c", "", "the deploy key is sk-ant-api03-abcdefghijklmnopqrstuvwx and we use zebrafish")
	ctx := context.Background()
	rows, _ := f.st.Messages(ctx, "c", session.ListOptions{})
	if err := f.st.ReplaceLive(ctx, "c", []session.Message{{SessionID: "c", Role: "user", Content: "summary only"}}); err != nil {
		t.Fatal(err)
	}
	_ = rows
	r := f.call("", map[string]any{"query": "zebrafish"})
	if r["count"] != float64(1) {
		t.Fatalf("compacted history must stay recoverable: %v", r)
	}
	if blob, _ := json.Marshal(r); strings.Contains(string(blob), "abcdefghijklmnopqrstuvwx") {
		t.Errorf("credential leaked through recall: %s", blob)
	}
}

func TestParseBound(t *testing.T) {
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		in   string
		want time.Time
		bad  bool
	}{
		{"2026-06-01", time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), false},
		{"2026-06-01T08:30:00", time.Date(2026, 6, 1, 8, 30, 0, 0, time.UTC), false},
		{"2026-06-01T08:30:00Z", time.Date(2026, 6, 1, 8, 30, 0, 0, time.UTC), false},
		{"7d", now.Add(-7 * 24 * time.Hour), false},
		{"24H", now.Add(-24 * time.Hour), false},
		{"2w", now.Add(-14 * 24 * time.Hour), false},
		{"soon", time.Time{}, true},
		{"", time.Time{}, true},
	}
	for _, c := range cases {
		got, err := parseBound(c.in, now)
		if (err != nil) != c.bad {
			t.Errorf("%q: err = %v", c.in, err)
			continue
		}
		if !c.bad && got != float64(c.want.UnixNano())/1e9 {
			t.Errorf("%q = %v, want %v", c.in, got, c.want)
		}
	}
}

func FuzzParseBound(f *testing.F) {
	for _, s := range []string{"7d", "2026-01-01", "", "99999999999999999999d", "-1d"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) { _, _ = parseBound(s, time.Unix(1_800_000_000, 0)) })
}
