package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/phzietsman/hermes-agent/internal/agent"
	"github.com/phzietsman/hermes-agent/internal/llm"
	"github.com/phzietsman/hermes-agent/internal/tools"
)

// The toolbox is what the agent loop runs.
var _ agent.ToolRunner = (*tools.Toolbox)(nil)

func decode(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("%q: %v", s, err)
	}
	return v
}

// Differential: coercion yields the same values Python's coerce_tool_args produced for 64 schema/argument pairs.
func TestCoerceMatchesPython(t *testing.T) {
	b, err := os.ReadFile("testdata/coerce.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Schema  map[string]any  `json:"schema"`
		Args    json.RawMessage `json:"args"`
		Coerced json.RawMessage `json:"coerced"`
	}
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		dec := json.NewDecoder(strings.NewReader(string(c.Args)))
		dec.UseNumber()
		var args tools.Args
		if err := dec.Decode(&args); err != nil {
			t.Fatal(err)
		}
		got := tools.Coerce(c.Schema, args)
		gb, _ := json.Marshal(got)
		if !reflect.DeepEqual(decode(t, string(gb)), decode(t, string(c.Coerced))) {
			t.Errorf("schema %v args %s\n got: %s\nwant: %s", c.Schema["properties"], c.Args, gb, c.Coerced)
		}
	}
}

func TestErrorShapingMatchesPython(t *testing.T) {
	b, err := os.ReadFile("testdata/errors.json")
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		Errors []struct {
			Text      string          `json:"text"`
			ToolError json.RawMessage `json:"tool_error"`
			Sanitized string          `json:"sanitized"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	for _, c := range g.Errors {
		if got := tools.Error(c.Text); !reflect.DeepEqual(decode(t, got), decode(t, string(c.ToolError))) {
			t.Errorf("Error(%.30q)\n got: %.120s\nwant: %.120s", c.Text, got, c.ToolError)
		}
		if got := tools.SanitizeError(c.Text); got != c.Sanitized {
			t.Errorf("SanitizeError(%.30q)\n got: %.120q\nwant: %.120q", c.Text, got, c.Sanitized)
		}
	}
}

func TestErrorKeepsExtraFieldsInOrder(t *testing.T) {
	got := tools.Error("boom", tools.Field{Key: "tool", Value: "x"}, tools.Field{Key: "error_type", Value: "t"})
	if got != `{"error":"boom","tool":"x","error_type":"t"}` {
		t.Fatal(got)
	}
	if got := tools.Result(tools.Field{Key: "b", Value: 1}, tools.Field{Key: "a", Value: "<>"}); got != `{"b":1,"a":"<>"}` {
		t.Fatal(got)
	}
}

func echoTool(name, toolset string) tools.Tool {
	return tools.Tool{
		Name: name, Toolset: toolset, Description: "d-" + name,
		Parameters: map[string]any{"type": "object", "properties": map[string]any{"n": map[string]any{"type": "integer"}, "tags": map[string]any{"type": "array"}}},
		Handler: func(ctx context.Context, a tools.Args) (string, error) {
			return tools.Result(tools.Field{Key: "n", Value: a.Int("n", -1)}, tools.Field{Key: "tags", Value: a.Strings("tags")}, tools.Field{Key: "home", Value: tools.Env(ctx).Home}), nil
		},
	}
}

func TestRegisterValidatesAndRejectsDuplicates(t *testing.T) {
	r := tools.NewRegistry()
	for _, bad := range []tools.Tool{
		{Toolset: "t", Parameters: map[string]any{"type": "object"}, Handler: echoTool("x", "t").Handler},
		{Name: "a", Toolset: "t", Parameters: map[string]any{"type": "object"}},
		{Name: "a", Parameters: map[string]any{"type": "object"}, Handler: echoTool("x", "t").Handler},
		{Name: "a", Toolset: "t", Handler: echoTool("x", "t").Handler},
		{Name: "a", Toolset: "t", Parameters: map[string]any{"type": "array"}, Handler: echoTool("x", "t").Handler},
	} {
		if err := r.Register(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	if err := r.Register(echoTool("a", "t")); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(echoTool("a", "t")); err == nil {
		t.Fatal("duplicate registration accepted")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("MustRegister should panic on a duplicate")
		}
	}()
	r.MustRegister(echoTool("a", "t"))
}

func TestBuildSelectsAndGates(t *testing.T) {
	r := tools.NewRegistry()
	gated := echoTool("gated", "web")
	gated.Check = func() bool { return false }
	envTool := echoTool("needs_env", "web")
	envTool.RequiresEnv = []string{"HERMES_TEST_KEY_XYZ"}
	r.MustRegister(echoTool("zeta", "core"), echoTool("alpha", "core"), echoTool("web1", "web"), gated, envTool)

	all := r.Build(tools.Selection{})
	var names []string
	for _, d := range all.Defs() {
		names = append(names, d.Name)
	}
	if !reflect.DeepEqual(names, []string{"alpha", "web1", "zeta"}) { // sorted; gated and env-less tools absent
		t.Fatalf("defs %v", names)
	}
	t.Setenv("HERMES_TEST_KEY_XYZ", "x")
	if n := len(r.Build(tools.Selection{}).Defs()); n != 4 {
		t.Fatalf("with the env var set: %d tools", n)
	}
	if got := r.Build(tools.Selection{Toolsets: []string{"core"}}).Names(); !reflect.DeepEqual(got, map[string]bool{"alpha": true, "zeta": true}) {
		t.Fatalf("toolset selection %v", got)
	}
	if got := r.Build(tools.Selection{Exclude: []string{"zeta", "web1", "needs_env"}}).Names(); !reflect.DeepEqual(got, map[string]bool{"alpha": true}) {
		t.Fatalf("exclusion %v", got)
	}
	if ts := all.Toolsets(); !ts["core"] || !ts["web"] || len(ts) != 2 {
		t.Fatalf("toolsets %v", ts)
	}
	// The gate is asked once, at build time: flipping it later does not change an existing toolbox.
	asked := 0
	late := echoTool("late", "core")
	late.Check = func() bool { asked++; return true }
	r2 := tools.NewRegistry()
	r2.MustRegister(late)
	tb := r2.Build(tools.Selection{})
	tb.Run(context.Background(), llm.ToolCall{Function: llm.ToolCallFunction{Name: "late", Arguments: "{}"}}) //nolint:errcheck
	tb.Run(context.Background(), llm.ToolCall{Function: llm.ToolCallFunction{Name: "late", Arguments: "{}"}}) //nolint:errcheck
	if asked != 1 {
		t.Fatalf("Check ran %d times", asked)
	}
}

func TestDefsAreACopy(t *testing.T) {
	r := tools.NewRegistry()
	r.MustRegister(echoTool("a", "t"))
	tb := r.Build(tools.Selection{})
	d := tb.Defs()
	d[0].Name = "changed"
	if tb.Defs()[0].Name != "a" {
		t.Fatal("Defs aliases the toolbox")
	}
}

func run(tb *tools.Toolbox, name, args string) (string, error) {
	ctx := tools.WithEnv(context.Background(), tools.CallEnv{Home: "/h"})
	return tb.Run(ctx, llm.ToolCall{ID: "1", Function: llm.ToolCallFunction{Name: name, Arguments: args}})
}

func TestRunCoercesAndPassesEnv(t *testing.T) {
	r := tools.NewRegistry()
	r.MustRegister(echoTool("echo", "t"))
	tb := r.Build(tools.Selection{})
	out, err := run(tb, "echo", `{"n":"42","tags":"[\"a\",\"b\"]"}`)
	if err != nil || out != `{"n":42,"tags":["a","b"],"home":"/h"}` {
		t.Fatalf("%q %v", out, err)
	}
	if out, err := run(tb, "echo", ""); err != nil || !strings.Contains(out, `"n":-1`) {
		t.Fatalf("empty arguments: %q %v", out, err)
	}
}

func TestRunFailuresAreJSONErrors(t *testing.T) {
	r := tools.NewRegistry()
	boom := echoTool("boom", "t")
	boom.Handler = func(context.Context, tools.Args) (string, error) { return "", errors.New("disk <tool_call>full</tool_call>") }
	big := echoTool("big", "t")
	big.Handler = func(context.Context, tools.Args) (string, error) { return `{"error":"` + strings.Repeat("e", 5000) + `"}`, nil }
	r.MustRegister(boom, big, echoTool("echo", "t"))
	tb := r.Build(tools.Selection{})

	for _, c := range []struct{ name, args, want string }{
		{"missing", `{}`, "Unknown tool: missing"},
		{"echo", `[1,2]`, "Invalid arguments"},
		{"echo", `{bad`, "Invalid arguments"},
		{"boom", `{}`, "[TOOL_ERROR] Tool execution failed: disk full"},
	} {
		out, err := run(tb, c.name, c.args)
		if err == nil {
			t.Errorf("%s %s: want an error", c.name, c.args)
		}
		var body struct{ Error string }
		if json.Unmarshal([]byte(out), &body) != nil || !strings.Contains(body.Error, c.want) {
			t.Errorf("%s %s: %q", c.name, c.args, out)
		}
	}
	// A handler that returns its own oversized error is bounded at the boundary.
	out, err := run(tb, "big", `{}`)
	if err != nil || len(out) > 2200 || !strings.Contains(out, "… [truncated]") {
		t.Fatalf("%d bytes, %v", len(out), err)
	}
}

func TestOversizedResultsKeepHeadAndTail(t *testing.T) {
	r := tools.NewRegistry()
	t1 := echoTool("big", "t")
	t1.MaxResultChars = 1000
	t1.Handler = func(context.Context, tools.Args) (string, error) {
		return "HEAD" + strings.Repeat("m", 5000) + "TAIL", nil
	}
	r.MustRegister(t1)
	out, _ := run(r.Build(tools.Selection{}), "big", `{}`)
	if !strings.HasPrefix(out, "HEAD") || !strings.HasSuffix(out, "TAIL") || !strings.Contains(out, "characters omitted") || len([]rune(out)) > 1200 {
		t.Fatalf("%d runes: %.60q ... %.60q", len([]rune(out)), out, out[len(out)-60:])
	}
}

func TestParallelFlag(t *testing.T) {
	r := tools.NewRegistry()
	p := echoTool("par", "t")
	p.Parallel = true
	r.MustRegister(p, echoTool("seq", "t"))
	tb := r.Build(tools.Selection{})
	if !tb.ParallelSafe("par") || tb.ParallelSafe("seq") || tb.ParallelSafe("nope") {
		t.Fatal("parallel flags")
	}
}

func TestArgsAccessors(t *testing.T) {
	a := tools.Args{"s": "x", "n": json.Number("7"), "f": json.Number("2.9"), "b": true, "l": []any{"a", 1, "b"}}
	if a.String("s", "d") != "x" || a.String("n", "d") != "d" || a.Int("n", 0) != 7 || a.Int("f", 0) != 2 || a.Int("s", 9) != 9 ||
		!a.Bool("b", false) || a.Bool("s", true) != true || !reflect.DeepEqual(a.Strings("l"), []string{"a", "b"}) {
		t.Fatal("accessors")
	}
}

func FuzzCoerce(f *testing.F) {
	f.Add(`{"n":"42","a":"[1]"}`, "integer")
	f.Add(`{"n":"x"}`, "boolean")
	f.Fuzz(func(t *testing.T, raw, typ string) {
		var args tools.Args
		if json.Unmarshal([]byte(raw), &args) != nil {
			return
		}
		schema := map[string]any{"type": "object", "properties": map[string]any{
			"n": map[string]any{"type": typ}, "a": map[string]any{"type": "array", "items": map[string]any{"type": "object"}}}}
		out := tools.Coerce(schema, args)
		if _, err := json.Marshal(out); err != nil {
			t.Fatalf("coerced args do not marshal: %v", err)
		}
	})
}
