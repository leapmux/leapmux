//go:build windows

package procutil

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

var isProcessInJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

// CheckNoJob refuses a process that belongs to any Windows Job or whose Job
// membership cannot be verified. Call it in the child after requesting breakaway.
func CheckNoJob() error {
	return validateWindowsProcessJob(func() (bool, error) {
		if err := isProcessInJob.Find(); err != nil {
			return false, err
		}
		var inJob int32
		result, _, _ := isProcessInJob.Call(uintptr(windows.CurrentProcess()), 0, uintptr(unsafe.Pointer(&inJob)))
		if result == 0 {
			return false, errors.New("Windows Job membership could not be queried")
		}
		return inJob != 0, nil
	})
}

func validateWindowsProcessJob(query func() (bool, error)) error {
	// Nested breakaway can succeed while an ancestor Job still owns the child.
	// A null Job argument above asks about every Job, without acquiring a handle.
	inJob, err := query()
	if err != nil || inJob {
		return errors.New("the process could not establish an independent Windows Job scope")
	}
	return nil
}
