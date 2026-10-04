//go:build windows

// Package procutil contains small helpers for configuring child processes.
package procutil

import (
	"os/exec"
	"sync/atomic"
	"syscall"

	"golang.org/x/sys/windows"
)

// Win32 CREATE_NO_WINDOW suppresses the child's console allocation.
const createNoWindow = 0x08000000

// HideConsoleWindow suppresses the child's console window on Windows.
//
// Do not apply this to a process attached to a ConPTY.
// CREATE_NO_WINDOW prevents the pseudo console from becoming the child's console.
// The process then has no console, and its standard I/O can stop.
func HideConsoleWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}

// DetachFromTerminal does nothing on Windows.
// Windows has no POSIX controlling terminal or job-control signal (SIGTTIN).
func DetachFromTerminal(*exec.Cmd) {}

// JobObject wraps a Win32 job configured with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE.
// The job owns its assigned process and that process's descendants.
// Closing the handle or calling Terminate ends those processes.
// Windows has no equivalent of Unix process-group signals.
type JobObject struct {
	handle atomic.Uintptr // windows.Handle; zero once Close has released it
}

// Terminate kills every process in the job and releases the handle.
// It consumes the handle atomically so a concurrent Close cannot invalidate it.
// Later Terminate and Close calls do nothing. It accepts a nil receiver.
func (j *JobObject) Terminate() error {
	if j == nil {
		return nil
	}
	raw := j.handle.Swap(0)
	if raw == 0 {
		return nil
	}
	return terminateOwnedJob(raw, windowsJobDriver{})
}

// Close releases the kernel handle.
// KILL_ON_JOB_CLOSE ends any surviving processes when the last handle closes.
// It accepts a nil receiver. Later Close or Terminate calls do nothing.
func (j *JobObject) Close() error {
	if j == nil {
		return nil
	}
	raw := j.handle.Swap(0)
	if raw == 0 {
		return nil
	}
	return windows.CloseHandle(windows.Handle(raw))
}

// SignalProcessGroup signals the child process. Windows has no POSIX
// process-group signal. The job that the owner attaches after Start ends descendants.
func SignalProcessGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Signal(sig)
}
