// Package evals is the memory eval harness: seeded scenarios driven against a real agent, scored by the Jev
// judge, written in the ScenarioResult shape the Python harness used (so results compare directly with
// evals/memory/*.json). Scenario instances are exported from the Python harness (legacy/python/scripts/port/gen_eval_scenarios.py)
// and embedded, so the wording a model sees is the wording that was tuned.
package evals

// ScenarioResult is one scenario run for one seed. The JSON shape is Python's.
type ScenarioResult struct {
	Scenario     string         `json:"scenario"`
	Passed       int            `json:"passed"`
	Total        int            `json:"total"`
	RequiresLive bool           `json:"requires_live"`
	Seed         int            `json:"seed"`
	Detail       map[string]int `json:"detail"`
}

// Rate is passed/total, 0 when there was nothing to pass.
func (r ScenarioResult) Rate() float64 {
	if r.Total == 0 {
		return 0
	}
	return float64(r.Passed) / float64(r.Total)
}

// Report is the file a run writes.
type Report struct {
	Arm         string           `json:"arm"`
	Seed        int              `json:"seed"`
	HarnessOnly bool             `json:"harness_only"`
	Results     []ScenarioResult `json:"results"`
}

func result(name string, passed, total, seed int, detail map[string]int) ScenarioResult {
	if detail == nil {
		detail = map[string]int{}
	}
	return ScenarioResult{Scenario: name, Passed: passed, Total: total, Seed: seed, Detail: detail}
}
