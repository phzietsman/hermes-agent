package approval

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestProtectedWriteAlwaysAsksAndNeverPersists(t *testing.T) {
	// Not bypassed by yolo, mode off or a permanent approval.
	g := newGate(Config{Mode: "off", Yolo: true})
	g.SetSessionYolo("s", true)
	g.ApprovePermanent("protected_instruction_file")
	if msg := g.ProtectedWrite(ctx, []string{"AGENTS.md"}, Check{SessionID: "s"}); !strings.Contains(msg, "BLOCKED") || !strings.Contains(msg, "no interactive user") {
		t.Errorf("nobody to ask must fail closed: %q", msg)
	}
	for _, choice := range []Choice{Once, Session, Always} {
		p := &scriptedPrompter{choice: choice}
		if msg := g.ProtectedWrite(ctx, []string{"AGENTS.md", "AGENTS.md", "CLAUDE.md"}, Check{SessionID: "s", Prompter: p}); msg != "" {
			t.Errorf("%s: %q", choice, msg)
		}
		if len(p.asked) != 1 || !strings.Contains(p.asked[0].Description, "AGENTS.md, CLAUDE.md") || strings.Count(p.asked[0].Description, "AGENTS.md") != 1 {
			t.Errorf("%s: asked %+v (duplicates must collapse)", choice, p.asked)
		}
	}
	// Every grant is one operation: the next write asks again.
	p := &scriptedPrompter{choice: Always}
	g.ProtectedWrite(ctx, []string{"SOUL.md"}, Check{SessionID: "s", Prompter: p})
	g.ProtectedWrite(ctx, []string{"SOUL.md"}, Check{SessionID: "s", Prompter: p})
	if len(p.asked) != 2 {
		t.Errorf("an 'always' answer persisted: asked %d times", len(p.asked))
	}
	if msg := g.ProtectedWrite(ctx, []string{"AGENTS.md"}, Check{Prompter: &scriptedPrompter{choice: Deny}}); !strings.Contains(msg, "denied by the user") {
		t.Errorf("%q", msg)
	}
	if msg := g.ProtectedWrite(ctx, []string{"AGENTS.md"}, Check{Prompter: &scriptedPrompter{err: errors.New("no channel")}}); !strings.Contains(msg, "could not be delivered") {
		t.Errorf("%q", msg)
	}
}

func TestProtectedWriteAndActionTimeOutAsSilence(t *testing.T) {
	g := newGate(Config{Timeout: 30 * time.Millisecond})
	if msg := g.ProtectedWrite(ctx, []string{"AGENTS.md"}, Check{Prompter: &scriptedPrompter{block: true}}); !strings.Contains(msg, "Silence is not consent") {
		t.Errorf("%q", msg)
	}
	if msg := g.ActionApproval(ctx, "k", "desc", "<write>", Check{Prompter: &scriptedPrompter{block: true}}); !strings.Contains(msg, "Silence is not consent") {
		t.Errorf("%q", msg)
	}
}

func TestActionApprovalHonoursYoloSessionAndPermanentGrants(t *testing.T) {
	const key = "ssh_config_write"
	ask := func(g *Gate, c Check) string { return g.ActionApproval(ctx, key, "d", "<write ~/.ssh/config>", c) }
	if msg := ask(newGate(Config{}), Check{}); !strings.Contains(msg, "BLOCKED") {
		t.Errorf("no human: %q", msg)
	}
	if ask(newGate(Config{Mode: "off"}), Check{}) != "" {
		t.Error("mode off")
	}
	y := newGate(Config{})
	y.SetSessionYolo("s", true)
	if ask(y, Check{SessionID: "s"}) != "" {
		t.Error("yolo")
	}
	g := newGate(Config{})
	p := &scriptedPrompter{choice: Session}
	first := ask(g, Check{SessionID: "s", Prompter: p})
	second := ask(g, Check{SessionID: "s", Prompter: p})
	if first != "" || second != "" || len(p.asked) != 1 {
		t.Errorf("a session grant must be remembered: asked %d", len(p.asked))
	}
	if msg := ask(g, Check{SessionID: "other", Prompter: &scriptedPrompter{choice: Deny}}); !strings.Contains(msg, "denied") {
		t.Errorf("%q", msg)
	}
	if msg := ask(g, Check{SessionID: "x", Prompter: &scriptedPrompter{err: errors.New("down")}}); !strings.Contains(msg, "could not be delivered") {
		t.Errorf("%q", msg)
	}
	g.ApprovePermanent(key)
	if ask(g, Check{SessionID: "fresh"}) != "" {
		t.Error("a permanent approval covers every session")
	}
}

func TestDetectorFromEnvReadsHomeAndSudo(t *testing.T) {
	t.Setenv("HOME", "/home/x")
	t.Setenv("HERMES_HOME", "")
	t.Setenv("SUDO_PASSWORD", "pw")
	d := DetectorFromEnv()
	if d.Home != "/home/x" || d.HermesHome != "/home/x/.hermes" || !d.SudoPasswordConfigured {
		t.Errorf("%+v", d)
	}
	t.Setenv("HERMES_HOME", "/elsewhere")
	if DetectorFromEnv().HermesHome != "/elsewhere" {
		t.Error("HERMES_HOME wins")
	}
}
