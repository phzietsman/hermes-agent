package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phzietsman/hermes-agent/internal/llm"
)

func userMsg(text string) []llm.Message {
	return []llm.Message{{Role: llm.RoleUser, Content: llm.Text(text)}}
}

func buildReq(t *testing.T, o RequestOptions) Request {
	t.Helper()
	if o.Model == "" {
		o.Model = "claude-opus-4-5"
	}
	if o.Messages == nil {
		o.Messages = userMsg("hi")
	}
	req, err := BuildRequest(o)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

const okStream = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"model\":\"x\",\"usage\":{\"input_tokens\":4,\"output_tokens\":1}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

const okJSON = `{"id":"m2","model":"x","stop_reason":"end_turn","content":[{"type":"text","text":"plain"}],"usage":{"input_tokens":7,"output_tokens":3,"cache_read_input_tokens":2}}`

func TestCreateStreamsAndSendsTheRightRequest(t *testing.T) {
	var gotBody map[string]any
	var gotHeader http.Header
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotHeader = r.URL.Path, r.Header.Clone()
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("content-type", "text/event-stream")
		io.WriteString(w, okStream)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL + "/", Auth: APIKey("sk-ant-api-test")}
	var events []string
	var sawResponse bool
	msg, err := c.Create(context.Background(), buildReq(t, RequestOptions{}), CallOptions{
		OnEvent:    func(ev StreamEvent) error { events = append(events, ev.Type); return nil },
		OnResponse: func(r *http.Response) { sawResponse = r.StatusCode == 200 },
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, _ := Normalize(msg, NormalizeOptions{})
	if resp.Content == nil || *resp.Content != "hello" || resp.Usage.InputTokens != 4 || resp.Usage.OutputTokens != 2 {
		t.Fatalf("response = %+v", resp)
	}
	if len(events) != 6 || !sawResponse {
		t.Fatalf("events = %v, saw response = %v", events, sawResponse)
	}
	if gotPath != "/v1/messages" {
		t.Errorf("path = %q", gotPath)
	}
	for k, want := range map[string]string{
		"X-Api-Key": "sk-ant-api-test", "Anthropic-Version": "2023-06-01", "Content-Type": "application/json",
		"Accept": "text/event-stream", "Anthropic-Beta": BetaHeader(false),
	} {
		if got := gotHeader.Get(k); got != want {
			t.Errorf("header %s = %q, want %q", k, got, want)
		}
	}
	if gotHeader.Get("Authorization") != "" {
		t.Error("an API key must not send Authorization")
	}
	if gotBody["stream"] != true || gotBody["model"] != "claude-opus-4-5" {
		t.Errorf("body = %v", gotBody)
	}
}

type oauthStub struct{}

func (oauthStub) Apply(_ context.Context, h http.Header) (bool, error) {
	h.Set("Authorization", "Bearer tok")
	return true, nil
}

func TestOAuthAuthSelectsOAuthBetas(t *testing.T) {
	var beta string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		beta = r.Header.Get("anthropic-beta")
		w.Header().Set("content-type", "text/event-stream")
		io.WriteString(w, okStream)
	}))
	defer srv.Close()
	if _, err := (&Client{BaseURL: srv.URL, Auth: oauthStub{}}).Create(context.Background(), buildReq(t, RequestOptions{OAuth: true}), CallOptions{}); err != nil {
		t.Fatal(err)
	}
	if beta != BetaHeader(true) {
		t.Errorf("beta = %q, want %q", beta, BetaHeader(true))
	}
}

func TestPerRequestBetaReplacesTheClientBeta(t *testing.T) {
	var beta string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		beta = r.Header.Get("anthropic-beta")
		w.Header().Set("content-type", "text/event-stream")
		io.WriteString(w, okStream)
	}))
	defer srv.Close()
	req := buildReq(t, RequestOptions{Model: "claude-opus-4-8", FastMode: true})
	if _, err := (&Client{BaseURL: srv.URL, Auth: APIKey("k")}).Create(context.Background(), req, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(beta, fastModeBeta) || !strings.HasPrefix(beta, "interleaved-thinking") {
		t.Errorf("beta = %q: the per-request header must carry the full list plus the fast-mode beta", beta)
	}
}

func TestNonStreamingCreate(t *testing.T) {
	var gotStream any = "unset"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		gotStream = b["stream"]
		if r.Header.Get("accept") != "application/json" {
			t.Errorf("accept = %q", r.Header.Get("accept"))
		}
		w.Header().Set("content-type", "application/json")
		io.WriteString(w, okJSON)
	}))
	defer srv.Close()
	msg, err := (&Client{BaseURL: srv.URL, Auth: APIKey("k")}).Create(context.Background(), buildReq(t, RequestOptions{}), CallOptions{NoStream: true})
	if err != nil {
		t.Fatal(err)
	}
	if gotStream != nil {
		t.Errorf("stream = %v, must be absent", gotStream)
	}
	if msg.ID != "m2" || msg.StopReason != "end_turn" || msg.Usage.CacheReadTokens != 2 || len(msg.Content) != 1 {
		t.Fatalf("msg = %+v", msg)
	}
	if !ValidResponse(msg) {
		t.Error("response should be valid")
	}
}

func TestEventStreamAnswerToAPlainCreateIsAggregated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream") // an SSE-only gateway
		io.WriteString(w, okStream)
	}))
	defer srv.Close()
	msg, err := (&Client{BaseURL: srv.URL}).Create(context.Background(), buildReq(t, RequestOptions{}), CallOptions{NoStream: true})
	if err != nil || len(msg.Content) != 1 {
		t.Fatalf("msg = %+v, err = %v", msg, err)
	}
}

func TestStreamFallsBackToCreateWhenStreamingIsUnavailable(t *testing.T) {
	var mu sync.Mutex
	var calls []bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		calls = append(calls, b["stream"] == true)
		mu.Unlock()
		if b["stream"] == true {
			w.WriteHeader(400)
			io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"Streaming is not supported for this model"}}`)
			return
		}
		w.Header().Set("content-type", "application/json")
		io.WriteString(w, okJSON)
	}))
	defer srv.Close()
	msg, err := (&Client{BaseURL: srv.URL}).Create(context.Background(), buildReq(t, RequestOptions{}), CallOptions{})
	if err != nil || msg.ID != "m2" {
		t.Fatalf("msg = %+v, err = %v", msg, err)
	}
	if len(calls) != 2 || !calls[0] || calls[1] {
		t.Fatalf("calls (stream flag) = %v, want [true false]", calls)
	}
}

func TestStreamFallsBackOnUnexpectedEventOrder(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		if b["stream"] == true {
			w.Header().Set("content-type", "text/event-stream")
			io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"x\"}}\n\n")
			return
		}
		w.Header().Set("content-type", "application/json")
		io.WriteString(w, okJSON)
	}))
	defer srv.Close()
	if msg, err := (&Client{BaseURL: srv.URL}).Create(context.Background(), buildReq(t, RequestOptions{}), CallOptions{}); err != nil || msg.ID != "m2" || n != 2 {
		t.Fatalf("msg = %+v, err = %v, calls = %d", msg, err, n)
	}
}

func TestOtherStreamFailuresDoNotFallBack(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.Header().Set("content-type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"m\"}}\n\n") // dropped before message_stop
	}))
	defer srv.Close()
	_, err := (&Client{BaseURL: srv.URL}).Create(context.Background(), buildReq(t, RequestOptions{}), CallOptions{})
	if !errors.Is(err, ErrIncompleteStream) || n != 1 {
		t.Fatalf("err = %v, calls = %d: a dropped stream is retryable, not a reason to switch modes", err, n)
	}
}

func TestAPIErrorParsing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("request-id", "req_123")
		w.Header().Set("retry-after", "7")
		w.WriteHeader(429)
		io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`)
	}))
	defer srv.Close()
	_, err := (&Client{BaseURL: srv.URL}).Create(context.Background(), buildReq(t, RequestOptions{}), CallOptions{})
	var ae *APIError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v", err)
	}
	if ae.Status != 429 || ae.Type != "rate_limit_error" || ae.Message != "slow down" || ae.RequestID != "req_123" || ae.RetryAfter != "7" {
		t.Fatalf("ae = %+v", ae)
	}
	if !strings.Contains(ae.Error(), "429") || !strings.Contains(ae.Error(), "slow down") {
		t.Errorf("Error() = %q", ae.Error())
	}
}

func TestAPIErrorWithNonJSONBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(502)
		io.WriteString(w, "<html>Bad Gateway</html>")
	}))
	defer srv.Close()
	_, err := (&Client{BaseURL: srv.URL}).Create(context.Background(), buildReq(t, RequestOptions{}), CallOptions{})
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 502 || ae.Message != "" || !strings.Contains(ae.Body, "Bad Gateway") || !strings.Contains(ae.Error(), "Bad Gateway") {
		t.Fatalf("err = %v / %+v", err, ae)
	}
}

func TestIdleTimeoutAbandonsAStalledStream(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("content-type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"m\"}}\n\n")
		w.(http.Flusher).Flush()
		select { // then go quiet
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	c := &Client{BaseURL: srv.URL, IdleTimeout: 150 * time.Millisecond}
	start := time.Now()
	_, err := c.Create(context.Background(), buildReq(t, RequestOptions{}), CallOptions{})
	if !errors.Is(err, ErrIdleTimeout) {
		t.Fatalf("err = %v, want ErrIdleTimeout", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
}

func TestIdleTimeoutResetsOnEveryByte(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		for _, part := range strings.SplitAfter(okStream, "\n\n") {
			io.WriteString(w, part)
			w.(http.Flusher).Flush()
			time.Sleep(60 * time.Millisecond) // total well over the idle timeout, each gap well under it
		}
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, IdleTimeout: 250 * time.Millisecond}
	if msg, err := c.Create(context.Background(), buildReq(t, RequestOptions{}), CallOptions{}); err != nil || len(msg.Content) != 1 {
		t.Fatalf("msg = %+v, err = %v: a slow but steady stream must not be cut off", msg, err)
	}
}

func TestContextCancelStopsTheCall(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body) // the server only notices a closed connection once the body is read
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-started; cancel() }()
	_, err := (&Client{BaseURL: srv.URL}).Create(ctx, buildReq(t, RequestOptions{}), CallOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestOnEventErrorAbandonsTheStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		io.WriteString(w, okStream)
	}))
	defer srv.Close()
	stop := errors.New("host deadline")
	_, err := (&Client{BaseURL: srv.URL}).Create(context.Background(), buildReq(t, RequestOptions{}), CallOptions{OnEvent: func(StreamEvent) error { return stop }})
	if !errors.Is(err, stop) {
		t.Fatalf("err = %v", err)
	}
}

type failingAuth struct{}

func (failingAuth) Apply(context.Context, http.Header) (bool, error) {
	return false, errors.New("no token")
}

func TestAuthFailureIsReportedBeforeAnyRequest(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer srv.Close()
	_, err := (&Client{BaseURL: srv.URL, Auth: failingAuth{}}).Create(context.Background(), buildReq(t, RequestOptions{}), CallOptions{})
	if err == nil || !strings.Contains(err.Error(), "no token") || hit {
		t.Fatalf("err = %v, request sent = %v", err, hit)
	}
}

func TestPromptTextIsSentUnescaped(t *testing.T) {
	var raw string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		raw = string(b)
		w.Header().Set("content-type", "text/event-stream")
		io.WriteString(w, okStream)
	}))
	defer srv.Close()
	req := buildReq(t, RequestOptions{Messages: userMsg("if a < b && c > d")})
	if _, err := (&Client{BaseURL: srv.URL}).Create(context.Background(), req, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "if a < b && c > d") {
		t.Fatalf("body = %s", raw)
	}
}
