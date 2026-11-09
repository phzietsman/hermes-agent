package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// isolate points HOME at a temp dir and clears the Hermes env so resolution is hermetic.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HERMES_HOME", "")
	t.Setenv("HERMES_DATA_DIR_SUFFIX", "")
	return home
}

func TestHomePrecedence(t *testing.T) {
	user := isolate(t)
	native := filepath.Join(user, ".hermes")

	if got := Home(context.Background()); got != native {
		t.Fatalf("default home = %q, want %q", got, native)
	}

	t.Setenv("HERMES_DATA_DIR_SUFFIX", "-dev")
	if got, want := Home(context.Background()), native+"-dev"; got != want {
		t.Fatalf("suffix home = %q, want %q", got, want)
	}

	t.Setenv("HERMES_HOME", "~/from-env")
	if got, want := Home(context.Background()), filepath.Join(user, "from-env"); got != want {
		t.Fatalf("env home = %q, want %q", got, want)
	}

	t.Setenv("PROFILE_DIR", "p1")
	t.Setenv("HERMES_HOME", "$HOME/$PROFILE_DIR")
	if got, want := Home(context.Background()), filepath.Join(user, "p1"); got != want {
		t.Fatalf("expanded env home = %q, want %q", got, want)
	}

	ctx := WithHome(context.Background(), "/srv/other")
	if got := Home(ctx); got != "/srv/other" {
		t.Fatalf("ctx override = %q", got)
	}
	// The override is context-local: the environment is untouched.
	if os.Getenv("HERMES_HOME") != "$HOME/$PROFILE_DIR" {
		t.Fatal("WithHome mutated the process environment")
	}
	if got := Home(WithHome(context.Background(), "  ")); got != filepath.Join(user, "p1") {
		t.Fatalf("blank override must fall through, got %q", got)
	}
}

func TestDefaultRoot(t *testing.T) {
	user := isolate(t)
	native := filepath.Join(user, ".hermes")
	tests := []struct {
		name, home, want string
	}{
		{"unset", "", native},
		{"native itself", native, native},
		{"native profile", filepath.Join(native, "profiles", "coder"), native},
		{"custom root", "/opt/data", "/opt/data"},
		{"custom root profile", "/opt/data/profiles/coder", "/opt/data"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DefaultRoot(tc.home); got != tc.want {
				t.Fatalf("DefaultRoot(%q) = %q, want %q", tc.home, got, tc.want)
			}
		})
	}
	t.Setenv("HERMES_HOME", "/opt/data/profiles/x")
	if got := DefaultRoot(""); got != "/opt/data" {
		t.Fatalf("env-derived root = %q", got)
	}
}

func TestProfileName(t *testing.T) {
	user := isolate(t)
	native := filepath.Join(user, ".hermes")
	tests := []struct {
		name, path, want string
	}{
		{"empty", "", ""},
		{"blank", "   ", ""},
		{"default root", native, "default"},
		{"named", filepath.Join(native, "profiles", "coder"), "coder"},
		{"under named", filepath.Join(native, "profiles", "coder", "sessions"), "coder"},
		{"hidden dir is not a profile", filepath.Join(native, "profiles", ".deleted"), ""},
		{"unrelated", "/var/data", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ProfileName(tc.path); got != tc.want {
				t.Fatalf("ProfileName(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestNamedProfileHomeNeedsRealHermesRoot(t *testing.T) {
	isolate(t)
	elsewhere := t.TempDir()
	path := filepath.Join(elsewhere, "profiles", "x")
	if got := NamedProfileHome(path); got != "" {
		t.Fatalf("a profiles dir without Hermes markers is not a profile root, got %q", got)
	}
	if err := os.WriteFile(filepath.Join(elsewhere, "config.yaml"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := NamedProfileHome(path); got != path {
		t.Fatalf("marker file should make it a profile root, got %q", got)
	}
}

func TestValidateProfileName(t *testing.T) {
	for _, name := range []string{"default", "coder", "a", "my-work", "my_work", "0", "a1-b2", "x" + string(make([]byte, 0)) + "yz"} {
		if err := ValidateProfileName(name); err != nil {
			t.Errorf("ValidateProfileName(%q) = %v, want nil", name, err)
		}
	}
	long := make([]byte, 65)
	for i := range long {
		long[i] = 'a'
	}
	for _, name := range []string{"", "Coder", "-x", "_x", "a b", "a/b", "../x", ".hidden", "hermes", "root", "sudo", "tmp", "test", string(long)} {
		if err := ValidateProfileName(name); err == nil {
			t.Errorf("ValidateProfileName(%q) = nil, want error", name)
		}
	}
}

func TestNormalizeProfileName(t *testing.T) {
	tests := []struct {
		in, want string
		err      bool
	}{
		{"  Coder ", "coder", false},
		{"DEFAULT", "default", false},
		{"Default", "default", false},
		{"0", "0", false},
		{"", "", true},
		{"   ", "", true},
	}
	for _, tc := range tests {
		got, err := NormalizeProfileName(tc.in)
		if (err != nil) != tc.err || got != tc.want {
			t.Errorf("NormalizeProfileName(%q) = %q, %v", tc.in, got, err)
		}
	}
}

func TestProfileHome(t *testing.T) {
	user := isolate(t)
	native := filepath.Join(user, ".hermes")
	if got, err := ProfileHome("Default"); err != nil || got != native {
		t.Fatalf("default = %q, %v", got, err)
	}
	if got, err := ProfileHome(" Coder"); err != nil || got != filepath.Join(native, "profiles", "coder") {
		t.Fatalf("named = %q, %v", got, err)
	}
	// Names that could escape profiles/ must fail closed.
	for _, bad := range []string{"../etc", "a/b", "..", "a\\b", ".hidden", ""} {
		if got, err := ProfileHome(bad); err == nil {
			t.Errorf("ProfileHome(%q) = %q, want error", bad, got)
		}
	}
	// A reserved name still resolves (its directory may predate the reserved list).
	if _, err := ProfileHome("hermes"); err != nil {
		t.Errorf("reserved name must still resolve: %v", err)
	}
}

func TestDisplayHome(t *testing.T) {
	user := isolate(t)
	tests := []struct{ in, want string }{
		{user, "~"},
		{filepath.Join(user, ".hermes"), filepath.Join("~", ".hermes")},
		{"/opt/data", "/opt/data"},
	}
	for _, tc := range tests {
		if got := DisplayHome(tc.in); got != tc.want {
			t.Errorf("DisplayHome(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
