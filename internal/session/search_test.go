package session

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestSanitizeQuery(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"plain", "docker networking", "docker networking"},
		{"special chars become spaces", "TODO: fix (this)!", "TODO  fix  this"},
		{"hyphenated term is quoted", "chat-send", `"chat-send"`},
		{"dotted term is quoted", "P2.2 my-app.config.ts", `"P2.2" "my-app.config.ts"`},
		{"underscore term is quoted", "session_search", `"session_search"`},
		{"quoted phrase kept", `"exact phrase" other`, `"exact phrase" other`},
		{"quoted phrase with specials untouched", `"a:b (c)" x`, `"a:b (c)" x`},
		{"unmatched quote dropped", `say "hello`, "say  hello"},
		{"prefix star kept", "nimb*", "nimb*"},
		{"repeated stars collapse", "nimb***", "nimb*"},
		{"leading star dropped", "*foo bar", "foo bar"},
		{"dangling leading operator", "AND foo", "foo"},
		{"dangling trailing operator", "foo OR", "foo"},
		{"operators in the middle stay", "foo AND bar NOT baz", "foo AND bar NOT baz"},
		{"percent stripped for fts", "100% sure", "100  sure"},
		{"percent kept for cjk", "100% 你好", "100% 你好"},
		{"only specials", "()[]{}", ""},
		{"whitespace only", "  \t ", ""},
		{"nul bytes cannot forge a placeholder", "a\x00Q0\x00 \"x y\"", `a Q0  "x y"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeQuery(tc.in); got != tc.want {
				t.Fatalf("sanitizeQuery(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSanitizeQueryCapsLength(t *testing.T) {
	got := sanitizeQuery(strings.Repeat("a", maxQueryChars+500))
	if len(got) != maxQueryChars {
		t.Fatalf("len = %d, want %d", len(got), maxQueryChars)
	}
	// Truncation counts characters, not bytes, so it never splits a rune.
	cjk := sanitizeQuery(strings.Repeat("你", maxQueryChars+10))
	if n := len([]rune(cjk)); n != maxQueryChars {
		t.Fatalf("rune len = %d", n)
	}
}

func TestOrRelaxed(t *testing.T) {
	tests := []struct{ in, want string }{
		{"a b c", "a OR b OR c"},
		{`"docker networking" tls`, `"docker networking" OR tls`},
		{"a AND b", "a OR b"},
		{"single", ""},
		{"a OR b", ""},
		{"a NOT b", ""},
		{"", ""},
	}
	for _, tc := range tests {
		if got := orRelaxed(tc.in); got != tc.want {
			t.Errorf("orRelaxed(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// seed builds a store with sessions and messages the search tests share.
func seed(t *testing.T) *Store {
	t.Helper()
	s := open(t)
	for _, n := range []NewSession{
		{ID: "cli1", Source: "cli", Model: "claude"},
		{ID: "tg1", Source: "telegram", Model: "claude"},
		{ID: "cron1", Source: "cron", Model: "claude"},
	} {
		if err := s.CreateSession(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(t, s, `UPDATE sessions SET started_at = 1000 WHERE id = 'cli1'`)
	mustExec(t, s, `UPDATE sessions SET started_at = 2000 WHERE id = 'tg1'`)
	mustExec(t, s, `UPDATE sessions SET started_at = 3000 WHERE id = 'cron1'`)
	add := func(sess, role, content string, ts float64) int64 {
		id, err := s.AppendMessage(ctx, Message{SessionID: sess, Role: role, Content: content, Timestamp: ts})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	add("cli1", "user", "How do I configure docker networking for the staging cluster?", 10)
	add("cli1", "assistant", "Use a user-defined bridge network and attach the containers to it.", 20)
	add("tg1", "user", "Sarah prefers the standup meeting scheduled for Thursday mornings", 30)
	add("tg1", "assistant", "Noted: the chat-send helper retries three times.", 40)
	add("cron1", "assistant", "nightly report finished", 50)
	return s
}

func ids(hits []Hit) []int64 {
	out := make([]int64, len(hits))
	for i, h := range hits {
		out[i] = h.MessageID
	}
	return out
}

func sessionsOf(hits []Hit) []string {
	var out []string
	for _, h := range hits {
		out = append(out, h.SessionID)
	}
	sort.Strings(out)
	return out
}

func TestSearchRoutesAndOperators(t *testing.T) {
	s := seed(t)
	tests := []struct {
		name  string
		query string
		want  []string // sessions, sorted
	}{
		{"keyword", "docker", []string{"cli1"}},
		{"implicit AND", "docker staging", []string{"cli1"}},
		{"phrase", `"bridge network"`, []string{"cli1"}},
		{"prefix", "netw*", []string{"cli1", "cli1"}},
		{"OR", "docker OR standup", []string{"cli1", "tg1"}},
		{"NOT", "docker NOT staging", nil},
		{"hyphenated term", "chat-send", []string{"tg1"}},
		{"special characters do not error", "TODO: fix (docker)!", []string{"cli1"}}, // misses as AND, found by the OR retry
		{"case insensitive", "DOCKER", []string{"cli1"}},
		{"no match", "kubernetes", nil},
		{"empty", "", nil},
		{"only punctuation", "?!()", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hits, err := s.Search(ctx, SearchQuery{Query: tc.query})
			if err != nil {
				t.Fatalf("Search error: %v", err)
			}
			if got := sessionsOf(hits); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("sessions = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSearchHitShape(t *testing.T) {
	s := seed(t)
	hits, err := s.Search(ctx, SearchQuery{Query: "standup"})
	if err != nil || len(hits) != 1 {
		t.Fatalf("hits = %v, %v", hits, err)
	}
	h := hits[0]
	if h.SessionID != "tg1" || h.Role != "user" || h.Source != "telegram" || h.Model != "claude" || h.Timestamp != 30 || h.SessionStarted != 2000 {
		t.Fatalf("metadata wrong: %+v", h)
	}
	if !strings.Contains(h.Snippet, ">>>standup<<<") {
		t.Fatalf("snippet should mark the match: %q", h.Snippet)
	}
}

func TestSearchORRelaxedFindsParaphrase(t *testing.T) {
	s := seed(t)
	// "when does Sarah like her standup scheduled" shares words with the stored sentence but not all:
	// the exact AND query misses, the relaxed retry finds it.
	hits, err := s.Search(ctx, SearchQuery{Query: "when does Sarah like her standup scheduled"})
	if err != nil || len(hits) != 1 || hits[0].SessionID != "tg1" {
		t.Fatalf("hits = %+v, %v", hits, err)
	}
	// An explicit NOT means exact semantics: no relaxation.
	hits, _ = s.Search(ctx, SearchQuery{Query: "standup NOT sarah"})
	if len(hits) != 0 {
		t.Fatalf("explicit NOT must not relax: %+v", hits)
	}
	// Exact hits keep exact semantics: relaxation only runs on a miss.
	hits, _ = s.Search(ctx, SearchQuery{Query: "docker networking"})
	if len(hits) != 1 {
		t.Fatalf("exact query matched %d, want 1", len(hits))
	}
}

func TestSearchFilters(t *testing.T) {
	s := seed(t)
	has := func(q SearchQuery) []string {
		hits, err := s.Search(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		return sessionsOf(hits)
	}
	two := func(v float64) *float64 { return &v }

	if got := has(SearchQuery{Query: "docker OR standup OR nightly", Sources: []string{"telegram"}}); !reflect.DeepEqual(got, []string{"tg1"}) {
		t.Errorf("source filter: %v", got)
	}
	if got := has(SearchQuery{Query: "docker OR standup OR nightly", ExcludeSources: []string{"cron", "cli"}}); !reflect.DeepEqual(got, []string{"tg1"}) {
		t.Errorf("exclude filter: %v", got)
	}
	if got := has(SearchQuery{Query: "docker OR standup OR bridge", Roles: []string{"assistant"}}); !reflect.DeepEqual(got, []string{"cli1"}) {
		t.Errorf("role filter: %v", got)
	}
	// AfterTS is inclusive and BeforeTS exclusive on the session start.
	if got := has(SearchQuery{Query: "docker OR standup OR nightly", AfterTS: two(2000)}); !reflect.DeepEqual(got, []string{"cron1", "tg1"}) {
		t.Errorf("after (inclusive): %v", got)
	}
	if got := has(SearchQuery{Query: "docker OR standup OR nightly", BeforeTS: two(2000)}); !reflect.DeepEqual(got, []string{"cli1"}) {
		t.Errorf("before (exclusive): %v", got)
	}
	if got := has(SearchQuery{Query: "docker OR standup OR nightly", AfterTS: two(1500), BeforeTS: two(2500)}); !reflect.DeepEqual(got, []string{"tg1"}) {
		t.Errorf("window: %v", got)
	}
}

func TestSearchVisibilityOfRewoundAndCompactedRows(t *testing.T) {
	s := open(t)
	mustSession(t, s, "a")
	var ids [3]int64
	for i, c := range []string{"zebra live", "zebra rewound", "zebra archived"} {
		ids[i], _ = s.AppendMessage(ctx, Message{SessionID: "a", Role: "user", Content: c})
	}
	mustExec(t, s, `UPDATE messages SET active = 0 WHERE id = ?`, ids[1])
	mustExec(t, s, `UPDATE messages SET active = 0, compacted = 1 WHERE id = ?`, ids[2])

	hits, _ := s.Search(ctx, SearchQuery{Query: "zebra", Sort: "oldest"})
	if got := len(hits); got != 2 || hits[0].MessageID != ids[0] || hits[1].MessageID != ids[2] {
		t.Fatalf("default search = %v, want live and archived only", ids)
	}
	hits, _ = s.Search(ctx, SearchQuery{Query: "zebra", IncludeInactive: true})
	if len(hits) != 3 {
		t.Fatalf("include inactive = %d, want 3", len(hits))
	}
}

func TestSearchSortLimitOffset(t *testing.T) {
	s := open(t)
	mustSession(t, s, "a")
	var want []int64
	for i := 0; i < 5; i++ {
		id, _ := s.AppendMessage(ctx, Message{SessionID: "a", Role: "user", Content: "needle", Timestamp: float64(100 + i)})
		want = append(want, id)
	}
	newest, _ := s.Search(ctx, SearchQuery{Query: "needle", Sort: "newest"})
	oldest, _ := s.Search(ctx, SearchQuery{Query: "needle", Sort: "oldest"})
	if got := ids(newest); got[0] != want[4] || got[4] != want[0] {
		t.Errorf("newest order = %v", got)
	}
	if got := ids(oldest); got[0] != want[0] || got[4] != want[4] {
		t.Errorf("oldest order = %v", got)
	}
	page, _ := s.Search(ctx, SearchQuery{Query: "needle", Sort: "oldest", Limit: 2, Offset: 2})
	if got := ids(page); !reflect.DeepEqual(got, want[2:4]) {
		t.Errorf("page = %v, want %v", got, want[2:4])
	}
	if all, _ := s.Search(ctx, SearchQuery{Query: "needle"}); len(all) != 5 {
		t.Errorf("default limit cut results: %d", len(all))
	}
}

func TestSearchToolRoleScansFullBody(t *testing.T) {
	s := open(t)
	mustSession(t, s, "a")
	// The index holds only a prefix of tool output; a tool-role search must still find the tail.
	body := strings.Repeat("filler ", ftsToolPrefixChars/7+10) + "UNIQUETAILMARKER"
	if _, err := s.AppendMessage(ctx, Message{SessionID: "a", Role: "tool", ToolName: "terminal", Content: body}); err != nil {
		t.Fatal(err)
	}
	if hits, _ := s.Search(ctx, SearchQuery{Query: "UNIQUETAILMARKER"}); len(hits) != 0 {
		t.Fatalf("the word index should not hold the tail of oversized tool output: %d hits", len(hits))
	}
	hits, err := s.Search(ctx, SearchQuery{Query: "UNIQUETAILMARKER", Roles: []string{"tool"}})
	if err != nil || len(hits) != 1 || hits[0].ToolName != "terminal" {
		t.Fatalf("tool-role search = %+v, %v", hits, err)
	}
	// Tool name and tool-call JSON are searchable too.
	if _, err := s.AppendMessage(ctx, Message{SessionID: "a", Role: "assistant", ToolCalls: []byte(`[{"function":{"name":"web_extract"}}]`)}); err != nil {
		t.Fatal(err)
	}
	if hits, _ := s.Search(ctx, SearchQuery{Query: "web_extract", Roles: []string{"assistant"}}); len(hits) != 1 {
		t.Fatalf("tool call JSON not searchable: %d", len(hits))
	}
}

func TestSearchCJKUsesSubstringScan(t *testing.T) {
	s := open(t)
	mustSession(t, s, "a")
	for _, c := range []string{"今天去桂林漓江游玩", "修改youer服务端配置", "unrelated english"} {
		if _, err := s.AppendMessage(ctx, Message{SessionID: "a", Role: "user", Content: c}); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		query string
		want  int
	}{
		{"桂林", 1},
		{"桂林*", 1}, // the prefix star callers append must not be searched literally
		{"漓江", 1},
		{"桂林 OR 服务端", 2},
		{"广西", 0},
	}
	for _, tc := range tests {
		hits, err := s.Search(ctx, SearchQuery{Query: tc.query})
		if err != nil || len(hits) != tc.want {
			t.Errorf("Search(%q) = %d hits, %v; want %d", tc.query, len(hits), err, tc.want)
		}
	}
}

func TestSearchLikeEscapesWildcards(t *testing.T) {
	s := open(t)
	mustSession(t, s, "a")
	for _, c := range []string{"progress 50%你好 done", "progress 5000你好 done"} {
		if _, err := s.AppendMessage(ctx, Message{SessionID: "a", Role: "user", Content: c}); err != nil {
			t.Fatal(err)
		}
	}
	hits, _ := s.Search(ctx, SearchQuery{Query: "50%你好"}) // CJK keeps % literal and routes to LIKE
	if len(hits) != 1 {
		t.Fatalf("a literal %% must not act as a wildcard: %d hits", len(hits))
	}
}

func TestSearchIndexFollowsEditsAndDeletes(t *testing.T) {
	s := open(t)
	mustSession(t, s, "a")
	id, _ := s.AppendMessage(ctx, Message{SessionID: "a", Role: "user", Content: "alpha beta"})
	mustExec(t, s, `UPDATE messages SET content = 'gamma delta' WHERE id = ?`, id)
	if hits, _ := s.Search(ctx, SearchQuery{Query: "alpha"}); len(hits) != 0 {
		t.Fatal("stale index entry after update")
	}
	if hits, _ := s.Search(ctx, SearchQuery{Query: "gamma"}); len(hits) != 1 {
		t.Fatal("updated content not indexed")
	}
	mustExec(t, s, `UPDATE messages SET active = 0 WHERE id = ?`, id) // non-content write: index untouched
	if hits, _ := s.Search(ctx, SearchQuery{Query: "gamma", IncludeInactive: true}); len(hits) != 1 {
		t.Fatal("index lost a row on a non-content update")
	}
	mustExec(t, s, `DELETE FROM messages WHERE id = ?`, id)
	if hits, _ := s.Search(ctx, SearchQuery{Query: "gamma", IncludeInactive: true}); len(hits) != 0 {
		t.Fatal("deleted row still searchable")
	}
}

func FuzzSearchNeverErrors(f *testing.F) {
	s, err := Open(f.TempDir())
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { s.Close() })
	if err := s.CreateSession(ctx, NewSession{ID: "a", Source: "cli"}); err != nil {
		f.Fatal(err)
	}
	for _, c := range []string{"hello world", "chat-send helper", "你好 世界", "P2.2 my-app.config.ts"} {
		if _, err := s.AppendMessage(ctx, Message{SessionID: "a", Role: "user", Content: c}); err != nil {
			f.Fatal(err)
		}
	}
	for _, seed := range []string{"hello", `"unterminated`, "AND OR NOT", "*", "a OR", "()", "你好*", `"a" "b"`, "NEAR(a b)", "col:val", "\x00Q0\x00", "-", "a--b", "%", "_"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, q string) {
		if _, err := s.Search(ctx, SearchQuery{Query: q}); err != nil {
			t.Fatalf("Search(%q) errored: %v", q, err)
		}
		if _, err := s.Search(ctx, SearchQuery{Query: q, Roles: []string{"tool"}}); err != nil {
			t.Fatalf("tool-role Search(%q) errored: %v", q, err)
		}
	})
}
