//go:build !linux && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd && !windows

package procutil

import "errors"

func openNativeProcessExitWatch(int) (*processExitWatch, error) {
	return nil, errors.New("this operating system supplies no process exit watch")
}
