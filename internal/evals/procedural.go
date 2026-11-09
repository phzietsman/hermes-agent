package evals

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

// TaskMetrics is what one procedural task cost.
type TaskMetrics map[string]int

// ProcArm runs tasks with terminal and file tools in a scratch directory.
type ProcArm interface {
	StartSession(ctx context.Context) error
	RunTask(ctx context.Context, text string) (TaskMetrics, error)
	SkillsCreated() int
}

type week struct {
	Files    map[string]string `json:"files"`
	Expected json.RawMessage   `json:"expected"`
}

// Procedural is scenario 5: a multi-step procedure taught once must be repeatable from a terse request. Run 1
// spells out a quirky "weekly rollup" recipe; runs 2 to 4 (fresh sessions, new data) only say "run the weekly
// rollup on <dir>", so reproducing the output needs the recipe from memory or a skill. Correctness is a
// deterministic file comparison; cost is tool calls, errors, tokens and wall time per run.
func Procedural(ctx context.Context, arm ProcArm, work string, seed int) ([]ScenarioResult, error) {
	var in struct {
		Weeks  []week `json:"weeks"`
		Recipe string `json:"recipe"`
		Terse  string `json:"terse"`
	}
	if err := load(fmt.Sprintf("procedural/%d", seed), &in); err != nil {
		return nil, err
	}
	dirs := make([]string, len(in.Weeks))
	for i, w := range in.Weeks {
		dirs[i] = filepath.Join(work, fmt.Sprintf("week%d", i+1))
		if err := os.MkdirAll(dirs[i], 0o755); err != nil {
			return nil, err
		}
		for name, body := range w.Files {
			if err := os.WriteFile(filepath.Join(dirs[i], name), []byte(body), 0o644); err != nil {
				return nil, err
			}
		}
	}
	// Python's recipe is a format string with {dir} and doubled braces.
	render := func(tpl, dir string) string {
		return strings.ReplaceAll(strings.NewReplacer("{{", "{", "}}", "}").Replace(tpl), "{dir}", dir)
	}
	first, err := arm.RunTask(ctx, render(in.Recipe, dirs[0]))
	if err != nil {
		return nil, err
	}
	if err := arm.StartSession(ctx); err != nil { // session end: memory flushes and any background review runs
		return nil, err
	}
	created := arm.SkillsCreated()
	ok := 0
	sums := map[string]int{}
	for i := 1; i < len(dirs); i++ {
		if err := arm.StartSession(ctx); err != nil {
			return nil, err
		}
		t0 := time.Now()
		m, err := arm.RunTask(ctx, render(in.Terse, dirs[i]))
		if err != nil {
			return nil, err
		}
		if Correct(dirs[i], in.Weeks[i].Expected) {
			ok++
		}
		for k, v := range m {
			sums[k] += v
		}
		sums["ms"] += int(time.Since(t0).Milliseconds())
	}
	if err := arm.StartSession(ctx); err != nil {
		return nil, err
	}
	detail := map[string]int{"run1_correct": b2i(Correct(dirs[0], in.Weeks[0].Expected)), "run1_calls": first["calls"],
		"run1_tokens": first["tokens"], "skills_created": created}
	for k, v := range sums {
		detail["reuse_"+k] = v
	}
	return []ScenarioResult{result("5_procedural", ok, len(dirs)-1, seed, detail)}, nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Correct reports whether dir/rollup.json decodes to the expected document.
func Correct(dir string, expected json.RawMessage) bool {
	raw, err := os.ReadFile(filepath.Join(dir, "rollup.json"))
	if err != nil {
		return false
	}
	var got, want any
	if json.Unmarshal(raw, &got) != nil || json.Unmarshal(expected, &want) != nil {
		return false
	}
	return reflect.DeepEqual(got, want)
}
