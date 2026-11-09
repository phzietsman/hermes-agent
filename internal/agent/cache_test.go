package agent

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/phzietsman/hermes-agent/internal/llm"
)

type cacheCase struct {
	Name     string          `json:"name"`
	Messages []llm.Message   `json:"messages"`
	Tools    []llm.ToolDef   `json:"tools"`
	TTL      string          `json:"ttl"`
	Prefix   *string         `json:"prefix"`
	Direct   bool            `json:"direct"`
	Scaffold string          `json:"scaffold"`
	Expected json.RawMessage `json:"expected"`
	Replan   json.RawMessage `json:"expected_replan"`
}

func (c cacheCase) options(reg *stablePrefixes) CacheOptions {
	o := CacheOptions{TTL: c.TTL, DirectNativeToolCache: c.Direct, Prefixes: reg}
	if c.Prefix != nil {
		o.StaticSystemPrefix = *c.Prefix
	}
	return o
}

func loadCacheCases(t testing.TB) []cacheCase {
	t.Helper()
	b, err := os.ReadFile("testdata/cache.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []cacheCase
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 250 {
		t.Fatalf("only %d cache cases", len(cases))
	}
	return cases
}

// normalize round-trips through JSON and drops null-valued keys: Python writes content: null on an assistant
// tool-call turn, which Go omits.
func normalize(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return dropNulls(out)
}

func dropNulls(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			if x == nil {
				delete(t, k)
			} else {
				t[k] = dropNulls(x)
			}
		}
	case []any:
		for i := range t {
			t[i] = dropNulls(t[i])
		}
	}
	return v
}

func decodeExpected(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return dropNulls(v)
}

func planShape(p CachePlan) map[string]any {
	return map[string]any{"messages": p.Messages, "tools": p.Tools}
}

// toolsJSON renders ToolDefs in the OpenAI shape the Python planner uses.
func (p CachePlan) MarshalJSON() ([]byte, error) {
	tools := make([]map[string]any, len(p.Tools))
	for i, td := range p.Tools {
		tools[i] = map[string]any{"type": "function", "function": map[string]any{"name": td.Name, "description": td.Description, "parameters": td.Parameters}}
		if td.CacheControl != nil {
			tools[i]["cache_control"] = td.CacheControl.Map()
		}
	}
	return json.Marshal(map[string]any{"messages": p.Messages, "tools": tools})
}

func TestCachePlanMatchesPython(t *testing.T) {
	bad := 0
	for _, c := range loadCacheCases(t) {
		reg := &stablePrefixes{}
		reg.Register(c.Scaffold)
		opts := c.options(reg)
		before := normalize(t, planShape(CachePlan{Messages: c.Messages, Tools: c.Tools}))

		plan := BuildPromptCachePlan(c.Messages, c.Tools, opts)
		if got, want := normalize(t, plan), decodeExpected(t, c.Expected); !reflect.DeepEqual(got, want) {
			bad++
			if bad <= 2 {
				t.Errorf("%s: plan differs\n--- python\n%s\n--- go\n%s", c.Name, pj(want), pj(got))
			}
			continue
		}
		// The planner must be idempotent: planning its own output again gives what Python gave.
		replan := BuildPromptCachePlan(plan.Messages, plan.Tools, opts)
		if got, want := normalize(t, replan), decodeExpected(t, c.Replan); !reflect.DeepEqual(got, want) {
			bad++
			if bad <= 2 {
				t.Errorf("%s: re-plan differs\n--- python\n%s\n--- go\n%s", c.Name, pj(want), pj(got))
			}
			continue
		}
		// And it never rewrites its inputs: the stored transcript stays undecorated.
		if after := normalize(t, planShape(CachePlan{Messages: c.Messages, Tools: c.Tools})); !reflect.DeepEqual(before, after) {
			bad++
			t.Errorf("%s: the planner mutated its input", c.Name)
		}
	}
	if bad > 0 {
		t.Fatalf("%d cache cases differ from Python", bad)
	}
}

func pj(v any) string { b, _ := json.MarshalIndent(v, "", " "); return string(b) }

// countMarkers counts the wire-visible cache markers in a plan.
func countMarkers(p CachePlan) int {
	n := 0
	for _, m := range p.Messages {
		if m.CacheControl != nil {
			n++
		}
		for _, part := range m.Content.AsParts() {
			if hasCC(part) {
				n++
			}
		}
	}
	for _, td := range p.Tools {
		if td.CacheControl != nil {
			n++
		}
	}
	return n
}

func TestPlanNeverExceedsFourBreakpoints(t *testing.T) {
	for _, c := range loadCacheCases(t) {
		reg := &stablePrefixes{}
		reg.Register(c.Scaffold)
		p := BuildPromptCachePlan(c.Messages, c.Tools, c.options(reg))
		if n := countMarkers(p); n > 4 {
			t.Errorf("%s: %d breakpoints, the API allows 4", c.Name, n)
		}
		if n := countMarkers(BuildPromptCachePlan(p.Messages, p.Tools, c.options(reg))); n > 4 {
			t.Errorf("%s: re-planning accumulated %d breakpoints", c.Name, n)
		}
	}
}

func TestCacheMarkersShareOneTTL(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleSystem, Content: llm.Text("static\n\nvolatile")}, {Role: llm.RoleUser, Content: llm.Text("q")},
		{Role: llm.RoleAssistant, Content: llm.Text("a")}, {Role: llm.RoleUser, Content: llm.Text("q2")},
	}
	tools := []llm.ToolDef{{Name: "t"}}
	for _, ttl := range []string{"5m", "1h", ""} {
		p := BuildPromptCachePlan(msgs, tools, CacheOptions{TTL: ttl, StaticSystemPrefix: "static", DirectNativeToolCache: true, Prefixes: &stablePrefixes{}})
		var ttls []string
		collect := func(cc map[string]any) { s, _ := cc["ttl"].(string); ttls = append(ttls, s) }
		for _, m := range p.Messages {
			if m.CacheControl != nil {
				collect(m.CacheControl.Map())
			}
			for _, part := range m.Content.AsParts() {
				if cc, ok := part["cache_control"].(map[string]any); ok {
					collect(cc)
				}
			}
		}
		if p.Tools[0].CacheControl != nil {
			collect(p.Tools[0].CacheControl.Map())
		}
		for _, got := range ttls {
			if got != ttls[0] {
				t.Fatalf("ttl %q: mixed TTLs in one request: %v", ttl, ttls)
			}
		}
		want := ""
		if ttl == "1h" {
			want = "1h"
		}
		if len(ttls) == 0 || ttls[0] != want {
			t.Errorf("ttl %q produced markers %v, want ttl field %q", ttl, ttls, want)
		}
	}
}

func TestTTLPolicy(t *testing.T) {
	for in, want := range map[string]string{"": "5m", "5m": "5m", "1h": "1h", "weird": "weird"} {
		if got := EffectiveCacheTTL(in); got != want {
			t.Errorf("EffectiveCacheTTL(%q) = %q, want %q", in, got, want)
		}
	}
	for src, want := range map[string]string{
		"cli": "1h", "tui": "1h", "telegram": "1h", "discord": "1h", "": "1h", "desktop": "1h",
		"cron": "5m", "subagent": "5m", "CRON": "5m", " webhook ": "5m", "batch": "5m", "api": "5m",
	} {
		if got := AutoCacheTTLForSource(src); got != want {
			t.Errorf("AutoCacheTTLForSource(%q) = %q, want %q", src, got, want)
		}
	}
}

func TestStripCacheControlRestoresStringContent(t *testing.T) {
	reg := &stablePrefixes{}
	msgs := []llm.Message{
		{Role: llm.RoleSystem, Content: llm.Text("static part, volatile part")},
		{Role: llm.RoleUser, Content: llm.Text("hello")},
		{Role: llm.RoleTool, ToolCallID: "c", Content: llm.Text("r")},
	}
	planned := ApplyCacheControl(msgs, CacheOptions{TTL: "1h", StaticSystemPrefix: "static part", Prefixes: reg})
	restored := StripCacheControl(planned, reg)
	if !reflect.DeepEqual(normalize(t, restored), normalize(t, msgs)) {
		t.Fatalf("strip did not restore the original shape:\n%s\n%s", pj(normalize(t, restored)), pj(normalize(t, msgs)))
	}
	// A user-supplied list of two parts is not decoration and survives untouched.
	two := []llm.Message{{Role: llm.RoleUser, Content: llm.Parts(
		llm.Block{"type": "text", "text": "a"}, llm.Block{"type": "text", "text": "b"})}}
	if got := StripCacheControl(two, reg); !got[0].Content.IsParts() {
		t.Error("a genuine two-part user message must stay a list")
	}
}

func TestStablePrefixRegistry(t *testing.T) {
	r := &stablePrefixes{}
	r.Register("")
	if _, ok := r.Find("anything"); ok {
		t.Fatal("empty prefix must not register")
	}
	r.Register("SCAFFOLD ")
	r.Register("SCAFFOLD LONGER ")
	tests := []struct {
		content, want string
		ok            bool
	}{
		{"SCAFFOLD tail", "SCAFFOLD ", true},
		{"SCAFFOLD LONGER tail", "SCAFFOLD LONGER ", true}, // the longest registered prefix wins
		{"SCAFFOLD ", "", false},                           // an empty tail would be an empty text block (400)
		{"SCAFFOLD    \n", "", false},                      // so would a blank one
		{"unrelated", "", false},
		{"SCAFF", "", false},
	}
	for _, tc := range tests {
		got, ok := r.Find(tc.content)
		if got != tc.want || ok != tc.ok {
			t.Errorf("Find(%q) = %q, %v; want %q, %v", tc.content, got, ok, tc.want, tc.ok)
		}
	}
}

func TestStablePrefixRegistryIsBoundedAndLRU(t *testing.T) {
	r := &stablePrefixes{}
	for i := 0; i < stableMaxEntries+5; i++ {
		r.Register(strings.Repeat("x", i+1) + "|")
	}
	if len(r.order) != stableMaxEntries {
		t.Fatalf("entries = %d, want %d", len(r.order), stableMaxEntries)
	}
	if _, ok := r.Find("x|tail"); ok {
		t.Error("the oldest entry should have been evicted")
	}
	// A hit refreshes its LRU slot, so a cron scaffold survives a burst of one-offs.
	r.Reset()
	r.Register("CRON|")
	for i := 0; i < stableMaxEntries-1; i++ {
		r.Register(strings.Repeat("o", i+1) + "#")
	}
	if _, ok := r.Find("CRON|tail"); !ok {
		t.Fatal("CRON should still be present")
	}
	for i := 0; i < 5; i++ {
		r.Register(strings.Repeat("n", i+1) + "@")
	}
	if _, ok := r.Find("CRON|tail"); !ok {
		t.Error("a recently used scaffold must outlive older unused ones")
	}
	// The newest entry is always kept, even one larger than the whole character budget.
	r.Reset()
	r.Register("a|")
	r.Register(strings.Repeat("z", stableMaxChars+10) + "|")
	if len(r.order) != 1 {
		t.Errorf("entries = %d, want only the oversized newest", len(r.order))
	}
}

func TestIterationBudget(t *testing.T) {
	b := NewIterationBudget(3)
	if b.Max() != 3 || b.Remaining() != 3 || b.Used() != 0 {
		t.Fatalf("fresh: %d/%d/%d", b.Max(), b.Used(), b.Remaining())
	}
	for i := 0; i < 3; i++ {
		if !b.Consume() {
			t.Fatalf("consume %d refused", i)
		}
	}
	if b.Consume() || b.Remaining() != 0 || b.Used() != 3 {
		t.Fatal("an exhausted budget must refuse")
	}
	b.Refund()
	if b.Used() != 2 || !b.Consume() {
		t.Fatal("refund must give one back")
	}
	z := NewIterationBudget(0)
	z.Refund()
	if z.Used() != 0 || z.Consume() {
		t.Fatal("a zero budget stays at zero")
	}
}

func TestIterationBudgetIsSafeUnderConcurrency(t *testing.T) {
	b := NewIterationBudget(100)
	done := make(chan int, 8)
	for g := 0; g < 8; g++ {
		go func() {
			n := 0
			for i := 0; i < 50; i++ {
				if b.Consume() {
					n++
				}
			}
			done <- n
		}()
	}
	total := 0
	for g := 0; g < 8; g++ {
		total += <-done
	}
	if total != 100 || b.Used() != 100 {
		t.Fatalf("granted %d, used %d: the cap must hold exactly across goroutines", total, b.Used())
	}
}

func TestNormalizeBudgetWarningRatio(t *testing.T) {
	for _, v := range []float64{0.5, 0.0001, 0.9999} {
		if got, ok := NormalizeBudgetWarningRatio(v); !ok || got != v {
			t.Errorf("%v rejected", v)
		}
	}
	for _, v := range []float64{0, 1, -0.5, 1.5, nanFloat(), infFloat()} {
		if _, ok := NormalizeBudgetWarningRatio(v); ok {
			t.Errorf("%v accepted", v)
		}
	}
}

func nanFloat() float64 { z := 0.0; return z / z }
func infFloat() float64 { z := 0.0; return 1 / z }

// A marker may land on a blank text part (a message that is only whitespace): the planner matches Python
// there, and the converter in provider/anthropic relocates or replaces blank blocks before the wire.
func FuzzPlanInvariants(f *testing.F) {
	for _, seed := range []string{
		`[{"role":"system","content":"S rest"},{"role":"user","content":"q"},{"role":"assistant","content":"a"},{"role":"user","content":"q2"}]`,
		`[{"role":"user","content":"x"},{"role":"assistant","tool_calls":[{"id":"1","function":{"name":"t"}}]},{"role":"tool","tool_call_id":"1","content":"r"}]`,
		`[{"role":"system","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]},{"role":"user","content":[]}]`,
		`[{"role":"user","content":"SCAF tail"},{"role":"user","content":"SCAF "}]`,
		`[{"role":"tool","tool_call_id":"c","content":{"_multimodal":true,"content":[],"text_summary":""}}]`,
		`[]`,
	} {
		f.Add(seed, "S", true, true)
		f.Add(seed, "", false, false)
	}
	f.Fuzz(func(t *testing.T, raw, prefix string, withTools, oneHour bool) {
		var msgs []llm.Message
		if json.Unmarshal([]byte(raw), &msgs) != nil {
			t.Skip()
		}
		reg := &stablePrefixes{}
		reg.Register("SCAF ")
		o := CacheOptions{TTL: "5m", StaticSystemPrefix: prefix, DirectNativeToolCache: withTools, Prefixes: reg}
		if oneHour {
			o.TTL = "1h"
		}
		var tools []llm.ToolDef
		if withTools {
			tools = []llm.ToolDef{{Name: "a"}, {Name: "b"}}
		}
		before, _ := json.Marshal(CachePlan{Messages: msgs, Tools: tools})
		p := BuildPromptCachePlan(msgs, tools, o)
		if n := countMarkers(p); n > 4 {
			t.Fatalf("%d breakpoints", n)
		}
		if after, _ := json.Marshal(CachePlan{Messages: msgs, Tools: tools}); string(before) != string(after) {
			t.Fatal("the planner mutated its input")
		}
		p2 := BuildPromptCachePlan(p.Messages, p.Tools, o)
		a, _ := json.Marshal(p)
		b, _ := json.Marshal(p2)
		if string(a) != string(b) {
			t.Fatalf("not idempotent:\n%s\n%s", a, b)
		}
	})
}
