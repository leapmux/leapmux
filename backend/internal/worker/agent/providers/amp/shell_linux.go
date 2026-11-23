//go:build linux

package amp

import (
	"errors"
	"math"
	"os"
	"strconv"
	"syscall"
)

// ampProcessAlive reports whether the local process identified by pid is still alive
// and not a zombie or dead task. EPERM from kill(pid, 0) indicates the process exists
// under another owner.
func ampProcessAlive(pid int) bool {
	if pid <= 0 || pid > math.MaxInt32 {
		return false
	}
	killErr := syscall.Kill(pid, 0)
	if killErr != nil && !errors.Is(killErr, syscall.EPERM) {
		return false
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		// Under hidepid=2, reading another owner's /proc/<pid>/stat returns ENOENT
		// even though kill(pid, 0) returned EPERM; keep returning true conservatively.
		if (errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH)) && !errors.Is(killErr, syscall.EPERM) {
			return false
		}
		return true
	}
	return ampLinuxStatAlive(data)
}
