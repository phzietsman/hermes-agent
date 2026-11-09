package sam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/surrealdb/surrealdb.go/memory"
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

// Client talks to one SAM context through the official SurrealDB SDK (surrealdb.go/memory), which owns the wire
// protocol: paths, headers, idempotency keys and the retry schedule. This type adds Hermes's failure
// classification (Kind) and a few flattened result shapes. It holds only configuration, so it is cheap to copy.
type Client struct {
	Endpoint string
	Context  string
	APIKey   string
	// Timeout bounds one attempt (default 20s). MaxRetries is the number of retries of a retryable call (the
	// SDK retries idempotent writes and reads on transport errors and 5xx, on its 0.25s, 0.5s, 1s schedule).
	Timeout    time.Duration
	MaxRetries int
}

const userAgent = "hermes-agent-go/1.0"

func (c *Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 20 * time.Second
}

func (c *Client) sdk() (*memory.Client, error) {
	m, err := memory.New(c.Context, c.Endpoint, c.APIKey,
		memory.WithTimeout(c.timeout()), memory.WithMaxRetries(max(0, c.MaxRetries)), memory.WithUserAgent(userAgent))
	if err != nil {
		return nil, &Error{Kind: Rejected, Message: err.Error()}
	}
	return m, nil
}

// classify turns an SDK error into Hermes's Error (CONTRACT.md, "Errors"). A cancelled caller context is
// returned as is: it is the caller's decision, not the service's failure.
func classify(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var ae *memory.APIError
	if !errors.As(err, &ae) {
		return &Error{Kind: Rejected, Message: err.Error()}
	}
	e := &Error{Status: ae.StatusCode, Message: ae.Message, TraceID: ae.TraceID}
	low := strings.ToLower(ae.Message)
	switch s := ae.StatusCode; {
	case s == http.StatusUnauthorized:
		e.Kind = AuthFailed
	case s >= 400 && s < 500 && s != http.StatusRequestTimeout && s != http.StatusTooManyRequests:
		e.Kind = Rejected
	case strings.Contains(low, "timed out") || strings.Contains(low, "deadline exceeded"):
		e.Kind = Timeout
	default:
		e.Kind = Unavailable
	}
	return e
}

// asMap flattens a typed SDK response into the loose shape callers print or ignore.
func asMap(v any) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// Scopes encodes a scope as the DNF the service takes: a bare path is one clause of one path. An empty path
// yields nil (the field is omitted: the server's default write region).
func Scopes(path string) memory.ScopeSets {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	return memory.ScopeSets{{path}}
}

// lensSets turns lens paths into DNF: each path is its own clause, so several lenses widen the view (OR).
func lensSets(lens []string) memory.ScopeSets {
	var out memory.ScopeSets
	for _, l := range lens {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, []string{l})
		}
	}
	return out
}

// Hit is one recalled memory.
type Hit struct {
	ID    string  `json:"id"`
	Text  string  `json:"text"`
	Score float64 `json:"score"`
}

// RememberOptions are the optional fields of a single write.
type RememberOptions struct {
	Labels    []string
	Category  string // identity, knowledge or context
	Scopes    string
	SessionID string
}

// Remember stores one fact. The SDK marks it idempotent: a retry within 30 s does not duplicate it.
func (c *Client) Remember(ctx context.Context, text string, o RememberOptions) (map[string]any, error) {
	m, err := c.sdk()
	if err != nil {
		return nil, err
	}
	req := &memory.RememberRequest{Text: text, Labels: o.Labels, SessionID: o.SessionID, Scopes: Scopes(o.Scopes)}
	if o.Category != "" {
		cat := memory.MemoryCategory(o.Category)
		req.MemoryCategory = &cat
	}
	out, err := m.Remember(ctx, req)
	if err != nil {
		return nil, classify(ctx, err)
	}
	return asMap(out), nil
}

// Message is one turn of a conversation for RememberMany.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// RememberMany stores a batch of messages. Idempotent.
func (c *Client) RememberMany(ctx context.Context, items []Message, sessionID, scopes string, labels []string) (map[string]any, error) {
	m, err := c.sdk()
	if err != nil {
		return nil, err
	}
	req := &memory.RememberManyRequest{SessionID: sessionID, Scopes: Scopes(scopes), Labels: labels}
	for _, it := range items {
		req.Messages = append(req.Messages, memory.BatchMessage{Role: memory.TurnRole(it.Role), Content: it.Content})
	}
	out, err := m.RememberMany(ctx, req)
	if err != nil {
		return nil, classify(ctx, err)
	}
	return asMap(out), nil
}

// Recall searches memory. Not retried: a slow recall must not stack up.
func (c *Client) Recall(ctx context.Context, query string, k int, lens []string) ([]Hit, error) {
	m, err := c.sdk()
	if err != nil {
		return nil, err
	}
	out, err := m.Recall(ctx, &memory.RecallRequest{Query: query, K: k, Lens: lensSets(lens)})
	if err != nil {
		return nil, classify(ctx, err)
	}
	hits := make([]Hit, 0, len(out.Hits))
	for _, h := range out.Hits {
		hits = append(hits, Hit{ID: h.ID, Text: h.Text, Score: h.Score})
	}
	return hits, nil
}

// QueryContext asks the service for synthesised context text.
func (c *Client) QueryContext(ctx context.Context, query string, k int, lens []string) (string, error) {
	m, err := c.sdk()
	if err != nil {
		return "", err
	}
	out, err := m.QueryContext(ctx, &memory.ContextQueryRequest{Query: query, K: k, Lens: lensSets(lens)})
	if err != nil {
		return "", classify(ctx, err)
	}
	return out.Context, nil
}

// Forget deletes memories matching query (purge removes them for good).
func (c *Client) Forget(ctx context.Context, query string, purge bool) (map[string]any, error) {
	m, err := c.sdk()
	if err != nil {
		return nil, err
	}
	var opts []memory.ForgetOption
	if purge {
		opts = append(opts, memory.WithPurge())
	}
	out, err := m.Forget(ctx, query, opts...)
	if err != nil {
		return nil, classify(ctx, err)
	}
	return asMap(out), nil
}

// Consolidate folds facts into observations.
func (c *Client) Consolidate(ctx context.Context) (map[string]any, error) {
	m, err := c.sdk()
	if err != nil {
		return nil, err
	}
	out, err := m.Consolidate(ctx, memory.ConsolidateRequest{})
	if err != nil {
		return nil, classify(ctx, err)
	}
	return asMap(out), nil
}

// Reflect asks the service to reason over memory (persist stores what it concludes).
func (c *Client) Reflect(ctx context.Context, query string, persist bool) (map[string]any, error) {
	m, err := c.sdk()
	if err != nil {
		return nil, err
	}
	out, err := m.Reflect(ctx, memory.ReflectRequest{Query: query, Persist: persist})
	if err != nil {
		return nil, classify(ctx, err)
	}
	return asMap(out), nil
}

// Profile is the standing facts about the user, flattened to text.
type Profile struct {
	Instructions, Preferences, Static []string
}

// Profile reads the user's standing profile: instructions as their description, preferences and static facts
// as "key: value".
func (c *Client) Profile(ctx context.Context) (Profile, error) {
	m, err := c.sdk()
	if err != nil {
		return Profile{}, err
	}
	out, err := m.Profile(ctx)
	if err != nil {
		return Profile{}, classify(ctx, err)
	}
	p := Profile{Instructions: []string{}, Preferences: []string{}, Static: []string{}}
	for _, i := range out.Instructions {
		p.Instructions = append(p.Instructions, i.Description)
	}
	for _, e := range out.Preferences {
		p.Preferences = append(p.Preferences, entryText(e))
	}
	for _, e := range out.Static {
		p.Static = append(p.Static, entryText(e))
	}
	return p, nil
}

func entryText(e memory.ProfileEntry) string {
	if e.Key == "" {
		return e.Value
	}
	return fmt.Sprintf("%s: %s", e.Key, e.Value)
}

// RegisterScope makes a scope path exist.
func (c *Client) RegisterScope(ctx context.Context, path string) error {
	m, err := c.sdk()
	if err != nil {
		return err
	}
	_, err = m.Scopes().Register(ctx, memory.RegisterScopeRequest{Path: path})
	return classify(ctx, err)
}

// Whoami reads the caller's principal.
func (c *Client) Whoami(ctx context.Context) (map[string]any, error) {
	m, err := c.sdk()
	if err != nil {
		return nil, err
	}
	out, err := m.Whoami(ctx)
	if err != nil {
		return nil, classify(ctx, err)
	}
	return asMap(out), nil
}

// Health checks the service (not context-scoped).
func (c *Client) Health(ctx context.Context) error {
	m, err := c.sdk()
	if err != nil {
		return err
	}
	return classify(ctx, m.Health(ctx))
}
