package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/phzietsman/hermes-agent/internal/llm"
	"github.com/phzietsman/hermes-agent/internal/provider/anthropic"
	"github.com/phzietsman/hermes-agent/internal/session"
)

// ErrTurnInProgress is returned when RunTurn is called while another turn of the same agent is running. A
// session has one turn at a time: concurrent turns would interleave messages and break role alternation.
var ErrTurnInProgress = errors.New("a turn is already running for this session")

// ToolRunner executes the tools offered to the model. The tool set is fixed for the session: changing it
// mid-conversation would break the provider's prompt cache.
type ToolRunner interface {
	Defs() []llm.ToolDef
	// Run executes one call. A returned error is shown to the model as the tool's result.
	Run(ctx context.Context, call llm.ToolCall) (string, error)
	// ParallelSafe reports whether calls to name may run concurrently with other calls.
	ParallelSafe(name string) bool
}

// Compactor shrinks a transcript that has outgrown the context window (task 2.4).
type Compactor interface {
	// ShouldCompress is asked after each response with the prompt size the provider reported.
	ShouldCompress(promptTokens int) bool
	// Compress returns the replacement transcript.
	Compress(ctx context.Context, history []llm.Message) ([]llm.Message, error)
}

// Events observes a turn. Every field is optional.
type Events struct {
	// Text sees streamed assistant text.
	Text func(string)
	// ToolStart and ToolDone bracket each tool call.
	ToolStart func(call llm.ToolCall)
	ToolDone  func(call llm.ToolCall, result string, failed bool)
	// Status carries diagnostics the user should see (retries, truncation, recovery).
	Status func(string)
}

// Config wires an Agent. Store, SessionID, Provider, Prompt and Model are required.
type Config struct {
	Store     *session.Store
	SessionID string
	Provider  Provider
	Tools     ToolRunner
	// Prompt builds the system prompt. It runs only when the session has none stored.
	Prompt PromptBuilder
	Model  string
	// MaxTokens caps one response; 0 uses the model's ceiling.
	MaxTokens     int
	ContextLength int
	Budget        *IterationBudget
	Compactor     Compactor
	Events        Events
	// MaxRetries is how many times a retryable API error is retried (default 3).
	MaxRetries int
	Backoff    anthropic.Backoff
	// Sleep waits between retries; nil uses a context-aware timer. Tests inject a recorder.
	Sleep func(ctx context.Context, d time.Duration) error
	// OnAuthError is called once per call on an auth failure; true means credentials were refreshed and the
	// call should be retried.
	OnAuthError func(ctx context.Context, err error) bool
}

// PromptBuilder assembles the system prompt tiers.
type PromptBuilder func() PromptParts

// TurnResult is the outcome of one RunTurn.
type TurnResult struct {
	// FinalResponse is the text for the user: the model's answer, or an explanation when the turn did not finish.
	FinalResponse string
	// Completed is true when the model produced a final answer.
	Completed bool
	// Partial is true when the turn ended early (truncation, invalid tool calls, budget).
	Partial     bool
	Interrupted bool
	// Error describes why the turn did not complete, when it did not.
	Error    string
	APICalls int
	Usage    llm.Usage
}

// Agent runs turns against one session. Its history is loaded once and kept in step with the store.
type Agent struct {
	cfg     Config
	mu      sync.Mutex
	history []llm.Message
	loaded  bool
	system  string
	static  string
}

// New returns an Agent.
func New(cfg Config) *Agent {
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 3
	}
	if cfg.Backoff.Base == 0 {
		cfg.Backoff = anthropic.DefaultBackoff
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleepCtx
	}
	if cfg.Budget == nil {
		cfg.Budget = NewIterationBudget(90)
	}
	return &Agent{cfg: cfg}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// History is a copy of the transcript as the model sees it.
func (a *Agent) History() []llm.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]llm.Message(nil), a.history...)
}

func (a *Agent) status(format string, args ...any) {
	if a.cfg.Events.Status != nil {
		a.cfg.Events.Status(fmt.Sprintf(format, args...))
	}
}

// turn is the per-RunTurn retry state; none of it survives the turn.
type turn struct {
	result           TurnResult
	invalidToolTries int
	invalidJSONTries int
	emptyTries       int
	lengthContinues  int
	truncToolTries   int
	compressAttempts int
	outputCap        int // 0 means the configured cap
	fragments        []string
	trail            int  // ephemeral continuation messages at the tail of history
	stripThinking    bool // a rejected thinking signature: replay without preserved thinking
	lastPromptTokens int
	authRetried      bool
}

func (a *Agent) begin(ctx context.Context) error {
	ctx = context.WithoutCancel(ctx) // bookkeeping must survive an interrupt
	sp, err := EnsureSystemPrompt(ctx, a.cfg.Store, a.cfg.SessionID, func() PromptParts { return a.cfg.Prompt() })
	if err != nil {
		return err
	}
	a.system = sp.Text
	a.static = sp.Static
	if sp.Restored && a.static == "" {
		// Only the stored bytes are known after a resume; the stable tier is rebuilt and used only when the stored
		// prompt literally starts with it.
		if stable := a.cfg.Prompt().Stable; stable != "" && strings.HasPrefix(sp.Text, stable) {
			a.static = stable
		}
	}
	if a.loaded {
		return nil
	}
	rows, err := a.cfg.Store.Messages(ctx, a.cfg.SessionID, session.ListOptions{})
	if err != nil {
		return err
	}
	for _, r := range rows {
		a.history = append(a.history, fromRow(r))
	}
	a.loaded = true
	return nil
}

// add appends a message to the transcript and, unless it is a transient continuation message, stores it.
func (a *Agent) add(ctx context.Context, m llm.Message, toolName, finish string, persist bool) error {
	ctx = context.WithoutCancel(ctx) // what already happened is recorded even if the turn is interrupted
	a.history = append(a.history, m)
	if !persist {
		return nil
	}
	row, err := toRow(a.cfg.SessionID, m, toolName, finish)
	if err != nil {
		return err
	}
	_, err = a.cfg.Store.AppendMessage(ctx, row)
	return err
}

// closeTail ends a turn that stopped on a user or tool message with an assistant message, so the next user
// message does not follow a raw tool result or another user message.
func (a *Agent) closeTail(ctx context.Context, text string) error {
	if len(a.history) == 0 {
		return nil
	}
	if last := a.history[len(a.history)-1].Role; last != llm.RoleTool && last != llm.RoleUser {
		return nil
	}
	if strings.TrimSpace(text) == "" {
		text = interruptedPlaceholder
	}
	return a.add(ctx, llm.Message{Role: llm.RoleAssistant, Content: llm.Text(text)}, "", "", true)
}

func (a *Agent) tools() []llm.ToolDef {
	if a.cfg.Tools == nil {
		return nil
	}
	return a.cfg.Tools.Defs()
}

// RunTurn adds the user's message and runs the model, and any tools it calls, until it answers.
func (a *Agent) RunTurn(ctx context.Context, userText string) (TurnResult, error) {
	if !a.mu.TryLock() {
		return TurnResult{}, ErrTurnInProgress
	}
	defer a.mu.Unlock()
	if err := a.begin(ctx); err != nil {
		return TurnResult{}, err
	}
	// A crash or interrupt can leave a tool result as the tail; close it before the user speaks again.
	if err := a.closeTail(ctx, ""); err != nil {
		return TurnResult{}, err
	}
	t := &turn{}
	if err := a.add(ctx, llm.Message{Role: llm.RoleUser, Content: llm.Text(userText)}, "", "", true); err != nil {
		return TurnResult{}, err
	}
	for {
		if ctx.Err() != nil {
			return a.interrupted(ctx, t)
		}
		if !a.cfg.Budget.Consume() {
			return a.budgetExhausted(ctx, t)
		}
		res, err := a.step(ctx, t)
		if err != nil {
			if ctx.Err() != nil {
				return a.interrupted(ctx, t)
			}
			return TurnResult{}, err
		}
		if res != nil {
			res.APICalls, res.Usage = t.result.APICalls, t.result.Usage
			return *res, nil
		}
	}
}

func (a *Agent) interrupted(ctx context.Context, t *turn) (TurnResult, error) {
	a.dropTrail(t)
	if err := a.closeTail(context.WithoutCancel(ctx), ""); err != nil {
		return TurnResult{}, err
	}
	r := t.result
	r.Interrupted, r.Partial, r.Error, r.FinalResponse = true, true, "interrupted", "Interrupted."
	return r, nil
}

// budgetExhausted asks the model for a summary without tools. The request says so in the last tool result
// (or user message) of a request-local copy; the stored transcript gets only the model's answer.
func (a *Agent) budgetExhausted(ctx context.Context, t *turn) (TurnResult, error) {
	const note = "\n\n[System: The iteration limit for this turn was reached and no more tools are available. " +
		"Summarize what was done, what is left and anything the user must do next. Answer in plain text.]"
	msgs := outbound(a.history, t.stripThinking)
	if n := len(msgs); n > 0 {
		last := msgs[n-1].Clone()
		last.Content = llm.Text(contentText(last.Content) + note)
		msgs[n-1] = last
	}
	resp, err := a.cfg.Provider.Complete(ctx, CallInput{Messages: a.withSystem(msgs), MaxTokens: a.cfg.MaxTokens, StaticPrefix: a.static}, a.cfg.Events.Text)
	r := t.result
	r.Partial = true
	r.Error = "iteration limit reached"
	if err == nil && resp.Content != nil && strings.TrimSpace(*resp.Content) != "" {
		t.result.APICalls++
		r.APICalls = t.result.APICalls
		r.FinalResponse = *resp.Content
		if e := a.add(ctx, llm.Message{Role: llm.RoleAssistant, Content: llm.Text(r.FinalResponse)}, "", llm.FinishStop, true); e != nil {
			return TurnResult{}, e
		}
		return r, nil
	}
	r.FinalResponse = "I reached the iteration limit for this turn before finishing. Send `continue` to keep going."
	if e := a.closeTail(ctx, r.FinalResponse); e != nil {
		return TurnResult{}, e
	}
	return r, nil
}

func (a *Agent) withSystem(msgs []llm.Message) []llm.Message {
	return append([]llm.Message{{Role: llm.RoleSystem, Content: llm.Text(a.system)}}, msgs...)
}

// failure is a turn-ending API error with the user-facing explanation.
type failure struct{ message string }

func (f failure) Error() string { return f.message }

// step makes one model call and acts on the answer. A non-nil result ends the turn; nil continues the loop.
func (a *Agent) step(ctx context.Context, t *turn) (*TurnResult, error) {
	resp, err := a.callModel(ctx, t)
	if err != nil {
		var f failure
		switch {
		case ctx.Err() != nil:
			r, e := a.interrupted(ctx, t)
			return &r, e
		case errors.As(err, &f):
			return a.failTurn(ctx, t, f.message)
		}
		return nil, err
	}
	t.result.APICalls++
	t.lastPromptTokens = resp.Usage.InputTokens + resp.Usage.CacheReadTokens + resp.Usage.CacheWriteTokens
	t.result.Usage.InputTokens += resp.Usage.InputTokens
	t.result.Usage.OutputTokens += resp.Usage.OutputTokens
	t.result.Usage.CacheReadTokens += resp.Usage.CacheReadTokens
	t.result.Usage.CacheWriteTokens += resp.Usage.CacheWriteTokens
	if err := a.cfg.Store.AddUsage(context.WithoutCancel(ctx), a.cfg.SessionID, session.Usage{Input: resp.Usage.InputTokens, Output: resp.Usage.OutputTokens,
		CacheRead: resp.Usage.CacheReadTokens, CacheWrite: resp.Usage.CacheWriteTokens}); err != nil {
		return nil, err
	}
	if resp.FinishReason == llm.FinishLength {
		return a.handleLength(ctx, t, resp)
	}
	t.outputCap = 0
	if len(resp.ToolCalls) > 0 {
		return a.handleToolCalls(ctx, t, resp)
	}
	return a.handleFinal(ctx, t, resp)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func assistantMessage(resp llm.Response, content string, calls []llm.ToolCall) llm.Message {
	return llm.Message{
		Role: llm.RoleAssistant, Content: llm.Text(content), ToolCalls: calls, ReasoningContent: resp.Reasoning,
		ReasoningDetails: resp.ReasoningDetails, ContentBlocks: resp.ContentBlocks,
	}
}

// failTurn ends the turn with an explanation and a closed transcript.
func (a *Agent) failTurn(ctx context.Context, t *turn, text string) (*TurnResult, error) {
	a.dropTrail(t)
	if err := a.closeTail(ctx, text); err != nil {
		return nil, err
	}
	r := t.result
	r.Partial, r.Error, r.FinalResponse = true, text, text
	return &r, nil
}

func (a *Agent) handleFinal(ctx context.Context, t *turn, resp llm.Response) (*TurnResult, error) {
	content := deref(resp.Content)
	if strings.TrimSpace(content) == "" && len(t.fragments) == 0 {
		if resp.FinishReason == llm.FinishContentFilter {
			return a.failTurn(ctx, t, "The model declined to answer this request.")
		}
		return a.handleEmpty(ctx, t, resp)
	}
	t.emptyTries = 0
	text := a.collapseTrail(t, content)
	if err := a.add(ctx, assistantMessage(resp, text, nil), "", resp.FinishReason, true); err != nil {
		return nil, err
	}
	r := t.result
	r.Completed, r.FinalResponse = true, text
	return &r, nil
}

var emptyBackoff = anthropic.Backoff{Base: 5 * time.Second, Max: 60 * time.Second, JitterRatio: 0.5}

const maxEmptyRetries = 3

// handleEmpty retries a reply with neither text nor tool calls: each attempt is billed, so the budget is small.
func (a *Agent) handleEmpty(ctx context.Context, t *turn, resp llm.Response) (*TurnResult, error) {
	if t.emptyTries < maxEmptyRetries {
		t.emptyTries++
		a.status("Empty response from the model, retrying (%d/%d)", t.emptyTries, maxEmptyRetries)
		if err := a.cfg.Sleep(ctx, emptyBackoff.Delay(t.emptyTries)); err != nil {
			return nil, err
		}
		return nil, nil
	}
	a.dropTrail(t)
	// The sentinel keeps later "continue" turns from replaying reasoning and looping on empties.
	if err := a.add(ctx, assistantMessage(resp, "(empty)", nil), "", resp.FinishReason, true); err != nil {
		return nil, err
	}
	r := t.result
	r.Partial, r.Error, r.FinalResponse = true, "the model returned no content after all retries", "(empty)"
	return &r, nil
}

func sprintf(format string, args ...any) string { return fmt.Sprintf(format, args...) }
