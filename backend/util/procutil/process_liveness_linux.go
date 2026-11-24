//go:build linux

package procutil

import (
	"errors"
	"math"
	"os"
	"strconv"
	"syscall"
)

// InspectLiveProcess checks liveness (excluding zombies and dead tasks) and
// extracts the kernel starttime ticks from /proc/<pid>/stat in a single file
// read.
func InspectLiveProcess(pid int) LiveProcessInfo {
	return inspectLinuxLiveProcess(pid, os.ReadFile, func(p int) error {
		return syscall.Kill(p, 0)
	})
}

func inspectLinuxLiveProcess(pid int, readStat func(string) ([]byte, error), killZero func(int) error) LiveProcessInfo {
	if pid <= 0 || pid > math.MaxInt32 {
		return LiveProcessInfo{}
	}
	data, err := readStat("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		// Under hidepid=2, reading another owner's /proc/<pid>/stat returns ENOENT
		// even though kill(pid, 0) returns EPERM; keep returning Alive: true
		// conservatively.
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
			return LiveProcessInfo{Alive: errors.Is(killZero(pid), syscall.EPERM)}
		}
		return LiveProcessInfo{Alive: true}
	}
	return parseLinuxLiveProcessStat(data)
}
