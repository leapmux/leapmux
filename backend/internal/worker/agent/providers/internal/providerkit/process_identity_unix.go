//go:build unix

package providerkit

import (
	"errors"
	"syscall"
)

// ProcessRuns reports whether a process with pid runs. A process of another
// user answers EPERM, and it runs too. A process that ended and that its
// parent did not reap yet runs too, because it still holds its pid.
func ProcessRuns(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
