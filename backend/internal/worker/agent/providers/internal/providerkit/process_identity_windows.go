//go:build windows

package providerkit

import (
	"errors"

	"golang.org/x/sys/windows"
)

// stillActive is the exit code that Windows reports for a process that runs.
const stillActive = 259

// ProcessRuns reports whether a process with pid runs. A process that the
// worker may not open runs too. A process that ended does not run, although
// an open handle keeps its pid.
func ProcessRuns(pid int) bool {
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
	return code == stillActive
}
