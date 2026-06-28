//go:build unix

// These tests exercise the unix lock mechanism: the "lock" symlink (<ip>:+<pid>)
// and /proc-style process identification. On Windows ffctl's symlink functions
// are inert (Firefox locks via an exclusive parent.lock there) and aren't
// safety-critical; the safety-critical Windows path is store.parentLockHeld,
// covered by parentlock_windows_test.go.
package ffctl

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseLockPID(t *testing.T) {
	cases := map[string]int{
		"127.0.0.1:+12345": 12345,
		"192.168.1.5:+1":   1,
		"127.0.1.1:+99999": 99999,
		"[::1]:+4242":      4242, // IPv6-bracketed form
	}
	for target, want := range cases {
		got, err := parseLockPID(target)
		if err != nil {
			t.Errorf("%q: %v", target, err)
			continue
		}
		if got != want {
			t.Errorf("%q: got %d, want %d", target, got, want)
		}
	}

	for _, bad := range []string{"", "no-plus-here", "127.0.0.1:+", "ip:+abc"} {
		if _, err := parseLockPID(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestLockHolderPID_NoLockIsNotRunning(t *testing.T) {
	dir := t.TempDir() // no "lock" symlink
	pid, ok, err := LockHolderPID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ok || pid != 0 {
		t.Fatalf("empty profile should report not running, got pid=%d ok=%v", pid, ok)
	}
}

func TestLockHolderPID_ReadsSymlinkPID(t *testing.T) {
	dir := t.TempDir()
	// Firefox's lock is a symlink whose target encodes the PID; the target need
	// not resolve to a real path.
	if err := os.Symlink("127.0.0.1:+54321", filepath.Join(dir, "lock")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	pid, ok, err := LockHolderPID(dir)
	if err != nil || !ok {
		t.Fatalf("expected a lock holder, got pid=%d ok=%v err=%v", pid, ok, err)
	}
	if pid != 54321 {
		t.Errorf("got pid %d, want 54321", pid)
	}
}

func TestInspectLock_Unlocked(t *testing.T) {
	st, _, err := InspectLock(t.TempDir())
	if err != nil || st != Unlocked {
		t.Fatalf("empty profile: got %v err=%v, want unlocked", st, err)
	}
}

func TestInspectLock_StaleWhenHolderNotFirefox(t *testing.T) {
	dir := t.TempDir()
	// Our own PID is alive but is the test binary, not Firefox -> stale.
	target := "127.0.0.1:+" + strconv.Itoa(os.Getpid())
	if err := os.Symlink(target, filepath.Join(dir, "lock")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	st, pid, err := InspectLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st != LockedStale || pid != os.Getpid() {
		t.Fatalf("got %v pid=%d, want locked-stale pid=%d", st, pid, os.Getpid())
	}
}

func TestInspectLock_UnknownWhenUnparseable(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink("garbage-no-pid", filepath.Join(dir, "lock")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	st, _, err := InspectLock(dir)
	if err != nil || st != LockUnknown {
		t.Fatalf("unparseable lock: got %v err=%v, want unknown", st, err)
	}
}

func TestClearStale_RemovesStaleLockOnly(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, "lock")
	if err := os.Symlink("127.0.0.1:+"+strconv.Itoa(os.Getpid()), lock); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	cleared, err := ClearStale(dir)
	if err != nil || !cleared {
		t.Fatalf("expected to clear a stale lock, got cleared=%v err=%v", cleared, err)
	}
	if _, err := os.Lstat(lock); !os.IsNotExist(err) {
		t.Fatal("stale lock was not removed")
	}
	// Second call is a clean no-op now that the profile is unlocked.
	if cleared, err := ClearStale(dir); err != nil || cleared {
		t.Fatalf("clear on unlocked profile should be a no-op, got cleared=%v err=%v", cleared, err)
	}
}

func TestClearStale_LeavesUnparseableLock(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, "lock")
	if err := os.Symlink("garbage-no-pid", lock); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if cleared, err := ClearStale(dir); err != nil || cleared {
		t.Fatalf("must not clear an unparseable lock, got cleared=%v err=%v", cleared, err)
	}
	if _, err := os.Lstat(lock); err != nil {
		t.Fatal("unparseable lock should have been left in place")
	}
}

// TestProcessStrings_FindsSelf exercises the OS-specific process lookup against
// the test binary's own PID. It runs whichever seam the build selected (/proc on
// Linux, ps on macOS), so it validates the darwin and linux implementations the
// same way. The current process exists, so the lookup must return something that
// names this test binary, and must not be mistaken for Firefox.
func TestProcessStrings_FindsSelf(t *testing.T) {
	got := processStrings(os.Getpid())
	if len(got) == 0 {
		t.Fatal("processStrings returned nothing for the running test process")
	}
	joined := strings.ToLower(strings.Join(got, " "))
	if !strings.Contains(joined, "ffctl") && !strings.Contains(joined, "test") {
		t.Errorf("process strings %q name neither the package nor a test binary", got)
	}
	if looksLikeFirefox(os.Getpid()) {
		t.Error("the test binary was misidentified as Firefox")
	}
}

func TestStop_NotRunningIsNoop(t *testing.T) {
	stopped, err := Stop(t.TempDir(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if stopped {
		t.Fatal("Stop on a non-running profile should report stopped=false")
	}
}

func TestStop_RefusesNonFirefoxLockHolder(t *testing.T) {
	dir := t.TempDir()
	// Point the lock at our own PID, which is the test binary, not Firefox.
	target := "127.0.0.1:+" + strconv.Itoa(os.Getpid())
	if err := os.Symlink(target, filepath.Join(dir, "lock")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Stop(dir, time.Second); err == nil {
		t.Fatal("Stop must refuse to signal a non-Firefox lock holder")
	}
}
