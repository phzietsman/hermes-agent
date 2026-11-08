package evals

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/phzietsman/hermes-agent/internal/agent"
	"github.com/phzietsman/hermes-agent/internal/app"
	"github.com/phzietsman/hermes-agent/internal/config"
	"github.com/phzietsman/hermes-agent/internal/llm"
	"github.com/phzietsman/hermes-agent/internal/memory/sam"
	"github.com/phzietsman/hermes-agent/internal/tools"
)

// Env is what a live run needs. Credentials come from the environment, never from files in the repo.
type Env struct {
	AnthropicKey, Model             string
	SAMEndpoint, SAMContext, SAMKey string
	// Provider replaces the model provider (tests); the SAM service is still the real or a fake one.
	Provider agent.Provider
}

// SAMArm drives the real agent (app.Runtime) with Agent Memory as the provider, in an isolated home. One session at
// a time; ending a session runs the same lifecycle the CLI runs at exit and then waits for the background review, so
// its writes are counted.
type SAMArm struct {
	rt   *app.Runtime
	rec  *Recorder
	home string
	sess *app.Session
	sel  tools.Selection
	// ReviewWait bounds the wait for a background review at each session end.
	ReviewWait time.Duration

	mu        sync.Mutex
	told      []string
	AskTools  map[string]int
	TellTools map[string]int
	Scope     string
}

// NewSAMArm creates an isolated home under dir, registers run-unique memory scopes and opens the runtime.
func NewSAMArm(ctx context.Context, dir string, env Env, extra app.Options) (*SAMArm, error) {
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	tag := fmt.Sprintf("%x", time.Now().UnixNano())[8:]
	scope := "user/eval-" + tag
	settings := map[string]any{"endpoint": env.SAMEndpoint, "context": env.SAMContext, "default_scope": scope,
		"agent_scope": "agent/eval-" + tag, "timeout_s": 20}
	b, _ := json.Marshal(settings)
	if err := os.WriteFile(filepath.Join(home, sam.ConfigFile), b, 0o600); err != nil {
		return nil, err
	}
	var envFile strings.Builder
	if env.AnthropicKey != "" {
		fmt.Fprintf(&envFile, "ANTHROPIC_API_KEY=%s\n", env.AnthropicKey)
	}
	fmt.Fprintf(&envFile, "%s=%s\n", sam.APIKeyEnv, env.SAMKey)
	if err := os.WriteFile(filepath.Join(home, ".env"), []byte(envFile.String()), 0o600); err != nil {
		return nil, err
	}
	if env.Model != "" {
		if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("model: "+env.Model+"\n"), 0o644); err != nil {
			return nil, err
		}
	}
	s, why := sam.LoadSettings(home, env.SAMKey)
	if s == nil {
		return nil, fmt.Errorf("agent memory settings invalid: %s", why)
	}
	raw := s.Client()
	for _, sc := range []string{s.DefaultScope, s.AgentScope} {
		if err := raw.RegisterScope(ctx, sc); err != nil {
			return nil, fmt.Errorf("register scope %s: %w", sc, err)
		}
	}
	rec := &Recorder{}
	if env.Provider != nil {
		extra.Provider = env.Provider
	}
	extra.SAM = &sam.Client{HTTP: &http.Client{Transport: rec}}
	extra.LongRunning = true // a session end starts its review at once: the arm waits for it
	rt, err := app.Open(config.WithHome(ctx, home), home, extra)
	if err != nil {
		return nil, err
	}
	return &SAMArm{rt: rt, rec: rec, home: home, ReviewWait: 5 * time.Minute, Scope: scope,
		sel: tools.Selection{Toolsets: []string{"memory", "skills"}}, AskTools: map[string]int{}, TellTools: map[string]int{}}, nil
}

// UseTools widens the tools a session offers (the procedural scenario needs files and a shell).
func (a *SAMArm) UseTools(toolsets ...string) { a.sel = tools.Selection{Toolsets: toolsets} }

// Close ends the current session and releases the runtime.
func (a *SAMArm) Close() error {
	_ = a.StartSession(context.Background())
	return a.rt.Close()
}

func (a *SAMArm) session(ctx context.Context, cwd string) (*app.Session, error) {
	if a.sess != nil {
		return a.sess, nil
	}
	s, err := a.rt.NewSession(ctx, app.SessionOptions{Source: "eval", Platform: "cli", CWD: cwd, Selection: a.sel})
	if err != nil {
		return nil, err
	}
	a.sess = s
	return s, nil
}

func (a *SAMArm) StartSession(ctx context.Context) error {
	s := a.sess
	if s == nil {
		return nil
	}
	a.sess = nil
	s.End(context.WithoutCancel(ctx))
	s.WaitReviews(a.ReviewWait)
	s.Close()
	return nil
}

func countTools(msgs []llm.Message, into map[string]int) {
	for _, m := range msgs {
		for _, c := range m.ToolCalls {
			into[c.Function.Name]++
		}
	}
}

func (a *SAMArm) turn(ctx context.Context, text string, into map[string]int, cwd string) (string, llm.Usage, error) {
	s, err := a.session(ctx, cwd)
	if err != nil {
		return "", llm.Usage{}, err
	}
	before := len(s.Agent.History())
	res, err := s.Agent.RunTurn(ctx, text)
	if err != nil {
		return "", llm.Usage{}, err
	}
	countTools(s.Agent.History()[before:], into)
	return res.FinalResponse, res.Usage, nil
}

func (a *SAMArm) Tell(ctx context.Context, key, statement string) error {
	a.mu.Lock()
	a.told = append(a.told, statement)
	a.mu.Unlock()
	_, _, err := a.turn(ctx, statement, a.TellTools, "")
	return err
}

func (a *SAMArm) Ask(ctx context.Context, key, question string) (*string, error) {
	reply, _, err := a.turn(ctx, question, a.AskTools, "")
	if err != nil {
		return nil, err
	}
	if reply == "" {
		return nil, nil
	}
	return &reply, nil
}

func (a *SAMArm) WritesClaimed() int { a.mu.Lock(); defer a.mu.Unlock(); return len(a.told) }

func (a *SAMArm) landedValues() []string {
	var text strings.Builder
	for _, w := range a.rec.Writes() {
		text.WriteString(w.Text)
		text.WriteByte('\n')
	}
	var out []string
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, s := range a.told {
		if v := ToldValue(s); strings.Contains(text.String(), v) {
			out = append(out, v)
		}
	}
	return out
}

// WritesLanded drains the writer and any background review, then counts told facts the service accepted.
func (a *SAMArm) WritesLanded(ctx context.Context) (int, error) {
	if err := a.StartSession(ctx); err != nil {
		return 0, err
	}
	return len(a.landedValues()), nil
}

// LandingDetail says who wrote the landed facts: the agent in a turn (assistant_tool), the per-turn sync (turn),
// the end-of-session transcript (session) or a background review.
func (a *SAMArm) LandingDetail() map[string]int {
	landed := a.landedValues()
	out := map[string]int{"landed": len(landed)}
	for _, origin := range []string{"assistant_tool", "turn", "session", "background_review"} {
		var texts strings.Builder
		for _, w := range a.rec.Writes() {
			if w.Origin == origin {
				texts.WriteString(w.Text + "\n")
			}
		}
		n := 0
		for _, v := range landed {
			if strings.Contains(texts.String(), v) {
				n++
			}
		}
		out[origin] = n
	}
	return out
}

// AllowProfile implements ProfileAdmitter.
func (a *SAMArm) AllowProfile(tags []string) { a.rec.SetProfileTags(tags) }

// ProfileEntriesServed implements ProfileAdmitter.
func (a *SAMArm) ProfileEntriesServed() int { return a.rec.ProfileServed() }

// RunTask is the procedural arm: one task with terminal and file tools, measured.
func (a *SAMArm) RunTask(ctx context.Context, text string) (TaskMetrics, error) {
	counts := map[string]int{}
	cwd := filepath.Join(a.home, "work")
	_ = os.MkdirAll(cwd, 0o755)
	before := 0
	if a.sess != nil {
		before = len(a.sess.Agent.History())
	}
	_, usage, err := a.turn(ctx, text, counts, cwd)
	if err != nil {
		return nil, err
	}
	errs := 0
	if a.sess != nil {
		hist := a.sess.Agent.History()
		for _, m := range hist[before:] {
			if m.Role != llm.RoleTool {
				continue
			}
			c := m.Content.AsText()
			c = c[:min(len(c), 400)]
			for _, marker := range []string{`"error"`, `"exit_code": 1`, "Traceback", "No such file"} {
				if strings.Contains(c, marker) {
					errs++
					break
				}
			}
		}
	}
	total := 0
	for _, n := range counts {
		total += n
	}
	return TaskMetrics{"calls": total, "errors": errs, "tokens": usage.InputTokens + usage.OutputTokens,
		"skill_views": counts["skill_view"], "skill_writes": counts["skill_manage"],
		"memory_recalls": counts["agent_memory_recall"], "memory_saves": counts["agent_memory_remember"]}, nil
}

// SkillsCreated counts SKILL.md files under the isolated home.
func (a *SAMArm) SkillsCreated() int {
	n := 0
	_ = filepath.WalkDir(filepath.Join(a.home, "skills"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == "SKILL.md" {
			n++
		}
		return nil
	})
	return n
}

// Home is the isolated profile home (the procedural scenario's work directory lives under it).
func (a *SAMArm) Home() string { return a.home }

// ToolSummary is the tool use of the tell and ask phases, for diagnosis.
func (a *SAMArm) ToolSummary() string {
	f := func(m map[string]int) string {
		var ks []string
		for k, v := range m {
			ks = append(ks, fmt.Sprintf("%s=%d", k, v))
		}
		sort.Strings(ks)
		return strings.Join(ks, " ")
	}
	return "tell: " + f(a.TellTools) + " | ask: " + f(a.AskTools)
}
