package learning

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/phzietsman/hermes-agent/internal/llm"
)

// WaitTimeout bounds one review fork: a fork can legitimately run for minutes; past this the job is retried.
const WaitTimeout = 30 * time.Minute

// Host is the agent side of the seam: run a review fork over a transcript.
type Host interface {
	// SpawnReview starts a review of messages and reports its outcome exactly once, possibly from another goroutine.
	SpawnReview(ctx context.Context, messages []llm.Message, onOutcome func(Outcome))
	// LoadMessages returns a session's persisted transcript, for jobs that outlived the process that queued them.
	LoadMessages(ctx context.Context, sessionID string) []llm.Message
}

// Texts is the cheap per-message text used for digesting; tool calls and structured content collapse to JSON.
func Texts(msgs []llm.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		switch {
		case !m.Content.IsEmpty() && !m.Content.IsParts():
			out[i] = m.Content.AsText()
		case len(m.ToolCalls) > 0:
			b, _ := json.Marshal(m.ToolCalls)
			out[i] = string(b)
		}
	}
	return out
}

// HostExecutor runs a job through the host and blocks until the fork reports its outcome.
type HostExecutor struct {
	Host      Host
	Snapshots func(sessionID string) []llm.Message
	Wait      time.Duration
}

// Execute implements Executor.
func (e *HostExecutor) Execute(ctx context.Context, job Job) error {
	msgs := e.Snapshots(job.SessionID)
	if len(msgs) == 0 {
		msgs = e.Host.LoadMessages(ctx, job.SessionID)
	}
	if len(msgs) == 0 {
		return nil // the transcript is gone (session deleted): nothing to review
	}
	out := make(chan Outcome, 1)
	e.Host.SpawnReview(ctx, msgs, func(o Outcome) {
		select {
		case out <- o:
		default:
		}
	})
	wait := e.Wait
	if wait <= 0 {
		wait = WaitTimeout
	}
	select {
	case o := <-out:
		switch o {
		case Interrupted:
			return ErrInterrupted
		case Failed:
			return fmt.Errorf("review fork failed")
		}
		return nil
	case <-time.After(wait):
		return fmt.Errorf("review %s produced no outcome in %s", job.ID, wait)
	case <-ctx.Done():
		return ErrInterrupted
	}
}

// Loop is one per agent: scheduler, durable queue and background drain. Enqueueing is cheap and may happen
// mid-turn; reviews only start after the user's response is delivered, so a review never competes with the live
// task.
type Loop struct {
	sched  *Scheduler
	runner *Runner
	s      Settings

	mu        sync.Mutex
	snapshots map[string][]llm.Message
	worker    bool
	idle      *time.Timer
	closed    bool
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

// NewLoop builds a loop over q. A nil executor runs jobs through host.
func NewLoop(q Queue, s Settings, host Host, exec Executor) *Loop {
	l := &Loop{s: s, snapshots: map[string][]llm.Message{}}
	l.ctx, l.cancel = context.WithCancel(context.Background())
	if exec == nil {
		exec = &HostExecutor{Host: host, Snapshots: l.snapshot, Wait: WaitTimeout}
	}
	l.sched = NewScheduler(q, s, nil)
	l.runner = NewRunner(q, exec, s, nil)
	return l
}

func (l *Loop) snapshot(session string) []llm.Message {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.snapshots[session]
}

func (l *Loop) remember(session string, msgs []llm.Message) {
	cp := make([]llm.Message, len(msgs))
	for i, m := range msgs {
		cp[i] = m.Clone()
	}
	l.mu.Lock()
	l.snapshots[session] = cp
	l.mu.Unlock()
}

// OnToolIteration counts one model-call-with-tools; wrote says the agent saved memory or a skill itself in it,
// which restarts the count.
func (l *Loop) OnToolIteration(session string, history []llm.Message, wrote bool) bool {
	if wrote {
		l.sched.NoteAgentWrote()
	}
	if l.sched.OnToolIteration(l.ctx, session, func() []string { return Texts(history) }) {
		l.remember(session, history)
		return true
	}
	return false
}

// OnBoundary queues a review at a session boundary. With kick it starts draining at once; without, the job waits
// in the durable queue for the next process that runs a loop (a one-shot CLI exits right after its boundary, and
// a review cancelled by the exit would only waste the call).
func (l *Loop) OnBoundary(session string, trig Trigger, history []llm.Message, kick bool) bool {
	if len(history) == 0 {
		return false
	}
	if l.sched.OnBoundary(l.ctx, session, trig, Texts(history)) {
		l.remember(session, history)
		if kick {
			l.Kick()
		}
		return true
	}
	return false
}

// OnTurnEnd runs once the response is delivered: arm the idle timer and start draining anything queued.
func (l *Loop) OnTurnEnd(session string, history []llm.Message) {
	l.sched.OnTurnEnd()
	l.mu.Lock()
	if _, ok := l.snapshots[session]; !ok {
		l.mu.Unlock()
		l.remember(session, history)
	} else {
		l.mu.Unlock()
	}
	l.armIdle(session, history)
	l.Kick()
}

func (l *Loop) armIdle(session string, history []llm.Message) {
	if l.s.IdleSeconds <= 0 {
		return
	}
	snap := make([]llm.Message, len(history))
	for i, m := range history {
		snap[i] = m.Clone()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	if l.idle != nil {
		l.idle.Stop()
	}
	l.idle = time.AfterFunc(time.Duration(l.s.IdleSeconds)*time.Second, func() {
		if l.sched.CheckIdle(l.ctx, session, Texts(snap)) {
			l.remember(session, snap)
			l.Kick()
		}
	})
}

// Kick starts the drain goroutine if it is idle. It never blocks the caller.
func (l *Loop) Kick() {
	l.mu.Lock()
	if l.closed || l.worker {
		l.mu.Unlock()
		return
	}
	l.worker = true
	l.wg.Add(1)
	l.mu.Unlock()
	go l.drain()
}

func (l *Loop) drain() {
	defer l.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			slog.Error("review drain crashed", "panic", r)
		}
		l.mu.Lock()
		l.worker = false
		l.mu.Unlock()
	}()
	for {
		job, err := l.runner.RunNext(l.ctx)
		if err != nil {
			if l.ctx.Err() == nil {
				slog.Error("review queue error", "err", err)
			}
			return
		}
		if job == nil || l.ctx.Err() != nil {
			return
		}
	}
}

// Wait blocks until the drain goroutine has finished (tests, and a clean exit when the caller wants reviews done).
func (l *Loop) Wait(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() { l.wg.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// Close stops the idle timer and cancels a running review (it is released, not lost: the queue keeps the job).
func (l *Loop) Close() {
	l.mu.Lock()
	l.closed = true
	if l.idle != nil {
		l.idle.Stop()
	}
	l.mu.Unlock()
	l.cancel()
}
