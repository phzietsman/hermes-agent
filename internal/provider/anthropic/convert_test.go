package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/phzietsman/hermes-agent/internal/llm"
)

func TestImageRetireCountMatchesPython(t *testing.T) {
	var fx struct {
		Cases []struct {
			Carriers []int `json:"carriers"`
			Reserved int   `json:"reserved"`
			Want     int   `json:"want"`
		} `json:"image_retire"`
	}
	readJSON(t, "testdata/models.json", &fx)
	if len(fx.Cases) < 50 {
		t.Fatalf("only %d cases", len(fx.Cases))
	}
	for _, c := range fx.Cases {
		if got := imageRetireCount(c.Carriers, c.Reserved); got != c.Want {
			t.Errorf("imageRetireCount(%v, %d) = %d, python %d", c.Carriers, c.Reserved, got, c.Want)
		}
	}
}

func TestUnsupportedInlineImageType(t *testing.T) {
	tests := []struct{ url, want string }{
		{"data:image/png;base64,AA", ""},
		{"data:image/jpeg;base64,AA", ""},
		{"data:image/jpg;base64,AA", ""},
		{"data:IMAGE/PNG;base64,AA", ""},
		{"data:image/webp;base64,AA", ""},
		{"data:image/gif;base64,AA", ""},
		{"data:image/svg+xml;base64,AA", "image/svg+xml"},
		{"data:image/bmp;base64,AA", "image/bmp"},
		{"data:image/tiff,AA", "image/tiff"},
		{"data:image/;base64,AA", "image/unknown"},
		{"data:application/pdf;base64,AA", ""},
		{"https://example.com/a.svg", ""},
		{"", ""},
	}
	for _, tc := range tests {
		if got := unsupportedInlineImageType(tc.url); got != tc.want {
			t.Errorf("unsupportedInlineImageType(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

func TestSanitizeToolID(t *testing.T) {
	tests := []struct{ in, want string }{
		{"toolu_01A", "toolu_01A"}, {"call:1/x y", "call_1_x_y"}, {"", "tool_0"}, {"a-b_c", "a-b_c"}, {"日本", "__"},
	}
	for _, tc := range tests {
		if got := sanitizeToolID(tc.in); got != tc.want {
			t.Errorf("sanitizeToolID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// checkWire asserts the invariants the API demands of any converted request.
func checkWire(t *testing.T, out []WireMessage) {
	t.Helper()
	if len(out) == 0 {
		return
	}
	if out[0].Role != "user" {
		t.Fatalf("first message is %q, the API requires a user turn", out[0].Role)
	}
	for i := 1; i < len(out); i++ {
		if out[i].Role == out[i-1].Role {
			t.Fatalf("messages %d and %d both have role %q: alternation broken", i-1, i, out[i].Role)
		}
	}
	for i, m := range out {
		if m.Role != "user" && m.Role != "assistant" {
			t.Fatalf("message %d has role %q", i, m.Role)
		}
		switch c := m.content.(type) {
		case string:
			if strings.TrimSpace(c) == "" {
				t.Fatalf("message %d has blank string content", i)
			}
		case []llm.Block:
			if len(c) == 0 {
				t.Fatalf("message %d has no content blocks", i)
			}
			for _, b := range c {
				if isBlankText(b) {
					t.Fatalf("message %d carries a blank text block: %v", i, b)
				}
				if inner, ok := b["content"].([]llm.Block); ok {
					for _, x := range inner {
						if isBlankText(x) {
							t.Fatalf("message %d tool_result carries a blank text block", i)
						}
					}
				}
			}
		default:
			t.Fatalf("message %d content is %T", i, m.content)
		}
	}
	// Every tool_use is answered in the very next message, and every tool_result answers the one before.
	for i, m := range out {
		blocks, _ := m.blocks()
		if m.Role == "assistant" {
			uses := blockIDs(blocks, "tool_use", "id")
			if len(uses) == 0 {
				continue
			}
			var next map[string]bool
			if i+1 < len(out) {
				nb, _ := out[i+1].blocks()
				next = blockIDs(nb, "tool_result", "tool_use_id")
			}
			for id := range uses {
				if !next[id] {
					t.Fatalf("tool_use %q in message %d has no result in the next message", id, i)
				}
			}
		} else {
			results := blockIDs(blocks, "tool_result", "tool_use_id")
			if len(results) == 0 {
				continue
			}
			if i == 0 {
				t.Fatalf("tool_result in the first message")
			}
			pb, _ := out[i-1].blocks()
			prev := blockIDs(pb, "tool_use", "id")
			for id := range results {
				if !prev[id] {
					t.Fatalf("tool_result %q in message %d answers no tool_use in the previous message", id, i)
				}
			}
		}
	}
	if _, err := json.Marshal(out); err != nil {
		t.Fatal(err)
	}
}

func FuzzConvertMessagesInvariants(f *testing.F) {
	seeds := []string{
		`[{"role":"user","content":"hi"}]`,
		`[{"role":"assistant","content":null,"tool_calls":[{"id":"a","function":{"name":"t","arguments":"{}"}}]},{"role":"tool","tool_call_id":"a","content":""}]`,
		`[{"role":"tool","tool_call_id":"x","content":"orphan"},{"role":"assistant","content":""}]`,
		`[{"role":"user","content":[{"type":"text","text":" "}]},{"role":"user","content":""}]`,
		`[{"role":"assistant","content":"a","reasoning_details":[{"type":"thinking","thinking":"t","signature":"s"}],"tool_calls":[{"id":"1","function":{"name":"t"}},{"id":"2","function":{"name":"t"}}]},{"role":"tool","tool_call_id":"1","content":"r"}]`,
		`[{"role":"assistant","anthropic_content_blocks":[{"type":"text","text":""},{"type":"tool_use","id":"z","name":"n","input":{}}]},{"role":"user","content":"x"}]`,
		`[{"role":"system","content":[{"type":"text","text":"","cache_control":{"type":"ephemeral"}}]}]`,
		`[{"role":"tool","tool_call_id":"c","content":{"_multimodal":true,"content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA"}}],"text_summary":""}}]`,
	}
	for _, s := range seeds {
		f.Add(s, "claude-opus-4-5")
		f.Add(s, "claude-haiku-4-5")
	}
	f.Fuzz(func(t *testing.T, raw, model string) {
		var msgs []llm.Message
		if json.Unmarshal([]byte(raw), &msgs) != nil {
			t.Skip()
		}
		_, out := ConvertMessages(msgs, model)
		checkWire(t, out)
	})
}

func TestConvertMessagesDoesNotMutateInput(t *testing.T) {
	raw := `[{"role":"system","content":[{"type":"text","text":"s","cache_control":{"type":"ephemeral"}}]},
	{"role":"user","content":[{"type":"text","text":" ","cache_control":{"type":"ephemeral"}},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA"}}]},
	{"role":"assistant","content":"a","reasoning_details":[{"type":"thinking","thinking":"t","signature":"s","cache_control":{"type":"ephemeral"}}],
	 "tool_calls":[{"id":"1","function":{"name":"t","arguments":"{\"a\":1}"}}]},
	{"role":"tool","tool_call_id":"1","cache_control":{"type":"ephemeral"},"content":"r"}]`
	var msgs []llm.Message
	if err := json.Unmarshal([]byte(raw), &msgs); err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(msgs)
	ConvertMessages(msgs, "claude-opus-4-5")
	after, _ := json.Marshal(msgs)
	if string(before) != string(after) {
		t.Fatalf("ConvertMessages mutated its input:\nbefore %s\nafter  %s", before, after)
	}
}

func TestConvertMessagesIsDeterministic(t *testing.T) {
	raw := `[{"role":"user","content":"q"},{"role":"assistant","content":"a","reasoning_details":[{"type":"thinking","thinking":"t","signature":"s"}]},{"role":"user","content":"q2"}]`
	var msgs []llm.Message
	_ = json.Unmarshal([]byte(raw), &msgs)
	var first string
	for i := 0; i < 20; i++ {
		s, out := ConvertMessages(msgs, "claude-opus-4-5")
		b, _ := json.Marshal(struct {
			S any
			M []WireMessage
		}{s, out})
		if i == 0 {
			first = string(b)
		} else if string(b) != first {
			t.Fatalf("run %d differs: the request bytes must be stable for the prompt cache", i)
		}
	}
}
