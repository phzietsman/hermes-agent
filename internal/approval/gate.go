package approval

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Choice is the user's answer to an approval prompt.
type Choice string

const (
	// Once approves this one command.
	Once Choice = "once"
	// Session approves the pattern for the rest of the session.
	Session Choice = "session"
	// Always approves the pattern permanently (it is written to the allowlist).
	Always Choice = "always"
	// Deny refuses it.
	Deny Choice = "deny"
)

// Request is what the user is asked to approve.
type Request struct {
	Command     string
	Description string
	PatternKey  string
}

// Prompter asks a human. A returned error (the prompt could not be delivered, the context ended) fails closed:
// silence is never consent.
type Prompter interface {
	Ask(ctx context.Context, r Request) (Choice, error)
}

// Config is the approval policy, read from config.yaml by the caller.
type Config struct {
	// Mode is "manual" (the default: dangerous commands are asked about) or "off" (they are not; the hardline floor
	// and deny rules still apply).
	Mode string
	// Deny are fnmatch globs for commands that never run, whatever the mode.
	Deny []string
	// Allowlist is the permanent allowlist: pattern keys approved with "always", or command text and globs.
	Allowlist []string
	// UnattendedMode is what happens to a dangerous command when nobody can answer: "deny" (the default) or "approve".
	UnattendedMode string
	// Timeout bounds one prompt. Zero means no limit beyond the context.
	Timeout time.Duration
	// Yolo bypasses the dangerous-command prompt for the whole process. The floors still apply.
	Yolo bool
	// Home is the profile home; blocked oversized payloads are saved under <Home>/cache/blocked-scripts.
	Home string
	// OnAlways persists an "always" choice (the caller writes it to config.yaml).
	OnAlways func(patternKeys []string)
}

// Decision is the gate's answer for one command.
type Decision struct {
	Approved bool
	// Message is addressed to the model and explains a refusal.
	Message string
	// Outcome says how it ended: "denied", "timeout", "cancelled", "blocked" (non-interactive).
	Outcome     string
	PatternKey  string
	Description string
	// Hardline and UserDeny mark the unconditional floors.
	Hardline, UserDeny bool
	// UserApproved is set when a human said yes.
	UserApproved bool
}

// Gate is the approval state of one process: the policy, the detector, and per-session approvals.
type Gate struct {
	Detector Detector

	mu          sync.Mutex
	cfg         Config
	permanent   map[string]bool
	sessionKeys map[string]map[string]bool
	yolo        map[string]bool
}

// NewGate returns a Gate with the given policy.
func NewGate(d Detector, cfg Config) *Gate {
	g := &Gate{Detector: d, cfg: cfg, permanent: map[string]bool{}, sessionKeys: map[string]map[string]bool{}, yolo: map[string]bool{}}
	for _, a := range cfg.Allowlist {
		g.permanent[a] = true
	}
	return g
}

// Check is the one entry point before a shell command runs.
type Check struct {
	SessionID string
	// Prompter is nil when nobody can answer (cron, single-query, a platform without an approval surface).
	Prompter Prompter
	// ContainerIsolated is true for backends that cannot damage the host; the user's deny rules still apply but the
	// dangerous-command prompt is skipped.
	ContainerIsolated bool
}

// Command decides whether command may run. Order: container fast path, the unconditional floors (hardline, sudo
// -S, deny rules), yolo and mode off, the allowlist, unattended policy, session approvals, then the prompt.
func (g *Gate) Command(ctx context.Context, command string, c Check) Decision {
	if c.ContainerIsolated {
		if d, blocked := g.userDenyBlock(command); blocked {
			return d
		}
		return Decision{Approved: true}
	}
	if d, blocked := g.floors(command); blocked {
		return d
	}
	if g.yoloActive(c.SessionID) || g.cfg.Mode == "off" {
		return Decision{Approved: true}
	}
	if g.matchesAllowlist(command) {
		return Decision{Approved: true}
	}
	if c.Prompter == nil {
		return g.unattended(command)
	}
	v := g.Detector.DetectDangerous(command)
	if !v.Dangerous || g.IsApproved(c.SessionID, v.Key) {
		return Decision{Approved: true}
	}
	return g.ask(ctx, command, v, c)
}

func (g *Gate) floors(command string) (Decision, bool) {
	if hard, desc := g.Detector.DetectHardline(command); hard {
		return g.hardlineBlock(desc, command), true
	}
	if sudo, desc := g.Detector.DetectSudoStdin(command); sudo {
		return Decision{Message: fmt.Sprintf("BLOCKED: %s. Do not pipe passwords to 'sudo -S' — this is a brute-force "+
			"attack vector. Set SUDO_PASSWORD in your .env file if the "+
			"agent needs passwordless sudo, or run the sudo command manually in your own terminal.", desc)}, true
	}
	return g.userDenyBlock(command)
}

func (g *Gate) userDenyBlock(command string) (Decision, bool) {
	var globs []string
	for _, p := range g.cfg.Deny {
		if p = strings.TrimSpace(p); p != "" {
			globs = append(globs, strings.ToLower(p))
		}
	}
	if len(globs) == 0 {
		return Decision{}, false
	}
	for _, variant := range g.Detector.DenyVariants(command) {
		candidate := strings.TrimSpace(strings.ToLower(variant))
		for _, p := range globs {
			if fnmatch(candidate, p) {
				return Decision{UserDeny: true, Message: fmt.Sprintf("BLOCKED: this command matches the user-defined deny rule "+
					"'%s' (approvals.deny in config.yaml). It cannot be "+
					"executed via the agent — not even with --yolo, /yolo, or "+
					"approvals.mode=off. Do NOT retry or rephrase this command; the user has explicitly forbidden it.", p)}, true
			}
		}
	}
	return Decision{}, false
}

const recoveryPrefix = " RECOVERY: this block fires on oversized/unparseable inline command payloads (heredocs, giant one-liners), not on the operation itself. "

func (g *Gate) hardlineBlock(description, command string) Decision {
	msg := fmt.Sprintf("BLOCKED (hardline): %s. "+
		"This command is on the unconditional blocklist and cannot "+
		"be executed via the agent — not even with --yolo, /yolo, "+
		"approvals.mode=off, or cron approve mode. If you genuinely "+
		"need to run it, run it yourself in a terminal outside the agent.", description)
	if description == parserLimitDescription || description == malformedExecDescription {
		if saved := g.saveBlockedPayload(command); saved != "" {
			msg += recoveryPrefix + fmt.Sprintf("Your command was saved to %s — review it, then run: terminal(command=\"bash %s\"). Do not retry inline.", saved, saved)
		} else {
			msg += recoveryPrefix + "Write the script to a file with write_file, then run it: terminal(command=\"bash /path/script.sh\") or \"python3 /path/script.py\". Do not retry inline."
		}
	}
	return Decision{Message: msg, Hardline: true}
}

// saveBlockedPayload keeps a parser-limit-blocked command as a script: the block is about payload size or shape,
// not the operation, so saving makes recovery one turn. Nothing runs here. Returns "" on any failure.
func (g *Gate) saveBlockedPayload(command string) string {
	if command == "" || g.cfg.Home == "" {
		return ""
	}
	dir := filepath.Join(g.cfg.Home, "cache", "blocked-scripts")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	if old, _ := filepath.Glob(filepath.Join(dir, "blocked-*.sh")); len(old) > 0 {
		for _, f := range old {
			if fi, err := os.Stat(f); err == nil && fi.ModTime().Before(cutoff) {
				_ = os.Remove(f)
			}
		}
	}
	var suffix [4]byte
	_, _ = rand.Read(suffix[:])
	p := filepath.Join(dir, fmt.Sprintf("blocked-%d-%s.sh", time.Now().Unix(), hex.EncodeToString(suffix[:])))
	body := "#!/usr/bin/env bash\n# Auto-saved by Hermes: this command exceeded the inline command\n# parser limit and was blocked from direct execution. Review it,\n# then run it via: bash " + p + "\n" + command
	if !strings.HasSuffix(command, "\n") {
		body += "\n"
	}
	if err := os.WriteFile(p, []byte(strings.ToValidUTF8(body, "�")), 0o700); err != nil {
		return ""
	}
	return p
}

func (g *Gate) yoloActive(session string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.cfg.Yolo || g.yolo[session]
}

// SetSessionYolo turns the dangerous-command prompt off or on for one session. The floors always apply.
func (g *Gate) SetSessionYolo(session string, on bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if on {
		g.yolo[session] = true
	} else {
		delete(g.yolo, session)
	}
}

// ClearSession forgets a session's approvals and yolo state.
func (g *Gate) ClearSession(session string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.sessionKeys, session)
	delete(g.yolo, session)
}

// ApproveSession approves a pattern key for one session.
func (g *Gate) ApproveSession(session, key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.sessionKeys[session] == nil {
		g.sessionKeys[session] = map[string]bool{}
	}
	g.sessionKeys[session][key] = true
}

// ApprovePermanent adds a pattern key to the permanent allowlist.
func (g *Gate) ApprovePermanent(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.permanent[key] = true
}

// IsApproved reports a session or permanent approval for key, honouring the legacy regex-derived keys older
// allowlists may still hold.
func (g *Gate) IsApproved(session, key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for alias := range keyAliases(key) {
		if g.permanent[alias] || g.sessionKeys[session][alias] {
			return true
		}
	}
	return false
}

func (g *Gate) isPermanentlyApproved(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for alias := range keyAliases(key) {
		if g.permanent[alias] {
			return true
		}
	}
	return false
}

// unattended resolves a dangerous command when nobody can answer, from UnattendedMode.
func (g *Gate) unattended(command string) Decision {
	if g.cfg.UnattendedMode != "deny" && g.cfg.UnattendedMode != "" {
		return Decision{Approved: true}
	}
	v := g.Detector.DetectDangerous(command)
	if v.Dangerous && !g.isPermanentlyApproved(v.Key) {
		return Decision{Message: fmt.Sprintf("BLOCKED: Command flagged as dangerous (%s) but this session runs without a user present "+
			"to approve it. Find an alternative approach that avoids this command. To allow dangerous commands "+
			"here, set approvals.unattended_mode: approve in config.yaml.", v.Description), Outcome: "blocked",
			PatternKey: v.Key, Description: v.Description}
	}
	return Decision{Approved: true}
}

const stopCommand = " The user has NOT consented to this action. Do NOT retry this command, do " +
	"NOT rephrase it, and do NOT attempt the same outcome via a different " +
	"command. Stop the current workflow and wait for the user to respond before " +
	"taking any further destructive or irreversible action."

func (g *Gate) ask(ctx context.Context, command string, v Verdict, c Check) Decision {
	pctx := ctx
	if g.cfg.Timeout > 0 {
		var cancel context.CancelFunc
		pctx, cancel = context.WithTimeout(ctx, g.cfg.Timeout)
		defer cancel()
	}
	choice, err := c.Prompter.Ask(pctx, Request{Command: command, Description: v.Description, PatternKey: v.Key})
	deny := func(msg, outcome string) Decision {
		return Decision{Message: msg, Outcome: outcome, PatternKey: v.Key, Description: v.Description}
	}
	switch {
	case err != nil && pctx.Err() == context.DeadlineExceeded && ctx.Err() == nil:
		return deny("BLOCKED: Command timed out without user response."+stopCommand+" Silence is not consent.", "timeout")
	case err != nil:
		return deny(fmt.Sprintf("BLOCKED: Command was not approved: the approval prompt could not be delivered or was not answered (%v)."+
			stopCommand+" Silence is not consent.", err), "cancelled")
	}
	switch choice {
	case Once, Session, Always:
		g.persist(c.SessionID, choice, v.Key)
		return Decision{Approved: true, UserApproved: true, Description: v.Description}
	default:
		return deny("BLOCKED: User denied this command."+stopCommand, "denied")
	}
}

func (g *Gate) persist(session string, choice Choice, key string) {
	if choice != Session && choice != Always {
		return
	}
	g.ApproveSession(session, key)
	if choice == Always {
		g.ApprovePermanent(key)
		if g.cfg.OnAlways != nil {
			g.cfg.OnAlways([]string{key})
		}
	}
}

// ---- the permanent allowlist and fnmatch ---------------------------------------------------------

var shellControlChars = "\n\r;&|<>`$()"
var reinterpretedArgRe = regexp.MustCompile(`(?:^|[ \t])(?:-[^-\s]*[ce]|--(?:command|eval))(?:[= \t]|$)`)

// hasAllowlistShellOperator reports whether command is too compound for the allowlist shortcut. Metacharacters
// inside quotes or behind a backslash are literal arguments; $ or a backtick inside double quotes still expand,
// and a quoted control character counts when an option such as -c or --eval would hand the text to another
// interpreter.
func hasAllowlistShellOperator(command string) bool {
	r := []rune(command)
	var quote rune
	reinterpretable := false
	for i := 0; i < len(r); {
		ch := r[i]
		if ch == '\\' && quote != '\'' {
			if i+1 < len(r) && strings.ContainsRune(shellControlChars, r[i+1]) {
				reinterpretable = true
			}
			i += 2
			continue
		}
		switch {
		case quote != 0:
			if ch == quote {
				quote = 0
			} else if quote == '"' && (ch == '`' || ch == '$') {
				return true
			} else if strings.ContainsRune(shellControlChars, ch) {
				reinterpretable = true
			}
		case ch == '\'' || ch == '"':
			quote = ch
		case ch == '$':
			if i+1 < len(r) && r[i+1] == '(' {
				return true
			}
		case strings.ContainsRune(shellControlChars, ch) && ch != '(' && ch != ')':
			return true
		}
		i++
	}
	if quote != 0 {
		return true
	}
	return reinterpretable && reinterpretedArgRe.MatchString(command)
}

func (g *Gate) matchesAllowlist(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" || hasAllowlistShellOperator(command) {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for p := range g.permanent {
		p = strings.TrimSpace(p)
		if p != "" && (command == p || (strings.ContainsAny(p, "*?[") && fnmatch(command, p))) {
			return true
		}
	}
	return false
}

// fnmatch is Python's fnmatch.fnmatchcase: * matches anything (slashes included), ? one character, [seq] and
// [!seq] character classes.
func fnmatch(name, pattern string) bool {
	var b strings.Builder
	b.WriteString("(?s)^")
	p := []rune(pattern)
	for i := 0; i < len(p); i++ {
		switch c := p[i]; c {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		case '[':
			j := i + 1
			if j < len(p) && p[j] == '!' {
				j++
			}
			if j < len(p) && p[j] == ']' {
				j++
			}
			for j < len(p) && p[j] != ']' {
				j++
			}
			if j >= len(p) {
				b.WriteString(`\[`)
				continue
			}
			class := string(p[i+1 : j])
			class = strings.ReplaceAll(class, `\`, `\\`)
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			} else if strings.HasPrefix(class, "^") {
				class = `\` + class
			}
			b.WriteString("[" + class + "]")
			i = j
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	return err == nil && re.MatchString(name)
}

// ---- legacy approval keys ------------------------------------------------------------------------

var aliases = map[string]map[string]bool{}

func init() {
	add := func(canonical, legacy string) {
		for _, k := range []string{canonical, legacy} {
			if aliases[k] == nil {
				aliases[k] = map[string]bool{}
			}
			aliases[k][canonical], aliases[k][legacy] = true, true
		}
	}
	for _, d := range rawDangerous {
		legacy := d[0]
		if parts := strings.Split(d[0], `\b`); len(parts) > 1 {
			legacy = parts[1]
		} else if r := []rune(d[0]); len(r) > 20 {
			legacy = string(r[:20])
		}
		add(d[1], legacy)
	}
	for canonical, legacy := range removedKeyAliasSource {
		add(canonical, legacy)
	}
	add("git branch force delete", `git\s+branch\s+-D`)
}

func keyAliases(key string) map[string]bool {
	if a, ok := aliases[key]; ok {
		return a
	}
	return map[string]bool{key: true}
}
