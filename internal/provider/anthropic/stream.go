package anthropic

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/phzietsman/hermes-agent/internal/llm"
)

// Message is an accumulated Messages API reply: what the SDK calls the final message.
type Message struct {
	ID           string
	Model        string
	StopReason   string
	StopSequence string
	// StopDetails is the structured detail from message_delta (refusals), nil when absent.
	StopDetails any
	Content     []llm.Block
	Usage       llm.Usage
}

// StreamEvent is one decoded server-sent event. Data is the full JSON object.
type StreamEvent struct {
	Type string
	Data map[string]any
}

// ErrIncompleteStream means the stream ended before message_stop: an upstream stream drop. The
// response is incomplete and the call should be retried.
var ErrIncompleteStream = errors.New("anthropic messages stream ended before message_stop (possible upstream stream drop)")

// ErrUnexpectedEventOrder is an event that does not fit the stream so far. Some Anthropic-compatible
// gateways stream badly; the client falls back to a non-streaming call.
var ErrUnexpectedEventOrder = errors.New("unexpected event order in the Messages stream")

// ErrMalformedToolInput means a tool_use block's streamed JSON did not parse. The fine-grained tool
// streaming beta sends tool arguments unvalidated, so a model that emits {"names": cronjob_manage}
// breaks the parser and an identical retry breaks identically; Request.BufferToolInput is the retry knob.
type ErrMalformedToolInput struct {
	Tool string
	Err  error
}

func (e *ErrMalformedToolInput) Error() string {
	return fmt.Sprintf("malformed tool input JSON in the stream for %q: %v", e.Tool, e.Err)
}

func (e *ErrMalformedToolInput) Unwrap() error { return e.Err }

// StreamError is an error event sent inside a stream (overloaded_error, api_error, ...).
type StreamError struct {
	Type    string
	Message string
}

func (e *StreamError) Error() string {
	return fmt.Sprintf("stream error event %s: %s", e.Type, e.Message)
}

// ReadEvents parses server-sent events from r and calls fn for each. Events are dispatched on a
// blank line; comment lines (":") are ignored; multiple data lines join with newlines. The event
// type comes from the JSON "type" field, falling back to the event: line.
func ReadEvents(r io.Reader, fn func(StreamEvent) error) error {
	br := bufio.NewReader(r)
	var event string
	var data []string
	dispatch := func() error {
		defer func() { event, data = "", nil }()
		if len(data) == 0 {
			return nil
		}
		payload := strings.Join(data, "\n")
		dec := json.NewDecoder(strings.NewReader(payload))
		dec.UseNumber()
		var obj map[string]any
		if err := dec.Decode(&obj); err != nil {
			return fmt.Errorf("malformed stream event %q: %w", truncate(payload, 120), err)
		}
		typ, _ := obj["type"].(string)
		if typ == "" {
			typ = event
		}
		return fn(StreamEvent{Type: typ, Data: obj})
	}
	for {
		line, err := br.ReadString('\n')
		if line != "" || err == nil {
			line = strings.TrimRight(line, "\r\n")
			switch {
			case line == "":
				if derr := dispatch(); derr != nil {
					return derr
				}
			case strings.HasPrefix(line, ":"):
			default:
				field, value, _ := strings.Cut(line, ":")
				value = strings.TrimPrefix(value, " ")
				switch field {
				case "event":
					event = value
				case "data":
					data = append(data, value)
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return dispatch() // a final event without the trailing blank line
			}
			return err
		}
	}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// Accumulator builds a Message from stream events, with the SDK's semantics.
type Accumulator struct {
	msg      Message
	started  bool
	stopped  bool
	jsonBuf  map[int]*bytes.Buffer
	finished map[int]bool
}

// NewAccumulator returns an empty accumulator.
func NewAccumulator() *Accumulator {
	return &Accumulator{jsonBuf: map[int]*bytes.Buffer{}, finished: map[int]bool{}}
}

func intOf(v any) (int, bool) {
	switch n := v.(type) {
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	case float64:
		return int(n), true
	case int:
		return n, true
	}
	return 0, false
}

func strOf(v any) string { s, _ := v.(string); return s }

// setUsage copies the usage fields that are present and non-null. message_delta carries the final
// output_tokens and, on some gateways, refreshed input and cache counts; a null usage is tolerated
// (MiniMax sends usage:null on message_start and message_delta).
func setUsage(u *llm.Usage, raw any) {
	m, ok := raw.(map[string]any)
	if !ok {
		return
	}
	if n, ok := intOf(m["input_tokens"]); ok {
		u.InputTokens = n
	}
	if n, ok := intOf(m["output_tokens"]); ok {
		u.OutputTokens = n
	}
	if n, ok := intOf(m["cache_read_input_tokens"]); ok {
		u.CacheReadTokens = n
	}
	if n, ok := intOf(m["cache_creation_input_tokens"]); ok {
		u.CacheWriteTokens = n
	}
}

func (a *Accumulator) block(index any) (llm.Block, int, error) {
	i, ok := intOf(index)
	if !ok || i < 0 || i >= len(a.msg.Content) {
		return nil, 0, fmt.Errorf("%w: content block index %v", ErrUnexpectedEventOrder, index)
	}
	return a.msg.Content[i], i, nil
}

// Add applies one event. It returns a *StreamError for an error event.
func (a *Accumulator) Add(ev StreamEvent) error {
	switch ev.Type {
	case "message_start":
		if a.started {
			return fmt.Errorf("%w: second message_start", ErrUnexpectedEventOrder)
		}
		a.started = true
		m, _ := ev.Data["message"].(map[string]any)
		a.msg.ID, a.msg.Model = strOf(m["id"]), strOf(m["model"])
		a.msg.StopReason = strOf(m["stop_reason"])
		setUsage(&a.msg.Usage, m["usage"])
	case "content_block_start":
		if !a.started {
			return fmt.Errorf("%w: content_block_start before message_start", ErrUnexpectedEventOrder)
		}
		cb, _ := ev.Data["content_block"].(map[string]any)
		if cb == nil {
			return fmt.Errorf("%w: content_block_start without a block", ErrUnexpectedEventOrder)
		}
		a.msg.Content = append(a.msg.Content, cloneBlock(cb))
	case "content_block_delta":
		return a.delta(ev)
	case "content_block_stop":
		b, i, err := a.block(ev.Data["index"])
		if err != nil {
			return err
		}
		return a.finishBlock(b, i)
	case "message_delta":
		if !a.started {
			return fmt.Errorf("%w: message_delta before message_start", ErrUnexpectedEventOrder)
		}
		d, _ := ev.Data["delta"].(map[string]any)
		if s := strOf(d["stop_reason"]); s != "" {
			a.msg.StopReason = s
		}
		if s := strOf(d["stop_sequence"]); s != "" {
			a.msg.StopSequence = s
		}
		if sd := d["stop_details"]; sd != nil {
			a.msg.StopDetails = sd
		}
		setUsage(&a.msg.Usage, ev.Data["usage"])
	case "message_stop":
		if !a.started {
			return fmt.Errorf("%w: message_stop before message_start", ErrUnexpectedEventOrder)
		}
		a.stopped = true
	case "error":
		e, _ := ev.Data["error"].(map[string]any)
		return &StreamError{Type: strOf(e["type"]), Message: strOf(e["message"])}
	}
	return nil // ping and unknown event types are ignored
}

func (a *Accumulator) delta(ev StreamEvent) error {
	b, i, err := a.block(ev.Data["index"])
	if err != nil {
		return err
	}
	d, _ := ev.Data["delta"].(map[string]any)
	switch strOf(d["type"]) {
	case "text_delta":
		b["text"] = strOf(b["text"]) + strOf(d["text"])
	case "thinking_delta":
		b["thinking"] = strOf(b["thinking"]) + strOf(d["thinking"])
	case "signature_delta":
		b["signature"] = strOf(d["signature"])
	case "input_json_delta":
		buf := a.jsonBuf[i]
		if buf == nil {
			buf = &bytes.Buffer{}
			a.jsonBuf[i] = buf
		}
		buf.WriteString(strOf(d["partial_json"]))
	case "citations_delta":
		cits, _ := b["citations"].([]any)
		b["citations"] = append(cits, d["citation"])
	}
	return nil
}

// finishBlock parses a block's accumulated tool JSON. Numbers keep their written form (json.Number),
// so a large integer or a 1.0 survives the round trip.
func (a *Accumulator) finishBlock(b llm.Block, i int) error {
	buf, ok := a.jsonBuf[i]
	if !ok {
		return nil // a tool_use with no input_json_delta keeps its {} input
	}
	delete(a.jsonBuf, i)
	raw := bytes.TrimSpace(buf.Bytes())
	if len(raw) == 0 {
		b["input"] = map[string]any{}
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return &ErrMalformedToolInput{Tool: strOf(b["name"]), Err: err}
	}
	if dec.More() {
		return &ErrMalformedToolInput{Tool: strOf(b["name"]), Err: errors.New("trailing data after the JSON value")}
	}
	b["input"] = v
	return nil
}

// Message returns the accumulated message, or ErrIncompleteStream when message_stop never arrived.
func (a *Accumulator) Message() (*Message, error) {
	if !a.stopped {
		return nil, ErrIncompleteStream
	}
	m := a.msg
	return &m, nil
}

// ReadMessage reads a whole stream and returns the final message. onEvent, if set, sees every event
// first (forward-progress watchdogs, live display); an error from it abandons the stream.
func ReadMessage(r io.Reader, onEvent func(StreamEvent) error) (*Message, error) {
	acc := NewAccumulator()
	err := ReadEvents(r, func(ev StreamEvent) error {
		if err := acc.Add(ev); err != nil {
			return err
		}
		if onEvent != nil {
			return onEvent(ev)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return acc.Message()
}
