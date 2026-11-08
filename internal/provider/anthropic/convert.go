package anthropic

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/phzietsman/hermes-agent/internal/llm"
)

const (
	emptyTextPlaceholder = "(empty)"
	emptyUserPlaceholder = "(empty message)"
	noOutputPlaceholder  = "(no output)"
)

var thinkingTypes = map[string]bool{"thinking": true, "redacted_thinking": true}
var cacheableTypes = map[string]bool{"text": true, "tool_use": true}

// WireMessage is one message of the Anthropic request. Content is a string (a plain user turn) or
// a list of content blocks. It marshals to {"role", "content"}.
type WireMessage struct {
	Role    string
	content any // string or []llm.Block
	// thinkingInvalidated marks a turn whose signed thinking was signed against content that has
	// since been edited (an orphaned tool_use was stripped); it never goes on the wire.
	thinkingInvalidated bool
}

// MarshalJSON writes role and content.
func (w WireMessage) MarshalJSON() ([]byte, error) {
	return marshalNoEscape(struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	}{w.Role, w.content})
}

// Content returns the string or []llm.Block content.
func (w WireMessage) Content() any { return w.content }

func (w *WireMessage) blocks() ([]llm.Block, bool) {
	b, ok := w.content.([]llm.Block)
	return b, ok
}

// ---- block helpers ------------------------------------------------------------------------

func blockType(b llm.Block) string {
	t, _ := b["type"].(string)
	return t
}

func hasBlockType(blocks []llm.Block, types map[string]bool) bool {
	for _, b := range blocks {
		if types[blockType(b)] {
			return true
		}
	}
	return false
}

// isBlankText is a text block whose text is not a non-whitespace string. Anthropic 400s on these
// ("text content blocks must contain non-whitespace text"); a missing or non-string text counts.
func isBlankText(b llm.Block) bool {
	if blockType(b) != "text" {
		return false
	}
	s, ok := b["text"].(string)
	return !ok || strings.TrimSpace(s) == ""
}

func cacheControlOf(b llm.Block) llm.Block {
	cc, _ := b["cache_control"].(map[string]any)
	return cc
}

func textBlock(text string) llm.Block { return llm.Block{"type": "text", "text": text} }

// textBlockWithCitations carries citations only when it is a non-empty list, the one input-valid shape.
func textBlockWithCitations(text any, cits any) llm.Block {
	b := llm.Block{"type": "text", "text": text}
	if l, ok := cits.([]any); ok && len(l) > 0 {
		b["citations"] = l
	}
	return b
}

// carryCacheControl copies a dict-valued cache_control marker from src onto out (returned).
func carryCacheControl(out, src llm.Block, copyIt bool) llm.Block {
	if cc := cacheControlOf(src); cc != nil {
		if copyIt {
			cc = cloneBlock(cc)
		}
		out["cache_control"] = cc
	}
	return out
}

// cloneBlock deep-copies JSON-shaped data so the wire payload never aliases the caller's.
func cloneBlock(b llm.Block) llm.Block {
	if b == nil {
		return nil
	}
	return cloneValue(b).(llm.Block)
}

func cloneValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = cloneValue(x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = cloneValue(x)
		}
		return out
	case []llm.Block:
		out := make([]llm.Block, len(t))
		for i, x := range t {
			out[i] = cloneBlock(x)
		}
		return out
	}
	return v
}

// parseToolArgs decodes a tool call's JSON arguments. Bad or empty JSON becomes {}.
func parseToolArgs(raw string) any {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return map[string]any{}
	}
	return v
}

var badToolIDChars = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// sanitizeToolID maps an id to Anthropic's [a-zA-Z0-9_-] alphabet; it is never empty.
func sanitizeToolID(id string) string {
	if id = badToolIDChars.ReplaceAllString(id, "_"); id != "" {
		return id
	}
	return "tool_0"
}

func toolUseBlock(id, name string, input any) llm.Block {
	return llm.Block{"type": "tool_use", "id": sanitizeToolID(id), "name": name, "input": input}
}

func stripThinking(blocks []llm.Block) []llm.Block {
	var out []llm.Block
	for _, b := range blocks {
		if !thinkingTypes[blockType(b)] {
			out = append(out, b)
		}
	}
	return out
}

func blockIDs(blocks []llm.Block, btype, key string) map[string]bool {
	ids := map[string]bool{}
	for _, b := range blocks {
		if blockType(b) == btype {
			id, _ := b[key].(string)
			ids[id] = true
		}
	}
	return ids
}

// splitBlankText drops blank text blocks and returns the cache_control of the last one dropped, so
// the caller can relocate the breakpoint, plus the dropped indexes.
func splitBlankText(blocks []llm.Block) (kept []llm.Block, relocated llm.Block, dropped []int) {
	for i, b := range blocks {
		if isBlankText(b) {
			dropped = append(dropped, i)
			if cc := cacheControlOf(b); cc != nil {
				relocated = cc
			}
			continue
		}
		kept = append(kept, b)
	}
	return kept, relocated, dropped
}

// applyCacheToLastCacheable sets the marker on the last text or tool_use block that has none.
func applyCacheToLastCacheable(blocks []llm.Block, cc llm.Block) {
	if cc == nil {
		return
	}
	for i := len(blocks) - 1; i >= 0; i-- {
		if cacheableTypes[blockType(blocks[i])] {
			if _, has := blocks[i]["cache_control"]; !has {
				blocks[i]["cache_control"] = cloneBlock(cc)
			}
			return
		}
	}
}

// ---- tools --------------------------------------------------------------------------------

var emptySchema = func() llm.Block { return llm.Block{"type": "object", "properties": map[string]any{}} }

func isNullBranch(v any) bool {
	m, ok := v.(map[string]any)
	return ok && m["type"] == "null"
}

var unionMetaKeys = []string{"title", "description", "default", "examples"}

// stripNullableUnions collapses anyOf/oneOf nullable unions (MCP and Pydantic optional fields) to the
// single non-null branch: Anthropic rejects the null branch and optionality already lives in the
// parent's required list. It applies only when a null branch was dropped and exactly one branch
// survives. Bottom-up, so a surviving union is itself collapsed.
func stripNullableUnions(schema any) any {
	switch t := schema.(type) {
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = stripNullableUnions(x)
		}
		return out
	case map[string]any:
		node := make(map[string]any, len(t))
		for k, v := range t {
			node[k] = stripNullableUnions(v)
		}
		for _, key := range []string{"anyOf", "oneOf"} {
			variants, ok := node[key].([]any)
			if !ok {
				continue
			}
			var nonNull []any
			for _, item := range variants {
				if !isNullBranch(item) {
					nonNull = append(nonNull, item)
				}
			}
			if len(nonNull) == 1 && len(nonNull) != len(variants) {
				repl := map[string]any{}
				if m, ok := nonNull[0].(map[string]any); ok {
					for k, v := range m {
						repl[k] = v
					}
				}
				for _, mk := range unionMetaKeys {
					_, has := repl[mk]
					_, outerHas := node[mk]
					_, isRef := repl["$ref"]
					if outerHas && !has && !(mk == "default" && isRef) {
						repl[mk] = node[mk]
					}
				}
				return stripNullableUnions(repl)
			}
		}
		return node
	}
	return schema
}

// normalizeToolInputSchema prepares a tool schema for Anthropic's validator: nullable unions are
// collapsed, and top-level oneOf/allOf/anyOf (rejected with a generic 400) are dropped in favour of a
// plain object.
func normalizeToolInputSchema(schema llm.Block) llm.Block {
	if len(schema) == 0 {
		return emptySchema()
	}
	norm, ok := stripNullableUnions(map[string]any(schema)).(map[string]any)
	if !ok {
		return emptySchema()
	}
	for _, banned := range []string{"oneOf", "allOf", "anyOf"} {
		if _, has := norm[banned]; has {
			delete(norm, banned)
			if _, hasType := norm["type"]; !hasType {
				norm["type"] = "object"
			}
		}
	}
	if norm["type"] == "object" {
		if _, ok := norm["properties"].(map[string]any); !ok {
			norm["properties"] = map[string]any{}
		}
	}
	return norm
}

// ConvertTools converts tool definitions to the Anthropic format. A duplicate name is dropped with
// a warning (Anthropic hard-400s on them); cache_control on a tool is forwarded.
func ConvertTools(tools []llm.ToolDef) []llm.Block {
	var out []llm.Block
	seen := map[string]bool{}
	for _, t := range tools {
		if t.Name != "" {
			if seen[t.Name] {
				slog.Warn("duplicate tool name; dropping the second occurrence", "tool", t.Name)
				continue
			}
			seen[t.Name] = true
		}
		b := llm.Block{"name": t.Name, "description": t.Description, "input_schema": normalizeToolInputSchema(t.Parameters)}
		if t.CacheControl != nil {
			b["cache_control"] = t.CacheControl.Map()
		}
		out = append(out, b)
	}
	return out
}

// ---- content ------------------------------------------------------------------------------

// convertPart converts one OpenAI-style content part to an Anthropic block (nil drops it).
func convertPart(part llm.Block) llm.Block {
	if part == nil {
		return nil
	}
	var block llm.Block
	switch ptype, _ := part["type"].(string); ptype {
	case "input_text", "text":
		// Rebuilt from whitelisted fields only: stored SDK text blocks carry output-only siblings
		// (parsed_output, citations=None) that the input schema rejects with a 400.
		var cits any
		if ptype == "text" {
			cits = part["citations"]
		}
		text, present := part["text"]
		if !present {
			text = ""
		}
		block = textBlockWithCitations(text, cits)
	case "image_url", "input_image":
		url := ""
		switch v := part["image_url"].(type) {
		case map[string]any:
			url, _ = v["url"].(string)
		case string:
			url = v
		}
		block = imageBlockFromURL(url)
	default:
		block = cloneBlock(part)
	}
	if cc := cacheControlOf(part); cc != nil {
		if _, has := block["cache_control"]; !has {
			block["cache_control"] = cloneBlock(cc)
		}
	}
	return block
}

func convertParts(parts []llm.Block) []llm.Block {
	var out []llm.Block
	for _, p := range parts {
		if b := convertPart(p); b != nil {
			out = append(out, b)
		}
	}
	return out
}

// toolResultParts keeps only text and image blocks, the types Anthropic accepts inside a tool_result.
func toolResultParts(parts []llm.Block) []llm.Block {
	var out []llm.Block
	for _, p := range parts {
		b := convertPart(p)
		if b == nil {
			continue
		}
		switch blockType(b) {
		case "text":
			if s, ok := b["text"].(string); ok && s != "" {
				out = append(out, textBlock(s))
			}
		case "image":
			if src, ok := b["source"].(map[string]any); ok && len(src) > 0 {
				out = append(out, llm.Block{"type": "image", "source": src})
			}
		}
	}
	return out
}

// ---- replay of stored assistant blocks ----------------------------------------------------

func replayText(b llm.Block) llm.Block {
	// Blank blocks are dropped rather than coerced in place: the caller relocates any cache_control
	// and falls back to a placeholder only when nothing survives, so "(empty)" never sits as
	// model-visible noise next to real blocks.
	if isBlankText(b) {
		return nil
	}
	return carryCacheControl(textBlockWithCitations(b["text"], b["citations"]), b, false)
}

func replayThinking(b llm.Block) llm.Block {
	thinking, _ := b["thinking"].(string)
	out := llm.Block{"type": "thinking", "thinking": thinking}
	if sig, _ := b["signature"].(string); sig != "" {
		out["signature"] = sig
	}
	return out
}

func replayRedacted(b llm.Block) llm.Block {
	if data, _ := b["data"].(string); data != "" {
		return llm.Block{"type": "redacted_thinking", "data": data}
	}
	return nil
}

func replayToolUse(b llm.Block) llm.Block {
	id, _ := b["id"].(string)
	name, _ := b["name"].(string)
	input, ok := b["input"]
	if !ok {
		input = map[string]any{}
	}
	return carryCacheControl(toolUseBlock(id, name, input), b, false)
}

func replayImage(b llm.Block) llm.Block {
	if src, ok := b["source"].(map[string]any); ok {
		return llm.Block{"type": "image", "source": src}
	}
	return nil
}

// sanitizeReplayBlock whitelists a stored Anthropic block so it is valid as request input. SDK
// response blocks carry output-only fields the input schema forbids ("Extra inputs are not
// permitted": parsed_output, caller, citations=None). Whitelisting per type, not blacklisting, keeps
// future SDK fields from reintroducing the bug; unknown types are dropped.
func sanitizeReplayBlock(b llm.Block) llm.Block {
	switch blockType(b) {
	case "text":
		return replayText(b)
	case "thinking":
		return replayThinking(b)
	case "redacted_thinking":
		return replayRedacted(b)
	case "tool_use":
		return replayToolUse(b)
	case "image":
		return replayImage(b)
	}
	return nil
}

// replayOrdered rebuilds an assistant turn from the verbatim block list the response stored (kept
// only for turns that interleave signed thinking with tool_use), preserving block order. A tool_use
// input is re-sourced from tool_calls, which were redacted at storage time, rather than from the
// captured block (the raw API response, not redacted), so a secret the model inlined into a tool call
// never goes back on the wire. It returns nil when nothing survives.
func replayOrdered(m llm.Message, ordered []llm.Block) []llm.Block {
	redactedInput := map[string]any{}
	for _, tc := range m.ToolCalls {
		redactedInput[sanitizeToolID(tc.ID)] = parseToolArgs(orEmptyObject(tc.Function.Arguments))
	}
	var replayed []llm.Block
	var relocated llm.Block
	droppedBlankText := false
	for _, b := range ordered {
		clean := sanitizeReplayBlock(b)
		if clean == nil {
			droppedBlankText = droppedBlankText || blockType(b) == "text"
			if cc := cacheControlOf(b); cc != nil {
				relocated = cc
			}
			continue
		}
		if blockType(clean) == "tool_use" {
			id, _ := clean["id"].(string)
			if red, ok := redactedInput[id]; ok {
				clean["input"] = red
			}
		}
		replayed = append(replayed, clean)
	}
	// Nothing cacheable survived (signed thinking plus blank text): emit the placeholder so the turn
	// stays schema-valid and a relocated marker has a carrier.
	if !hasBlockType(replayed, cacheableTypes) && (droppedBlankText || relocated != nil) {
		replayed = append(replayed, textBlock(emptyTextPlaceholder))
	}
	if len(replayed) == 0 {
		return nil
	}
	applyCacheToLastCacheable(replayed, relocated)
	if m.CacheControl != nil {
		applyCacheToLastCacheable(replayed, m.CacheControl.Map())
	}
	// prompt caching marks an assistant turn that has text by writing cache_control into its content
	// parts, not at the top level. This path never reads content, so carry that marker over or the
	// breakpoint is burned instead of relocated.
	for _, p := range m.Content.AsParts() {
		if cc := cacheControlOf(p); cc != nil {
			applyCacheToLastCacheable(replayed, cc)
			break
		}
	}
	return replayed
}

func orEmptyObject(s string) string {
	if s == "" {
		return "{}"
	}
	return s
}

// extractThinking deep-copies the thinking and redacted_thinking blocks from reasoning_details.
func extractThinking(m llm.Message) []llm.Block {
	var out []llm.Block
	for _, d := range m.ReasoningDetails {
		t, _ := d["type"].(string)
		if thinkingTypes[strings.ToLower(strings.TrimSpace(t))] {
			out = append(out, cloneBlock(d))
		}
	}
	return out
}

// assistantReplayCarrier returns the sanitised ordered sidecar when any block survives it (the whole
// turn then replays from it), else the reasoning_details thinking blocks.
func assistantReplayCarrier(m llm.Message) (ordered bool, blocks []llm.Block) {
	if len(m.ContentBlocks) > 0 {
		if replayed := replayOrdered(m, m.ContentBlocks); len(replayed) > 0 {
			return true, replayed
		}
	}
	return false, extractThinking(m)
}

func convertAssistant(m llm.Message) *WireMessage {
	ordered, blocks := assistantReplayCarrier(m)
	if ordered {
		return &WireMessage{Role: "assistant", content: blocks}
	}
	// Blank text blocks are dropped; a cache marker riding on one is relocated onto the last
	// surviving cacheable block (prompt caching sets cache_control on content[-1], which may be
	// exactly the blank block).
	var relocated llm.Block
	switch {
	case m.Content.IsParts():
		var kept []llm.Block
		kept, relocated, _ = splitBlankText(convertParts(m.Content.AsParts()))
		blocks = append(blocks, kept...)
	case strings.TrimSpace(m.Content.AsText()) != "":
		blocks = append(blocks, textBlock(m.Content.AsText()))
	}
	for _, tc := range m.ToolCalls {
		if tc == (llm.ToolCall{}) {
			continue
		}
		blocks = append(blocks, toolUseBlock(tc.ID, tc.Function.Name, parseToolArgs(orEmptyObject(tc.Function.Arguments))))
	}
	// Kimi's /coding endpoint requires reasoning_content on replayed tool-call turns, even "".
	// Thinking must precede text and tool_use, so it is prepended. It is skipped when
	// reasoning_details already supplied signed thinking: an unsigned duplicate would be demoted to a
	// spurious text block on the last assistant message.
	if m.ReasoningContent != nil && !hasBlockType(blocks, thinkingTypes) {
		blocks = append([]llm.Block{{"type": "thinking", "thinking": *m.ReasoningContent}}, blocks...)
	}
	// An empty assistant turn is rejected. Fall back only to the placeholder, never to raw content
	// (the unfiltered blank payload just removed). Markers apply after the fallback so one from a
	// sole dropped blank block lands on the placeholder.
	if len(blocks) == 0 {
		blocks = []llm.Block{textBlock(emptyTextPlaceholder)}
	}
	applyCacheToLastCacheable(blocks, relocated)
	if m.CacheControl != nil {
		applyCacheToLastCacheable(blocks, m.CacheControl.Map())
	}
	return &WireMessage{Role: "assistant", content: blocks}
}

// toolResultContent resolves a tool message into tool_result content: blocks when it carries
// images, else a string.
func toolResultContent(m llm.Message) any {
	var multi []llm.Block
	resolved := false // a multimodal result that resolves to nothing must not fall back to stashed blocks
	switch mm := m.Content.AsMultimodal(); {
	case mm != nil:
		resolved = true
		multi = toolResultParts(mm.Parts)
		if len(multi) == 0 && mm.TextSummary != "" {
			multi = []llm.Block{textBlock(mm.TextSummary)}
		}
	case m.Content.IsParts():
		if converted := toolResultParts(m.Content.AsParts()); hasBlockType(converted, map[string]bool{"image": true}) {
			resolved = true
			multi = converted
		}
	}
	if !resolved && len(m.StashedBlocks) > 0 { // back-compat: blocks stashed under a private key
		if text := m.Content.AsText(); strings.TrimSpace(text) != "" {
			multi = append([]llm.Block{textBlock(text)}, m.StashedBlocks...)
		} else {
			multi = append([]llm.Block(nil), m.StashedBlocks...)
		}
	}
	if len(multi) > 0 {
		return multi
	}
	switch {
	case m.Content.AsMultimodal() != nil, m.Content.IsParts() && len(m.Content.AsParts()) > 0:
		b, _ := json.Marshal(m.Content)
		return string(b)
	case m.Content.AsText() != "":
		return m.Content.AsText()
	}
	return noOutputPlaceholder
}

// appendToolResult adds a tool_result, merging into a trailing tool_result user message.
func appendToolResult(result []*WireMessage, m llm.Message) []*WireMessage {
	tr := llm.Block{"type": "tool_result", "tool_use_id": sanitizeToolID(m.ToolCallID), "content": toolResultContent(m)}
	if m.CacheControl != nil {
		tr["cache_control"] = m.CacheControl.Map()
	}
	if n := len(result); n > 0 && result[n-1].Role == "user" {
		if blocks, ok := result[n-1].blocks(); ok && len(blocks) > 0 && blockType(blocks[0]) == "tool_result" {
			result[n-1].content = append(blocks, tr)
			return result
		}
	}
	return append(result, &WireMessage{Role: "user", content: []llm.Block{tr}})
}

func convertUser(c llm.Content) *WireMessage {
	if c.IsParts() {
		kept := fixBlankText(convertParts(c.AsParts()), emptyUserPlaceholder, -1, "user", "convertUser")
		return &WireMessage{Role: "user", content: kept}
	}
	if strings.TrimSpace(c.AsText()) == "" {
		return &WireMessage{Role: "user", content: emptyUserPlaceholder}
	}
	return &WireMessage{Role: "user", content: c.AsText()}
}

// ---- whole-request passes -----------------------------------------------------------------

// stripOrphanedToolBlocks removes tool_use blocks with no matching tool_result and the reverse.
// Compression or truncation can remove either side of a pair or insert messages between them, and
// Anthropic requires the tool_result in the immediately following user message: a global id match is
// not enough.
func stripOrphanedToolBlocks(result []*WireMessage) {
	for i, m := range result {
		blocks, ok := m.blocks()
		if m.Role != "assistant" || !ok {
			continue
		}
		useIDs := blockIDs(blocks, "tool_use", "id")
		if len(useIDs) == 0 {
			continue
		}
		adjacent := map[string]bool{}
		if i+1 < len(result) && result[i+1].Role == "user" {
			if nb, ok := result[i+1].blocks(); ok {
				adjacent = blockIDs(nb, "tool_result", "tool_use_id")
			}
		}
		orphaned := map[string]bool{}
		for id := range useIDs {
			if !adjacent[id] {
				orphaned[id] = true
			}
		}
		if len(orphaned) == 0 {
			continue
		}
		var kept []llm.Block
		for _, b := range blocks {
			if id, _ := b["id"].(string); blockType(b) == "tool_use" && orphaned[id] {
				continue
			}
			kept = append(kept, b)
		}
		// Signed thinking on this turn was signed against the original content and is now dead (400
		// "thinking blocks in the latest assistant message cannot be modified"): flag it so it is demoted.
		if len(kept) != len(blocks) && hasBlockType(blocks, thinkingTypes) {
			m.thinkingInvalidated = true
		}
		if len(kept) == 0 {
			kept = []llm.Block{textBlock("(tool call removed)")}
		}
		m.content = kept
	}
	surviving := map[string]bool{}
	for _, m := range result {
		if blocks, ok := m.blocks(); ok && m.Role == "assistant" {
			for id := range blockIDs(blocks, "tool_use", "id") {
				surviving[id] = true
			}
		}
	}
	for _, m := range result {
		blocks, ok := m.blocks()
		if m.Role != "user" || !ok {
			continue
		}
		var kept []llm.Block
		for _, b := range blocks {
			if id, _ := b["tool_use_id"].(string); blockType(b) == "tool_result" && !surviving[id] {
				continue
			}
			kept = append(kept, b)
		}
		if len(kept) != len(blocks) {
			if len(kept) == 0 {
				kept = []llm.Block{textBlock("(tool result removed)")}
			}
			m.content = kept
		}
	}
}

func asBlocks(c any) []llm.Block {
	if s, ok := c.(string); ok {
		return []llm.Block{textBlock(s)}
	}
	b, _ := c.([]llm.Block)
	return append([]llm.Block(nil), b...)
}

// mergeConsecutiveRoles merges same-role neighbours to enforce alternation. Contents are
// concatenated as block lists with each side's blocks intact and strings never joined: the first
// turn's bytes must equal what a later request replays as a standalone turn, or the prompt-cache
// prefix diverges at that block.
func mergeConsecutiveRoles(result []*WireMessage) []*WireMessage {
	var fixed []*WireMessage
	for _, m := range result {
		if n := len(fixed); n == 0 || fixed[n-1].Role != m.Role {
			fixed = append(fixed, m)
			continue
		}
		last := fixed[len(fixed)-1]
		if m.Role != "user" {
			if m.thinkingInvalidated {
				last.thinkingInvalidated = true
			}
			// The second message's thinking was signed against a different turn boundary and is
			// invalid once merged.
			if b, ok := m.blocks(); ok {
				m.content = stripThinking(b)
			}
		}
		last.content = append(asBlocks(last.content), asBlocks(m.content)...)
	}
	return fixed
}

// keepValidThinking keeps valid signed thinking and demotes readable unsigned or invalidated
// thinking to text. A redacted block's data is its signature; a dataless one is dropped.
func keepValidThinking(content []llm.Block, signatureDead bool) []llm.Block {
	var out []llm.Block
	for _, b := range content {
		if !thinkingTypes[blockType(b)] {
			out = append(out, b)
			continue
		}
		redacted := blockType(b) == "redacted_thinking"
		var signed string
		if redacted {
			signed, _ = b["data"].(string)
		} else {
			signed, _ = b["signature"].(string)
		}
		thinking, _ := b["thinking"].(string)
		switch {
		case signed != "" && !signatureDead:
			out = append(out, b)
		case (signatureDead || !redacted) && thinking != "":
			out = append(out, textBlock(thinking))
		}
	}
	return out
}

// manageThinkingSignatures strips or preserves thinking per model. Anthropic signs thinking blocks
// against the full turn and any upstream mutation invalidates them (400 "Invalid signature in
// thinking block"). Models that preserve prior thinking keep valid signed blocks on every assistant
// turn; older models keep only the latest assistant turn's.
func manageThinkingSignatures(result []*WireMessage, model string) {
	last := -1
	for i, m := range result {
		if m.Role == "assistant" {
			last = i
		}
	}
	preservePrior := ModelPreservesPriorThinking(model)
	for idx, m := range result {
		blocks, ok := m.blocks()
		if m.Role != "assistant" || !ok {
			continue
		}
		if idx != last && !preservePrior {
			if stripped := stripThinking(blocks); len(stripped) > 0 {
				m.content = stripped
			} else {
				m.content = []llm.Block{textBlock("(thinking elided)")}
			}
		} else {
			kept := keepValidThinking(blocks, m.thinkingInvalidated)
			if len(kept) == 0 {
				kept = []llm.Block{textBlock(emptyTextPlaceholder)}
			}
			m.content = kept
		}
		// cache_control on thinking blocks interferes with signature validation.
		cur, _ := m.blocks()
		for _, b := range cur {
			if thinkingTypes[blockType(b)] {
				delete(b, "cache_control")
			}
		}
		m.thinkingInvalidated = false
	}
}

// evictOldScreenshots retires screenshot payloads once the request would cross the API's
// per-request image limit. This wire pass has no byte sizes, so it enforces the block ceiling only.
func evictOldScreenshots(result []*WireMessage) {
	isImage := map[string]bool{"image": true}
	reserved := 0
	for _, m := range result {
		if blocks, ok := m.blocks(); ok {
			for _, b := range blocks {
				if blockType(b) == "image" {
					reserved++
				}
			}
		}
	}
	type carrier struct {
		block llm.Block
		n     int
	}
	var carriers []carrier // newest first; parallel tool results are walked newest to oldest too
	for i := len(result) - 1; i >= 0; i-- {
		blocks, ok := result[i].blocks()
		if !ok {
			continue
		}
		for j := len(blocks) - 1; j >= 0; j-- {
			b := blocks[j]
			inner, ok := b["content"].([]llm.Block)
			if blockType(b) != "tool_result" || !ok || !hasBlockType(inner, isImage) {
				continue
			}
			n := 0
			for _, x := range inner {
				if blockType(x) == "image" {
					n++
				}
			}
			carriers = append(carriers, carrier{b, n})
		}
	}
	counts := make([]int, len(carriers))
	for i, c := range carriers {
		counts[i] = c.n
	}
	retire := imageRetireCount(counts, reserved)
	for _, c := range carriers[len(carriers)-retire:] {
		inner := c.block["content"].([]llm.Block)
		replaced := make([]llm.Block, len(inner))
		for i, x := range inner {
			if blockType(x) == "image" {
				replaced[i] = textBlock("[screenshot removed to save context]")
			} else {
				replaced[i] = x
			}
		}
		c.block["content"] = replaced
	}
}

// ensureLeadingUserTurn prepends a placeholder turn when the first message is not a user's. A
// second auto-compaction can leave an assistant summary first, which the API rejects (often masked as
// a misleading tool_use/tool_result 400). The filler must be non-whitespace text or it trades that
// 400 for the blank-block one.
func ensureLeadingUserTurn(result []*WireMessage) []*WireMessage {
	if len(result) > 0 && result[0].Role != "user" {
		first := &WireMessage{Role: "user", content: []llm.Block{textBlock(emptyTextPlaceholder)}}
		return append([]*WireMessage{first}, result...)
	}
	return result
}

// fixBlankText drops blank text blocks, relocates any cache_control they carried onto the last
// surviving cacheable block, and substitutes one placeholder when nothing survives. Other blocks and
// the order are untouched.
func fixBlankText(blocks []llm.Block, placeholder string, msgIndex int, role, location string) []llm.Block {
	kept, relocated, dropped := splitBlankText(blocks)
	for _, bi := range dropped {
		slog.Warn("pre-call sanitizer dropped a blank text content block",
			"message_index", msgIndex, "role", role, "location", location, "block_index", bi)
	}
	if len(kept) == 0 {
		kept = []llm.Block{textBlock(placeholder)}
	}
	applyCacheToLastCacheable(kept, relocated)
	return kept
}

// scrubBlankText is the final guard against blank text blocks, including inside tool_result
// content. It runs last so a blank block from any producer never reaches the wire.
func scrubBlankText(result []*WireMessage) {
	for i, m := range result {
		blocks, ok := m.blocks()
		if !ok || len(blocks) == 0 {
			continue
		}
		ph := emptyUserPlaceholder
		if m.Role == "assistant" {
			ph = emptyTextPlaceholder
		}
		blocks = fixBlankText(blocks, ph, i, m.Role, "content")
		for _, b := range blocks {
			if inner, ok := b["content"].([]llm.Block); ok && blockType(b) == "tool_result" && len(inner) > 0 {
				b["content"] = fixBlankText(inner, noOutputPlaceholder, i, m.Role, "tool_result")
			}
		}
		m.content = blocks
	}
}

// convertSystem turns system content into the Anthropic system parameter: a string, or a block list
// when any part carries cache_control. With markers the blocks are copied and blank text is replaced
// by the placeholder: Anthropic rejects blank system blocks too, and a blank block carrying a
// breakpoint cannot simply be dropped.
func convertSystem(c llm.Content) any {
	if !c.IsParts() {
		return c.AsText()
	}
	parts := c.AsParts()
	marked := false
	for _, p := range parts {
		if len(cacheControlOf(p)) > 0 { // an empty marker object is falsy, as in Python
			marked = true
		}
	}
	if !marked {
		var texts []string
		for _, p := range parts {
			if blockType(p) == "text" {
				texts = append(texts, fmt.Sprint(p["text"]))
			}
		}
		return strings.Join(texts, "\n")
	}
	out := make([]llm.Block, 0, len(parts))
	for _, p := range parts {
		cp := cloneBlock(p)
		if isBlankText(cp) {
			if _, isStr := cp["text"].(string); isStr {
				cp["text"] = emptyTextPlaceholder
			}
		}
		out = append(out, cp)
	}
	return out
}

// ConvertMessages converts OpenAI-shaped messages to the Anthropic request form. The system
// message becomes the returned system parameter (a string, or a block list when cache_control is
// present; nil when there is none, and the last system message wins). model drives the
// thinking-signature policy.
func ConvertMessages(messages []llm.Message, model string) (system any, out []WireMessage) {
	var result []*WireMessage
	for _, m := range messages {
		switch m.Role {
		case llm.RoleSystem:
			system = convertSystem(m.Content)
		case llm.RoleAssistant:
			result = append(result, convertAssistant(m))
		case llm.RoleTool:
			result = appendToolResult(result, m)
		default:
			result = append(result, convertUser(m.Content))
		}
	}
	stripOrphanedToolBlocks(result)
	result = mergeConsecutiveRoles(result)
	result = ensureLeadingUserTurn(result)
	manageThinkingSignatures(result, model)
	evictOldScreenshots(result)
	scrubBlankText(result)
	out = make([]WireMessage, len(result))
	for i, m := range result {
		out[i] = *m
	}
	return system, out
}
