// Package app wires the pieces into a running agent: configuration, the session store, the tool registry, the
// approval gate and the model provider. The CLI, the gateway and cron all build their agents here, so a session
// gets the same tools, prompt and safety policy whichever door it came in through.
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/phzietsman/hermes-agent/internal/agent"
	"github.com/phzietsman/hermes-agent/internal/approval"
	"github.com/phzietsman/hermes-agent/internal/config"
	"github.com/phzietsman/hermes-agent/internal/learning"
	"github.com/phzietsman/hermes-agent/internal/llm"
	"github.com/phzietsman/hermes-agent/internal/memory"
	"github.com/phzietsman/hermes-agent/internal/memory/sam"
	"github.com/phzietsman/hermes-agent/internal/provider/anthropic"
	"github.com/phzietsman/hermes-agent/internal/session"
	"github.com/phzietsman/hermes-agent/internal/skills"
	"github.com/phzietsman/hermes-agent/internal/tools"
	"github.com/phzietsman/hermes-agent/internal/tools/delegate"
	"github.com/phzietsman/hermes-agent/internal/tools/filetools"
	"github.com/phzietsman/hermes-agent/internal/tools/sessiontools"
	"github.com/phzietsman/hermes-agent/internal/tools/shell"
	"github.com/phzietsman/hermes-agent/internal/tools/skilltools"
	"github.com/phzietsman/hermes-agent/internal/tools/webtools"
)

const (
	defaultModel         = "claude-sonnet-5-5"
	defaultContextLength = 200_000
	defaultMaxIterations = 90
)

// Options overrides parts of the runtime; the zero value is production.
type Options struct {
	// Provider replaces the Anthropic provider (tests).
	Provider agent.Provider
	// Web replaces the web tool options (tests).
	Web webtools.Options
	// SAM replaces the memory HTTP client (tests).
	SAM *sam.Client
	// LongRunning is set by processes that outlive a conversation (the gateway, cron): a session end then starts
	// its review at once, instead of leaving it queued for the next process.
	LongRunning bool
}

// Runtime is one profile's running state, shared by every session served from it.
type Runtime struct {
	Home     string
	Config   *config.Config
	Secrets  *config.Secrets
	Store    *session.Store
	Registry *tools.Registry
	Gate     *approval.Gate
	Provider agent.Provider
	// Memory is nil when long-term memory is not configured (no key, or no agent-memory.json).
	Memory *memory.Manager
	Model  string
	// ContextLength is the model window used for compaction thresholds.
	ContextLength int

	longRunning bool
	loopsMu     sync.Mutex
	loops       []*learning.Loop
	queueOnce   sync.Once
	queue       learning.Queue
	queueErr    error
}

// Open loads the profile at home (creating its skeleton), opens the session store and builds the registry and
// gate. It fails when no model credential can be found and no provider override is given.
func Open(ctx context.Context, home string, o Options) (*Runtime, error) {
	if err := config.Init(home); err != nil {
		return nil, err
	}
	secrets := config.LoadSecrets(home)
	cfg, err := config.Load(home, secrets)
	if err != nil {
		return nil, err
	}
	store, err := session.Open(home)
	if err != nil {
		return nil, err
	}
	rt := &Runtime{longRunning: o.LongRunning, Home: home, Config: cfg, Secrets: secrets, Store: store, Registry: tools.NewRegistry()}
	rt.Model = defaultModel
	if m := strings.TrimSpace(cfg.String("model", "")); m != "" {
		rt.Model = anthropic.NormalizeModelName(m)
	}
	rt.ContextLength = cfg.Int("model_context_length", defaultContextLength)
	if rt.Provider = o.Provider; rt.Provider == nil {
		if rt.Provider, err = rt.anthropicProvider(ctx); err != nil {
			store.Close()
			return nil, err
		}
	}
	rt.Gate = rt.newGate()
	if m, _ := memory.NewManager(home, secrets.Lookup); m != nil {
		m.Client = o.SAM
		rt.Memory = m
	}
	rt.registerTools(o)
	return rt, nil
}

// Close releases the store.
func (rt *Runtime) Close() error {
	// Stop every session's learning loop and wait for its drain before the store goes away; a review cut short
	// is released back to the queue, not lost.
	rt.loopsMu.Lock()
	loops := rt.loops
	rt.loops = nil
	rt.loopsMu.Unlock()
	for _, l := range loops {
		l.Close()
		l.Wait(10 * time.Second)
	}
	if rt.Memory != nil {
		rt.Memory.Close()
	}
	return rt.Store.Close()
}

func (rt *Runtime) anthropicProvider(ctx context.Context) (agent.Provider, error) {
	creds := &anthropic.Credentials{
		Env: rt.Secrets, AdoptExternalLogin: rt.Config.Bool("auth.adopt_external_logins", false),
		Store: anthropic.DefaultStore(os.Getenv),
	}
	tok, err := creds.Token(ctx)
	if err != nil || tok == "" {
		return nil, fmt.Errorf("no Anthropic credential found: set ANTHROPIC_API_KEY in %s (or set auth.adopt_external_logins: true to use a Claude Code login)%s",
			filepath.Join(config.DisplayHome(rt.Home), ".env"), errSuffix(err))
	}
	baseURL := rt.Config.String("anthropic.base_url", "")
	return &agent.AnthropicProvider{
		Client: &anthropic.Client{BaseURL: baseURL, Auth: creds}, Model: rt.Model, OAuth: anthropic.IsOAuthToken(tok),
		ContextLength: rt.ContextLength, CacheTTL: rt.Config.String("prompt_caching.ttl", "5m"),
		Direct: baseURL == "" || strings.Contains(baseURL, "api.anthropic.com"),
	}, nil
}

func errSuffix(err error) string {
	if err == nil {
		return ""
	}
	return ": " + err.Error()
}

func (rt *Runtime) newGate() *approval.Gate {
	userHome, _ := os.UserHomeDir()
	_, sudo := rt.Secrets.Lookup("SUDO_PASSWORD")
	cfg := approval.Config{
		Mode: rt.Config.String("approvals.mode", "manual"), Deny: rt.Config.Strings("approvals.deny", nil),
		Allowlist: rt.Config.Strings("approvals.allowlist", nil), UnattendedMode: rt.Config.String("approvals.unattended", "deny"),
		Timeout: time.Duration(rt.Config.Int("approvals.timeout", 60)) * time.Second, Home: rt.Home,
	}
	return approval.NewGate(approval.Detector{Home: userHome, HermesHome: rt.Home, SudoPasswordConfigured: sudo}, cfg)
}

func (rt *Runtime) skillSettings(home string) skills.Settings {
	return skills.LoadSettings(home, rt.Config, "")
}

func (rt *Runtime) registerTools(o Options) {
	r := rt.Registry
	filetools.Register(r)
	shell.Register(r)
	skilltools.Register(r, rt.skillSettings)
	sessiontools.Register(r, rt.Store)
	web := o.Web
	if web.APIKey == nil {
		web.APIKey = func() string { return rt.Secrets.Get("TAVILY_API_KEY", "") }
	}
	webtools.Register(r, web)
	if rt.Memory != nil {
		memory.RegisterTools(r, rt.Memory)
	}
	delegate.Register(r, rt.childFactory, rt.Config.Int("delegation.max_concurrent_children", delegate.DefaultMaxChildren))
}

// NewSessionID mints an id in Python's format (UTC timestamp plus six hex characters), so ids sort by start time.
func NewSessionID(now time.Time) string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return now.UTC().Format("20060102_150405") + "_" + hex.EncodeToString(b[:])
}

// SessionOptions describes one session's surroundings.
type SessionOptions struct {
	// ID resumes an existing session; empty starts a new one.
	ID       string
	Source   string // "cli", "telegram", ...
	Platform string // the prompt's platform hint key; "" for none
	CWD      string
	// UserID scopes memory to this person (gateway sessions); ReadOnly sessions never write memory.
	UserID   string
	ReadOnly bool
	// Selection limits the session's tools (an eval offers only memory and skills). A review fork of the session
	// gets the same selection, so its tools array matches its parent's.
	Selection tools.Selection
	// Prompter answers approval prompts; nil means nobody can answer (the unattended policy applies).
	Prompter approval.Prompter
	Events   agent.Events
}

// Session is an agent bound to one stored conversation.
type Session struct {
	ID    string
	Agent *agent.Agent
	Tools *tools.Toolbox
	mem   *memory.Provider
	learn *learningHook
}

// End records the finished conversation in long-term memory and waits for the writer, so nothing queued is lost
// when the process exits. It is safe to call without memory.
func (s *Session) End(ctx context.Context) {
	if s.learn != nil {
		s.learn.loop.OnBoundary(s.ID, learning.TriggerSessionEnd, s.Agent.History(), s.learn.rt.longRunning)
		if !s.learn.rt.longRunning {
			s.learn.loop.Close()
		}
	}
	if s.mem != nil {
		s.mem.EndSession(ctx, s.Agent.History())
		s.mem.Flush()
	}
}

// WaitReviews blocks until this session's background review drain has finished, or the timeout passes.
func (s *Session) WaitReviews(timeout time.Duration) bool {
	if s.learn == nil {
		return true
	}
	return s.learn.loop.Wait(timeout)
}

// Close stops the session's background work (an idle timer, a running review: the queue keeps its job).
func (s *Session) Close() {
	if s.learn != nil {
		s.learn.loop.Close()
	}
}

// NewSession starts or resumes a session. The system prompt is built (and stored) on the first turn only.
func (rt *Runtime) NewSession(ctx context.Context, so SessionOptions) (*Session, error) {
	id := so.ID
	if id == "" {
		id = NewSessionID(time.Now())
	}
	cwd := so.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if _, err := rt.Store.GetSession(ctx, id); errors.Is(err, session.ErrNotFound) {
		err = rt.Store.CreateSession(ctx, session.NewSession{ID: id, Source: orDefault(so.Source, "cli"), Model: rt.Model, Cwd: cwd,
			ProfileName: config.ProfileName(rt.Home)})
		if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	box := rt.Registry.Build(so.Selection)
	runner := &envTools{Toolbox: box, env: tools.CallEnv{Home: rt.Home, SessionID: id, CWD: cwd, Gate: rt.Gate,
		Approval: approval.Check{SessionID: id, Prompter: so.Prompter}}}
	var mem *memory.Provider
	block := ""
	if rt.Memory != nil {
		mem = rt.Memory.ForSession(ctx, id, so.UserID, so.ReadOnly)
		block = mem.SystemPromptBlock()
	}
	cfg := rt.agentConfig(id, runner, box, so.Platform, cwd, block, so.Events)
	if mem != nil {
		cfg.Memory = mem
	}
	learn := rt.newLearning(ctx, id, so.ReadOnly, so.Selection)
	if learn != nil {
		cfg.Learning = learn
	}
	return &Session{ID: id, Agent: agent.New(cfg), Tools: box, mem: mem, learn: learn}, nil
}

func orDefault(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

func (rt *Runtime) agentConfig(id string, runner agent.ToolRunner, box *tools.Toolbox, platform, cwd, memoryBlock string, ev agent.Events) agent.Config {
	maxTokens := anthropic.MaxOutputTokens(rt.Model)
	comp := agent.NewCompressor(agent.CompressorConfig{
		ContextLength: rt.ContextLength, MaxTokens: maxTokens, SessionID: id, Summarize: agent.ProviderSummarizer(rt.Provider),
	})
	return agent.Config{
		Store: rt.Store, SessionID: id, Provider: rt.Provider, Tools: runner, Model: rt.Model, MaxTokens: maxTokens,
		ContextLength: rt.ContextLength, Compactor: comp, Events: ev,
		Budget: agent.NewIterationBudget(rt.Config.Int("agent.max_turns", defaultMaxIterations)),
		Prompt: func() agent.PromptParts {
			return agent.BuildSystemPromptParts(agent.PromptInput{
				Home: rt.Home, SessionID: id, RootSessionID: id, SessionStart: time.Now(), Model: rt.Model, Provider: "anthropic",
				Platform: platform, Tools: box.Names(), Toolsets: box.Toolsets(), CWD: cwd, ContextLength: rt.ContextLength,
				MemoryBlock: memoryBlock, SkillsIndex: agent.SkillsIndexOptions{Settings: rt.skillSettings(rt.Home), Filter: skills.HostFilter()},
			})
		},
	}
}

// childFactory builds a delegated subagent: a fresh session under the parent, leaf tools only, and the focused
// system prompt the delegate tool composed.
func (rt *Runtime) childFactory(ctx context.Context, parent, system string) (*agent.Agent, error) {
	id := NewSessionID(time.Now())
	err := rt.Store.CreateSession(ctx, session.NewSession{ID: id, Source: "delegate", Model: rt.Model, ParentSessionID: parent,
		ProfileName: config.ProfileName(rt.Home)})
	if err != nil {
		return nil, err
	}
	box := rt.Registry.Build(delegate.ChildSelection(tools.Selection{}))
	// A child's file and shell calls are gated like its parent's, but nobody can answer a prompt for it: the
	// unattended policy (deny by default) applies.
	runner := &envTools{Toolbox: box, env: tools.CallEnv{Home: rt.Home, SessionID: id, Gate: rt.Gate, Approval: approval.Check{SessionID: id}}}
	cfg := rt.agentConfig(id, runner, box, "", "", "", agent.Events{})
	cfg.Budget = agent.NewIterationBudget(rt.Config.Int("delegation.max_iterations", 50))
	cfg.Prompt = func() agent.PromptParts { return agent.PromptParts{Stable: system} }
	return agent.New(cfg), nil
}

// envTools attaches the per-call facts (home, session, approval) the tools read from their context.
type envTools struct {
	*tools.Toolbox
	env tools.CallEnv
}

func (e *envTools) Run(ctx context.Context, call llm.ToolCall) (string, error) {
	return e.Toolbox.Run(tools.WithEnv(ctx, e.env), call)
}
