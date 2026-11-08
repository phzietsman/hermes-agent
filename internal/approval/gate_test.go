package approval

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestFnmatchMatchesPython(t *testing.T) {
	b, err := os.ReadFile("testdata/fnmatch.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, Pattern string
		Match         bool
	}
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if got := fnmatch(c.Name, c.Pattern); got != c.Match {
			t.Errorf("fnmatch(%q, %q) = %v, want %v", c.Name, c.Pattern, got, c.Match)
		}
	}
}

func TestAllowlistShellOperatorMatchesPython(t *testing.T) {
	b, err := os.ReadFile("testdata/corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []corpusCase
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if got := hasAllowlistShellOperator(c.Cmd); got != c.Compound {
			t.Errorf("%q: compound = %v, want %v", clip(c.Cmd), got, c.Compound)
		}
	}
}

type scriptedPrompter struct {
	choice Choice
	err    error
	asked  []Request
	block  bool
}

func (p *scriptedPrompter) Ask(ctx context.Context, r Request) (Choice, error) {
	p.asked = append(p.asked, r)
	if p.block {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return p.choice, p.err
}

func newGate(cfg Config) *Gate { return NewGate(testDetector(), cfg) }

var ctx = context.Background()

func TestHardlineIsNeverBypassed(t *testing.T) {
	for _, cfg := range []Config{{}, {Mode: "off"}, {Yolo: true}, {UnattendedMode: "approve"}} {
		g := newGate(cfg)
		g.SetSessionYolo("s", true)
		for _, c := range []Check{{SessionID: "s"}, {SessionID: "s", Prompter: &scriptedPrompter{choice: Once}}} {
			d := g.Command(ctx, "rm -rf /", c)
			if d.Approved || !d.Hardline || !strings.HasPrefix(d.Message, "BLOCKED (hardline): recursive delete of root filesystem.") {
				t.Fatalf("cfg %+v: %+v", cfg, d)
			}
		}
	}
	// ...but a container backend skips the host floors, as Python does.
	if d := newGate(Config{}).Command(ctx, "rm -rf /", Check{ContainerIsolated: true}); !d.Approved {
		t.Fatalf("%+v", d)
	}
}

func TestSudoStdinGuard(t *testing.T) {
	d := newGate(Config{}).Command(ctx, "echo pw | sudo -S ls", Check{})
	if d.Approved || !strings.Contains(d.Message, "sudo -S") {
		t.Fatalf("%+v", d)
	}
	g := NewGate(Detector{Home: "/home/tester", HermesHome: "/home/tester/.hermes", SudoPasswordConfigured: true}, Config{Mode: "off"})
	if d := g.Command(ctx, "echo pw | sudo -S ls", Check{}); !d.Approved {
		t.Fatalf("with SUDO_PASSWORD configured the guard stands down: %+v", d)
	}
}

func TestUserDenyRulesBeatYoloAndContainers(t *testing.T) {
	g := newGate(Config{Mode: "off", Yolo: true, Deny: []string{"git push*", "  ", "TERRAFORM *"}})
	for _, cmd := range []string{"git push origin main", "g\\it push --force", "terraform apply"} {
		for _, c := range []Check{{}, {ContainerIsolated: true}} {
			d := g.Command(ctx, cmd, c)
			if d.Approved || !d.UserDeny || !strings.Contains(d.Message, "user-defined deny rule") {
				t.Errorf("%q %+v: %+v", cmd, c, d)
			}
		}
	}
	if d := g.Command(ctx, "git status", Check{}); !d.Approved {
		t.Fatalf("%+v", d)
	}
}

func TestPromptChoicesAndPersistence(t *testing.T) {
	var always []string
	cmd := "git reset --hard"
	for _, c := range []struct {
		choice   Choice
		approved bool
		session  bool
		perm     bool
	}{{Once, true, false, false}, {Session, true, true, false}, {Always, true, true, true}, {Deny, false, false, false}} {
		g2 := newGate(Config{OnAlways: func(k []string) { always = append(always, k...) }})
		p := &scriptedPrompter{choice: c.choice}
		d := g2.Command(ctx, cmd, Check{SessionID: "s", Prompter: p})
		if d.Approved != c.approved || len(p.asked) != 1 {
			t.Fatalf("%s: %+v asked=%d", c.choice, d, len(p.asked))
		}
		v := g2.Detector.DetectDangerous(cmd)
		if g2.sessionKeys["s"][v.Key] != c.session || g2.permanent[v.Key] != c.perm {
			t.Fatalf("%s: session=%v permanent=%v", c.choice, g2.sessionKeys["s"][v.Key], g2.permanent[v.Key])
		}
		if !c.approved && (d.Outcome != "denied" || !strings.Contains(d.Message, "The user has NOT consented")) {
			t.Fatalf("%+v", d)
		}
		if c.approved && !d.UserApproved {
			t.Fatalf("%+v", d)
		}
	}
	if len(always) != 1 {
		t.Fatalf("OnAlways called %d times", len(always))
	}
	// A session approval answers the same pattern without asking again, in that session only.
	p := &scriptedPrompter{choice: Session}
	g := newGate(Config{})
	g.Command(ctx, cmd, Check{SessionID: "a", Prompter: p})
	g.Command(ctx, "git reset --hard HEAD~3", Check{SessionID: "a", Prompter: p})
	g.Command(ctx, cmd, Check{SessionID: "b", Prompter: p})
	if len(p.asked) != 2 {
		t.Fatalf("asked %d times", len(p.asked))
	}
	g.ClearSession("a")
	g.Command(ctx, cmd, Check{SessionID: "a", Prompter: p})
	if len(p.asked) != 3 {
		t.Fatalf("a cleared session should ask again (%d)", len(p.asked))
	}
}

func TestSafeCommandsDoNotPrompt(t *testing.T) {
	p := &scriptedPrompter{choice: Deny}
	g := newGate(Config{})
	for _, cmd := range []string{"ls -la", "git status", "echo hello", "go test ./..."} {
		if d := g.Command(ctx, cmd, Check{Prompter: p}); !d.Approved {
			t.Errorf("%q: %+v", cmd, d)
		}
	}
	if len(p.asked) != 0 {
		t.Fatalf("asked about %v", p.asked)
	}
}

func TestPromptFailuresFailClosed(t *testing.T) {
	g := newGate(Config{Timeout: 30 * time.Millisecond})
	d := g.Command(ctx, "git reset --hard", Check{Prompter: &scriptedPrompter{block: true}})
	if d.Approved || d.Outcome != "timeout" || !strings.Contains(d.Message, "Silence is not consent.") {
		t.Fatalf("%+v", d)
	}
	d = g.Command(ctx, "git reset --hard", Check{Prompter: &scriptedPrompter{err: errors.New("no terminal")}})
	if d.Approved || d.Outcome != "cancelled" || !strings.Contains(d.Message, "no terminal") {
		t.Fatalf("%+v", d)
	}
	// An interrupted turn is not a timeout and not a refusal by the user.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	d = newGate(Config{Timeout: time.Minute}).Command(cctx, "git reset --hard", Check{Prompter: &scriptedPrompter{block: true}})
	if d.Approved || d.Outcome != "cancelled" {
		t.Fatalf("%+v", d)
	}
}

func TestYoloAndModeOffSkipThePromptButNotTheFloor(t *testing.T) {
	for _, g := range []*Gate{newGate(Config{Mode: "off"}), newGate(Config{Yolo: true})} {
		p := &scriptedPrompter{choice: Deny}
		if d := g.Command(ctx, "git reset --hard", Check{Prompter: p}); !d.Approved || len(p.asked) != 0 {
			t.Fatalf("%+v", d)
		}
	}
	g := newGate(Config{})
	g.SetSessionYolo("s", true)
	if d := g.Command(ctx, "git reset --hard", Check{SessionID: "s", Prompter: &scriptedPrompter{choice: Deny}}); !d.Approved {
		t.Fatal("session yolo")
	}
	g.SetSessionYolo("s", false)
	if d := g.Command(ctx, "git reset --hard", Check{SessionID: "s", Prompter: &scriptedPrompter{choice: Deny}}); d.Approved {
		t.Fatal("session yolo not cleared")
	}
}

func TestUnattendedPolicy(t *testing.T) {
	deny := newGate(Config{}).Command(ctx, "git reset --hard", Check{})
	if deny.Approved || deny.Outcome != "blocked" || !strings.Contains(deny.Message, "without a user present") || deny.PatternKey == "" {
		t.Fatalf("%+v", deny)
	}
	if d := newGate(Config{UnattendedMode: "approve"}).Command(ctx, "git reset --hard", Check{}); !d.Approved {
		t.Fatalf("%+v", d)
	}
	// A permanently approved pattern passes even in deny mode; a safe command always does.
	g := newGate(Config{Allowlist: []string{deny.PatternKey}})
	if d := g.Command(ctx, "git reset --hard", Check{}); !d.Approved {
		t.Fatalf("%+v", d)
	}
	if d := newGate(Config{}).Command(ctx, "ls", Check{}); !d.Approved {
		t.Fatalf("%+v", d)
	}
}

func TestAllowlistCommandTextAndGlobs(t *testing.T) {
	g := newGate(Config{Allowlist: []string{"git reset *", "git clean -fd"}})
	for _, c := range []struct {
		cmd  string
		want bool
	}{
		{"git reset --hard HEAD~1", true}, {"git clean -fd", true},
		{"git reset --hard; echo done", false}, {"git reset --hard $(whoami)", false}, {"git clean -fd && git reset --hard", false},
		{"git clean -fdx", false},
	} {
		p := &scriptedPrompter{choice: Deny}
		d := g.Command(ctx, c.cmd, Check{Prompter: p})
		if d.Approved != c.want || (c.want && len(p.asked) != 0) || (!c.want && len(p.asked) != 1) {
			t.Errorf("%q: approved=%v asked=%d, want approved=%v (%+v)", c.cmd, d.Approved, len(p.asked), c.want, d)
		}
	}
}

func TestLegacyKeysStillApprove(t *testing.T) {
	// An allowlist written by the Python version keys some patterns by their regex fragment.
	g := newGate(Config{})
	v := g.Detector.DetectDangerous("git branch -D old")
	if !v.Dangerous {
		t.Fatalf("%+v", v)
	}
	g.ApprovePermanent(`git\s+branch\s+-D`)
	if !g.IsApproved("s", v.Key) {
		t.Fatal("the pre-change spelling of the git branch key no longer approves")
	}
	if g.IsApproved("s", "recursive delete") {
		t.Fatal("an unrelated key was approved")
	}
}

func TestOversizedPayloadIsSavedForRecovery(t *testing.T) {
	home := t.TempDir()
	g := newGate(Config{Home: home})
	d := g.Command(ctx, strings.Repeat("a", 5000), Check{})
	if d.Approved || !d.Hardline || !strings.Contains(d.Message, "saved to "+filepath.Join(home, "cache", "blocked-scripts")) {
		t.Fatalf("%+v", d)
	}
	files, _ := filepath.Glob(filepath.Join(home, "cache", "blocked-scripts", "blocked-*.sh"))
	if len(files) != 1 {
		t.Fatalf("saved %v", files)
	}
	body, _ := os.ReadFile(files[0])
	if !strings.HasPrefix(string(body), "#!/usr/bin/env bash") || !strings.Contains(string(body), strings.Repeat("a", 5000)) {
		t.Fatal("saved script content")
	}
	// Without a home there is nowhere to save, and the message says how to recover instead.
	d = newGate(Config{}).Command(ctx, strings.Repeat("a", 5000), Check{})
	if !strings.Contains(d.Message, "Write the script to a file with write_file") {
		t.Fatalf("%q", d.Message)
	}
}

func TestConcurrentSessionState(t *testing.T) {
	g := newGate(Config{})
	var n atomic.Int32
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 50; j++ {
				g.ApproveSession("s", "k")
				g.SetSessionYolo("s", j%2 == 0)
				if g.IsApproved("s", "k") {
					n.Add(1)
				}
				g.Command(ctx, "git status", Check{SessionID: "s"})
			}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	if n.Load() != 400 {
		t.Fatalf("approvals seen %d", n.Load())
	}
}

func FuzzDetect(f *testing.F) {
	for _, s := range []string{"ls", "rm -rf /", "echo 'unterminated", "bash -c \"a;b\"", "$(", "`", "a\\", "env -S 'x y'", "ｒｍ -rf /"} {
		f.Add(s)
	}
	d := testDetector()
	f.Fuzz(func(t *testing.T, cmd string) {
		// Detection must terminate and never panic, whatever the bytes.
		d.DetectHardline(cmd)
		d.DetectDangerous(cmd)
		d.DenyVariants(cmd)
	})
}
