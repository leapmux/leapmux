//go:build unix && !linux && !darwin

package amp

import (
	"errors"
	"math"
	"syscall"
)

// ampProcessAlive reports whether the local process identified by pid is still alive.
// EPERM indicates the process exists under another owner.
func ampProcessAlive(pid int) bool {
	if pid <= 0 || pid > math.MaxInt32 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
