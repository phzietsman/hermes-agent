package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/phzietsman/hermes-agent/internal/llm"
	"github.com/phzietsman/hermes-agent/internal/session"
)

// interruptedPlaceholder is what the model sees for a turn that produced no output because it was interrupted
// or left a tool result as the transcript tail. It closes the tail so the next user message does not follow a
// raw tool result.
const interruptedPlaceholder = "[interrupt: no assistant output for this turn]"

// missingToolResult stands in for a tool call whose result was never recorded (a crash or interrupt between
// the call and its result): every call keeps a matching result or the provider rejects the request.
const missingToolResult = "[tool result missing: the call was interrupted before it returned]"

// fromRow converts a stored row to a model message. The byte-exact API content wins when it differs from
// the display content.
func fromRow(r session.Message) llm.Message {
	m := llm.Message{Role: llm.Role(r.Role), ToolCallID: r.ToolCallID, Content: llm.Text(r.Content)}
	if r.APIContent != "" {
		m.Content = llm.Text(r.APIContent)
	}
	if len(r.ToolCalls) > 0 {
		_ = json.Unmarshal(r.ToolCalls, &m.ToolCalls)
	}
	if len(r.ReasoningDetails) > 0 {
		_ = json.Unmarshal(r.ReasoningDetails, &m.ReasoningDetails)
	}
	if r.ReasoningContent != "" {
		rc := r.ReasoningContent
		m.ReasoningContent = &rc
	}
	return m
}

// toRow converts a model message to a stored row. Tool results carry the tool's name; plain text is stored
// as content. Image-bearing content is stored as its text.
func toRow(sessionID string, m llm.Message, toolName, finishReason string) (session.Message, error) {
	r := session.Message{
		SessionID: sessionID, Role: string(m.Role), ToolCallID: m.ToolCallID, ToolName: toolName, FinishReason: finishReason,
		Content: contentText(m.Content),
	}
	if len(m.ToolCalls) > 0 {
		b, err := json.Marshal(m.ToolCalls)
		if err != nil {
			return r, err
		}
		r.ToolCalls = b
	}
	if len(m.ReasoningDetails) > 0 {
		b, err := json.Marshal(m.ReasoningDetails)
		if err != nil {
			return r, err
		}
		r.ReasoningDetails = b
	}
	if m.ReasoningContent != nil {
		r.ReasoningContent = *m.ReasoningContent
	}
	return r, nil
}

func contentText(c llm.Content) string {
	if !c.IsParts() {
		return c.AsText()
	}
	var parts []string
	for _, p := range c.AsParts() {
		if t, ok := p["text"].(string); ok {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n")
}

// outbound returns the request-local view of a transcript: tool arguments repaired, tool results without a
// call dropped, calls without a result answered with a placeholder, and (when stripThinking) preserved
// thinking blocks removed. Stored messages are never modified; changed messages are copies.
func outbound(history []llm.Message, stripThinking bool) []llm.Message {
	out := make([]llm.Message, 0, len(history)+2)
	pending := map[string]bool{} // calls of the last assistant message still waiting for a result
	var order []string
	flush := func() {
		for _, id := range order {
			if pending[id] {
				out = append(out, llm.Message{Role: llm.RoleTool, ToolCallID: id, Content: llm.Text(missingToolResult)})
			}
		}
		pending, order = map[string]bool{}, nil
	}
	for _, m := range history {
		switch m.Role {
		case llm.RoleTool:
			if !pending[m.ToolCallID] {
				continue // orphan or duplicate result
			}
			delete(pending, m.ToolCallID)
			out = append(out, m)
		default:
			flush()
			m = repairedCopy(m, stripThinking)
			out = append(out, m)
			if m.Role == llm.RoleAssistant {
				for _, tc := range m.ToolCalls {
					pending[tc.ID] = true
					order = append(order, tc.ID)
				}
			}
		}
	}
	flush()
	return out
}

func repairedCopy(m llm.Message, stripThinking bool) llm.Message {
	needsCopy := stripThinking && (len(m.ReasoningDetails) > 0 || len(m.ContentBlocks) > 0 || len(m.StashedBlocks) > 0)
	for _, tc := range m.ToolCalls {
		if RepairToolArguments(tc.Function.Arguments) != tc.Function.Arguments {
			needsCopy = true
		}
	}
	if !needsCopy {
		return m
	}
	c := m.Clone()
	for i := range c.ToolCalls {
		c.ToolCalls[i].Function.Arguments = RepairToolArguments(c.ToolCalls[i].Function.Arguments)
	}
	if stripThinking {
		c.ReasoningDetails, c.ContentBlocks, c.ReasoningContent = nil, nil, nil
	}
	return c
}

// CheckAlternation reports the first violation of the transcript shape every provider accepts: a user
// message first, user and assistant messages alternating, and each assistant tool call answered by exactly
// its tool results before anything else. System messages may only lead.
func CheckAlternation(msgs []llm.Message) error {
	prev := llm.Role("")
	pending := map[string]bool{}
	for i, m := range msgs {
		switch m.Role {
		case llm.RoleSystem:
			if i != 0 {
				return fmt.Errorf("message %d: system message after the start", i)
			}
			continue
		case llm.RoleUser:
			if prev == llm.RoleUser || prev == llm.RoleTool || len(pending) > 0 {
				return fmt.Errorf("message %d: user message after %q", i, prev)
			}
		case llm.RoleAssistant:
			if prev == "" || prev == llm.RoleAssistant || len(pending) > 0 {
				return fmt.Errorf("message %d: assistant message after %q", i, prev)
			}
			for _, tc := range m.ToolCalls {
				pending[tc.ID] = true
			}
		case llm.RoleTool:
			if !pending[m.ToolCallID] {
				return fmt.Errorf("message %d: tool result %q has no pending call", i, m.ToolCallID)
			}
			delete(pending, m.ToolCallID)
		default:
			return fmt.Errorf("message %d: unknown role %q", i, m.Role)
		}
		prev = m.Role
	}
	if len(pending) > 0 {
		return fmt.Errorf("transcript ends with %d unanswered tool call(s)", len(pending))
	}
	return nil
}
