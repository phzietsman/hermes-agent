package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var trailingCommaRe = regexp.MustCompile(`,\s*([}\]])`)

var jsonClosers = map[byte]byte{'{': '}', '[': ']'}

// escapeControlCharsInStrings escapes literal control characters inside JSON string values as \u00XX, for
// models that emit raw newlines or tabs in an argument string.
func escapeControlCharsInStrings(raw string) string {
	var out strings.Builder
	inString := false
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		if inString && ch == '\\' && i+1 < len(raw) {
			out.WriteString(raw[i : i+2])
			i++
			continue
		}
		if ch == '"' {
			inString = !inString
		}
		if inString && ch < 0x20 {
			fmt.Fprintf(&out, "\\u%04x", ch)
		} else {
			out.WriteByte(ch)
		}
	}
	return out.String()
}

// rebalanceJSONClosers closes a JSON prefix's open braces and brackets in stack order, ignoring delimiters
// inside strings. A closer that does not match the stack top but matches a deeper opener gets the missing
// inner closers inserted before it. ok is false when the text ends inside an unterminated string: that
// content is unrecoverable and is not guessed.
func rebalanceJSONClosers(raw string) (string, bool) {
	var out strings.Builder
	var stack []byte
	inString := false
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		switch {
		case inString:
			if ch == '\\' {
				end := min(i+2, len(raw))
				out.WriteString(raw[i:end])
				i = end - 1
				continue
			}
			if ch == '"' {
				inString = false
			}
		case ch == '"':
			inString = true
		case ch == '{' || ch == '[':
			stack = append(stack, ch)
		case ch == '}' || ch == ']':
			if openerFor(stack, ch) {
				for jsonClosers[stack[len(stack)-1]] != ch {
					out.WriteByte(jsonClosers[stack[len(stack)-1]])
					stack = stack[:len(stack)-1]
				}
				stack = stack[:len(stack)-1]
			}
		}
		out.WriteByte(ch)
	}
	if inString {
		return "", false
	}
	for i := len(stack) - 1; i >= 0; i-- {
		out.WriteByte(jsonClosers[stack[i]])
	}
	return out.String(), true
}

func openerFor(stack []byte, closer byte) bool {
	for _, o := range stack {
		if jsonClosers[o] == closer {
			return true
		}
	}
	return false
}

// RepairToolArguments turns a tool call's argument string into valid JSON: empty or "None" become "{}",
// literal control characters are escaped, trailing commas dropped, unclosed structures closed and excess
// closers trimmed. Anything unrepairable becomes "{}" so the request still goes through. The Python
// original re-serialises valid JSON with ASCII escapes; this returns valid input as it is, which parses to
// the same value.
func RepairToolArguments(raw string) string {
	s := pyStrip(raw)
	if s == "" || s == "None" {
		return "{}"
	}
	if json.Valid([]byte(s)) {
		return s
	}
	if e := escapeControlCharsInStrings(s); json.Valid([]byte(e)) {
		return e
	}
	fixed := trailingCommaRe.ReplaceAllString(s, "$1")
	if r, ok := rebalanceJSONClosers(fixed); ok && r != "" {
		fixed = r
	}
	for i := 0; i < 50; i++ {
		if json.Valid([]byte(fixed)) {
			break
		}
		excessBrace := strings.HasSuffix(fixed, "}") && strings.Count(fixed, "}") > strings.Count(fixed, "{")
		excessBracket := strings.HasSuffix(fixed, "]") && strings.Count(fixed, "]") > strings.Count(fixed, "[")
		if !excessBrace && !excessBracket {
			break
		}
		fixed = fixed[:len(fixed)-1]
	}
	if json.Valid([]byte(fixed)) {
		return fixed
	}
	if e := escapeControlCharsInStrings(fixed); e != fixed && json.Valid([]byte(e)) {
		return e
	}
	return "{}"
}
