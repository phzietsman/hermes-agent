package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"

	"github.com/phzietsman/hermes-agent/internal/agent"
	"github.com/phzietsman/hermes-agent/internal/app"
	"github.com/phzietsman/hermes-agent/internal/approval"
	"github.com/phzietsman/hermes-agent/internal/llm"
	"github.com/phzietsman/hermes-agent/internal/skills"
)

const chatHelp = `commands:
  /help            this list
  /new             start a new session (the old one stays searchable)
  /yolo            toggle: run dangerous commands without asking (hardline blocks still apply)
  /skills          list skill commands
  /<skill> [text]  load a skill's instructions into this turn
  /exit            leave
`

func chat(ctx context.Context, args []string, io stdio, opts app.Options) int {
	fs := newFlags("chat", io)
	query := fs.String("q", "", "ask one question, print the answer and exit")
	resume := fs.String("resume", "", "resume the session with this id")
	profile := fs.String("profile", "", "profile name (default: the ambient home)")
	yolo := fs.Bool("yolo", false, "run dangerous commands without asking (hardline blocks still apply)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	home, err := profileHome(ctx, *profile)
	if err != nil {
		return fatal(io.err, err)
	}
	rt, err := app.Open(ctx, home, opts)
	if err != nil {
		return fatal(io.err, err)
	}
	defer rt.Close()

	c := &cli{rt: rt, io: io, in: bufio.NewReader(io.in), yolo: *yolo}
	if err := c.start(ctx, *resume); err != nil {
		return fatal(io.err, err)
	}
	if *query != "" {
		if !c.turn(ctx, *query) {
			return 1
		}
		return 0
	}
	return c.repl(ctx)
}

type cli struct {
	rt       *app.Runtime
	io       stdio
	in       *bufio.Reader
	sess     *app.Session
	yolo     bool
	streamed bool
}

func (c *cli) start(ctx context.Context, id string) error {
	s, err := c.rt.NewSession(ctx, app.SessionOptions{ID: id, Source: "cli", Platform: "cli", Prompter: c,
		Events: agent.Events{
			Text:      func(t string) { c.streamed = true; fmt.Fprint(c.io.out, t) },
			ToolStart: func(call llm.ToolCall) { fmt.Fprintf(c.io.err, "  ⚙ %s\n", call.Function.Name) },
			Status:    func(m string) { fmt.Fprintf(c.io.err, "  · %s\n", m) },
		}})
	if err != nil {
		return err
	}
	c.sess = s
	if c.yolo {
		c.rt.Gate.SetSessionYolo(s.ID, true)
	}
	return nil
}

// Ask is the approval prompt: the turn is blocked on it, so reading the same input stream is safe.
func (c *cli) Ask(ctx context.Context, r approval.Request) (approval.Choice, error) {
	fmt.Fprintf(c.io.err, "\n⚠ Dangerous command (%s):\n    %s\n  [o]nce  [s]ession  [a]lways  [d]eny (default): ", r.Description, r.Command)
	line, err := readLine(ctx, c.in)
	if err != nil {
		return approval.Deny, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "o", "once", "y", "yes":
		return approval.Once, nil
	case "s", "session":
		return approval.Session, nil
	case "a", "always":
		return approval.Always, nil
	}
	return approval.Deny, nil
}

// readLine reads one line, giving up when ctx ends (the read itself cannot be interrupted, so it finishes in
// the background and its line is dropped).
func readLine(ctx context.Context, r *bufio.Reader) (string, error) {
	type res struct {
		s   string
		err error
	}
	ch := make(chan res, 1)
	go func() { s, err := r.ReadString('\n'); ch <- res{s, err} }()
	select {
	case x := <-ch:
		if x.err != nil && x.s == "" {
			return "", x.err
		}
		return strings.TrimRight(x.s, "\r\n"), nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// turn runs one user message and reports whether the model answered.
func (c *cli) turn(ctx context.Context, text string) bool {
	tctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	c.streamed = false
	res, err := c.sess.Agent.RunTurn(tctx, text)
	if err != nil {
		fmt.Fprintf(c.io.err, "hermes: %v\n", err)
		return false
	}
	switch {
	case !c.streamed && res.FinalResponse != "":
		fmt.Fprint(c.io.out, res.FinalResponse)
	}
	fmt.Fprintln(c.io.out)
	if res.Interrupted {
		fmt.Fprintln(c.io.err, "  · interrupted")
	}
	if !res.Completed && res.Error != "" {
		fmt.Fprintf(c.io.err, "  · %s\n", res.Error)
	}
	return res.Completed
}

func (c *cli) repl(ctx context.Context) int {
	fmt.Fprintf(c.io.err, "hermes — session %s (/help for commands)\n", c.sess.ID)
	for {
		fmt.Fprint(c.io.out, "› ")
		line, err := readLine(ctx, c.in)
		if err != nil {
			fmt.Fprintln(c.io.out)
			return 0
		}
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case strings.HasPrefix(line, "/"):
			if quit := c.slash(ctx, line); quit {
				return 0
			}
		default:
			c.turn(ctx, line)
		}
	}
}

func (c *cli) skillCommands() []skills.Command {
	st := skills.LoadSettings(c.rt.Home, c.rt.Config, "")
	cmds, _ := skills.Commands(skills.Scan(st.Roots, skills.HostFilter()), st.Disabled, func(slug string) bool { return coreCommands[slug] })
	return cmds
}

var coreCommands = map[string]bool{"help": true, "new": true, "yolo": true, "skills": true, "exit": true, "quit": true}

// slash handles a /command and reports whether to leave.
func (c *cli) slash(ctx context.Context, line string) bool {
	name, rest, _ := strings.Cut(strings.TrimPrefix(line, "/"), " ")
	rest = strings.TrimSpace(rest)
	switch name {
	case "exit", "quit":
		return true
	case "help":
		fmt.Fprint(c.io.err, chatHelp)
	case "new":
		if err := c.start(ctx, ""); err != nil {
			fmt.Fprintf(c.io.err, "hermes: %v\n", err)
			return false
		}
		fmt.Fprintf(c.io.err, "new session %s\n", c.sess.ID)
	case "yolo":
		c.yolo = !c.yolo
		c.rt.Gate.SetSessionYolo(c.sess.ID, c.yolo)
		fmt.Fprintf(c.io.err, "yolo %s\n", map[bool]string{true: "on", false: "off"}[c.yolo])
	case "skills":
		cmds := c.skillCommands()
		sort.Slice(cmds, func(i, j int) bool { return cmds[i].Key < cmds[j].Key })
		for _, k := range cmds {
			fmt.Fprintf(c.io.err, "  %-28s %s\n", k.Key, k.Description)
		}
	default:
		for _, k := range c.skillCommands() {
			if k.Key == "/"+name {
				c.invokeSkill(ctx, k, rest)
				return false
			}
		}
		fmt.Fprintf(c.io.err, "unknown command /%s (try /help)\n", name)
	}
	return false
}

// invokeSkill sends the skill's text as this turn's user message: the system prompt, and with it the cached
// prefix, stays untouched.
func (c *cli) invokeSkill(ctx context.Context, k skills.Command, args string) {
	body, err := os.ReadFile(k.Path)
	if err != nil {
		fmt.Fprintf(c.io.err, "hermes: cannot read skill %s: %v\n", k.Name, err)
		return
	}
	msg := fmt.Sprintf("[The user invoked the %q skill: follow its instructions. The full skill is below.]\n\n%s", k.Name, strings.TrimSpace(string(body)))
	if args != "" {
		msg += "\n\nThe user's instruction alongside the skill: " + args
	}
	c.turn(ctx, msg)
}
