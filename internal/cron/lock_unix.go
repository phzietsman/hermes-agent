//go:build !windows

package cron

import (
	"os"
	"syscall"
)

// flock takes an exclusive advisory lock on f, blocking; the returned func releases it.
func flock(f *os.File) (func(), error) {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }, nil
}

// tryFlock is flock without waiting; false means another process holds it.
func tryFlock(f *os.File) (func(), bool) {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, false
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }, true
}
