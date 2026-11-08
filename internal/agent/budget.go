// Package agent holds the turn machinery: the iteration budget, the prompt-cache planner, system-prompt
// assembly and the conversation loop. It ports agent/iteration_budget.py, agent/prompt_caching.py,
// agent/prompt_cache_boundary.py, agent/system_prompt.py and agent/prompt_builder.py.
package agent

import (
	"math"
	"sync"
)

// IterationBudget is a thread-safe iteration counter. Each agent (parent or subagent) holds its own: the
// parent's cap is its max iterations (default 500), each subagent's delegation.max_iterations (default
// 50), so total iterations across a parent and its subagents can exceed the parent's cap. Programmatic
// tool-calling turns are refunded so they do not eat into the budget.
type IterationBudget struct {
	mu   sync.Mutex
	max  int
	used int
}

// NewIterationBudget returns a budget of max iterations.
func NewIterationBudget(max int) *IterationBudget { return &IterationBudget{max: max} }

// Consume takes one iteration and reports whether one was left.
func (b *IterationBudget) Consume() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.used >= b.max {
		return false
	}
	b.used++
	return true
}

// Refund gives one iteration back.
func (b *IterationBudget) Refund() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.used > 0 {
		b.used--
	}
}

// Used is the number of iterations consumed.
func (b *IterationBudget) Used() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}

// Max is the cap.
func (b *IterationBudget) Max() int { return b.max }

// Remaining is the number of iterations left.
func (b *IterationBudget) Remaining() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return max(0, b.max-b.used)
}

// NormalizeBudgetWarningRatio returns a finite ratio strictly between zero and one, or ok=false (the
// warning feature is off).
func NormalizeBudgetWarningRatio(v float64) (ratio float64, ok bool) {
	if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 || v >= 1 {
		return 0, false
	}
	return v, true
}
