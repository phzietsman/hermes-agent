// Package delegate offers delegate_task: run focused subagents in fresh sessions and return their summaries.
// Children are leaves: they never get delegation, memory, scheduling or user-interaction tools (a child writing
// shared memory or spawning more work in the parent's name is the thing the restriction exists to stop).
package delegate

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/phzietsman/hermes-agent/internal/agent"
	"github.com/phzietsman/hermes-agent/internal/tools"
)

// Toolsets and tools a child never receives. "memory" owns no tool of its own (it only gates the memory
// provider's tools), so it must be named: deriving the list from "all tools blocked" stopped matching it once
// (the Python bug fixed in ce4e7f5417).
var (
	blockedToolsets = []string{"delegation", "memory", "cron", "kanban"}
	blockedTools    = []string{"delegate_task", "clarify", "cronjob", "send_message"}
)

// ChildSelection narrows a parent's selection to what a child may use.
func ChildSelection(parent tools.Selection) tools.Selection {
	return tools.Selection{
		Toolsets:        parent.Toolsets,
		Exclude:         append(append([]string(nil), parent.Exclude...), blockedTools...),
		ExcludeToolsets: append(append([]string(nil), parent.ExcludeToolsets...), blockedToolsets...),
	}
}

// Factory builds a child agent in a fresh session whose parent is parentSession, with system as its system
// prompt and its tools taken from ChildSelection.
type Factory func(ctx context.Context, parentSession, system string) (*agent.Agent, error)

// DefaultMaxChildren is how many subagents one call may run in parallel.
const DefaultMaxChildren = 3

const completionInstructions = "\nComplete this task using the tools available to you. When finished, provide a clear, concise summary of:\n" +
	"- What you did\n- What you found or accomplished\n- Any files you created or modified\n- Any issues encountered\n\n" +
	"Important workspace rule: Never assume a repository lives at /workspace/... or any other container-style path " +
	"unless the task/context explicitly gives that path. If no exact local path is provided, discover it first before " +
	"issuing git/workdir-specific commands.\n\n" +
	"Keep your final summary tight: lead with outcomes, prefer bullet points over paragraphs, and don't replay your " +
	"whole process. Your response is returned to the parent agent as a summary, and overlong summaries crowd out the " +
	"parent's context window."

// ChildSystemPrompt is the focused system prompt of a child. The goal is the child's first user turn, not part
// of this text.
func ChildSystemPrompt(context, workdir string) string {
	parts := []string{"You are a focused subagent working on a specific delegated task."}
	if strings.TrimSpace(context) != "" {
		parts = append(parts, "\nCONTEXT:\n"+context)
	}
	if strings.TrimSpace(workdir) != "" {
		parts = append(parts, "\nWORKSPACE PATH:\n"+workdir+"\nUse this exact path for local repository/workdir operations unless the task explicitly says otherwise.")
	}
	parts = append(parts, completionInstructions)
	return strings.Join(parts, "\n")
}

func description(max int) string {
	return "Spawn subagents in isolated contexts; each gets its own conversation, terminal session, and toolset, and only its final summary returns to you. " +
		"Pass every task in `tasks` — one entry spawns one subagent, several run in parallel (limit in the tasks description). The call returns when all children finish.\n\n" +
		"USE FOR: reasoning-heavy subtasks, work that would flood your context, or independent parallel workstreams.\n" +
		"DO NOT USE FOR (use these instead):\n" +
		"- A single tool call -> call the tool directly\n" +
		"- Tasks needing user interaction -> subagents cannot ask questions\n" +
		"- Durable work that must survive this session -> cronjob; /stop, /new, or process exit halts running subagents, each returning an 'interrupted' result with partial output.\n\n" +
		"RULES:\n" +
		"- Children know nothing of this conversation: pass everything needed via 'context', including any required output language, tone, or style.\n" +
		"- Child summaries are SELF-REPORTS, not verified facts: a child claiming \"uploaded successfully\" or \"file written\" may be wrong. For external side effects (uploads, remote writes, publishing), require a verifiable handle (URL, ID, absolute path) and verify it yourself before telling the user the operation succeeded.\n" +
		"- Children cannot call delegate_task, clarify, memory, or cronjob.\n" +
		"- Children inherit the parent model."
}

// Register adds delegate_task to r. maxChildren <= 0 means DefaultMaxChildren; it is baked into the tool text,
// which is fixed for the session.
func Register(r *tools.Registry, f Factory, maxChildren int) {
	if maxChildren <= 0 {
		maxChildren = DefaultMaxChildren
	}
	str := func(d string) map[string]any { return map[string]any{"type": "string", "description": d} }
	r.MustRegister(tools.Tool{
		Name: "delegate_task", Toolset: "delegation", Description: description(maxChildren),
		MaxResultChars: 200_000,
		Parameters: map[string]any{"type": "object", "required": []any{"tasks"}, "properties": map[string]any{
			"tasks": map[string]any{"type": "array", "minItems": 1,
				"description": fmt.Sprintf("The task(s), up to %d in parallel for this user (set via delegation.max_concurrent_children). Each entry spawns one subagent with isolated context and terminal session; a single task is a one-entry array. Required when spawning.", maxChildren),
				"items": map[string]any{"type": "object", "required": []any{"goal"}, "properties": map[string]any{
					"goal":    str("What this subagent should accomplish. Be specific and self-contained — it knows nothing about your conversation history."),
					"context": str("Background THIS child needs: file paths, error messages, constraints. Each child sees only its own context — repeat shared background in every task that needs it."),
				}}}}},
		Handler: func(ctx context.Context, a tools.Args) (string, error) { return run(ctx, f, maxChildren, a), nil },
	})
}

type task struct{ goal, context string }

func parseTasks(a tools.Args) ([]task, string) {
	raw, _ := a["tasks"].([]any)
	if len(raw) == 0 {
		// The single-goal shape is accepted from old transcripts, as in Python.
		if g := strings.TrimSpace(a.String("goal", "")); g != "" {
			return []task{{g, a.String("context", "")}}, ""
		}
		return nil, "Provide `tasks`: a non-empty array of {goal, context}."
	}
	out := make([]task, len(raw))
	for i, x := range raw {
		m, _ := x.(map[string]any)
		g, _ := m["goal"].(string)
		if strings.TrimSpace(g) == "" {
			return nil, fmt.Sprintf("Task %d is missing a goal.", i)
		}
		c, _ := m["context"].(string)
		out[i] = task{g, c}
	}
	return out, ""
}

func run(ctx context.Context, f Factory, max int, a tools.Args) string {
	ts, msg := parseTasks(a)
	if msg != "" {
		return tools.Error(msg)
	}
	if len(ts) > max {
		return tools.Error(fmt.Sprintf("Too many tasks: %d provided, but max_concurrent_children is %d. Either reduce the task count, split into multiple delegate_task calls, or increase delegation.max_concurrent_children in config.yaml.", len(ts), max))
	}
	env := tools.Env(ctx)
	start := time.Now()
	results := make([]map[string]any, len(ts))
	var wg sync.WaitGroup
	for i, t := range ts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = runChild(ctx, f, env, i, t)
		}()
	}
	wg.Wait()
	return tools.Result(tools.Field{Key: "results", Value: results},
		tools.Field{Key: "total_duration_seconds", Value: round2(time.Since(start).Seconds())})
}

func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }

func runChild(ctx context.Context, f Factory, env tools.CallEnv, i int, t task) (out map[string]any) {
	begin := time.Now()
	res := map[string]any{"task_index": i, "status": "failed", "summary": nil, "api_calls": 0}
	defer func() {
		if r := recover(); r != nil {
			res["error"] = fmt.Sprintf("subagent crashed: %v", r)
		}
		res["duration_seconds"] = round2(time.Since(begin).Seconds())
		out = res
	}()
	child, err := f(ctx, env.SessionID, ChildSystemPrompt(t.context, env.CWD))
	if err != nil {
		res["error"] = "could not start subagent: " + err.Error()
		return res
	}
	tr, err := child.RunTurn(ctx, t.goal)
	res["api_calls"] = tr.APICalls
	switch {
	case ctx.Err() != nil || tr.Interrupted:
		res["status"] = "interrupted"
		res["summary"] = tr.FinalResponse
	case err != nil:
		res["error"] = err.Error()
	case tr.Completed:
		res["status"] = "completed"
		res["summary"] = tr.FinalResponse
	default:
		res["summary"] = tr.FinalResponse
		res["error"] = tr.Error
	}
	return res
}
