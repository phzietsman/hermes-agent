package anthropic

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
)

// claudeCodeVersionFallback is used when the installed version cannot be detected. Anthropic rejects
// OAuth requests whose user-agent version is too far behind the actual release, so it is kept current.
const claudeCodeVersionFallback = "2.1.74"

// claudeCodePrefixes are install locations probed in addition to PATH. GUI launches and LaunchAgents
// inherit a bare /usr/bin:/bin PATH that carries none of these, so a PATH-only lookup finds nothing
// there even with the CLI installed, and detection would return the stale fallback.
var claudeCodePrefixes = []string{
	"~/.local/bin", "~/.claude/local", "~/bin", "~/.npm-global/bin", "~/.bun/bin", "~/.volta/bin",
	"/opt/homebrew/bin", "/usr/local/bin",
}

var claudeCodeNames = []string{"claude", "claude-code"}

// claudeCodeCandidates are executable paths to try: every PATH hit first (what the user's shell would
// run), then the well-known prefixes, de-duplicated and filtered to files that exist.
func claudeCodeCandidates(lookPath func(string) (string, error), home string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, name := range claudeCodeNames {
		if p, err := lookPath(name); err == nil {
			add(p)
		}
	}
	for _, prefix := range claudeCodePrefixes {
		if strings.HasPrefix(prefix, "~/") {
			prefix = filepath.Join(home, prefix[2:])
		}
		for _, name := range claudeCodeNames {
			p := filepath.Join(prefix, name)
			if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
				add(p)
			}
		}
	}
	return out
}

// DetectClaudeCodeVersion asks the installed CLI (`claude --version`) and falls back to a static
// version. "2.1.74 (Claude Code)" and "2.1.74" both give 2.1.74.
func DetectClaudeCodeVersion(run CommandRunner, lookPath func(string) (string, error), home string) string {
	for _, cmd := range claudeCodeCandidates(lookPath, home) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		out, _, err := run(ctx, "", cmd, "--version")
		cancel()
		if err != nil {
			continue
		}
		if fields := strings.Fields(out); len(fields) > 0 && unicode.IsDigit(rune(fields[0][0])) {
			return fields[0]
		}
	}
	return claudeCodeVersionFallback
}

var (
	versionOnce  sync.Once
	versionValue string
)

// ClaudeCodeVersion detects the installed version once and caches it for the process; only OAuth
// headers need it.
func ClaudeCodeVersion() string {
	versionOnce.Do(func() {
		home, _ := os.UserHomeDir()
		versionValue = DetectClaudeCodeVersion(execRunner, exec.LookPath, home)
	})
	return versionValue
}

// Lookuper reads a named credential (config.Secrets satisfies it: a value in .env beats the process
// environment).
type Lookuper interface {
	Lookup(name string) (string, bool)
}

// Credentials resolves an Anthropic credential on every request and implements Auth. Order:
//
//  1. ANTHROPIC_TOKEN or CLAUDE_CODE_OAUTH_TOKEN. A static OAuth token is replaced by Claude Code's
//     refreshable login when one is adopted, because setup tokens were historically persisted here and
//     would otherwise win before the refreshable login is inspected.
//  2. ANTHROPIC_API_KEY. An explicit API key is never shadowed by a discovered login.
//  3. Claude Code's own login (Keychain or ~/.claude/.credentials.json), refreshed when expired, only
//     when AdoptExternalLogin is set.
//
// Borrowing another application's login is opt-in (auth.adopt_external_logins), stricter than the
// Python default. Using a subscription login outside Claude Code is for the account holder to decide:
// see the decision log (G11).
type Credentials struct {
	Env                Lookuper
	AdoptExternalLogin bool
	Store              Store
	Refresher          *Refresher
	// Version is the Claude Code version for the OAuth user agent; nil detects it.
	Version func() string
}

func (c *Credentials) lookup(names ...string) string {
	for _, n := range names {
		if c.Env == nil {
			if v := strings.TrimSpace(os.Getenv(n)); v != "" {
				return v
			}
			continue
		}
		if v, ok := c.Env.Lookup(n); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func (c *Credentials) refresher() *Refresher {
	if c.Refresher != nil {
		return c.Refresher
	}
	return &Refresher{Now: c.Store.now}
}

func (c *Credentials) readLogin(ctx context.Context) *Login {
	if !c.AdoptExternalLogin {
		return nil
	}
	return c.Store.Read(ctx)
}

// tokenFromLogin returns the login's access token, refreshing it when expired.
func (c *Credentials) tokenFromLogin(ctx context.Context, login *Login) (string, error) {
	if login == nil {
		return "", ErrNoCredentials
	}
	if IsRotationConsumedUncommitted(c.Store.CredentialsPath(), login.AccessToken) {
		// The file still holds the spent pre-rotation copy of a failed commit.
		return "", ErrRotationSpent
	}
	if login.Valid(c.Store.now()) {
		return login.AccessToken, nil
	}
	token, err := c.refresher().RefreshLogin(ctx, c.Store, *login)
	if err != nil {
		return "", fmt.Errorf("the Claude Code login is expired and could not be refreshed: %w", err)
	}
	return token, nil
}

// Token resolves the credential to use now.
func (c *Credentials) Token(ctx context.Context) (string, error) {
	login := c.readLogin(ctx) // read at most once per resolve
	if env := c.lookup("ANTHROPIC_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"); env != "" {
		if IsOAuthToken(env) && login != nil && login.RefreshToken != "" {
			if t, err := c.tokenFromLogin(ctx, login); err == nil && t != env {
				return t, nil
			}
		}
		return env, nil
	}
	if key := c.lookup("ANTHROPIC_API_KEY"); key != "" {
		return key, nil
	}
	return c.tokenFromLogin(ctx, login)
}

// ForceRefresh refreshes the adopted Claude Code login now, for a 401 on a token that looked valid.
// It adopts a login another process already rotated before spending the refresh token. Credentials
// that are not an adopted login have nothing to refresh.
func (c *Credentials) ForceRefresh(ctx context.Context) (string, error) {
	login := c.readLogin(ctx)
	if login == nil {
		return "", ErrNoCredentials
	}
	return c.refresher().RefreshLogin(ctx, c.Store, *login)
}

func (c *Credentials) userAgent() string {
	v := c.Version
	if v == nil {
		v = ClaudeCodeVersion
	}
	return "claude-code/" + v() + " (external, cli)"
}

// Apply sets the credential headers. An OAuth token is sent as a bearer with the Claude Code identity
// (Anthropic routes OAuth by user agent and 500s intermittently without it); an API key is sent as x-api-key.
func (c *Credentials) Apply(ctx context.Context, h http.Header) (bool, error) {
	token, err := c.Token(ctx)
	if err != nil {
		return false, err
	}
	if !IsOAuthToken(token) {
		h.Set("x-api-key", token)
		return false, nil
	}
	h.Set("Authorization", "Bearer "+token)
	h.Set("User-Agent", c.userAgent())
	h.Set("x-app", "cli")
	return true, nil
}
