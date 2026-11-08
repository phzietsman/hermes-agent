package learning

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// Settings tune when reviews run.
type Settings struct {
	// IterationInterval is the number of tool iterations between reviews; 0 disables the trigger.
	IterationInterval int
	// IdleSeconds is the quiet period before an idle review; 0 disables the trigger.
	IdleSeconds int
	MaxAttempts int
	BackoffBase time.Duration
	// ReviewOnBoundary enables the session end, reset and pre-compress triggers.
	ReviewOnBoundary bool
}

// DefaultSettings are Python's defaults.
func DefaultSettings() Settings {
	return Settings{IterationInterval: 10, IdleSeconds: 900, MaxAttempts: 5, BackoffBase: 30 * time.Second, ReviewOnBoundary: true}
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Scheduler decides when a review is due and enqueues it (idempotently). It counts tool iterations, not user
// turns, so one tool-heavy turn can still trigger. The caller supplies the message range and texts; the scheduler
// never reads history.
type Scheduler struct {
	q     Queue
	s     Settings
	clock func() time.Time

	mu           sync.Mutex
	iters        int
	reviewedUpto int
	lastActivity time.Time
}

// NewScheduler returns a scheduler; a nil clock means time.Now.
func NewScheduler(q Queue, s Settings, clock func() time.Time) *Scheduler {
	if clock == nil {
		clock = time.Now
	}
	return &Scheduler{q: q, s: s, clock: clock, lastActivity: clock()}
}

func (s *Scheduler) enqueue(ctx context.Context, session string, trig Trigger, end int, texts []string) bool {
	start := s.reviewedUpto
	if end <= start || end > len(texts) {
		return false
	}
	job := Job{ID: newID(), SessionID: session, Trigger: trig, Start: start, End: end, Digest: DigestRange(session, start, end, texts[start:end]), CreatedAt: s.clock(), Status: Pending}
	// Advance even on a duplicate: that range is already queued or reviewed.
	s.reviewedUpto = end
	s.iters = 0
	ok, err := s.q.Enqueue(ctx, job)
	return err == nil && ok
}

// OnToolIteration counts one iteration. texts is only called when a review is due, so the hot path stays cheap.
func (s *Scheduler) OnToolIteration(ctx context.Context, session string, texts func() []string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastActivity = s.clock()
	s.iters++
	if n := s.s.IterationInterval; n > 0 && s.iters >= n {
		t := texts()
		return s.enqueue(ctx, session, TriggerIterations, len(t), t)
	}
	return false
}

// NoteAgentWrote restarts the pending iteration count: the agent saved memory or a skill itself.
func (s *Scheduler) NoteAgentWrote() {
	s.mu.Lock()
	s.iters = 0
	s.mu.Unlock()
}

// OnTurnEnd records activity.
func (s *Scheduler) OnTurnEnd() {
	s.mu.Lock()
	s.lastActivity = s.clock()
	s.mu.Unlock()
}

// OnBoundary queues a review at a session boundary.
func (s *Scheduler) OnBoundary(ctx context.Context, session string, trig Trigger, texts []string) bool {
	if !s.s.ReviewOnBoundary {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enqueue(ctx, session, trig, len(texts), texts)
}

// CheckIdle queues one review per quiet period.
func (s *Scheduler) CheckIdle(ctx context.Context, session string, texts []string) bool {
	idle := time.Duration(s.s.IdleSeconds) * time.Second
	if idle <= 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.clock().Sub(s.lastActivity) < idle {
		return false
	}
	fired := s.enqueue(ctx, session, TriggerIdle, len(texts), texts)
	s.lastActivity = s.clock()
	return fired
}
