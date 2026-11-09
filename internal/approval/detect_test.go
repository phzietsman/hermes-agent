package approval

import (
	"encoding/json"
	"os"
	"testing"
)

type corpusCase struct {
	Cmd       string `json:"cmd"`
	Hardline  []any  `json:"hardline"`
	Dangerous []any  `json:"dangerous"`
	Compound  bool   `json:"compound"`
}

// gatewaySpliceDescription is the verdict Python's cron lifecycle guard adds; it protects the gateway process and
// is ported with the gateway (task 7.x).
const gatewaySpliceDescription = "stop/restart hermes gateway via shell-spliced verb (kills running agents)"

func testDetector() Detector {
	return Detector{Home: "/home/tester", HermesHome: "/home/tester/.hermes"}
}

// Differential: the Go detector gives the same hardline and dangerous verdicts, descriptions included, as the
// Python detector on every command-looking literal from the Python approval tests plus hand-written adversarial
// commands.
func TestDetectionMatchesPython(t *testing.T) {
	b, err := os.ReadFile("testdata/corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []corpusCase
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	d := testDetector()
	bad := 0
	for _, c := range cases {
		wantHard, _ := c.Hardline[0].(bool)
		wantHardDesc, _ := c.Hardline[1].(string)
		gotHard, gotHardDesc := d.DetectHardline(c.Cmd)
		if gotHard != wantHard || (wantHard && gotHardDesc != wantHardDesc) {
			t.Errorf("hardline %q\n got: %v %q\nwant: %v %q", clip(c.Cmd), gotHard, gotHardDesc, wantHard, wantHardDesc)
			bad++
		}
		wantDanger, _ := c.Dangerous[0].(bool)
		wantDesc, _ := c.Dangerous[2].(string)
		if wantDesc == gatewaySpliceDescription {
			continue // verdicts of the gateway lifecycle guard (cron/lifecycle_guard.py), ported with the gateway
		}
		v := d.DetectDangerous(c.Cmd)
		if v.Dangerous != wantDanger || (wantDanger && v.Description != wantDesc) {
			t.Errorf("dangerous %q\n got: %v %q\nwant: %v %q", clip(c.Cmd), v.Dangerous, v.Description, wantDanger, wantDesc)
			bad++
		}
		if bad > 25 {
			t.Fatal("too many mismatches")
		}
	}
}

func clip(s string) string {
	r := []rune(s)
	if len(r) > 120 {
		return string(r[:120]) + "…"
	}
	return s
}
