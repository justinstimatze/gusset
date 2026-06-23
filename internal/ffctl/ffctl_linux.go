//go:build linux

package ffctl

import (
	"fmt"
	"os"
)

// processStrings returns identifying strings for a pid from /proc — the comm
// name and the full (NUL-separated) cmdline. Either may be missing if the
// process exited or is unreadable; callers treat an empty result as "not
// Firefox", which fails safe (Stop refuses, ClearStale won't clear).
func processStrings(pid int) []string {
	var out []string
	for _, f := range []string{"comm", "cmdline"} {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/%s", pid, f))
		if err != nil {
			continue
		}
		out = append(out, string(b))
	}
	return out
}

// defaultFirefoxBinary is the relaunch command when the user passes no
// --firefox-bin. "firefox" on PATH resolves snap, flatpak, and distro/tarball
// installs alike on Linux.
func defaultFirefoxBinary() string { return "firefox" }
