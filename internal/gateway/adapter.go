// Package gateway connects the agent to chat platforms. An Adapter turns a platform into inbound Events and outbound
// sends and edits; the Gateway authorises people, routes each chat to its own session, streams replies by editing one
// message in place, asks for dangerous-command approval in the chat, and delivers cron output.
package gateway

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/phzietsman/hermes-agent/internal/config"
)

// Target addresses one conversation on a platform.
type Target struct {
	ChatID   string
	ThreadID string
}

// Event is one inbound message.
type Event struct {
	Platform string
	Target
	UserID   string
	UserName string
	Text     string
	// MessageID is the platform's id for this message.
	MessageID string
	// DM marks a private chat. In a group, only messages addressed to the bot (mentioned or replied to) are routed:
	// the adapter sets Addressed; a DM is always addressed.
	DM, Addressed bool
}

// Adapter is one chat platform.
type Adapter interface {
	// Name is the platform key ("telegram", "discord"): it prefixes session keys and config.
	Name() string
	// MaxLen is the longest message the platform accepts, in characters.
	MaxLen() int
	// Start connects and begins delivering events to handle (which must not block long). It returns once connected;
	// event loops run until ctx ends or Stop is called.
	Start(ctx context.Context, handle func(Event)) error
	// Stop disconnects.
	Stop(ctx context.Context) error
	// Send posts text and returns the platform's id for the message (for later edits).
	Send(ctx context.Context, to Target, text string) (string, error)
	// Edit replaces the text of a message this adapter sent.
	Edit(ctx context.Context, to Target, msgID, text string) error
	// Typing shows a typing indicator; it lasts a few seconds.
	Typing(ctx context.Context, to Target) error
}

// Factory builds an adapter from the profile's configuration and secrets, or returns an error that says what is
// missing.
type Factory func(cfg *config.Config, secrets *config.Secrets) (Adapter, error)

var (
	regMu     sync.Mutex
	factories = map[string]Factory{}
)

// Register makes a platform available to `hermes gateway`. Platforms register explicitly from the command, not at
// import time.
func Register(name string, f Factory) {
	regMu.Lock()
	defer regMu.Unlock()
	factories[name] = f
}

// Registered lists the known platforms.
func Registered() []string {
	regMu.Lock()
	defer regMu.Unlock()
	names := make([]string, 0, len(factories))
	for n := range factories {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Build creates the adapters of every platform with gateway.platforms.<name>.enabled set.
func Build(cfg *config.Config, secrets *config.Secrets) ([]Adapter, error) {
	regMu.Lock()
	defer regMu.Unlock()
	var out []Adapter
	for name, f := range factories {
		if !cfg.Bool("gateway.platforms."+name+".enabled", false) {
			continue
		}
		a, err := f(cfg, secrets)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out, nil
}

// splitText cuts text into pieces of at most n characters, preferring paragraph, then line, then word breaks.
func splitText(text string, n int) []string {
	r := []rune(text)
	if n <= 0 || len(r) <= n {
		return []string{text}
	}
	var out []string
	for len(r) > n {
		cut := n
		window := string(r[:n])
		for _, sep := range []string{"\n\n", "\n", " "} {
			if i := lastIndex(window, sep); i > n/2 {
				cut = len([]rune(window[:i])) + len([]rune(sep))
				break
			}
		}
		out = append(out, string(r[:cut]))
		r = r[cut:]
	}
	if len(r) > 0 {
		out = append(out, string(r))
	}
	return out
}

func lastIndex(s, sep string) int {
	for i := len(s) - len(sep); i >= 0; i-- {
		if s[i:i+len(sep)] == sep {
			return i
		}
	}
	return -1
}
