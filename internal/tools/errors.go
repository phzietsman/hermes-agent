package tools

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	// maxToolErrorChars caps an error body: it only trims runaway interpolated exceptions, which would otherwise
	// pile up in history across retries.
	maxToolErrorChars       = 2048
	toolErrorTruncationMark = "… [truncated]"
)

func firstRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func boundErrorText(text string) string {
	if utf8.RuneCountInString(text) <= maxToolErrorChars {
		return text
	}
	return firstRunes(text, maxToolErrorChars) + toolErrorTruncationMark
}

// ordered keeps JSON key order so an error payload reads as it was written.
type ordered struct {
	keys []string
	vals map[string]any
}

func (o ordered) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := marshalNoEscape(k)
		vb, err := marshalNoEscape(o.vals[k])
		if err != nil {
			return nil, err
		}
		b.Write(kb)
		b.WriteByte(':')
		b.Write(vb)
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}

func marshalNoEscape(v any) ([]byte, error) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return []byte(strings.TrimRight(b.String(), "\n")), nil
}

// Error is the JSON body a tool returns for a failure: {"error": "..."} plus any extra fields, with the message
// bounded.
func Error(message string, extra ...Field) string {
	o := ordered{keys: []string{"error"}, vals: map[string]any{"error": boundErrorText(message)}}
	for _, f := range extra {
		if _, dup := o.vals[f.Key]; !dup {
			o.keys = append(o.keys, f.Key)
		}
		o.vals[f.Key] = f.Value
	}
	b, _ := marshalNoEscape(o)
	return string(b)
}

// Field is an extra key in an Error or Result body.
type Field struct {
	Key   string
	Value any
}

// Result JSON-encodes a tool's result object.
func Result(fields ...Field) string {
	o := ordered{vals: map[string]any{}}
	for _, f := range fields {
		if _, dup := o.vals[f.Key]; !dup {
			o.keys = append(o.keys, f.Key)
		}
		o.vals[f.Key] = f.Value
	}
	b, _ := marshalNoEscape(o)
	return string(b)
}

// boundJSONErrorResult trims an oversized "error" field in a JSON result: handlers that marshal their own error
// bypass Error's cap, so this runs at the dispatch boundary.
func boundJSONErrorResult(result string) string {
	if utf8.RuneCountInString(result) <= maxToolErrorChars || !strings.Contains(result, `"error"`) {
		return result
	}
	dec := json.NewDecoder(strings.NewReader(result))
	dec.UseNumber()
	var raw map[string]json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return result
	}
	var msg string
	if json.Unmarshal(raw["error"], &msg) != nil || utf8.RuneCountInString(msg) <= maxToolErrorChars {
		return result
	}
	// Re-encode in the original key order.
	keys := orderedKeys(result)
	o := ordered{keys: keys, vals: map[string]any{}}
	for k, v := range raw {
		o.vals[k] = v
	}
	o.vals["error"] = boundErrorText(msg)
	b, _ := marshalNoEscape(o)
	return string(b)
}

func orderedKeys(obj string) []string {
	dec := json.NewDecoder(strings.NewReader(obj))
	if _, err := dec.Token(); err != nil {
		return nil
	}
	var keys []string
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return keys
		}
		k, _ := t.(string)
		keys = append(keys, k)
		var skip json.RawMessage
		if dec.Decode(&skip) != nil {
			return keys
		}
	}
	return keys
}

var toolErrorStripRes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)</?(?:tool_call|function_call|result|response|output|input|system|assistant|user)>`),
	regexp.MustCompile("(?m)^\\s*```(?:json|xml|html|markdown)?\\s*"),
	regexp.MustCompile("(?m)\\s*```\\s*$"),
	regexp.MustCompile(`(?s)<!\[CDATA\[.*?\]\]>`),
}

// SanitizeError strips structural framing tokens (tool-call tags, code fences, CDATA) from an error before the
// model sees it, and marks it [TOOL_ERROR].
func SanitizeError(msg string) string {
	if msg == "" {
		return "[TOOL_ERROR] "
	}
	s := msg
	for _, re := range toolErrorStripRes {
		s = re.ReplaceAllString(s, "")
	}
	if utf8.RuneCountInString(s) > maxToolErrorChars {
		s = firstRunes(s, maxToolErrorChars-3) + "..."
	}
	return "[TOOL_ERROR] " + s
}
