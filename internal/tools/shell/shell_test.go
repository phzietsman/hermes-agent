package shell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/phzietsman/hermes-agent/internal/approval"
	"github.com/phzietsman/hermes-agent/internal/llm"
	"github.com/phzietsman/hermes-agent/internal/tools"
)

type fixture struct {
	t     *testing.T
	dir   string
	tb    *tools.Toolbox
	gate  *approval.Gate
	check approval.Check
	sess  string
}

type prompter struct {
	choice approval.Choice
	err    error
	asked  *int
}

func (p prompter) Ask(context.Context, approval.Request) (approval.Choice, error) {
	*p.asked++
	return p.choice, p.err
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	reg := tools.NewRegistry()
	Register(reg)
	fx := &fixture{t: t, dir: dir, tb: reg.Build(tools.Selection{}), sess: t.Name(),
		gate: approval.NewGate(approval.Detector{Home: "/home/tester", HermesHome: "/home/tester/.hermes"}, approval.Config{})}
	t.Cleanup(func() { ForgetSession(fx.sess) })
	return fx
}

func (fx *fixture) run(args map[string]any) map[string]any {
	return fx.runCtx(context.Background(), args)
}

func (fx *fixture) runCtx(ctx context.Context, args map[string]any) map[string]any {
	fx.t.Helper()
	b, _ := json.Marshal(args)
	ctx = tools.WithEnv(ctx, tools.CallEnv{SessionID: fx.sess, CWD: fx.dir, Gate: fx.gate, Approval: fx.check})
	out, _ := fx.tb.Run(ctx, llm.ToolCall{Function: llm.ToolCallFunction{Name: "terminal", Arguments: string(b)}})
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		fx.t.Fatalf("%q", out)
	}
	return m
}

func TestRunsAndReportsOutputAndExitCode(t *testing.T) {
	fx := newFixture(t)
	m := fx.run(map[string]any{"command": "echo out; echo err >&2; exit 3"})
	if m["output"] != "out\nerr" || m["exit_code"] != float64(3) || m["error"] != nil {
		t.Fatalf("%v", m)
	}
	m = fx.run(map[string]any{"command": "printf 'a\\nb\\n'"})
	if m["output"] != "a\nb" || m["exit_code"] != float64(0) {
		t.Fatalf("%v", m)
	}
}

func TestWorkingDirectoryFollowsCd(t *testing.T) {
	fx := newFixture(t)
	if err := os.MkdirAll(filepath.Join(fx.dir, "sub", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := fx.run(map[string]any{"command": "cd sub/deep && pwd"})
	if m["cwd"] != filepath.Join(fx.dir, "sub", "deep") || m["output"] != filepath.Join(fx.dir, "sub", "deep") {
		t.Fatalf("%v", m)
	}
	// The next call starts where the last ended, and says nothing new while it stays there.
	m = fx.run(map[string]any{"command": "pwd"})
	if m["output"] != filepath.Join(fx.dir, "sub", "deep") || m["cwd"] != nil {
		t.Fatalf("%v", m)
	}
	// workdir is per command and never moves the session.
	m = fx.run(map[string]any{"command": "pwd", "workdir": fx.dir})
	if m["output"] != fx.dir {
		t.Fatalf("%v", m)
	}
	if m := fx.run(map[string]any{"command": "pwd"}); m["output"] != filepath.Join(fx.dir, "sub", "deep") {
		t.Fatalf("workdir moved the session: %v", m)
	}
	// A command that exits explicitly still reports where it ended.
	fx.run(map[string]any{"command": "cd .. ; exit 2"})
	if m := fx.run(map[string]any{"command": "pwd"}); m["output"] != filepath.Join(fx.dir, "sub") {
		t.Fatalf("%v", m)
	}
	// Sessions do not share a directory.
	other := newFixture(t)
	other.dir = fx.dir
	other.sess = "another-session"
	defer ForgetSession(other.sess)
	if m := other.run(map[string]any{"command": "pwd"}); m["output"] != fx.dir {
		t.Fatalf("%v", m)
	}
}

func TestWorkdirValidation(t *testing.T) {
	fx := newFixture(t)
	m := fx.run(map[string]any{"command": "pwd", "workdir": "/tmp; rm -rf x"})
	if m["status"] != "blocked" || !strings.Contains(m["error"].(string), "disallowed characters") {
		t.Fatalf("%v", m)
	}
	m = fx.run(map[string]any{"command": "pwd", "workdir": "/definitely/not/here"})
	if m["status"] != "error" || !strings.Contains(m["error"].(string), "does not exist") {
		t.Fatalf("%v", m)
	}
}

func TestTimeoutKillsTheWholeProcessGroup(t *testing.T) {
	fx := newFixture(t)
	pidFile := filepath.Join(fx.dir, "child.pid")
	st := time.Now()
	// A background child that would outlive the command by a minute.
	m := fx.run(map[string]any{"command": "(sleep 60 & echo $! > child.pid; wait) & while [ ! -s child.pid ]; do sleep 0.05; done; echo started; sleep 30", "timeout": 2})
	if time.Since(st) > 10*time.Second {
		t.Fatalf("took %v", time.Since(st))
	}
	if m["exit_code"] != float64(124) || !strings.Contains(m["output"].(string), "[Command timed out after 2s]") || !strings.Contains(m["output"].(string), "started") {
		t.Fatalf("%v", m)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	if _, err := fmt.Sscan(strings.TrimSpace(string(raw)), &pid); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ { // the kill is asynchronous for the grandchild
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatal("a background child survived the timeout")
}

func TestInterruptStopsTheCommand(t *testing.T) {
	fx := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	st := time.Now()
	m := fx.runCtx(ctx, map[string]any{"command": "echo before; sleep 30"})
	if time.Since(st) > 6*time.Second || m["exit_code"] != float64(130) || !strings.Contains(m["output"].(string), "[Command interrupted]") {
		t.Fatalf("%v (%v)", m, time.Since(st))
	}
}

func TestOutputIsBoundedHeadAndTail(t *testing.T) {
	fx := newFixture(t)
	m := fx.run(map[string]any{"command": "echo HEADLINE; seq 1 40000; echo TAILLINE"})
	out := m["output"].(string)
	if !strings.HasPrefix(out, "HEADLINE") || !strings.HasSuffix(out, "TAILLINE") || !strings.Contains(out, "OUTPUT TRUNCATED") || len([]rune(out)) > 51_000 {
		t.Fatalf("%d chars: %.80q ... %.60q", len([]rune(out)), out, out[len(out)-60:])
	}
}

func TestRunawayOutputIsCappedInMemory(t *testing.T) {
	fx := newFixture(t)
	m := fx.run(map[string]any{"command": "head -c 20000000 /dev/zero | tr '\\0' 'x'; echo END", "timeout": 60})
	out := m["output"].(string)
	if !strings.HasSuffix(out, "END") || len(out) > 60_000 {
		t.Fatalf("%d bytes", len(out))
	}
}

func TestOutputIsCleanedAndRedacted(t *testing.T) {
	fx := newFixture(t)
	m := fx.run(map[string]any{"command": `printf '\033[31mred\033[0m\n'; echo ghp_abcdefghij1234567890; echo "token = compute(x)"`})
	if got := m["output"].(string); got != "red\n"+"[REDACTED]\ntoken = compute(x)" {
		t.Fatalf("%q", got)
	}
	m = fx.run(map[string]any{"command": "export MY_SECRET=supersecretvalue99; env | grep MY_SECRET"})
	if got := m["output"].(string); strings.Contains(got, "supersecretvalue99") {
		t.Fatalf("an env dump leaked: %q", got)
	}
}

func TestExitCodeMeaning(t *testing.T) {
	fx := newFixture(t)
	if m := fx.run(map[string]any{"command": "echo hi | grep nothing"}); m["exit_code"] != float64(1) || m["exit_code_meaning"] != "No matches found (not an error)" {
		t.Fatalf("%v", m)
	}
	m := fx.run(map[string]any{"command": "kill -SEGV $$"})
	if m["exit_code"].(float64) >= 0 && !strings.Contains(m["exit_code_meaning"].(string), "SIGSEGV") {
		t.Fatalf("%v", m)
	}
	if m := fx.run(map[string]any{"command": "false"}); m["exit_code_meaning"] != nil {
		t.Fatalf("%v", m)
	}
}

func TestHardlineAndApprovalGateTheCommand(t *testing.T) {
	fx := newFixture(t)
	marker := filepath.Join(fx.dir, "ran")
	m := fx.run(map[string]any{"command": "rm -rf / ; touch " + marker})
	if m["status"] != "blocked" || m["exit_code"] != float64(-1) || !strings.Contains(m["error"].(string), "BLOCKED (hardline)") {
		t.Fatalf("%v", m)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a hardline-blocked command ran")
	}
	// Dangerous but not forbidden: nobody to ask means blocked, a human saying no means blocked, yes runs it.
	cmd := "git reset --hard HEAD~1 2>/dev/null; touch " + marker
	if m := fx.run(map[string]any{"command": cmd}); m["status"] != "blocked" {
		t.Fatalf("unattended: %v", m)
	}
	asked := 0
	fx.check = approval.Check{SessionID: fx.sess, Prompter: prompter{choice: approval.Deny, asked: &asked}}
	if m := fx.run(map[string]any{"command": cmd}); m["status"] != "blocked" || asked != 1 {
		t.Fatalf("denied: %v", m)
	}
	fx.check = approval.Check{SessionID: fx.sess, Prompter: prompter{choice: approval.Once, asked: &asked}}
	m = fx.run(map[string]any{"command": cmd})
	if m["exit_code"] != float64(0) || !strings.Contains(m["approval"].(string), "was approved by the user") {
		t.Fatalf("approved: %v", m)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("the approved command did not run")
	}
	// A prompt that errors is not consent.
	fx.check = approval.Check{SessionID: fx.sess, Prompter: prompter{err: errors.New("no tty"), asked: &asked}}
	if m := fx.run(map[string]any{"command": "git clean -fdx"}); m["status"] != "blocked" || !strings.Contains(m["error"].(string), "no tty") {
		t.Fatalf("%v", m)
	}
	// Ordinary commands never ask.
	asked = 0
	fx.run(map[string]any{"command": "ls"})
	if asked != 0 {
		t.Fatal("asked about ls")
	}
}

func TestTimeoutIsClampedAndExplained(t *testing.T) {
	fx := newFixture(t)
	m := fx.run(map[string]any{"command": "true", "timeout": 100000})
	if !strings.Contains(m["timeout_clamped"].(string), "foreground maximum is 600s") {
		t.Fatalf("%v", m)
	}
}

func TestEmptyCommandIsRefused(t *testing.T) {
	fx := newFixture(t)
	if m := fx.run(map[string]any{"command": "  "}); m["error"] != "command is required" || m["exit_code"] != float64(-1) {
		t.Fatalf("%v", m)
	}
}

func TestStdinIsNotInherited(t *testing.T) {
	fx := newFixture(t)
	st := time.Now()
	m := fx.run(map[string]any{"command": "cat; echo done", "timeout": 20})
	if time.Since(st) > 10*time.Second || !strings.Contains(m["output"].(string), "done") {
		t.Fatalf("a command waiting on stdin hung: %v", m)
	}
}
