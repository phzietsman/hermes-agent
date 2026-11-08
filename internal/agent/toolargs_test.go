package agent

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// Differential: repaired arguments parse to the same value Python's repair produced. Python re-serialises
// valid JSON with ASCII escapes, so values are compared, not bytes.
func TestRepairToolArgumentsMatchesPython(t *testing.T) {
	b, err := os.ReadFile("testdata/toolargs.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct{ Input, Output string }
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		got := RepairToolArguments(c.Input)
		var gv, wv any
		if err := json.Unmarshal([]byte(got), &gv); err != nil {
			t.Errorf("%q: Go output %q is not JSON: %v", c.Input, got, err)
			continue
		}
		if err := json.Unmarshal([]byte(c.Output), &wv); err != nil {
			t.Fatalf("python output %q is not JSON", c.Output)
		}
		if !reflect.DeepEqual(gv, wv) {
			t.Errorf("%q\n got: %s\nwant: %s", c.Input, got, c.Output)
		}
	}
}

func FuzzRepairToolArguments(f *testing.F) {
	for _, s := range []string{"", `{"a":1`, `{"a": [1,`, "None", `{"s":"x` + "\n" + `y"}`, `{"a":1}}`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := RepairToolArguments(s)
		if !json.Valid([]byte(out)) {
			t.Fatalf("not valid JSON: %q -> %q", s, out)
		}
		// Valid output is left alone (up to the surrounding whitespace Python also keeps).
		if again := RepairToolArguments(out); pyStrip(again) != pyStrip(out) {
			t.Fatalf("not stable: %q -> %q -> %q", s, out, again)
		}
	})
}
