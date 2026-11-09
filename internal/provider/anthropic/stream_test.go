package anthropic

import (
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/phzietsman/hermes-agent/internal/llm"
)

// streamFixture is one scenario from testdata/stream.json: recorded SSE plus what the Python SDK and
// AnthropicTransport.normalize_response produced from the same bytes.
type streamFixture struct {
	Name     string `json:"name"`
	SSE      string `json:"sse"`
	Expected struct {
		StopReason string `json:"stop_reason"`
		Valid      bool   `json:"valid"`
		Usage      struct {
			Input      int `json:"input"`
			Output     int `json:"output"`
			CacheRead  int `json:"cache_read"`
			CacheWrite int `json:"cache_write"`
		} `json:"usage"`
		Normalized struct {
			Content   *string `json:"content"`
			ToolCalls []struct {
				ID        string `json:"id"`
				Name      string `json:"name"`
				Arguments any    `json:"arguments"`
			} `json:"tool_calls"`
			FinishReason string         `json:"finish_reason"`
			Reasoning    *string        `json:"reasoning"`
			ProviderData map[string]any `json:"provider_data"`
		} `json:"normalized"`
	} `json:"expected"`
}

func loadStreamFixtures(t *testing.T) []streamFixture {
	t.Helper()
	var fx []streamFixture
	readJSON(t, "testdata/stream.json", &fx)
	if len(fx) < 20 {
		t.Fatalf("only %d stream fixtures", len(fx))
	}
	return fx
}

// providerData rebuilds Python's provider_data dict from a Response.
func providerData(r llm.Response) map[string]any {
	pd := map[string]any{}
	if len(r.ReasoningDetails) > 0 {
		pd["reasoning_details"] = r.ReasoningDetails
	}
	if r.StopDetails != nil {
		pd["stop_details"] = r.StopDetails
	}
	if len(r.ContentBlocks) > 0 {
		pd["anthropic_content_blocks"] = r.ContentBlocks
	}
	if len(pd) == 0 {
		return nil
	}
	return pd
}

func TestStreamMatchesPythonSDK(t *testing.T) {
	for _, fx := range loadStreamFixtures(t) {
		t.Run(fx.Name, func(t *testing.T) {
			msg, err := ReadMessage(strings.NewReader(fx.SSE), nil)
			if err != nil {
				t.Fatalf("ReadMessage: %v", err)
			}
			resp, err := Normalize(msg, NormalizeOptions{})
			if err != nil {
				t.Fatal(err)
			}
			want := fx.Expected
			if msg.StopReason != want.StopReason {
				t.Errorf("stop_reason = %q, python %q", msg.StopReason, want.StopReason)
			}
			if got := ValidResponse(msg); got != want.Valid {
				t.Errorf("valid = %v, python %v", got, want.Valid)
			}
			u := msg.Usage
			if u.InputTokens != want.Usage.Input || u.OutputTokens != want.Usage.Output ||
				u.CacheReadTokens != want.Usage.CacheRead || u.CacheWriteTokens != want.Usage.CacheWrite {
				t.Errorf("usage = %+v, python %+v", u, want.Usage)
			}
			n := want.Normalized
			if !reflect.DeepEqual(resp.Content, n.Content) {
				t.Errorf("content = %v, python %v", deref0(resp.Content), deref0(n.Content))
			}
			if !reflect.DeepEqual(resp.Reasoning, n.Reasoning) {
				t.Errorf("reasoning = %v, python %v", deref0(resp.Reasoning), deref0(n.Reasoning))
			}
			if resp.FinishReason != n.FinishReason {
				t.Errorf("finish_reason = %q, python %q", resp.FinishReason, n.FinishReason)
			}
			if len(resp.ToolCalls) != len(n.ToolCalls) {
				t.Fatalf("tool calls = %d, python %d", len(resp.ToolCalls), len(n.ToolCalls))
			}
			for i, tc := range resp.ToolCalls {
				var args any
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
					t.Fatalf("tool call %d arguments are not JSON: %v", i, err)
				}
				w := n.ToolCalls[i]
				if tc.ID != w.ID || tc.Function.Name != w.Name || !reflect.DeepEqual(args, w.Arguments) {
					t.Errorf("tool call %d = %+v (args %v), python %+v", i, tc, args, w)
				}
			}
			if got := normalize(t, providerData(resp)); !reflect.DeepEqual(got, normalizeAny(t, n.ProviderData)) {
				t.Errorf("provider_data differs\n--- python\n%s\n--- go\n%s", pretty(n.ProviderData), pretty(got))
			}
		})
	}
}

func normalizeAny(t *testing.T, v any) any { t.Helper(); return normalize(t, v) }

func deref0(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func TestReadEventsFraming(t *testing.T) {
	in := ": comment\r\n" +
		"event: ping\r\ndata: {\"type\":\"ping\"}\r\n\r\n" +
		"data: {\"type\":\"a\",\r\n" + // a data field split over two lines joins with a newline
		"data:  \"x\":1}\n\n" +
		"event: only_event_line\ndata: {\"no_type\":true}\n\n" +
		"data: {\"type\":\"last\"}" // no trailing blank line
	var got []string
	err := ReadEvents(strings.NewReader(in), func(ev StreamEvent) error { got = append(got, ev.Type); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"ping", "a", "only_event_line", "last"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if err := ReadEvents(strings.NewReader("data: {not json}\n\n"), func(StreamEvent) error { return nil }); err == nil {
		t.Fatal("malformed JSON must be an error")
	}
	if err := ReadEvents(strings.NewReader("\n\n\nevent: x\n\n"), func(StreamEvent) error { t.Fatal("no data, no event"); return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestReadEventsPropagatesCallbackErrorAndReadError(t *testing.T) {
	stop := errors.New("deadline")
	err := ReadEvents(strings.NewReader("data: {\"type\":\"a\"}\n\ndata: {\"type\":\"b\"}\n\n"), func(StreamEvent) error { return stop })
	if !errors.Is(err, stop) {
		t.Fatalf("callback error lost: %v", err)
	}
	boom := errors.New("connection reset")
	if err := ReadEvents(io.MultiReader(strings.NewReader("data: {\"type\":\"a\"}\n\n"), errReader{boom}), func(StreamEvent) error { return nil }); !errors.Is(err, boom) {
		t.Fatalf("read error lost: %v", err)
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func sse(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString("data: " + e + "\n\n")
	}
	return b.String()
}

const (
	evStart = `{"type":"message_start","message":{"id":"m","model":"x","usage":{"input_tokens":3,"output_tokens":1}}}`
	evStop  = `{"type":"message_stop"}`
)

func TestStreamEndingWithoutMessageStopIsIncomplete(t *testing.T) {
	_, err := ReadMessage(strings.NewReader(sse(evStart, `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)), nil)
	if !errors.Is(err, ErrIncompleteStream) {
		t.Fatalf("err = %v, want ErrIncompleteStream", err)
	}
	if _, err := ReadMessage(strings.NewReader(""), nil); !errors.Is(err, ErrIncompleteStream) {
		t.Fatalf("empty stream: %v", err)
	}
}

func TestStreamOrderErrors(t *testing.T) {
	tests := []struct{ name, in string }{
		{"delta without a block", sse(evStart, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"x"}}`)},
		{"block before message_start", sse(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)},
		{"stop for unknown block", sse(evStart, `{"type":"content_block_stop","index":3}`)},
		{"second message_start", sse(evStart, evStart)},
		{"message_delta first", sse(`{"type":"message_delta","delta":{},"usage":{"output_tokens":1}}`)},
		{"message_stop first", sse(evStop)},
		{"block start without a block", sse(evStart, `{"type":"content_block_start","index":0}`)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ReadMessage(strings.NewReader(tc.in), nil); !errors.Is(err, ErrUnexpectedEventOrder) {
				t.Fatalf("err = %v, want ErrUnexpectedEventOrder", err)
			}
		})
	}
}

func TestStreamErrorEvent(t *testing.T) {
	_, err := ReadMessage(strings.NewReader(sse(evStart, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)), nil)
	var se *StreamError
	if !errors.As(err, &se) || se.Type != "overloaded_error" || se.Message != "Overloaded" {
		t.Fatalf("err = %v", err)
	}
}

func TestMalformedToolInputIsATypedError(t *testing.T) {
	in := sse(evStart,
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t","name":"cronjob_manage","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"names\": cronjob_manage}"}}`,
		`{"type":"content_block_stop","index":0}`, evStop)
	_, err := ReadMessage(strings.NewReader(in), nil)
	var me *ErrMalformedToolInput
	if !errors.As(err, &me) || me.Tool != "cronjob_manage" {
		t.Fatalf("err = %v, want ErrMalformedToolInput for cronjob_manage", err)
	}
	trailing := strings.Replace(in, `{\"names\": cronjob_manage}`, `{}{}`, 1)
	if _, err := ReadMessage(strings.NewReader(trailing), nil); !errors.As(err, &me) {
		t.Fatalf("trailing data must be malformed: %v", err)
	}
}

func TestToolInputKeepsNumbersVerbatim(t *testing.T) {
	in := sse(evStart,
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t","name":"n","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"big\": 12345678901234567890, \"f\": 1.0, \"e\": 1e3}"}}`,
		`{"type":"content_block_stop","index":0}`, `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":2}}`, evStop)
	msg, err := ReadMessage(strings.NewReader(in), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, _ := Normalize(msg, NormalizeOptions{})
	if got := resp.ToolCalls[0].Function.Arguments; got != `{"big":12345678901234567890,"e":1e3,"f":1.0}` {
		t.Fatalf("arguments = %s", got)
	}
}

func TestNullUsageIsTolerated(t *testing.T) {
	in := sse(`{"type":"message_start","message":{"id":"m","model":"x","usage":null}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":null}`, evStop)
	msg, err := ReadMessage(strings.NewReader(in), nil)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Usage != (llm.Usage{}) || msg.StopReason != "end_turn" {
		t.Fatalf("msg = %+v", msg)
	}
}

func TestOnEventSeesEveryEventAndCanAbort(t *testing.T) {
	in := sse(evStart, `{"type":"ping"}`, evStop)
	var types []string
	if _, err := ReadMessage(strings.NewReader(in), func(ev StreamEvent) error { types = append(types, ev.Type); return nil }); err != nil {
		t.Fatal(err)
	}
	if want := []string{"message_start", "ping", "message_stop"}; !reflect.DeepEqual(types, want) {
		t.Fatalf("types = %v", types)
	}
	stop := errors.New("host gave up")
	if _, err := ReadMessage(strings.NewReader(in), func(StreamEvent) error { return stop }); !errors.Is(err, stop) {
		t.Fatalf("abort lost: %v", err)
	}
}

func TestUnprefixOAuthToolName(t *testing.T) {
	registered := map[string]bool{"read_file": true, "mcp_linear_get_issue": true, "context_notes": true}
	exists := func(n string) bool { return registered[n] }
	tests := []struct{ in, want string }{
		{"mcp__read_file", "read_file"},
		{"mcp__linear_get_issue", "mcp_linear_get_issue"},
		{"mcp__chat_history_lookup", "session_search"}, // aliases apply only when nothing registered matches
		{"mcp__context_notes", "context_notes"},        // a real tool under the wire name wins over the alias
		{"mcp__unknown", "mcp__unknown"},
	}
	for _, tc := range tests {
		if got := unprefixOAuthToolName(tc.in, exists); got != tc.want {
			t.Errorf("unprefixOAuthToolName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := unprefixOAuthToolName("mcp__memory_x", nil); got != "mcp__memory_x" {
		t.Errorf("nil resolver = %q", got)
	}
	// Through Normalize, only when StripToolPrefix is set.
	msg := &Message{StopReason: "tool_use", Content: []llm.Block{{"type": "tool_use", "id": "a", "name": "mcp__read_file", "input": map[string]any{}}}}
	if r, _ := Normalize(msg, NormalizeOptions{}); r.ToolCalls[0].Function.Name != "mcp__read_file" {
		t.Errorf("prefix stripped without StripToolPrefix: %s", r.ToolCalls[0].Function.Name)
	}
	if r, _ := Normalize(msg, NormalizeOptions{StripToolPrefix: true, ToolExists: exists}); r.ToolCalls[0].Function.Name != "read_file" {
		t.Errorf("prefix not stripped: %s", r.ToolCalls[0].Function.Name)
	}
}

func TestFinishReasonMapping(t *testing.T) {
	tests := map[string]string{
		"end_turn": "stop", "tool_use": "tool_calls", "max_tokens": "length", "stop_sequence": "stop",
		"refusal": "content_filter", "model_context_window_exceeded": "length", "": "stop", "new_future_reason": "stop",
	}
	for in, want := range tests {
		if got := FinishReason(in); got != want {
			t.Errorf("FinishReason(%q) = %q, want %q", in, got, want)
		}
	}
}

func FuzzReadMessageNeverPanics(f *testing.F) {
	for _, s := range loadSeedStreams(f) {
		f.Add(s)
	}
	f.Add("data: {\"type\":\"message_start\"}\n\n")
	f.Add("event: x\n\n:\n")
	f.Fuzz(func(t *testing.T, in string) {
		msg, err := ReadMessage(strings.NewReader(in), nil)
		if err != nil {
			return
		}
		if _, err := Normalize(msg, NormalizeOptions{StripToolPrefix: true}); err != nil {
			t.Fatalf("Normalize failed on an accepted message: %v", err)
		}
	})
}

func loadSeedStreams(f *testing.F) []string {
	var fx []streamFixture
	b, err := readFile("testdata/stream.json")
	if err != nil {
		f.Fatal(err)
	}
	if err := json.Unmarshal(b, &fx); err != nil {
		f.Fatal(err)
	}
	out := make([]string, len(fx))
	for i, c := range fx {
		out[i] = c.SSE
	}
	return out
}
