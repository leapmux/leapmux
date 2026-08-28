//go:build !windows

// Package procutil contains small helpers for configuring child processes.
package procutil

import (
	"os/exec"
	"sync/atomic"
	"syscall"
	"time"
)

// sighupGrace is how long Terminate waits between SIGHUP and SIGKILL to let
// an interactive shell propagate SIGHUP to its job process groups.
const sighupGrace = 200 * time.Millisecond

// HideConsoleWindow suppresses the child's console window on Windows;
// no-op on Unix.
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

// JobObject is a process-tree kill group. On Unix it wraps a process group
// leader PID: Terminate sends SIGHUP (letting an interactive shell propagate
// to its jobs), waits briefly, then SIGKILLs the group. Methods are safe on
// a nil receiver and idempotent.
type JobObject struct {
	pgid atomic.Int32 // 0 once Terminate/Close has consumed it
}

// AssignCmd records cmd.Process as a process-group leader to tear down
// on Terminate/Close. The child must already be a group leader (Setsid
// or Setpgid). DetachFromTerminal provides that for no-pty login shells.
func AssignCmd(cmd *exec.Cmd) (*JobObject, error) {
	return AssignPID(cmd.Process.Pid)
}

// AssignPID records pid as the leader of a process group to be torn down on
// Terminate/Close. The caller is responsible for ensuring pid was started
// such that it is its own process group leader (e.g. with SysProcAttr.Setsid
// or Setpgid). go-pty's pty.Cmd sets Setsid on Unix, so PTY-spawned shells
// satisfy this contract.
func AssignPID(pid int) (*JobObject, error) {
	j := &JobObject{}
	j.pgid.Store(int32(pid))
	return j, nil
}

// Terminate kills every process in the group. Sends SIGHUP first — interactive
// shells handle SIGHUP by forwarding it to their own jobs (which run in
// distinct process groups) — then SIGKILLs the leader's group after a short
// grace period. Safe on nil receiver; idempotent.
func (j *JobObject) Terminate() error {
	if j == nil {
		return nil
	}
	pgid := j.pgid.Swap(0)
	if pgid == 0 {
		return nil
	}
	_ = syscall.Kill(-int(pgid), syscall.SIGHUP)
	time.Sleep(sighupGrace)
	// Darwin returns EPERM for kill(-pgid) after the group has exited,
	// not ESRCH. The group is gone either way.
	if err := syscall.Kill(-int(pgid), syscall.SIGKILL); err != nil && err != syscall.ESRCH && err != syscall.EPERM {
		return err
	}
	return nil
}

// Close is equivalent to Terminate on Unix since there is no separate
// handle to release. Safe on nil receiver; idempotent.
func (j *JobObject) Close() error {
	return j.Terminate()
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
