package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// pruneMinChars is the shortest tool result worth replacing with a stub: a stub for anything shorter could be
// longer than what it replaces.
const pruneMinChars = 200

// orderedArgs parses a tool-call argument string into its values and key order. Non-object or invalid input
// is empty.
func orderedArgs(raw string) (map[string]any, []string) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	tok, err := dec.Token()
	if d, ok := tok.(json.Delim); err != nil || !ok || d != '{' {
		return nil, nil
	}
	vals := map[string]any{}
	var keys []string
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, nil
		}
		k, _ := kt.(string)
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, nil
		}
		if _, dup := vals[k]; !dup {
			keys = append(keys, k)
		}
		vals[k] = v
	}
	return vals, keys
}

// pyStr renders a decoded JSON value the way Python's str() renders it.
func pyStr(v any) string {
	switch t := v.(type) {
	case nil:
		return "None"
	case string:
		return t
	case bool:
		if t {
			return "True"
		}
		return "False"
	case json.Number:
		return t.String()
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case string:
		return t != ""
	case bool:
		return t
	case json.Number:
		f, _ := t.Float64()
		return f != 0
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	}
	return true
}

func strArg(args map[string]any, key string) string {
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	return pyStr(v)
}

func argOr(args map[string]any, key, def string) string {
	if v, ok := args[key]; ok {
		return pyStr(v)
	}
	return def
}

func firstRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func failureSuffix(reason any, label string) string {
	preview := ""
	if truthy(reason) {
		preview = firstRunes(strings.Join(strings.Fields(pyStr(reason)), " "), 80)
	}
	if preview != "" {
		return " " + label + ": " + preview
	}
	return " " + label
}

func payloadFailureSuffix(payload map[string]any) string {
	errVal := payload["error"]
	if !truthy(errVal) && payload["success"] != false {
		return ""
	}
	return failureSuffix(errVal, "FAILED")
}

func jsonObject(text string) map[string]any {
	if strings.TrimSpace(text) == "" {
		return map[string]any{}
	}
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return map[string]any{}
	}
	if dec.More() {
		return map[string]any{}
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

var (
	exitCodeRe   = regexp.MustCompile(`"exit_code"\s*:\s*(-?\d+)`)
	totalCountRe = regexp.MustCompile(`"total_count"\s*:\s*(\d+)`)
)

func refusedSummary(tool string, args map[string]any, content string) (string, bool) {
	payload := jsonObject(content)
	status, _ := payload["status"].(string)
	errText, _ := payload["error"].(string)
	if errText == "" && strings.HasPrefix(strings.TrimLeft(content, " \t\r\n"), "BLOCKED") {
		errText = strings.TrimSpace(content)
	}
	var outcome string
	switch {
	case status == "pending_approval":
		outcome = "awaiting the user's approval, not run"
	case errText != "" && (status == "blocked" || strings.HasPrefix(strings.TrimLeft(errText, " \t\r\n"), "BLOCKED")):
		outcome = "BLOCKED, not run"
		if strings.Contains(errText, "NOT consented") {
			outcome += "; the user did NOT consent, do not retry or reach the same outcome another way"
		}
	default:
		return "", false
	}
	target := strArg(args, "command")
	if target == "" {
		target = strArg(args, "path")
	}
	if target != "" {
		if utf8.RuneCountInString(target) > 60 {
			target = firstRunes(target, 57) + "..."
		}
		target = " `" + target + "`"
	}
	return firstRunes("["+tool+"]"+target+" "+outcome, pruneMinChars-1), true
}

// summarizeToolResult is the one-line stand-in for an old tool result: what was called, with what, and how it
// ended (a failure is marked, so the stub never reads like a success).
func summarizeToolResult(tool, argsJSON, content string) string {
	args, keys := orderedArgs(argsJSON)
	if args == nil {
		args = map[string]any{}
	}
	if s, ok := refusedSummary(tool, args, content); ok {
		return s
	}
	n := utf8.RuneCountInString(content)
	lines := 0
	if strings.TrimSpace(content) != "" {
		lines = strings.Count(content, "\n") + 1
	}
	fail := payloadFailureSuffix(jsonObject(content))
	switch tool {
	case "terminal":
		cmd := strArg(args, "command")
		if utf8.RuneCountInString(cmd) > 80 {
			cmd = firstRunes(cmd, 77) + "..."
		}
		code := "?"
		if m := exitCodeRe.FindStringSubmatch(content); m != nil {
			code = m[1]
		}
		return fmt.Sprintf("[terminal] ran `%s` -> exit %s, %d lines output", cmd, code, lines)
	case "read_file":
		return fmt.Sprintf("[read_file] read %s from line %s (%s chars)", argOr(args, "path", "?"), argOr(args, "offset", "1"), withCommas(n)) + fail
	case "write_file":
		if fail != "" {
			return "[write_file] " + argOr(args, "path", "?") + fail
		}
		written := "?"
		if truthy(args["content"]) {
			written = fmt.Sprint(strings.Count(strArg(args, "content"), "\n") + 1)
		}
		return fmt.Sprintf("[write_file] wrote to %s (%s lines)", argOr(args, "path", "?"), written)
	case "search_files":
		count := "?"
		if m := totalCountRe.FindStringSubmatch(content); m != nil {
			count = m[1]
		}
		return fmt.Sprintf("[search_files] %s search for '%s' in %s -> %s matches", argOr(args, "target", "content"),
			argOr(args, "pattern", "?"), argOr(args, "path", "."), count)
	case "patch":
		return fmt.Sprintf("[patch] %s in %s (%s chars result)", argOr(args, "mode", "replace"), argOr(args, "path", "?"), withCommas(n)) + fail
	case "web_search":
		return fmt.Sprintf("[web_search] query='%s' (%s chars result)", argOr(args, "query", "?"), withCommas(n)) + fail
	}
	var first bytes.Buffer
	for i, k := range keys {
		if i == 2 {
			break
		}
		first.WriteString(" " + k + "=" + firstRunes(pyStr(args[k]), 40))
	}
	return fmt.Sprintf("[%s]%s (%s chars result)", tool, first.String(), withCommas(n)) + fail
}
