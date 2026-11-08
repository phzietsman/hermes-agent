//go:build windows

package filelock

import "os"

// Windows has no flock: in-process mutexes still serialise a single gateway; cross-process exclusion is not
// provided there.
func Lock(f *os.File) (func(), error)   { return func() {}, nil }
func TryLock(f *os.File) (func(), bool) { return func() {}, true }
