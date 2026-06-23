package main

import (
	"database/sql"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/justinstimatze/gusset/internal/profile"

	_ "modernc.org/sqlite"
)

const (
	twoBoxExt    = "uBlock0@raymondhill.net"
	twoBoxPass   = "correct horse battery staple lorem ipsum dolor sit"
	twoBoxTarget = "abcdef01-2345-6789-abcd-ef0123456789"
)

// TestTwoBox_RealProcessesOverLoopback is the closest thing to a two-machine
// LAN test without two machines: it runs two real `gusset sync` *processes*,
// each with its own HOME (its own Firefox profile) and config, talking over a
// real loopback TCP/TLS connection. The "source" process serves a copy of the
// live uBlock store; the "target" process — a separate profile with a different
// extension UUID and no running Firefox — dials it, pulls, and applies. We then
// verify the data landed in the target profile, re-homed onto its UUID.
//
// It exercises the production binary end to end (config, key derivation, offer
// build, listener, mutual-TLS dial, reconcile, apply, banner) across two OS
// processes — everything a real two-box run does except spanning two kernels.
// Direct-dial via --peer keeps it deterministic; mDNS discovery is unit-tested
// separately (loopback multicast is unreliable).
func TestTwoBox_RealProcessesOverLoopback(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping two-process integration test in -short mode")
	}
	liveProfile, srcUUID := liveUBOProfile(t)

	// Build the binary once.
	bin := filepath.Join(t.TempDir(), "gusset")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build gusset: %v\n%s", err, out)
	}

	srcHome := makeSourceHome(t, liveProfile, srcUUID)
	tgtHome, tgtProfile := makeTargetHome(t)

	addr := freeLoopbackAddr(t)

	// Source: serve the uBO store on a known port for a window.
	src := exec.Command(bin, "sync", "--extensions", twoBoxExt, "--listen", addr, "--for", "8s")
	src.Env = childEnv(srcHome)
	var srcOut strings.Builder
	src.Stdout, src.Stderr = &srcOut, &srcOut
	if err := src.Start(); err != nil {
		t.Fatalf("start source: %v", err)
	}
	t.Cleanup(func() {
		_ = src.Process.Kill()
		_ = src.Wait()
	})

	time.Sleep(1500 * time.Millisecond) // let the source bind and start serving

	// Target: dial the source, pull, apply. Runs to completion.
	tgt := exec.Command(bin, "sync", "--extensions", twoBoxExt, "--peer", addr, "--for", "3s")
	tgt.Env = childEnv(tgtHome)
	tgtOut, err := tgt.CombinedOutput()
	if err != nil {
		t.Fatalf("target sync failed: %v\n%s", err, tgtOut)
	}
	t.Logf("target output:\n%s", tgtOut)

	// Verify the store landed in the target profile, re-homed onto its UUID.
	originName := "moz-extension+++" + twoBoxTarget + "^userContextId=4294967295"
	matches, _ := filepath.Glob(filepath.Join(tgtProfile, "storage", "default", originName, "idb", "*.sqlite"))
	if len(matches) != 1 {
		t.Fatalf("expected one applied sqlite in the target profile, found %d\nsource log:\n%s", len(matches), srcOut.String())
	}
	keys := countKeys(t, matches[0])
	if keys == 0 {
		t.Fatal("target profile received no keys")
	}
	if !strings.Contains(string(tgtOut), "Restart Firefox") {
		t.Errorf("target should tell the user to restart Firefox to load the applied settings")
	}
	t.Logf("two-box over loopback: target process applied %d keys", keys)
}

// liveUBOProfile resolves the live profile and the uBO install UUID, skipping if
// uBlock is not installed here.
func liveUBOProfile(t *testing.T) (profileDir, uuid string) {
	t.Helper()
	root, err := profile.FirefoxRoot()
	if err != nil {
		t.Skipf("no Firefox profile: %v", err)
	}
	dir, err := profile.DefaultProfileDir(root)
	if err != nil {
		t.Skipf("no active profile: %v", err)
	}
	uuids, err := profile.ExtensionUUIDs(dir)
	if err != nil || uuids[twoBoxExt] == "" {
		t.Skip("uBlock not installed")
	}
	return dir, uuids[twoBoxExt]
}

// makeSourceHome builds a synthetic HOME containing a plain Firefox profile with
// a minimal prefs.js (uBO -> srcUUID) and a copy of the live uBO storage origin.
func makeSourceHome(t *testing.T, liveProfile, srcUUID string) string {
	t.Helper()
	home := t.TempDir()
	prof := filepath.Join(home, ".mozilla", "firefox", "srcprof")
	if err := os.MkdirAll(prof, 0o755); err != nil {
		t.Fatal(err)
	}
	writeProfilesINI(t, filepath.Join(home, ".mozilla", "firefox"), "srcprof")
	writePrefs(t, prof, srcUUID)

	originName := "moz-extension+++" + srcUUID + "^userContextId=4294967295"
	srcOrigin := filepath.Join(liveProfile, "storage", "default", originName)
	if _, err := os.Stat(srcOrigin); err != nil {
		t.Skipf("uBO origin dir not found in live profile: %v", err)
	}
	copyTree(t, srcOrigin, filepath.Join(prof, "storage", "default", originName))
	return home
}

// makeTargetHome builds a synthetic HOME with an empty profile that has uBO
// mapped to a *different* UUID and no Firefox lock, so apply succeeds.
func makeTargetHome(t *testing.T) (home, profileDir string) {
	t.Helper()
	home = t.TempDir()
	prof := filepath.Join(home, ".mozilla", "firefox", "tgtprof")
	if err := os.MkdirAll(prof, 0o755); err != nil {
		t.Fatal(err)
	}
	writeProfilesINI(t, filepath.Join(home, ".mozilla", "firefox"), "tgtprof")
	writePrefs(t, prof, twoBoxTarget)
	return home, prof
}

func writeProfilesINI(t *testing.T, dir, profName string) {
	t.Helper()
	ini := fmt.Sprintf("[Profile0]\nName=%s\nIsRelative=1\nPath=%s\n", profName, profName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "profiles.ini"), []byte(ini), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writePrefs(t *testing.T, profileDir, uuid string) {
	t.Helper()
	prefs := `user_pref("extensions.webextensions.uuids", "{\"` + twoBoxExt + `\":\"` + uuid + `\"}");` + "\n"
	if err := os.WriteFile(filepath.Join(profileDir, "prefs.js"), []byte(prefs), 0o600); err != nil {
		t.Fatal(err)
	}
}

// childEnv returns the environment for a child gusset process: the real env with
// HOME redirected and the passphrase supplied, and any inherited GUSSET_* config
// pointers cleared so the child uses only its own HOME-derived config.
func childEnv(home string) []string {
	env := []string{"HOME=" + home, "GUSSET_PASSPHRASE=" + twoBoxPass}
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "HOME=") || strings.HasPrefix(e, "GUSSET_") ||
			strings.HasPrefix(e, "XDG_CONFIG_HOME=") {
			continue
		}
		env = append(env, e)
	}
	return env
}

func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = in.Close() }()
		out, err := os.Create(target)
		if err != nil {
			return err
		}
		defer func() { _ = out.Close() }()
		_, err = io.Copy(out, in)
		return err
	})
	if err != nil {
		t.Fatalf("copy %s -> %s: %v", src, dst, err)
	}
}

func countKeys(t *testing.T, sqlitePath string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+sqlitePath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var keys int
	if err := db.QueryRow("SELECT count(*) FROM object_data").Scan(&keys); err != nil {
		t.Fatal(err)
	}
	return keys
}
