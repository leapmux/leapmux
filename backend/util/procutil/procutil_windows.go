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
func HideConsoleWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
