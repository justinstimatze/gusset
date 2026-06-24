//go:build !unix

package store

// parentLockHeld is a no-op on non-unix platforms (gusset targets macOS and
// Linux); the "lock" symlink path covers detection there.
func parentLockHeld(profileDir string) bool { return false }
