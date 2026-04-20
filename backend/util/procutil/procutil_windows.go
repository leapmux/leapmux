//go:build windows

// Package procutil contains small helpers for configuring child processes.
package procutil

import (
	"fmt"
	"os/exec"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Win32 CREATE_NO_WINDOW — suppress the child's console allocation.
const createNoWindow = 0x08000000

// HideConsoleWindow suppresses the child's console window on Windows.
//
// Note: do NOT apply this to a process attached to a ConPTY — CREATE_NO_WINDOW
// prevents the pseudo console from becoming the child's console, leaving the
// process with no console at all and hanging stdio.
func HideConsoleWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}

// JobObject wraps a Win32 job object configured with
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE. Assigning a process to the job puts
// the process and its descendants under a single kill group: closing the
// handle (or calling Terminate) terminates the whole tree, which is the
// only reliable way to avoid orphaned grandchildren on Windows since
// there is no equivalent of Unix's process-group signalling.
type JobObject struct {
	handle atomic.Uintptr // windows.Handle; zero once Close has released it
}

// AssignCmd creates a kill-on-close job object and assigns cmd.Process to it.
// Must be called after cmd.Start has succeeded. A nil *JobObject is returned
// only when creation itself failed; callers may still invoke Terminate/Close
// on a nil receiver safely.
func AssignCmd(cmd *exec.Cmd) (*JobObject, error) {
	return AssignPID(cmd.Process.Pid)
}

// AssignPID creates a kill-on-close job object and assigns the process with
// the given PID to it. Equivalent to AssignCmd but usable when the caller
// holds only the PID (e.g. go-pty's pty.Cmd doesn't expose *exec.Cmd).
func AssignPID(pid int) (*JobObject, error) {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("CreateJobObject: %w", err)
	}

	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(
		h,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(h)
		return nil, fmt.Errorf("SetInformationJobObject: %w", err)
	}

	procHandle, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(pid),
	)
	if err != nil {
		_ = windows.CloseHandle(h)
		return nil, fmt.Errorf("OpenProcess: %w", err)
	}
	defer windows.CloseHandle(procHandle)

	if err := windows.AssignProcessToJobObject(h, procHandle); err != nil {
		_ = windows.CloseHandle(h)
		return nil, fmt.Errorf("AssignProcessToJobObject: %w", err)
	}

	j := &JobObject{}
	j.handle.Store(uintptr(h))
	return j, nil
}

// Terminate kills every process currently in the job and releases the
// handle. Consumes the handle atomically so a concurrent Close cannot
// invalidate it mid-call; subsequent Terminate/Close are no-ops. Safe on
// nil receiver.
func (j *JobObject) Terminate() error {
	if j == nil {
		return nil
	}
	raw := j.handle.Swap(0)
	if raw == 0 {
		return nil
	}
	h := windows.Handle(raw)
	termErr := windows.TerminateJobObject(h, 1)
	closeErr := windows.CloseHandle(h)
	if termErr != nil {
		return termErr
	}
	return closeErr
}

// Close releases the kernel handle. Because the job was created with
// KILL_ON_JOB_CLOSE, the kernel tears down any surviving processes when the
// last handle is closed. Safe on nil receiver and idempotent; a no-op if
// Terminate has already run.
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
