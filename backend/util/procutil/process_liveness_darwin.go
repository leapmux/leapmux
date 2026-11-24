//go:build darwin

package procutil

import (
	"errors"
	"math"
	"syscall"

	"golang.org/x/sys/unix"
)

// darwinSZOMB is the Darwin process state constant SZOMB from <sys/proc.h>.
const darwinSZOMB = 5

// InspectLiveProcess checks liveness (excluding zombies) and extracts an opaque
// start token in a single kern.proc.pid sysctl call.
func InspectLiveProcess(pid int) LiveProcessInfo {
	if pid <= 0 || pid > math.MaxInt32 {
		return LiveProcessInfo{}
	}
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return LiveProcessInfo{}
		}
		// SysctlKinfoProc returns EIO when a vanished PID yields an empty record.
		if errors.Is(err, syscall.EIO) {
			killErr := syscall.Kill(pid, 0)
			return LiveProcessInfo{Alive: killErr == nil || errors.Is(killErr, syscall.EPERM)}
		}
		return LiveProcessInfo{Alive: true}
	}
	if kp.Proc.P_stat == darwinSZOMB {
		return LiveProcessInfo{}
	}
	start := int64(kp.Proc.P_starttime.Sec)*1_000_000 + int64(kp.Proc.P_starttime.Usec)
	if start > 0 {
		return LiveProcessInfo{Alive: true, StartToken: start}
	}
	return LiveProcessInfo{Alive: true}
}
