//go:build !windows

package filelock

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLockExcludesOtherHolders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "l")
	a, _ := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	b, _ := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	defer a.Close()
	defer b.Close()
	unlock, err := Lock(a)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := TryLock(b); ok {
		t.Fatal("a second holder got the lock")
	}
	unlock()
	release, ok := TryLock(b)
	if !ok {
		t.Fatal("the lock was not released")
	}
	release()
}
