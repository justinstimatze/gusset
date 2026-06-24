package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/justinstimatze/gusset/internal/config"
)

// TestReadPassphrase_RefusesPermissiveFile verifies the root secret is rejected
// when its file is readable by group or other, and accepted at 0600.
func TestReadPassphrase_RefusesPermissiveFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "passphrase")
	if err := os.WriteFile(path, []byte("correct horse battery staple\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{PassphraseFile: path}

	// The mode-bits guard is unix-only (Windows has no Unix perm bits; os.Stat
	// reports 0666 and access is ACL-governed), so the refusal and the chmod that
	// follows only apply there. The acceptance + trim check below runs everywhere.
	if runtime.GOOS != "windows" {
		if _, err := readPassphrase(cfg); err == nil || !strings.Contains(err.Error(), "too permissive") {
			t.Fatalf("expected a too-permissive refusal for mode 0644, got %v", err)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := readPassphrase(cfg)
	if err != nil {
		t.Fatalf("0600 passphrase file rejected: %v", err)
	}
	if got != "correct horse battery staple" {
		t.Fatalf("passphrase = %q, want trimmed content", got)
	}
}
