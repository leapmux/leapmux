//go:build unix && !linux && !darwin

package procutil

import (
	"errors"
	"math"
	"syscall"
)

// InspectLiveProcess checks liveness with kill(pid, 0) and reads the process
// start time as an opaque start token when available.
func InspectLiveProcess(pid int) LiveProcessInfo {
	if pid <= 0 || pid > math.MaxInt32 {
		return LiveProcessInfo{}
	}
	err := syscall.Kill(pid, 0)
	if err != nil && !errors.Is(err, syscall.EPERM) {
		return LiveProcessInfo{}
	}
	if ident, ok := IdentifyProcess(pid); ok && ident.StartTime > 0 {
		return LiveProcessInfo{Alive: true, StartToken: ident.StartTime}
	}
	return LiveProcessInfo{Alive: true}
}
