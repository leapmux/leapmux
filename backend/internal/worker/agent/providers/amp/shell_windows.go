//go:build windows

package amp

import (
	"errors"
	"math"

	"golang.org/x/sys/windows"
)

// ampStillActive is the Windows exit code for a running process (STILL_ACTIVE),
// used only as a fallback when SYNCHRONIZE access is denied.
const ampStillActive = 259

// ampProcessAlive reports whether the local process identified by pid is still alive.
// It uses WaitForSingleObject(handle, 0) so a process exiting with code 259 is not
// misclassified as still running. Access-denied and kernel query errors return true
// conservatively so an unreadable process is not closed while it may still run.
func ampProcessAlive(pid int) bool {
	if pid <= 0 || pid > math.MaxUint32 {
		return false
	}
	pid32 := uint32(pid)
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, pid32)
	if err == nil {
		defer func() { _ = windows.CloseHandle(handle) }()
		event, waitErr := windows.WaitForSingleObject(handle, 0)
		if waitErr != nil {
			return true
		}
		return event == uint32(windows.WAIT_TIMEOUT)
	}
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return false
	}
	handle, err = windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid32)
	if err != nil {
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	var code uint32
	if err := windows.GetExitCodeProcess(handle, &code); err != nil {
		return true
	}
	return code == ampStillActive
}
