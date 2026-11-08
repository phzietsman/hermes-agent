//go:build windows

package cron

import "os"

// Windows has no flock: in-process mutexes still serialise a single gateway; cross-process exclusion is not
// provided there.
func flock(f *os.File) (func(), error)   { return func() {}, nil }
func tryFlock(f *os.File) (func(), bool) { return func() {}, true }
