package learning

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// ErrInterrupted means the next user message cancelled the review; the job must not be consumed.
var ErrInterrupted = errors.New("review interrupted")

// Executor runs one job in a fork. It returns ErrInterrupted on cancellation.
type Executor interface {
	Execute(ctx context.Context, job Job) error
}

// Runner claims and runs due jobs with bounded retries.
type Runner struct {
	q     Queue
	exec  Executor
	s     Settings
	clock func() time.Time
}

// NewRunner returns a runner; a nil clock means time.Now.
func NewRunner(q Queue, e Executor, s Settings, clock func() time.Time) *Runner {
	if clock == nil {
		clock = time.Now
	}
	return &Runner{q: q, exec: e, s: s, clock: clock}
}

// RunNext claims and runs one due job and returns it as claimed, or nil when nothing is due. A failure is retried
// with exponential backoff (base * 2^attempts) until MaxAttempts, then terminal; an interruption is released with
// attempts unchanged; nothing is ever dropped silently.
func (r *Runner) RunNext(ctx context.Context) (*Job, error) {
	job, err := r.q.Claim(ctx, r.clock())
	if err != nil || job == nil {
		return nil, err
	}
	// Bookkeeping must survive the cancellation that interrupted the review: the job is released, not lost.
	bg := context.WithoutCancel(ctx)
	switch err := r.exec.Execute(ctx, *job); {
	case err == nil:
		return job, r.q.Complete(bg, job.ID)
	case errors.Is(err, ErrInterrupted):
		return job, r.q.Release(bg, job.ID)
	default:
		attempts := job.Attempts + 1
		slog.Warn("review job failed", "job", job.ID, "attempt", attempts, "err", err)
		if attempts >= r.s.MaxAttempts {
			slog.Warn("review job failed permanently", "job", job.ID, "attempts", attempts)
			return job, r.q.Fail(bg, job.ID, err.Error(), time.Time{})
		}
		delay := r.s.BackoffBase * time.Duration(1<<job.Attempts)
		return job, r.q.Fail(bg, job.ID, err.Error(), r.clock().Add(delay))
	}
}
