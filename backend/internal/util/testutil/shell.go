package testutil

import (
	"os"
	"runtime"
)

// TestShell returns a shell binary suitable for spawning a real PTY in
// tests. On Windows it prefers %COMSPEC% (typically cmd.exe) so the test
// works on both classic Windows and stripped-down container images. On
// Unix it returns /bin/sh, which is part of the POSIX baseline.
func TestShell() string {
	if runtime.GOOS == "windows" {
		if shell := os.Getenv("COMSPEC"); shell != "" {
			return shell
		}
		return "cmd.exe"
	}
	return "/bin/sh"
}

// TestShellEnter returns the line terminator that the test shell needs to
// commit a command. cmd.exe via ConPTY only treats CR (\r) as Enter, while
// POSIX shells accept LF (\n).
func TestShellEnter() string {
	if runtime.GOOS == "windows" {
		return "\r"
	}
	return "\n"
}
