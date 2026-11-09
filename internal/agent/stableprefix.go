package agent

import (
	"strings"
	"sync"
)

// Builder-declared stable prefixes for Anthropic prompt caching.
//
// Skill, webhook and cron builders concatenate a large static scaffold with a small volatile tail into
// one user-message string. Only the builder knows where the tail begins, so it registers the stable
// prefix here and the cache planner places a breakpoint at that boundary. Re-parsing marker strings at
// request time is deliberately avoided: markers can legitimately appear inside skill bodies, and any
// delimiter heuristic then shrinks the cached prefix or absorbs volatile bytes. Process-local by design:
// any miss falls back to the whole-message policy.

const (
	// stableMaxEntries is generous for one gateway process: a couple dozen active scaffolds.
	stableMaxEntries = 32
	// stableMaxChars also bounds total retained characters, since entries hold whole expanded skill
	// bodies. The newest entry is always kept so one oversized scaffold still gets a boundary.
	stableMaxChars = 4 * 1024 * 1024
)

type stablePrefixes struct {
	mu    sync.Mutex
	order []string // least recently used first
}

// DefaultStablePrefixes is the process-wide registry.
var DefaultStablePrefixes = &stablePrefixes{}

// Register records prefix as the stable scaffold of a just-built message.
func (r *stablePrefixes) Register(prefix string) {
	if prefix == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.touchLocked(prefix)
	for len(r.order) > stableMaxEntries {
		r.order = r.order[1:]
	}
	for len(r.order) > 1 && r.totalCharsLocked() > stableMaxChars {
		r.order = r.order[1:]
	}
}

func (r *stablePrefixes) touchLocked(prefix string) {
	for i, p := range r.order {
		if p == prefix {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
	r.order = append(r.order, prefix)
}

func (r *stablePrefixes) totalCharsLocked() int {
	n := 0
	for _, p := range r.order {
		n += len([]rune(p))
	}
	return n
}

// Find returns the longest registered proper prefix of content whose tail is non-whitespace. The tail
// must be non-whitespace so the split never yields an empty text block (Anthropic rejects it with HTTP
// 400). A hit refreshes the entry's LRU position, so a scaffold fired every minute by cron is not
// evicted by a burst of one-off skill invocations.
func (r *stablePrefixes) Find(content string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	best := ""
	found := false
	for _, p := range r.order {
		if strings.HasPrefix(content, p) && strings.TrimSpace(content[len(p):]) != "" && (!found || len(p) > len(best)) {
			best, found = p, true
		}
	}
	if found {
		r.touchLocked(best) // after the scan: never mutate mid-iteration
	}
	return best, found
}

// Reset empties the registry (tests).
func (r *stablePrefixes) Reset() {
	r.mu.Lock()
	r.order = nil
	r.mu.Unlock()
}
