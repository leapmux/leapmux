package procutil

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

var peekPipe = windows.NewLazySystemDLL("kernel32.dll").NewProc("PeekNamedPipe")

// DuplicatePipeReader retains a private read end and closes the original.
func DuplicatePipeReader(reader *os.File, closeOriginal func() error) (*os.File, error) {
	conn, err := reader.SyscallConn()
	if err != nil {
		return nil, errors.Join(err, closeOriginal())
	}
	var duplicate windows.Handle
	var duplicateErr error
	if err := conn.Control(func(handle uintptr) {
		process := windows.CurrentProcess()
		duplicateErr = windows.DuplicateHandle(process, windows.Handle(handle), process, &duplicate, 0, false, windows.DUPLICATE_SAME_ACCESS)
	}); err != nil {
		return nil, errors.Join(err, closeOriginal())
	}
	if duplicateErr != nil {
		return nil, errors.Join(duplicateErr, closeOriginal())
	}
	// NewFile can inspect synchronous I/O state, which requires the original
	// handle to finish its outstanding reads first.
	closeErr := closeOriginal()
	return os.NewFile(uintptr(duplicate), reader.Name()), closeErr
}

// PipeReadState has no concurrent reads on this handle. PeekNamedPipe can
// block on a synchronous pipe with another read in progress.
func PipeReadState(reader *os.File) (int, bool, error) {
	conn, err := reader.SyscallConn()
	if err != nil {
		return 0, false, err
	}
	var count uint32
	var peekErr error
	if err := conn.Control(func(handle uintptr) {
		ok, _, err := peekPipe.Call(handle, 0, 0, 0, uintptr(unsafe.Pointer(&count)), 0)
		if ok == 0 {
			peekErr = err
		}
	}); err != nil {
		return 0, false, err
	}
	if errors.Is(peekErr, windows.ERROR_BROKEN_PIPE) || errors.Is(peekErr, windows.ERROR_PIPE_NOT_CONNECTED) {
		return 0, true, nil
	}
	return int(count), false, peekErr
}
