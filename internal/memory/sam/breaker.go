package sam

import "sync"

// Breaker is a consecutive-failure circuit breaker. It opens for the rest of the session; a success resets the
// count. An auth failure opens it at once (retrying a bad key cannot help).
type Breaker struct {
	threshold int

	mu       sync.Mutex
	failures int
	open     bool
}

// NewBreaker returns a closed breaker that opens after threshold consecutive failures.
func NewBreaker(threshold int) *Breaker { return &Breaker{threshold: max(1, threshold)} }

// IsOpen reports whether memory is degraded for this session.
func (b *Breaker) IsOpen() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.open
}

// RecordOK resets the failure count.
func (b *Breaker) RecordOK() {
	b.mu.Lock()
	b.failures = 0
	b.mu.Unlock()
}

// RecordFailure counts a failure and reports whether this one opened the breaker (so the caller warns once).
func (b *Breaker) RecordFailure(auth bool) (opened bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures++
	if !b.open && (auth || b.failures >= b.threshold) {
		b.open = true
		return true
	}
	return false
}
