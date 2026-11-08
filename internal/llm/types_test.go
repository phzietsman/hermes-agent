package llm

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestContentJSONRoundTrip(t *testing.T) {
	tests := []struct {
		name, in string
		check    func(Content) bool
	}{
		{"text", `"hello"`, func(c Content) bool { return c.AsText() == "hello" && !c.IsParts() }},
		{"empty text", `""`, func(c Content) bool { return c.IsEmpty() && !c.IsParts() }},
		{"null", `null`, func(c Content) bool { return c.IsEmpty() }},
		{"parts", `[{"type":"text","text":"a"},{"type":"image_url","image_url":{"url":"u"}}]`, func(c Content) bool { return c.IsParts() && len(c.AsParts()) == 2 }},
		{"empty parts stay parts", `[]`, func(c Content) bool { return c.IsParts() && len(c.AsParts()) == 0 }},
		{"multimodal", `{"_multimodal":true,"content":[{"type":"text","text":"t"}],"text_summary":"s"}`, func(c Content) bool {
			m := c.AsMultimodal()
			return m != nil && len(m.Parts) == 1 && m.TextSummary == "s"
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var c Content
			if err := json.Unmarshal([]byte(tc.in), &c); err != nil {
				t.Fatal(err)
			}
			if !tc.check(c) {
				t.Fatalf("decoded wrongly: %+v", c)
			}
			out, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			var a, b any
			_ = json.Unmarshal([]byte(tc.in), &a)
			_ = json.Unmarshal(out, &b)
			if tc.in != "null" && !reflect.DeepEqual(a, b) {
				t.Fatalf("round trip changed it: %s -> %s", tc.in, out)
			}
		})
	}
}

func TestContentRejectsUnknownShapes(t *testing.T) {
	for _, in := range []string{`{"no":"flag"}`, `42`, `true`} {
		var c Content
		if err := json.Unmarshal([]byte(in), &c); err == nil {
			t.Errorf("%s should be rejected", in)
		}
	}
}

func TestMessageJSONRoundTrip(t *testing.T) {
	in := `{"role":"assistant","content":"hi","tool_calls":[{"id":"c1","type":"function","function":{"name":"t","arguments":"{}"}}],
	"cache_control":{"type":"ephemeral","ttl":"1h"},"reasoning_content":"","reasoning_details":[{"type":"thinking","thinking":"x"}],
	"anthropic_content_blocks":[{"type":"text","text":"y"}],"_anthropic_content_blocks":[{"type":"image"}]}`
	var m Message
	if err := json.Unmarshal([]byte(in), &m); err != nil {
		t.Fatal(err)
	}
	if m.Role != RoleAssistant || m.Content.AsText() != "hi" || len(m.ToolCalls) != 1 || m.CacheControl == nil || m.CacheControl.TTL != "1h" ||
		m.ReasoningContent == nil || *m.ReasoningContent != "" || len(m.ReasoningDetails) != 1 || len(m.ContentBlocks) != 1 || len(m.StashedBlocks) != 1 {
		t.Fatalf("decoded wrongly: %+v", m)
	}
	out, _ := json.Marshal(m)
	var a, b any
	_ = json.Unmarshal([]byte(in), &a)
	_ = json.Unmarshal(out, &b)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("round trip changed it:\n%s\n%s", in, out)
	}
}

func TestMessageContentOmittedOnlyForToolCallTurns(t *testing.T) {
	has := func(m Message) bool {
		b, _ := json.Marshal(m)
		var o map[string]any
		_ = json.Unmarshal(b, &o)
		_, ok := o["content"]
		return ok
	}
	if has(Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "a"}}}) {
		t.Error("an assistant tool-call turn with no text should omit content")
	}
	if !has(Message{Role: RoleAssistant}) {
		t.Error("an empty assistant turn without tool calls keeps content")
	}
	if !has(Message{Role: RoleUser}) {
		t.Error("a user message always carries content")
	}
}

func TestToolDefJSON(t *testing.T) {
	var td ToolDef
	in := `{"type":"function","function":{"name":"n","description":"d","parameters":{"type":"object"}},"cache_control":{"type":"ephemeral"}}`
	if err := json.Unmarshal([]byte(in), &td); err != nil {
		t.Fatal(err)
	}
	if td.Name != "n" || td.Description != "d" || td.Parameters["type"] != "object" || td.CacheControl == nil {
		t.Fatalf("%+v", td)
	}
}

func TestCacheControlMapIsFresh(t *testing.T) {
	cc := CacheControl{Type: "ephemeral", TTL: "1h"}
	a, b := cc.Map(), cc.Map()
	a["x"] = 1
	if _, ok := b["x"]; ok {
		t.Fatal("Map must return a fresh map each call")
	}
	if got := (CacheControl{Type: "ephemeral"}).Map(); len(got) != 1 {
		t.Fatalf("ttl must be omitted when empty: %v", got)
	}
}
