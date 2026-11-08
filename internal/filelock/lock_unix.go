//go:build !windows

package filelock

import (
	"os"
	"syscall"
)

// Lock takes an exclusive advisory lock on f, blocking; the returned func releases it.
func Lock(f *os.File) (func(), error) {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }, nil
}

// TryLock is Lock without waiting; false means another process holds it.
func TryLock(f *os.File) (func(), bool) {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, false
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }, true
}
