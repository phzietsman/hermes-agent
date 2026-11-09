package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// DefaultBaseURL is the native Anthropic API.
	DefaultBaseURL = "https://api.anthropic.com"
	// anthropicVersion is the API version header every request carries.
	anthropicVersion = "2023-06-01"
	// DefaultIdleTimeout is how long a response may go without a byte before it is abandoned. It
	// matches the Python client's 900 second read timeout, which also counts time between reads.
	DefaultIdleTimeout = 900 * time.Second
	connectTimeout     = 10 * time.Second
	maxErrorBody       = 1 << 20
)

// Auth adds credentials to a request. It reports whether the credential is a Claude Code OAuth token,
// which changes the beta list and request transforms.
type Auth interface {
	Apply(ctx context.Context, h http.Header) (oauth bool, err error)
}

// APIKey is an Anthropic Console key, sent as x-api-key.
type APIKey string

// Apply sets x-api-key.
func (k APIKey) Apply(_ context.Context, h http.Header) (bool, error) {
	h.Set("x-api-key", string(k))
	return false, nil
}

// APIError is a non-2xx response from the API.
type APIError struct {
	Status     int
	Type       string // error.type from the body, e.g. rate_limit_error, overloaded_error
	Message    string
	RequestID  string
	RetryAfter string // the retry-after header, verbatim
	Header     http.Header
	Body       string
}

func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = truncate(e.Body, 200)
	}
	return fmt.Sprintf("anthropic API error %d %s: %s", e.Status, e.Type, msg)
}

// ErrIdleTimeout means the response produced no bytes for the idle timeout.
var ErrIdleTimeout = errors.New("anthropic response idle timeout")

// Client calls the Messages API over net/http. Retry is the caller's job: the SDK's default retry
// uses its own backoff that ignores retry-after, so the loop owns it (task 2.1e).
type Client struct {
	BaseURL string
	Auth    Auth
	// HTTP is optional; the default has a 10s dial and TLS timeout and no overall deadline.
	HTTP        *http.Client
	IdleTimeout time.Duration
}

func (c *Client) baseURL() string {
	if c.BaseURL == "" {
		return DefaultBaseURL
	}
	return strings.TrimRight(c.BaseURL, "/")
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Transport: &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: connectTimeout}).DialContext,
		TLSHandshakeTimeout: connectTimeout,
		ForceAttemptHTTP2:   true,
	}}
}

// CallOptions tunes one Create call.
type CallOptions struct {
	// NoStream sends a plain create() instead of streaming.
	NoStream bool
	// OnEvent sees every stream event (liveness watchdogs, live display). An error abandons the stream.
	OnEvent func(StreamEvent) error
	// OnResponse sees the HTTP response once its headers arrive, before the body is read.
	OnResponse func(*http.Response)
}

// Create sends the request and returns the final message. It streams by default and aggregates the
// events, falling back to a non-streaming call only for gateways that explicitly cannot stream.
func (c *Client) Create(ctx context.Context, req Request, opts CallOptions) (*Message, error) {
	if !opts.NoStream {
		req.Stream = true
		msg, err := c.do(ctx, req, opts)
		if err == nil || !streamUnavailable(err) || ctx.Err() != nil {
			return msg, err
		}
	}
	req.Stream = false
	return c.do(ctx, req, opts)
}

// streamUnavailable is true when a stream call should fall back to create().
func streamUnavailable(err error) bool {
	if errors.Is(err, ErrUnexpectedEventOrder) {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "stream") && strings.Contains(s, "not supported")
}

func (c *Client) do(ctx context.Context, req Request, opts CallOptions) (*Message, error) {
	body, err := req.Body()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	h := hreq.Header
	h.Set("content-type", "application/json")
	h.Set("anthropic-version", anthropicVersion)
	if req.Stream {
		h.Set("accept", "text/event-stream")
	} else {
		h.Set("accept", "application/json")
	}
	oauth := false
	if c.Auth != nil {
		if oauth, err = c.Auth.Apply(ctx, h); err != nil {
			return nil, fmt.Errorf("credentials: %w", err)
		}
	}
	// A per-request anthropic-beta replaces the client's, so it carries the full list.
	if _, ok := req.ExtraHeaders["anthropic-beta"]; !ok {
		h.Set("anthropic-beta", BetaHeader(oauth))
	}
	for k, v := range req.ExtraHeaders {
		h.Set(k, v)
	}

	resp, err := c.httpClient().Do(hreq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if opts.OnResponse != nil {
		opts.OnResponse(resp)
	}
	if resp.StatusCode/100 != 2 {
		return nil, newAPIError(resp)
	}

	idle := c.IdleTimeout
	if idle <= 0 {
		idle = DefaultIdleTimeout
	}
	r := newIdleReader(resp.Body, idle, cancel)
	defer r.stop()

	var msg *Message
	if req.Stream || strings.HasPrefix(resp.Header.Get("content-type"), "text/event-stream") {
		// Some gateways answer even a plain create() with an event stream.
		msg, err = ReadMessage(r, opts.OnEvent)
	} else {
		msg, err = decodeMessage(r)
	}
	if err != nil && r.fired() {
		return nil, ErrIdleTimeout
	}
	return msg, err
}

func newAPIError(resp *http.Response) *APIError {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	e := &APIError{
		Status: resp.StatusCode, RequestID: resp.Header.Get("request-id"), RetryAfter: resp.Header.Get("retry-after"),
		Header: resp.Header.Clone(), Body: string(raw),
	}
	var parsed struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &parsed) == nil {
		e.Type, e.Message = parsed.Error.Type, parsed.Error.Message
		if e.Message == "" {
			e.Message = parsed.Message
		}
	}
	return e
}

// decodeMessage reads a non-streaming Messages response.
func decodeMessage(r io.Reader) (*Message, error) {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	m := &Message{ID: strOf(raw["id"]), Model: strOf(raw["model"]), StopReason: strOf(raw["stop_reason"]), StopSequence: strOf(raw["stop_sequence"]), StopDetails: raw["stop_details"]}
	if list, ok := raw["content"].([]any); ok {
		for _, b := range list {
			if blk, ok := b.(map[string]any); ok {
				m.Content = append(m.Content, blk)
			}
		}
	}
	setUsage(&m.Usage, raw["usage"])
	return m, nil
}

// idleReader abandons the request when the body goes quiet. Every successful Read resets the timer.
type idleReader struct {
	r     io.Reader
	timer *time.Timer
	d     time.Duration
	done  atomic.Bool
}

func newIdleReader(r io.Reader, d time.Duration, cancel context.CancelFunc) *idleReader {
	ir := &idleReader{r: r, d: d}
	ir.timer = time.AfterFunc(d, func() { ir.done.Store(true); cancel() })
	return ir
}

func (i *idleReader) Read(p []byte) (int, error) {
	n, err := i.r.Read(p)
	if n > 0 {
		i.timer.Reset(i.d)
	}
	return n, err
}

func (i *idleReader) stop()       { i.timer.Stop() }
func (i *idleReader) fired() bool { return i.done.Load() }
