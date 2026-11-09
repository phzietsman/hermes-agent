// Package crontool offers cronjob_manage: the agent's way to schedule, inspect and change cron jobs.
package crontool

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/phzietsman/hermes-agent/internal/cron"
	"github.com/phzietsman/hermes-agent/internal/tools"
)

// Toolset is the toolset name; cron runs and delegated children are denied it (a scheduled run must not schedule
// more work from the recurring-sounding words in its own prompt).
const Toolset = "cron"

// Service is what the tool needs from the runtime.
type Service interface {
	Store() *cron.Store
	// RunAsync fires a job now in the background; the outcome is recorded like any run.
	RunAsync(jobID, transient string) error
}

const description = `Manage scheduled cron jobs: action='create' schedules a job from a prompt and/or skills; 'list' inspects jobs; 'update'/'pause'/'resume'/'remove' manage one by job_id (always list first — never guess job IDs); 'run' fires a job immediately in the BACKGROUND (returns a handle at once, the outcome is recorded when done — do not wait or poll; optional 'prompt' adds transient context for that fire only).

Jobs run on the main agent model unless pinned.

Jobs run in a fresh session with no current-chat context, so prompts must be self-contained, and the agent's FINAL RESPONSE is what gets delivered — cron runs are autonomous and cannot ask questions. Prefer updating an existing job over creating near-duplicates.`

const scheduleDoc = "REQUIRED for create. Schedule forms: (1) recurring interval — '30m', 'every 2h', 'every hour' (EVERY 30 minutes / 2 hours / hour, forever by default); (2) explicit one-shot by duration — 'in 30m', 'in 2h' (fires ONCE that far from now; use this for 'remind me in N minutes' — do NOT hand-compute an absolute timestamp); (3) natural day/time — 'every monday 9am', 'weekdays at 9am', 'every day at 9am' (recurring weekly/daily); (4) cron syntax — '0 9 * * *' (daily 9am); (5) absolute one-shot — ISO timestamp '2026-06-01T09:00:00'."

// Register adds the tool to r.
func Register(r *tools.Registry, svc Service) {
	str := func(d string) map[string]any { return map[string]any{"type": "string", "description": d} }
	list := func(d string) map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": d}
	}
	r.MustRegister(tools.Tool{
		Name: "cronjob_manage", Toolset: Toolset, Description: description,
		Parameters: map[string]any{"type": "object", "required": []any{"action"}, "properties": map[string]any{
			"action":           str("One of: create, list, update, pause, resume, remove, run. When action=create, the 'schedule' and 'prompt' fields are REQUIRED."),
			"job_id":           str("Required for update/pause/resume/remove/run."),
			"prompt":           str("For create: the full self-contained prompt (paired with any skills as the task instruction). For run: optional transient context for that single fire (never persisted)."),
			"schedule":         str(scheduleDoc),
			"name":             str("Optional human-friendly name"),
			"repeat":           map[string]any{"type": "integer", "description": "Optional repeat count. Omit for defaults (once for one-shot, forever for recurring)."},
			"deliver":          str("Where the job's output is delivered. Omit to address the chat this job was created from, or 'local' when created from the CLI. 'local' saves the output only, no delivery; otherwise platform:chat_id[:thread_id] (e.g. 'telegram:-1001234567890:17585')."),
			"skills":           list("Optional ordered skill names loaded before the cron prompt. On update, [] clears."),
			"context_from":     list("Optional job ID(s) whose most recent completed output is injected as context each run — chains jobs (A collects, B processes). Use 'self' for the job's own previous output. On update, [] clears."),
			"enabled_toolsets": list("Optional toolset names to restrict the job's agent to (e.g. [\"web\", \"terminal\"]) — cuts token overhead. Omit for all default tools."),
			"workdir":          str("Optional absolute existing path to run the job from: injects that directory's AGENTS.md/context files and anchors terminal/file tools there. On update, '' clears."),
			"paused":           map[string]any{"type": "boolean", "description": "Create only: persist disabled atomically. Resume to schedule; explicit run remains available. Default false."},
			"paused_reason":    str("Create only: auditable reason; requires paused=true."),
		}},
		Handler: func(ctx context.Context, a tools.Args) (string, error) { return handle(ctx, svc, a), nil },
	})
}

func fail(msg string) string { return tools.Error(msg, tools.Field{Key: "success", Value: false}) }

func optList(a tools.Args, key string) *[]string {
	if _, ok := a[key]; !ok {
		return nil
	}
	l := a.Strings(key)
	if l == nil {
		l = []string{}
	}
	return &l
}

func optStr(a tools.Args, key string) *string {
	s, ok := a[key].(string)
	if !ok {
		return nil
	}
	return &s
}

func handle(ctx context.Context, svc Service, a tools.Args) string {
	st := svc.Store()
	action := strings.ToLower(strings.TrimSpace(a.String("action", "")))
	id := strings.TrimSpace(a.String("job_id", ""))
	needID := func() string {
		if id == "" {
			return fail(fmt.Sprintf("job_id is required for action '%s'. Use action='list' to see job ids.", action))
		}
		return ""
	}
	switch action {
	case "create":
		return create(ctx, st, a)
	case "list":
		jobs, err := st.List()
		if err != nil {
			return fail(err.Error())
		}
		rows := make([]map[string]any, 0, len(jobs))
		for _, j := range jobs {
			rows = append(rows, summary(j))
		}
		return tools.Result(tools.Field{Key: "success", Value: true}, tools.Field{Key: "count", Value: len(rows)}, tools.Field{Key: "jobs", Value: rows})
	case "update":
		if m := needID(); m != "" {
			return m
		}
		var repeat *int
		if _, ok := a["repeat"]; ok {
			n := a.Int("repeat", 0)
			repeat = &n
		}
		u := cron.UpdateOptions{Prompt: optStr(a, "prompt"), Schedule: optStr(a, "schedule"), Name: optStr(a, "name"), Deliver: optStr(a, "deliver"),
			Workdir: optStr(a, "workdir"), Skills: optList(a, "skills"), ContextFrom: optList(a, "context_from"), EnabledToolsets: optList(a, "enabled_toolsets"), Repeat: repeat}
		j, err := st.Update(id, u)
		if err != nil {
			return failErr(err)
		}
		return tools.Result(tools.Field{Key: "success", Value: true}, tools.Field{Key: "message", Value: fmt.Sprintf("Job '%s' updated.", j.Name)}, tools.Field{Key: "job", Value: summary(*j)})
	case "pause":
		if m := needID(); m != "" {
			return m
		}
		if err := st.Pause(id, a.String("paused_reason", "")); err != nil {
			return failErr(err)
		}
		return tools.Result(tools.Field{Key: "success", Value: true}, tools.Field{Key: "message", Value: fmt.Sprintf("Job %s paused.", id)})
	case "resume":
		if m := needID(); m != "" {
			return m
		}
		j, err := st.Resume(id)
		if err != nil {
			return failErr(err)
		}
		return tools.Result(tools.Field{Key: "success", Value: true}, tools.Field{Key: "message", Value: fmt.Sprintf("Job '%s' resumed.", j.Name)}, tools.Field{Key: "next_run_at", Value: nullable(string(j.NextRunAt))})
	case "remove":
		if m := needID(); m != "" {
			return m
		}
		if err := st.Remove(id); err != nil {
			return failErr(err)
		}
		return tools.Result(tools.Field{Key: "success", Value: true}, tools.Field{Key: "message", Value: fmt.Sprintf("Job %s removed.", id)})
	case "run":
		if m := needID(); m != "" {
			return m
		}
		if msg := cron.ScanPrompt(a.String("prompt", "")); msg != "" {
			return fail(msg)
		}
		if err := svc.RunAsync(id, a.String("prompt", "")); err != nil {
			return failErr(err)
		}
		return tools.Result(tools.Field{Key: "success", Value: true}, tools.Field{Key: "job_id", Value: id}, tools.Field{Key: "status", Value: "started"},
			tools.Field{Key: "message", Value: "The job is running in the background; its outcome is recorded when it finishes. Do not wait or poll for it."})
	}
	return fail(fmt.Sprintf("Unknown action '%s'. Use one of: create, list, update, pause, resume, remove, run.", action))
}

func failErr(err error) string {
	var ve *cron.ValidationError
	if errors.As(err, &ve) || errors.Is(err, cron.ErrNotFound) {
		return fail(err.Error())
	}
	return fail("cron store error: " + err.Error())
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func create(ctx context.Context, st *cron.Store, a tools.Args) string {
	if strings.TrimSpace(a.String("schedule", "")) == "" {
		return fail("schedule is required for create.")
	}
	var repeat *int
	if _, ok := a["repeat"]; ok {
		n := a.Int("repeat", 0)
		repeat = &n
	}
	env := tools.Env(ctx)
	var origin *cron.Origin
	if env.Platform != "" && env.ChatID != "" {
		origin = &cron.Origin{Platform: env.Platform, ChatID: env.ChatID, ThreadID: env.ThreadID}
	}
	j, err := st.Create(cron.CreateOptions{
		Prompt: a.String("prompt", ""), Schedule: a.String("schedule", ""), Name: a.String("name", ""), Repeat: repeat, Deliver: a.String("deliver", ""),
		Origin: origin, Skills: a.Strings("skills"), ContextFrom: a.Strings("context_from"), EnabledToolsets: a.Strings("enabled_toolsets"),
		Workdir: a.String("workdir", ""), Paused: a.Bool("paused", false), PausedReason: a.String("paused_reason", ""),
	})
	if err != nil {
		return failErr(err)
	}
	return tools.Result(tools.Field{Key: "success", Value: true}, tools.Field{Key: "job_id", Value: j.ID}, tools.Field{Key: "name", Value: j.Name},
		tools.Field{Key: "schedule", Value: j.ScheduleDisplay}, tools.Field{Key: "repeat", Value: repeatText(j.Repeat)}, tools.Field{Key: "deliver", Value: j.Deliver},
		tools.Field{Key: "next_run_at", Value: nullable(string(j.NextRunAt))}, tools.Field{Key: "message", Value: fmt.Sprintf("Cron job '%s' created.", j.Name)})
}

func repeatText(r cron.Repeat) string {
	if r.Times == nil {
		return "forever"
	}
	if *r.Times == 1 {
		return "once"
	}
	return fmt.Sprintf("%d times", *r.Times)
}

func preview(s string) string {
	if utf8.RuneCountInString(s) > 100 {
		return string([]rune(s)[:100]) + "…"
	}
	return s
}

func summary(j cron.Job) map[string]any {
	return map[string]any{
		"job_id": j.ID, "name": j.Name, "prompt_preview": preview(j.Prompt), "skills": j.Skills, "schedule": j.ScheduleDisplay,
		"repeat": repeatText(j.Repeat), "deliver": j.Deliver, "next_run_at": nullable(string(j.NextRunAt)), "last_run_at": nullable(string(j.LastRunAt)),
		"last_status": nullable(string(j.LastStatus)), "last_error": nullable(string(j.LastError)), "enabled": j.Enabled, "state": j.State,
		"workdir": nullable(string(j.Workdir)), "context_from": j.ContextFrom,
	}
}
