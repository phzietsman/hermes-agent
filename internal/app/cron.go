package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/phzietsman/hermes-agent/internal/agent"
	"github.com/phzietsman/hermes-agent/internal/cron"
	"github.com/phzietsman/hermes-agent/internal/llm"
	"github.com/phzietsman/hermes-agent/internal/skills"
	"github.com/phzietsman/hermes-agent/internal/tools"
	"github.com/phzietsman/hermes-agent/internal/tools/crontool"
)

// defaultCronInactivity is how long a cron run may sit idle (no model text, no tool call) before it is cut off.
const defaultCronInactivity = 600 * time.Second

// CronService is the profile's cron: its store, its scheduler and the runner that fires jobs through the agent.
type CronService struct {
	rt    *Runtime
	store *cron.Store
	sched *cron.Scheduler
}

// Cron returns the profile's cron service.
func (rt *Runtime) Cron() *CronService {
	rt.cronOnce.Do(func() {
		loc := time.Local
		if name := rt.Config.String("timezone", ""); name != "" {
			if l, err := time.LoadLocation(name); err == nil {
				loc = l
			} else {
				slog.Warn("timezone not recognised; using the system zone", "timezone", name, "err", err)
			}
		}
		st := cron.NewStore(rt.Home)
		st.Loc = loc
		c := &CronService{rt: rt, store: st}
		c.sched = &cron.Scheduler{Store: st, Runner: cronRunner{rt}, Deliver: rt.CronDeliverer,
			Interval:      time.Minute,
			MaxParallel:   rt.Config.Int("cron.max_parallel_jobs", 1),
			CatchUpMissed: rt.Config.Bool("cron.catch_up_missed", true)}
		rt.cron = c
	})
	return rt.cron
}

// Store implements crontool.Service.
func (c *CronService) Store() *cron.Store { return c.store }

// Scheduler is the tick loop (the gateway or `hermes cron daemon` runs it).
func (c *CronService) Scheduler() *cron.Scheduler { return c.sched }

// RunAsync implements crontool.Service: the run is detached from the caller's turn.
func (c *CronService) RunAsync(jobID, transient string) error {
	if _, err := c.store.Get(jobID); err != nil {
		return err
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("cron run crashed", "job", jobID, "panic", r)
			}
		}()
		if _, err := c.sched.RunNow(context.Background(), jobID, transient); err != nil {
			slog.Warn("cron run not started", "job", jobID, "err", err)
		}
	}()
	return nil
}

var _ crontool.Service = (*CronService)(nil)

// cronRunner runs a job in a fresh read-only session: a scheduled run is not the user's conversation, so it never
// writes memory and never reviews itself, and it cannot schedule more jobs.
type cronRunner struct{ rt *Runtime }

func (r cronRunner) Run(ctx context.Context, j cron.Job, transient string) cron.RunResult {
	rt := r.rt
	st := skills.LoadSettings(rt.Home, rt.Config, "")
	src := cron.PromptSource{
		Skill: func(name string) (string, bool) {
			for _, s := range skills.Scan(st.Roots, skills.HostFilter()) {
				if s.Status != skills.StatusShadowed && (s.LoadName == name || s.Name == name) {
					raw, err := os.ReadFile(s.Path)
					return string(raw), err == nil
				}
			}
			return "", false
		},
		LastOutput: rt.Cron().store.LastOutput,
	}
	prompt := cron.BuildPrompt(j, transient, src)

	idle := time.Duration(rt.Config.Int("cron.inactivity_timeout", int(defaultCronInactivity/time.Second))) * time.Second
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var timedOut bool
	var mu sync.Mutex
	var timer *time.Timer
	touch := func() {}
	if idle > 0 {
		timer = time.AfterFunc(idle, func() { mu.Lock(); timedOut = true; mu.Unlock(); cancel() })
		defer timer.Stop()
		touch = func() { timer.Reset(idle) }
	}
	ev := agent.Events{
		Text:      func(string) { touch() },
		ToolStart: func(llm.ToolCall) { touch() },
		ToolDone:  func(llm.ToolCall, string, bool) { touch() },
	}
	cwd := string(j.Workdir)
	sel := tools.Selection{ExcludeToolsets: []string{crontool.Toolset}}
	if len(j.EnabledToolsets) > 0 {
		sel.Toolsets = j.EnabledToolsets
	}
	sess, err := rt.NewSession(runCtx, SessionOptions{Source: "cron", Platform: "cron", CWD: cwd, ReadOnly: true, Selection: sel, Events: ev})
	if err != nil {
		return cron.RunResult{Err: fmt.Errorf("could not start the run: %w", err)}
	}
	defer sess.Close()
	res, err := sess.Agent.RunTurn(runCtx, prompt)
	mu.Lock()
	idled := timedOut
	mu.Unlock()
	switch {
	case idled:
		return cron.RunResult{Output: res.FinalResponse, Err: fmt.Errorf("the run was idle for %s and was stopped", idle)}
	case err != nil:
		return cron.RunResult{Err: err}
	case res.Interrupted:
		return cron.RunResult{Err: fmt.Errorf("the run was interrupted")}
	case !res.Completed:
		msg := res.Error
		if msg == "" {
			msg = "the run did not complete"
		}
		return cron.RunResult{Output: res.FinalResponse, Err: fmt.Errorf("%s", msg)}
	}
	return cron.RunResult{Output: strings.TrimSpace(res.FinalResponse)}
}

// OutputPath is where a job's saved outputs live, for display.
func (c *CronService) OutputPath(id string) string { return filepath.Join(c.store.Dir, "output", id) }
