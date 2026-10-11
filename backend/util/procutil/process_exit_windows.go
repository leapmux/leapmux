//go:build windows

package procutil

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

func openNativeProcessExitWatch(pid int) (*processExitWatch, error) {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return nil, os.ErrProcessDone
	}
	if err != nil {
		return nil, err
	}
	return &processExitWatch{
		poll: func(wait time.Duration) (bool, error) {
			milliseconds := uint32((wait + time.Millisecond - 1) / time.Millisecond)
			event, err := windows.WaitForSingleObject(handle, milliseconds)
			if err != nil {
				return false, err
			}
			switch event {
			case windows.WAIT_OBJECT_0:
				return true, nil
			case uint32(windows.WAIT_TIMEOUT):
				return false, nil
			default:
				return false, errors.New("the native process handle returned an invalid wait result")
			}
		},
		release: func() error { return windows.CloseHandle(handle) },
	}, nil
}
