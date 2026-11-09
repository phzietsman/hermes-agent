package filetools

import (
	"os"
	"path/filepath"
	"strings"
)

// Every guard here is defense in depth, not a security boundary: the terminal tool runs as the same OS user. The
// value is a clear refusal for models that respect tool errors, and a visible audit trail.

var (
	sensitivePrefixes  = []string{"/etc/", "/boot/", "/usr/lib/systemd/", "/private/etc/", "/private/var/db/", "/private/var/root/"}
	sensitiveExact     = map[string]bool{"/var/run/docker.sock": true, "/run/docker.sock": true}
	homeCredentialDirs = []string{".ssh", ".aws", ".gnupg", ".kube", ".docker", ".azure", ".config/gh", ".config/gcloud"}
	homeDeniedFiles    = [][]string{{".ssh", "authorized_keys"}, {".ssh", "id_rsa"}, {".ssh", "id_ed25519"}, {".netrc"}, {".pgpass"}, {".npmrc"}, {".pypirc"}, {".git-credentials"}}
	hermesDeniedFiles  = []string{".env", ".anthropic_oauth.json", filepath.Join("auth", "google_oauth.json"),
		filepath.Join("cache", "bws_cache.json"), filepath.Join("cache", "bws_cache.enc.json")}
	hermesProtectedSub = []string{"state.db", "sessions", "mcp-tokens", "pairing", "vault", "browser-profile"}
	credentialNames    = []string{"auth.json", "auth.lock", ".anthropic_oauth.json", ".env", "webhook_subscriptions.json",
		filepath.Join("auth", "google_oauth.json"), filepath.Join("cache", "bws_cache.json")}
	projectEnvNames = map[string]bool{".env": true, ".env.local": true, ".env.development": true, ".env.production": true, ".env.test": true, ".env.staging": true, ".envrc": true}
	protectedNames  = map[string]bool{"agents.md": true, "claude.md": true, "soul.md": true, ".cursorrules": true}
)

const didSuffix = " (Defense-in-depth — not a security boundary; the terminal tool can still bypass.)"

// guard holds the facts the guards depend on.
type guard struct {
	home      string // OS user's home
	hermesDir []string
}

func newGuard(hermesHome string) guard {
	home, _ := os.UserHomeDir()
	g := guard{home: realpath(home)}
	for _, d := range []string{hermesHome, rootOf(hermesHome)} {
		if d != "" {
			g.hermesDir = appendUnique(g.hermesDir, realpath(d))
		}
	}
	return g
}

func appendUnique(xs []string, x string) []string {
	for _, y := range xs {
		if y == x {
			return xs
		}
	}
	return append(xs, x)
}

// rootOf is the Hermes root when home is a named profile (<root>/profiles/<name>), else home itself.
func rootOf(home string) string {
	if home == "" {
		return ""
	}
	if filepath.Base(filepath.Dir(home)) == "profiles" {
		return filepath.Dir(filepath.Dir(home))
	}
	return home
}

func realpath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	// Resolve the longest existing prefix so a missing tail does not defeat the symlink walk.
	dir, base := filepath.Split(filepath.Clean(p))
	if dir == "" || dir == p {
		return filepath.Clean(p)
	}
	return filepath.Join(realpath(strings.TrimSuffix(dir, string(filepath.Separator))), base)
}

func isUnder(p, base string) bool {
	return p == base || strings.HasPrefix(p, base+string(filepath.Separator))
}

// writeDenied returns the refusal for a path that must never be written, or "".
func (g guard) writeDenied(path, resolved string) string {
	// ~/.ssh/config is approval-gated, not denied.
	if resolved == filepath.Join(g.home, ".ssh", "config") {
		return ""
	}
	denied := false
	for _, f := range homeDeniedFiles {
		if resolved == filepath.Join(append([]string{g.home}, f...)...) {
			denied = true
		}
	}
	for _, d := range homeCredentialDirs {
		if strings.HasPrefix(resolved, filepath.Join(g.home, filepath.FromSlash(d))+string(filepath.Separator)) {
			denied = true
		}
	}
	for _, p := range []string{"/etc/sudoers", "/etc/passwd", "/etc/shadow"} {
		if resolved == realpath(p) {
			denied = true
		}
	}
	for _, p := range []string{"/etc/sudoers.d", "/etc/systemd"} {
		if strings.HasPrefix(resolved, realpath(p)+string(filepath.Separator)) {
			denied = true
		}
	}
	for _, base := range g.hermesDir {
		for _, f := range hermesDeniedFiles {
			if resolved == filepath.Join(base, f) {
				denied = true
			}
		}
		for _, sub := range hermesProtectedSub {
			if isUnder(resolved, filepath.Join(base, sub)) {
				denied = true
			}
		}
	}
	if denied {
		return "Write denied: '" + path + "' is a protected system/credential file."
	}
	return ""
}

// sensitivePath is the write refusal for system locations and the Hermes config file.
func (g guard) sensitivePath(path, resolved string, hermesConfig string) string {
	candidates := []string{resolved, filepath.Clean(path)}
	for _, c := range candidates {
		for _, p := range sensitivePrefixes {
			if strings.HasPrefix(c, p) {
				return "Refusing to write to sensitive system path: " + path + "\nUse the terminal tool with sudo if you need to modify system files."
			}
		}
		if sensitiveExact[c] {
			return "Refusing to write to sensitive system path: " + path + "\nUse the terminal tool with sudo if you need to modify system files."
		}
	}
	if hermesConfig != "" {
		for _, c := range candidates {
			if c == hermesConfig {
				return "Refusing to write to Hermes config file: " + path + "\nAgent cannot modify security-sensitive configuration. " +
					"Edit ~/.hermes/config.yaml directly or use 'hermes config' instead."
			}
		}
	}
	return ""
}

// readBlocked is the refusal for a read of credential stores, internal caches and project .env files.
func (g guard) readBlocked(path, resolved string) string {
	reason := ""
	for _, hd := range g.hermesDir {
		if isUnder(resolved, filepath.Join(hd, "skills", ".hub")) {
			reason = "is an internal Hermes cache file and cannot be read directly to prevent " +
				"prompt injection. Use the skills_list or skill_view tools instead."
		}
	}
	if reason == "" {
		for _, hd := range g.hermesDir {
			for _, n := range credentialNames {
				if resolved == filepath.Join(hd, n) {
					reason = "is a Hermes credential store and cannot be read directly. Provider tools " +
						"consume these credentials through internal channels." + didSuffix
				}
			}
		}
	}
	if reason == "" {
		type deniedDir struct{ sub, dirMsg, fileMsg string }
		for _, d := range []deniedDir{
			{"mcp-tokens", "is the Hermes MCP token directory and cannot be read directly.", "is a Hermes MCP token file and cannot be read directly."},
			{"browser-profile", "is the Hermes real-profile browser snapshot directory (copied cookies/logins) and cannot be read directly.",
				"is inside the Hermes real-profile browser snapshot (copied cookies/logins) and cannot be read directly."},
			{"vault", "is the Hermes credential vault directory and cannot be read directly (secrets are filled server-side by browser_vault_fill).",
				"is inside the Hermes credential vault (encrypted secrets + local key) and cannot be read directly (browser_vault_fill resolves them server-side)."},
		} {
			for _, hd := range g.hermesDir {
				blocked := filepath.Join(hd, d.sub)
				if isUnder(resolved, blocked) {
					if resolved == blocked {
						reason = d.dirMsg + didSuffix
					} else {
						reason = d.fileMsg + didSuffix
					}
				}
			}
		}
	}
	if reason == "" && projectEnvNames[strings.ToLower(filepath.Base(resolved))] {
		reason = "is a secret-bearing environment file and cannot be read to prevent credential " +
			"leakage. If you need to check the file structure, read .env.example instead." + didSuffix
	}
	if reason == "" {
		return ""
	}
	return "Access denied: " + path + " " + reason
}

// protectedInstructionTarget reports the name of a protected agent-instruction file the path points at, or "".
// Both the normalized input and its realpath are checked so no symlink direction escapes. Files under the Hermes
// home are governed by their own guards.
func (g guard) protectedInstructionTarget(path, resolved string) string {
	for _, hd := range g.hermesDir {
		if isUnder(resolved, hd) {
			return ""
		}
	}
	for _, c := range []string{filepath.Clean(path), resolved} {
		base := filepath.Base(c)
		if protectedNames[strings.ToLower(base)] {
			return base
		}
		parts := strings.Split(strings.TrimRight(filepath.ToSlash(c), "/"), "/")
		if len(parts) >= 2 && parts[len(parts)-2] == ".hermes" {
			return c
		}
	}
	return ""
}

// blockedDevicePaths would block or produce endless output when read.
var blockedDevicePaths = map[string]bool{
	"/dev/zero": true, "/dev/random": true, "/dev/urandom": true, "/dev/full": true, "/dev/stdin": true, "/dev/tty": true,
	"/dev/console": true, "/dev/stdout": true, "/dev/stderr": true, "/dev/fd/0": true, "/dev/fd/1": true, "/dev/fd/2": true,
}

func isBlockedDevice(path string) bool {
	if blockedDevicePaths[path] {
		return true
	}
	if strings.HasPrefix(path, "/proc/") {
		for _, suf := range []string{"/fd/0", "/fd/1", "/fd/2", "/environ", "/cmdline", "/maps", "/smaps", "/smaps_rollup", "/numa_maps", "/mem", "/auxv", "/pagemap"} {
			if strings.HasSuffix(path, suf) {
				return true
			}
		}
	}
	return false
}
