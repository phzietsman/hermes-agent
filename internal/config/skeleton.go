package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// homeSubdirs is the skeleton every home carries.
var homeSubdirs = []string{"cron", "sessions", "logs", "memories"}

// HomeInitError means the home skeleton is unavailable (a dangling link, a read-only mount),
// as opposed to an invalid config file.
type HomeInitError struct {
	Path string
	Err  error
}

func (e *HomeInitError) Error() string {
	return fmt.Sprintf("cannot initialize Hermes directory %s: %v. Check the directory or link target, mount availability and access permissions; Hermes has not replaced the link or created its missing target", e.Path, e.Err)
}

func (e *HomeInitError) Unwrap() error { return e.Err }

// Init creates the home and its cron, sessions, logs and memories directories at mode 0700.
//
// A symlink anywhere on the path must resolve: Init never creates the target of a missing mount.
// Permissions are only tightened when no symlink sits at or below the home, since a link inside
// the home means the operator owns that directory's mode. Links above the home (macOS /tmp or
// /var) say nothing about ownership and do not stop the chmod.
func Init(home string) error {
	for _, dir := range append([]string{home}, joinAll(home, homeSubdirs)...) {
		if err := ensureDir(dir, home); err != nil {
			return &HomeInitError{Path: dir, Err: err}
		}
	}
	return nil
}

func joinAll(base string, names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = filepath.Join(base, n)
	}
	return out
}

// symlinkChain lists every component of path (parents first) that is a symlink.
func symlinkChain(path string) []string {
	var parts []string
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		parts = append([]string{p}, parts...)
		if filepath.Dir(p) == p {
			break
		}
	}
	var links []string
	for _, p := range parts {
		if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			links = append(links, p)
		}
	}
	return links
}

func ensureDir(path, home string) error {
	links := symlinkChain(path)
	for _, l := range links {
		if fi, err := os.Stat(l); err != nil || !fi.IsDir() {
			return fmt.Errorf("directory link is unavailable: %s", l)
		}
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	for _, l := range links {
		if l == home || within(l, home) {
			return nil // operator-owned: leave the mode alone
		}
	}
	return os.Chmod(path, 0o700)
}
