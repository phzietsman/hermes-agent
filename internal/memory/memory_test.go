package memory

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phzietsman/hermes-agent/internal/llm"
	"github.com/phzietsman/hermes-agent/internal/memory/sam"
	"github.com/phzietsman/hermes-agent/internal/session"
	"github.com/phzietsman/hermes-agent/internal/tools"
)

// ---- goldens from Python ----

func TestFormatHitsAndDigestMatchPython(t *testing.T) {
	raw, err := os.ReadFile("testdata/provider.json")
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		Hits []struct {
			Hits  []sam.Hit
			Limit int
			Out   string
		}
		Digests []struct {
			Messages []struct {
				Role    string
				Content *string
			}
			Digest string
		}
	}
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	for i, c := range g.Hits {
		if got := FormatHits(c.Hits, c.Limit); got != c.Out {
			t.Errorf("hits %d:\n got %q\nwant %q", i, got, c.Out)
		}
	}
	for i, c := range g.Digests {
		var msgs []llm.Message
		for _, m := range c.Messages {
			lm := llm.Message{Role: llm.Role(m.Role)}
			if m.Content != nil {
				lm.Content = llm.Text(*m.Content)
			}
			msgs = append(msgs, lm)
		}
		if got := transcriptDigest(msgs); got != c.Digest {
			t.Errorf("digest %d: %s, want %s", i, got, c.Digest)
		}
	}
}

// ---- a fake SAM ----

type fakeSAM struct {
	*httptest.Server
	mu       sync.Mutex
	down     atomic.Bool
	slow     atomic.Int64 // milliseconds of delay for /query
	hits     []sam.Hit
	profile  string
	requests []string // "path body"
	facts    []string
}

func newFake(t *testing.T) *fakeSAM {
	f := &fakeSAM{profile: `{"instructions":[{"description":"Be brief"}],"preferences":[{"key":"drink","value":"tea"}],"static":[],"dynamic":[]}`}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, r.URL.Path+" "+string(b))
		f.mu.Unlock()
		if f.down.Load() {
			http.Error(w, `{"message":"down"}`, http.StatusServiceUnavailable)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/profile"):
			io.WriteString(w, f.profile)
		case strings.HasSuffix(r.URL.Path, "/query"):
			if d := f.slow.Load(); d > 0 {
				time.Sleep(time.Duration(d) * time.Millisecond)
			}
			json.NewEncoder(w).Encode(map[string]any{"hits": f.hits})
		case strings.HasSuffix(r.URL.Path, "/context"):
			io.WriteString(w, `{"context":"synthesised context"}`)
		case strings.HasSuffix(r.URL.Path, "/facts"), strings.HasSuffix(r.URL.Path, "/facts/batch"):
			f.mu.Lock()
			f.facts = append(f.facts, string(b))
			f.mu.Unlock()
			io.WriteString(w, `{}`)
		default:
			io.WriteString(w, `{"ok":true}`)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeSAM) count(suffix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if p, _, _ := strings.Cut(r, " "); strings.HasSuffix(p, suffix) {
			n++
		}
	}
	return n
}

func (f *fakeSAM) factsText() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.facts, "\n")
}

func settings(f *fakeSAM, mode sam.RecallMode, mods ...func(*sam.Settings)) *sam.Settings {
	s := &sam.Settings{Endpoint: f.URL, Context: "ctx", APIKey: "k", DefaultScope: "user/default", AgentScope: "agent/hermes", RecallMode: mode,
		TopK: 5, MaxPrefetchChars: 4000, CoreProfileMaxChars: 1500, Timeout: 3 * time.Second, PrefetchWait: 300 * time.Millisecond,
		MaxRetries: 0, BreakerThreshold: 3, ConsolidateOnEnd: true, ReplayEveryNTurns: 2}
	for _, m := range mods {
		m(s)
	}
	return s
}

func newProvider(t *testing.T, f *fakeSAM, home string, s *sam.Settings, o Options) *Provider {
	o.Home, o.SessionID = home, orDefault(o.SessionID, "sess 1")
	p := New(context.Background(), s, o)
	t.Cleanup(p.Close)
	return p
}

func orDefault(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ---- core profile ----

func TestCoreProfileIsFrozenAndCachedForADegradedStart(t *testing.T) {
	f, home := newFake(t), t.TempDir()
	p := newProvider(t, f, home, settings(f, sam.RecallHybrid), Options{})
	block := p.SystemPromptBlock()
	if !strings.Contains(block, "persistent long-term memory") || !strings.Contains(block, "- Be brief") || !strings.Contains(block, "- drink: tea") {
		t.Fatalf("block %q", block)
	}
	f.profile = `{"instructions":[{"description":"CHANGED"}],"preferences":[],"static":[]}`
	if p.SystemPromptBlock() != block {
		t.Error("the core profile must not change mid-session")
	}
	f.down.Store(true)
	p2 := newProvider(t, f, home, settings(f, sam.RecallHybrid), Options{SessionID: "s2"})
	if !strings.Contains(p2.SystemPromptBlock(), "- Be brief") {
		t.Errorf("a degraded start should use the cached profile: %q", p2.SystemPromptBlock())
	}
	if strings.Contains(p2.SystemPromptBlock(), "CHANGED") {
		t.Error("cache served a profile it never saw")
	}
	home2 := t.TempDir()
	p3 := newProvider(t, f, home2, settings(f, sam.RecallHybrid), Options{})
	if p3.SystemPromptBlock() != guide {
		t.Errorf("no profile and no cache: just the guide, got %q", p3.SystemPromptBlock())
	}
}

func TestUserIDScopesTheDefaultScopeOnly(t *testing.T) {
	f := newFake(t)
	p := newProvider(t, f, t.TempDir(), settings(f, sam.RecallHybrid), Options{UserID: "alice"})
	if p.Settings().DefaultScope != "user/alice" {
		t.Errorf("%s", p.Settings().DefaultScope)
	}
	p2 := newProvider(t, f, t.TempDir(), settings(f, sam.RecallHybrid, func(s *sam.Settings) { s.DefaultScope = "user/custom" }), Options{UserID: "alice"})
	if p2.Settings().DefaultScope != "user/custom" {
		t.Error("an explicit scope must win over the user id")
	}
}

// ---- prefetch ----

func TestPrefetchWaitsBrieflyThenServesTheLastCompletedRecall(t *testing.T) {
	f := newFake(t)
	f.hits = []sam.Hit{{ID: "1", Text: "likes tea", Score: 0.9}}
	p := newProvider(t, f, t.TempDir(), settings(f, sam.RecallHybrid), Options{})
	ctx := context.Background()
	if got := p.Prefetch(ctx, "what do I drink?"); !strings.Contains(got, "## Recalled memory") || !strings.Contains(got, "likes tea (0.90)") {
		t.Fatalf("fast recall: %q", got)
	}
	// Same query again is served without a second call.
	before := f.count("/query")
	p.Prefetch(ctx, "what do I drink?")
	if f.count("/query") != before {
		t.Error("a completed recall for the same query must be reused")
	}
	// A slow recall (longer than prefetch_wait_s) does not hold the turn: the last completed text is served.
	f.slow.Store(1500)
	start := time.Now()
	got := p.Prefetch(ctx, "something new")
	if time.Since(start) > time.Second {
		t.Errorf("prefetch blocked for %v", time.Since(start))
	}
	if !strings.Contains(got, "likes tea") {
		t.Errorf("should serve the last completed recall, got %q", got)
	}
	// The slow recall keeps running and warms the next turn.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if p.Prefetch(ctx, "something new") != "" && p.doneQuery == "something new" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("the slow recall never completed")
}

func TestPrefetchModes(t *testing.T) {
	f := newFake(t)
	f.hits = []sam.Hit{{ID: "1", Text: "x", Score: 1}}
	ctx := context.Background()
	if got := newProvider(t, f, t.TempDir(), settings(f, sam.RecallTools), Options{}).Prefetch(ctx, "q"); got != "" {
		t.Errorf("tools mode injects nothing: %q", got)
	}
	if got := newProvider(t, f, t.TempDir(), settings(f, sam.RecallContext), Options{}).Prefetch(ctx, "q"); got != "## Recalled memory\nsynthesised context" {
		t.Errorf("context mode: %q", got)
	}
	p := newProvider(t, f, t.TempDir(), settings(f, sam.RecallHybrid), Options{})
	if p.Prefetch(ctx, "   ") != "" {
		t.Error("a blank query recalls nothing")
	}
}

func TestPrefetchGivesUpOnAnOutageAndTheBreakerOpens(t *testing.T) {
	f := newFake(t)
	f.down.Store(true)
	p := newProvider(t, f, t.TempDir(), settings(f, sam.RecallHybrid), Options{})
	for i := range 5 {
		p.Prefetch(context.Background(), "q"+string(rune('a'+i)))
		time.Sleep(20 * time.Millisecond)
	}
	if p.Active() {
		t.Error("repeated failures must open the breaker")
	}
	n := f.count("/query")
	p.Prefetch(context.Background(), "after")
	if f.count("/query") != n {
		t.Error("an open breaker must stop calling the service")
	}
}

// ---- writes ----

func TestSyncTurnDeliversThroughTheOutbox(t *testing.T) {
	f, home := newFake(t), t.TempDir()
	p := newProvider(t, f, home, settings(f, sam.RecallHybrid), Options{})
	p.SyncTurn(context.Background(), "I like green tea", "Noted.")
	p.Flush()
	body := f.factsText()
	for _, want := range []string{`"role":"user"`, "I like green tea", `"origin=turn"`, `"session=sess_1"`, `"scopes":[["user/default"]]`} {
		if !strings.Contains(body, want) {
			t.Errorf("batch lacks %s: %s", want, body)
		}
	}
	if pend, _ := sam.NewOutbox(filepath.Join(home, outboxFile)).Pending(); len(pend) != 0 {
		t.Errorf("delivered writes must leave the outbox: %d", len(pend))
	}
	// Empty turns are not written.
	n := len(f.facts)
	p.SyncTurn(context.Background(), "  ", "")
	p.Flush()
	if len(f.facts) != n {
		t.Error("a blank turn was written")
	}
}

func TestWritesSurviveAnOutageAndReplayOnTheNextSession(t *testing.T) {
	f, home := newFake(t), t.TempDir()
	f.down.Store(true)
	p := newProvider(t, f, home, settings(f, sam.RecallHybrid), Options{})
	for _, w := range []string{"one", "two", "three"} {
		p.SyncTurn(context.Background(), "fact "+w, "ok")
	}
	p.Flush()
	pend, _ := sam.NewOutbox(filepath.Join(home, outboxFile)).Pending()
	if len(pend) != 3 {
		t.Fatalf("queued %d, want 3", len(pend))
	}
	p.Close()
	// The service is back; a new session replays on start, oldest first, once each.
	f.down.Store(false)
	f.mu.Lock()
	f.facts = nil
	f.mu.Unlock()
	p2 := newProvider(t, f, home, settings(f, sam.RecallHybrid), Options{SessionID: "next"})
	p2.Flush()
	body := f.factsText()
	i1, i2, i3 := strings.Index(body, "fact one"), strings.Index(body, "fact two"), strings.Index(body, "fact three")
	if i1 < 0 || i2 < i1 || i3 < i2 || strings.Count(body, "fact one") != 1 {
		t.Errorf("replay order/duplication wrong: %s", body)
	}
	if pend, _ := sam.NewOutbox(filepath.Join(home, outboxFile)).Pending(); len(pend) != 0 {
		t.Errorf("%d entries left after replay", len(pend))
	}
}

func TestReadOnlySessionsNeverWrite(t *testing.T) {
	f, home := newFake(t), t.TempDir()
	p := newProvider(t, f, home, settings(f, sam.RecallHybrid), Options{ReadOnly: true})
	p.SyncTurn(context.Background(), "u", "a")
	p.EndSession(context.Background(), []llm.Message{{Role: llm.RoleUser, Content: llm.Text("x")}})
	p.Flush()
	if f.count("/facts") != 0 || f.count("/facts/batch") != 0 || f.count("/consolidate") != 0 {
		t.Errorf("read-only session wrote: %v", f.requests)
	}
	if _, err := os.Stat(filepath.Join(home, outboxFile)); err == nil {
		t.Error("a read-only session queued a write")
	}
}

func TestTranscriptIsRememberedOnceAcrossPreCompressAndSessionEnd(t *testing.T) {
	f := newFake(t)
	p := newProvider(t, f, t.TempDir(), settings(f, sam.RecallHybrid), Options{})
	hist := []llm.Message{
		{Role: llm.RoleUser, Content: llm.Text("hello")}, {Role: llm.RoleAssistant, Content: llm.Text("hi")},
		{Role: llm.RoleTool, ToolCallID: "c", Content: llm.Text("SECRET TOOL OUTPUT")},
	}
	p.PreCompress(context.Background(), hist)
	p.Flush()
	p.EndSession(context.Background(), hist)
	p.Flush()
	if got := f.count("/facts/batch"); got != 1 {
		t.Errorf("transcript sent %d times, want 1", got)
	}
	if strings.Contains(f.factsText(), "SECRET TOOL OUTPUT") {
		t.Error("tool output left the machine")
	}
	if f.count("/consolidate") != 1 {
		t.Errorf("consolidate ran %d times", f.count("/consolidate"))
	}
	// Consolidation runs after the replay of pending writes.
	f.mu.Lock()
	defer f.mu.Unlock()
	order := ""
	for _, r := range f.requests {
		switch {
		case strings.Contains(r, "/facts/batch"):
			order += "B"
		case strings.Contains(r, "/consolidate"):
			order += "C"
		}
	}
	if order != "BC" {
		t.Errorf("order %q", order)
	}
}

func TestPeriodicReplayRunsEveryNTurns(t *testing.T) {
	f, home := newFake(t), t.TempDir()
	f.down.Store(true)
	p := newProvider(t, f, home, settings(f, sam.RecallHybrid, func(s *sam.Settings) { s.BreakerThreshold = 100 }), Options{})
	p.SyncTurn(context.Background(), "a", "b")
	p.Flush()
	f.down.Store(false)
	before := f.count("/facts/batch")
	p.SyncTurn(context.Background(), "c", "d") // second turn: replays the pending write
	p.Flush()
	if f.count("/facts/batch") <= before {
		t.Error("the periodic replay did not run")
	}
}

// ---- tools ----

func toolCall(t *testing.T, m *Manager, session, name string, args map[string]any) map[string]any {
	t.Helper()
	reg := tools.NewRegistry()
	RegisterTools(reg, m)
	b, _ := json.Marshal(args)
	ctx := tools.WithEnv(context.Background(), tools.CallEnv{SessionID: session})
	out, _ := reg.Build(tools.Selection{}).Run(ctx, llm.ToolCall{Function: llm.ToolCallFunction{Name: name, Arguments: string(b)}})
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("%s: not JSON %q", name, out)
	}
	return res
}

func newManager(t *testing.T, f *fakeSAM, mods ...func(*sam.Settings)) (*Manager, string) {
	home := t.TempDir()
	m := &Manager{Settings: settings(f, sam.RecallHybrid, mods...), Home: home, byID: map[string]*Provider{}}
	t.Cleanup(m.Close)
	m.ForSession(context.Background(), "s1", "", false)
	return m, home
}

func TestToolsRecallRememberForgetReflect(t *testing.T) {
	f := newFake(t)
	f.hits = []sam.Hit{{ID: "h1", Text: "likes tea", Score: 0.8}}
	m, home := newManager(t, f)

	r := toolCall(t, m, "s1", "agent_memory_recall", map[string]any{"query": "drink", "k": 2})
	hits := r["result"].(map[string]any)["hits"].([]any)
	if r["success"] != true || len(hits) != 1 || hits[0].(map[string]any)["text"] != "likes tea" {
		t.Fatalf("recall %v", r)
	}
	if !strings.Contains(f.requests[len(f.requests)-1], `"k":2`) || !strings.Contains(f.requests[len(f.requests)-1], `"lens":["user/default"]`) {
		t.Errorf("recall request %s", f.requests[len(f.requests)-1])
	}

	r = toolCall(t, m, "s1", "agent_memory_remember", map[string]any{"text": "prefers dark mode", "kind": "preference"})
	if r["result"].(map[string]any)["queued"] != true {
		t.Fatalf("remember %v", r)
	}
	m.Lookup("s1").Flush()
	body := f.factsText()
	for _, want := range []string{"prefers dark mode", `"memory_category":"identity"`, `"kind=preference"`, `"origin=assistant_tool"`, `"session=s1"`} {
		if !strings.Contains(body, want) {
			t.Errorf("remember body lacks %s: %s", want, body)
		}
	}
	// A fact defaults to knowledge; an unknown kind sends no category.
	toolCall(t, m, "s1", "agent_memory_remember", map[string]any{"text": "the sky is blue"})
	toolCall(t, m, "s1", "agent_memory_remember", map[string]any{"text": "odd one", "kind": "whim"})
	m.Lookup("s1").Flush()
	if !strings.Contains(f.factsText(), `"memory_category":"knowledge"`) {
		t.Error("a fact is knowledge")
	}
	if pend, _ := sam.NewOutbox(filepath.Join(home, outboxFile)).Pending(); len(pend) != 0 {
		t.Errorf("%d left in the outbox", len(pend))
	}

	if r = toolCall(t, m, "s1", "agent_memory_forget", map[string]any{"query": "tea", "purge": true}); r["result"].(map[string]any)["queued"] != true {
		t.Errorf("forget %v", r)
	}
	m.Lookup("s1").Flush()
	if f.count("/forget") != 1 {
		t.Error("forget not delivered")
	}
	if r = toolCall(t, m, "s1", "agent_memory_reflect", map[string]any{"query": "patterns", "persist": true}); r["success"] != true {
		t.Errorf("reflect %v", r)
	}
}

func TestToolsFailureModes(t *testing.T) {
	f := newFake(t)
	m, _ := newManager(t, f)
	for _, name := range []string{"agent_memory_recall", "agent_memory_remember", "agent_memory_forget", "agent_memory_reflect"} {
		if r := toolCall(t, m, "s1", name, map[string]any{}); r["error"] == nil || r["provider"] != "agent_memory" {
			t.Errorf("%s with no args: %v", name, r)
		}
	}
	if r := toolCall(t, m, "no-such-session", "agent_memory_recall", map[string]any{"query": "q"}); !strings.Contains(r["error"].(string), "unavailable") {
		t.Errorf("unknown session: %v", r)
	}
	f.down.Store(true)
	if r := toolCall(t, m, "s1", "agent_memory_recall", map[string]any{"query": "q"}); r["error"] == nil {
		t.Errorf("outage must be an error result, not a crash: %v", r)
	}
	// Writes still succeed from the model's view during an outage: they are queued.
	if r := toolCall(t, m, "s1", "agent_memory_remember", map[string]any{"text": "kept"}); r["success"] != true {
		t.Errorf("a queued write must report success: %v", r)
	}
}

func TestToolsetNameIsTheOneChildrenAreDeniedAndOriginCanBeOverridden(t *testing.T) {
	f := newFake(t)
	reg := tools.NewRegistry()
	m, _ := newManager(t, f)
	RegisterTools(reg, m)
	for n := range reg.Build(tools.Selection{}).Names() {
		if !strings.HasPrefix(n, "agent_memory_") {
			t.Errorf("unexpected tool %s", n)
		}
	}
	if got := reg.Build(tools.Selection{ExcludeToolsets: []string{Toolset}}).Names(); len(got) != 0 {
		t.Errorf("excluding %q left %v", Toolset, got)
	}
	m.Lookup("s1").Origin = OriginReview
	toolCall(t, m, "s1", "agent_memory_remember", map[string]any{"text": "from a fork"})
	m.Lookup("s1").Flush()
	if !strings.Contains(f.factsText(), "origin=background_review") {
		t.Error("the review origin was not used")
	}
}

func TestNewManager(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	if m, why := NewManager(home, lookup); m != nil || !strings.Contains(why, sam.APIKeyEnv) {
		t.Errorf("no key: %v %q", m, why)
	}
	env[sam.APIKeyEnv] = "k"
	if m, why := NewManager(home, lookup); m != nil || !strings.Contains(why, "required") {
		t.Errorf("no config: %v %q", m, why)
	}
	os.WriteFile(filepath.Join(home, sam.ConfigFile), []byte(`{"endpoint":"https://x.example","context":"c"}`), 0o600)
	if m, why := NewManager(home, lookup); m == nil || why != "" {
		t.Errorf("configured: %v %q", m, why)
	}
}

// ---- importer ----

func TestImportMemoryFilesIsIdempotentAndResumable(t *testing.T) {
	f, home := newFake(t), t.TempDir()
	os.MkdirAll(filepath.Join(home, "memories"), 0o755)
	os.WriteFile(filepath.Join(home, "memories", "MEMORY.md"), []byte("learned one\n§\nlearned two\n§\n  \n§\n"), 0o644)
	os.WriteFile(filepath.Join(home, "memories", "USER.md"), []byte("name is Pat"), 0o644)
	s := settings(f, sam.RecallHybrid)
	c := s.Client()
	var lines []string
	o := ImportOptions{Out: func(l string) { lines = append(lines, l) }}

	dry := ImportMemoryFiles(context.Background(), c, home, s, ImportOptions{DryRun: true, Out: o.Out})
	if dry.Planned != 3 || dry.Written != 0 || f.count("/facts") != 0 || len(lines) != 3 {
		t.Fatalf("dry run: %+v lines=%v", dry, lines)
	}
	r := ImportMemoryFiles(context.Background(), c, home, s, o)
	if r.Written != 3 || r.Failed != 0 || f.count("/facts") != 3 {
		t.Fatalf("%+v", r)
	}
	body := f.factsText()
	for _, want := range []string{`"memory_category":"knowledge"`, `"memory_category":"identity"`, "origin=import", "source=user", "digest="} {
		if !strings.Contains(body, want) {
			t.Errorf("import lacks %s", want)
		}
	}
	if again := ImportMemoryFiles(context.Background(), c, home, s, o); again.Written != 0 || again.Skipped != 3 || f.count("/facts") != 3 {
		t.Errorf("second run wrote again: %+v", again)
	}
	// An entry that fails is reported, not recorded, and a later run picks it up.
	os.WriteFile(filepath.Join(home, "memories", "MEMORY.md"), []byte("learned one\n§\nbrand new"), 0o644)
	f.down.Store(true)
	if r := ImportMemoryFiles(context.Background(), c, home, s, o); r.Failed != 1 || r.Written != 0 {
		t.Errorf("outage: %+v", r)
	}
	f.down.Store(false)
	if r := ImportMemoryFiles(context.Background(), c, home, s, o); r.Written != 1 {
		t.Errorf("resume: %+v", r)
	}
}

func TestImportSessionsBackfillsOnceAndSkipsToolRows(t *testing.T) {
	f, home := newFake(t), t.TempDir()
	st, err := session.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	st.CreateSession(ctx, session.NewSession{ID: "old", Source: "cli"})
	st.AppendMessage(ctx, session.Message{SessionID: "old", Role: "user", Content: "remember the milk"})
	st.AppendMessage(ctx, session.Message{SessionID: "old", Role: "tool", Content: "TOOLROW", ToolCallID: "x"})
	st.AppendMessage(ctx, session.Message{SessionID: "old", Role: "assistant", Content: "ok"})
	s := settings(f, sam.RecallHybrid)
	r, err := ImportSessions(ctx, s.Client(), st, home, s, 5, ImportOptions{})
	if err != nil || r.Written != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	if strings.Contains(f.factsText(), "TOOLROW") || !strings.Contains(f.factsText(), "remember the milk") || !strings.Contains(f.factsText(), "origin=import-session") {
		t.Errorf("body %s", f.factsText())
	}
	if r2, _ := ImportSessions(ctx, s.Client(), st, home, s, 5, ImportOptions{}); r2.Written != 0 || r2.Skipped != 1 {
		t.Errorf("second run %+v", r2)
	}
}

func TestAForkWritesAsAReviewAndNeverRecallsOrSyncs(t *testing.T) {
	f := newFake(t)
	f.hits = []sam.Hit{{ID: "1", Text: "likes tea", Score: 0.9}}
	m, _ := newManager(t, f)
	fork := m.ForFork(context.Background(), "fork1")
	if fork.Origin != OriginReview {
		t.Errorf("origin %q", fork.Origin)
	}
	if fork.Prefetch(context.Background(), "what do I drink?") != "" {
		t.Error("a fork never recalls")
	}
	fork.QueuePrefetch("warm it")
	fork.SyncTurn(context.Background(), "review prompt", "review answer")
	fork.EndSession(context.Background(), []llm.Message{{Role: llm.RoleUser, Content: llm.Text("x")}})
	fork.Flush()
	if f.count("/query") != 0 || f.count("/facts/batch") != 0 || f.count("/consolidate") != 0 {
		t.Errorf("a fork touched recall, sync or consolidation: %v", f.requests)
	}
	toolCall(t, m, "fork1", "agent_memory_remember", map[string]any{"text": "learned in review"})
	fork.Flush()
	if !strings.Contains(f.factsText(), "origin=background_review") {
		t.Error("the review's write was not labelled")
	}
	m.Drop("fork1")
	if m.Lookup("fork1") != nil {
		t.Error("Drop must forget the provider")
	}
	m.Drop("never-existed")
	// The normal provider warms recall ahead of the next turn.
	p := m.Lookup("s1")
	p.QueuePrefetch("what do I drink?")
	waitUntil := time.Now().Add(5 * time.Second)
	for f.count("/query") == 0 && time.Now().Before(waitUntil) {
		time.Sleep(10 * time.Millisecond)
	}
	if f.count("/query") == 0 {
		t.Error("QueuePrefetch did not start a recall")
	}
}
