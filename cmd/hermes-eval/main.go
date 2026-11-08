// Command hermes-eval runs the memory evals against the real agent, the real memory service and the Jev judge.
//
//	hermes-eval memory -seeds 1,2,3 -out evals/memory/loopfix_sam_go.json
//	hermes-eval latency -out evals/memory/regression_go.json
//
// Credentials come from the environment: ANTHROPIC_API_KEY, TYPESAFE_API_KEY, AGENT_MEMORY_API_KEY,
// AGENT_MEMORY_ENDPOINT and AGENT_MEMORY_CONTEXT (or AGENT_MEMORY_ID).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/phzietsman/hermes-agent/internal/app"
	"github.com/phzietsman/hermes-agent/internal/evals"
	"github.com/phzietsman/hermes-agent/internal/memory/sam"
)

const defaultModel = "claude-haiku-5-5"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, out, errw *os.File) int {
	if len(args) == 0 {
		fmt.Fprintln(errw, "usage: hermes-eval {memory|latency} [flags]")
		return 2
	}
	switch args[0] {
	case "memory":
		return memoryCmd(ctx, args[1:], out, errw)
	case "latency":
		return latencyCmd(ctx, args[1:], out, errw)
	}
	fmt.Fprintf(errw, "hermes-eval: unknown command %q\n", args[0])
	return 2
}

func envOr(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

func samEnv() (evals.Env, error) {
	e := evals.Env{AnthropicKey: envOr("ANTHROPIC_API_KEY"), SAMEndpoint: envOr("AGENT_MEMORY_ENDPOINT"),
		SAMContext: envOr("AGENT_MEMORY_CONTEXT", "AGENT_MEMORY_ID"), SAMKey: envOr("AGENT_MEMORY_API_KEY")}
	var missing []string
	for name, v := range map[string]string{"AGENT_MEMORY_ENDPOINT": e.SAMEndpoint, "AGENT_MEMORY_CONTEXT": e.SAMContext, "AGENT_MEMORY_API_KEY": e.SAMKey} {
		if v == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return e, fmt.Errorf("missing %s", strings.Join(missing, ", "))
	}
	return e, nil
}

func parseSeeds(s string) ([]int, error) {
	var out []int
	for _, p := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return nil, fmt.Errorf("bad seed %q", p)
		}
		out = append(out, n)
	}
	return out, nil
}

func memoryCmd(ctx context.Context, args []string, out, errw *os.File) int {
	fs := flag.NewFlagSet("memory", flag.ContinueOnError)
	fs.SetOutput(errw)
	seeds := fs.String("seeds", "1,2,3", "comma-separated seeds")
	sizes := fs.String("sizes", "8,4,4,3", "per seed: recall,supersession,abstention,preferences")
	only := fs.String("only", "", "run a single scenario ("+strings.Join(evals.Scenarios, ", ")+")")
	outPath := fs.String("out", "", "write the report here (required)")
	model := fs.String("model", defaultModel, "model under test")
	workers := fs.Int("workers", 3, "scenario runs in parallel")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *outPath == "" {
		fmt.Fprintln(errw, "hermes-eval: -out is required")
		return 2
	}
	env, err := samEnv()
	if err != nil {
		fmt.Fprintf(errw, "hermes-eval: %v\n", err)
		return 1
	}
	env.Model = *model
	jevKey := envOr("TYPESAFE_API_KEY")
	if jevKey == "" || env.AnthropicKey == "" {
		fmt.Fprintln(errw, "hermes-eval: ANTHROPIC_API_KEY and TYPESAFE_API_KEY are required")
		return 1
	}
	z, err := evals.ParseSizes(*sizes)
	if err != nil {
		fmt.Fprintf(errw, "hermes-eval: %v\n", err)
		return 2
	}
	sd, err := parseSeeds(*seeds)
	if err != nil {
		fmt.Fprintf(errw, "hermes-eval: %v\n", err)
		return 2
	}
	names := evals.Scenarios
	if *only != "" {
		names = []string{*only}
	}
	root, err := os.MkdirTemp("", "memeval-")
	if err != nil {
		fmt.Fprintf(errw, "hermes-eval: %v\n", err)
		return 1
	}
	defer os.RemoveAll(root)
	ctx, cancel := context.WithTimeout(ctx, evals.Timeout)
	defer cancel()
	results, err := evals.Run(ctx, names, sd, z, env, &evals.JevJudge{APIKey: jevKey}, root, *workers, app.Options{})
	if err != nil {
		fmt.Fprintf(errw, "hermes-eval: %v\n", err)
		return 1
	}
	if err := evals.WriteReport(*outPath, "loopfix_sam", sd, results); err != nil {
		fmt.Fprintf(errw, "hermes-eval: %v\n", err)
		return 1
	}
	for _, l := range evals.Summarize(results) {
		fmt.Fprintln(out, "loopfix_sam", l)
	}
	return 0
}

func latencyCmd(ctx context.Context, args []string, out, errw *os.File) int {
	fs := flag.NewFlagSet("latency", flag.ContinueOnError)
	fs.SetOutput(errw)
	outPath := fs.String("out", "", "write the result here (required)")
	n := fs.Int("n", 20, "cold prefetch and raw recall samples")
	warmN := fs.Int("warm-n", 10, "warm prefetch samples")
	gap := fs.Duration("warm-gap", 10*time.Second, "pause standing in for the model's own turn")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *outPath == "" {
		fmt.Fprintln(errw, "hermes-eval: -out is required")
		return 2
	}
	env, err := samEnv()
	if err != nil {
		fmt.Fprintf(errw, "hermes-eval: %v\n", err)
		return 1
	}
	home, err := os.MkdirTemp("", "memreg-")
	if err != nil {
		fmt.Fprintf(errw, "hermes-eval: %v\n", err)
		return 1
	}
	defer os.RemoveAll(home)
	tag := strconv.FormatInt(time.Now().UnixNano(), 16)
	cfg, _ := json.Marshal(map[string]any{"endpoint": env.SAMEndpoint, "context": env.SAMContext,
		"default_scope": "user/reg-" + tag, "agent_scope": "agent/reg-" + tag, "timeout_s": 30})
	if err := os.WriteFile(filepath.Join(home, sam.ConfigFile), cfg, 0o600); err != nil {
		fmt.Fprintf(errw, "hermes-eval: %v\n", err)
		return 1
	}
	s, why := sam.LoadSettings(home, env.SAMKey)
	if s == nil {
		fmt.Fprintf(errw, "hermes-eval: %s\n", why)
		return 1
	}
	res, err := evals.MeasureLatency(ctx, s, home, *n, *warmN, *gap)
	if err != nil {
		fmt.Fprintf(errw, "hermes-eval: %v\n", err)
		return 1
	}
	b, _ := json.MarshalIndent(res, "", "  ")
	if err := os.WriteFile(*outPath, append(b, '\n'), 0o644); err != nil {
		fmt.Fprintf(errw, "hermes-eval: %v\n", err)
		return 1
	}
	fmt.Fprintln(out, string(b))
	return 0
}
