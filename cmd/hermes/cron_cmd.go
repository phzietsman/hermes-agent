package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/phzietsman/hermes-agent/internal/app"
	"github.com/phzietsman/hermes-agent/internal/config"
	"github.com/phzietsman/hermes-agent/internal/cron"
)

const cronUsage = `usage: hermes cron <command>

commands:
  list                         show every job
  add <schedule> <prompt...>   create a job (flags: -name -deliver -skill -repeat -workdir -paused)
  edit <id>                    change a job (flags: -schedule -prompt -name -deliver -workdir)
  pause <id> | resume <id> | remove <id>
  run <id> [-prompt text]      run a job now and print its output
  tick                         run every due job once
  daemon                       tick every minute until interrupted
  status                       show whether a ticker is running and when jobs next fire

schedules: "30m", "every 2h", "in 20m", "every monday 9am", "weekdays at 9am", "0 9 * * *", "2026-06-01T09:00:00"
`

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

// cronStore opens only the store (no model credential needed to list or edit jobs).
func cronStore(ctx context.Context, profile string) (*cron.Store, error) {
	home, err := profileHome(ctx, profile)
	if err != nil {
		return nil, err
	}
	st := cron.NewStore(home)
	if cfg, err := config.Load(home, config.LoadSecrets(home)); err == nil {
		if name := cfg.String("timezone", ""); name != "" {
			if loc, err := time.LoadLocation(name); err == nil {
				st.Loc = loc
			}
		}
	}
	return st, nil
}

func cronCmd(ctx context.Context, args []string, io stdio, opts app.Options) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "help" {
		fmt.Fprint(io.err, cronUsage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	name, rest := args[0], args[1:]
	fs := newFlags("cron "+name, io)
	profile := fs.String("profile", "", "profile name (default: the ambient home)")
	var (
		cname, deliver, workdir, reason, prompt string
		skills                                  listFlag
		repeat                                  int
		paused                                  bool
	)
	switch name {
	case "add":
		fs.StringVar(&cname, "name", "", "job name")
		fs.StringVar(&deliver, "deliver", "", "delivery target (default local)")
		fs.StringVar(&workdir, "workdir", "", "absolute working directory")
		fs.Var(&skills, "skill", "skill to load first (repeatable)")
		fs.IntVar(&repeat, "repeat", 0, "run this many times (default: once for a one-shot, forever otherwise)")
		fs.BoolVar(&paused, "paused", false, "create paused")
	case "edit":
		fs.String("schedule", "", "new schedule")
		fs.String("prompt", "", "new prompt")
		fs.String("name", "", "new name")
		fs.String("deliver", "", "new delivery target")
		fs.String("workdir", "", "new working directory")
	case "run":
		fs.StringVar(&prompt, "prompt", "", "transient context for this run only")
	case "pause":
		fs.StringVar(&reason, "reason", "", "why")
	}
	if err := fs.Parse(reorder(rest)); err != nil {
		return 2
	}
	pos := fs.Args()
	st, err := cronStore(ctx, *profile)
	if err != nil {
		return fatal(io.err, err)
	}
	needID := func() (string, bool) {
		if len(pos) != 1 {
			fmt.Fprintf(io.err, "hermes cron %s: expected one job id\n", name)
			return "", false
		}
		return pos[0], true
	}
	switch name {
	case "list":
		return cronList(st, io)
	case "add":
		if len(pos) < 2 {
			fmt.Fprintln(io.err, "usage: hermes cron add [flags] <schedule> <prompt...>")
			return 2
		}
		o := cron.CreateOptions{Schedule: pos[0], Prompt: strings.Join(pos[1:], " "), Name: cname, Deliver: deliver, Workdir: workdir, Skills: skills, Paused: paused}
		if repeat != 0 {
			o.Repeat = &repeat
		}
		j, err := st.Create(o)
		if err != nil {
			return fatal(io.err, err)
		}
		fmt.Fprintf(io.out, "created %s (%s) — %s, next run %s\n", j.ID, j.Name, j.ScheduleDisplay, orDash(string(j.NextRunAt)))
		return 0
	case "edit":
		id, ok := needID()
		if !ok {
			return 2
		}
		u := cron.UpdateOptions{}
		fs.Visit(func(f *flag.Flag) {
			v := f.Value.String()
			switch f.Name {
			case "schedule":
				u.Schedule = &v
			case "prompt":
				u.Prompt = &v
			case "name":
				u.Name = &v
			case "deliver":
				u.Deliver = &v
			case "workdir":
				u.Workdir = &v
			}
		})
		j, err := st.Update(id, u)
		if err != nil {
			return fatal(io.err, err)
		}
		fmt.Fprintf(io.out, "updated %s (%s) — %s, next run %s\n", j.ID, j.Name, j.ScheduleDisplay, orDash(string(j.NextRunAt)))
		return 0
	case "pause", "resume", "remove":
		id, ok := needID()
		if !ok {
			return 2
		}
		switch name {
		case "pause":
			err = st.Pause(id, reason)
		case "resume":
			_, err = st.Resume(id)
		default:
			err = st.Remove(id)
		}
		if err != nil {
			return fatal(io.err, err)
		}
		fmt.Fprintf(io.out, "%s %s\n", map[string]string{"pause": "paused", "resume": "resumed", "remove": "removed"}[name], id)
		return 0
	case "status":
		return cronStatus(st, io)
	case "run", "tick", "daemon":
		return cronRun(ctx, name, pos, prompt, *profile, io, opts)
	}
	fmt.Fprintf(io.err, "hermes cron: unknown command %q\n\n%s", name, cronUsage)
	return 2
}

// reorder moves flags ahead of positional arguments so `add "every 2h" hello -name x` works like `add -name x ...`.
func reorder(args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case strings.HasPrefix(a, "-") && len(a) > 1 && !strings.Contains(a, "="):
			flags = append(flags, a)
			if name := strings.TrimLeft(a, "-"); name != "paused" && name != "h" && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		case strings.HasPrefix(a, "-") && len(a) > 1:
			flags = append(flags, a)
		default:
			pos = append(pos, a)
		}
	}
	return append(flags, pos...)
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func cronList(st *cron.Store, io stdio) int {
	jobs, err := st.List()
	if err != nil {
		return fatal(io.err, err)
	}
	if len(jobs) == 0 {
		fmt.Fprintln(io.out, "no cron jobs")
		return 0
	}
	for _, j := range jobs {
		fmt.Fprintf(io.out, "%s  %-9s %-24s %-22s next %-27s last %s\n", j.ID, j.State, trunc(j.Name, 24), trunc(j.ScheduleDisplay, 22), orDash(string(j.NextRunAt)), orDash(string(j.LastStatus)))
	}
	return 0
}

func trunc(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func cronStatus(st *cron.Store, io stdio) int {
	if age, ok := st.HeartbeatAge(); ok && age < 3*time.Minute {
		fmt.Fprintf(io.out, "ticker: running (last tick %s ago)\n", age.Round(time.Second))
	} else if ok {
		fmt.Fprintf(io.out, "ticker: NOT running (last tick %s ago): jobs will not fire until `hermes gateway` or `hermes cron daemon` runs\n", age.Round(time.Second))
	} else {
		fmt.Fprintln(io.out, "ticker: never ran: jobs will not fire until `hermes gateway` or `hermes cron daemon` runs")
	}
	jobs, _ := st.List()
	active := 0
	var next string
	for _, j := range jobs {
		if j.Runnable() {
			active++
			if n := string(j.NextRunAt); next == "" || n < next {
				next = n
			}
		}
	}
	fmt.Fprintf(io.out, "jobs: %d active of %d; next fire %s\n", active, len(jobs), orDash(next))
	return 0
}

func cronRun(ctx context.Context, name string, pos []string, prompt, profile string, io stdio, opts app.Options) int {
	home, err := profileHome(ctx, profile)
	if err != nil {
		return fatal(io.err, err)
	}
	opts.LongRunning = true
	rt, err := app.Open(config.WithHome(ctx, home), home, opts)
	if err != nil {
		return fatal(io.err, err)
	}
	defer rt.Close()
	sched := rt.Cron().Scheduler()
	switch name {
	case "run":
		if len(pos) != 1 {
			fmt.Fprintln(io.err, "hermes cron run: expected one job id")
			return 2
		}
		res, err := sched.RunNow(ctx, pos[0], prompt)
		if err != nil {
			return fatal(io.err, err)
		}
		fmt.Fprintln(io.out, res.Output)
		if res.Err != nil {
			fmt.Fprintf(io.err, "run failed: %v\n", res.Err)
			return 1
		}
		return 0
	case "tick":
		n, err := sched.Tick(ctx)
		if err != nil {
			return fatal(io.err, err)
		}
		fmt.Fprintf(io.out, "ran %d job(s)\n", n)
		return 0
	}
	fmt.Fprintln(io.err, "cron daemon: ticking every minute (Ctrl-C to stop)")
	sched.Run(ctx)
	if errors.Is(ctx.Err(), context.Canceled) {
		return 0
	}
	return 0
}
