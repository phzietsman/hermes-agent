package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mode(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestInitCreatesPrivateSkeleton(t *testing.T) {
	home := filepath.Join(t.TempDir(), "hermes")
	for i := 0; i < 2; i++ { // idempotent
		if err := Init(home); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range append([]string{"."}, homeSubdirs...) {
		if got := mode(t, filepath.Join(home, d)); got != 0o700 {
			t.Errorf("%s mode = %o, want 700", d, got)
		}
	}
}

func TestInitTightensLooseModes(t *testing.T) {
	home := filepath.Join(t.TempDir(), "hermes")
	if err := os.MkdirAll(filepath.Join(home, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Init(home); err != nil {
		t.Fatal(err)
	}
	if got := mode(t, filepath.Join(home, "logs")); got != 0o700 {
		t.Errorf("logs mode = %o, want 700", got)
	}
}

func TestInitDiagnosesDanglingLinksWithoutCreatingTargets(t *testing.T) {
	for _, sub := range append([]string{"."}, homeSubdirs...) {
		t.Run(sub, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "hermes")
			link := filepath.Join(home, sub)
			if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, "unmounted", "external")
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}

			err := Init(home)

			var hie *HomeInitError
			if !errors.As(err, &hie) {
				t.Fatalf("err = %v, want *HomeInitError", err)
			}
			if !strings.Contains(err.Error(), link) {
				t.Errorf("error should name the link %s: %v", link, err)
			}
			if _, statErr := os.Stat(filepath.Dir(target)); !os.IsNotExist(statErr) {
				t.Errorf("Init created the missing mount target")
			}
			if got, _ := os.Readlink(link); got != target {
				t.Errorf("link was replaced: %q", got)
			}
		})
	}
}

func TestInitLeavesOperatorOwnedLinkModes(t *testing.T) {
	for _, linked := range []string{"home", "logs"} {
		t.Run(linked, func(t *testing.T) {
			root := t.TempDir()
			shared := filepath.Join(root, "shared")
			if err := os.Mkdir(shared, 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(shared, 0o750); err != nil {
				t.Fatal(err)
			}
			home := filepath.Join(root, "hermes")
			if linked == "home" {
				if err := os.Symlink(shared, home); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(home, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(shared, filepath.Join(home, "logs")); err != nil {
					t.Fatal(err)
				}
			}
			if err := Init(home); err != nil {
				t.Fatal(err)
			}
			if got := mode(t, shared); got != 0o750 {
				t.Errorf("shared target mode = %o, want 750 untouched", got)
			}
			for _, d := range homeSubdirs {
				if fi, err := os.Stat(filepath.Join(home, d)); err != nil || !fi.IsDir() {
					t.Errorf("%s missing: %v", d, err)
				}
			}
		})
	}
}
