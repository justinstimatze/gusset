//go:build darwin

package ffctl

import (
	"os/exec"
	"strconv"
)

// processStrings returns identifying strings for a pid via ps — there is no
// /proc on macOS. It reads the executable basename (comm) and the full command
// line (command); either query failing yields no string for that column, which
// callers treat as "not Firefox" and so fail safe (Stop refuses, ClearStale
// won't clear).
func processStrings(pid int) []string {
	var out []string
	for _, col := range []string{"comm=", "command="} {
		b, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", col).Output()
		if err != nil {
			continue
		}
		out = append(out, string(b))
	}
	return out
}

// defaultFirefoxBinary is the relaunch command when the user passes no
// --firefox-bin. The .app's inner binary accepts --profile/--new-instance
// directly; `open -a Firefox` cannot forward arbitrary args cleanly, so the
// direct path is the default. Users with Firefox elsewhere pass --firefox-bin.
func defaultFirefoxBinary() string {
	return "/Applications/Firefox.app/Contents/MacOS/firefox"
}
