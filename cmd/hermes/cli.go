package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/phzietsman/hermes-agent/internal/app"
	"github.com/phzietsman/hermes-agent/internal/config"
	"github.com/phzietsman/hermes-agent/internal/session"
)

type stdio struct {
	in       io.Reader
	out, err io.Writer
}

const usage = `usage: hermes <command> [flags]

commands:
  chat          talk to the agent (interactive, or one question with -q)
  cron          scheduled jobs: list, add, edit, pause, resume, remove, run, tick, daemon, status
  agent_memory  long-term memory: status, doctor, scopes-init, replay, import

run "hermes <command> -h" for a command's flags.
`

// run dispatches a command line and returns the process exit code.
func run(ctx context.Context, args []string, io stdio, opts app.Options) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(io.err, usage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	switch args[0] {
	case "chat":
		return chat(ctx, args[1:], io, opts)
	case "cron":
		return cronCmd(ctx, args[1:], io, opts)
	case "agent_memory":
		return agentMemory(ctx, args[1:], io, opts)
	}
	fmt.Fprintf(io.err, "hermes: unknown command %q\n\n%s", args[0], usage)
	return 2
}

// profileHome resolves --profile, else the ambient home (context override, HERMES_HOME, ~/.hermes).
func profileHome(ctx context.Context, profile string) (string, error) {
	if profile == "" || profile == "default" {
		return config.Home(ctx), nil
	}
	return config.ProfileHome(profile)
}

func newFlags(name string, io stdio) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.err)
	return fs
}

func fatal(w io.Writer, err error) int {
	fmt.Fprintf(w, "hermes: %v\n", err)
	return 1
}

func openStore(home string) (*session.Store, error) { return session.Open(home) }
