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

// TestSleepCommand returns a command line that keeps a child process alive for
// about a minute in the test shell, plus the process name the OS reports for
// it. Used by the terminal descendant-walk tests, which need a child that
// outlives the assertion.
//
// The POSIX form backgrounds the job so the shell keeps its prompt, which also
// makes it the case that matters: a backgrounded process is invisible to a
// foreground-process-group probe and is exactly the work a user would lose
// without a warning.
func TestSleepCommand() (line, name string) {
	if runtime.GOOS == "windows" {
		return "start /b ping -n 60 127.0.0.1 > NUL", "ping"
	}
	return "sleep 60 &", "sleep"
}

// TestNestedSleepCommand returns a command line that puts a GRANDCHILD under
// the test shell, plus the name of that grandchild. This is the `make` -> `cc`
// shape: an answer that stopped at depth 1 would name the wrapper and miss the
// process doing the actual work.
//
// The returned name is the DEEPEST process, deliberately. Finding it is what
// proves the walk reached depth 2, and it is the only name stable across
// platforms: the intermediate shell reports whatever the OS calls it, and on
// macOS `/bin/sh` reports "bash", because there /bin/sh IS bash in POSIX mode.
func TestNestedSleepCommand() (line, grandchildName string) {
	if runtime.GOOS == "windows" {
		return `start /b cmd /c "ping -n 60 127.0.0.1 & rem"`, "ping"
	}
	// The trailing `; true` is load-bearing. `sh -c 'sleep 60'` is a SINGLE
	// simple command, which every POSIX shell optimizes by exec'ing it in place
	// -- the intermediate shell replaces itself with sleep, and the grandchild
	// this helper exists to create never appears. A compound command defeats
	// that, so the shell stays and forks.
	return "/bin/sh -c 'sleep 60; true' &", "sleep"
}
