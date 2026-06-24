//go:build windows

package store

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestParentLockHeld_Windows checks the running-Firefox probe end to end on
// Windows, including the safety-critical held case via a real exclusive handle
// (CreateFile with share mode 0 — the same primitive nsProfileLock uses), so a
// regression that stops detecting a live browser fails here rather than risking
// a write under a running Firefox.
func TestParentLockHeld_Windows(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, "parent.lock")

	if parentLockHeld(dir) {
		t.Fatal("no parent.lock present: want not held")
	}

	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if parentLockHeld(dir) {
		t.Fatal("parent.lock present but openable (Firefox closed): want not held")
	}

	// Hold parent.lock exclusively (no sharing), as Firefox does while running.
	p, err := syscall.UTF16PtrFromString(lock)
	if err != nil {
		t.Fatal(err)
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ, 0 /* no sharing */, nil,
		syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatalf("exclusive open of parent.lock: %v", err)
	}
	defer func() { _ = syscall.CloseHandle(h) }()

	if !parentLockHeld(dir) {
		t.Fatal("parent.lock held exclusively (Firefox running): want held")
	}
}
