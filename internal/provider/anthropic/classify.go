package anthropic

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Reason is why an API call failed, and so which recovery applies. The set is the part of the Python
// FailoverReason enum that a native Anthropic call can produce.
type Reason string

const (
	ReasonAuth                         Reason = "auth"                  // 401/403: refresh or rotate the credential
	ReasonBilling                      Reason = "billing"               // 402 or confirmed credit exhaustion
	ReasonRateLimit                    Reason = "rate_limit"            // 429 or quota-based throttling: back off, then rotate
	ReasonUpstreamBlocked              Reason = "upstream_blocked"      // 403 from a WAF/CDN/proxy in front of the API: the key is healthy
	ReasonOverloaded                   Reason = "overloaded"            // 503/529: back off
	ReasonServerError                  Reason = "server_error"          // 500/502: retry
	ReasonTimeout                      Reason = "timeout"               // connection or read timeout
	ReasonSSLCert                      Reason = "ssl_cert_verification" // deterministic TLS chain failure: fail fast
	ReasonContextOverflow              Reason = "context_overflow"      // compress, do not fail over
	ReasonPayloadTooLarge              Reason = "payload_too_large"     // 413: compress the payload
	ReasonImageTooLarge                Reason = "image_too_large"       // shrink and retry
	ReasonImageCorrupt                 Reason = "image_corrupt"         // strip and retry
	ReasonModelNotFound                Reason = "model_not_found"       // 404 or invalid model
	ReasonProviderPolicyBlocked        Reason = "provider_policy_blocked"
	ReasonContentPolicyBlocked         Reason = "content_policy_blocked" // safety filter rejected this prompt: deterministic per request
	ReasonFormatError                  Reason = "format_error"           // 400 bad request
	ReasonRoleAlternation              Reason = "role_alternation"
	ReasonMultimodalToolContent        Reason = "multimodal_tool_content_unsupported"
	ReasonReasoningMandatory           Reason = "reasoning_mandatory" // the route rejects thinking disabled
	ReasonThinkingSignature            Reason = "thinking_signature"  // an Anthropic thinking block signature is invalid
	ReasonLongContextTier              Reason = "long_context_tier"   // the "extra usage" long-context tier gate
	ReasonOAuthLongContextBetaRejected Reason = "oauth_long_context_beta_forbidden"
	ReasonUnknown                      Reason = "unknown" // retry with backoff
)

// ClassifiedError is the structured recovery recommendation for an API error. The loop checks the hints
// instead of re-classifying.
type ClassifiedError struct {
	Reason  Reason
	Status  int // 0 when none
	Message string
	// Retryable: retry the same request (with backoff).
	Retryable bool
	// ShouldCompress: shrink the context or payload and retry.
	ShouldCompress bool
	// ShouldRotateCredential: this credential is the problem.
	ShouldRotateCredential bool
	// ShouldFallback: another model or provider may succeed.
	ShouldFallback bool
	// BillingUnverified marks a billing verdict that rests on an ambiguous body: Anthropic returns the same
	// "out of extra usage" body for a content-filter rejection.
	BillingUnverified bool
	// ResetAfter is the provider-declared time until a rate limit reopens, 0 when none was named.
	ResetAfter time.Duration
	Err        error
}

func (c ClassifiedError) Error() string { return string(c.Reason) + ": " + c.Message }
func (c ClassifiedError) Unwrap() error { return c.Err }

// IsAuth is true for auth failures.
func (c ClassifiedError) IsAuth() bool { return c.Reason == ReasonAuth }

// RetryableClientReasons are the reasons the loop keeps retrying with backoff even though the verdict may
// also carry ShouldFallback: the cascade for these runs after the backoff budget, not immediately.
var RetryableClientReasons = map[Reason]bool{
	ReasonRateLimit: true, ReasonOverloaded: true, ReasonContextOverflow: true, ReasonPayloadTooLarge: true,
	ReasonLongContextTier: true, ReasonThinkingSignature: true,
}

// ClassifyOptions describes the failed call.
type ClassifyOptions struct {
	Model string
	// ApproxTokens, ContextLength and NumMessages size the session. A vague 400 or a mid-stream
	// disconnect on a large session is read as a probable context overflow. ContextLength defaults to 200000.
	ApproxTokens  int
	ContextLength int
	NumMessages   int
}

type verdict struct {
	reason                 Reason
	retryable              bool
	compress, rotate, fall bool
	billingUnverified      bool
	resetAfter             time.Duration
}

func vd(r Reason) verdict { return verdict{reason: r, retryable: true} }

func (v verdict) abortFallback() verdict  { v.retryable, v.fall = false, true; return v }
func (v verdict) rotateFallback() verdict { v.rotate, v.fall = true, true; return v }
func (v verdict) noRetry() verdict        { v.retryable = false; return v }
func (v verdict) withCompress() verdict   { v.compress = true; return v }

var (
	vBilling            = vd(ReasonBilling).noRetry().rotateFallback()
	vRateLimit          = vd(ReasonRateLimit).rotateFallback()
	vAuthRotate         = vd(ReasonAuth).noRetry().rotateFallback()
	vAuthFallback       = vd(ReasonAuth).abortFallback()
	vModelNotFound      = vd(ReasonModelNotFound).abortFallback()
	vUpstreamBlocked    = vd(ReasonUpstreamBlocked).abortFallback()
	vContentBlocked     = vd(ReasonContentPolicyBlocked).abortFallback()
	vFormatError        = vd(ReasonFormatError).abortFallback()
	vPolicyBlocked      = vd(ReasonProviderPolicyBlocked).abortFallback()
	vSSLCert            = vd(ReasonSSLCert).abortFallback()
	vContextOverflow    = vd(ReasonContextOverflow).withCompress()
	vPayloadTooLarge    = vd(ReasonPayloadTooLarge).withCompress()
	vOverloaded         = vd(ReasonOverloaded)
	vServerError        = vd(ReasonServerError)
	vTimeout            = vd(ReasonTimeout)
	vUnknown            = vd(ReasonUnknown)
	vImageTooLarge      = vd(ReasonImageTooLarge)
	vImageCorrupt       = vd(ReasonImageCorrupt)
	vMultimodal         = vd(ReasonMultimodalToolContent)
	vReasoningMandatory = vd(ReasonReasoningMandatory)
	vRoleAlternation    = vd(ReasonRoleAlternation).abortFallback()
	// The MODEL emitted unparseable tool-call JSON and a proxy rejected it: no other provider can fix that
	// output, so falling back only replays the same broken turn. Abort this call; argument repair handles the retry.
	vMalformedToolArgs = vd(ReasonFormatError).noRetry()
)

type rule struct {
	patterns []string
	verdict  func(msg string) verdict
}

func fixed(v verdict) func(string) verdict { return func(string) verdict { return v } }

func billingHints(msg string) verdict {
	v := vBilling
	if containsAny(msg, unverifiedBillingPatterns) {
		v.billingUnverified = true
	}
	return v
}

func containsAny(s string, patterns []string) bool {
	for _, p := range patterns {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

func firstMatch(msg string, rules []rule) (verdict, bool) {
	for _, r := range rules {
		if containsAny(msg, r.patterns) {
			return r.verdict(msg), true
		}
	}
	return verdict{}, false
}

var (
	imageToolRules = []rule{
		{multimodalToolContentPatterns, fixed(vMultimodal)}, {imageCorruptPatterns, fixed(vImageCorrupt)},
		{imageTooLargePatterns, fixed(vImageTooLarge)},
	}
	overflowAs5xxRules = []rule{
		{emptyProviderResponsePatterns, fixed(vServerError)}, {memoryCeilingPatterns, fixed(vOverloaded)},
		{contextOverflowPatterns, fixed(vContextOverflow)},
	}
	notFoundRules = []rule{
		{billingPatterns, fixed(vBilling)}, {providerPolicyBlockedPatterns, fixed(vPolicyBlocked)},
		{modelNotFoundPatterns, fixed(vModelNotFound)},
	}
	// 400 tail after the deterministic request-shape checks: some providers return model-not-found,
	// rate-limit or billing as 400 instead of 404/429/402.
	badRequestTailRules = append(append([]rule{}, overflowAs5xxRules...),
		rule{providerPolicyBlockedPatterns, fixed(vPolicyBlocked)}, rule{modelNotFoundPatterns, fixed(vModelNotFound)},
		rule{rateLimitPatterns, fixed(vRateLimit)}, rule{billingPatterns, billingHints})
	// Status-less message path, head (before usage-limit disambiguation).
	messageHeadRules = append([]rule{
		{memoryCeilingPatterns, fixed(vOverloaded)}, {payloadTooLargePatterns, fixed(vPayloadTooLarge)},
		{roleAlternationPatterns, fixed(vRoleAlternation)}, {streamRenderErrorPatterns, fixed(vFormatError)},
	}, imageToolRules...)
	// Status-less tail. Overload before rate_limit and billing so "overloaded" backs off instead of
	// rotating; timeout and connection wording last, classified as transport (never compression).
	messageTailRules = []rule{
		{overloadedPatterns, fixed(vOverloaded)}, {billingPatterns, billingHints},
		{rateLimitPatterns, fixed(vRateLimit)}, {emptyProviderResponsePatterns, fixed(vServerError)},
		{contextOverflowPatterns, fixed(vContextOverflow)}, {authPatterns, fixed(vAuthRotate)},
		{providerPolicyBlockedPatterns, fixed(vPolicyBlocked)}, {modelNotFoundPatterns, fixed(vModelNotFound)},
		{timeoutMessagePatterns, fixed(vTimeout)}, {connectionMessagePatterns, fixed(vTimeout)},
	}
	// errorCodeVerdicts: structured error code to verdict. The error-code rate_limit verdict rotates but
	// does not set fallback, unlike the message and status paths.
	errorCodeVerdicts = func() map[string]verdict {
		m := map[string]verdict{}
		rl := vd(ReasonRateLimit)
		rl.rotate = true
		for _, c := range []string{"resource_exhausted", "throttled", "rate_limit_exceeded"} {
			m[c] = rl
		}
		for c := range billingErrorCodes {
			m[c] = vBilling
		}
		for _, c := range []string{"model_not_found", "model_not_available", "invalid_model"} {
			m[c] = vModelNotFound
		}
		for _, c := range []string{"context_length_exceeded", "max_tokens_exceeded"} {
			m[c] = vContextOverflow
		}
		for c := range memoryCeilingErrorCodes {
			m[c] = vOverloaded
		}
		return m
	}()
	// anthropicCodeVerdicts: Anthropic error types that arrive as a bare {"error": {"type": ...}} body with
	// no HTTP status and no prose.
	anthropicCodeVerdicts = map[string]verdict{"api_error": vServerError, "rate_limit_error": vRateLimit}

	badRequestValidationPatterns = func() []string {
		var out []string
		for _, p := range requestValidationPatterns {
			if p != "invalid_request_error" { // generic and stamped on genuine overflow 400s too
				out = append(out, p)
			}
		}
		return out
	}()
	badRequestValidationCodes = setOf("unknown_parameter", "unsupported_parameter")
	validation5xxCodes        = setOf("unknown_parameter", "unsupported_parameter", "invalid_request_error")
)

// ctx is everything the classifier stages need about one failed call.
type ctx struct {
	status                      int
	body                        map[string]any
	msg                         string // lowercased error text plus the body message
	code                        string // lowercased structured error code
	headers                     http.Header
	model                       string
	approxTokens, contextLength int
	numMessages                 int
	err                         error
}

func (c *ctx) largeSession(frac float64, tokens, messages int) bool {
	return float64(c.approxTokens) > float64(c.contextLength)*frac ||
		(c.contextLength <= 256000 && (c.approxTokens > tokens || c.numMessages > messages))
}

// Classify turns an API error into a recovery recommendation. Stages, in order: provider-specific shapes
// a status code would misroute, the HTTP status with message-aware refinement, the structured error
// code, message patterns, then transport (SSL, disconnects, timeouts); anything else is unknown, which
// the loop retries with backoff.
func Classify(err error, o ClassifyOptions) ClassifiedError {
	if o.ContextLength == 0 {
		o.ContextLength = 200000
	}
	c := &ctx{model: o.Model, approxTokens: o.ApproxTokens, contextLength: o.ContextLength, numMessages: o.NumMessages, err: err}
	raw := ""
	var ae *APIError
	var se *StreamError
	switch {
	case errors.As(err, &ae):
		c.status, c.body = ae.Status, parseBody(ae.Body)
		c.headers = ae.Header
		raw = sdkText(ae, c.body)
	case errors.As(err, &se):
		// A stream error event carries no HTTP status of its own: the SDK reports it as the 200 response
		// it arrived on, which no status handler claims, so the structured code and the message decide.
		// The SDK's text for it is the error object, so the type word (authentication_error) takes part in
		// message matching, while the structured code comes from the nested error body.
		c.status = 200
		c.body = map[string]any{"error": map[string]any{"type": se.Type, "message": se.Message}}
		raw = "{'message': '" + se.Message + "'}"
		if se.Type != "" {
			raw = "{'type': '" + se.Type + "', 'message': '" + se.Message + "'}"
		}
	default:
		raw = err.Error()
	}
	c.msg = buildErrorMsg(strings.ToLower(raw), c.body)
	c.code = strings.ToLower(extractErrorCode(c.body))

	v := vUnknown
	for _, stage := range []func(*ctx) (verdict, bool){providerSpecial, byStatus, byErrorCode, byMessage, byTransport} {
		if got, ok := stage(c); ok {
			v = got
			break
		}
	}
	return ClassifiedError{
		Reason: v.reason, Status: c.status, Message: extractMessage(err, c.body),
		Retryable: v.retryable, ShouldCompress: v.compress, ShouldRotateCredential: v.rotate, ShouldFallback: v.fall,
		BillingUnverified: v.billingUnverified, ResetAfter: v.resetAfter, Err: err,
	}
}

// ---- stages -----------------------------------------------------------------------------------

func providerSpecial(c *ctx) (verdict, bool) {
	msg, status := c.msg, c.status
	// A safety refusal comes before status classification so a 400 block is not downgraded to
	// format_error and a status-less block is not left retryable.
	if containsAny(msg, contentPolicyBlockedPatterns) {
		return vContentBlocked, true
	}
	// Status-agnostic: the stream-relayed ban has no status, and a 403 variant is not a bad key.
	if containsAny(msg, accountPolicyBlockPatterns) {
		return vPolicyBlocked, true
	}
	// Anthropic thinking-block 400s: a signature mismatch after the transcript was mutated.
	if status == 400 && strings.Contains(msg, "thinking") && containsAny(msg, thinkingMutationWords) {
		return vd(ReasonThinkingSignature), true
	}
	// The long-context tier gate: 429 "extra usage" with "long context".
	if status == 429 && strings.Contains(msg, "extra usage") && strings.Contains(msg, "long context") {
		return vd(ReasonLongContextTier).withCompress(), true
	}
	// OAuth rejects the 1M beta header; the caller retries without it.
	if status == 400 && strings.Contains(msg, "long context beta") && strings.Contains(msg, "not yet available") {
		return vd(ReasonOAuthLongContextBetaRejected), true
	}
	return verdict{}, false
}

func byStatus(c *ctx) (verdict, bool) {
	s := c.status
	if s == 0 {
		return verdict{}, false
	}
	switch s {
	case 400:
		return classify400(c), true
	case 401:
		return vAuthRotate, true
	case 402:
		return classify402(c.msg), true
	case 403:
		return status403(c), true
	case 404:
		return status404(c), true
	case 408:
		return vTimeout, true
	case 413:
		return vPayloadTooLarge, true
	case 422:
		if v, ok := firstMatch(c.msg, imageToolRules); ok {
			return v, true
		}
		return vFormatError, true
	case 429:
		return status429(c), true
	case 500, 502:
		return status5xx(c), true
	case 503, 529:
		if v, ok := firstMatch(c.msg, overflowAs5xxRules); ok {
			return v, true
		}
		return vOverloaded, true
	}
	switch {
	case s >= 400 && s < 500:
		return vFormatError, true
	case s >= 500 && s < 600:
		return vServerError, true
	}
	return verdict{}, false
}

// status403: structured codes some gateways put on a 403 mean "the upstream is down, retry later", not a
// credential refusal.
func status403(c *ctx) verdict {
	if c.code == "upstream_unavailable" {
		return vOverloaded
	}
	if containsAny(c.msg, append([]string{"key limit exceeded", "spending limit"}, billingPatterns...)) {
		return vBilling
	}
	// A WAF or CDN in front of the provider answered, not the provider: the credential never reached it, so
	// key guidance and credential rotation are wrong. Gated on 403 and established block markers; any other
	// 403 stays auth.
	if containsAny(c.msg, upstreamBlockedPatterns) {
		return vUpstreamBlocked
	}
	return vAuthFallback
}

func status404(c *ctx) verdict {
	if billingErrorCodes[c.code] {
		return vBilling
	}
	if v, ok := firstMatch(c.msg, notFoundRules); ok {
		return v
	}
	// A generic 404 (wrong path, proxy glitch) stays unknown so the real error surfaces instead of a
	// silently misreported fallback.
	return vUnknown
}

func status429(c *ctx) verdict {
	// A structured billing code is decisive: a hard cap, not throttling.
	if billingErrorCodes[c.code] {
		return vBilling
	}
	// Some providers reuse 429 for server-wide overload: back off on the same key instead of burning it.
	if containsAny(c.msg, overloadedPatterns) {
		return vOverloaded
	}
	// Quota walls as 429 (usage_limit_reached, "quota", billing phrases) are billing ONLY when the body is
	// not itself a rate-limit phrase ("Rate limit exceeded" contains "limit exceeded") and carries no
	// reset or retry signal.
	quotaWall := c.code == "usage_limit_reached" || containsAny(c.msg, append(append([]string{"usage_limit_reached"}, usageLimitPatterns...), billingPatterns...))
	explicitRateLimit := containsAny(c.msg, rateLimitPatterns)
	if quotaWall && !explicitRateLimit && !hasUsageLimitTransientSignal(c.msg, c.body, c.headers) {
		return vBilling
	}
	// Carry the reset window so the terminal copy can name it instead of "wait a minute".
	if reset, ok := rateLimitResetSeconds(c.msg, c.body, c.headers); ok && reset != 0 {
		v := vRateLimit
		v.resetAfter = time.Duration(reset * float64(time.Second))
		return v
	}
	return vRateLimit
}

func status5xx(c *ctx) verdict {
	// Request-validation errors as 5xx fail fast instead of retry-flooding. (The Python carve-out for
	// parameters a gateway injected itself does not apply to the native endpoint.)
	if containsAny(c.msg, requestValidationPatterns) || validation5xxCodes[c.code] {
		if !isServerInjectedParamRejection(c.msg) {
			return vFormatError
		}
	}
	if v, ok := firstMatch(c.msg, overflowAs5xxRules); ok {
		return v
	}
	return vServerError
}

// classify402 disambiguates 402: "usage limit, try again in 5 minutes" is a periodic quota, not billing.
func classify402(msg string) verdict {
	if containsAny(msg, usageLimitPatterns) && containsAny(msg, usageLimitTransientSignals) {
		return vRateLimit
	}
	return vBilling
}

func classify400(c *ctx) verdict {
	msg, code := c.msg, c.code
	if v, ok := firstMatch(msg, imageToolRules); ok {
		return v
	}
	// A route rejecting a reasoning disable, deterministic for the request shape, where the only bad field
	// is the disable. Must precede request validation, which would abort as format_error.
	if strings.Contains(msg, "reasoning is mandatory") || IsReasoningFieldRejection(msg) {
		return vReasoningMandatory
	}
	// A 400 blaming a field this route never sent is transient: retry the identical request.
	if isServerInjectedParamRejection(msg) {
		return vServerError
	}
	if containsAny(msg, malformedToolArgsPatterns) {
		return vMalformedToolArgs
	}
	if containsAny(msg, roleAlternationPatterns) {
		return vRoleAlternation
	}
	// Before overflow: "Unsupported parameter: 'max_tokens'" contains it.
	if containsAny(msg, badRequestValidationPatterns) || badRequestValidationCodes[code] {
		return vFormatError
	}
	// A malformed message array comes before overflow: the input can be tiny and compression cannot fix it.
	if containsAny(msg, invalidMessageBodyPatterns) || code == "invalid_request_body" {
		return vFormatError
	}
	if memoryCeilingErrorCodes[code] {
		return vOverloaded
	}
	if v, ok := firstMatch(msg, badRequestTailRules); ok {
		return v
	}
	// A generic 400 on a large session is a probable overflow (Anthropic can return a bare "Error");
	// proxy shapes are read so a descriptive rejection is not "bare".
	bodyMsg := ""
	for _, cand := range bodyMessageCandidates(c.body) {
		if s, _ := cand.(string); strings.TrimSpace(s) != "" {
			bodyMsg = strings.ToLower(strings.TrimSpace(s))
			break
		}
	}
	if (len([]rune(bodyMsg)) < 30 || bodyMsg == "error") && c.largeSession(0.4, 80000, 80) {
		return vContextOverflow
	}
	return vFormatError
}

func byErrorCode(c *ctx) (verdict, bool) {
	if v, ok := errorCodeVerdicts[c.code]; ok {
		return v, true
	}
	v, ok := anthropicCodeVerdicts[c.code]
	return v, ok
}

func byMessage(c *ctx) (verdict, bool) {
	if v, ok := firstMatch(c.msg, messageHeadRules); ok {
		return v, true
	}
	if containsAny(c.msg, usageLimitPatterns) {
		return classify402(c.msg), true
	}
	return firstMatch(c.msg, messageTailRules)
}

// byTransport applies SSL, disconnect and transport-type heuristics, in that order.
func byTransport(c *ctx) (verdict, bool) {
	msg := c.msg
	// A certificate failure fails fast (checked first: it also contains "[ssl:"); a transient alert retries,
	// before disconnects so a flaky handshake never compresses.
	if containsAny(msg, sslCertVerifyPatterns) || isCertError(c.err) {
		return vSSLCert, true
	}
	if containsAny(msg, sslTransientPatterns) || isTLSAlert(c.err) {
		return vTimeout, true
	}
	// A disconnect on a large session is a probable overflow rejection, not a hiccup. A reasoning model
	// is far more likely to have had a gateway idle-kill a long thinking stream, so it is a timeout and
	// never compresses on a phantom overflow.
	if (containsAny(msg, serverDisconnectPatterns) || errors.Is(c.err, io.ErrUnexpectedEOF)) && c.status == 0 {
		if _, ok := reasoningStaleTimeoutFloor(c.model); ok {
			return vTimeout, true
		}
		if c.largeSession(0.6, 120000, 200) {
			return vContextOverflow, true
		}
		return vTimeout, true
	}
	if isTransportError(c.err) {
		return vTimeout, true
	}
	return verdict{}, false
}

// sdkText is the text the Python SDK gives a status error, which message matching reads: the response
// text for a body that is not JSON, "Error code: N - {body}" for a JSON object, "Error code: N" for no body.
func sdkText(ae *APIError, body map[string]any) string {
	switch {
	case body != nil:
		return "Error code: " + strconv.Itoa(ae.Status) + " - " + ae.Body
	case strings.TrimSpace(ae.Body) != "":
		return ae.Body
	}
	return "Error code: " + strconv.Itoa(ae.Status)
}

// ---- Go transport errors ------------------------------------------------------------------------

func isCertError(err error) bool {
	var (
		unknown  x509.UnknownAuthorityError
		invalid  x509.CertificateInvalidError
		hostname x509.HostnameError
		verify   *tls.CertificateVerificationError
	)
	return errors.As(err, &unknown) || errors.As(err, &invalid) || errors.As(err, &hostname) || errors.As(err, &verify)
}

func isTLSAlert(err error) bool {
	var rec tls.RecordHeaderError
	var op *net.OpError
	return errors.As(err, &rec) || (errors.As(err, &op) && op.Op == "remote error")
}

// isTransportError is true for connection, DNS and timeout failures, and a context deadline.
func isTransportError(err error) bool {
	var (
		netErr net.Error
		dns    *net.DNSError
		op     *net.OpError
		uerr   *url.Error
	)
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, ErrIdleTimeout),
		errors.Is(err, syscall.ECONNREFUSED), errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE),
		errors.Is(err, io.ErrUnexpectedEOF):
		return true
	case errors.As(err, &netErr), errors.As(err, &dns), errors.As(err, &op):
		return true
	case errors.As(err, &uerr):
		return uerr.Timeout()
	}
	return false
}

// ---- extraction helpers --------------------------------------------------------------------------

func parseBody(raw string) map[string]any {
	var m map[string]any
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&m) != nil {
		return nil
	}
	return m
}

func errorObj(body map[string]any) map[string]any {
	m, _ := body["error"].(map[string]any)
	return m
}

// bodyMessageCandidates are the body message fields in priority order: Anthropic and OpenAI, flat,
// litellm/Bedrock proxy, FastAPI shapes.
func bodyMessageCandidates(body map[string]any) []any {
	out := []any{errorObj(body)["message"], body["message"], body["errorMessage"]}
	if args, ok := body["errorArgs"].(map[string]any); ok {
		out = append(out, args["reason"])
	} else {
		out = append(out, nil)
	}
	switch d := body["detail"].(type) {
	case map[string]any:
		out = append(out, d["message"])
	case string:
		out = append(out, d)
	default:
		out = append(out, nil)
	}
	return out
}

// buildErrorMsg is the lowercased error text plus the body message when the text lacks it.
func buildErrorMsg(raw string, body map[string]any) string {
	if body == nil {
		return raw
	}
	bodyMsg := strings.ToLower(asString(errorObj(body)["message"]))
	if bodyMsg == "" {
		bodyMsg = strings.ToLower(asString(body["message"]))
	}
	if bodyMsg != "" && !strings.Contains(raw, bodyMsg) {
		return raw + " " + bodyMsg
	}
	return raw
}

func asString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case json.Number:
		return t.String()
	case bool:
		if t {
			return "True"
		}
		return "False"
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	}
	return ""
}

func jsonDict(text any) map[string]any {
	s, ok := text.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return nil
	}
	return parseBody(s)
}

// codeFromPayload reads the code or type from payload.error or a top-level key; "400" is not a code.
// peekMessage also parses a JSON error.message for a nested code. A numeric code defers to error.status.
func codeFromPayload(payload map[string]any, topKeys []string, peekMessage bool) string {
	if payload == nil {
		return ""
	}
	if eo, ok := payload["error"].(map[string]any); ok {
		code := eo["code"]
		if !truthy(code) {
			code = eo["type"]
		}
		if _, isStr := code.(string); !isStr {
			if truthy(eo["status"]) {
				code = eo["status"]
			}
		}
		if s, isStr := code.(string); isStr && strings.TrimSpace(s) != "" && strings.TrimSpace(s) != "400" {
			return strings.TrimSpace(s)
		}
		if msg, isStr := eo["message"].(string); peekMessage && isStr && strings.HasPrefix(strings.TrimSpace(msg), "{") {
			if nested := codeFromPayload(jsonDict(msg), []string{"code", "error_code"}, false); nested != "" {
				return nested
			}
		}
	}
	for _, k := range topKeys {
		if truthy(payload[k]) {
			text := strings.TrimSpace(asString(payload[k]))
			if _, isStr := payload[k].(string); !isStr {
				if _, isNum := payload[k].(json.Number); !isNum {
					return ""
				}
			}
			if text != "" && text != "400" {
				return text
			}
			return ""
		}
	}
	return ""
}

func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case string:
		return t != ""
	case bool:
		return t
	case json.Number:
		f, _ := t.Float64()
		return f != 0
	case float64:
		return t != 0
	case map[string]any:
		return len(t) > 0
	case []any:
		return len(t) > 0
	}
	return true
}

func extractErrorCode(body map[string]any) string {
	if len(body) == 0 {
		return ""
	}
	return codeFromPayload(body, []string{"code", "error_code", "errorCode"}, true)
}

// extractMessage is the most informative error message, structured body first, at most 500 characters.
func extractMessage(err error, body map[string]any) string {
	var msg string
	for _, cand := range bodyMessageCandidates(body) {
		if s, ok := cand.(string); ok && strings.TrimSpace(s) != "" {
			msg = strings.TrimSpace(s)
			break
		}
	}
	if msg == "" {
		var ae *APIError
		if errors.As(err, &ae) {
			msg = sdkText(ae, body)
		} else {
			msg = err.Error()
		}
	}
	if r := []rune(msg); len(r) > 500 {
		msg = string(r[:500])
	}
	return msg
}

func hasUsageLimitTransientSignal(msg string, body map[string]any, headers http.Header) bool {
	if containsAny(msg, usageLimitTransientSignals) {
		return true
	}
	for _, payload := range []map[string]any{body, errorObj(body)} {
		for _, f := range resetFields {
			if v, ok := payload[f]; ok && v != nil && v != "" {
				return true
			}
		}
	}
	for _, h := range resetHeaders {
		if headers.Get(h) != "" {
			return true
		}
	}
	return false
}

// rateLimitResetSeconds is the time until a 429's window reopens, from the body's reset fields,
// Retry-After or the message grammar ("retry after Ns", "resets in 4hr"); ok is false when none is named.
func rateLimitResetSeconds(msg string, body map[string]any, headers http.Header) (float64, bool) {
	for _, payload := range []map[string]any{body, errorObj(body)} {
		for _, name := range resetFields {
			v, ok := payload[name]
			if !ok || v == nil || v == "" {
				continue
			}
			if strings.HasSuffix(name, "_at") {
				if f, isNum := numericFloat(v); isNum {
					return math.Max(0, f-float64(now().UnixNano())/1e9), true
				}
			}
			if secs, ok := numberToSeconds(v); ok {
				return secs, true
			}
		}
	}
	if secs, ok := parseRetryAfterSeconds(headers.Get("Retry-After")); ok {
		return secs, true
	}
	return ResetDelayFromMessage(msg)
}

func numericFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case float64:
		return t, true
	}
	return 0, false
}

// serverInjectedParamSenders: parameters Hermes sends on SOME routes only. A rejection of one on a route
// that never sends it means the provider's gateway injected the field itself: a server-side flake, not
// our request shape. The native Anthropic route sends none of them.
var serverInjectedParams = []string{"prompt_cache_retention"}

func isServerInjectedParamRejection(msg string) bool {
	for _, p := range serverInjectedParams {
		if msg != "" && strings.Contains(msg, p) && containsAny(msg, paramRejectionWords) {
			return true
		}
	}
	return false
}

// reasoningFloors are the stale-timeout floors in seconds for Claude reasoning models: Mythos-class named
// models think longest, then Opus 4+/5, then Sonnet 4.5+. Anchored so Claude 3 never matches.
var reasoningFloors = []struct {
	slug  string
	floor int
	re    *regexp.Regexp
}{}

func init() {
	for floor, slugs := range map[int][]string{
		600: {"claude-fable"}, 240: {"claude-opus-4", "claude-opus-5"},
		180: {"claude-sonnet-5", "claude-sonnet-4.5", "claude-sonnet-4.6"},
	} {
		for _, s := range slugs {
			reasoningFloors = append(reasoningFloors, struct {
				slug  string
				floor int
				re    *regexp.Regexp
			}{s, floor, regexp.MustCompile(`^` + regexp.QuoteMeta(s) + `(?:$|[\-._:])`)})
		}
	}
	// Longest slug first, so a more specific slug wins.
	for i := range reasoningFloors {
		for j := i + 1; j < len(reasoningFloors); j++ {
			if len(reasoningFloors[j].slug) > len(reasoningFloors[i].slug) {
				reasoningFloors[i], reasoningFloors[j] = reasoningFloors[j], reasoningFloors[i]
			}
		}
	}
}

// reasoningStaleTimeoutFloor is the stale-timeout floor for a Claude reasoning model. The aggregator
// prefix (up to the last "/") is stripped and the slug matched start-anchored.
func reasoningStaleTimeoutFloor(model string) (time.Duration, bool) {
	name := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	for _, f := range reasoningFloors {
		if f.re.MatchString(name) {
			return time.Duration(f.floor) * time.Second, true
		}
	}
	return 0, false
}

var (
	reasoningFieldTokenRe = regexp.MustCompile(`(?:^|[^\w\-/])(reasoning_effort|thinking_config|thinking_budget|enable_thinking|thinkingconfig|thinkingbudget|reasoning|thinking|think)(?:$|[^\w\-/])`)
	modelsSuffixRe        = regexp.MustCompile(`^\s+models?\b`)
	reasoningParamReject  = regexp.MustCompile(`['"]param['"]\s*:\s*['"](?:reasoning(?:[._]effort)?|thinking(?:_config|_budget)?|enable_thinking)['"]|invalid_reasoning_effort`)
)

// reasoningFieldToken finds a standalone reasoning wire-field name: never a model-id segment ("the model
// kimi-k2-thinking is not supported when using this account") nor the adjective in "not supported with
// reasoning models". It returns the token's start and end offsets.
func reasoningFieldToken(msg string) (start, end int, ok bool) {
	for off := 0; off < len(msg); {
		loc := reasoningFieldTokenRe.FindStringSubmatchIndex(msg[off:])
		if loc == nil {
			return 0, 0, false
		}
		s, e := off+loc[2], off+loc[3]
		// The Python pattern is (?<![\w\-/])token(?![\w\-/])(?!\s+models?\b): the lookahead on the trailing
		// character and on "models" are checked here because RE2 has no lookaround.
		tail := msg[e:]
		followedByModels := modelsSuffixRe.MatchString(tail)
		leftOK := s == 0 || !strings.ContainsRune("_-/", rune(msg[s-1])) && !isWordByte(msg[s-1])
		rightOK := e == len(msg) || !strings.ContainsRune("-/", rune(msg[e])) && !isWordByte(msg[e])
		if leftOK && rightOK && !followedByModels {
			return s, e, true
		}
		off = s + 1
	}
	return 0, 0, false
}

func isWordByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || b >= 0x80
}

// IsReasoningFieldRejection is true for a provider 400 rejecting a reasoning wire control by name
// (reasoning_effort, reasoning, thinking, think): the field token plus either a generic unsupported
// marker or a standalone "unsupported" next to the field. The route default is the right answer for such a
// model, so the loop retries once without the disable.
func IsReasoningFieldRejection(msg string) bool {
	msg = strings.ToLower(msg)
	if reasoningParamReject.MatchString(msg) {
		return true
	}
	s, e, ok := reasoningFieldToken(msg)
	if !ok {
		return false
	}
	near := msg[max(0, s-32):min(len(msg), e+32)]
	return strings.Contains(near, "unsupported") || containsAny(msg, unsupportedParamMarkers)
}

// IsReasoningRequiredRejection is true for a provider 400 saying reasoning cannot be switched OFF
// ("Reasoning is mandatory for this endpoint and cannot be disabled"). The field is understood, the
// disable is refused.
func IsReasoningRequiredRejection(msg string) bool {
	msg = strings.ToLower(msg)
	s, e, ok := reasoningFieldToken(msg)
	if !ok {
		return false
	}
	return containsAny(msg[max(0, s-48):min(len(msg), e+96)], reasoningRequiredMarkers)
}
