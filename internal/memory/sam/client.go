package sam

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Kind classifies a failure the way Hermes reacts to it (CONTRACT.md, "Errors").
type Kind int

const (
	// Unavailable: transport failure, 408, 429 or 5xx. Counts toward the breaker; writes stay queued.
	Unavailable Kind = iota
	// Timeout: the service answered too slowly. Unavailable, but reads do not count it toward the breaker.
	Timeout
	// AuthFailed: 401. Opens the breaker at once; the writes stay queued.
	AuthFailed
	// Rejected: another 4xx. The service is fine and retrying the same request cannot succeed, so the
	// outbox drops the entry instead of letting it block everything behind it.
	Rejected
)

func (k Kind) String() string {
	return [...]string{"unavailable", "timeout", "auth failed", "rejected"}[k]
}

// Error is a failed request.
type Error struct {
	Kind    Kind
	Status  int // 0 for a transport failure
	Message string
	TraceID string
}

func (e *Error) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("agent memory %s (HTTP %d): %s", e.Kind, e.Status, e.Message)
	}
	return fmt.Sprintf("agent memory %s: %s", e.Kind, e.Message)
}

// KindOf reports the Kind of err, and false when err is not a SAM error.
func KindOf(err error) (Kind, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind, true
	}
	return 0, false
}

// IsRejected reports a request the service refused for good.
func IsRejected(err error) bool { k, ok := KindOf(err); return ok && k == Rejected }

// IsAuth reports a bad or unauthorised key.
func IsAuth(err error) bool { k, ok := KindOf(err); return ok && k == AuthFailed }

// IsTimeout reports a slow answer (as opposed to a down service).
func IsTimeout(err error) bool { k, ok := KindOf(err); return ok && k == Timeout }

// Client talks to one SAM context over HTTP/JSON. It is safe for concurrent use.
type Client struct {
	Endpoint string
	Context  string
	APIKey   string
	// Timeout bounds one attempt (default 20s). MaxRetries is the number of retries of a retryable call
	// (default 2; the schedule is 0.25s, 0.5s, 1s, truncated).
	Timeout    time.Duration
	MaxRetries int
	HTTP       *http.Client
	// Sleep waits between retries; nil uses a context-aware timer.
	Sleep func(ctx context.Context, d time.Duration) error
	// Now is the clock for idempotency buckets; nil means time.Now.
	Now func() time.Time
}

var backoff = []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second}

func (c *Client) base() string {
	return strings.TrimRight(c.Endpoint, "/") + c.path("")
}

func (c *Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 20 * time.Second
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// idempotencyKey is sha256 over METHOD \0 path \0 body \0 bucket, with a 30 s bucket, so a retry of the same
// write inside the window dedupes server-side. The body bytes are this client's own encoding: only repeats from
// this client need to match.
func idempotencyKey(method, path string, body []byte, now time.Time) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%d", method, path, body, now.Unix()/30)
	return hex.EncodeToString(h.Sum(nil))
}

// do sends one request (with the retry policy) and decodes a 2xx JSON body into out (nil to discard).
func (c *Client) do(ctx context.Context, method, url, path string, body any, idempotent bool, out any) error {
	var raw []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return &Error{Kind: Rejected, Message: "cannot encode request: " + err.Error()}
		}
		raw = b
	}
	limit := min(c.maxRetries(), len(backoff))
	var last error
	for attempt := 0; ; attempt++ {
		err := c.once(ctx, method, url, path, raw, idempotent, out)
		if err == nil {
			return nil
		}
		last = err
		var e *Error
		retryable := errors.As(err, &e) && (e.Status >= 500 || e.Status == 0) && (method == http.MethodGet || idempotent)
		if !retryable || attempt >= limit || ctx.Err() != nil {
			return last
		}
		if err := c.sleep(ctx, backoff[attempt]); err != nil {
			return last
		}
	}
}

func (c *Client) maxRetries() int { return max(0, c.MaxRetries) }

func (c *Client) once(ctx context.Context, method, url, path string, raw []byte, idempotent bool, out any) error {
	rctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	var rd io.Reader
	if raw != nil {
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(rctx, method, url, rd)
	if err != nil {
		return &Error{Kind: Rejected, Message: err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "hermes-agent-go/1.0")
	if raw != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idempotent {
		req.Header.Set("Idempotency-Key", idempotencyKey(method, path, raw, c.now()))
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		kind := Unavailable
		if errors.Is(err, context.DeadlineExceeded) || strings.Contains(strings.ToLower(err.Error()), "timed out") || strings.Contains(strings.ToLower(err.Error()), "deadline exceeded") {
			kind = Timeout
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &Error{Kind: kind, Message: "Connection failed: " + err.Error()}
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode >= 400 {
		return httpError(resp, data)
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return &Error{Kind: Unavailable, Status: resp.StatusCode, Message: "unreadable response: " + err.Error()}
	}
	return nil
}

func httpError(resp *http.Response, data []byte) *Error {
	e := &Error{Status: resp.StatusCode, TraceID: resp.Header.Get("X-Trace-Id")}
	var body map[string]any
	if json.Unmarshal(data, &body) == nil {
		for _, k := range []string{"message", "title", "error"} {
			if s, ok := body[k].(string); ok && s != "" {
				e.Message = s
				break
			}
		}
	}
	if e.Message == "" {
		e.Message = strings.TrimSpace(string(data))
	}
	if e.Message == "" {
		e.Message = "Memory request failed"
	}
	switch s := resp.StatusCode; {
	case s == http.StatusUnauthorized:
		e.Kind = AuthFailed
	case s >= 400 && s < 500 && s != http.StatusRequestTimeout && s != http.StatusTooManyRequests:
		e.Kind = Rejected
	case strings.Contains(strings.ToLower(e.Message), "timed out"):
		e.Kind = Timeout
	default:
		e.Kind = Unavailable
	}
	return e
}

// Scopes encodes a scope as the DNF the service takes: a bare path is one clause of one path. An empty path
// yields nil (the field is omitted: the server's default write region).
func Scopes(path string) [][]string {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	return [][]string{{path}}
}

// ---- operations ----

// Hit is one recalled memory.
type Hit struct {
	ID    string  `json:"id"`
	Text  string  `json:"text"`
	Score float64 `json:"score"`
}

// UnmarshalJSON accepts an id given as a string or a number (Python stringified whatever it got).
func (h *Hit) UnmarshalJSON(b []byte) error {
	var aux struct {
		ID    json.RawMessage `json:"id"`
		Text  string          `json:"text"`
		Score float64         `json:"score"`
	}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	h.Text, h.Score = aux.Text, aux.Score
	h.ID = strings.Trim(string(aux.ID), `"`)
	if h.ID == "null" {
		h.ID = ""
	}
	return nil
}

// RememberOptions are the optional fields of a single write.
type RememberOptions struct {
	Labels    []string
	Category  string // identity, knowledge or context
	Scopes    string
	SessionID string
}

// Remember stores one fact. It is idempotent: a retry within 30 s does not duplicate it.
func (c *Client) Remember(ctx context.Context, text string, o RememberOptions) (map[string]any, error) {
	body := map[string]any{"text": text}
	if len(o.Labels) > 0 {
		body["labels"] = o.Labels
	}
	if o.Category != "" {
		body["memory_category"] = o.Category
	}
	if s := Scopes(o.Scopes); s != nil {
		body["scopes"] = s
	}
	if o.SessionID != "" {
		body["session_id"] = o.SessionID
	}
	var out map[string]any
	return out, c.do(ctx, http.MethodPost, c.base()+"/facts", c.path("/facts"), body, true, &out)
}

// Message is one turn of a conversation for RememberMany.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// RememberMany stores a batch of messages. Idempotent.
func (c *Client) RememberMany(ctx context.Context, items []Message, sessionID, scopes string, labels []string) (map[string]any, error) {
	body := map[string]any{"messages": items}
	if sessionID != "" {
		body["session_id"] = sessionID
	}
	if s := Scopes(scopes); s != nil {
		body["scopes"] = s
	}
	if len(labels) > 0 {
		body["labels"] = labels
	}
	var out map[string]any
	return out, c.do(ctx, http.MethodPost, c.base()+"/facts/batch", c.path("/facts/batch"), body, true, &out)
}

// Recall searches memory. Not retried: a slow recall must not stack up.
func (c *Client) Recall(ctx context.Context, query string, k int, lens []string) ([]Hit, error) {
	body := map[string]any{"query": query, "k": k}
	if len(lens) > 0 {
		body["lens"] = lens
	}
	var out struct {
		Hits []Hit `json:"hits"`
	}
	if err := c.do(ctx, http.MethodPost, c.base()+"/query", c.path("/query"), body, false, &out); err != nil {
		return nil, err
	}
	return out.Hits, nil
}

// QueryContext asks the service for synthesised context text.
func (c *Client) QueryContext(ctx context.Context, query string, k int, lens []string) (string, error) {
	body := map[string]any{"query": query, "k": k}
	if len(lens) > 0 {
		body["lens"] = lens
	}
	var out struct {
		Context string `json:"context"`
	}
	err := c.do(ctx, http.MethodPost, c.base()+"/context", c.path("/context"), body, false, &out)
	return out.Context, err
}

// Forget deletes memories matching query (purge removes them for good).
func (c *Client) Forget(ctx context.Context, query string, purge bool) (map[string]any, error) {
	body := map[string]any{"query": query}
	if purge {
		body["purge"] = true
	}
	var out map[string]any
	return out, c.do(ctx, http.MethodPost, c.base()+"/forget", c.path("/forget"), body, false, &out)
}

// Consolidate folds facts into observations.
func (c *Client) Consolidate(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, http.MethodPost, c.base()+"/consolidate", c.path("/consolidate"), nil, false, &out)
}

// Reflect asks the service to reason over memory (persist stores what it concludes).
func (c *Client) Reflect(ctx context.Context, query string, persist bool) (map[string]any, error) {
	body := map[string]any{"query": query}
	if persist {
		body["persist"] = true
	}
	var out map[string]any
	return out, c.do(ctx, http.MethodPost, c.base()+"/reflect", c.path("/reflect"), body, false, &out)
}

// Profile is the standing facts about the user, flattened to text.
type Profile struct {
	Instructions, Preferences, Static []string
}

// Profile reads the user's standing profile.
func (c *Client) Profile(ctx context.Context) (Profile, error) {
	var raw struct {
		Instructions []json.RawMessage `json:"instructions"`
		Preferences  []json.RawMessage `json:"preferences"`
		Static       []json.RawMessage `json:"static"`
	}
	if err := c.do(ctx, http.MethodGet, c.base()+"/profile", c.path("/profile"), nil, false, &raw); err != nil {
		return Profile{}, err
	}
	return Profile{Instructions: entryTexts(raw.Instructions), Preferences: entryTexts(raw.Preferences), Static: entryTexts(raw.Static)}, nil
}

// entryText flattens a profile entry. Free-text objects (instructions) use the first string of description,
// text; key/value entries (preferences, static facts) read "key: value"; otherwise value or content.
func entryText(raw json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
		return string(raw)
	}
	for _, k := range []string{"description", "text"} {
		if s, ok := m[k].(string); ok {
			return s
		}
	}
	if name, ok := m["key"]; ok {
		if v, ok := m["value"]; ok {
			return fmt.Sprintf("%v: %v", name, v)
		}
	}
	for _, k := range []string{"value", "content"} {
		if s, ok := m[k].(string); ok {
			return s
		}
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func entryTexts(rs []json.RawMessage) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, entryText(r))
	}
	return out
}

// RegisterScope makes a scope path exist.
func (c *Client) RegisterScope(ctx context.Context, path string) error {
	return c.do(ctx, http.MethodPost, c.base()+"/scopes", c.path("/scopes"), map[string]any{"path": path}, false, nil)
}

// Whoami reads the caller's principal.
func (c *Client) Whoami(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, http.MethodGet, c.base()+"/me", c.path("/me"), nil, false, &out)
}

// Health checks the service (not context-scoped).
func (c *Client) Health(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	u := strings.TrimRight(c.Endpoint, "/") + "/api/v1/health"
	return out, c.do(ctx, http.MethodGet, u, "/api/v1/health", nil, false, &out)
}

func (c *Client) path(suffix string) string {
	return "/api/v1/" + escapeContext(c.Context) + suffix
}

// escapeContext percent-encodes everything but unreserved characters (Python's quote(value, safe="")), so a
// "/" in a context id stays inside one path segment.
func escapeContext(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}
