package session

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestAppendMessageRoundTripAndCounters(t *testing.T) {
	s := open(t)
	mustSession(t, s, "a")

	calls := json.RawMessage(`[{"id":"t1","type":"function","function":{"name":"read_file","arguments":"{}"}},{"id":"t2","type":"function","function":{"name":"terminal","arguments":"{}"}}]`)
	id1, err := s.AppendMessage(ctx, Message{SessionID: "a", Role: "user", Content: "hi", PlatformMessageID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.AppendMessage(ctx, Message{
		SessionID: "a", Role: "assistant", ToolCalls: calls, TokenCount: 42, FinishReason: "tool_calls",
		Reasoning: "think", ReasoningContent: "think more", ReasoningDetails: json.RawMessage(`[{"type":"thinking"}]`),
		APIContent: "exact bytes sent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(ctx, Message{SessionID: "a", Role: "tool", ToolCallID: "t1", ToolName: "read_file", Content: "file body"}); err != nil {
		t.Fatal(err)
	}
	if id2 <= id1 {
		t.Fatalf("ids not increasing: %d then %d", id1, id2)
	}

	sess, _ := s.GetSession(ctx, "a")
	if sess.MessageCount != 3 || sess.ToolCallCount != 2 {
		t.Fatalf("counters = %d messages, %d tool calls; want 3, 2", sess.MessageCount, sess.ToolCallCount)
	}

	msgs, err := s.Messages(ctx, "a", ListOptions{})
	if err != nil || len(msgs) != 3 {
		t.Fatalf("messages = %d, %v", len(msgs), err)
	}
	user, asst, tool := msgs[0], msgs[1], msgs[2]
	if user.Content != "hi" || user.PlatformMessageID != "p1" || !user.Active || user.Compacted || user.CompressedSummary {
		t.Fatalf("user row: %+v", user)
	}
	if asst.Content != "" || string(asst.ToolCalls) != string(calls) || asst.TokenCount != 42 || asst.FinishReason != "tool_calls" ||
		asst.Reasoning != "think" || asst.ReasoningContent != "think more" || string(asst.ReasoningDetails) != `[{"type":"thinking"}]` ||
		asst.APIContent != "exact bytes sent" {
		t.Fatalf("assistant row: %+v", asst)
	}
	if tool.ToolCallID != "t1" || tool.ToolName != "read_file" || tool.ToolCalls != nil {
		t.Fatalf("tool row: %+v", tool)
	}
	if user.Timestamp == 0 {
		t.Fatal("timestamp not defaulted")
	}
}

func TestAppendMessageValidation(t *testing.T) {
	s := open(t)
	mustSession(t, s, "a")
	for _, m := range []Message{{Role: "user"}, {SessionID: "a"}} {
		if _, err := s.AppendMessage(ctx, m); err == nil {
			t.Errorf("AppendMessage(%+v) should fail", m)
		}
	}
	if _, err := s.AppendMessage(ctx, Message{SessionID: "missing", Role: "user", Content: "x"}); err == nil {
		t.Error("a message for a missing session must violate the foreign key")
	}
	// A non-array tool_calls value counts as zero calls rather than failing the append.
	if _, err := s.AppendMessage(ctx, Message{SessionID: "a", Role: "assistant", ToolCalls: json.RawMessage(`{"not":"array"}`)}); err != nil {
		t.Fatal(err)
	}
	if sess, _ := s.GetSession(ctx, "a"); sess.ToolCallCount != 0 {
		t.Fatalf("tool_call_count = %d", sess.ToolCallCount)
	}
}

func TestMessagesOrderIsByIDNotTimestamp(t *testing.T) {
	s := open(t)
	mustSession(t, s, "a")
	for i, ts := range []float64{300, 100, 200} { // a clock that went backwards
		if _, err := s.AppendMessage(ctx, Message{SessionID: "a", Role: "user", Content: string(rune('a' + i)), Timestamp: ts}); err != nil {
			t.Fatal(err)
		}
	}
	msgs, _ := s.Messages(ctx, "a", ListOptions{})
	var got []string
	for _, m := range msgs {
		got = append(got, m.Content)
	}
	if !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("order = %v", got)
	}
}

func TestMessagesVisibility(t *testing.T) {
	s := open(t)
	mustSession(t, s, "a")
	var ids []int64
	for _, c := range []string{"live", "rewound", "archived", "live2"} {
		id, err := s.AppendMessage(ctx, Message{SessionID: "a", Role: "user", Content: c})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	mustExec(t, s, `UPDATE messages SET active = 0 WHERE id = ?`, ids[1])                // rewound
	mustExec(t, s, `UPDATE messages SET active = 0, compacted = 1 WHERE id = ?`, ids[2]) // archived by compaction

	contents := func(o ListOptions) []string {
		msgs, err := s.Messages(ctx, "a", o)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, m := range msgs {
			out = append(out, m.Content)
		}
		return out
	}
	tests := []struct {
		name string
		o    ListOptions
		want []string
	}{
		{"default is live only", ListOptions{}, []string{"live", "live2"}},
		{"display adds archived but never rewound", ListOptions{IncludeCompacted: true}, []string{"live", "archived", "live2"}},
		{"audit is everything", ListOptions{IncludeInactive: true}, []string{"live", "rewound", "archived", "live2"}},
	}
	for _, tc := range tests {
		if got := contents(tc.o); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestMessagesPaging(t *testing.T) {
	s := open(t)
	mustSession(t, s, "a")
	var ids []int64
	for i := 0; i < 6; i++ {
		id, _ := s.AppendMessage(ctx, Message{SessionID: "a", Role: "user", Content: string(rune('a' + i))})
		ids = append(ids, id)
	}
	names := func(o ListOptions) string {
		msgs, err := s.Messages(ctx, "a", o)
		if err != nil {
			t.Fatal(err)
		}
		var b []byte
		for _, m := range msgs {
			b = append(b, m.Content...)
		}
		return string(b)
	}
	tests := []struct {
		name string
		o    ListOptions
		want string
	}{
		{"all", ListOptions{}, "abcdef"},
		{"limit", ListOptions{Limit: 2}, "ab"},
		{"offset without limit", ListOptions{Offset: 4}, "ef"},
		{"limit and offset", ListOptions{Limit: 2, Offset: 2}, "cd"},
		{"latest is still chronological", ListOptions{Latest: true, Limit: 3}, "def"},
		{"latest with offset", ListOptions{Latest: true, Limit: 2, Offset: 2}, "cd"},
		{"after id", ListOptions{AfterID: ids[2]}, "def"},
		{"after id with limit", ListOptions{AfterID: ids[2], Limit: 1}, "d"},
		{"past the end", ListOptions{Offset: 99}, ""},
	}
	for _, tc := range tests {
		if got := names(tc.o); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
	for _, bad := range []ListOptions{{AfterID: 1, Latest: true}, {AfterID: 1, Offset: 1}} {
		if _, err := s.Messages(ctx, "a", bad); err == nil {
			t.Errorf("Messages(%+v) should reject keyset paging combined with offset/latest", bad)
		}
	}
	if msgs, err := s.Messages(ctx, "no-such-session", ListOptions{}); err != nil || len(msgs) != 0 {
		t.Fatalf("unknown session = %v, %v", msgs, err)
	}
}

func TestCompressedSummaryFlagRoundTrips(t *testing.T) {
	s := open(t)
	mustSession(t, s, "a")
	if _, err := s.AppendMessage(ctx, Message{SessionID: "a", Role: "user", Content: "[summary]", CompressedSummary: true}); err != nil {
		t.Fatal(err)
	}
	msgs, _ := s.Messages(ctx, "a", ListOptions{})
	if len(msgs) != 1 || !msgs[0].CompressedSummary {
		t.Fatalf("summary flag lost: %+v", msgs)
	}
}

func mustExec(t *testing.T, s *Store, q string, args ...any) {
	t.Helper()
	if _, err := s.db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}
