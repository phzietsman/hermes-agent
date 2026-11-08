package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/phzietsman/hermes-agent/internal/app"
	"github.com/phzietsman/hermes-agent/internal/config"
	"github.com/phzietsman/hermes-agent/internal/gateway"
)

// buildAdapters creates the adapters of every enabled platform. A variable so tests can supply a fake platform.
var buildAdapters = gateway.Build

const gatewayUsage = "usage: hermes gateway run [-profile name]\n\nserves the platforms enabled under gateway.platforms in config.yaml, and runs the cron ticker\n"

// gatewayCmd runs the messaging gateway until interrupted.
func gatewayCmd(ctx context.Context, args []string, io stdio, opts app.Options) int {
	if len(args) == 0 || args[0] != "run" {
		fmt.Fprint(io.err, gatewayUsage)
		return 2
	}
	fs := newFlags("gateway run", io)
	profile := fs.String("profile", "", "profile name (default: the ambient home)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	home, err := profileHome(ctx, *profile)
	if err != nil {
		return fatal(io.err, err)
	}
	opts.LongRunning = true
	rt, err := app.Open(config.WithHome(ctx, home), home, opts)
	if err != nil {
		return fatal(io.err, err)
	}
	defer rt.Close()
	adapters, err := buildAdapters(rt.Config, rt.Secrets)
	if err != nil {
		return fatal(io.err, err)
	}
	gw := gateway.New(gateway.Options{
		RT: rt, Adapters: adapters, Pairing: gateway.NewPairing(home), RunCron: true,
		IdleEnd:      time.Duration(rt.Config.Int("gateway.idle_minutes", 60)) * time.Minute,
		MaxQueue:     rt.Config.Int("gateway.max_queue", 20),
		ToolProgress: rt.Config.Bool("gateway.tool_progress", false),
	})
	names := make([]string, len(adapters))
	for i, a := range adapters {
		names[i] = a.Name()
	}
	fmt.Fprintf(io.err, "hermes gateway: serving %s (Ctrl-C to stop)\n", strings.Join(names, ", "))
	if err := gw.Run(ctx); err != nil {
		return fatal(io.err, err)
	}
	return 0
}

const pairingUsage = `usage: hermes pairing <command>

commands:
  list [platform]              show approved people and waiting requests
  approve <platform> <code>    let the person behind a pairing code talk to the bot
  revoke <platform> <user-id>  remove an approved person
`

// pairingCmd manages who may talk to the bot.
func pairingCmd(ctx context.Context, args []string, io stdio) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "help" {
		fmt.Fprint(io.err, pairingUsage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	fs := newFlags("pairing "+args[0], io)
	profile := fs.String("profile", "", "profile name (default: the ambient home)")
	if err := fs.Parse(reorder(args[1:])); err != nil {
		return 2
	}
	pos := fs.Args()
	home, err := profileHome(ctx, *profile)
	if err != nil {
		return fatal(io.err, err)
	}
	p := gateway.NewPairing(home)
	switch args[0] {
	case "approve":
		if len(pos) != 2 {
			fmt.Fprintln(io.err, "usage: hermes pairing approve <platform> <code>")
			return 2
		}
		uid, err := p.Approve(pos[0], pos[1])
		if err != nil {
			return fatal(io.err, err)
		}
		fmt.Fprintf(io.out, "approved %s on %s\n", uid, pos[0])
		return 0
	case "revoke":
		if len(pos) != 2 {
			fmt.Fprintln(io.err, "usage: hermes pairing revoke <platform> <user-id>")
			return 2
		}
		if err := p.Revoke(pos[0], pos[1]); err != nil {
			return fatal(io.err, err)
		}
		fmt.Fprintf(io.out, "revoked %s on %s\n", pos[1], pos[0])
		return 0
	case "list":
		platforms := pos
		if len(platforms) == 0 {
			platforms = pairedPlatforms(home)
		}
		if len(platforms) == 0 {
			fmt.Fprintln(io.out, "no pairing records")
			return 0
		}
		for _, pl := range platforms {
			approved, pending, err := p.List(pl)
			if err != nil {
				return fatal(io.err, err)
			}
			fmt.Fprintf(io.out, "%s:\n", pl)
			ids := make([]string, 0, len(approved))
			for id := range approved {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			for _, id := range ids {
				fmt.Fprintf(io.out, "  approved  %s %s\n", id, approved[id])
			}
			for _, w := range pending {
				fmt.Fprintf(io.out, "  waiting   %s  %s %s\n", w.Code, w.UserID, w.Name)
			}
			if len(approved) == 0 && len(pending) == 0 {
				fmt.Fprintln(io.out, "  (none)")
			}
		}
		return 0
	}
	fmt.Fprintf(io.err, "hermes pairing: unknown command %q\n\n%s", args[0], pairingUsage)
	return 2
}

func pairedPlatforms(home string) []string {
	entries, err := os.ReadDir(gateway.NewPairing(home).Dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if n, ok := strings.CutSuffix(e.Name(), ".json"); ok {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}
