//go:build windows

package amp

import (
	"errors"

	"golang.org/x/sys/windows"
)

// ampStillActive is the Windows exit code for a running process (STILL_ACTIVE).
const ampStillActive = 259

// ampProcessAlive reports whether the local process identified by pid is still alive.
// Access-denied and GetExitCodeProcess errors return true conservatively so an
// unreadable process is not closed while it may still run.
func ampProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
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
