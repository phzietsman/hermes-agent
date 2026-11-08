package anthropic

import (
	"strings"

	"github.com/phzietsman/hermes-agent/internal/llm"
)

// stopReasonMap maps Anthropic stop reasons to the loop's finish reasons. An unknown reason is "stop".
var stopReasonMap = map[string]string{
	"end_turn":                      llm.FinishStop,
	"tool_use":                      llm.FinishToolCalls,
	"max_tokens":                    llm.FinishLength,
	"stop_sequence":                 llm.FinishStop,
	"refusal":                       llm.FinishContentFilter,
	"model_context_window_exceeded": llm.FinishLength,
}

// FinishReason maps a raw stop reason to the OpenAI vocabulary.
func FinishReason(stopReason string) string {
	if r, ok := stopReasonMap[stopReason]; ok {
		return r
	}
	return llm.FinishStop
}

// NormalizeOptions tunes Normalize.
type NormalizeOptions struct {
	// StripToolPrefix reverses the OAuth wire's mcp__ tool-name prefix. It is set on OAuth requests.
	StripToolPrefix bool
	// ToolExists reports whether a name is a registered tool. It resolves the prefix: two originals
	// (read_file and mcp_linear_get_issue) map onto one wire name, so the name is looked up, never
	// rewritten when it already resolves natively. A nil func treats every name as unregistered.
	ToolExists func(name string) bool
}

// oauthReverseAliases undoes oauthToolAliases.
var oauthReverseAliases = func() map[string]string {
	m := make(map[string]string, len(oauthToolAliases))
	for name, wire := range oauthToolAliases {
		m[wire] = name
	}
	return m
}()

// unprefixOAuthToolName reverses the OAuth wire form back to the registered tool name. The OAuth wire
// aliases are checked last, so a real tool under the wire name still wins.
func unprefixOAuthToolName(name string, exists func(string) bool) string {
	bare := strings.TrimPrefix(name, mcpToolPrefix)
	if exists != nil {
		for _, candidate := range []string{name, "mcp_" + bare, bare} {
			if exists(candidate) {
				return candidate
			}
		}
	}
	if orig, ok := oauthReverseAliases[bare]; ok {
		return orig
	}
	return name
}

// Normalize converts an accumulated Message into the loop's Response: joined text, tool calls,
// reasoning text, the preserved thinking blocks, and a finish reason.
//
// Anthropic signs each thinking block against the blocks that precede it. When thinking interleaves
// with tool_use the parallel lists lose that order and replay fails with HTTP 400, so the ordered
// block list is kept for exactly that shape. Blocks are whitelisted at capture so output-only fields
// never persist and replay.
func Normalize(m *Message, o NormalizeOptions) (llm.Response, error) {
	var texts, reasoning []string
	var details, ordered []llm.Block
	var calls []llm.ToolCall
	for _, block := range m.Content {
		clean := sanitizeReplayBlock(block)
		if clean != nil {
			ordered = append(ordered, clean)
		}
		switch blockType(block) {
		case "text":
			t, _ := block["text"].(string)
			texts = append(texts, t)
		case "thinking", "redacted_thinking":
			if blockType(block) == "thinking" {
				t, _ := block["thinking"].(string)
				reasoning = append(reasoning, t)
			}
			detail := clean
			if detail == nil {
				detail = block // raw only when the whitelist dropped it
			}
			details = append(details, detail)
		case "tool_use":
			name, _ := block["name"].(string)
			if o.StripToolPrefix && strings.HasPrefix(name, mcpToolPrefix) {
				name = unprefixOAuthToolName(name, o.ToolExists)
			}
			input, ok := block["input"]
			if !ok {
				input = map[string]any{}
			}
			args, err := marshalNoEscape(input)
			if err != nil {
				return llm.Response{}, err
			}
			id, _ := block["id"].(string)
			calls = append(calls, llm.ToolCall{ID: id, Type: "function", Function: llm.ToolCallFunction{Name: name, Arguments: string(args)}})
		}
	}
	resp := llm.Response{
		ToolCalls:        calls,
		FinishReason:     FinishReason(m.StopReason),
		StopReason:       m.StopReason,
		ReasoningDetails: details,
		StopDetails:      m.StopDetails,
		Usage:            m.Usage,
	}
	if len(texts) > 0 {
		s := strings.Join(texts, "\n")
		resp.Content = &s
	}
	if len(reasoning) > 0 {
		s := strings.Join(reasoning, "\n\n")
		resp.Reasoning = &s
	}
	if signedThinking(ordered) && hasBlockType(ordered, map[string]bool{"tool_use": true}) {
		resp.ContentBlocks = ordered
	}
	return resp, nil
}

func signedThinking(blocks []llm.Block) bool {
	for _, b := range blocks {
		if !thinkingTypes[blockType(b)] {
			continue
		}
		if s, _ := b["signature"].(string); s != "" {
			return true
		}
		if d, _ := b["data"].(string); d != "" {
			return true
		}
	}
	return false
}

// ValidResponse is a structural check: empty content is legitimate for end_turn and refusal
// (retrying either would loop forever), anything else needs at least one block.
func ValidResponse(m *Message) bool {
	return len(m.Content) > 0 || m.StopReason == "end_turn" || m.StopReason == "refusal"
}
