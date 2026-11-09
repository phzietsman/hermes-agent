package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/phzietsman/hermes-agent/internal/app"
	"github.com/phzietsman/hermes-agent/internal/config"
	"github.com/phzietsman/hermes-agent/internal/memory"
	"github.com/phzietsman/hermes-agent/internal/memory/sam"
)

const memoryUsage = "usage: hermes agent_memory {status|doctor|scopes-init|replay|import}\n"

// agentMemory runs `hermes agent_memory <command>`. It talks to the service directly: no model credential is
// needed, so it does not open the full runtime unless sessions are imported.
func agentMemory(ctx context.Context, args []string, io stdio, opts app.Options) int {
	if len(args) == 0 {
		fmt.Fprint(io.err, memoryUsage)
		return 2
	}
	fs := newFlags("agent_memory "+args[0], io)
	profile := fs.String("profile", "", "profile name (default: the ambient home)")
	dry := fs.Bool("dry-run", false, "import: show what would be written; write nothing")
	sessions := fs.Int("sessions", 0, "import: also backfill the last N sessions")
	rate := fs.Duration("rate", 0, "import: pause between writes")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	home, err := profileHome(ctx, *profile)
	if err != nil {
		return fatal(io.err, err)
	}
	secrets := config.LoadSecrets(home)
	key, _ := secrets.Lookup(sam.APIKeyEnv)
	settings, why := sam.LoadSettings(home, key)
	switch args[0] {
	case "status":
		fmt.Fprintf(io.out, "config file : %s\n", filepath.Join(config.DisplayHome(home), sam.ConfigFile))
		if settings == nil {
			fmt.Fprintf(io.out, "configured  : no (%s)\n", why)
			return 1
		}
		pending, _ := sam.NewOutbox(filepath.Join(home, "agent-memory-outbox.jsonl")).Pending()
		fmt.Fprintf(io.out, "endpoint    : %s\ncontext     : %s\nscopes      : %s, %s\nrecall mode : %s\noutbox      : %d pending write(s)\n",
			settings.Endpoint, settings.Context, settings.DefaultScope, settings.AgentScope, settings.RecallMode, len(pending))
		return 0
	case "doctor", "scopes-init", "replay", "import":
	default:
		fmt.Fprint(io.err, memoryUsage)
		return 2
	}
	if settings == nil {
		fmt.Fprintf(io.out, "✗ not configured: %s\n", why)
		return 1
	}
	c := settings.Client()
	if opts.SAM != nil {
		cc := *opts.SAM
		cc.Endpoint, cc.Context, cc.APIKey = orStr(cc.Endpoint, settings.Endpoint), orStr(cc.Context, settings.Context), orStr(cc.APIKey, settings.APIKey)
		c = &cc
	}
	switch args[0] {
	case "doctor":
		return doctor(ctx, c, io)
	case "scopes-init":
		return scopesInit(ctx, c, settings, io)
	case "replay":
		return replay(ctx, c, home, io)
	}
	return importCmd(ctx, c, home, settings, io, memory.ImportOptions{DryRun: *dry, Rate: *rate, Out: func(s string) { fmt.Fprintln(io.out, s) }}, *sessions, opts)
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func doctor(ctx context.Context, c *sam.Client, io stdio) int {
	ok := true
	probe := func(label string, f func() error) {
		if err := f(); err != nil {
			ok = false
			fmt.Fprintf(io.out, "✗ %s: %v\n", label, err)
			return
		}
		fmt.Fprintf(io.out, "✓ %s\n", label)
	}
	probe("whoami", func() error { _, err := c.Whoami(ctx); return err })
	probe("profile", func() error { _, err := c.Profile(ctx); return err })
	probe("recall", func() error { _, err := c.Recall(ctx, "ping", 1, nil); return err })
	if !ok {
		return 1
	}
	return 0
}

func scopesInit(ctx context.Context, c *sam.Client, s *sam.Settings, io stdio) int {
	seen := map[string]bool{}
	for _, scope := range []string{s.DefaultScope, s.AgentScope} {
		if seen[scope] {
			continue
		}
		seen[scope] = true
		if err := c.RegisterScope(ctx, scope); err != nil {
			fmt.Fprintf(io.out, "✗ %s: %v\n", scope, err)
			return 1
		}
		fmt.Fprintf(io.out, "✓ registered %s\n", scope)
	}
	return 0
}

func replay(ctx context.Context, c *sam.Client, home string, io stdio) int {
	o := sam.NewOutbox(filepath.Join(home, "agent-memory-outbox.jsonl"))
	before, _ := o.Pending()
	applied, err := o.Replay(ctx, func(ctx context.Context, e sam.Entry) error { return sam.Apply(ctx, c, e) })
	after, _ := o.Pending()
	fmt.Fprintf(io.out, "replayed %d/%d; %d left\n", applied, len(before), len(after))
	if err != nil || applied != len(before) {
		return 1
	}
	return 0
}

func importCmd(ctx context.Context, c *sam.Client, home string, s *sam.Settings, io stdio, o memory.ImportOptions, sessions int, opts app.Options) int {
	prefix := ""
	if o.DryRun {
		prefix = "Dry run: "
	}
	fmt.Fprintf(io.out, "%simporting builtin memory from %s\n", prefix, filepath.Join(config.DisplayHome(home), "memories"))
	reports := []memory.ImportReport{memory.ImportMemoryFiles(ctx, c, home, s, o)}
	if sessions > 0 {
		store, err := openStore(home)
		if err != nil {
			return fatal(io.err, err)
		}
		defer store.Close()
		fmt.Fprintf(io.out, "%sbackfilling the last %d session(s)\n", prefix, sessions)
		r, err := memory.ImportSessions(ctx, c, store, home, s, sessions, o)
		if err != nil {
			return fatal(io.err, err)
		}
		reports = append(reports, r)
	}
	var planned, written, skipped, failed int
	for _, r := range reports {
		planned, written, skipped, failed = planned+r.Planned, written+r.Written, skipped+r.Skipped, failed+r.Failed
	}
	verb, n := "wrote", written
	if o.DryRun {
		verb, n = "would write", planned
	}
	fmt.Fprintf(io.out, "%s %d, already imported %d, failed %d\n", verb, n, skipped, failed)
	if failed > 0 {
		return 1
	}
	return 0
}
