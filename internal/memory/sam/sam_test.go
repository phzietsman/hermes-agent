package sam

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
)

type goldens struct {
	EntryIDs []struct {
		Op      Op
		Payload map[string]any
		ID      string
	} `json:"entry_ids"`
	Profiles []struct {
		Instructions, Preferences, Static []string
		Max                               int
		Out                               string
	} `json:"profiles"`
	Flat []struct {
		Entry map[string]any
		Text  string
	} `json:"flat"`
}

func load(t *testing.T) goldens {
	raw, err := os.ReadFile("testdata/sam.json")
	if err != nil {
		t.Fatal(err)
	}
	var g goldens
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&g); err != nil {
		t.Fatal(err)
	}
	return g
}

func TestEntryIDMatchesPython(t *testing.T) {
	for i, c := range load(t).EntryIDs {
		if got := EntryID(c.Op, c.Payload); got != c.ID {
			t.Errorf("case %d: %s, want %s", i, got, c.ID)
		}
	}
}

func TestFormatProfileMatchesPython(t *testing.T) {
	for i, c := range load(t).Profiles {
		if got := FormatProfile(Profile{c.Instructions, c.Preferences, c.Static}, c.Max); got != c.Out {
			t.Errorf("case %d:\n got %q\nwant %q", i, got, c.Out)
		}
	}
}

func TestEntryTextMatchesPythonForFreeText(t *testing.T) {
	for i, c := range load(t).Flat {
		_, hasKey := c.Entry["key"]
		_, hasValue := c.Entry["value"]
		_, hasDesc := c.Entry["description"]
		_, hasText := c.Entry["text"]
		switch {
		case hasKey && hasValue && !hasDesc && !hasText:
			continue // Python flattens these as SDK objects ("key: value"); covered by TestEntryTextKeyValue
		case len(c.Entry) == 1 && c.Entry["other"] != nil:
			continue // Python prints a dict repr
		}
		raw, _ := json.Marshal(c.Entry)
		if got := entryText(raw); got != c.Text {
			t.Errorf("case %d: %q, want %q", i, got, c.Text)
		}
	}
}

func TestEntryTextKeyValue(t *testing.T) {
	if got := entryText(json.RawMessage(`{"key":"tz","value":"UTC"}`)); got != "tz: UTC" {
		t.Errorf("%q", got)
	}
}

// ---- client ----

type srv struct {
	*httptest.Server
	mu     sync.Mutex
	reqs   []*http.Request
	bodies []string
	h      func(w http.ResponseWriter, r *http.Request, n int)
}

func newSrv(t *testing.T, h func(w http.ResponseWriter, r *http.Request, n int)) *srv {
	s := &srv{h: h}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.reqs = append(s.reqs, r)
		s.bodies = append(s.bodies, string(b))
		n := len(s.reqs)
		s.mu.Unlock()
		s.h(w, r, n)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *srv) client(retries int) (*Client, *[]time.Duration) {
	var slept []time.Duration
	return &Client{Endpoint: s.URL + "/", Context: "my ctx/1", APIKey: "k-secret", Timeout: 2 * time.Second, MaxRetries: retries,
		Sleep: func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil },
		Now:   func() time.Time { return time.Unix(1_700_000_010, 0) }}, &slept
}

func TestRequestShapeAndHeaders(t *testing.T) {
	s := newSrv(t, func(w http.ResponseWriter, r *http.Request, n int) {
		io.WriteString(w, `{"mode":"x","session_id":"s1"}`)
	})
	c, _ := s.client(2)
	out, err := c.Remember(context.Background(), "likes tea", RememberOptions{Labels: []string{"session=1"}, Category: "identity", Scopes: "user/p", SessionID: "sid"})
	if err != nil || out["session_id"] != "s1" {
		t.Fatalf("%v %v", out, err)
	}
	r := s.reqs[0]
	if r.URL.EscapedPath() != "/api/v1/my%20ctx%2F1/facts" || r.Method != "POST" {
		t.Errorf("%s %s", r.Method, r.URL.EscapedPath())
	}
	if r.Header.Get("Authorization") != "Bearer k-secret" || r.Header.Get("Accept") != "application/json" || r.Header.Get("Content-Type") != "application/json" {
		t.Errorf("headers %v", r.Header)
	}
	if k := r.Header.Get("Idempotency-Key"); len(k) != 64 {
		t.Errorf("idempotency key %q", k)
	}
	var body map[string]any
	json.Unmarshal([]byte(s.bodies[0]), &body)
	if body["text"] != "likes tea" || body["memory_category"] != "identity" || body["session_id"] != "sid" ||
		body["scopes"].([]any)[0].([]any)[0] != "user/p" || body["labels"].([]any)[0] != "session=1" {
		t.Errorf("body %v", body)
	}
	// Recall uses the other casing family and no idempotency key.
	s2 := newSrv(t, func(w http.ResponseWriter, r *http.Request, n int) {
		io.WriteString(w, `{"hits":[{"id":"1","score":0.5,"source":"s","text":"hit"}]}`)
	})
	c2, _ := s2.client(2)
	hits, err := c2.Recall(context.Background(), "q", 3, []string{"recent"})
	if err != nil || len(hits) != 1 || hits[0].Text != "hit" {
		t.Fatalf("%v %v", hits, err)
	}
	if s2.reqs[0].Header.Get("Idempotency-Key") != "" || !strings.Contains(s2.bodies[0], `"k":3`) || !strings.Contains(s2.bodies[0], `"lens":["recent"]`) {
		t.Errorf("recall request %v %s", s2.reqs[0].Header, s2.bodies[0])
	}
}

func TestIdempotencyKeyIsStableWithinABucketAndChangesAcrossThem(t *testing.T) {
	b := []byte(`{"text":"x"}`)
	t0 := time.Unix(1_700_000_010, 0)
	k := idempotencyKey("POST", "/p", b, t0)
	if k != idempotencyKey("POST", "/p", b, t0.Add(5*time.Second)) {
		t.Error("key changed inside the 30 s bucket")
	}
	if k == idempotencyKey("POST", "/p", b, t0.Add(31*time.Second)) || k == idempotencyKey("POST", "/p", []byte(`{"text":"y"}`), t0) {
		t.Error("key must change across buckets and bodies")
	}
}

func TestErrorClassification(t *testing.T) {
	cases := []struct {
		status int
		body   string
		kind   Kind
		msg    string
	}{
		{401, `{"message":"bad key"}`, AuthFailed, "bad key"},
		{403, `{"title":"scope"}`, Rejected, "scope"},
		{404, `{"error":"nope"}`, Rejected, "nope"},
		{422, `plain text`, Rejected, "plain text"},
		{408, ``, Unavailable, "Memory request failed"},
		{429, `{"message":"slow down"}`, Unavailable, "slow down"},
		{503, `{"message":"down"}`, Unavailable, "down"},
		{500, `{"message":"upstream timed out"}`, Timeout, "upstream timed out"},
	}
	for _, c := range cases {
		s := newSrv(t, func(w http.ResponseWriter, r *http.Request, n int) {
			w.Header().Set("X-Trace-Id", "tr1")
			w.WriteHeader(c.status)
			io.WriteString(w, c.body)
		})
		cl, _ := s.client(0)
		_, err := cl.Recall(context.Background(), "q", 1, nil)
		var e *Error
		if !errors.As(err, &e) || e.Kind != c.kind || e.Message != c.msg || e.Status != c.status || e.TraceID != "tr1" {
			t.Errorf("%d: %+v", c.status, e)
		}
	}
}

func TestRetryPolicy(t *testing.T) {
	flaky := func(fail int) func(w http.ResponseWriter, r *http.Request, n int) {
		return func(w http.ResponseWriter, r *http.Request, n int) {
			if n <= fail {
				http.Error(w, `{"message":"boom"}`, http.StatusBadGateway)
				return
			}
			io.WriteString(w, `{}`)
		}
	}
	// Idempotent write: retried with the 0.25 s, 0.5 s schedule, then succeeds.
	s := newSrv(t, flaky(2))
	c, slept := s.client(2)
	if _, err := c.Remember(context.Background(), "x", RememberOptions{}); err != nil || len(s.reqs) != 3 {
		t.Fatalf("err=%v reqs=%d", err, len(s.reqs))
	}
	if len(*slept) != 2 || (*slept)[0] != 250*time.Millisecond || (*slept)[1] != 500*time.Millisecond {
		t.Errorf("backoff %v", *slept)
	}
	// Same body each time, same idempotency key (a retry must dedupe).
	if s.reqs[0].Header.Get("Idempotency-Key") != s.reqs[2].Header.Get("Idempotency-Key") {
		t.Error("retry changed the idempotency key")
	}
	// Retries are capped.
	s = newSrv(t, flaky(100))
	c, _ = s.client(1)
	if _, err := c.Remember(context.Background(), "x", RememberOptions{}); err == nil || len(s.reqs) != 2 {
		t.Errorf("err=%v reqs=%d", err, len(s.reqs))
	}
	// A GET is retried; a recall (non-idempotent POST) is not.
	s = newSrv(t, flaky(1))
	c, _ = s.client(2)
	if _, err := c.Whoami(context.Background()); err != nil || len(s.reqs) != 2 {
		t.Errorf("GET: err=%v reqs=%d", err, len(s.reqs))
	}
	s = newSrv(t, flaky(1))
	c, _ = s.client(2)
	if _, err := c.Recall(context.Background(), "q", 1, nil); err == nil || len(s.reqs) != 1 {
		t.Errorf("recall: err=%v reqs=%d", err, len(s.reqs))
	}
	// 4xx is never retried.
	s = newSrv(t, func(w http.ResponseWriter, r *http.Request, n int) { http.Error(w, `{}`, 400) })
	c, _ = s.client(3)
	c.Remember(context.Background(), "x", RememberOptions{})
	if len(s.reqs) != 1 {
		t.Errorf("4xx retried: %d", len(s.reqs))
	}
}

func TestTransportFailuresAndTimeouts(t *testing.T) {
	s := newSrv(t, func(w http.ResponseWriter, r *http.Request, n int) {})
	c, _ := s.client(0)
	s.Close()
	_, err := c.Recall(context.Background(), "q", 1, nil)
	var e *Error
	if !errors.As(err, &e) || e.Kind != Unavailable || e.Status != 0 || !strings.HasPrefix(e.Message, "Connection failed") {
		t.Errorf("%v", err)
	}
	slow := newSrv(t, func(w http.ResponseWriter, r *http.Request, n int) { time.Sleep(300 * time.Millisecond) })
	c, _ = slow.client(0)
	c.Timeout = 50 * time.Millisecond
	if _, err := c.Recall(context.Background(), "q", 1, nil); !IsTimeout(err) {
		t.Errorf("slow answer should be a timeout: %v", err)
	}
	// A cancelled caller context is not a SAM failure.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, _ = slow.client(0)
	if _, err := c.Recall(ctx, "q", 1, nil); err == nil || errors.As(err, &e) {
		t.Errorf("cancellation reported as a service error: %v", err)
	}
}

func TestProfileHealthAndOtherCalls(t *testing.T) {
	s := newSrv(t, func(w http.ResponseWriter, r *http.Request, n int) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/profile"):
			io.WriteString(w, `{"instructions":[{"description":"Be brief"}],"preferences":[{"key":"drink","value":"tea"}],"static":[{"key":"city","value":"Cape Town"}],"dynamic":[]}`)
		case strings.HasSuffix(r.URL.Path, "/health"):
			io.WriteString(w, `{"status":"ok"}`)
		case strings.HasSuffix(r.URL.Path, "/context"):
			io.WriteString(w, `{"context":"synth"}`)
		default:
			io.WriteString(w, `{"deleted":2}`)
		}
	})
	c, _ := s.client(0)
	ctx := context.Background()
	p, err := c.Profile(ctx)
	if err != nil || p.Instructions[0] != "Be brief" || p.Preferences[0] != "drink: tea" || p.Static[0] != "city: Cape Town" {
		t.Fatalf("%+v %v", p, err)
	}
	if h, err := c.Health(ctx); err != nil || h["status"] != "ok" || s.reqs[1].URL.Path != "/api/v1/health" {
		t.Errorf("%v %v %s", h, err, s.reqs[1].URL.Path)
	}
	if got, _ := c.QueryContext(ctx, "q", 5, nil); got != "synth" {
		t.Errorf("%q", got)
	}
	if f, _ := c.Forget(ctx, "q", true); f["deleted"] != float64(2) || !strings.Contains(s.bodies[3], `"purge":true`) {
		t.Errorf("%v %s", f, s.bodies[3])
	}
	c.Consolidate(ctx)
	c.Reflect(ctx, "q", true)
	c.RegisterScope(ctx, "user/x")
	c.RememberMany(ctx, []Message{{"user", "hi"}}, "", "", nil)
	if len(s.reqs) != 8 {
		t.Errorf("%d requests", len(s.reqs))
	}
}

func TestScopes(t *testing.T) {
	if Scopes("") != nil || Scopes("  ") != nil {
		t.Error("blank scope must be omitted")
	}
	if got := Scopes(" user/a "); len(got) != 1 || got[0][0] != "user/a" {
		t.Errorf("%v", got)
	}
}

// ---- settings ----

func TestLoadSettings(t *testing.T) {
	home := t.TempDir()
	write := func(s string) { os.WriteFile(filepath.Join(home, ConfigFile), []byte(s), 0o600) }
	if _, why := LoadSettings(home, ""); !strings.Contains(why, APIKeyEnv) {
		t.Errorf("no key: %q", why)
	}
	if _, why := LoadSettings(home, "k"); !strings.Contains(why, "endpoint and context are required") {
		t.Errorf("no file: %q", why)
	}
	write(`{"endpoint":"https://x.example/","context":"c"}`)
	s, why := LoadSettings(home, "k")
	if s == nil || why != "" || s.Endpoint != "https://x.example" || s.TopK != 5 || s.Timeout != 20*time.Second || s.PrefetchWait != 2500*time.Millisecond ||
		s.RecallMode != RecallHybrid || s.DefaultScope != "user/default" || !s.ConsolidateOnEnd || s.MaxRetries != 2 {
		t.Fatalf("%+v %q", s, why)
	}
	if c := s.Client(); c.Context != "c" || c.MaxRetries != 2 || c.Timeout != 20*time.Second {
		t.Errorf("%+v", c)
	}
	for body, want := range map[string]string{
		`{"endpoint":"https://x","context":"c","top_k":50}`:          "top_k",
		`{"endpoint":"https://x","context":"c","recall_mode":"x"}`:   "recall_mode",
		`{"endpoint":"https://x","context":"c","prefetch_wait_s":9}`: "prefetch_wait_s",
		`{"endpoint":"https://x","context":"c","timeout_s":0}`:       "timeout_s",
		`{"endpoint":"https://x","context":"c","surprise":1}`:        "unknown field",
		`{"endpoint":"ftp://x","context":"c"}`:                       "endpoint",
		`not json`:                                                   "invalid agent-memory settings",
	} {
		write(body)
		if _, why := LoadSettings(home, "k"); !strings.Contains(why, want) {
			t.Errorf("%s: %q", body, why)
		}
	}
}

// ---- breaker ----

func TestBreaker(t *testing.T) {
	b := NewBreaker(3)
	b.RecordFailure(false)
	b.RecordFailure(false)
	b.RecordOK()
	b.RecordFailure(false)
	b.RecordFailure(false)
	if b.IsOpen() {
		t.Fatal("opened before the threshold, or a success did not reset the count")
	}
	if !b.RecordFailure(false) || !b.IsOpen() {
		t.Fatal("third consecutive failure must open it, and say so")
	}
	if b.RecordFailure(false) {
		t.Error("only the opening failure reports opened")
	}
	b.RecordOK()
	if !b.IsOpen() {
		t.Error("an open breaker stays open for the session")
	}
	a := NewBreaker(10)
	if !a.RecordFailure(true) || !a.IsOpen() {
		t.Error("an auth failure opens it immediately")
	}
}

func TestBreakerRace(t *testing.T) {
	b := NewBreaker(1000)
	var wg sync.WaitGroup
	var opened atomic.Int32
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				if b.RecordFailure(false) {
					opened.Add(1)
				}
				b.RecordOK()
				b.IsOpen()
			}
		}()
	}
	wg.Wait()
	if opened.Load() > 1 {
		t.Errorf("opened %d times", opened.Load())
	}
}

// ---- outbox ----

func TestOutboxDedupesAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "outbox.jsonl")
	o := NewOutbox(path)
	p := map[string]any{"text": "likes tea"}
	if ok, err := o.Append(OpRemember, p); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, _ := o.Append(OpRemember, map[string]any{"text": "likes tea"}); ok {
		t.Error("an identical pending write must not queue twice")
	}
	if ok, _ := o.Append(OpForget, map[string]any{"query": "x"}); !ok {
		t.Error("a different write queues")
	}
	got, err := NewOutbox(path).Pending() // a fresh handle sees the same queue
	if err != nil || len(got) != 2 || got[0].Op != OpRemember || got[1].Op != OpForget || got[0].EntryID != EntryID(OpRemember, p) {
		t.Fatalf("%+v %v", got, err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
}

func TestOutboxReadsPythonFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.jsonl")
	line := `{"entry_id":"abc","op":"remember","payload":{"text":"x","labels":[]},"created_at":"2026-11-01T10:00:00.123456Z","attempts":2}` + "\n"
	os.WriteFile(path, []byte(line), 0o600)
	got, err := NewOutbox(path).Pending()
	if err != nil || len(got) != 1 || got[0].Attempts != 2 || got[0].CreatedAt.Year() != 2026 {
		t.Fatalf("%+v %v", got, err)
	}
	os.WriteFile(path, []byte("{broken\n"), 0o600)
	if _, err := NewOutbox(path).Pending(); err == nil {
		t.Error("a corrupt queue must be reported, not silently emptied")
	}
}

func TestReplayStopsAtFirstFailureKeepsTheRestAndDropsRejected(t *testing.T) {
	o := NewOutbox(filepath.Join(t.TempDir(), "o.jsonl"))
	for _, q := range []string{"a", "b", "c", "d"} {
		o.Append(OpRemember, map[string]any{"text": q})
	}
	var seen []string
	n, err := o.Replay(context.Background(), func(_ context.Context, e Entry) error {
		txt := e.Payload["text"].(string)
		seen = append(seen, txt)
		switch txt {
		case "b":
			return &Error{Kind: Rejected, Status: 400}
		case "c":
			return &Error{Kind: Unavailable, Status: 503}
		}
		return nil
	})
	if err != nil || n != 1 || strings.Join(seen, "") != "abc" {
		t.Fatalf("n=%d err=%v seen=%v", n, err, seen)
	}
	left, _ := o.Pending()
	if len(left) != 2 || left[0].Payload["text"] != "c" || left[0].Attempts != 1 || left[1].Payload["text"] != "d" {
		t.Errorf("remaining %+v", left)
	}
	// Once the backend is back everything drains, in order, and the file is gone.
	seen = nil
	n, _ = o.Replay(context.Background(), func(_ context.Context, e Entry) error { seen = append(seen, e.Payload["text"].(string)); return nil })
	if n != 2 || strings.Join(seen, "") != "cd" {
		t.Errorf("n=%d seen=%v", n, seen)
	}
	if _, err := os.Stat(o.path); !os.IsNotExist(err) {
		t.Error("an empty outbox should remove its file")
	}
}

func TestApplyReplaysEachOpThroughTheClient(t *testing.T) {
	s := newSrv(t, func(w http.ResponseWriter, r *http.Request, n int) { io.WriteString(w, `{}`) })
	c, _ := s.client(0)
	ctx := context.Background()
	for _, e := range []Entry{
		{Op: OpRemember, Payload: map[string]any{"text": "t", "labels": []any{"a=b"}, "category": "identity", "scopes": "user/x", "session_id": "s"}},
		{Op: OpRememberMany, Payload: map[string]any{"items": []any{map[string]any{"role": "user", "content": "hi"}}, "session_id": "s"}},
		{Op: OpForget, Payload: map[string]any{"query": "q", "purge": true}},
	} {
		if err := Apply(ctx, c, e); err != nil {
			t.Errorf("%s: %v", e.Op, err)
		}
	}
	if len(s.reqs) != 3 || !strings.HasSuffix(s.reqs[0].URL.Path, "/facts") || !strings.HasSuffix(s.reqs[1].URL.Path, "/facts/batch") || !strings.HasSuffix(s.reqs[2].URL.Path, "/forget") {
		t.Errorf("paths %v %v %v", s.reqs[0].URL.Path, s.reqs[1].URL.Path, s.reqs[2].URL.Path)
	}
	if !strings.Contains(s.bodies[0], `"memory_category":"identity"`) || !strings.Contains(s.bodies[2], `"purge":true`) {
		t.Errorf("bodies %v", s.bodies)
	}
	if err := Apply(ctx, c, Entry{Op: "bogus"}); !IsRejected(err) {
		t.Errorf("unknown op: %v", err)
	}
}

// Zero writes are lost across an outage: while the service is down every write is queued, and when it is back
// the replay delivers each exactly once, in order. (Scenario 7 of the Python evals, offline.)
func TestOutageLosesNoWrites(t *testing.T) {
	var down atomic.Bool
	down.Store(true)
	var mu sync.Mutex
	var got []string
	s := newSrv(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if down.Load() {
			http.Error(w, `{"message":"down"}`, http.StatusServiceUnavailable)
			return
		}
		mu.Lock()
		got = append(got, "ok")
		mu.Unlock()
		io.WriteString(w, `{}`)
	})
	c, _ := s.client(0)
	o := NewOutbox(filepath.Join(t.TempDir(), "o.jsonl"))
	br := NewBreaker(3)
	ctx := context.Background()
	const writes = 25
	for i := range writes {
		text := strings.Repeat("fact ", 1) + string(rune('A'+i))
		if br.IsOpen() {
			o.Append(OpRemember, map[string]any{"text": text})
			continue
		}
		if _, err := c.Remember(ctx, text, RememberOptions{}); err != nil {
			br.RecordFailure(IsAuth(err))
			o.Append(OpRemember, map[string]any{"text": text})
		}
	}
	if pend, _ := o.Pending(); len(pend) != writes {
		t.Fatalf("queued %d of %d writes", len(pend), writes)
	}
	down.Store(false)
	var order []string
	n, err := o.Replay(ctx, func(ctx context.Context, e Entry) error {
		order = append(order, e.Payload["text"].(string))
		return Apply(ctx, c, e)
	})
	if err != nil || n != writes || len(got) != writes {
		t.Fatalf("replayed %d, delivered %d, err %v", n, len(got), err)
	}
	if order[0] != "fact A" || order[writes-1] != "fact Y" {
		t.Errorf("order %v", order)
	}
}

func TestProfileCache(t *testing.T) {
	c := ProfileCache{Path: filepath.Join(t.TempDir(), "d", "core.json")}
	if c.Load() != "" {
		t.Error("empty cache must load empty")
	}
	if err := c.Save("## About\n- x"); err != nil {
		t.Fatal(err)
	}
	if c.Load() != "## About\n- x" {
		t.Errorf("%q", c.Load())
	}
	os.WriteFile(c.Path, []byte("garbage"), 0o600)
	if c.Load() != "" {
		t.Error("a corrupt cache must load empty")
	}
}

func FuzzEntryIDAndOutboxLine(f *testing.F) {
	f.Add(`{"entry_id":"a","op":"remember","payload":{"text":"é"},"created_at":"2026-01-01T00:00:00Z","attempts":0}`)
	f.Add("")
	f.Fuzz(func(t *testing.T, line string) {
		path := filepath.Join(t.TempDir(), "o.jsonl")
		os.WriteFile(path, []byte(line+"\n"), 0o600)
		es, err := NewOutbox(path).Pending()
		if err == nil {
			for _, e := range es {
				_ = EntryID(e.Op, e.Payload)
			}
		}
	})
}

func TestErrorTextAndKindOf(t *testing.T) {
	e := &Error{Kind: AuthFailed, Status: 401, Message: "bad key"}
	if e.Error() != "agent memory auth failed (HTTP 401): bad key" {
		t.Error(e.Error())
	}
	if (&Error{Kind: Unavailable, Message: "Connection failed: x"}).Error() != "agent memory unavailable: Connection failed: x" {
		t.Error("transport error text")
	}
	if _, ok := KindOf(errors.New("plain")); ok {
		t.Error("a plain error has no kind")
	}
	if !IsAuth(e) || IsRejected(e) || IsTimeout(e) {
		t.Error("kind predicates")
	}
}

func TestDefaultsAndRealSleep(t *testing.T) {
	c := &Client{}
	if c.timeout() != 20*time.Second || c.now().IsZero() {
		t.Error("defaults")
	}
	if err := c.sleep(context.Background(), time.Millisecond); err != nil {
		t.Error(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.sleep(ctx, time.Hour); err == nil {
		t.Error("a cancelled sleep must return")
	}
}

func TestEntryTextShapes(t *testing.T) {
	for in, want := range map[string]string{
		`"plain"`:               "plain",
		`{"content":"c"}`:       "c",
		`{"value":"v"}`:         "v",
		`{"other":1}`:           `{"other":1}`,
		`42`:                    "42",
		`{"key":"k","value":3}`: "k: 3",
	} {
		if got := entryText(json.RawMessage(in)); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
}

func TestPyJSONTypesAndFloats(t *testing.T) {
	got := pyJSON(map[string]any{"b": []string{"x", "é"}, "a": 1, "c": int64(2), "d": 2.5, "e": 100.0, "f": []map[string]string{{"k": "v"}},
		"g": struct{ N int }{3}, "h": "😀"})
	want := `{"a": 1, "b": ["x", "\u00e9"], "c": 2, "d": 2.5, "e": 100.0, "f": [{"k": "v"}], "g": {"N": 3}, "h": "\ud83d\ude00"}`
	if got != want {
		t.Errorf("\n got %s\nwant %s", got, want)
	}
	for f, w := range map[float64]string{1e22: "1e+22", 0.00001: "1e-05", 3: "3.0"} {
		if pyFloat(f) != w {
			t.Errorf("pyFloat(%v) = %s, want %s", f, pyFloat(f), w)
		}
	}
}

func TestRememberManyAndContextOptions(t *testing.T) {
	s := newSrv(t, func(w http.ResponseWriter, r *http.Request, n int) { io.WriteString(w, `{}`) })
	c, _ := s.client(0)
	c.RememberMany(context.Background(), []Message{{"user", "hi"}}, "sid", "user/x", []string{"a=b"})
	for _, want := range []string{`"session_id":"sid"`, `"scopes":[["user/x"]]`, `"labels":["a=b"]`, `"messages":[{"role":"user","content":"hi"}]`} {
		if !strings.Contains(s.bodies[0], want) {
			t.Errorf("%s lacks %s", s.bodies[0], want)
		}
	}
	c.QueryContext(context.Background(), "q", 2, []string{"recent"})
	if !strings.Contains(s.bodies[1], `"lens":["recent"]`) {
		t.Error(s.bodies[1])
	}
}

func TestOutboxWriteFailureIsReported(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	os.WriteFile(blocker, []byte("x"), 0o600)
	o := NewOutbox(filepath.Join(blocker, "sub", "o.jsonl")) // a path below a regular file can never be created
	if _, err := o.Append(OpRemember, map[string]any{"text": "x"}); err == nil {
		t.Error("an unwritable outbox must say so: a silent drop would lose the write")
	}
	if err := (ProfileCache{Path: filepath.Join(blocker, "c.json")}).Save("x"); err == nil {
		t.Error("an unwritable cache must say so")
	}
}

func TestReplayHonoursCancellation(t *testing.T) {
	o := NewOutbox(filepath.Join(t.TempDir(), "o.jsonl"))
	o.Append(OpRemember, map[string]any{"text": "a"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n, err := o.Replay(ctx, func(context.Context, Entry) error { t.Error("applied after cancellation"); return nil })
	if n != 0 || err != nil {
		t.Errorf("n=%d err=%v", n, err)
	}
	if p, _ := o.Pending(); len(p) != 1 {
		t.Error("a cancelled replay must keep every entry")
	}
}

func TestHitIDMayBeANumber(t *testing.T) {
	var h []Hit
	if err := json.Unmarshal([]byte(`[{"id":"a","text":"t","score":0.5},{"id":7,"text":"u","score":1},{"text":"v"}]`), &h); err != nil {
		t.Fatal(err)
	}
	if h[0].ID != "a" || h[1].ID != "7" || h[2].ID != "" || h[1].Text != "u" || h[1].Score != 1 {
		t.Errorf("%+v", h)
	}
}
