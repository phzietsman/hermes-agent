package anthropic

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"
)

type errCase struct {
	Name    string            `json:"name"`
	Status  int               `json:"status"`
	Body    json.RawMessage   `json:"body"`
	Text    *string           `json:"text"`
	Headers map[string]string `json:"headers"`
	Model   string            `json:"model"`
	Tokens  int               `json:"tokens"`
	Ctx     int               `json:"ctx"`
	Msgs    int               `json:"msgs"`
	Stream  map[string]any    `json:"stream"`
	Want    struct {
		Reason          string   `json:"reason"`
		Status          int      `json:"status_code"`
		Message         string   `json:"message"`
		Retryable       bool     `json:"retryable"`
		Compress        bool     `json:"should_compress"`
		Rotate          bool     `json:"should_rotate_credential"`
		Fallback        bool     `json:"should_fallback"`
		BillingUnverify bool     `json:"billing_unverified"`
		ResetAfter      *float64 `json:"reset_after"`
	} `json:"expected"`
}

func (c errCase) err() error {
	if c.Stream != nil {
		t, _ := c.Stream["type"].(string)
		m, _ := c.Stream["message"].(string)
		return &StreamError{Type: t, Message: m}
	}
	body := string(c.Body)
	if c.Text != nil {
		body = *c.Text
	}
	h := http.Header{}
	for k, v := range c.Headers {
		h.Set(k, v)
	}
	ae := &APIError{Status: c.Status, Body: body, Header: h}
	var parsed struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(body), &parsed) == nil {
		ae.Type, ae.Message = parsed.Error.Type, parsed.Error.Message
	}
	return ae
}

func TestClassifyMatchesPython(t *testing.T) {
	var fx struct {
		Cases []errCase `json:"cases"`
	}
	readJSON(t, "testdata/errors.json", &fx)
	if len(fx.Cases) < 150 {
		t.Fatalf("only %d cases", len(fx.Cases))
	}
	bad := 0
	for _, c := range fx.Cases {
		got := Classify(c.err(), ClassifyOptions{Model: c.Model, ApproxTokens: c.Tokens, ContextLength: c.Ctx, NumMessages: c.Msgs})
		w := c.Want
		ok := string(got.Reason) == w.Reason && got.Status == w.Status && got.Retryable == w.Retryable && got.ShouldCompress == w.Compress &&
			got.ShouldRotateCredential == w.Rotate && got.ShouldFallback == w.Fallback && got.BillingUnverified == w.BillingUnverify &&
			messageMatches(got.Message, w.Message)
		if w.ResetAfter != nil {
			ok = ok && absDiff(got.ResetAfter.Seconds(), *w.ResetAfter) < 1
		} else {
			ok = ok && got.ResetAfter == 0
		}
		if !ok {
			bad++
			t.Errorf("%s:\n  go     %+v\n  python %+v", c.Name, brief(got), w)
		}
	}
	if bad > 0 {
		t.Fatalf("%d of %d cases differ from Python", bad, len(fx.Cases))
	}
}

// messageMatches compares the user-facing message. Python falls back to str(error) when the body has no
// message, and that text embeds a Python dict repr, so there only the "Error code: N" lead is comparable.
func messageMatches(got, want string) bool {
	if strings.HasPrefix(want, "Error code: ") {
		lead, _, _ := strings.Cut(want, " - ")
		return strings.HasPrefix(got, lead)
	}
	return got == want
}

func brief(c ClassifiedError) string {
	return fmt.Sprintf("reason=%s status=%d retry=%v compress=%v rotate=%v fallback=%v unverified=%v reset=%v msg=%q",
		c.Reason, c.Status, c.Retryable, c.ShouldCompress, c.ShouldRotateCredential, c.ShouldFallback, c.BillingUnverified, c.ResetAfter, c.Message)
}

func absDiff(a, b float64) float64 {
	if a > b {
		return a - b
	}
	return b - a
}

func TestRetryHelpersMatchPython(t *testing.T) {
	var fx struct {
		Retry struct {
			RetryAfter []struct {
				In   string   `json:"in"`
				Want *float64 `json:"want"`
			} `json:"retry_after"`
			Messages []struct {
				In   string   `json:"in"`
				Want *float64 `json:"want"`
			} `json:"messages"`
		} `json:"retry"`
	}
	readJSON(t, "testdata/errors.json", &fx)
	for _, c := range fx.Retry.RetryAfter {
		got, ok := parseRetryAfterSeconds(c.In)
		if ok != (c.Want != nil) || (ok && absDiff(got, *c.Want) > 1e-9) {
			t.Errorf("parseRetryAfterSeconds(%q) = %v, %v; python %v", c.In, got, ok, c.Want)
		}
	}
	for _, c := range fx.Retry.Messages {
		got, ok := ResetDelayFromMessage(c.In)
		if ok != (c.Want != nil) || (ok && absDiff(got, *c.Want) > 1e-9) {
			t.Errorf("ResetDelayFromMessage(%q) = %v, %v; python %v", c.In, got, ok, c.Want)
		}
	}
}

func TestParseRetryAfterHTTPDate(t *testing.T) {
	defer func(f func() time.Time) { now = f }(now)
	now = func() time.Time { return time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC) }
	for in, want := range map[string]float64{
		"Sun, 08 Nov 2026 12:00:30 GMT": 30,
		"Sun, 08 Nov 2026 11:00:00 GMT": 0, // already past: clamped at zero
	} {
		if got, ok := parseRetryAfterSeconds(in); !ok || got != want {
			t.Errorf("parseRetryAfterSeconds(%q) = %v, %v; want %v", in, got, ok, want)
		}
	}
	if _, ok := parseRetryAfterSeconds("Someday soon"); ok {
		t.Error("garbage must not parse")
	}
}

func TestProviderRetryAfter(t *testing.T) {
	hdr := func(k, v string) http.Header { h := http.Header{}; h.Set(k, v); return h }
	tests := []struct {
		name string
		err  error
		want time.Duration
		ok   bool
	}{
		{"header", &APIError{Header: hdr("Retry-After", "17")}, 17 * time.Second, true},
		{"header wins over body", &APIError{Header: hdr("Retry-After", "5"), Body: `{"retry_after": 99}`}, 5 * time.Second, true},
		{"body top level", &APIError{Body: `{"retry_after": 12}`}, 12 * time.Second, true},
		{"body under error", &APIError{Body: `{"error": {"retry_after": "9"}}`}, 9 * time.Second, true},
		{"zero is no usable wait", &APIError{Header: hdr("Retry-After", "0")}, 0, false},
		{"negative clamps to zero: no wait", &APIError{Header: hdr("Retry-After", "-3")}, 0, false},
		{"absent", &APIError{}, 0, false},
		{"not an API error", errors.New("x"), 0, false},
		{"wrapped", fmt.Errorf("call: %w", &APIError{Header: hdr("Retry-After", "4")}), 4 * time.Second, true},
		{"inf is not a wait", &APIError{Header: hdr("Retry-After", "inf")}, 0, false},
	}
	for _, tc := range tests {
		got, ok := ProviderRetryAfter(tc.err)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: = %v, %v; want %v, %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestBackoffDelay(t *testing.T) {
	zero := func() float64 { return 0 }
	one := func() float64 { return 0.9999999 }
	b := Backoff{Base: 2 * time.Second, Max: 60 * time.Second, JitterRatio: 0.5, Rand: zero}
	for attempt, want := range map[int]time.Duration{1: 2 * time.Second, 2: 4 * time.Second, 3: 8 * time.Second, 5: 32 * time.Second, 6: 60 * time.Second, 40: 60 * time.Second, 0: 2 * time.Second, -3: 2 * time.Second, 100: 60 * time.Second} {
		if got := b.Delay(attempt); got != want {
			t.Errorf("Delay(%d) = %v, want %v", attempt, got, want)
		}
	}
	b.Rand = one
	if got := b.Delay(1); got < 2*time.Second || got > 3*time.Second {
		t.Errorf("max jitter delay = %v, want within [2s, 3s]", got)
	}
	if got := (Backoff{Base: 0, Max: 30 * time.Second, Rand: zero}).Delay(1); got != 30*time.Second {
		t.Errorf("zero base uses the max: %v", got)
	}
	for i := 0; i < 200; i++ { // real randomness stays inside its bounds
		if got := DefaultBackoff.Delay(3); got < 8*time.Second || got > 12*time.Second {
			t.Fatalf("Delay(3) = %v, want within [8s, 12s]", got)
		}
	}
}

func TestRetryWait(t *testing.T) {
	zero := Backoff{Base: 2 * time.Second, Max: 60 * time.Second, Rand: func() float64 { return 0 }}
	h := func(v string) *APIError { x := http.Header{}; x.Set("Retry-After", v); return &APIError{Header: x} }
	if w, a := RetryWait(h("17"), 1, zero); w != 17*time.Second || a {
		t.Errorf("short Retry-After: %v %v", w, a)
	}
	if w, a := RetryWait(h("90"), 1, zero); w != 90*time.Second || !a {
		t.Errorf("a long wait is announced: %v %v", w, a)
	}
	if w, a := RetryWait(h("100000"), 1, zero); w != RetryAfterCap || !a {
		t.Errorf("a huge Retry-After is capped: %v %v", w, a)
	}
	if w, a := RetryWait(errors.New("boom"), 3, zero); w != 8*time.Second || a {
		t.Errorf("no hint: jittered backoff, got %v %v", w, a)
	}
}

func TestClassifyGoTransportErrors(t *testing.T) {
	dnsErr := &net.DNSError{Err: "no such host", Name: "api.anthropic.com", IsNotFound: true}
	opErr := &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	timeoutErr := &url.Error{Op: "Post", URL: "https://x", Err: context.DeadlineExceeded}
	tests := []struct {
		name   string
		err    error
		reason Reason
		retry  bool
		opts   ClassifyOptions
	}{
		{"connection refused", opErr, ReasonTimeout, true, ClassifyOptions{}},
		{"dns", fmt.Errorf("post: %w", dnsErr), ReasonTimeout, true, ClassifyOptions{}},
		{"deadline", timeoutErr, ReasonTimeout, true, ClassifyOptions{}},
		{"bare deadline", context.DeadlineExceeded, ReasonTimeout, true, ClassifyOptions{}},
		{"idle timeout", ErrIdleTimeout, ReasonTimeout, true, ClassifyOptions{}},
		{"connection reset", &net.OpError{Op: "read", Err: syscall.ECONNRESET}, ReasonTimeout, true, ClassifyOptions{}},
		{"unknown authority", x509.UnknownAuthorityError{}, ReasonSSLCert, false, ClassifyOptions{}},
		{"cert invalid", x509.CertificateInvalidError{Reason: x509.Expired}, ReasonSSLCert, false, ClassifyOptions{}},
		{"hostname", x509.HostnameError{Certificate: &x509.Certificate{}, Host: "x"}, ReasonSSLCert, false, ClassifyOptions{}},
		{"tls verify wrapper", &tls.CertificateVerificationError{Err: errors.New("bad")}, ReasonSSLCert, false, ClassifyOptions{}},
		{"tls record header", tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}, ReasonTimeout, true, ClassifyOptions{}},
		{"unexpected eof, small session", io.ErrUnexpectedEOF, ReasonTimeout, true, ClassifyOptions{Model: "claude-haiku-4-5", ApproxTokens: 1000}},
		{"unexpected eof, large session", io.ErrUnexpectedEOF, ReasonContextOverflow, true, ClassifyOptions{Model: "claude-haiku-4-5", ApproxTokens: 150000}},
		{"unexpected eof, reasoning model", io.ErrUnexpectedEOF, ReasonTimeout, true, ClassifyOptions{Model: "claude-opus-4-5", ApproxTokens: 150000}},
		{"incomplete stream is unknown", ErrIncompleteStream, ReasonUnknown, true, ClassifyOptions{}},
		{"malformed tool input is unknown", &ErrMalformedToolInput{Tool: "x", Err: errors.New("bad json")}, ReasonUnknown, true, ClassifyOptions{}},
		{"plain", errors.New("something odd"), ReasonUnknown, true, ClassifyOptions{}},
		{"wording only", errors.New("dial tcp: lookup api.anthropic.com: no such host"), ReasonTimeout, true, ClassifyOptions{}},
		{"x509 wording", errors.New("tls: failed to verify certificate: x509: certificate signed by unknown authority"), ReasonSSLCert, false, ClassifyOptions{}},
	}
	for _, tc := range tests {
		got := Classify(tc.err, tc.opts)
		if got.Reason != tc.reason || got.Retryable != tc.retry {
			t.Errorf("%s: reason = %s retry = %v; want %s %v", tc.name, got.Reason, got.Retryable, tc.reason, tc.retry)
		}
	}
}

func TestClassifiedErrorImplementsError(t *testing.T) {
	inner := &APIError{Status: 429, Body: `{"error":{"type":"rate_limit_error","message":"slow"}}`}
	ce := Classify(inner, ClassifyOptions{})
	var asClassified ClassifiedError
	if !errors.As(error(ce), &asClassified) || !errors.Is(ce, inner) {
		t.Fatalf("the classified error must wrap its cause: %v", ce)
	}
	if !strings.Contains(ce.Error(), "rate_limit") || !strings.Contains(ce.Error(), "slow") {
		t.Errorf("Error() = %q", ce.Error())
	}
	if !(ClassifiedError{Reason: ReasonAuth}).IsAuth() || (ClassifiedError{Reason: ReasonBilling}).IsAuth() {
		t.Error("IsAuth")
	}
}

func TestReasoningStaleTimeoutFloor(t *testing.T) {
	tests := map[string]time.Duration{
		"claude-fable-5-1": 600 * time.Second, "claude-opus-4-5": 240 * time.Second, "claude-opus-5-5": 240 * time.Second,
		"claude-sonnet-5-5": 180 * time.Second, "claude-sonnet-4.5": 180 * time.Second, "anthropic/claude-sonnet-4.6": 180 * time.Second,
		"claude-opus-4-5:fast": 240 * time.Second, "CLAUDE-OPUS-4-5": 240 * time.Second,
	}
	for m, want := range tests {
		if got, ok := reasoningStaleTimeoutFloor(m); !ok || got != want {
			t.Errorf("floor(%q) = %v, %v; want %v", m, got, ok, want)
		}
	}
	for _, m := range []string{"claude-haiku-4-5", "claude-3-5-sonnet", "claude-sonnet-4-5", "my-claude-opus-4", "", "gpt-5"} {
		if got, ok := reasoningStaleTimeoutFloor(m); ok {
			t.Errorf("floor(%q) = %v, want none", m, got)
		}
	}
}

func TestReasoningRejectionPredicates(t *testing.T) {
	tests := []struct {
		msg                      string
		fieldRejection, required bool
	}{
		{"Unrecognized request argument supplied: reasoning_effort", true, false},
		{"reasoning_effort 'none' unsupported; use minimal|low", true, false},
		{`{'param': 'reasoning.effort'}`, true, false},
		{"invalid_reasoning_effort", true, false},
		{"Reasoning is mandatory for this endpoint and cannot be disabled", false, true},
		{"thinking cannot be disabled on this model", false, true},
		{"the model kimi-k2-thinking is not supported when using this account", false, false}, // a model id, not the field
		{"not supported with reasoning models", false, false},
		{"the thinking param is unsupported", true, false},
		{"hello world", false, false},
		{"", false, false},
	}
	for _, tc := range tests {
		if got := IsReasoningFieldRejection(tc.msg); got != tc.fieldRejection {
			t.Errorf("IsReasoningFieldRejection(%q) = %v, want %v", tc.msg, got, tc.fieldRejection)
		}
		if got := IsReasoningRequiredRejection(tc.msg); got != tc.required {
			t.Errorf("IsReasoningRequiredRejection(%q) = %v, want %v", tc.msg, got, tc.required)
		}
	}
}

func FuzzClassifyNeverPanics(f *testing.F) {
	f.Add(400, `{"error":{"type":"invalid_request_error","message":"prompt is too long"}}`, "retry-after", "5")
	f.Add(429, `{"error":{"retry_after":"x","message":"resets in 4hr"}}`, "", "")
	f.Add(200, `not json at all`, "retry-after", "Sun, 08 Nov 2026 12:00:30 GMT")
	f.Add(0, ``, "", "")
	f.Add(599, `{"error":"string not object","code":400}`, "", "")
	f.Add(403, `{"error":{"message":"{\"code\": \"x\"}","code":false}}`, "", "")
	f.Fuzz(func(t *testing.T, status int, body, hk, hv string) {
		h := http.Header{}
		if hk != "" {
			h.Set(hk, hv)
		}
		ce := Classify(&APIError{Status: status, Body: body, Header: h}, ClassifyOptions{Model: "claude-opus-4-5", ApproxTokens: 150000, NumMessages: 100})
		if ce.Reason == "" {
			t.Fatal("every error gets a reason")
		}
		if ce.Retryable && ce.ShouldFallback && !ce.ShouldRotateCredential && ce.Reason == ReasonAuth {
			t.Fatalf("inconsistent hints: %+v", ce)
		}
		_, _ = RetryWait(&APIError{Status: status, Body: body, Header: h}, 3, DefaultBackoff)
	})
}
