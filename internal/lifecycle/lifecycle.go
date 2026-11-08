// Package lifecycle recognises commands that stop, restart or kill the messaging gateway from inside it. Such a
// command kills the process that runs it; the supervisor revives the gateway, auto-resume re-runs the turn, and the
// loop repeats. Cron rejects them at creation and the terminal tool refuses them when the agent runs inside a
// gateway. Patterns are anchored on concrete command identifiers, so prose never trips them.
package lifecycle

import (
	"regexp"
	"strings"
)

// A: destructive `hermes gateway` verbs (`start` is benign: a job may start a sibling profile). B: launchctl verbs on
// a hermes-gateway label, including submit and bootstrap, which register a NEW keepalive job (a laundered restart).
// C: systemctl on a hermes-gateway unit. D: pkill, kill and Stop-Process naming hermes and gateway in either order.
var (
	hermesGateway = regexp.MustCompile(`(?i)hermes\s+gateway\s+(?:restart|stop|uninstall)\b`)
	launchctl     = regexp.MustCompile(`(?i)launchctl\s+(?:kickstart|unload|load|stop|restart|submit|bootstrap|bootout|remove|disable)\b[^\n]*\bhermes[.\-]?gateway`)
	systemctl     = regexp.MustCompile(`(?i)systemctl\s+(?:-\S+\s+)*(?:restart|stop|start)\b[^\n]*\bhermes[.\-]?gateway`)
	killNamed     = regexp.MustCompile(`(?i)\b(?:p?kill|stop-process)\b[^\n]*\bhermes\b[^\n]*\bgateway`)
	killNamedRev  = regexp.MustCompile(`(?i)\b(?:p?kill|stop-process)\b[^\n]*\bgateway\b[^\n]*\bhermes`)
)

// HostInterpreterKillRejection is shown when a command would kill every Python interpreter, the image that hosts a
// supervised gateway (and the command itself).
const HostInterpreterKillRejection = "Blocked: this command kills every process whose image/name matches the Python " +
	"interpreter, which is the process hosting this gateway (and this command). " +
	"Stop only the process you own instead, by its explicit PID."

// GatewayRejection is shown for the command-shaped branches.
const GatewayRejection = "Blocked: this would stop or restart the gateway from inside the gateway, which kills the process " +
	"running this command and starts a restart loop. Ask the user to run it from a terminal outside the agent."

func isWordChar(c byte) bool {
	return c == '_' || c == '/' || c == '.' || c == '-' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// hermesGatewayVerb matches branch A with Python's lookbehind: `hermes` must not be a path component or the tail of
// a word (`/docs/hermes gateway restart-notes.md`), while every real command position still matches.
func hermesGatewayVerb(text string) bool {
	for _, loc := range hermesGateway.FindAllStringIndex(text, -1) {
		if loc[0] == 0 || !isWordChar(text[loc[0]-1]) {
			return true
		}
	}
	return false
}

var interpreterRe = regexp.MustCompile(`^pythonw?(?:\d+(?:\.\d+)*)?$`)

// killers whose operand is a process NAME; option values are not operands.
var valueOptions = map[string]map[string]bool{
	"pkill":   {"-g": true, "-G": true, "-P": true, "-s": true, "-t": true, "-u": true, "-U": true, "-F": true, "-d": true, "-O": true, "-r": true, "-A": true, "--signal": true, "--pgroup": true, "--parent": true, "--euid": true, "--uid": true},
	"killall": {"-s": true, "-u": true, "-o": true, "-y": true, "-n": true, "-Z": true, "--signal": true, "--user": true},
}

func isInterpreterImage(v string, substring bool) bool {
	name := strings.ToLower(strings.Trim(strings.TrimSpace(v), `"'`))
	if name == "" {
		return false
	}
	if strings.HasSuffix(name, "*") {
		return strings.HasPrefix("python", strings.TrimRight(name, "*"))
	}
	if interpreterRe.MatchString(name) {
		return true
	}
	return substring && len(name) >= 2 && strings.HasPrefix("python", name)
}

var ereTail = regexp.MustCompile(`[.*+?\\\[\](){}|].*$`)

// killsHostInterpreter is branch E, token-aware: `pkill -9 python3` and `killall python` carry no hermes or gateway
// token yet terminate the gateway. Option values are never read as targets, `-f` patterns are judged as patterns,
// other image names stay available, and a numeric PID is the explicit, ownership-scoped route (out of scope).
func killsHostInterpreter(text string) bool {
	for _, seg := range splitSegments(text) {
		toks := shellFields(seg)
		for i, t := range toks {
			base := t
			if j := strings.LastIndex(base, "/"); j >= 0 {
				base = base[j+1:]
			}
			base = strings.ToLower(base)
			if base != "pkill" && base != "killall" {
				continue
			}
			full, exact := false, base == "killall"
			var operands []string
			for k := i + 1; k < len(toks); k++ {
				a := toks[k]
				switch {
				case strings.HasPrefix(a, "-"):
					if valueOptions[base][a] {
						k++
						continue
					}
					if base == "pkill" && strings.Contains(a, "f") && !strings.HasPrefix(a, "--") {
						full = true
					}
					if base == "pkill" && (strings.Contains(a, "x") && !strings.HasPrefix(a, "--")) {
						exact = true
					}
				default:
					operands = append(operands, a)
				}
			}
			for _, op := range operands {
				if patternReachesHost(op, full, exact) {
					return true
				}
			}
		}
	}
	return false
}

var (
	ereWildcardOnly = regexp.MustCompile(`^[.*+?$\s]*$`)
	gatewayCmdline  = regexp.MustCompile(`(?i)hermes_cli|\bhermes\b[^\n]*\bgateway\b|\bgateway\b[^\n]*\bhermes\b`)
)

// patternReachesHost reads a pkill/pgrep/killall operand as an ERE over the process name (or, with -f, the whole
// command line). `python -m hermes_cli.main ...` is the gateway's own command line, so a -f pattern that names the
// interpreter and then only wildcards or a hermes token reaches it, while `python mt_add.py` (a specific script) does
// not.
func patternReachesHost(pattern string, full, exact bool) bool {
	core := strings.TrimLeft(strings.Trim(strings.TrimSpace(pattern), `"'`), "^")
	core = strings.TrimSuffix(core, "$")
	head, rest := core, ""
	if full {
		head, rest, _ = strings.Cut(core, " ")
	}
	if !isInterpreterImage(head, !exact) {
		if loc := ereTail.FindStringIndex(head); loc != nil {
			rest = head[loc[0]:] + " " + rest
			head = head[:loc[0]]
		}
	}
	if !isInterpreterImage(head, !exact) {
		return full && gatewayCmdline.MatchString(core)
	}
	return strings.TrimSpace(rest) == "" || ereWildcardOnly.MatchString(rest) || strings.Contains(strings.ToLower(rest), "hermes")
}

var segSplit = regexp.MustCompile(`[;&|\n]+`)

func splitSegments(text string) []string { return segSplit.Split(text, -1) }

// Check returns a rejection message for text (a command, a cron prompt) that contains a gateway-lifecycle command,
// else "".
func Check(text string) string {
	switch {
	case hermesGatewayVerb(text), launchctl.MatchString(text), systemctl.MatchString(text), killNamed.MatchString(text), killNamedRev.MatchString(text):
		return GatewayRejection
	case killsHostInterpreter(text):
		return HostInterpreterKillRejection
	}
	return ""
}

// shellFields splits on whitespace outside quotes and drops the quotes, so `-f 'python mt_add.py'` is one operand.
func shellFields(s string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	has := false
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, has = r, true
		case r == ' ' || r == '\t':
			if has || cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteRune(r)
		}
	}
	if has || cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}
