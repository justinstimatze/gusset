//go:build windows

package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// parentLockHeld reports whether Firefox is holding the profile open on Windows.
// Windows Firefox does not write the parseable "lock" symlink that ffctl reads
// on Linux; instead nsProfileLock opens "parent.lock" with an exclusive share
// mode for the life of the session. So the running-Firefox signal on Windows is
// simply: can we open parent.lock ourselves? If Firefox holds it exclusively,
// our open fails with a sharing violation; if Firefox is closed, the file is
// still there but opens cleanly.
//
// This is the Windows half of the same data-safety guard the unix build gets
// from the fcntl F_GETLK probe: store.Apply consults it so it never rewrites the
// store out from under a live browser. It fails toward "locked" — any open error
// other than "file does not exist" is treated as held, so an ambiguous result
// refuses the write (safe) rather than risking corruption.
//
// UNVERIFIED against a live Windows Firefox (parent.lock filename and exclusive
// share semantics are from Mozilla's documented behavior, not yet confirmed on
// hardware) — see docs/firefox-internals-verified.md. Until confirmed, close
// Firefox by hand on Windows; the worst case here is an over-cautious refusal,
// never a silent overwrite.
func parentLockHeld(profileDir string) bool {
	f, err := os.Open(filepath.Join(profileDir, "parent.lock"))
	if err == nil {
		_ = f.Close()
		return false // opened cleanly: nothing holds it exclusively
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false // no parent.lock at all: Firefox isn't running
	}
	return true // sharing violation or any other error: assume held (fail safe)
}
