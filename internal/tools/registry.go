package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/phzietsman/hermes-agent/internal/approval"
	"github.com/phzietsman/hermes-agent/internal/llm"
)

// DefaultMaxResultChars bounds one tool result unless the tool sets its own limit.
const DefaultMaxResultChars = 100_000

// Handler runs one call. A returned error is shown to the model as the call's result (a failure); the string
// is the result otherwise. Handlers read per-call facts (home, session) from Env(ctx).
type Handler func(ctx context.Context, args Args) (string, error)

// Tool is one capability offered to the model.
type Tool struct {
	Name    string
	Toolset string
	// Description and Parameters (a JSON Schema object) are what the model sees. They are fixed for the life of
	// a session: changing them would change the cached prompt prefix.
	Description string
	Parameters  map[string]any
	Handler     Handler
	// Check reports whether the tool can work at all (a binary exists, a service answers, the user opted in).
	// It is asked once, when a session's toolbox is built, never per call or per turn. Nil means always.
	Check func() bool
	// RequiresEnv names environment variables that must be set for the tool to be offered.
	RequiresEnv []string
	// Parallel marks calls that are safe to run concurrently with other calls.
	Parallel bool
	// MaxResultChars overrides DefaultMaxResultChars.
	MaxResultChars int
}

// Registry holds every tool the binary knows. Registration is explicit: there is no import-time discovery.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{tools: map[string]Tool{}} }

// Register adds a tool. A name is registered once.
func (r *Registry) Register(t Tool) error {
	switch {
	case strings.TrimSpace(t.Name) == "":
		return errors.New("tool needs a name")
	case t.Handler == nil:
		return fmt.Errorf("tool %q has no handler", t.Name)
	case t.Toolset == "":
		return fmt.Errorf("tool %q has no toolset", t.Name)
	case t.Parameters == nil || t.Parameters["type"] != "object":
		return fmt.Errorf("tool %q: parameters must be a JSON Schema object", t.Name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.tools[t.Name]; dup {
		return fmt.Errorf("tool %q is already registered", t.Name)
	}
	r.tools[t.Name] = t
	return nil
}

// MustRegister registers or panics; for the built-in tool set, where a clash is a build defect.
func (r *Registry) MustRegister(ts ...Tool) {
	for _, t := range ts {
		if err := r.Register(t); err != nil {
			panic("tools: " + err.Error())
		}
	}
}

// Names lists every registered tool, sorted.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.tools))
	for n := range r.tools {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Selection says which tools a session gets.
type Selection struct {
	// Toolsets limits the session to these toolsets; empty means every toolset.
	Toolsets []string
	// Exclude removes individual tools by name.
	Exclude []string
	// ExcludeToolsets removes whole toolsets, also when Toolsets is empty (every toolset).
	ExcludeToolsets []string
}

// Toolbox is the tool set of one session, resolved once: the gates have been asked and the definitions are
// fixed. It implements the agent loop's tool runner.
type Toolbox struct {
	tools map[string]Tool
	defs  []llm.ToolDef
}

// Build resolves a selection into a Toolbox. A tool is left out when its toolset is not selected, it is excluded,
// a required environment variable is unset, or its Check says no. Definitions are sorted by name so the tools
// array is byte-stable.
func (r *Registry) Build(sel Selection) *Toolbox {
	r.mu.RLock()
	defer r.mu.RUnlock()
	wantSet := toSet(sel.Toolsets)
	skip := toSet(sel.Exclude)
	skipSets := toSet(sel.ExcludeToolsets)
	tb := &Toolbox{tools: map[string]Tool{}}
	for _, name := range sortedKeys(r.tools) {
		t := r.tools[name]
		if skip[name] || skipSets[t.Toolset] || (len(wantSet) > 0 && !wantSet[t.Toolset]) || !envSet(t.RequiresEnv) || (t.Check != nil && !t.Check()) {
			continue
		}
		tb.tools[name] = t
		tb.defs = append(tb.defs, llm.ToolDef{Name: name, Description: t.Description, Parameters: llm.Block(t.Parameters)})
	}
	return tb
}

func toSet(xs []string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

func sortedKeys(m map[string]Tool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func envSet(names []string) bool {
	for _, n := range names {
		if os.Getenv(n) == "" {
			return false
		}
	}
	return true
}

// Defs are the definitions offered to the model.
func (tb *Toolbox) Defs() []llm.ToolDef { return append([]llm.ToolDef(nil), tb.defs...) }

// Names is the set of offered tool names, for prompt gating.
func (tb *Toolbox) Names() map[string]bool {
	m := make(map[string]bool, len(tb.tools))
	for n := range tb.tools {
		m[n] = true
	}
	return m
}

// Toolsets is the set of toolsets with at least one offered tool.
func (tb *Toolbox) Toolsets() map[string]bool {
	m := map[string]bool{}
	for _, t := range tb.tools {
		m[t.Toolset] = true
	}
	return m
}

// ParallelSafe reports whether calls to name may run concurrently with others.
func (tb *Toolbox) ParallelSafe(name string) bool { return tb.tools[name].Parallel }

// Run executes one call: arguments are decoded and coerced against the schema, the handler runs, and the
// result is bounded. Every failure comes back as a JSON error body (and as err, so the loop marks the call
// failed); nothing here panics into the loop.
func (tb *Toolbox) Run(ctx context.Context, call llm.ToolCall) (string, error) {
	t, ok := tb.tools[call.Function.Name]
	if !ok {
		return Error("Unknown tool: " + call.Function.Name), fmt.Errorf("unknown tool %q", call.Function.Name)
	}
	args, err := decodeArgs(call.Function.Arguments)
	if err != nil {
		return Error("Invalid arguments: " + err.Error()), err
	}
	args = Coerce(t.Parameters, args)
	out, err := t.Handler(ctx, args)
	if err != nil {
		return Error(SanitizeError("Tool execution failed: " + err.Error())), err
	}
	return limitResult(boundJSONErrorResult(out), t.MaxResultChars), nil
}

func decodeArgs(raw string) (Args, error) {
	if strings.TrimSpace(raw) == "" {
		return Args{}, nil
	}
	v, err := decodeJSON(raw)
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("arguments must be a JSON object")
	}
	return Args(m), nil
}

// limitResult keeps the head and tail of an oversized result around a marker that says how much was left out.
func limitResult(s string, limit int) string {
	if limit <= 0 {
		limit = DefaultMaxResultChars
	}
	n := utf8.RuneCountInString(s)
	if n <= limit {
		return s
	}
	if json.Valid([]byte(s)) {
		// Cutting JSON in the middle would hand the model a broken document, so say it was too large instead.
		r := []rune(s)
		return Error(fmt.Sprintf("The tool result was %d characters, over the %d-character limit, and was not delivered. "+
			"Narrow the request (a smaller range, a tighter query). The start of it follows in preview.", n, limit),
			Field{Key: "preview", Value: string(r[:min(len(r), 2000)])})
	}
	marker := fmt.Sprintf("\n\n[... %d of %d characters omitted by the tool-result limit ...]\n\n", n-limit, n)
	r := []rune(s)
	head := limit * 7 / 10
	tail := limit - head
	return string(r[:head]) + marker + string(r[len(r)-tail:])
}

type envKey struct{}

// CallEnv is what a handler may need beyond its arguments.
type CallEnv struct {
	// Home is the profile home (credential stores, caches); SessionID keys per-session tool state; CWD is the
	// session's working directory for relative paths.
	Home      string
	SessionID string
	CWD       string
	// Platform, ChatID and ThreadID say where the conversation lives (empty for the CLI), so a tool that schedules
	// work can address its results back to it.
	Platform, ChatID, ThreadID string
	// Gateway is true in a long-running process (the messaging gateway): commands that would stop it are refused.
	Gateway bool
	// Gate and Approval carry the approval policy and the way to reach a human; nil Gate means no approval layer.
	Gate     *approval.Gate
	Approval approval.Check
}

// WithEnv attaches per-call facts to ctx.
func WithEnv(ctx context.Context, e CallEnv) context.Context {
	return context.WithValue(ctx, envKey{}, e)
}

// Env returns the per-call facts attached to ctx, or the zero value.
func Env(ctx context.Context) CallEnv { e, _ := ctx.Value(envKey{}).(CallEnv); return e }

// Typed accessors: arguments are already coerced, so a wrong type means "absent".

// String returns the string argument key, or def.
func (a Args) String(key, def string) string {
	if s, ok := a[key].(string); ok {
		return s
	}
	return def
}

// Int returns the integer argument key, or def.
func (a Args) Int(key string, def int) int {
	if n, ok := a[key].(json.Number); ok {
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
		if f, err := n.Float64(); err == nil {
			return int(f)
		}
	}
	return def
}

// Bool returns the boolean argument key, or def.
func (a Args) Bool(key string, def bool) bool {
	if b, ok := a[key].(bool); ok {
		return b
	}
	return def
}

// Strings returns the string-list argument key.
func (a Args) Strings(key string) []string {
	list, _ := a[key].([]any)
	var out []string
	for _, x := range list {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
