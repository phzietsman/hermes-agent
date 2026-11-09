package learning

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phzietsman/hermes-agent/internal/llm"
	"github.com/phzietsman/hermes-agent/internal/session"
)

var ctx = context.Background()

func newQueue(t *testing.T) *SQLQueue {
	st, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	q, err := NewSQLQueue(ctx, st.DB())
	if err != nil {
		t.Fatal(err)
	}
	return q
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)} }
func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *clock) advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func job(id, session string, start, end int, at time.Time) Job {
	return Job{ID: id, SessionID: session, Trigger: TriggerIterations, Start: start, End: end,
		Digest: DigestRange(session, start, end, nil), CreatedAt: at, Status: Pending}
}

// ---- queue ----

func TestQueueDedupesSameRangeAndClaimsOldestFirst(t *testing.T) {
	q, c := newQueue(t), newClock()
	if ok, err := q.Enqueue(ctx, job("a", "s", 0, 4, c.now())); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, _ := q.Enqueue(ctx, job("a2", "s", 0, 4, c.now())); ok {
		t.Error("the same session range must not queue twice")
	}
	q.Enqueue(ctx, job("b", "s", 4, 9, c.now().Add(time.Second)))
	q.Enqueue(ctx, job("c", "other", 0, 4, c.now().Add(-time.Second)))
	var order []string
	for {
		j, err := q.Claim(ctx, c.now().Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if j == nil {
			break
		}
		if j.Status != Running {
			t.Errorf("claimed job status %s", j.Status)
		}
		order = append(order, j.ID)
	}
	if strings.Join(order, ",") != "c,a,b" {
		t.Errorf("order %v", order)
	}
}

func TestQueueHonoursNotBeforeAndRetries(t *testing.T) {
	q, c := newQueue(t), newClock()
	q.Enqueue(ctx, job("a", "s", 0, 4, c.now()))
	j, _ := q.Claim(ctx, c.now())
	if err := q.Fail(ctx, j.ID, "boom", c.now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got, _ := q.Claim(ctx, c.now()); got != nil {
		t.Error("a job in backoff was claimed")
	}
	got, _ := q.Claim(ctx, c.now().Add(2*time.Minute))
	if got == nil || got.Attempts != 1 || got.LastError != "boom" || got.NotBefore.IsZero() {
		t.Fatalf("%+v", got)
	}
	q.Fail(ctx, got.ID, "again", time.Time{}) // terminal
	if again, _ := q.Claim(ctx, c.now().Add(time.Hour)); again != nil {
		t.Error("a terminally failed job must never be claimed")
	}
}

func TestQueueReleaseKeepsAttemptsAndCompleteRemovesFromRotation(t *testing.T) {
	q, c := newQueue(t), newClock()
	q.Enqueue(ctx, job("a", "s", 0, 4, c.now()))
	j, _ := q.Claim(ctx, c.now())
	q.Release(ctx, j.ID)
	j2, _ := q.Claim(ctx, c.now())
	if j2 == nil || j2.Attempts != 0 {
		t.Fatalf("a released job returns with attempts unchanged: %+v", j2)
	}
	q.Complete(ctx, j2.ID)
	if j3, _ := q.Claim(ctx, c.now()); j3 != nil {
		t.Error("a completed job was claimed again")
	}
}

func TestACrashLeavesRunningJobsToBeRetried(t *testing.T) {
	st, _ := session.Open(t.TempDir())
	defer st.Close()
	q, _ := NewSQLQueue(ctx, st.DB())
	c := newClock()
	q.Enqueue(ctx, job("a", "s", 0, 4, c.now()))
	if j, _ := q.Claim(ctx, c.now()); j == nil {
		t.Fatal("no job")
	}
	q2, _ := NewSQLQueue(ctx, st.DB()) // the next process
	if j, _ := q2.Claim(ctx, c.now()); j == nil || j.ID != "a" || j.Attempts != 0 {
		t.Errorf("a job left running by a crash must be retried: %+v", j)
	}
}

func TestTimestampsSortChronologicallyAcrossFractionalDigits(t *testing.T) {
	q := newQueue(t)
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	// 12:00:00.5 and 12:00:00.123456 would sort wrongly as variable-width strings.
	q.Enqueue(ctx, job("late", "s", 0, 1, base.Add(500*time.Millisecond)))
	q.Enqueue(ctx, job("early", "s", 1, 2, base.Add(123456*time.Microsecond)))
	j, _ := q.Claim(ctx, base.Add(time.Hour))
	if j.ID != "early" {
		t.Errorf("claimed %s first", j.ID)
	}
}

// ---- scheduler ----

func texts(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("message %d", i)
	}
	return out
}

func TestSchedulerQueuesEveryNIterationsOverTheNewRange(t *testing.T) {
	q, c := newQueue(t), newClock()
	s := NewScheduler(q, Settings{IterationInterval: 3, ReviewOnBoundary: true}, c.now)
	calls := 0
	tx := func() []string { calls++; return texts(8) }
	first := s.OnToolIteration(ctx, "s", tx)
	second := s.OnToolIteration(ctx, "s", tx)
	if first || second {
		t.Fatal("queued before the interval")
	}
	if calls != 0 {
		t.Error("texts must be built only when a review is due")
	}
	if !s.OnToolIteration(ctx, "s", tx) {
		t.Fatal("third iteration should queue")
	}
	j, _ := q.Claim(ctx, c.now())
	if j.Start != 0 || j.End != 8 || j.Trigger != TriggerIterations {
		t.Errorf("%+v", j)
	}
	// The next window starts where the last ended.
	for range 3 {
		s.OnToolIteration(ctx, "s", func() []string { return texts(12) })
	}
	j2, _ := q.Claim(ctx, c.now())
	if j2 == nil || j2.Start != 8 || j2.End != 12 {
		t.Errorf("%+v", j2)
	}
}

func TestSchedulerNoteAgentWroteRestartsTheCount(t *testing.T) {
	q, c := newQueue(t), newClock()
	s := NewScheduler(q, Settings{IterationInterval: 2}, c.now)
	s.OnToolIteration(ctx, "s", func() []string { return texts(3) })
	s.NoteAgentWrote()
	if s.OnToolIteration(ctx, "s", func() []string { return texts(3) }) {
		t.Error("the count should have restarted")
	}
}

func TestSchedulerDisabledTriggers(t *testing.T) {
	q, c := newQueue(t), newClock()
	s := NewScheduler(q, Settings{IterationInterval: 0, IdleSeconds: 0, ReviewOnBoundary: false}, c.now)
	for range 50 {
		if s.OnToolIteration(ctx, "s", func() []string { return texts(5) }) {
			t.Fatal("iteration trigger fired while disabled")
		}
	}
	if s.OnBoundary(ctx, "s", TriggerSessionEnd, texts(5)) {
		t.Error("boundary trigger fired while disabled")
	}
	c.advance(24 * time.Hour)
	if s.CheckIdle(ctx, "s", texts(5)) {
		t.Error("idle trigger fired while disabled")
	}
}

func TestSchedulerBoundaryAdvancesPastDuplicates(t *testing.T) {
	q, c := newQueue(t), newClock()
	s := NewScheduler(q, Settings{ReviewOnBoundary: true}, c.now)
	if !s.OnBoundary(ctx, "s", TriggerSessionEnd, texts(4)) {
		t.Fatal("boundary should queue")
	}
	if s.OnBoundary(ctx, "s", TriggerPreCompress, texts(4)) {
		t.Error("nothing new since the last review: nothing to queue")
	}
	if !s.OnBoundary(ctx, "s", TriggerPreCompress, texts(6)) {
		t.Error("new messages should queue")
	}
	// A restarted scheduler re-queues the same range, which the queue refuses: no double review.
	s2 := NewScheduler(q, Settings{ReviewOnBoundary: true}, c.now)
	if s2.OnBoundary(ctx, "s", TriggerSessionEnd, texts(4)) {
		t.Error("an already-queued range was queued again")
	}
}

func TestSchedulerIdleFiresOncePerQuietPeriod(t *testing.T) {
	q, c := newQueue(t), newClock()
	s := NewScheduler(q, Settings{IdleSeconds: 60}, c.now)
	s.OnTurnEnd()
	c.advance(30 * time.Second)
	if s.CheckIdle(ctx, "s", texts(4)) {
		t.Error("fired before the quiet period")
	}
	c.advance(31 * time.Second)
	if !s.CheckIdle(ctx, "s", texts(4)) {
		t.Fatal("should fire after the quiet period")
	}
	c.advance(61 * time.Second)
	if s.CheckIdle(ctx, "s", texts(4)) {
		t.Error("nothing new to review")
	}
}

// ---- runner ----

type execFunc func(context.Context, Job) error

func (f execFunc) Execute(c context.Context, j Job) error { return f(c, j) }

func TestRunnerRetriesWithBackoffThenGivesUp(t *testing.T) {
	q, c := newQueue(t), newClock()
	q.Enqueue(ctx, job("a", "s", 0, 4, c.now()))
	r := NewRunner(q, execFunc(func(context.Context, Job) error { return errors.New("model down") }),
		Settings{MaxAttempts: 3, BackoffBase: 30 * time.Second}, c.now)
	if j, err := r.RunNext(ctx); err != nil || j == nil {
		t.Fatal(j, err)
	}
	if j, _ := r.RunNext(ctx); j != nil {
		t.Error("a job in backoff ran")
	}
	c.advance(31 * time.Second) // first retry: base * 2^0 = 30 s
	if j, _ := r.RunNext(ctx); j == nil || j.Attempts != 1 {
		t.Fatalf("%+v", j)
	}
	c.advance(31 * time.Second) // second retry waits base * 2^1 = 60 s
	if j, _ := r.RunNext(ctx); j != nil {
		t.Error("the backoff must double")
	}
	c.advance(30 * time.Second)
	if j, _ := r.RunNext(ctx); j == nil || j.Attempts != 2 {
		t.Fatalf("%+v", j)
	}
	c.advance(24 * time.Hour)
	if j, _ := r.RunNext(ctx); j != nil {
		t.Error("a job that failed MaxAttempts times must stay failed")
	}
}

func TestRunnerInterruptedJobsAreReleasedUnchangedAndSuccessCompletes(t *testing.T) {
	q, c := newQueue(t), newClock()
	q.Enqueue(ctx, job("a", "s", 0, 4, c.now()))
	var calls atomic.Int32
	r := NewRunner(q, execFunc(func(context.Context, Job) error {
		if calls.Add(1) == 1 {
			return ErrInterrupted
		}
		return nil
	}), Settings{MaxAttempts: 3, BackoffBase: time.Second}, c.now)
	r.RunNext(ctx)
	j, _ := r.RunNext(ctx)
	if j == nil || j.Attempts != 0 {
		t.Fatalf("an interrupted job must retry immediately with attempts unchanged: %+v", j)
	}
	if j, _ := r.RunNext(ctx); j != nil {
		t.Error("a completed job ran again")
	}
}

// ---- loop ----

type fakeHost struct {
	mu       sync.Mutex
	reviewed [][]llm.Message
	outcome  Outcome
	stored   map[string][]llm.Message
	block    chan struct{}
}

func (h *fakeHost) SpawnReview(c context.Context, msgs []llm.Message, done func(Outcome)) {
	go func() {
		if h.block != nil {
			select {
			case <-h.block:
			case <-c.Done():
				done(Interrupted)
				return
			}
		}
		h.mu.Lock()
		h.reviewed = append(h.reviewed, msgs)
		h.mu.Unlock()
		o := h.outcome
		if o == "" {
			o = Done
		}
		done(o)
	}()
}

func (h *fakeHost) LoadMessages(_ context.Context, id string) []llm.Message { return h.stored[id] }

func (h *fakeHost) count() int { h.mu.Lock(); defer h.mu.Unlock(); return len(h.reviewed) }

func conv(n int) []llm.Message {
	out := make([]llm.Message, n)
	for i := range out {
		role := llm.RoleUser
		if i%2 == 1 {
			role = llm.RoleAssistant
		}
		out[i] = llm.Message{Role: role, Content: llm.Text(fmt.Sprintf("m%d", i))}
	}
	return out
}

func TestLoopReviewsAfterTheIterationIntervalOnlyOnceTheTurnEnds(t *testing.T) {
	q := newQueue(t)
	h := &fakeHost{}
	l := NewLoop(q, Settings{IterationInterval: 2, MaxAttempts: 3, BackoffBase: time.Second}, h, nil)
	defer l.Close()
	hist := conv(6)
	l.OnToolIteration("s", hist, false)
	if !l.OnToolIteration("s", hist, false) {
		t.Fatal("second iteration should queue")
	}
	time.Sleep(50 * time.Millisecond)
	if h.count() != 0 {
		t.Error("a review started mid-turn: it must wait for the response to be delivered")
	}
	l.OnTurnEnd("s", hist)
	if !l.Wait(5*time.Second) || h.count() != 1 {
		t.Fatalf("reviews run: %d", h.count())
	}
	if len(h.reviewed[0]) != 6 {
		t.Errorf("the fork saw %d messages", len(h.reviewed[0]))
	}
}

func TestLoopBoundaryWithoutKickWaitsForTheNextProcess(t *testing.T) {
	st, _ := session.Open(t.TempDir())
	defer st.Close()
	q, _ := NewSQLQueue(ctx, st.DB())
	h1 := &fakeHost{}
	l1 := NewLoop(q, Settings{ReviewOnBoundary: true, MaxAttempts: 3, BackoffBase: time.Second}, h1, nil)
	if !l1.OnBoundary("old-session", TriggerSessionEnd, conv(4), false) {
		t.Fatal("should queue")
	}
	l1.Close()
	time.Sleep(50 * time.Millisecond)
	if h1.count() != 0 {
		t.Error("a boundary without kick must not start a review")
	}
	// A later process drains it, reading the transcript back from the store because its snapshot is gone.
	h2 := &fakeHost{stored: map[string][]llm.Message{"old-session": conv(4)}}
	q2, _ := NewSQLQueue(ctx, st.DB())
	l2 := NewLoop(q2, Settings{MaxAttempts: 3, BackoffBase: time.Second}, h2, nil)
	defer l2.Close()
	l2.OnTurnEnd("new-session", conv(2))
	l2.Wait(5 * time.Second)
	if h2.count() != 1 || len(h2.reviewed[0]) != 4 {
		t.Errorf("the queued review should have run over the stored transcript: %d", h2.count())
	}
}

func TestLoopFailedReviewIsRetriedNotLost(t *testing.T) {
	q := newQueue(t)
	h := &fakeHost{outcome: Failed}
	l := NewLoop(q, Settings{ReviewOnBoundary: true, MaxAttempts: 3, BackoffBase: time.Hour}, h, nil)
	defer l.Close()
	l.OnBoundary("s", TriggerSessionEnd, conv(4), true)
	l.Wait(5 * time.Second)
	if h.count() != 1 {
		t.Fatalf("%d", h.count())
	}
	// It is in backoff, not gone: claimable once the backoff has passed.
	if j, _ := q.Claim(ctx, time.Now()); j != nil {
		t.Error("claimable inside the backoff")
	}
	if j, _ := q.Claim(ctx, time.Now().Add(2*time.Hour)); j == nil || j.Attempts != 1 {
		t.Errorf("%+v", j)
	}
}

func TestLoopCloseInterruptsARunningReviewAndKeepsTheJob(t *testing.T) {
	q := newQueue(t)
	h := &fakeHost{block: make(chan struct{})}
	l := NewLoop(q, Settings{ReviewOnBoundary: true, MaxAttempts: 3, BackoffBase: time.Second}, h, nil)
	l.OnBoundary("s", TriggerSessionEnd, conv(4), true)
	time.Sleep(50 * time.Millisecond)
	l.Close()
	if !l.Wait(5 * time.Second) {
		t.Fatal("Close did not stop the drain")
	}
	if j, _ := q.Claim(ctx, time.Now()); j == nil || j.Attempts != 0 {
		t.Errorf("an interrupted review must stay queued, unchanged: %+v", j)
	}
}

func TestLoopIdleTimerQueuesAReview(t *testing.T) {
	q := newQueue(t)
	h := &fakeHost{}
	// IdleSeconds is whole seconds: 1 s is the shortest quiet period.
	l := NewLoop(q, Settings{IdleSeconds: 1, MaxAttempts: 3, BackoffBase: time.Second}, h, nil)
	defer l.Close()
	l.OnTurnEnd("s", conv(4))
	deadline := time.Now().Add(5 * time.Second)
	for h.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if h.count() != 1 {
		t.Error("the idle review never ran")
	}
}

func TestTextsAndDigestAreStable(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: llm.Text("hi")},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "c1", Type: "function", Function: llm.ToolCallFunction{Name: "x", Arguments: "{}"}}}},
	}
	tx := Texts(msgs)
	if tx[0] != "hi" || !strings.Contains(tx[1], `"x"`) {
		t.Errorf("%v", tx)
	}
	if DigestRange("s", 0, 2, tx) != DigestRange("s", 0, 2, Texts(msgs)) || DigestRange("s", 0, 2, tx) == DigestRange("s", 0, 3, tx) {
		t.Error("digest must be a pure function of session, range and texts")
	}
}

func TestHostExecutorTimeoutAndMissingTranscript(t *testing.T) {
	h := &fakeHost{block: make(chan struct{})}
	e := &HostExecutor{Host: h, Snapshots: func(string) []llm.Message { return conv(2) }, Wait: 50 * time.Millisecond}
	if err := e.Execute(ctx, Job{ID: "j", SessionID: "s"}); err == nil || errors.Is(err, ErrInterrupted) {
		t.Errorf("a review that never reports must time out as a failure: %v", err)
	}
	e2 := &HostExecutor{Host: &fakeHost{}, Snapshots: func(string) []llm.Message { return nil }}
	if err := e2.Execute(ctx, Job{ID: "j", SessionID: "gone"}); err != nil {
		t.Errorf("a vanished transcript is not an error: %v", err)
	}
}
