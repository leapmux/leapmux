//go:build linux

package procutil

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func openNativeProcessExitWatch(pid int) (*processExitWatch, error) {
	fd, err := unix.PidfdOpen(pid, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil, os.ErrProcessDone
	}
	if err != nil {
		return nil, err
	}
	return &processExitWatch{
		poll: func(wait time.Duration) (bool, error) {
			events := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			milliseconds := int((wait + time.Millisecond - 1) / time.Millisecond)
			_, err := unix.Poll(events, milliseconds)
			if errors.Is(err, unix.EINTR) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			if events[0].Revents&(unix.POLLERR|unix.POLLNVAL) != 0 {
				return false, errors.New("the native process exit descriptor failed")
			}
			return events[0].Revents&(unix.POLLIN|unix.POLLHUP) != 0, nil
		},
		release: func() error { return unix.Close(fd) },
	}, nil
}
