package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/phzietsman/hermes-agent/internal/agent"
	"github.com/phzietsman/hermes-agent/internal/approval"
	"github.com/phzietsman/hermes-agent/internal/config"
	"github.com/phzietsman/hermes-agent/internal/learning"
	"github.com/phzietsman/hermes-agent/internal/learning/prompttext"
	"github.com/phzietsman/hermes-agent/internal/llm"
	"github.com/phzietsman/hermes-agent/internal/session"
	"github.com/phzietsman/hermes-agent/internal/tools"
)

const reviewMaxIterations = 16

// reviewWhitelist is what a review fork may call. Dispatch-side only: the advertised tools array stays identical
// to the parent's so the fork reads the same cached prefix. Memory tools are admitted only when memory is
// configured, and only the non-destructive pair (a review can save and look up, never forget). Read-only file
// access lets it inspect a skill before patching; every write goes through skill_manage's validation.
var reviewWhitelist = map[string]bool{
	"skills_list": true, "skill_view": true, "skill_manage": true, "read_file": true,
	"agent_memory_remember": true, "agent_memory_recall": true,
}

// wroteTools are the calls that count as the agent having saved something itself.
var wroteTools = map[string]bool{"agent_memory_remember": true, "skill_manage": true}

func (rt *Runtime) learningSettings() learning.Settings {
	c := rt.Config
	s := learning.DefaultSettings()
	// The old turn-based memory.nudge_interval and skills.creation_nudge_interval are aliases for the iteration
	// interval (skills wins); learning.* overrides both.
	s.IterationInterval = c.Int("skills.creation_nudge_interval", c.Int("memory.nudge_interval", s.IterationInterval))
	s.IterationInterval = max(0, c.Int("learning.iteration_interval", s.IterationInterval))
	s.IdleSeconds = max(0, c.Int("learning.idle_seconds", s.IdleSeconds))
	s.MaxAttempts = max(1, c.Int("learning.max_attempts", s.MaxAttempts))
	s.BackoffBase = time.Duration(c.Float("learning.backoff_base_s", s.BackoffBase.Seconds()) * float64(time.Second))
	s.ReviewOnBoundary = c.Bool("learning.review_on_boundary", s.ReviewOnBoundary)
	return s
}

// reviewsEnabled follows Python's fail-open default: auxiliary.background_review.enabled, true unless set false.
func (rt *Runtime) reviewsEnabled() bool {
	return rt.Config.Bool("auxiliary.background_review.enabled", true)
}

func (rt *Runtime) reviewQueue(ctx context.Context) (learning.Queue, error) {
	rt.queueOnce.Do(func() { rt.queue, rt.queueErr = learning.NewSQLQueue(ctx, rt.Store.DB()) })
	return rt.queue, rt.queueErr
}

// newLearning builds a session's loop, or nil when reviews cannot run for it.
func (rt *Runtime) newLearning(ctx context.Context, sessionID string, readOnly bool) *learningHook {
	if !rt.reviewsEnabled() || readOnly {
		return nil
	}
	q, err := rt.reviewQueue(context.WithoutCancel(ctx))
	if err != nil {
		slog.Warn("learning loop disabled: review queue unavailable", "err", err)
		return nil
	}
	host := &reviewHost{rt: rt, parent: sessionID}
	return &learningHook{loop: learning.NewLoop(q, rt.learningSettings(), host, nil), rt: rt}
}

// learningHook adapts the loop to the agent's Learning interface.
type learningHook struct {
	loop *learning.Loop
	rt   *Runtime
}

func (h *learningHook) OnToolIteration(session string, history []llm.Message, names []string) {
	wrote := false
	for _, n := range names {
		wrote = wrote || wroteTools[n]
	}
	h.loop.OnToolIteration(session, history, wrote)
}

func (h *learningHook) OnTurnEnd(session string, history []llm.Message) {
	h.loop.OnTurnEnd(session, history)
}

func (h *learningHook) OnPreCompress(session string, history []llm.Message) {
	h.loop.OnBoundary(session, learning.TriggerPreCompress, history, true)
}

// reviewHost runs review forks for one session's loop.
type reviewHost struct {
	rt     *Runtime
	parent string
}

func (h *reviewHost) LoadMessages(ctx context.Context, sessionID string) []llm.Message {
	rows, err := h.rt.Store.Messages(ctx, sessionID, session.ListOptions{})
	if err != nil {
		return nil
	}
	return agent.FromRows(rows)
}

func (h *reviewHost) SpawnReview(ctx context.Context, msgs []llm.Message, onOutcome func(learning.Outcome)) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("review fork crashed", "panic", r)
				onOutcome(learning.Failed)
			}
		}()
		onOutcome(h.run(ctx, msgs))
	}()
}

func (h *reviewHost) prompt() string {
	if h.rt.Memory != nil {
		return prompttext.P.Combined
	}
	return prompttext.P.Skills
}

func (h *reviewHost) run(ctx context.Context, msgs []llm.Message) learning.Outcome {
	rt := h.rt
	id := NewSessionID(time.Now())
	if err := rt.Store.CreateSession(ctx, session.NewSession{ID: id, Source: "review", Model: rt.Model, ParentSessionID: h.parent,
		ProfileName: config.ProfileName(rt.Home)}); err != nil {
		slog.Warn("review fork not started", "err", err)
		return learning.Failed
	}
	// The fork reads its parent's stored system prompt, byte for byte, so it hits the same cached prefix.
	system := ""
	if p, err := rt.Store.GetSession(ctx, h.parent); err == nil {
		system = p.SystemPrompt
	}
	box := rt.Registry.Build(tools.Selection{})
	var inner agent.ToolRunner = &envTools{Toolbox: box, env: tools.CallEnv{Home: rt.Home, SessionID: id, Gate: rt.Gate, Approval: approval.Check{SessionID: id}}}
	if rt.Memory != nil {
		defer rt.Memory.Drop(id)
		rt.Memory.ForFork(ctx, id)
	}
	runner := &reviewTools{inner: inner}
	cfg := rt.agentConfig(id, runner, box, "", "", "", agent.Events{})
	cfg.Budget = agent.NewIterationBudget(rt.Config.Int("learning.max_iterations", reviewMaxIterations))
	cfg.Seed = msgs
	cfg.Prompt = func() agent.PromptParts { return agent.PromptParts{Stable: system} }
	res, err := agent.New(cfg).RunTurn(ctx, h.prompt())
	switch {
	case ctx.Err() != nil || res.Interrupted:
		return learning.Interrupted
	case err != nil:
		slog.Warn("review fork failed", "err", err)
		return learning.Failed
	case res.Completed || res.Partial:
		return learning.Done
	}
	return learning.Failed
}

// reviewTools refuses everything outside the review whitelist, and skill deletion: a review never deletes.
type reviewTools struct{ inner agent.ToolRunner }

func (r *reviewTools) Defs() []llm.ToolDef           { return r.inner.Defs() }
func (r *reviewTools) ParallelSafe(name string) bool { return r.inner.ParallelSafe(name) }

func (r *reviewTools) Run(ctx context.Context, call llm.ToolCall) (string, error) {
	name := call.Function.Name
	if !reviewWhitelist[name] {
		return "", fmt.Errorf("tool %q is not available in a background review. Save with agent_memory_remember or skill_manage; read with skill_view, skills_list or read_file", name)
	}
	if name == "skill_manage" && strings.Contains(call.Function.Arguments, `"delete"`) {
		return "", fmt.Errorf("a background review never deletes a skill: note overlap in your reply and let the curator consolidate")
	}
	return r.inner.Run(ctx, call)
}
