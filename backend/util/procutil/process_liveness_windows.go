//go:build windows

package procutil

import (
	"errors"
	"math"

	"golang.org/x/sys/windows"
)

// InspectLiveProcess checks liveness and reads the process creation FILETIME
// ticks from a single process handle when available.
//
// It prefers WaitForSingleObject(handle, 0) so a process exiting with code 259
// (STILL_ACTIVE) is not misclassified as still running. When SYNCHRONIZE access
// is denied, it falls back to GetExitCodeProcess. Access-denied and kernel
// query errors return Alive: true conservatively so an unreadable process is
// not reported as exited while it may still run.
func InspectLiveProcess(pid int) LiveProcessInfo {
	if pid <= 0 || uint64(pid) > math.MaxUint32 {
		return LiveProcessInfo{}
	}
	pid32 := uint32(pid)
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, pid32)
	if err == nil {
		defer func() { _ = windows.CloseHandle(handle) }()
		event, waitErr := windows.WaitForSingleObject(handle, 0)
		if waitErr == nil && event != uint32(windows.WAIT_TIMEOUT) {
			return LiveProcessInfo{}
		}
		return LiveProcessInfo{Alive: true, StartToken: windowsProcessStartToken(handle)}
	}
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return LiveProcessInfo{}
	}
	handle, err = windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid32)
	if err != nil {
		return LiveProcessInfo{Alive: errors.Is(err, windows.ERROR_ACCESS_DENIED)}
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	var code uint32
	if err := windows.GetExitCodeProcess(handle, &code); err == nil && code != stillActive {
		return LiveProcessInfo{}
	}
	return LiveProcessInfo{Alive: true, StartToken: windowsProcessStartToken(handle)}
}

func windowsProcessStartToken(handle windows.Handle) int64 {
	var creation, exit, kernel, user windows.Filetime
	if windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user) != nil {
		return 0
	}
	raw := (uint64(creation.HighDateTime) << 32) | uint64(creation.LowDateTime)
	if raw == 0 || raw > math.MaxInt64 {
		return 0
	}
	return int64(raw)
}
