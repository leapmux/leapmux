//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package procutil

import (
	"errors"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func openNativeProcessExitWatch(pid int) (*processExitWatch, error) {
	fd, err := unix.Kqueue()
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(fd)
	var change unix.Kevent_t
	unix.SetKevent(&change, pid, unix.EVFILT_PROC, unix.EV_ADD|unix.EV_ONESHOT)
	change.Fflags = unix.NOTE_EXIT
	_, err = unix.Kevent(fd, []unix.Kevent_t{change}, nil, nil)
	if err != nil {
		closeErr := unix.Close(fd)
		if errors.Is(err, unix.ESRCH) && closeErr == nil {
			return nil, os.ErrProcessDone
		}
		return nil, errors.Join(err, closeErr)
	}
	return &processExitWatch{
		poll: func(wait time.Duration) (bool, error) {
			events := make([]unix.Kevent_t, 1)
			timeout := unix.NsecToTimespec(wait.Nanoseconds())
			count, err := unix.Kevent(fd, nil, events, &timeout)
			if errors.Is(err, unix.EINTR) {
				return false, nil
			}
			if err != nil || count == 0 {
				return false, err
			}
			event := events[0]
			if event.Flags&unix.EV_ERROR != 0 {
				return false, syscall.Errno(event.Data)
			}
			if uint64(event.Ident) != uint64(pid) || event.Fflags&unix.NOTE_EXIT == 0 {
				return false, errors.New("the native process exit event identifies another process")
			}
			return true, nil
		},
		release: func() error { return unix.Close(fd) },
	}, nil
}
