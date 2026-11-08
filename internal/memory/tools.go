package memory

import (
	"context"
	"strings"
	"sync"

	"github.com/phzietsman/hermes-agent/internal/memory/sam"
	"github.com/phzietsman/hermes-agent/internal/tools"
)

// Toolset is the toolset name of the memory tools; delegated children never receive it.
const Toolset = "memory"

// category maps a kind of memory to the service's categories (it accepts only identity, knowledge and context):
// who the user is and how they want the agent to behave is identity; learned facts and lessons are knowledge.
var category = map[string]string{"fact": "knowledge", "preference": "identity", "instruction": "identity", "lesson": "knowledge"}

// Manager owns the settings and the per-session providers. Providers are per session because the frozen core
// profile, the session label and the recall state belong to one conversation.
type Manager struct {
	Settings *sam.Settings
	Home     string
	// Client overrides the HTTP client of every provider (tests).
	Client *sam.Client

	mu   sync.Mutex
	byID map[string]*Provider
}

// NewManager returns a manager when memory is configured, else (nil, reason).
func NewManager(home string, lookup func(string) (string, bool)) (*Manager, string) {
	key, _ := lookup(sam.APIKeyEnv)
	s, why := sam.LoadSettings(home, key)
	if s == nil {
		return nil, why
	}
	return &Manager{Settings: s, Home: home, byID: map[string]*Provider{}}, ""
}

// ForSession returns the session's provider, building it on first use.
func (m *Manager) ForSession(ctx context.Context, sessionID, userID string, readOnly bool) *Provider {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p := m.byID[sessionID]; p != nil {
		return p
	}
	var c *sam.Client
	if m.Client != nil {
		cc := *m.Client
		c = &cc
	}
	p := New(ctx, m.Settings, Options{Home: m.Home, SessionID: sessionID, UserID: userID, ReadOnly: readOnly, Client: c})
	m.byID[sessionID] = p
	return p
}

// Lookup returns a session's provider, or nil.
func (m *Manager) Lookup(sessionID string) *Provider {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.byID[sessionID]
}

// Close drains every provider's writer.
func (m *Manager) Close() {
	m.mu.Lock()
	ps := make([]*Provider, 0, len(m.byID))
	for _, p := range m.byID {
		ps = append(ps, p)
	}
	m.byID = map[string]*Provider{}
	m.mu.Unlock()
	for _, p := range ps {
		p.Close()
	}
}

func errJSON(msg string) string {
	return tools.Error(msg, tools.Field{Key: "provider", Value: "agent_memory"})
}

func okJSON(payload any) string {
	return tools.Result(tools.Field{Key: "success", Value: true}, tools.Field{Key: "result", Value: payload})
}

// RegisterTools adds the four agent_memory_* tools. They are service-gated: the caller registers them only when
// memory is configured.
func RegisterTools(r *tools.Registry, m *Manager) {
	str := map[string]any{"type": "string"}
	def := func(name, desc string, props map[string]any, required ...string) tools.Tool {
		req := make([]any, len(required))
		for i, x := range required {
			req[i] = x
		}
		return tools.Tool{
			Name: name, Toolset: Toolset, Description: desc,
			Parameters: map[string]any{"type": "object", "properties": props, "required": req},
			Handler: func(ctx context.Context, a tools.Args) (string, error) {
				return handle(ctx, m, name, a), nil
			},
		}
	}
	r.MustRegister(
		def("agent_memory_recall", "Search long-term memory for facts, preferences and past lessons.",
			map[string]any{"query": str, "k": map[string]any{"type": "integer", "description": "Max results (default 5)."}}, "query"),
		def("agent_memory_remember", "Save a durable fact, user preference, standing instruction or lesson learned. Procedures belong in skills, not here.",
			map[string]any{"text": str, "kind": map[string]any{"type": "string", "enum": []any{"fact", "preference", "instruction", "lesson"}}}, "text"),
		def("agent_memory_forget", "Forget memories matching a description. Soft by default.",
			map[string]any{"query": str, "purge": map[string]any{"type": "boolean", "description": "Hard delete."}}, "query"),
		def("agent_memory_reflect", "Reflect over memory to surface patterns on a question.",
			map[string]any{"query": str, "persist": map[string]any{"type": "boolean"}}, "query"),
	)
}

func handle(ctx context.Context, m *Manager, name string, a tools.Args) string {
	p := m.Lookup(tools.Env(ctx).SessionID)
	if p == nil || !p.active() {
		return errJSON("Agent Memory is unavailable")
	}
	query := a.String("query", "")
	var out string
	var err error
	switch name {
	case "agent_memory_recall":
		if strings.TrimSpace(query) == "" {
			return errJSON("query is required")
		}
		var hits []sam.Hit
		hits, err = p.client.Recall(ctx, query, a.Int("k", p.settings.TopK), []string{p.settings.DefaultScope})
		rows := make([]map[string]any, 0, len(hits))
		for _, h := range hits {
			rows = append(rows, map[string]any{"text": h.Text, "score": h.Score, "id": h.ID})
		}
		out = okJSON(map[string]any{"hits": rows})
	case "agent_memory_reflect":
		if strings.TrimSpace(query) == "" {
			return errJSON("query is required")
		}
		var res map[string]any
		res, err = p.client.Reflect(ctx, query, a.Bool("persist", false))
		out = okJSON(res)
	case "agent_memory_forget":
		if strings.TrimSpace(query) == "" {
			return errJSON("query is required")
		}
		p.write(sam.OpForget, map[string]any{"query": query, "purge": a.Bool("purge", false)})
		out = okJSON(map[string]any{"queued": true})
	case "agent_memory_remember":
		text := a.String("text", "")
		if strings.TrimSpace(text) == "" {
			return errJSON("text is required")
		}
		kind := a.String("kind", "")
		if kind == "" {
			kind = "fact"
		}
		var cat any
		if c, ok := category[kind]; ok {
			cat = c
		}
		p.write(sam.OpRemember, map[string]any{"text": text, "labels": []any{"kind=" + kind, "origin=" + p.Origin, p.sessionLabel()},
			"category": cat, "scopes": p.settings.DefaultScope})
		out = okJSON(map[string]any{"queued": true})
	default:
		return errJSON("unknown tool " + name)
	}
	if err != nil {
		return toolFailure(p, name, err)
	}
	p.breaker.RecordOK()
	return out
}

func toolFailure(p *Provider, name string, err error) string {
	k, ok := sam.KindOf(err)
	switch {
	case !ok:
		return errJSON(err.Error())
	case k == sam.Timeout:
		return errJSON("Agent Memory is slow right now (" + err.Error() + "); try again or continue without it")
	case k == sam.Rejected:
		return errJSON("Agent Memory rejected the request: " + err.Error())
	}
	p.breaker.RecordFailure(k == sam.AuthFailed)
	_ = name
	return errJSON(err.Error())
}
