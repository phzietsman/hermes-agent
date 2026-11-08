package agent

import (
	"strings"

	"github.com/phzietsman/hermes-agent/internal/llm"
)

// Anthropic prompt caching strategy: pure functions over messages and tools, request-local. The stored
// transcript is never decorated; a plan is a copy-on-write view with cache_control markers added.
//
// Native Anthropic route only. With tools, the layout is four breakpoints: the static system prefix, the
// last tool, and the last two completed transaction endpoints (the end of a finished tool run or an
// ordinary turn). Without tools it is the legacy system-and-3 layout: the system prompt split at its
// static prefix, then the latest messages. All markers in a request share one TTL (5m or 1h).

// CacheTTL values.
const (
	CacheTTL5m = "5m"
	CacheTTL1h = "1h"
)

// machinePacedSources never wait long between turns, so the 1h tier (written at 2x base against 1.25x for
// 5m) never pays back: they stay on 5m.
var machinePacedSources = setOf("subagent", "cron", "oneshot", "webhook", "kanban", "api", "tool", "batch")

func setOf(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, i := range items {
		m[i] = true
	}
	return m
}

// AutoCacheTTLForSource is the tier prompt_caching.cache_ttl "auto" resolves to: 5m for machine-paced
// sources, 1h for everything a human types into (cli, tui, desktop, messaging platforms). The 1h tier only
// pays off when turns are more than five minutes apart: a person stepping away and coming back.
func AutoCacheTTLForSource(source string) string {
	if machinePacedSources[strings.ToLower(strings.TrimSpace(source))] {
		return CacheTTL5m
	}
	return CacheTTL1h
}

// EffectiveCacheTTL clamps a requested TTL to what the native route supports. An empty TTL is 5m.
func EffectiveCacheTTL(ttl string) string {
	if ttl == CacheTTL1h {
		return CacheTTL1h
	}
	if ttl == "" {
		return CacheTTL5m
	}
	return ttl
}

// CachePlan is the request-local message and tool sections with their cache markers.
type CachePlan struct {
	Messages []llm.Message
	Tools    []llm.ToolDef
}

// CacheOptions configures a plan.
type CacheOptions struct {
	// TTL is "5m" (default) or "1h".
	TTL string
	// StaticSystemPrefix is the stable tier of the system prompt. When the system message starts with it,
	// the prefix gets its own breakpoint.
	StaticSystemPrefix string
	// DirectNativeToolCache selects the tool-cache layout (the last tool carries a marker). It is true on
	// api.anthropic.com, whose tools array accepts markers.
	DirectNativeToolCache bool
	// Prefixes is the stable-prefix registry; nil uses DefaultStablePrefixes.
	Prefixes *stablePrefixes
}

func (o CacheOptions) prefixes() *stablePrefixes {
	if o.Prefixes != nil {
		return o.Prefixes
	}
	return DefaultStablePrefixes
}

func buildMarker(ttl string) llm.CacheControl {
	if ttl == CacheTTL1h {
		return llm.CacheControl{Type: "ephemeral", TTL: CacheTTL1h}
	}
	return llm.CacheControl{Type: "ephemeral"}
}

func textPart(text string, marker *llm.CacheControl) llm.Block {
	p := llm.Block{"type": "text", "text": text}
	if marker != nil {
		p["cache_control"] = marker.Map()
	}
	return p
}

func noneOrEmpty(c llm.Content) bool {
	return !c.IsParts() && c.AsMultimodal() == nil && c.AsText() == ""
}

func hasCC(p llm.Block) bool { _, ok := p["cache_control"]; return ok }

// scaffoldParts is [scaffold, tail] for a string user message with a registered scaffold, the marker (if
// any) on the scaffold. Find keeps the tail non-blank.
func scaffoldParts(m llm.Message, marker *llm.CacheControl, reg *stablePrefixes) []llm.Block {
	if m.Role != llm.RoleUser || m.Content.IsParts() || m.Content.AsMultimodal() != nil {
		return nil
	}
	text := m.Content.AsText()
	prefix, ok := reg.Find(text)
	if !ok {
		return nil
	}
	return []llm.Block{textPart(prefix, marker), textPart(text[len(prefix):], nil)}
}

// applyMarker adds a cache marker to one (already copied) message, handling every content shape.
func applyMarker(m *llm.Message, marker llm.CacheControl, reg *stablePrefixes) {
	switch {
	case m.Role == llm.RoleTool || noneOrEmpty(m.Content):
		// A role:tool message takes a top-level marker that the adapter moves inside the tool_result; empty
		// content has no part to carry one.
		m.CacheControl = &marker
	case !m.Content.IsParts() && m.Content.AsMultimodal() == nil:
		// A builder-declared boundary: the scaffold carries the breakpoint and the volatile tail rides
		// unmarked. Request-local only: the stored message stays a string.
		parts := scaffoldParts(*m, &marker, reg)
		if parts == nil {
			parts = []llm.Block{textPart(m.Content.AsText(), &marker)}
		}
		m.Content = llm.Parts(parts...)
	case m.Content.IsParts() && len(m.Content.AsParts()) > 0:
		parts := m.Content.AsParts()
		parts[len(parts)-1]["cache_control"] = marker.Map()
	}
}

// splitUnmarkedScaffolds sends every registered scaffold message as [scaffold, tail] parts, marked or not.
// Provider caches key on content, and two text parts are different content from one string: a skill message
// split while it held a breakpoint must keep the same parts after the rolling marker window moves past it,
// or the next request re-writes the whole scaffold.
func splitUnmarkedScaffolds(msgs []llm.Message, reg *stablePrefixes) {
	for i, m := range msgs {
		if parts := scaffoldParts(m, nil, reg); parts != nil {
			c := m
			c.Content = llm.Parts(parts...)
			msgs[i] = c
		}
	}
}

// applySystemMarkers marks the static system prefix (and optionally the full prompt) and returns the number
// of markers applied. The stored system prompt stays one string, split only in the request. markSuffix
// false is the tool-cache layout (the suffix budget is spent on the tools array); fallbackToWhole false
// marks nothing when the split is impossible. When the prompt IS the prefix the whole message is one block:
// never an empty text block (400).
func applySystemMarkers(m *llm.Message, marker llm.CacheControl, prefix string, markSuffix, fallbackToWhole bool, reg *stablePrefixes) int {
	if prefix != "" && !m.Content.IsParts() && m.Content.AsMultimodal() == nil && strings.HasPrefix(m.Content.AsText(), prefix) {
		suffix := m.Content.AsText()[len(prefix):]
		if strings.TrimSpace(suffix) != "" {
			var suffixMarker *llm.CacheControl
			if markSuffix {
				suffixMarker = &marker
			}
			m.Content = llm.Parts(textPart(prefix, &marker), textPart(suffix, suffixMarker))
			if markSuffix {
				return 2
			}
			return 1
		}
	} else if !fallbackToWhole {
		return 0
	}
	applyMarker(m, marker, reg)
	return 1
}

func hasPartMarker(parts []llm.Block) bool {
	for _, p := range parts {
		if hasCC(p) {
			return true
		}
	}
	return false
}

func isPlainTextPart(p llm.Block) bool {
	if t, ok := p["type"]; ok && t != "text" {
		return false
	}
	if _, ok := p["text"].(string); !ok {
		return false
	}
	for k := range p {
		if k != "type" && k != "text" {
			return false
		}
	}
	return true
}

// stripOne removes cache_control markers and undoes the list shapes decoration produces, on a copy.
// Flattening back to a string is restricted to the exact shapes the planner produces from string content (a
// single text part, the two-part system split, the two-part skill split) so the "" join is byte-exact. Marker
// removal is copy-on-write on part maps: parts can alias caller-held lists, and stripping must never rewrite
// the stored transcript.
func stripOne(m llm.Message, reg *stablePrefixes) llm.Message {
	m.CacheControl = nil
	if !m.Content.IsParts() {
		return m
	}
	parts := m.Content.AsParts()
	userPair := m.Role == llm.RoleUser && len(parts) == 2
	// The skill split is the only decoration marking the FIRST part of a user message, so the marked shape
	// identifies it even after the prefix registry evicted the entry.
	firstPartMarked := userPair && hasCC(parts[0]) && !hasCC(parts[1])
	if hasPartMarker(parts) {
		stripped := make([]llm.Block, len(parts))
		for i, p := range parts {
			if hasCC(p) {
				np := make(llm.Block, len(p))
				for k, v := range p {
					if k != "cache_control" {
						np[k] = v
					}
				}
				p = np
			}
			stripped[i] = p
		}
		parts = stripped
	}
	plain := len(parts) > 0
	for _, p := range parts {
		plain = plain && isPlainTextPart(p)
	}
	if !plain {
		m.Content = llm.Parts(parts...)
		return m
	}
	var joined strings.Builder
	for _, p := range parts {
		joined.WriteString(p["text"].(string))
	}
	skillSplit := firstPartMarked
	if !skillSplit && userPair {
		if prefix, ok := reg.Find(joined.String()); ok && prefix == parts[0]["text"] {
			skillSplit = true
		}
	}
	if len(parts) == 1 || (m.Role == llm.RoleSystem && len(parts) == 2) || skillSplit {
		m.Content = llm.Text(joined.String())
	} else {
		m.Content = llm.Parts(parts...)
	}
	return m
}

// StripCacheControl removes request-local markers and decoration shapes from copies of the messages.
func StripCacheControl(msgs []llm.Message, prefixes *stablePrefixes) []llm.Message {
	if prefixes == nil {
		prefixes = DefaultStablePrefixes
	}
	out := make([]llm.Message, len(msgs))
	for i, m := range msgs {
		out[i] = stripOne(m, prefixes)
	}
	return out
}

// stripPriorDecoration re-canonicalises planner output before re-planning. Every list-content message is
// stripped, not just marked ones: an UNMARKED skill split (outside the previous request's marker window)
// left as two parts would get the new breakpoint on its volatile tail instead of the scaffold.
func stripPriorDecoration(msgs []llm.Message, reg *stablePrefixes) []llm.Message {
	out := make([]llm.Message, len(msgs))
	copy(out, msgs)
	for i, m := range out {
		if m.CacheControl != nil || m.Content.IsParts() {
			out[i] = stripOne(m, reg)
		}
	}
	return out
}

// StripToolCacheControl returns copied tools without request-local markers.
func StripToolCacheControl(tools []llm.ToolDef) []llm.ToolDef {
	out := make([]llm.ToolDef, len(tools))
	for i, t := range tools {
		out[i] = t.Clone()
		out[i].CacheControl = nil
	}
	return out
}

// completedTransactionEndpoints selects the legal ends of completed tool runs and ordinary turns: the
// last result of an assistant tool-call run, and a finished user or assistant message. An open turn (a
// user message awaiting its reply, an empty assistant message) is not an endpoint.
func completedTransactionEndpoints(msgs []llm.Message) []int {
	toolRunEnd := func(start int) int {
		end := start
		for end < len(msgs) && msgs[end].Role == llm.RoleTool {
			end++
		}
		return end
	}
	var endpoints []int
	for i := 0; i < len(msgs); {
		m := msgs[i]
		if m.Role == llm.RoleSystem {
			i++
			continue
		}
		if m.Role == llm.RoleAssistant && len(m.ToolCalls) > 0 {
			end := toolRunEnd(i + 1)
			if end > i+1 {
				endpoints = append(endpoints, end-1)
			}
			i = end
			continue
		}
		if m.Role == llm.RoleTool {
			i = toolRunEnd(i)
			continue
		}
		openTurn := (m.Role == llm.RoleUser && i+1 < len(msgs)) || (m.Role == llm.RoleAssistant && noneOrEmpty(m.Content))
		if !openTurn {
			endpoints = append(endpoints, i)
		}
		i++
	}
	return endpoints
}

func lastN(xs []int, n int) []int {
	if n >= len(xs) {
		return xs
	}
	return xs[len(xs)-n:]
}

// BuildPromptCachePlan builds the copy-on-write cache sections for one native Anthropic request. The
// inputs are never modified. Without a tools array (or without DirectNativeToolCache) the legacy
// system-and-3 layout applies.
func BuildPromptCachePlan(msgs []llm.Message, tools []llm.ToolDef, o CacheOptions) CachePlan {
	reg := o.prefixes()
	plannedTools := StripToolCacheControl(tools)
	if !o.DirectNativeToolCache || len(plannedTools) == 0 {
		return CachePlan{Messages: ApplyCacheControl(msgs, o), Tools: plannedTools}
	}
	messages := stripPriorDecoration(msgs, reg)
	marker := buildMarker(o.TTL)
	if len(messages) > 0 && messages[0].Role == llm.RoleSystem {
		// Tool-cache layout: only the static prefix carries a system-side marker; the volatile suffix's
		// budget is spent on the tools array.
		c := messages[0].Clone()
		applySystemMarkers(&c, marker, o.StaticSystemPrefix, false, false, reg)
		messages[0] = c
	}
	cc := marker
	plannedTools[len(plannedTools)-1].CacheControl = &cc
	for _, idx := range lastN(completedTransactionEndpoints(messages), 2) {
		c := messages[idx].Clone()
		applyMarker(&c, marker, reg)
		messages[idx] = c
	}
	splitUnmarkedScaffolds(messages, reg)
	return CachePlan{Messages: messages, Tools: plannedTools}
}

// ApplyCacheControl applies the legacy layout: with a matching static system prefix, the prefix and the
// full system prompt each get a marker and the remaining two go to the latest non-system messages;
// otherwise the system prompt gets one and the last three messages the rest. It is idempotent: prior
// decoration is stripped from a per-message copy first, so a second call (or messages a prior call already
// planned) can never accumulate past four markers. It returns a list copy with deep copies of modified messages.
func ApplyCacheControl(msgs []llm.Message, o CacheOptions) []llm.Message {
	if len(msgs) == 0 {
		return msgs
	}
	reg := o.prefixes()
	messages := stripPriorDecoration(msgs, reg)
	marker := buildMarker(o.TTL)
	used := 0
	if messages[0].Role == llm.RoleSystem {
		c := messages[0].Clone()
		used = applySystemMarkers(&c, marker, o.StaticSystemPrefix, true, true, reg)
		messages[0] = c
	}
	var nonSystem []int
	for i, m := range messages {
		if m.Role != llm.RoleSystem {
			nonSystem = append(nonSystem, i)
		}
	}
	for _, idx := range lastN(nonSystem, 4-used) {
		c := messages[idx].Clone()
		applyMarker(&c, marker, reg)
		messages[idx] = c
	}
	splitUnmarkedScaffolds(messages, reg)
	return messages
}
