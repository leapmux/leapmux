//go:build windows

// Package procutil contains small helpers for configuring child processes.
package procutil

import (
	"os/exec"
	"syscall"
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
