//go:build !windows

package terminal

import (
	"errors"
	"syscall"
)

// processGroupOf returns pid's process group, and how to read a failure.
//
// One syscall, and only for a process the walk already found beneath the shell
// -- never for the whole process table.
//
// getpgid has exactly ONE documented error: ESRCH, "no process whose process ID
// equals pid". It does not refuse another user's process, so a failure here says
// the process is gone rather than that the answer is unavailable. Any other
// errno is unexpected, and reportsAsWork treats it the way it treats Windows --
// as a question the OS refused, which must not silence the guard.
func processGroupOf(pid int) (int, processGroupResult) {
	group, err := syscall.Getpgid(pid)
	if err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return 0, processGroupGone
		}
		return 0, processGroupRefused
	}
	return group, processGroupFound
}
