// Package shell implements the terminal tool for the local host: a foreground shell command with a timeout, a
// per-session working directory that follows cd, approval before anything dangerous runs, and bounded, redacted
// output. It ports the foreground path of tools/terminal_tool*.py. Not ported: background processes and PTYs,
// remote and container backends, sudo password handling, the persistent shell environment snapshot.
package shell

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/phzietsman/hermes-agent/internal/redact"
	"github.com/phzietsman/hermes-agent/internal/tools"
)

const (
	defaultTimeout    = 180
	foregroundMax     = 600
	maxOutputChars    = 50_000
	headRatio         = 0.4
	killGrace         = 2 * time.Second
	captureHeadBytes  = 256 * 1024
	captureTailBytes  = 1024 * 1024
	interruptedMarker = "\n[Command interrupted]"
)

const description = `Execute shell commands. Filesystem, current working directory, and exported environment variables set inside a command do not persist between calls, except the working directory: when a command changes it (cd, pushd) the new directory is used by the next call.

Do NOT use cat/head/tail (use read_file), sed/awk (use patch), or echo/heredoc file creation (use write_file). Use terminal for: searching (grep, rg, find, ls), builds, installs, git, processes, scripts, network, package managers — anything that needs a shell. Output is auto-truncated (head and tail kept) — never pipe through tail/head to shorten it.

Foreground only: the call returns INSTANTLY when the command finishes, even with a high timeout — set timeout generously for long builds and fixed waits.
Working directory: use 'workdir' for per-command cwd; when a command changes the session cwd (cd, pushd), trust the result's "cwd" field instead of prefixing every command with 'cd'.
`

var (
	cwdMu   sync.Mutex
	cwdByID = map[string]string{}
)

// SessionCWD is the working directory the next command of a session starts in.
func SessionCWD(session, fallback string) string {
	cwdMu.Lock()
	defer cwdMu.Unlock()
	if d, ok := cwdByID[session]; ok {
		return d
	}
	return fallback
}

func setSessionCWD(session, dir string) {
	cwdMu.Lock()
	defer cwdMu.Unlock()
	cwdByID[session] = dir
}

// ForgetSession drops a finished session's working directory.
func ForgetSession(session string) {
	cwdMu.Lock()
	defer cwdMu.Unlock()
	delete(cwdByID, session)
}

// Register adds the terminal tool to r.
func Register(r *tools.Registry) {
	r.MustRegister(tools.Tool{
		Name: "terminal", Toolset: "terminal", Description: description, MaxResultChars: 1_000_000, // self-limited
		Check: func() bool { _, err := shellPath(); return err == nil },
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string", "description": "The shell command to execute"},
				"timeout": map[string]any{"type": "integer", "minimum": 1,
					"description": fmt.Sprintf("Max seconds to wait (default: %d, max: %d). Returns INSTANTLY when command finishes — set high for long tasks, you won't wait unnecessarily.", defaultTimeout, foregroundMax)},
				"workdir": map[string]any{"type": "string", "description": "Working directory for this command (absolute path). Defaults to the session working directory."},
			},
			"required": []any{"command"},
		},
		Handler: handle,
	})
}

func shellPath() (string, error) {
	if p, err := exec.LookPath("bash"); err == nil {
		return p, nil
	}
	return exec.LookPath("sh")
}

func errorJSON(msg, status string, extra ...tools.Field) string {
	fields := []tools.Field{{Key: "output", Value: ""}, {Key: "exit_code", Value: -1}, {Key: "error", Value: msg}}
	if status != "" {
		fields = append(fields, tools.Field{Key: "status", Value: status})
	}
	return tools.Result(append(fields, extra...)...)
}

func handle(ctx context.Context, args tools.Args) (string, error) {
	env := tools.Env(ctx)
	command := args.String("command", "")
	if strings.TrimSpace(command) == "" {
		return errorJSON("command is required", ""), nil
	}
	timeout := args.Int("timeout", defaultTimeout)
	if timeout < 1 {
		timeout = defaultTimeout
	}
	var notes []tools.Field
	if timeout > foregroundMax {
		notes = append(notes, tools.Field{Key: "timeout_clamped", Value: fmt.Sprintf("requested %ds; the foreground maximum is %ds and background execution is not available, so the command ran with %ds", timeout, foregroundMax, foregroundMax)})
		timeout = foregroundMax
	}
	sessionCwd := SessionCWD(env.SessionID, env.CWD)
	if sessionCwd == "" {
		sessionCwd, _ = os.Getwd()
	}
	cwd := sessionCwd
	workdir := args.String("workdir", "")
	if workdir != "" {
		if msg := validateWorkdir(workdir); msg != "" {
			return errorJSON(msg, "blocked"), nil
		}
		cwd = workdir
	}
	if fi, err := os.Stat(cwd); err != nil || !fi.IsDir() {
		return errorJSON(fmt.Sprintf("Working directory does not exist: %s", cwd), "error"), nil
	}

	approvalNote := ""
	if env.Gate != nil {
		d := env.Gate.Command(ctx, command, env.Approval)
		if !d.Approved {
			msg := d.Message
			if msg == "" {
				msg = fmt.Sprintf("Command denied: %s. Use the approval prompt to allow it, or rephrase the command.", d.Description)
			}
			return errorJSON(msg, "blocked"), nil
		}
		if d.UserApproved {
			desc := d.Description
			if desc == "" {
				desc = "flagged as dangerous"
			}
			approvalNote = fmt.Sprintf("Command required approval (%s) and was approved by the user.", desc)
		}
	}

	res := run(ctx, command, cwd, time.Duration(timeout)*time.Second)
	if workdir == "" && res.cwdObserved {
		setSessionCWD(env.SessionID, res.cwd)
	}
	output := res.output
	switch {
	case res.interrupted:
		output += interruptedMarker
	case res.timedOut:
		output += fmt.Sprintf("\n[Command timed out after %ds]", timeout)
	}
	output = truncateHeadTail(output)
	output = stripANSI(output)
	if isEnvDump(command) {
		output = redact.Text(strings.TrimSpace(output))
	} else {
		output = redact.Tokens(strings.TrimSpace(output))
	}
	fields := []tools.Field{{Key: "output", Value: output}, {Key: "exit_code", Value: res.exitCode}, {Key: "error", Value: nil}}
	if res.cwdObserved && workdir == "" && realpath(res.cwd) != realpath(cwd) {
		fields = append(fields, tools.Field{Key: "cwd", Value: res.cwd})
	}
	if approvalNote != "" {
		fields = append(fields, tools.Field{Key: "approval", Value: approvalNote})
	}
	if meaning := interpretExit(command, res.exitCode); meaning != "" {
		fields = append(fields, tools.Field{Key: "exit_code_meaning", Value: meaning})
	}
	return tools.Result(append(fields, notes...)...), nil
}

// validateWorkdir refuses working directories that smuggle shell syntax into the command's cd.
var workdirOK = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./~ -]+$`)

func validateWorkdir(dir string) string {
	if !workdirOK.MatchString(dir) {
		return fmt.Sprintf("Blocked: workdir contains disallowed characters (%q). Use a plain absolute path.", dir)
	}
	return ""
}

func realpath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

type result struct {
	output      string
	exitCode    int
	cwd         string
	cwdObserved bool
	timedOut    bool
	interrupted bool
}

// capture keeps the head and the tail of unbounded output so a runaway command cannot exhaust memory, while the
// start (where errors surface) and the end (the most recent lines) both survive.
type capture struct {
	mu      sync.Mutex
	head    []byte
	tail    []byte
	total   int
	dropped int
}

func (c *capture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(p)
	c.total += n
	if len(c.head) < captureHeadBytes {
		take := min(len(p), captureHeadBytes-len(c.head))
		c.head = append(c.head, p[:take]...)
		p = p[take:]
	}
	if len(p) > 0 {
		c.tail = append(c.tail, p...)
		if over := len(c.tail) - captureTailBytes; over > 0 {
			c.tail = c.tail[over:]
			c.dropped += over
		}
	}
	return n, nil
}

func (c *capture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dropped == 0 {
		return strings.ToValidUTF8(string(c.head)+string(c.tail), "�")
	}
	return strings.ToValidUTF8(string(c.head), "�") + fmt.Sprintf("\n\n... [%d bytes omitted from the middle of a very long output] ...\n\n", c.dropped) + strings.ToValidUTF8(string(c.tail), "�")
}

func run(ctx context.Context, command, cwd string, timeout time.Duration) result {
	sh, err := shellPath()
	if err != nil {
		return result{output: "no shell found: " + err.Error(), exitCode: 127}
	}
	var tok [8]byte
	_, _ = rand.Read(tok[:])
	marker := "__HERMES_CWD_" + hex.EncodeToString(tok[:]) + "__"
	// The trap reports the final directory even when the command calls exit.
	script := "trap 'printf \"\\n" + marker + "%s\\n\" \"$PWD\"' EXIT\n" + command
	cmd := exec.Command(sh, "-c", script)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "PAGER=cat", "GIT_PAGER=cat", "TERM=dumb")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out := &capture{}
	cmd.Stdout, cmd.Stderr = out, out
	cmd.Stdin = nil
	if err := cmd.Start(); err != nil {
		return result{output: err.Error(), exitCode: 126}
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var res result
	var waitErr error
	select {
	case waitErr = <-done:
	case <-timer.C:
		res.timedOut = true
		waitErr = killGroup(cmd, done)
	case <-ctx.Done():
		res.interrupted = true
		waitErr = killGroup(cmd, done)
	}
	res.output, res.cwd, res.cwdObserved = splitMarker(out.String(), marker)
	var ee *exec.ExitError
	switch {
	case res.timedOut:
		res.exitCode = 124
	case res.interrupted:
		res.exitCode = 130
	case waitErr == nil:
		res.exitCode = 0
	case errors.As(waitErr, &ee):
		res.exitCode = ee.ExitCode()
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			res.exitCode = -int(ws.Signal())
		}
	default:
		res.exitCode = 1
	}
	return res
}

// killGroup stops the command's whole process group: SIGTERM, then SIGKILL if it does not leave.
func killGroup(cmd *exec.Cmd, done chan error) error {
	pgid := cmd.Process.Pid
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	select {
	case err := <-done:
		return err
	case <-time.After(killGrace):
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	return <-done
}

func splitMarker(output, marker string) (clean, cwd string, observed bool) {
	i := strings.LastIndex(output, "\n"+marker)
	if i < 0 {
		return output, "", false
	}
	rest := output[i+1+len(marker):]
	return output[:i], strings.TrimRight(strings.SplitN(rest, "\n", 2)[0], "\r"), true
}

func truncateHeadTail(s string) string {
	r := []rune(s)
	if len(r) <= maxOutputChars {
		return s
	}
	head := int(float64(maxOutputChars) * headRatio)
	tail := maxOutputChars - head
	omitted := len(r) - head - tail
	return string(r[:head]) + fmt.Sprintf("\n\n... [OUTPUT TRUNCATED - %s chars omitted out of %s total] ...\n\n", commas(omitted), commas(len(r))) + string(r[len(r)-tail:])
}

func commas(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

var ansiRe = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)|[PX^_][\s\S]*?\x1b\\|[ -/]+[0-~]|[0-~])`)

func stripANSI(s string) string {
	if !strings.Contains(s, "\x1b") {
		return s
	}
	return ansiRe.ReplaceAllString(s, "")
}

var envDumpRe = regexp.MustCompile(`(?:^|[;&|]\s*)(?:env|printenv|set|export|declare)(?:\s|$)`)

func isEnvDump(command string) bool { return envDumpRe.MatchString(strings.TrimSpace(command)) }

var exitSemantics = func() map[string]map[int]string {
	m := map[string]map[int]string{}
	for _, c := range []string{"grep", "egrep", "fgrep", "rg", "ag", "ack"} {
		m[c] = map[int]string{1: "No matches found (not an error)"}
	}
	for _, c := range []string{"diff", "colordiff"} {
		m[c] = map[int]string{1: "Files differ (expected, not an error)"}
	}
	for _, c := range []string{"test", "["} {
		m[c] = map[int]string{1: "Condition evaluated to false (expected, not an error)"}
	}
	m["find"] = map[int]string{1: "Some directories were inaccessible (partial results may still be valid)"}
	m["curl"] = map[int]string{6: "Could not resolve host", 7: "Failed to connect to host", 22: "HTTP response code indicated error (e.g. 404, 500)", 28: "Operation timed out"}
	m["git"] = map[int]string{1: "Non-zero exit (often normal — e.g. 'git diff' returns 1 when files differ)"}
	return m
}()

var signalNotes = map[int]string{
	3: "SIGQUIT (quit from keyboard)", 4: "SIGILL (illegal instruction — corrupt binary or wrong architecture)",
	6: "SIGABRT (abort — assertion failure, fatal runtime error, or glibc abort)", 7: "SIGBUS (bus error — misaligned or unmapped memory access)",
	8: "SIGFPE (fatal arithmetic error, e.g. integer division by zero)", 9: "SIGKILL — often the kernel OOM killer on memory exhaustion, or an explicit kill -9",
	11: "SIGSEGV (segmentation fault — the program crashed)", 13: "SIGPIPE (wrote to a closed pipe — e.g. output piped to a reader that exited)",
	15: "SIGTERM (terminated — kill/timeout or shutdown requested it to stop)", 24: "SIGXCPU (CPU time limit exceeded)", 25: "SIGXFSZ (file size limit exceeded)",
}

var splitCmdRe = regexp.MustCompile(`\s*(?:\|\||&&|[|;])\s*`)

// interpretExit explains a non-zero exit that is informational (grep 1, diff 1, a signal death) so the model does
// not spend turns investigating it.
func interpretExit(command string, code int) string {
	if code == 0 {
		return ""
	}
	if code < 0 {
		sig := -code
		if sig == 2 {
			return ""
		}
		if n, ok := signalNotes[sig]; ok {
			return fmt.Sprintf("Command terminated by signal %d: %s", sig, n)
		}
		return fmt.Sprintf("Command terminated by signal %d", sig)
	}
	if code > 128 {
		if n, ok := signalNotes[code-128]; ok {
			return fmt.Sprintf("Exit code %d usually means the command was terminated by signal %d: %s", code, code-128, n)
		}
	}
	segs := splitCmdRe.Split(command, -1)
	last := strings.TrimSpace(segs[len(segs)-1])
	base := ""
	for _, w := range strings.Fields(last) {
		if !strings.Contains(w, "=") || strings.HasPrefix(w, "-") {
			base = w[strings.LastIndex(w, "/")+1:]
			break
		}
	}
	return exitSemantics[base][code]
}
