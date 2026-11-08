package evals

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/phzietsman/hermes-agent/internal/app"
)

// Scenario names, as the Python orchestrator spells them.
var Scenarios = []string{"recall", "supersession", "abstention", "preference", "procedural"}

// Sizes are the per-seed scenario sizes: recall, supersession, abstention, preferences.
type Sizes struct{ Recall, Supersession, Abstention, Preference int }

// DefaultSizes are Python's.
var DefaultSizes = Sizes{8, 4, 4, 3}

// ParseSizes reads "8,4,4,3".
func ParseSizes(s string) (Sizes, error) {
	var z Sizes
	if _, err := fmt.Sscanf(s, "%d,%d,%d,%d", &z.Recall, &z.Supersession, &z.Abstention, &z.Preference); err != nil {
		return z, fmt.Errorf("sizes must be four comma-separated numbers (recall,supersession,abstention,preference): %w", err)
	}
	return z, nil
}

// RunScenario runs one scenario for one seed in its own isolated arm.
func RunScenario(ctx context.Context, name string, seed int, z Sizes, env Env, judge Judge, workDir string, extra app.Options) ([]ScenarioResult, error) {
	arm, err := NewSAMArm(ctx, workDir, env, extra)
	if err != nil {
		return nil, err
	}
	defer arm.Close()
	sc := JudgeScorer{J: judge}
	switch name {
	case "recall":
		return RecallAndLanding(ctx, arm, seed, z.Recall, sc)
	case "supersession":
		return Supersession(ctx, arm, seed, z.Supersession, sc)
	case "abstention":
		return Abstention(ctx, arm, seed, z.Abstention, sc)
	case "preference":
		return Preference(ctx, arm, seed, z.Preference, sc)
	case "procedural":
		arm.UseTools("terminal", "file", "memory", "skills")
		work := filepath.Join(arm.Home(), "work")
		if err := os.MkdirAll(work, 0o755); err != nil {
			return nil, err
		}
		return Procedural(ctx, arm, work, seed)
	}
	return nil, fmt.Errorf("unknown scenario %q (have %s)", name, strings.Join(Scenarios, ", "))
}

// Run runs every (scenario, seed) with at most workers at a time and returns the results in a stable order. The
// first error aborts the run: a half report would read as a regression.
func Run(ctx context.Context, scenarios []string, seeds []int, z Sizes, env Env, judge Judge, root string, workers int, extra app.Options) ([]ScenarioResult, error) {
	type job struct {
		name string
		seed int
	}
	var jobs []job
	for _, n := range scenarios {
		for _, s := range seeds {
			jobs = append(jobs, job{n, s})
		}
	}
	out := make([][]ScenarioResult, len(jobs))
	errs := make([]error, len(jobs))
	sem := make(chan struct{}, max(1, workers))
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			dir := filepath.Join(root, fmt.Sprintf("%s-%d", j.name, j.seed))
			if err := os.MkdirAll(dir, 0o700); err != nil {
				errs[i] = err
				return
			}
			out[i], errs[i] = RunScenario(ctx, j.name, j.seed, z, env, judge, dir, extra)
			if errs[i] != nil {
				errs[i] = fmt.Errorf("%s seed=%d: %w", j.name, j.seed, errs[i])
			}
		}()
	}
	wg.Wait()
	var all []ScenarioResult
	for i := range jobs {
		if errs[i] != nil {
			return nil, errs[i]
		}
		all = append(all, out[i]...)
	}
	return all, nil
}

// Summarize is Python's per-scenario line: mean, min, max and each seed's score.
func Summarize(results []ScenarioResult) []string {
	by := map[string][]ScenarioResult{}
	for _, r := range results {
		by[r.Scenario] = append(by[r.Scenario], r)
	}
	names := make([]string, 0, len(by))
	for n := range by {
		names = append(names, n)
	}
	sort.Strings(names)
	var lines []string
	for _, n := range names {
		rs := by[n]
		sort.Slice(rs, func(i, j int) bool { return rs[i].Seed < rs[j].Seed })
		sum, lo, hi := 0.0, 2.0, -1.0
		var per []string
		for _, r := range rs {
			sum += r.Rate()
			lo, hi = min(lo, r.Rate()), max(hi, r.Rate())
			per = append(per, fmt.Sprintf("%d/%d", r.Passed, r.Total))
		}
		lines = append(lines, fmt.Sprintf("%-16s mean %.0f%% (min %.0f%%, max %.0f%%)  [%s]", n, 100*sum/float64(len(rs)), 100*lo, 100*hi, strings.Join(per, " ")))
	}
	return lines
}

// WriteReport writes the Python-shaped report.
func WriteReport(path, arm string, seeds []int, results []ScenarioResult) error {
	if len(seeds) == 0 {
		seeds = []int{0}
	}
	b, err := json.MarshalIndent(Report{Arm: arm, Seed: seeds[0], Results: results}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// Timeout is the longest a single scenario run may take.
const Timeout = time.Hour
