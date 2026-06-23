package ffctl

import (
	"os"
	"path/filepath"
	"strconv"
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
