package anthropic

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// keychainService is Claude Code's macOS Keychain entry (a generic password). Hermes reads it and
// mirrors a refresh into it, so the two stores do not diverge on a single-use rotation.
const keychainService = "Claude Code-credentials"

// KeychainItem is the Claude Code item: the account it is stored under and its JSON payload.
type KeychainItem struct {
	Account string
	Payload map[string]any
}

// Keychain is the macOS Keychain item Claude Code stores its login in.
type Keychain interface {
	// Find returns the item, or nil when there is none (or no Keychain on this host).
	Find(ctx context.Context) (*KeychainItem, error)
	// Write updates the existing item under its own account. It never creates one.
	Write(ctx context.Context, account string, payload map[string]any) error
}

// CommandRunner runs an external command with stdin, returning stdout and stderr.
type CommandRunner func(ctx context.Context, stdin string, name string, args ...string) (stdout, stderr string, err error)

func execRunner(ctx context.Context, stdin string, name string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(stdin) // an empty stdin, never the terminal
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

type securityKeychain struct {
	run CommandRunner
}

// SecurityKeychain is the real Keychain through the macOS `security` tool. On any other OS it
// returns nil.
func SecurityKeychain() Keychain {
	if runtime.GOOS != "darwin" {
		return nil
	}
	return securityKeychain{run: execRunner}
}

// NewSecurityKeychain is SecurityKeychain with an injected command runner, for tests.
func NewSecurityKeychain(run CommandRunner) Keychain { return securityKeychain{run: run} }

// `security` prints an attribute as "text" when it is plain printable ASCII and as
// 0x<HEX> "<octal-escaped echo>" otherwise. The quoted form is NOT escaped (an embedded " appears
// raw), so the text group must run to the last quote on the line.
const keychainAttr = `(?:0x([0-9A-Fa-f]+)\b.*|"(.*)")`

var (
	acctRe     = regexp.MustCompile(`(?m)^\s*"acct"<blob>=` + keychainAttr + `\s*$`)
	passwordRe = regexp.MustCompile(`(?m)^password: ` + keychainAttr + `\s*$`)
)

func decodeKeychainAttr(m []string) string {
	if m == nil {
		return ""
	}
	if m[1] != "" {
		b, err := hex.DecodeString(m[1])
		if err != nil {
			return ""
		}
		return string(b)
	}
	return m[2]
}

// Find runs one `find-generic-password -g` call: attributes arrive on stdout and `password: ...` on
// stderr. The account matters because `add-generic-password -U` matches on account AND service, and
// writing under another account would create a second item instead of updating the one Claude Code reads.
func (k securityKeychain) Find(ctx context.Context) (*KeychainItem, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	stdout, stderr, err := k.run(ctx, "", "security", "find-generic-password", "-s", keychainService, "-g")
	if err != nil {
		return nil, nil // no entry, no `security`, or a timeout: all mean "no login here"
	}
	account := decodeKeychainAttr(acctRe.FindStringSubmatch(stdout))
	raw := decodeKeychainAttr(passwordRe.FindStringSubmatch(stderr))
	if account == "" || raw == "" {
		return nil, nil
	}
	var payload map[string]any
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&payload) != nil || payload == nil {
		return nil, nil
	}
	return &KeychainItem{Account: account, Payload: payload}, nil
}

// mirrorCommand is the `security -i` line that updates the item. The command goes to stdin with the
// secret hex-encoded (-X): a bare -w prompts twice on /dev/tty when a terminal exists (hanging the
// CLI) and, with none, reads only the first line and stores an EMPTY password when the confirmation
// read hits EOF. Either way the live token must never sit on argv.
func mirrorCommand(account string, payload map[string]any) (string, error) {
	quoted := func(v string) string { // the `security -i` tokenizer: double quotes, backslash escapes
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v) + `"`
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("add-generic-password -U -a %s -s %s -X %s\n", quoted(account), quoted(keychainService), hex.EncodeToString(asciiJSON(raw))), nil
}

// asciiJSON escapes every non-ASCII rune as \uXXXX so the stored payload is pure ASCII.
func asciiJSON(b []byte) []byte {
	var out bytes.Buffer
	for _, r := range string(b) {
		switch {
		case r < 0x80:
			out.WriteRune(r)
		case r > 0xFFFF:
			r -= 0x10000
			fmt.Fprintf(&out, `\u%04x\u%04x`, 0xD800+(r>>10), 0xDC00+(r&0x3FF))
		default:
			fmt.Fprintf(&out, `\u%04x`, r)
		}
	}
	return out.Bytes()
}

func (k securityKeychain) Write(ctx context.Context, account string, payload map[string]any) error {
	line, err := mirrorCommand(account, payload)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, stderr, err := k.run(ctx, line, "security", "-i")
	if err != nil {
		return fmt.Errorf("keychain write: %w: %s", err, truncate(strings.TrimSpace(stderr), 200))
	}
	return nil
}

// mergeKeychainPayload rotates the claudeAiOauth token triple over the existing payload, keeping its
// metadata (subscriptionType, rateLimitTier, scopes).
func mergeKeychainPayload(existing map[string]any, access, refresh string, expiresAtMS int64) map[string]any {
	merged := make(map[string]any, len(existing))
	for k, v := range existing {
		merged[k] = v
	}
	oauth := map[string]any{}
	if prev, ok := existing["claudeAiOauth"].(map[string]any); ok {
		for k, v := range prev {
			oauth[k] = v
		}
	}
	oauth["accessToken"], oauth["refreshToken"], oauth["expiresAt"] = access, refresh, expiresAtMS
	merged["claudeAiOauth"] = oauth
	return merged
}

// mirrorToKeychain writes the rotated pair into the Claude Code Keychain item too. Claude Code on
// macOS reads the login Keychain first, and refresh tokens are single-use, so a refresh that only
// updated the file would leave the Keychain holding a spent token and Claude Code would log itself
// out. Only the item that held the pair just spent is updated: a different pair there means a
// different login (CLAUDE_CONFIG_DIR) or a rotation Claude Code already made, and overwriting it would
// be the bug in the other direction. Best-effort: it never creates an item and its error is only
// reported to the caller, which must not fail the rotation over it.
func mirrorToKeychain(ctx context.Context, kc Keychain, access, refresh string, expiresAtMS int64, spentRefresh string) error {
	if kc == nil {
		return nil
	}
	item, err := kc.Find(ctx)
	if err != nil || item == nil {
		return err
	}
	oauth, _ := item.Payload["claudeAiOauth"].(map[string]any)
	if cur, _ := oauth["refreshToken"].(string); cur != spentRefresh {
		return nil // not the pair we spent
	}
	return kc.Write(ctx, item.Account, mergeKeychainPayload(item.Payload, access, refresh, expiresAtMS))
}
