//go:build unix

package store

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestParentLockHeld_Fcntl proves the macOS-path detector: when a *live* process
// holds an fcntl write lock on .parentlock, parentLockHeld reports true; when no
// process holds it (a lingering file), it reports false so Apply does not refuse
// over a stale lock. The lock must be held by a separate process — POSIX locks
// are per-process, so a second descriptor in this process would not conflict —
// so we re-exec the test binary as a brief lock holder.
func TestParentLockHeld_Fcntl(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(dir, ".parentlock")
	if err := os.WriteFile(parent, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	// No holder yet: a lingering .parentlock must not read as locked.
	if parentLockHeld(dir) {
		t.Fatal("parentLockHeld reported locked for an unheld .parentlock")
	}

	stop := holdParentLock(t, parent)
	if !parentLockHeld(dir) {
		stop()
		t.Fatal("parentLockHeld did not detect a live fcntl holder")
	}
	stop() // holder exits, releasing the lock

	if parentLockHeld(dir) {
		t.Fatal("parentLockHeld still reported locked after the holder exited")
	}
}

// holdParentLock starts a helper process that takes an fcntl write lock on path
// and holds it until the returned stop func is called (which closes its stdin).
func holdParentLock(t *testing.T, path string) (stop func()) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperHoldParentLock")
	cmd.Env = append(os.Environ(), "GUSSET_HOLD_PARENTLOCK="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, _ := bufio.NewReader(stdout).ReadString('\n')
	if !strings.Contains(line, "LOCKED") {
		_ = stdin.Close()
		_ = cmd.Wait()
		t.Fatalf("helper did not acquire the lock: %q", line)
	}
	return func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	}
}

// TestHelperHoldParentLock is not a real test: re-exec'd by holdParentLock, it
// acquires an fcntl write lock on the file named in GUSSET_HOLD_PARENTLOCK,
// prints "LOCKED", and blocks until its stdin is closed.
func TestHelperHoldParentLock(t *testing.T) {
	path := os.Getenv("GUSSET_HOLD_PARENTLOCK")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	lk := syscall.Flock_t{Type: syscall.F_WRLCK, Whence: 0, Start: 0, Len: 0}
	if err := syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &lk); err != nil {
		return // prints nothing; parent sees a non-LOCKED line and fails
	}
	_, _ = os.Stdout.WriteString("LOCKED\n")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n') // block until stdin closes
}
