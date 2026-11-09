// Package llm holds the provider-neutral conversation types shared by the agent loop, the session
// store and the provider adapters. Messages use the OpenAI chat shape (role, content, tool_calls,
// tool_call_id) because that is the form stored in transcripts; each provider converts it on the
// way out.
package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Role is a message role.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Block is a raw JSON object: a content block, content part or schema fragment whose exact keys
// matter on the wire.
type Block = map[string]any

// CacheControl is a prompt-cache breakpoint marker, {"type": "ephemeral"} with an optional ttl.
type CacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

// Map is the marker as a wire object. It returns a fresh map each time so callers never share one.
func (c CacheControl) Map() Block {
	m := Block{"type": c.Type}
	if c.TTL != "" {
		m["ttl"] = c.TTL
	}
	return m
}

// ToolCall is an assistant tool invocation. Arguments is a JSON object encoded as a string.
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type,omitempty"`
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction names the tool and carries its JSON arguments.
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Multimodal is a tool result that carries images next to text: {"_multimodal": true, ...}.
type Multimodal struct {
	Parts       []Block
	TextSummary string
}

// Content is a message body: plain text, a list of parts, or a multimodal tool result. The zero
// value is empty text.
type Content struct {
	text       string
	parts      []Block
	multimodal *Multimodal
	isParts    bool
}

// Text is plain-text content.
func Text(s string) Content { return Content{text: s} }

// Parts is list content (text and image_url parts, each possibly carrying cache_control).
func Parts(p ...Block) Content { return Content{parts: p, isParts: true} }

// MultimodalContent is a tool result with images.
func MultimodalContent(m Multimodal) Content { return Content{multimodal: &m} }

// IsParts reports whether the content is a part list.
func (c Content) IsParts() bool { return c.isParts }

// AsParts returns the parts (nil for text or multimodal content).
func (c Content) AsParts() []Block { return c.parts }

// AsText returns the text of plain-text content ("" otherwise).
func (c Content) AsText() string { return c.text }

// AsMultimodal returns the multimodal payload, or nil.
func (c Content) AsMultimodal() *Multimodal { return c.multimodal }

// IsEmpty is true for empty text, or an empty part list.
func (c Content) IsEmpty() bool {
	return c.multimodal == nil && c.text == "" && len(c.parts) == 0
}

// MarshalJSON writes a string, an array of parts, or the multimodal object.
func (c Content) MarshalJSON() ([]byte, error) {
	switch {
	case c.multimodal != nil:
		parts := c.multimodal.Parts
		if parts == nil {
			parts = []Block{}
		}
		return json.Marshal(map[string]any{"_multimodal": true, "content": parts, "text_summary": c.multimodal.TextSummary})
	case c.isParts:
		parts := c.parts
		if parts == nil {
			parts = []Block{}
		}
		return json.Marshal(parts)
	}
	return json.Marshal(c.text)
}

// UnmarshalJSON reads a string, an array of parts, an object with _multimodal, or null (empty).
func (c *Content) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	*c = Content{}
	switch {
	case len(b) == 0 || string(b) == "null":
		return nil
	case b[0] == '"':
		return json.Unmarshal(b, &c.text)
	case b[0] == '[':
		c.isParts = true
		return json.Unmarshal(b, &c.parts)
	case b[0] == '{':
		var obj struct {
			Multimodal  bool    `json:"_multimodal"`
			Content     []Block `json:"content"`
			TextSummary string  `json:"text_summary"`
		}
		if err := json.Unmarshal(b, &obj); err != nil {
			return err
		}
		if !obj.Multimodal {
			return fmt.Errorf("content object without _multimodal: %s", truncate(string(b), 80))
		}
		c.multimodal = &Multimodal{Parts: obj.Content, TextSummary: obj.TextSummary}
		return nil
	}
	return fmt.Errorf("unsupported content JSON: %s", truncate(string(b), 80))
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// Message is one conversation message in the OpenAI shape plus the sidecars Anthropic needs to
// replay thinking verbatim.
type Message struct {
	Role       Role
	Content    Content
	ToolCalls  []ToolCall
	ToolCallID string
	// CacheControl is a top-level breakpoint marker (tool results, assistant turns).
	CacheControl *CacheControl
	// ReasoningContent is the provider's plain reasoning text. nil means absent; a pointer to ""
	// means present but empty (Kimi-style endpoints need that distinction).
	ReasoningContent *string
	// ReasoningDetails holds preserved thinking and redacted_thinking blocks.
	ReasoningDetails []Block
	// ContentBlocks is the verbatim ordered block list of an assistant turn that interleaved signed
	// thinking with tool_use. When present and valid the whole turn replays from it.
	ContentBlocks []Block
	// StashedBlocks is a back-compat carrier for multimodal tool-result blocks.
	StashedBlocks []Block
}

// ToolDef is a tool offered to the model.
type ToolDef struct {
	Name         string
	Description  string
	Parameters   Block // JSON Schema object
	CacheControl *CacheControl
}

// wireMessage is the JSON shape of a Message: the OpenAI chat fields plus the sidecar keys the
// Python transcript used, so stored transcripts and recorded fixtures load unchanged.
type wireMessage struct {
	Role             Role          `json:"role"`
	Content          *Content      `json:"content,omitempty"`
	ToolCalls        []ToolCall    `json:"tool_calls,omitempty"`
	ToolCallID       string        `json:"tool_call_id,omitempty"`
	CacheControl     *CacheControl `json:"cache_control,omitempty"`
	ReasoningContent *string       `json:"reasoning_content,omitempty"`
	ReasoningDetails []Block       `json:"reasoning_details,omitempty"`
	ContentBlocks    []Block       `json:"anthropic_content_blocks,omitempty"`
	StashedBlocks    []Block       `json:"_anthropic_content_blocks,omitempty"`
}

// MarshalJSON writes the OpenAI-shaped message.
func (m Message) MarshalJSON() ([]byte, error) {
	w := wireMessage{
		Role: m.Role, ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID, CacheControl: m.CacheControl,
		ReasoningContent: m.ReasoningContent, ReasoningDetails: m.ReasoningDetails,
		ContentBlocks: m.ContentBlocks, StashedBlocks: m.StashedBlocks,
	}
	if !m.Content.IsEmpty() || m.Role != RoleAssistant || len(m.ToolCalls) == 0 {
		c := m.Content
		w.Content = &c
	}
	return json.Marshal(w)
}

// UnmarshalJSON reads the OpenAI-shaped message. A missing content is empty text.
func (m *Message) UnmarshalJSON(b []byte) error {
	var w wireMessage
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*m = Message{
		Role: w.Role, ToolCalls: w.ToolCalls, ToolCallID: w.ToolCallID, CacheControl: w.CacheControl,
		ReasoningContent: w.ReasoningContent, ReasoningDetails: w.ReasoningDetails,
		ContentBlocks: w.ContentBlocks, StashedBlocks: w.StashedBlocks,
	}
	if w.Content != nil {
		m.Content = *w.Content
	}
	return nil
}

// UnmarshalJSON reads a tool definition in the OpenAI shape: {"type":"function","function":{...}}
// with an optional top-level cache_control.
func (t *ToolDef) UnmarshalJSON(b []byte) error {
	var w struct {
		Function struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Parameters  Block  `json:"parameters"`
		} `json:"function"`
		CacheControl *CacheControl `json:"cache_control"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*t = ToolDef{Name: w.Function.Name, Description: w.Function.Description, Parameters: w.Function.Parameters, CacheControl: w.CacheControl}
	return nil
}

// Usage is one API call's token accounting as the provider reports it. Anthropic counts cached
// tokens separately from InputTokens, so the prompt size is the sum of the three input fields.
type Usage struct {
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
}

// Finish reasons, in the OpenAI vocabulary the loop speaks.
const (
	FinishStop          = "stop"
	FinishToolCalls     = "tool_calls"
	FinishLength        = "length"
	FinishContentFilter = "content_filter"
)

// Response is a normalised model reply.
type Response struct {
	// Content is the joined text, nil when the reply had no text block (an empty text block is "").
	Content   *string
	ToolCalls []ToolCall
	// Reasoning is the plain thinking text, nil when there was none.
	Reasoning    *string
	FinishReason string
	// StopReason is the provider's raw stop reason.
	StopReason string
	// ReasoningDetails are the preserved thinking and redacted_thinking blocks.
	ReasoningDetails []Block
	// ContentBlocks is the ordered block list, kept only when signed thinking interleaves with tool_use
	// (the one shape the parallel lists above reconstruct in the wrong order).
	ContentBlocks []Block
	// StopDetails is the provider's structured refusal detail, if any.
	StopDetails any
	Usage       Usage
}

// CloneBlock deep-copies JSON-shaped data (maps, slices and scalars), so a copy never aliases the original.
func CloneBlock(b Block) Block {
	if b == nil {
		return nil
	}
	return cloneValue(b).(map[string]any)
}

// CloneBlocks deep-copies a block list.
func CloneBlocks(bs []Block) []Block {
	if bs == nil {
		return nil
	}
	out := make([]Block, len(bs))
	for i, b := range bs {
		out[i] = CloneBlock(b)
	}
	return out
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
	case []Block:
		return CloneBlocks(t)
	}
	return v
}

// Clone deep-copies the message so decorating the copy never rewrites the stored transcript.
func (m Message) Clone() Message {
	c := m
	c.ToolCalls = append([]ToolCall(nil), m.ToolCalls...)
	c.ReasoningDetails = CloneBlocks(m.ReasoningDetails)
	c.ContentBlocks = CloneBlocks(m.ContentBlocks)
	c.StashedBlocks = CloneBlocks(m.StashedBlocks)
	if m.CacheControl != nil {
		cc := *m.CacheControl
		c.CacheControl = &cc
	}
	if m.ReasoningContent != nil {
		rc := *m.ReasoningContent
		c.ReasoningContent = &rc
	}
	switch {
	case m.Content.multimodal != nil:
		mm := *m.Content.multimodal
		mm.Parts = CloneBlocks(mm.Parts)
		c.Content = MultimodalContent(mm)
	case m.Content.isParts:
		c.Content = Content{parts: CloneBlocks(m.Content.parts), isParts: true}
	}
	return c
}

// Clone copies the tool definition, including its schema.
func (t ToolDef) Clone() ToolDef {
	c := t
	c.Parameters = CloneBlock(t.Parameters)
	if t.CacheControl != nil {
		cc := *t.CacheControl
		c.CacheControl = &cc
	}
	return c
}
