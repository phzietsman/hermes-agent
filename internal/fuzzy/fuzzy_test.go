package fuzzy

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func load(t *testing.T, name string, v any) {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

// Differential: ratios, matching blocks and opcodes equal Python's difflib on 79 pairs, including pairs long
// enough for the autojunk heuristic.
func TestSequenceMatcherMatchesDifflib(t *testing.T) {
	var cases []struct {
		A, B    string
		Ratio   float64
		Opcodes [][]any
		Blocks  [][]int
	}
	load(t, "matcher.json", &cases)
	for i, c := range cases {
		sm := NewSequenceMatcher(c.A, c.B)
		if got := sm.Ratio(); got != c.Ratio {
			t.Errorf("case %d: ratio %v, want %v", i, got, c.Ratio)
		}
		var blocks [][]int
		for _, b := range sm.MatchingBlocks() {
			blocks = append(blocks, []int{b.A, b.B, b.Size})
		}
		if !reflect.DeepEqual(blocks, c.Blocks) && !(len(blocks) == 0 && len(c.Blocks) == 0) {
			t.Errorf("case %d: blocks %v, want %v", i, blocks, c.Blocks)
		}
		var ops [][]any
		for _, o := range sm.Opcodes() {
			ops = append(ops, []any{o.Tag, float64(o.I1), float64(o.I2), float64(o.J1), float64(o.J2)})
		}
		if len(ops) != len(c.Opcodes) {
			t.Errorf("case %d: %d opcodes, want %d", i, len(ops), len(c.Opcodes))
			continue
		}
		for k := range ops {
			if !reflect.DeepEqual(ops[k], c.Opcodes[k]) {
				t.Errorf("case %d opcode %d: %v, want %v", i, k, ops[k], c.Opcodes[k])
			}
		}
	}
}

func lines(s string) []string {
	var out []string
	for len(s) > 0 {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			out = append(out, s)
			break
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
	}
	return out
}

func TestUnifiedDiffMatchesDifflib(t *testing.T) {
	var cases []struct{ Old, New, Diff string }
	load(t, "diff.json", &cases)
	for i, c := range cases {
		if got := UnifiedDiff(lines(c.Old), lines(c.New), "a/f", "b/f"); got != c.Diff {
			t.Errorf("case %d\n got: %q\nwant: %q", i, got, c.Diff)
		}
	}
}

func TestFindAndReplaceMatchesPython(t *testing.T) {
	var cases []struct {
		Content, Old, New string
		ReplaceAll        bool `json:"replace_all"`
		Result            string
		Count             int
		Strategy          *string
		Error             *string
		Hint              string
	}
	load(t, "replace.json", &cases)
	strategies := map[string]int{}
	for i, c := range cases {
		got := FindAndReplace(c.Content, c.Old, c.New, c.ReplaceAll)
		wantStrategy, wantErr := "", ""
		if c.Strategy != nil {
			wantStrategy = *c.Strategy
		}
		if c.Error != nil {
			wantErr = *c.Error
		}
		if got.Content != c.Result || got.Count != c.Count || got.Strategy != wantStrategy || got.Error != wantErr {
			t.Errorf("case %d (old=%q)\n got: %q count=%d strategy=%q err=%q\nwant: %q count=%d strategy=%q err=%q", i, c.Old,
				got.Content, got.Count, got.Strategy, got.Error, c.Result, c.Count, wantStrategy, wantErr)
		}
		if h := NoMatchHint(got.Error, got.Count, c.Old, c.Content); h != c.Hint {
			t.Errorf("case %d hint\n got: %q\nwant: %q", i, h, c.Hint)
		}
		strategies[wantStrategy]++
	}
	// The corpus exercises every strategy that can win. indentation_flexible and trimmed_boundary are strictly
	// narrower than line_trimmed, which runs first, so they never produce a match of their own.
	for _, s := range strategies2() {
		if strategies[s] == 0 && s != "indentation_flexible" && s != "trimmed_boundary" {
			t.Errorf("no golden case lands on strategy %s", s)
		}
	}
}

func strategies2() []string {
	var out []string
	for _, s := range strategies {
		out = append(out, s.name)
	}
	return out
}

func TestFindClosestLinesMatchesPython(t *testing.T) {
	var cases []struct{ Old, Content, Closest string }
	load(t, "closest.json", &cases)
	for i, c := range cases {
		if got := FindClosestLines(c.Old, c.Content, 2, 3); got != c.Closest {
			t.Errorf("case %d\n got: %q\nwant: %q", i, got, c.Closest)
		}
	}
}

func TestIsAlreadyApplied(t *testing.T) {
	for _, c := range []struct {
		content, old, new string
		want              bool
	}{
		{"hello brave new world", "hello world", "hello brave new world", true},
		{"hello world", "hello world", "hello brave new world", false},
		{"short", "a", "short", false}, // new text under 8 characters
		{"x = 1", "x = 1", "x = 1", false},
		{"alpha beta gamma", "alpha beta gamma", "alpha beta gamma", true},
	} {
		if got := IsAlreadyApplied(c.content, c.old, c.new); got != c.want {
			t.Errorf("%+v: %v", c, got)
		}
	}
}

func FuzzFindAndReplace(f *testing.F) {
	f.Add("a\nb\nc\n", "b", "B", false)
	f.Add("  x\n\ty\n", "x\ny", "z", true)
	f.Add("é — “q”\n", "é - \"q\"", "ok", false)
	f.Fuzz(func(t *testing.T, content, old, new string, all bool) {
		r := FindAndReplace(content, old, new, all)
		if r.Error != "" && (r.Content != content || r.Count != 0) {
			t.Fatalf("a failed edit changed the content: %+v", r)
		}
		if r.Error == "" && r.Count == 0 {
			t.Fatal("success with no match")
		}
	})
}
