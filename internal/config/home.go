// Package config resolves the Hermes home directory and profiles, loads .env secrets
// and config.yaml. It ports hermes_constants.py, hermes_cli/profiles.py (name and path
// resolution only), hermes_cli/config_home.py, hermes_cli/env_loader.py and the .env
// tokenizer in agent/secret_scope.py.
//
// Python's multiplexed-gateway secret scope, managed (Nix) mode and profile tombstones
// are not ported: Stage 1 serves one profile per process.
package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type homeKey struct{}

// WithHome returns a context whose Home() is path, without touching the process
// environment (the Go counterpart of set_hermes_home_override). An empty path clears it.
func WithHome(ctx context.Context, path string) context.Context {
	return context.WithValue(ctx, homeKey{}, path)
}

// Home resolves the Hermes home: context override, then HERMES_HOME, then the
// platform default (~/.hermes plus HERMES_DATA_DIR_SUFFIX).
func Home(ctx context.Context) string {
	if p, _ := ctx.Value(homeKey{}).(string); strings.TrimSpace(p) != "" {
		return expandHome(p)
	}
	if p := strings.TrimSpace(os.Getenv("HERMES_HOME")); p != "" {
		return expandHome(p)
	}
	return nativeDefaultHome()
}

func nativeDefaultHome() string {
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		h = "."
	}
	return filepath.Join(h, ".hermes"+os.Getenv("HERMES_DATA_DIR_SUFFIX"))
}

// expandHome expands $VAR and a leading ~ the way os.path.expanduser(expandvars()) does.
func expandHome(p string) string {
	p = os.ExpandEnv(p)
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(h, strings.TrimPrefix(p, "~"))
		}
	}
	return filepath.Clean(p)
}

// resolve returns the symlink-free absolute form of p, falling back to the cleaned
// absolute path when p does not exist (Path.resolve(strict=False)).
func resolve(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = filepath.Clean(p)
	}
	// Resolve the longest existing prefix so a missing tail does not defeat the symlink walk.
	rest := ""
	cur := abs
	for {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

func within(child, parent string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// DefaultRoot is the root that holds profiles/: the native ~/.hermes for any home under it,
// otherwise (Docker or a custom root) <root> for <root>/profiles/<name>, else the home itself.
// An empty home means the HERMES_HOME environment variable.
func DefaultRoot(home string) string {
	native := nativeDefaultHome()
	h := strings.TrimSpace(home)
	if h == "" {
		h = strings.TrimSpace(os.Getenv("HERMES_HOME"))
	}
	if h == "" {
		return native
	}
	env := expandHome(h)
	if within(resolve(env), resolve(native)) {
		return native
	}
	if filepath.Base(filepath.Dir(env)) == "profiles" {
		return filepath.Dir(filepath.Dir(env))
	}
	return env
}

// homeMarkers are files that mark a real Hermes home.
var homeMarkers = []string{"config.yaml", ".env", "state.db"}

func isProfilesRoot(profilesDir string) bool {
	root := filepath.Dir(profilesDir)
	if filepath.Base(root) == ".hermes" {
		return true
	}
	for _, m := range homeMarkers {
		if _, err := os.Stat(filepath.Join(root, m)); err == nil {
			return true
		}
	}
	return resolve(root) == resolve(DefaultRoot(""))
}

// NamedProfileHome returns <root>/profiles/<name> when path is that home or lies under it.
// The name must not start with "." and profiles/ must belong to a real Hermes home. A default
// home that merely has a profiles segment above it is not a named profile. "" means none.
func NamedProfileHome(path string) string {
	cur := filepath.Clean(path)
	for {
		if filepath.Base(filepath.Dir(cur)) == "profiles" && !strings.HasPrefix(filepath.Base(cur), ".") &&
			isProfilesRoot(filepath.Dir(cur)) {
			return cur
		}
		if filepath.Base(cur) == ".hermes" {
			return ""
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return ""
		}
		cur = parent
	}
}

// ProfileName returns the canonical profile id owning path ("default" for the root home),
// or "" when path is not a profile home.
func ProfileName(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	cur := expandHome(path)
	root := DefaultRoot("")
	for _, c := range []string{cur, resolve(cur)} {
		if c == root || c == resolve(root) {
			return "default"
		}
		if n := NamedProfileHome(c); n != "" {
			return filepath.Base(n)
		}
		// A stored profile home is authoritative: its owner already resolved it.
		if filepath.Base(filepath.Dir(c)) == "profiles" && !strings.HasPrefix(filepath.Base(c), ".") {
			return filepath.Base(c)
		}
	}
	return ""
}

var profileIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// reservedProfileNames collide with the installation or common system binaries.
var reservedProfileNames = map[string]bool{"hermes": true, "default": true, "test": true, "tmp": true, "root": true, "sudo": true}

// NormalizeProfileName trims and lowercases; "default" matches case-insensitively.
func NormalizeProfileName(name string) (string, error) {
	s := strings.TrimSpace(name)
	if s == "" {
		return "", errors.New("profile name cannot be empty")
	}
	if strings.EqualFold(s, "default") {
		return "default", nil
	}
	return strings.ToLower(s), nil
}

// ValidateProfileName accepts "default" and lowercase ids matching [a-z0-9][a-z0-9_-]{0,63}
// that are not reserved. The name is never normalised here.
func ValidateProfileName(name string) error {
	if name == "default" {
		return nil
	}
	if !profileIDRe.MatchString(name) {
		return fmt.Errorf("invalid profile name %q: use lowercase letters, digits, '-' and '_' (at most 64 characters, starting with a letter or digit)", name)
	}
	if reservedProfileNames[name] {
		return fmt.Errorf("profile name %q is reserved; pick a different name", name)
	}
	return nil
}

// ProfileHome resolves a profile name to its home directory. Reserved names still resolve if
// their directory already exists; only the id syntax is enforced so nothing escapes profiles/.
func ProfileHome(name string) (string, error) {
	canon, err := NormalizeProfileName(name)
	if err != nil {
		return "", err
	}
	if canon == "default" {
		return DefaultRoot(""), nil
	}
	if !profileIDRe.MatchString(canon) {
		return "", fmt.Errorf("invalid profile name %q", canon)
	}
	return filepath.Join(DefaultRoot(""), "profiles", canon), nil
}

// DisplayHome renders home for user-facing text, with the user's home directory as ~.
func DisplayHome(home string) string {
	if h, err := os.UserHomeDir(); err == nil && within(home, h) {
		rel, _ := filepath.Rel(h, home)
		if rel == "." {
			return "~"
		}
		return filepath.Join("~", rel)
	}
	return home
}
