// Package ffctl is the opt-in "close and reopen Firefox for you" helper behind
// `gusset sync --restart-firefox`. Applying incoming settings needs Firefox not
// running (it locks the profile and caches the store in memory), so this stops
// the exact Firefox holding the target profile, waits for a clean shutdown, and
// relaunches it afterward. It is deliberately conservative: it signals only a
// process it has confirmed is Firefox, sends SIGTERM (never SIGKILL — a clean
// shutdown flushes the store and saves the session), and never runs unless the
// user opts in. Linux-focused for v1.
package ffctl

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// pollInterval is how often Stop checks whether the profile has unlocked.
const pollInterval = 200 * time.Millisecond

// parseLockPID extracts the PID from a Firefox profile lock symlink target,
// which on Linux has the form "<ip>:+<pid>" (nsProfileLock). The PID follows the
// last '+'.
func parseLockPID(target string) (int, error) {
	i := strings.LastIndex(target, "+")
	if i < 0 || i+1 >= len(target) {
		return 0, fmt.Errorf("ffctl: unrecognized lock target %q", target)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(target[i+1:]))
	if err != nil {
		return 0, fmt.Errorf("ffctl: parse pid from lock target %q: %w", target, err)
	}
	return pid, nil
}

// LockHolderPID returns the PID holding profileDir's lock, with ok=false when the
// profile is not locked (no running Firefox). The "lock" symlink is removed on a
// clean shutdown, so its absence means not running.
func LockHolderPID(profileDir string) (pid int, ok bool, err error) {
	target, err := os.Readlink(filepath.Join(profileDir, "lock"))
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("ffctl: read lock: %w", err)
	}
	pid, err = parseLockPID(target)
	if err != nil {
		return 0, false, err
	}
	return pid, true, nil
}

// LockStatus classifies a profile's lock symlink.
type LockStatus int

const (
	Unlocked    LockStatus = iota // no lock symlink: Firefox not running
	LockedLive                    // held by a process confirmed to be Firefox
	LockedStale                   // lock present but no running Firefox holds it
	LockUnknown                   // lock present but unparseable: do not auto-clear
)

func (s LockStatus) String() string {
	switch s {
	case Unlocked:
		return "unlocked"
	case LockedLive:
		return "locked-live"
	case LockedStale:
		return "locked-stale"
	default:
		return "unknown"
	}
}

// InspectLock classifies profileDir's lock: Unlocked (no symlink), LockedLive
// (a running Firefox holds it), LockedStale (the lock lingers but its holder is
// gone or is some other process — e.g. a crash, or a recycled PID), or
// LockUnknown (the symlink target is unparseable, so liveness can't be judged
// and the lock must not be auto-cleared). pid is the holder when known.
func InspectLock(profileDir string) (LockStatus, int, error) {
	target, err := os.Readlink(filepath.Join(profileDir, "lock"))
	if errors.Is(err, os.ErrNotExist) {
		return Unlocked, 0, nil
	}
	if err != nil {
		return LockUnknown, 0, fmt.Errorf("ffctl: read lock: %w", err)
	}
	pid, perr := parseLockPID(target)
	if perr != nil {
		return LockUnknown, 0, nil // present but unrecognized; leave it alone
	}
	if looksLikeFirefox(pid) {
		return LockedLive, pid, nil
	}
	return LockedStale, pid, nil
}

// ClearStale removes the profile lock only when it is provably stale — no
// running Firefox holds it. It re-checks liveness immediately before removing
// (closing the check-then-act window: if a Firefox started in the meantime and
// now holds the lock, it aborts), and is a no-op when the profile is unlocked,
// held by a live Firefox, or in an unparseable state. store.Apply independently
// re-checks the lock, so this only removes a false "running" signal, never a real
// one. Returns cleared=true only if it actually removed the lock.
func ClearStale(profileDir string) (cleared bool, err error) {
	status, _, err := InspectLock(profileDir)
	if err != nil {
		return false, err
	}
	if status != LockedStale {
		return false, nil
	}
	// Re-read right before removing: if the holder is now Firefox, a browser
	// started since the inspection — do not remove its live lock.
	lock := filepath.Join(profileDir, "lock")
	target, err := os.Readlink(lock)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil // already gone
	}
	if err != nil {
		return false, fmt.Errorf("ffctl: re-read lock: %w", err)
	}
	if pid, perr := parseLockPID(target); perr == nil && looksLikeFirefox(pid) {
		return false, nil // became live; leave it
	}
	if err := os.Remove(lock); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("ffctl: remove stale lock: %w", err)
	}
	return true, nil
}

// looksLikeFirefox checks the process command so a misparsed or recycled PID is
// never signaled by mistake. It reads /proc/<pid>/comm and /proc/<pid>/cmdline.
func looksLikeFirefox(pid int) bool {
	for _, f := range []string{"comm", "cmdline"} {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/%s", pid, f))
		if err != nil {
			continue
		}
		if strings.Contains(strings.ToLower(string(b)), "firefox") {
			return true
		}
	}
	return false
}

// Stop sends SIGTERM to the Firefox holding profileDir and waits up to timeout
// for the profile to unlock. It returns stopped=false (no error) when Firefox is
// not running. It refuses to signal a PID that does not look like Firefox, and
// errors if the profile is still locked after timeout (the caller then leaves
// the settings unapplied and tells the user, rather than applying under a still-
// live browser).
func Stop(profileDir string, timeout time.Duration) (stopped bool, err error) {
	pid, ok, err := LockHolderPID(profileDir)
	if err != nil || !ok {
		return false, err
	}
	if !looksLikeFirefox(pid) {
		return false, fmt.Errorf("ffctl: lock holder pid %d is not Firefox; refusing to signal it", pid)
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return false, fmt.Errorf("ffctl: SIGTERM firefox pid %d: %w", pid, err)
	}
	deadline := time.Now().Add(timeout)
	for {
		_, stillLocked, lerr := LockHolderPID(profileDir)
		if lerr != nil {
			return false, lerr
		}
		if !stillLocked {
			return true, nil
		}
		if time.Now().After(deadline) {
			return false, fmt.Errorf("ffctl: Firefox (pid %d) did not exit within %s", pid, timeout)
		}
		time.Sleep(pollInterval)
	}
}

// Launch starts Firefox detached so it survives the gusset process and restores
// the session after a clean shutdown. binary is the Firefox command (default
// "firefox" on PATH); extra args (e.g. "--profile", dir) may be passed for a
// non-default profile.
func Launch(binary string, args ...string) error {
	if binary == "" {
		binary = "firefox"
	}
	cmd := exec.Command(binary, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // detach from gusset's session
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("ffctl: launch %s: %w", binary, err)
	}
	// Release the child so it is not left a zombie when gusset exits.
	return cmd.Process.Release()
}
