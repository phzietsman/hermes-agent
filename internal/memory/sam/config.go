package sam

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// ConfigFile is the non-secret settings file in the profile home; the API key never goes in it.
	ConfigFile = "agent-memory.json"
	// APIKeyEnv names the secret.
	APIKeyEnv = "AGENT_MEMORY_API_KEY"
)

// RecallMode says how memory is surfaced each turn.
type RecallMode string

const (
	// RecallHybrid injects recalled hits every turn.
	RecallHybrid RecallMode = "hybrid"
	// RecallContext injects SAM-synthesised context every turn.
	RecallContext RecallMode = "context"
	// RecallTools injects nothing: the model calls agent_memory_recall.
	RecallTools RecallMode = "tools"
)

// Settings is the validated memory configuration. Every field is explicit: nothing is read from the environment
// except the key, by the caller.
type Settings struct {
	Endpoint            string
	Context             string
	APIKey              string
	DefaultScope        string
	AgentScope          string
	RecallMode          RecallMode
	TopK                int
	MaxPrefetchChars    int
	CoreProfileMaxChars int
	Timeout             time.Duration
	PrefetchWait        time.Duration
	MaxRetries          int
	BreakerThreshold    int
	SendToolOutputs     bool
	ConsolidateOnEnd    bool
	ReplayEveryNTurns   int
}

type fileSettings struct {
	Endpoint            string   `json:"endpoint"`
	Context             string   `json:"context"`
	DefaultScope        *string  `json:"default_scope"`
	AgentScope          *string  `json:"agent_scope"`
	RecallMode          *string  `json:"recall_mode"`
	TopK                *int     `json:"top_k"`
	MaxPrefetchChars    *int     `json:"max_prefetch_chars"`
	CoreProfileMaxChars *int     `json:"core_profile_max_chars"`
	TimeoutS            *float64 `json:"timeout_s"`
	PrefetchWaitS       *float64 `json:"prefetch_wait_s"`
	MaxRetries          *int     `json:"max_retries"`
	BreakerThreshold    *int     `json:"breaker_threshold"`
	SendToolOutputs     *bool    `json:"send_tool_outputs"`
	ConsolidateOnEnd    *bool    `json:"consolidate_on_end"`
	ReplayEveryNTurns   *int     `json:"replay_every_n_turns"`
}

func def[T any](p *T, d T) T {
	if p != nil {
		return *p
	}
	return d
}

func inRange(name string, v, lo, hi int) error {
	if v < lo || v > hi {
		return fmt.Errorf("%s must be between %d and %d", name, lo, hi)
	}
	return nil
}

// LoadSettings reads <home>/agent-memory.json and combines it with the key. It returns (nil, reason) when memory
// cannot be used; it never touches the network. Unknown fields are an error, as in Python.
func LoadSettings(home, apiKey string) (*Settings, string) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, APIKeyEnv + " is not set"
	}
	path := filepath.Join(home, ConfigFile)
	var f fileSettings
	if raw, err := os.ReadFile(path); err == nil {
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&f); err != nil {
			return nil, fmt.Sprintf("invalid agent-memory settings: %v", err)
		}
	}
	if f.Endpoint == "" || f.Context == "" {
		return nil, "endpoint and context are required in " + path
	}
	if u, err := url.Parse(f.Endpoint); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, "invalid agent-memory settings: endpoint"
	}
	s := &Settings{
		Endpoint: strings.TrimRight(f.Endpoint, "/"), Context: f.Context, APIKey: apiKey,
		DefaultScope: def(f.DefaultScope, "user/default"), AgentScope: def(f.AgentScope, "agent/hermes"),
		RecallMode: RecallMode(def(f.RecallMode, "hybrid")), TopK: def(f.TopK, 5), MaxPrefetchChars: def(f.MaxPrefetchChars, 4000),
		CoreProfileMaxChars: def(f.CoreProfileMaxChars, 1500), Timeout: secs(def(f.TimeoutS, 20)), PrefetchWait: secs(def(f.PrefetchWaitS, 2.5)),
		MaxRetries: def(f.MaxRetries, 2), BreakerThreshold: def(f.BreakerThreshold, 3), SendToolOutputs: def(f.SendToolOutputs, false),
		ConsolidateOnEnd: def(f.ConsolidateOnEnd, true), ReplayEveryNTurns: def(f.ReplayEveryNTurns, 10),
	}
	var bad []string
	check := func(name string, err error) {
		if err != nil {
			bad = append(bad, name)
		}
	}
	switch s.RecallMode {
	case RecallHybrid, RecallContext, RecallTools:
	default:
		bad = append(bad, "recall_mode")
	}
	check("top_k", inRange("top_k", s.TopK, 1, 20))
	check("max_prefetch_chars", inRange("max_prefetch_chars", s.MaxPrefetchChars, 500, 20000))
	check("core_profile_max_chars", inRange("core_profile_max_chars", s.CoreProfileMaxChars, 200, 6000))
	check("max_retries", inRange("max_retries", s.MaxRetries, 0, 5))
	check("breaker_threshold", inRange("breaker_threshold", s.BreakerThreshold, 1, 1<<30))
	check("replay_every_n_turns", inRange("replay_every_n_turns", s.ReplayEveryNTurns, 1, 1<<30))
	if s.Timeout <= 0 || s.Timeout > 60*time.Second {
		bad = append(bad, "timeout_s")
	}
	// Prefetch never waits as long as a tool call: it must stay under the 8 s prefetch ceiling.
	if s.PrefetchWait < 0 || s.PrefetchWait >= 8*time.Second {
		bad = append(bad, "prefetch_wait_s")
	}
	if len(bad) > 0 {
		return nil, "invalid agent-memory settings: " + strings.Join(bad, ", ")
	}
	return s, ""
}

func secs(f float64) time.Duration { return time.Duration(f * float64(time.Second)) }

// Client builds the HTTP client for these settings.
func (s *Settings) Client() *Client {
	return &Client{Endpoint: s.Endpoint, Context: s.Context, APIKey: s.APIKey, Timeout: s.Timeout, MaxRetries: s.MaxRetries}
}
