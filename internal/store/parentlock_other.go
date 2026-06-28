//go:build !unix && !windows

package store

// parentLockHeld is a no-op on platforms gusset doesn't target (unix has the
// fcntl probe, Windows the parent.lock probe). Kept so the package still builds
// for, e.g., plan9 or js/wasm.
func parentLockHeld(profileDir string) bool { return false }
