//go:build windows

package ffctl

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
)

// processStrings returns identifying strings for a pid via tasklist (built into
// Windows) — the image name, e.g. "firefox.exe". There is no /proc; tasklist's
// default columns don't include the full command line, but the image name is
// enough for the "firefox" match in looksLikeFirefox. A failed query yields no
// string, which callers treat as "not Firefox" and so fail safe (Stop refuses,
// ClearStale won't clear).
//
// Note: on Windows the "lock" symlink that LockHolderPID/InspectLock read does
// not exist (Firefox locks the profile via an exclusive open on parent.lock),
// so those functions report "unlocked" and Stop is a no-op here. The running-
// Firefox data-safety guard on Windows comes from store.parentLockHeld probing
// parent.lock, not from this lookup. processStrings exists so the package builds
// and looksLikeFirefox stays meaningful if a PID is ever obtained another way.
func processStrings(pid int) []string {
	out, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/FO", "CSV", "/NH").Output()
	if err != nil {
		return nil
	}
	return []string{string(out)}
}

// defaultFirefoxBinary is the relaunch command when the user passes no
// --firefox-bin. It returns the first standard install path that exists (64-bit
// then 32-bit), falling back to the 64-bit default; users with Firefox elsewhere
// pass --firefox-bin.
func defaultFirefoxBinary() string {
	candidates := []string{
		filepath.Join(os.Getenv("ProgramFiles"), "Mozilla Firefox", "firefox.exe"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Mozilla Firefox", "firefox.exe"),
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return `C:\Program Files\Mozilla Firefox\firefox.exe`
}

// terminateProcess asks a process to exit cleanly. Windows has no SIGTERM;
// `taskkill /PID` (without /F) posts a close request the application can handle,
// so Firefox saves its session and flushes its store — the Windows analog of the
// SIGTERM the unix path sends. (Stop does not currently reach this on Windows;
// see processStrings.)
func terminateProcess(pid int) error {
	return exec.Command("taskkill", "/PID", strconv.Itoa(pid)).Run()
}

// detachSysProcAttr returns the attributes for launching Firefox on Windows.
// There is no setsid; an empty attr plus the Process.Release() the caller does
// is enough to let the browser outlive gusset.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{}
}
