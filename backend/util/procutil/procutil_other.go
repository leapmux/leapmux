//go:build !windows

// Package procutil contains small helpers for configuring child processes.
package procutil

import "os/exec"

// HideConsoleWindow suppresses the child's console window on Windows;
// no-op on Unix.
func HideConsoleWindow(*exec.Cmd) {}
