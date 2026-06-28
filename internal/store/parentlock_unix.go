//go:build unix

package store

import (
	"os"
	"path/filepath"
	"syscall"
)

// parentLockHeld reports whether a live process holds an fcntl (POSIX) write lock
// on the profile's .parentlock file. That is the lock primitive Firefox's
// nsProfileLock uses on platforms where it does not also write a parseable "lock"
// symlink (the open question for macOS — see docs/firefox-internals-verified.md).
// Checking it alongside the symlink means whichever mechanism a given Firefox
// build uses, one of the two catches a running browser.
//
// F_GETLK only *queries* for a conflicting lock; it never acquires one, so it
// cannot disturb Firefox's own lock, and a stale .parentlock that no live process
// holds reports unlocked. This check can therefore only ever add a real "running"
// signal, never manufacture a false one — so it is safe to consult on every unix
// platform even though on Linux the "lock" symlink already does the work.
func parentLockHeld(profileDir string) bool {
	f, err := os.Open(filepath.Join(profileDir, ".parentlock"))
	if err != nil {
		return false // no .parentlock to conflict with
	}
	defer func() { _ = f.Close() }()

	lk := syscall.Flock_t{Type: syscall.F_WRLCK, Whence: 0, Start: 0, Len: 0}
	if err := syscall.FcntlFlock(f.Fd(), syscall.F_GETLK, &lk); err != nil {
		return false // can't determine the holder: do not invent a lock
	}
	return lk.Type != syscall.F_UNLCK
}
