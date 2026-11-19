//go:build darwin || linux

package procutil

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// DuplicatePipeReader retains a private read end and closes the original.
func DuplicatePipeReader(reader *os.File, closeOriginal func() error) (*os.File, error) {
	conn, err := reader.SyscallConn()
	if err != nil {
		return nil, errors.Join(err, closeOriginal())
	}
	var descriptor int
	var duplicateErr error
	if err := conn.Control(func(handle uintptr) {
		descriptor, duplicateErr = unix.FcntlInt(handle, unix.F_DUPFD_CLOEXEC, 0)
	}); err != nil {
		return nil, errors.Join(err, closeOriginal())
	}
	if duplicateErr != nil {
		return nil, errors.Join(duplicateErr, closeOriginal())
	}
	closeErr := closeOriginal()
	return os.NewFile(uintptr(descriptor), reader.Name()), closeErr
}

// PipeReadState reports how many bytes are queued and whether every writer
// has closed. The caller must prevent other readers from taking the bytes.
func PipeReadState(reader *os.File) (count int, atEOF bool, resultErr error) {
	conn, err := reader.SyscallConn()
	if err != nil {
		return 0, false, err
	}
	if err := conn.Control(func(handle uintptr) {
		count, atEOF, resultErr = inspectPipeReadState(int(handle), unix.IoctlGetInt, unix.Poll)
	}); err != nil {
		return 0, false, err
	}
	return count, atEOF, resultErr
}

func inspectPipeReadState(fd int, ioctl func(int, uint) (int, error), poll func([]unix.PollFd, int) (int, error)) (int, bool, error) {
	count, err := retryInterruptedPipeQuery(func() (int, error) { return ioctl(fd, pipeBytesAvailable) })
	if err != nil {
		return 0, false, err
	}
	descriptors := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	_, err = retryInterruptedPipeQuery(func() (int, error) { return poll(descriptors, 0) })
	return count, descriptors[0].Revents&unix.POLLHUP != 0, err
}

func retryInterruptedPipeQuery(query func() (int, error)) (int, error) {
	for {
		value, err := query()
		if !errors.Is(err, unix.EINTR) {
			return value, err
		}
	}
}
