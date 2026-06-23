//go:build docker_integration

// This test needs Docker and is slow (~25s), so it is gated behind the
// `docker_integration` build tag and excluded from the default `go test ./...`.
// Run it explicitly:
//
//	go test -tags docker_integration -run TestTwoBox_Docker ./cmd/gusset/
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestTwoBox_Docker is the highest-fidelity single-host test: two gusset
// containers on a user-defined Docker bridge, each in its own network namespace
// with its own IP — two genuinely separate network stacks, not loopback. The
// source serves a copy of the live uBlock store; the target resolves the source
// by container name over the bridge, dials it, pulls, and applies all 42 keys.
// It then probes whether mDNS discovery survives the bridge (best-effort: logged,
// never failed, since loopback/bridge multicast is environment-dependent).
//
// gusset is a static CGO-free binary, so the image is FROM scratch with no base
// pull. The test skips cleanly when Docker is unavailable or in -short mode.
func TestTwoBox_Docker(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Docker integration test in -short mode")
	}
	if !dockerUsable() {
		t.Skip("Docker not usable here")
	}
	liveProfile, srcUUID := liveUBOProfile(t)

	// Static binary -> scratch image (no registry pull).
	ctx := t.TempDir()
	build := exec.Command("go", "build", "-o", filepath.Join(ctx, "gusset"), ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("static build: %v\n%s", err, out)
	}
	dockerfile := "FROM scratch\nCOPY gusset /gusset\nENTRYPOINT [\"/gusset\"]\n"
	if err := os.WriteFile(filepath.Join(ctx, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		t.Fatal(err)
	}
	const image = "gusset-twobox-test:latest"
	if out, err := exec.Command("docker", "build", "-q", "-t", image, ctx).CombinedOutput(); err != nil {
		t.Fatalf("docker build: %v\n%s", err, out)
	}

	const net = "gusset-twobox-net"
	dockerRm(net, "gusset-src", "gusset-tgt", "gusset-tgt-mdns") // clear any leftovers
	if out, err := exec.Command("docker", "network", "create", net).CombinedOutput(); err != nil {
		t.Fatalf("docker network create: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		dockerRm("gusset-src", "gusset-tgt", "gusset-tgt-mdns")
		_ = exec.Command("docker", "network", "rm", net).Run()
		_ = exec.Command("docker", "image", "rm", "-f", image).Run()
	})

	srcHome := makeSourceHome(t, liveProfile, srcUUID)

	// Source container: serve on a fixed port for a generous window.
	srcArgs := []string{
		"run", "-d", "--name", "gusset-src", "--network", net,
		"--user", currentUserGroup(), "--tmpfs", "/tmp:rw,mode=1777",
		"-e", "HOME=/root", "-e", "GUSSET_PASSPHRASE=" + twoBoxPass,
		"-v", srcHome + "/.mozilla:/root/.mozilla",
		image, "sync", "--extensions", twoBoxExt, "--listen", "0.0.0.0:9999", "--for", "40s",
	}
	if out, err := exec.Command("docker", srcArgs...).CombinedOutput(); err != nil {
		t.Fatalf("run source container: %v\n%s", err, out)
	}
	time.Sleep(2 * time.Second) // let it bind and serve

	// --- Phase 1 (required): target dials the source by container name. ---
	tgtHome, tgtProfile := makeTargetHome(t)
	out := dockerRunTarget(t, net, "gusset-tgt", tgtHome,
		"sync", "--extensions", twoBoxExt, "--peer", "gusset-src:9999", "--for", "4s")
	t.Logf("target (--peer) output:\n%s", out)
	if keys := appliedKeys(t, tgtProfile); keys != 42 {
		t.Fatalf("--peer over the bridge: expected 42 keys applied, got %d\n%s", keys, dockerLogs("gusset-src"))
	}
	t.Logf("docker two-box (--peer by container name over the bridge): 42 keys applied")

	// --- Phase 2 (best-effort): does mDNS discovery survive the bridge? ---
	tgtHome2, tgtProfile2 := makeTargetHome(t)
	out2 := dockerRunTarget(t, net, "gusset-tgt-mdns", tgtHome2,
		"sync", "--extensions", twoBoxExt, "--for", "12s")
	if appliedKeys(t, tgtProfile2) == 42 {
		t.Logf("mDNS discovery WORKS over the Docker bridge — target found the source with no --peer")
	} else {
		t.Logf("mDNS discovery did not converge over the Docker bridge (expected on bridges without multicast flooding); --peer is the reliable path. Output:\n%s", out2)
	}
}

func dockerUsable() bool {
	return exec.Command("docker", "info").Run() == nil
}

func currentUserGroup() string {
	return strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
}

// dockerRunTarget runs a target container to completion, mounting home and
// returning its combined output. Foreground so the caller can assert afterward.
func dockerRunTarget(t *testing.T, net, name, home string, gussetArgs ...string) string {
	t.Helper()
	args := []string{
		"run", "--rm", "--name", name, "--network", net,
		"--user", currentUserGroup(), "--tmpfs", "/tmp:rw,mode=1777",
		"-e", "HOME=/root", "-e", "GUSSET_PASSPHRASE=" + twoBoxPass,
		"-v", home + "/.mozilla:/root/.mozilla",
		"gusset-twobox-test:latest",
	}
	args = append(args, gussetArgs...)
	out, _ := exec.Command("docker", args...).CombinedOutput() // exit code varies; we assert on applied keys
	return string(out)
}

// appliedKeys returns the key count of the applied uBO store in a target
// profile, or 0 if nothing was applied.
func appliedKeys(t *testing.T, profileDir string) int {
	t.Helper()
	originName := "moz-extension+++" + twoBoxTarget + "^userContextId=4294967295"
	matches, _ := filepath.Glob(filepath.Join(profileDir, "storage", "default", originName, "idb", "*.sqlite"))
	if len(matches) != 1 {
		return 0
	}
	return countKeys(t, matches[0])
}

func dockerRm(names ...string) {
	for _, n := range names {
		_ = exec.Command("docker", "rm", "-f", n).Run()
	}
}

func dockerLogs(name string) string {
	out, _ := exec.Command("docker", "logs", name).CombinedOutput()
	return "source container logs:\n" + string(out)
}
