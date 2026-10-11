//go:build !windows

// Package procutil contains small helpers for configuring child processes.
package procutil

import (
	"os/exec"
	"syscall"
)

// HideConsoleWindow does nothing on Unix.
func HideConsoleWindow(*exec.Cmd) {}

// DetachFromTerminal starts the child in a new session (Setsid).
// An interactive login shell (bash -i -l) otherwise inherits the
// parent's process group and controlling terminal. Concurrent probes
// then share that group: one calls tcsetpgrp, the next calls
// kill(0, SIGTTIN), and the signal stops every process in the group,
// including leapmux solo. A new session has no controlling terminal
// and its own process group, so kill(0) cannot reach the parent.
// /dev/tty in the child fails with ENXIO; that is the cost of the
// detach. LeapMux elevation is the hub privilege prompt, not sudo,
// ssh, or a tty askpass. Agent tools that need a controlling
// terminal must use non-interactive auth, or they fail.
//
// The function merges into an existing SysProcAttr. It allocates a new
// SysProcAttr only when cmd.SysProcAttr is nil. It clears Setpgid,
// Pgid, Noctty, Foreground, and Setctty: Go's fork+exec runs setsid
// first, then setpgid and TIOCNOTTY, and those fail with EPERM/ENOTTY
// on a session leader so cmd.Start never runs.
func DetachFromTerminal(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
	cmd.SysProcAttr.Setpgid = false
	cmd.SysProcAttr.Pgid = 0
	cmd.SysProcAttr.Noctty = false
	cmd.SysProcAttr.Foreground = false
	cmd.SysProcAttr.Setctty = false
}

// SignalProcessGroup sends sig to the child's process group. The child
// must be a group leader (Setsid or Setpgid). A nil cmd or Process is
// a no-op.
func SignalProcessGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, sig)
}
