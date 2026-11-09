package anthropic

import (
	"errors"
	"math"
	"math/rand/v2"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// RetryAfterCap bounds a provider-declared wait. Anthropic Tier 1 input-token buckets reset in about
	// 171s, so a 120s cap re-tripped the limit; 600s covers realistic provider windows while still
	// rejecting pathological values.
	RetryAfterCap = 600 * time.Second
	// LiveRetryWaitCap is a wait long enough that a person feels it: a non-rate-limit Retry-After this
	// long is announced when it starts rather than buffered.
	LiveRetryWaitCap = 60 * time.Second
	// DefaultBackoffBase and DefaultBackoffMax are the jittered backoff bounds for retryable errors.
	DefaultBackoffBase = 2 * time.Second
	DefaultBackoffMax  = 60 * time.Second
)

// now is the clock for HTTP-date Retry-After values; tests replace it.
var now = time.Now

// parseRetryAfterSeconds parses a Retry-After value, a number of seconds or an HTTP-date, into
// seconds clamped at zero. ok is false when it is absent or unparseable.
func parseRetryAfterSeconds(raw string) (float64, bool) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return 0, false
	}
	if f, err := strconv.ParseFloat(text, 64); err == nil {
		if math.IsNaN(f) {
			return 0, true
		}
		return math.Max(0, f), true
	}
	// HTTP-date form (RFC 7231): seconds until that instant, clamped at zero.
	when, err := http.ParseTime(text)
	if err != nil {
		return 0, false
	}
	return math.Max(0, when.Sub(now()).Seconds()), true
}

// numberToSeconds reads a JSON number or numeric string as Retry-After seconds.
func numberToSeconds(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return math.Max(0, t), true
	case int:
		return math.Max(0, float64(t)), true
	case int64:
		return math.Max(0, float64(t)), true
	case string:
		return parseRetryAfterSeconds(t)
	}
	if n, ok := v.(interface{ Float64() (float64, error) }); ok { // json.Number
		f, err := n.Float64()
		return math.Max(0, f), err == nil
	}
	return 0, false
}

// Free-text reset grammars providers put in error bodies, tried in order. One table so the
// conversation loop and any cooldown agree on the same wait. An explicit "retry after N s" wins over
// "resets in ...": a body carrying both describes a short throttle inside a long quota window, and the
// shorter explicit wait is the one the provider actually asks for.
var (
	quotaResetDelayRe     = regexp.MustCompile(`(?i)quotaResetDelay[:\s"]+(\d+(?:\.\d+)?)(ms|s)`)
	resetsInRe            = regexp.MustCompile(`(?i)resets?\s+in\s+(?:(\d+(?:\.\d+)?)\s*(?:h|hr|hrs|hour|hours)\b\s*)?(?:(\d+(?:\.\d+)?)\s*(?:m|min|mins|minute|minutes)\b\s*)?(?:(\d+(?:\.\d+)?)\s*(?:s|sec|secs|second|seconds)\b)?`)
	retryAfterSecondsRe   = regexp.MustCompile(`(?i)retry\s+(?:after\s+)?(\d+(?:\.\d+)?)\s*(?:sec|secs|seconds|s\b)`)
	resetsInSecondsFieldR = regexp.MustCompile(`(?i)resets_in_seconds\W{1,4}(\d+(?:\.\d+)?)`)
)

func atof(s string) float64 { f, _ := strconv.ParseFloat(s, 64); return f }

// ResetDelayFromMessage is the seconds-until-reset parsed from a free-text provider error message.
func ResetDelayFromMessage(msg string) (seconds float64, ok bool) {
	if msg == "" {
		return 0, false
	}
	if m := quotaResetDelayRe.FindStringSubmatch(msg); m != nil {
		v := atof(m[1])
		if strings.EqualFold(m[2], "ms") {
			v /= 1000
		}
		return v, true
	}
	if m := retryAfterSecondsRe.FindStringSubmatch(msg); m != nil {
		return atof(m[1]), true
	}
	if m := resetsInSecondsFieldR.FindStringSubmatch(msg); m != nil {
		return atof(m[1]), true
	}
	if m := resetsInRe.FindStringSubmatch(msg); m != nil && (m[1] != "" || m[2] != "" || m[3] != "") {
		return atof(m[1])*3600 + atof(m[2])*60 + atof(m[3]), true
	}
	return 0, false
}

// ProviderRetryAfter is the provider-declared cooldown: the Retry-After header, else a retry_after body
// field (top level or under "error"). It is absent when missing, unparseable or zero: a zero or expired
// cooldown carries no usable wait, and treating it as one would hot-loop the provider.
func ProviderRetryAfter(err error) (time.Duration, bool) {
	var ae *APIError
	if !errors.As(err, &ae) {
		return 0, false
	}
	secs, ok := parseRetryAfterSeconds(ae.Header.Get("Retry-After"))
	if !ok {
		body := parseBody(ae.Body)
		src := body
		if nested, isMap := body["error"].(map[string]any); isMap {
			src = nested
		}
		secs, ok = numberToSeconds(src["retry_after"])
	}
	if !ok || secs <= 0 || math.IsInf(secs, 0) {
		return 0, false
	}
	return time.Duration(secs * float64(time.Second)), true
}

// Backoff is a jittered exponential backoff for decorrelated retries. Jitter prevents a thundering
// herd when many sessions hit the same rate-limited provider at once.
type Backoff struct {
	Base, Max   time.Duration
	JitterRatio float64
	// Rand returns a uniform float in [0, 1); nil uses math/rand/v2.
	Rand func() float64
}

// DefaultBackoff is 2s doubling to 60s with 50% jitter.
var DefaultBackoff = Backoff{Base: DefaultBackoffBase, Max: DefaultBackoffMax, JitterRatio: 0.5}

// Delay is min(base * 2^(attempt-1), max) plus uniform jitter in [0, ratio * delay]. attempt is 1-based.
func (b Backoff) Delay(attempt int) time.Duration {
	exp := max(0, attempt-1)
	delay := b.Max
	if exp < 63 && b.Base > 0 {
		delay = min(time.Duration(float64(b.Base)*math.Pow(2, float64(exp))), b.Max)
	}
	r := b.Rand
	if r == nil {
		r = rand.Float64
	}
	return delay + time.Duration(r()*b.JitterRatio*float64(delay))
}

// RetryWait is how long to wait before the next attempt. Retry-After wins for any retryable error, not
// just 429s: retryable 5xx responses (Cloudflare 520/524) also carry the header or a structured
// retry_after body field, and ignoring either turns an origin outage into a retry storm. It is capped
// at RetryAfterCap; otherwise jittered backoff. announce is true when the wait is long enough that the
// user should be told when it starts.
func RetryWait(err error, retryCount int, b Backoff) (wait time.Duration, announce bool) {
	if ra, ok := ProviderRetryAfter(err); ok {
		ra = min(ra, RetryAfterCap)
		return ra, ra > LiveRetryWaitCap
	}
	return b.Delay(retryCount), false
}
