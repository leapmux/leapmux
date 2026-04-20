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

// JobObject is a process-tree kill group. On Unix it wraps a process group
// leader PID: Terminate sends SIGHUP (letting an interactive shell propagate
// to its jobs), waits briefly, then SIGKILLs the group. Methods are safe on
// a nil receiver and idempotent.
type JobObject struct {
	pgid atomic.Int32 // 0 once Terminate/Close has consumed it
}

// AssignCmd is a no-op on Unix. Callers that need tree-kill semantics for an
// arbitrary child process should start it with Setpgid and use AssignPID.
// Retained as a no-op so agent startup paths keep the pre-existing Unix
// behavior of relying on SIGTERM propagation.
func AssignCmd(*exec.Cmd) (*JobObject, error) { return nil, nil }

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
	if err := syscall.Kill(-int(pgid), syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}

// Close is equivalent to Terminate on Unix since there is no separate
// handle to release. Safe on nil receiver; idempotent.
func (j *JobObject) Close() error {
	return j.Terminate()
}
