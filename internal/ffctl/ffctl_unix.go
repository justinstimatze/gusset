//go:build unix

package ffctl

import "syscall"

// terminateProcess asks a process to exit cleanly. On unix that is SIGTERM —
// never SIGKILL — so Firefox flushes its store and saves the session before it
// goes, which is the whole reason Stop is safe to use.
func terminateProcess(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}

// detachSysProcAttr returns the attributes that launch Firefox detached from
// gusset's session, so the browser keeps running after gusset exits. On unix
// that is a new session (setsid).
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
