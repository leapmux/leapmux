//go:build darwin

package amp

import (
	"errors"
	"math"
	"syscall"

	"golang.org/x/sys/unix"
)

// ampDarwinSZOMB is the Darwin process state constant SZOMB from <sys/proc.h>.
const ampDarwinSZOMB = 5

// ampProcessAlive reports whether the local process identified by pid is still alive
// and not a zombie. EPERM from kill(pid, 0) indicates the process exists under another owner.
func ampProcessAlive(pid int) bool {
	if pid <= 0 || pid > math.MaxInt32 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil && !errors.Is(err, syscall.EPERM) {
		return false
	}
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return false
		}
		// SysctlKinfoProc returns EIO when a vanished PID yields an empty record.
		if errors.Is(err, syscall.EIO) {
			killErr := syscall.Kill(pid, 0)
			return killErr == nil || errors.Is(killErr, syscall.EPERM)
		}
		return true
	}
	return kp.Proc.P_stat != ampDarwinSZOMB
}
