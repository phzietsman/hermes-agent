package cron

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Run statuses recorded on a job.
const (
	StatusOK             = "ok"
	StatusError          = "error"
	StatusDeliveryFailed = "delivery_failed"
	StatusMissed         = "missed"
	StatusSkipped        = "skipped"
)

// SilentToken as a whole response suppresses delivery ("nothing new to report").
const SilentToken = "[SILENT]"

// FailureToken on the first line of a response records the run as failed.
const FailureToken = "[CRON_FAILURE]"

// RunResult is what a job's agent run produced.
type RunResult struct {
	// Output is the agent's final response: what gets delivered and saved.
	Output string
	// Err is a failed run (the model was unreachable, the run timed out or was interrupted).
	Err error
}

// Runner runs one job in a fresh session. transient is context for this run only (an explicit run's extra prompt).
type Runner interface {
	Run(ctx context.Context, job Job, transient string) RunResult
}

// Deliverer sends a job's output to where "deliver" says. Local-only jobs never reach it.
type Deliverer interface {
	Deliver(ctx context.Context, job Job, text string) error
}

// Scheduler fires due jobs. One Scheduler serves one store; a per-store tick lock keeps two processes from
// ticking the same store at once.
type Scheduler struct {
	Store    *Store
	Runner   Runner
	Deliver  Deliverer
	Interval time.Duration // between ticks; default one minute
	// MaxParallel is how many due jobs run at once (default 1).
	MaxParallel int
	// CatchUpMissed runs a recurring job that was missed past its grace once, late (default true); false skips it
	// with a logged reason.
	CatchUpMissed bool

	mu      sync.Mutex
	running map[string]bool
}

func (s *Scheduler) interval() time.Duration {
	if s.Interval > 0 {
		return s.Interval
	}
	return time.Minute
}

// Run ticks until ctx ends. A tick that fails is logged and the loop carries on: one bad tick must not stop every
// future job.
func (s *Scheduler) Run(ctx context.Context) {
	t := time.NewTicker(s.interval())
	defer t.Stop()
	for {
		if _, err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			slog.Error("cron tick failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

type due struct {
	job Job
}

func (s *Scheduler) claim(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running == nil {
		s.running = map[string]bool{}
	}
	if s.running[id] {
		return false
	}
	s.running[id] = true
	return true
}

func (s *Scheduler) release(id string) { s.mu.Lock(); delete(s.running, id); s.mu.Unlock() }

// Tick fires every due job once and returns how many ran. A recurring job's next run is advanced and saved BEFORE it
// is dispatched, so a crash mid-run costs at most that one run and never replays a burst; a one-shot stays put until
// it has run, so a crash retries it. A one-shot past its grace window is marked missed, not run.
func (s *Scheduler) Tick(ctx context.Context) (int, error) {
	if err := os.MkdirAll(s.Store.Dir, 0o700); err != nil {
		return 0, err
	}
	lf, err := os.OpenFile(filepath.Join(s.Store.Dir, ".tick.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return 0, err
	}
	defer lf.Close()
	unlock, ok := tryFlock(lf)
	if !ok {
		return 0, nil // another process is ticking this store
	}
	defer unlock()
	s.heartbeat()

	now := s.Store.now()
	var batch []due
	err = s.Store.Edit(func(jobs []Job) ([]Job, bool, error) {
		changed := false
		for i := range jobs {
			j := &jobs[i]
			next, has := j.Next()
			if !j.Runnable() || !has || next.After(now) {
				continue
			}
			lateness := now.Sub(next)
			switch j.Schedule.Kind {
			case KindOnce:
				if lateness > OneShotGrace {
					j.State, j.Enabled, j.NextRunAt = StateCompleted, false, ""
					j.LastStatus, j.LastError, j.LastRunAt = StatusMissed, "the one-shot's run window passed before it could run", OptStr(Iso(now))
					changed = true
					continue
				}
			default:
				grace := s.Store.Grace(j.Schedule)
				late := lateness > grace
				if late && !s.CatchUpMissed {
					slog.Warn("cron job missed past its grace; skipped", "job", j.ID, "late", lateness.Round(time.Second))
					j.LastStatus, j.LastError = StatusSkipped, "missed its run window; catch_up_missed is off"
					if n, ok := s.Store.NextRun(j.Schedule, "", now); ok {
						j.NextRunAt = OptStr(n)
					}
					changed = true
					continue
				}
				if n, ok := s.Store.NextRun(j.Schedule, "", now); ok {
					j.NextRunAt = OptStr(n)
					changed = true
				}
			}
			if s.claim(j.ID) {
				batch = append(batch, due{job: *j})
			}
		}
		return jobs, changed, nil
	})
	if err != nil {
		return 0, err
	}
	s.runBatch(ctx, batch, "")
	return len(batch), nil
}

func (s *Scheduler) runBatch(ctx context.Context, batch []due, transient string) {
	par := max(1, s.MaxParallel)
	sem := make(chan struct{}, par)
	var wg sync.WaitGroup
	for _, d := range batch {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			defer s.release(d.job.ID)
			s.execute(ctx, d.job, transient)
		}()
	}
	wg.Wait()
}

// RunNow runs one job immediately (an explicit run, whatever its state), blocking until it ends, and records it like
// a scheduled run. transient is context for this run only.
func (s *Scheduler) RunNow(ctx context.Context, id, transient string) (RunResult, error) {
	j, err := s.Store.Get(id)
	if err != nil {
		return RunResult{}, err
	}
	if !s.claim(id) {
		return RunResult{}, &ValidationError{fmt.Sprintf("job %s is already running", id)}
	}
	defer s.release(id)
	return s.execute(ctx, *j, transient), nil
}

func (s *Scheduler) execute(ctx context.Context, j Job, transient string) RunResult {
	res := s.Runner.Run(ctx, j, transient)
	status, errText := StatusOK, ""
	output := res.Output
	switch {
	case res.Err != nil:
		status, errText = StatusError, res.Err.Error()
	case strings.HasPrefix(strings.TrimSpace(output), FailureToken):
		status = StatusError
		errText = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(output), FailureToken))
		if errText == "" {
			errText = "the run reported a failure"
		}
	}
	path := s.saveOutput(j, status, output, errText)
	var deliveryErr string
	if status == StatusOK && strings.TrimSpace(output) != SilentToken && strings.TrimSpace(output) != "" {
		deliveryErr = s.deliver(ctx, j, output)
	} else if status == StatusError {
		deliveryErr = s.deliver(ctx, j, fmt.Sprintf("⚠️ Cron job '%s' failed: %s", j.Name, errText))
	}
	if deliveryErr != "" && status == StatusOK {
		status = StatusDeliveryFailed
	}
	if err := s.Store.MarkRun(j.ID, status, errText, deliveryErr); err != nil {
		slog.Error("cron job outcome could not be saved", "job", j.ID, "err", err, "output", path)
	}
	return res
}

// deliver sends text to the job's targets; "local" and an unavailable gateway only keep the saved file.
func (s *Scheduler) deliver(ctx context.Context, j Job, text string) string {
	if strings.TrimSpace(j.Deliver) == "local" || s.Deliver == nil {
		return ""
	}
	if err := s.Deliver.Deliver(ctx, j, text); err != nil {
		slog.Warn("cron delivery failed", "job", j.ID, "err", err)
		return err.Error()
	}
	return ""
}

// saveOutput writes the run's output under cron/output/<job>/; it returns the path ("" if it could not be written).
func (s *Scheduler) saveOutput(j Job, status, output, errText string) string {
	dir := s.Store.OutputDir(j.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		slog.Warn("cron output not saved", "job", j.ID, "err", err)
		return ""
	}
	now := s.Store.now()
	path := filepath.Join(dir, now.Format("2006-01-02_15-04-05")+".md")
	body := fmt.Sprintf("# Cron job: %s\n\n**Job ID:** %s\n**Run time:** %s\n**Schedule:** %s\n**Status:** %s\n\n", j.Name, j.ID, Iso(now), j.ScheduleDisplay, status)
	if errText != "" {
		body += "## Error\n\n" + errText + "\n\n"
	}
	body += "## Response\n\n" + output + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		slog.Warn("cron output not saved", "job", j.ID, "err", err)
		return ""
	}
	return path
}

// LastOutput is the response of a job's most recent saved run ("" when none): the context another job chains on.
func (s *Store) LastOutput(jobID string) string {
	entries, err := os.ReadDir(s.OutputDir(jobID))
	if err != nil || len(entries) == 0 {
		return ""
	}
	for i := len(entries) - 1; i >= 0; i-- {
		raw, err := os.ReadFile(filepath.Join(s.OutputDir(jobID), entries[i].Name()))
		if err != nil {
			continue
		}
		text := string(raw)
		if _, resp, ok := strings.Cut(text, "## Response\n\n"); ok {
			resp = strings.TrimSpace(resp)
			if resp != "" && resp != SilentToken {
				return resp
			}
		}
	}
	return ""
}

// MarkRun records a finished run: status fields, the failure streak, repeat.completed, and the next run (or the
// terminal completed state when the repeat limit is reached or a one-shot has run).
func (s *Store) MarkRun(id, status, errText, deliveryErr string) error {
	return s.with(id, func(j *Job) error {
		now := s.now()
		j.LastRunAt = OptStr(Iso(now))
		j.LastStatus = OptStr(status)
		j.LastError = OptStr(errText)
		j.LastDeliveryError = OptStr(deliveryErr)
		if status == StatusError {
			j.FailureStreak++
		} else {
			j.FailureStreak = 0
		}
		j.Repeat.Completed++
		if j.Repeat.Times != nil && j.Repeat.Completed >= *j.Repeat.Times {
			complete(j)
			return nil
		}
		next, ok := s.NextRun(j.Schedule, Iso(now), now)
		switch {
		case ok:
			j.NextRunAt = OptStr(next)
			if j.State != StatePaused {
				j.State = StateScheduled
			}
		case j.Schedule.Kind == KindOnce:
			complete(j)
		default:
			// A recurring job must never be silently disabled: flag it instead.
			j.State = StateError
			if j.LastError == "" {
				j.LastError = "Failed to compute the next run for a recurring schedule."
			}
		}
		return nil
	})
}

func complete(j *Job) {
	j.State, j.Enabled, j.NextRunAt = StateCompleted, false, ""
}

// heartbeat stamps the tick time for `hermes cron status`.
func (s *Scheduler) heartbeat() {
	_ = os.WriteFile(filepath.Join(s.Store.Dir, "ticker_heartbeat"), []byte(strconv.FormatInt(time.Now().Unix(), 10)), 0o600)
}

// HeartbeatAge is how long ago a ticker last ticked this store; ok is false when none ever did.
func (s *Store) HeartbeatAge() (time.Duration, bool) {
	raw, err := os.ReadFile(filepath.Join(s.Dir, "ticker_heartbeat"))
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return 0, false
	}
	return time.Since(time.Unix(n, 0)), true
}
