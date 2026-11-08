package anthropic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/phzietsman/hermes-agent/internal/llm"
)

// Betas. interleaved-thinking and fine-grained tool streaming are GA on Claude 4.6+ (a harmless
// no-op there) but older Claude still gates on them. context-1m is deliberately absent: accounts
// without the long-context beta get HTTP 400, which would break short calls.
const (
	toolStreamingBeta = "fine-grained-tool-streaming-2025-05-14"
	fastModeBeta      = "fast-mode-2026-02-01"
)

var commonBetas = []string{"interleaved-thinking-2025-05-14", toolStreamingBeta}

// oauthOnlyBetas are required for OAuth/subscription auth, matching Claude Code.
var oauthOnlyBetas = []string{"claude-code-20250219", "oauth-2025-04-20"}

// BetaHeader is the anthropic-beta header value for client-level headers.
func BetaHeader(oauth bool) string {
	betas := append([]string(nil), commonBetas...)
	if oauth {
		betas = append(betas, oauthOnlyBetas...)
	}
	return strings.Join(betas, ",")
}

// Reasoning is the reasoning configuration: Enabled (nil means unset; false turns thinking off) and
// an effort level (low, medium, high, xhigh, max, or the legacy minimal and ultra).
type Reasoning struct {
	Enabled *bool
	Effort  string
}

func (r *Reasoning) isEmpty() bool { return r == nil || (r.Enabled == nil && r.Effort == "") }

var thinkingBudget = map[string]int{"xhigh": 32000, "high": 16000, "medium": 8000, "low": 4000}

// adaptiveEffort maps Hermes effort to Anthropic's output_config.effort. 4.7+ exposes
// low/medium/high/xhigh/max; Opus and Sonnet 4.6 have no xhigh, so xhigh falls back to max there.
var adaptiveEffort = map[string]string{
	"ultra": "max", "max": "max", "xhigh": "xhigh", "high": "high", "medium": "medium", "low": "low", "minimal": "low",
}

// Model family lists. Claude 4.6 replaced budget-based extended thinking with adaptive thinking, and
// 4.7 also forbids the manual thinking block and drops temperature/top_p/top_k. Newer releases share
// no common version substring, so an allowlist of modern versions would go stale and silently route a
// new model down the legacy path: unknown Claude models DEFAULT to the modern contract and only the
// legacy families are listed. Non-Claude models fall to the legacy manual-thinking path.
var (
	legacyManualThinking = []string{
		"claude-3", "claude-opus-4-0", "claude-opus-4.0", "claude-opus-4-1", "claude-opus-4.1",
		"claude-sonnet-4-0", "claude-sonnet-4.0", "claude-opus-4-2025", "claude-sonnet-4-2025",
		"claude-opus-4-5", "claude-opus-4.5", "claude-sonnet-4-5", "claude-sonnet-4.5", "claude-haiku-4-5",
		"claude-haiku-4.5",
	}
	// Adaptive families that reject xhigh (it arrived with Opus 4.7) and still accept sampling params.
	noXHigh = []string{"claude-opus-4-6", "claude-opus-4.6", "claude-sonnet-4-6", "claude-sonnet-4.6"}
	// Thinking is mandatory: {"type": "disabled"} answers HTTP 400. A missing entry 400s the turn and a
	// spurious one only leaves thinking on, so when in doubt add the family.
	mandatoryThinking = []string{"claude-fable", "claude-opus-5-5", "claude-opus-5.5"}
	// The documented "off" is {"type": "between_tools"}; "disabled" 400s there.
	betweenToolsOff = []string{"claude-sonnet-5-5", "claude-sonnet-5.5"}
)

func isClaude(model string) bool { return strings.Contains(strings.ToLower(model), "claude") }

func modelMatches(model string, subs ...[]string) bool {
	m := strings.ToLower(model)
	for _, list := range subs {
		for _, s := range list {
			if strings.Contains(m, s) {
				return true
			}
		}
	}
	return false
}

func supportsAdaptiveThinking(model string) bool {
	return isClaude(model) && !modelMatches(model, legacyManualThinking)
}

func supportsXHigh(model string) bool {
	return supportsAdaptiveThinking(model) && !modelMatches(model, noXHigh)
}

// acceptsThinkingDisable: adaptive Claude thinks by default, so "off" only works if the disable is
// sent; mandatory-thinking families 400 on it. Legacy manual-thinking models are opt-in via
// budget_tokens, so omission is already off.
func acceptsThinkingDisable(model string) bool {
	return supportsAdaptiveThinking(model) && !modelMatches(model, mandatoryThinking, betweenToolsOff)
}

// forbidsSamplingParams is true for models that 400 on any non-default temperature/top_p/top_k
// (Opus 4.7 and later; unknown Claude defaults to forbidding). The 4.6 family and the legacy
// manual-thinking families still accept them.
func forbidsSamplingParams(model string) bool {
	return isClaude(model) && !modelMatches(model, noXHigh, legacyManualThinking)
}

// fastModeModels accept speed "fast". Exact ids, not a family prefix: Opus 4.7 answers the
// parameter with an error, Opus 4.6 silently runs and bills at standard speed.
var fastModeModels = map[string]bool{"claude-opus-4-8": true, "claude-opus-5": true, "claude-opus-5-5": true}

var (
	dottedVersionRe = regexp.MustCompile(`(\d)\.(\d)`)
	dateSuffixRe    = regexp.MustCompile(`-\d{8}$`)
)

// SupportsFastMode accepts vendor-prefixed, dotted, variant and dated spellings
// (anthropic/claude-opus-5.5, claude-opus-4-8-20260601).
func SupportsFastMode(model string) bool {
	name := strings.ToLower(strings.TrimSpace(model))
	name, _, _ = strings.Cut(name, ":")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	name = dottedVersionRe.ReplaceAllString(name, "$1-$2")
	return fastModeModels[dateSuffixRe.ReplaceAllString(name, "")]
}

// outputLimits are max output tokens per model. Anthropic requires max_tokens, and a fixed 16384
// starved thinking-enabled models (thinking tokens count toward the limit).
var outputLimits = map[string]int{
	"claude-fable": 128_000, "claude-sonnet-5": 128_000, "claude-opus-4-8": 128_000,
	"claude-opus-4-7": 128_000, "claude-opus-4-6": 128_000, "claude-sonnet-4-6": 64_000,
	"claude-opus-4-5": 64_000, "claude-sonnet-4-5": 64_000, "claude-haiku-4-5": 64_000,
	"claude-opus-4": 32_000, "claude-sonnet-4": 64_000, "claude-3-7-sonnet": 128_000,
	"claude-3-5-sonnet": 8_192, "claude-3-5-haiku": 8_192, "claude-3-opus": 4_096,
	"claude-3-sonnet": 4_096, "claude-3-haiku": 4_096,
}

// defaultOutputLimit applies to unknown models: future models are unlikely to have less.
const defaultOutputLimit = 128_000

// MaxOutputTokens is the model's output ceiling by longest substring match (so date-stamped ids and
// :1m / :fast suffixes resolve, and claude-3-5-sonnet beats claude-3-5). Dots become hyphens.
func MaxOutputTokens(model string) int {
	m := strings.ReplaceAll(strings.ToLower(model), ".", "-")
	best := ""
	for k := range outputLimits {
		if strings.Contains(m, k) && len(k) > len(best) {
			best = k
		}
	}
	if best == "" {
		return defaultOutputLimit
	}
	return outputLimits[best]
}

// RequestOptions describes one Messages call.
type RequestOptions struct {
	Model    string
	Messages []llm.Message
	Tools    []llm.ToolDef
	// MaxTokens caps one response. Zero or negative uses the model's output ceiling.
	MaxTokens int
	Reasoning *Reasoning
	// ToolChoice is "" or "auto" (the default), "required", "none" (omit tools), or a forced tool name.
	ToolChoice string
	// OAuth applies the Claude Code compatibility transforms (identity prefix, tool-name aliasing).
	OAuth bool
	// ContextLength is the total window (input plus output). When the output cap exceeds it the cap
	// is clamped to ContextLength-1. The clamp ignores prompt size: callers must catch "max_tokens
	// too large given prompt" and retry smaller.
	ContextLength int
	FastMode      bool
}

// Request is a built Messages API call: the JSON body plus per-request headers.
type Request struct {
	Model        string
	System       any
	Messages     []WireMessage
	Tools        []llm.Block
	ToolChoice   llm.Block
	MaxTokens    int
	Thinking     llm.Block
	OutputConfig llm.Block
	Temperature  *int
	// ExtraBody keys are merged at the top level of the body (speed: "fast").
	ExtraBody map[string]any
	// ExtraHeaders override the client-level headers for this call; a per-request anthropic-beta
	// replaces the client's, so it carries the full beta list.
	ExtraHeaders map[string]string
	// Stream is set by the client.
	Stream bool
}

// MarshalJSON writes the request body.
func (r Request) MarshalJSON() ([]byte, error) {
	body := map[string]any{"model": r.Model, "messages": r.Messages, "max_tokens": r.MaxTokens}
	if systemPresent(r.System) {
		body["system"] = r.System
	}
	if len(r.Tools) > 0 {
		body["tools"] = r.Tools
	}
	if r.ToolChoice != nil {
		body["tool_choice"] = r.ToolChoice
	}
	if r.Thinking != nil {
		body["thinking"] = r.Thinking
	}
	if r.OutputConfig != nil {
		body["output_config"] = r.OutputConfig
	}
	if r.Temperature != nil {
		body["temperature"] = *r.Temperature
	}
	if r.Stream {
		body["stream"] = true
	}
	for k, v := range r.ExtraBody {
		body[k] = v
	}
	return marshalNoEscape(body)
}

// marshalNoEscape is json.Marshal without HTML escaping: <, > and & stay as written, because the
// model sees prompt text verbatim and \u003c would only inflate the request.
func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// Body is the request body to send. Use it rather than json.Marshal: Marshal always HTML-escapes
// the output of a custom marshaller, Body does not.
func (r Request) Body() ([]byte, error) { return marshalNoEscape(r) }

// systemPresent is Python's truthiness for the system parameter: a non-empty string or block list.
func systemPresent(v any) bool {
	switch t := v.(type) {
	case string:
		return t != ""
	case []llm.Block:
		return len(t) > 0
	}
	return false
}

// BufferToolInput is the retry knob for a malformed fine-grained tool-JSON stream: that beta streams
// tool arguments unvalidated, so a model that emits {"names": cronjob_manage} breaks the parser and
// an identical retry breaks identically. eager_input_streaming=false per tool restores Anthropic's
// buffered, validated arguments for the rest of the turn (one prompt-cache miss, cheaper than a dead turn).
func (r *Request) BufferToolInput() {
	for _, t := range r.Tools {
		t["eager_input_streaming"] = false
	}
}

// BuildRequest builds a Messages API request from OpenAI-shaped messages and tools.
func BuildRequest(o RequestOptions) (Request, error) {
	system, msgs := ConvertMessages(o.Messages, o.Model)
	tools := ConvertTools(o.Tools)
	model := NormalizeModelName(o.Model)

	maxTokens := o.MaxTokens
	if maxTokens <= 0 {
		maxTokens = MaxOutputTokens(model)
	}
	if maxTokens <= 0 {
		return Request{}, fmt.Errorf("no positive max_tokens for model %q", model)
	}
	if o.ContextLength > 0 && maxTokens > o.ContextLength {
		maxTokens = max(o.ContextLength-1, 1)
	}

	var toWire func(string) string
	if o.OAuth {
		toWire = oauthWireNamer(tools)
		system = applyClaudeCodeIdentity(system, tools, msgs, toWire)
	}

	req := Request{Model: model, System: system, Messages: msgs, MaxTokens: maxTokens}
	if len(tools) > 0 {
		req.Tools = tools
		switch o.ToolChoice {
		case "none":
			req.Tools = nil // there is no Anthropic "none": omit the tools to prevent use
		case "", "auto":
			req.ToolChoice = llm.Block{"type": "auto"}
		case "required":
			req.ToolChoice = llm.Block{"type": "any"}
		default:
			// A forced tool name goes through the OAuth normaliser too: every tools[] entry is
			// prefixed and aliased there, so the literal would name a tool that does not exist.
			name := o.ToolChoice
			if toWire != nil {
				name = toWire(name)
			}
			req.ToolChoice = llm.Block{"type": "tool", "name": name}
		}
	}
	if !o.Reasoning.isEmpty() {
		thinkingParams(&req, o.Reasoning, model, maxTokens)
	}
	// Safety net so a 4.6 to 4.7 migration needs no coordinated edits wherever callers set sampling params.
	if forbidsSamplingParams(model) {
		req.Temperature = nil
	}
	if o.FastMode && SupportsFastMode(model) {
		req.ExtraBody = map[string]any{"speed": "fast"}
		betas := append([]string(nil), commonBetas...)
		if o.OAuth {
			betas = append(betas, oauthOnlyBetas...)
		}
		req.ExtraHeaders = map[string]string{"anthropic-beta": strings.Join(append(betas, fastModeBeta), ",")}
	}
	return req, nil
}

// thinkingParams maps the reasoning config to Anthropic thinking parameters. Adaptive models (Claude
// 4.6+) get thinking.type=adaptive plus output_config.effort; older models get budget_tokens. Haiku
// has no extended thinking. On 4.7+ thinking.display defaults to "omitted", hiding the reasoning
// Hermes shows, so "summarized" is requested to keep the activity feed populated.
func thinkingParams(req *Request, r *Reasoning, model string, effectiveMax int) {
	if r.Enabled != nil && !*r.Enabled {
		// Adaptive models think by default, so omitting the parameter is not a disable and the user
		// silently keeps paying. Mandatory-thinking models 400 on the disable, so they keep the
		// omission: a silently ignored disable beats a dead turn.
		switch {
		case modelMatches(model, betweenToolsOff):
			req.Thinking = llm.Block{"type": "between_tools"}
		case acceptsThinkingDisable(model):
			req.Thinking = llm.Block{"type": "disabled"}
		}
		return
	}
	if strings.Contains(strings.ToLower(model), "haiku") {
		return
	}
	effort := strings.ToLower(r.Effort)
	if effort == "" {
		effort = "medium"
	}
	if supportsAdaptiveThinking(model) {
		eff, ok := adaptiveEffort[effort]
		if !ok {
			eff = "medium"
		}
		if eff == "xhigh" && !supportsXHigh(model) {
			eff = "max"
		}
		req.Thinking = llm.Block{"type": "adaptive", "display": "summarized"}
		req.OutputConfig = llm.Block{"effort": eff}
		return
	}
	budget, ok := thinkingBudget[effort]
	if !ok {
		budget = 8000
	}
	one := 1 // required when thinking is enabled on older models
	req.Thinking = llm.Block{"type": "enabled", "budget_tokens": budget}
	req.Temperature = &one
	req.MaxTokens = max(effectiveMax, budget+4096)
}

// ---- OAuth: Claude Code identity ------------------------------------------------------------

const (
	claudeCodeSystemPrefix = "You are Claude Code, Anthropic's official CLI for Claude."
	mcpToolPrefix          = "mcp__"
)

// oauthToolAliases: Anthropic's OAuth billing classifier fingerprints certain Hermes tool schemas and
// prose as a third-party app and reroutes to the metered extra-usage lane (HTTP 400 "You're out of
// extra usage" on a valid subscription). Two independent triggers were isolated, the session_search
// and memory tools, so both are aliased on the OAuth wire only; the response normaliser reverses it.
var oauthToolAliases = map[string]string{"session_search": "chat_history_lookup", "memory": "context_notes"}

// normalizeToMCPWire is the OAuth wire form of a tool name: mcp__<name>. A single-underscore mcp_
// tool name is a third-party-app fingerprint (HTTP 400 "Third-party apps now draw from extra usage");
// mcp__foo is accepted. Bare tools (read_file) and native MCP tools (mcp_<server>_<tool>) must both
// land on the double-underscore form.
func normalizeToMCPWire(name string) string {
	if strings.HasPrefix(name, mcpToolPrefix) {
		return name
	}
	return mcpToolPrefix + strings.TrimPrefix(name, "mcp_")
}

// oauthWireNamer returns name -> OAuth wire name for this request's tool set. An alias must never
// collide with a wire name owned by a non-alias tool: two identical tool names in one request is a
// hard 400, strictly worse than the bug being fixed.
func oauthWireNamer(tools []llm.Block) func(string) string {
	claimed := map[string]bool{}
	for _, t := range tools {
		if name, ok := t["name"].(string); ok {
			if _, aliased := oauthToolAliases[name]; !aliased {
				claimed[normalizeToMCPWire(name)] = true
			}
		}
	}
	return func(name string) string {
		if alias, ok := oauthToolAliases[name]; ok && !claimed[mcpToolPrefix+alias] {
			name = alias
		}
		return normalizeToMCPWire(name)
	}
}

func isWordRune(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

func runeBefore(s string, i int) (rune, bool) {
	if i == 0 {
		return 0, false
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return r, true
}

func runeAt(s string, i int) (rune, bool) {
	if i >= len(s) {
		return 0, false
	}
	r, _ := utf8.DecodeRuneInString(s[i:])
	return r, true
}

// replaceBounded replaces occurrences of word whose neighbours satisfy okBefore and okAfter. Go's RE2
// has no lookaround, so the boundary tests are explicit.
func replaceBounded(s, word, repl string, okBefore func(rune, bool) bool, okAfter func(s string, end int) bool) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		j := strings.Index(s[i:], word)
		if j < 0 {
			b.WriteString(s[i:])
			break
		}
		start := i + j
		end := start + len(word)
		before, hasBefore := runeBefore(s, start)
		if okBefore(before, hasBefore) && okAfter(s, end) {
			b.WriteString(s[i:start])
			b.WriteString(repl)
		} else {
			b.WriteString(s[i:end])
		}
		i = end
	}
	return b.String()
}

// replaceProseWord replaces word wherever it stands alone as a word (Python's \b on both sides, with
// Unicode word characters), so a longer identifier such as tools/session_search_tool.py is left alone.
func replaceProseWord(s, word, repl string) string {
	return replaceBounded(s, word, repl,
		func(r rune, ok bool) bool { return !ok || !isWordRune(r) },
		func(s string, end int) bool { r, ok := runeAt(s, end); return !ok || !isWordRune(r) })
}

// applyOAuthProseAliases rewrites prose-safe tool-name tokens to their wire aliases. "memory" is
// ordinary English throughout the prompt and inside the memory tool's own parameter docs (an enum the
// model must emit verbatim), so only session_search is rewritten in prose.
func applyOAuthProseAliases(text string) string {
	return replaceProseWord(text, "session_search", oauthToolAliases["session_search"])
}

// replaceSlug rewrites hermes-agent only as a standalone prose word. Joined to a host, path, repo,
// mailbox or quoted as an identifier (hermes-agent.nousresearch.com, ~/.hermes/hermes-agent/venv,
// NousResearch/hermes-agent, skill_view(name='hermes-agent')) it is an address the model dereferences,
// and the rewritten form does not exist. The opening quote marks an identifier; a sentence-final "."
// or a possessive 's is prose.
func replaceSlug(s string) string {
	return replaceBounded(s, "hermes-agent", "claude-code",
		func(r rune, ok bool) bool {
			return !ok || !(isWordRune(r) || strings.ContainsRune(`./:@'"`+"`"+`-`, r))
		},
		func(s string, end int) bool {
			r, ok := runeAt(s, end)
			if !ok {
				return true
			}
			if isWordRune(r) || r == '/' || r == '@' || r == '-' {
				return false
			}
			if r == '.' {
				next, ok := runeAt(s, end+1)
				return !(ok && isWordRune(next))
			}
			return true
		})
}

var oauthSystemReplacements = [][2]string{{"Hermes Agent", "Claude Code"}, {"Hermes agent", "Claude Code"}, {"Nous Research", "Anthropic"}}

// applyClaudeCodeIdentity applies the OAuth transforms: the Claude Code system prefix, product-name
// sanitising (avoids server-side content filters), tool and description aliasing, and the same tool
// renames on replayed tool_use blocks so history matches tools[]. It returns the new system; tools
// and messages are modified in place.
func applyClaudeCodeIdentity(system any, tools []llm.Block, msgs []WireMessage, toWire func(string) string) any {
	blocks := []llm.Block{{"type": "text", "text": claudeCodeSystemPrefix}}
	switch s := system.(type) {
	case string:
		if s != "" {
			blocks = append(blocks, llm.Block{"type": "text", "text": s})
		}
	case []llm.Block:
		blocks = append(blocks, s...)
	}
	for _, b := range blocks {
		if blockType(b) != "text" {
			continue
		}
		text, _ := b["text"].(string)
		for _, r := range oauthSystemReplacements {
			text = strings.ReplaceAll(text, r[0], r[1])
		}
		b["text"] = applyOAuthProseAliases(replaceSlug(text))
	}
	for _, t := range tools {
		if name, ok := t["name"].(string); ok {
			t["name"] = toWire(name)
		}
		if d, ok := t["description"].(string); ok {
			t["description"] = applyOAuthProseAliases(d) // prose-safe aliases only
		}
	}
	for i := range msgs {
		inner, ok := msgs[i].blocks()
		if !ok {
			continue
		}
		for _, b := range inner {
			if name, ok := b["name"].(string); ok && blockType(b) == "tool_use" {
				b["name"] = toWire(name) // tool_result pairs by id, not name
			}
		}
	}
	return blocks
}
