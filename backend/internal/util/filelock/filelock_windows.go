//go:build windows

package filelock

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"golang.org/x/sys/windows"
)

// Open opens the lock file at path for reading and writing, and creates
// it when it does not exist. exclusive fails with fs.ErrExist when it
// exists.
//
// It calls CreateFile itself for three properties:
//
//   - The handle is not inheritable, so no process that a holder starts
//     keeps it, and with it the lock.
//   - The share mode admits a delete, so another process can remove the
//     lock file while this handle is open.
//   - FILE_FLAG_OPEN_REPARSE_POINT opens a link itself and not its
//     target, so Open can reject a reparse point or any non-regular
//     object at path before the caller can lock or modify the target.
func Open(path string, exclusive bool) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	disposition := uint32(windows.OPEN_ALWAYS)
	if exclusive {
		disposition = windows.CREATE_NEW
	}
	handle, err := windows.CreateFile(name,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, disposition,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(handle), path)
	info, statErr := f.Stat()
	if statErr != nil {
		_ = f.Close()
		return nil, statErr
	}
	// A handle opened through the reparse point itself reports the link,
	// which Go marks non-regular; a directory or device fails the same
	// check.
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("filelock: %s is not a regular file", path)
	}
	return f, nil
}

// TryLock takes an exclusive lock on the first byte of file without
// waiting. It reports false when another handle holds the lock.
func TryLock(file *os.File) (bool, error) {
	var overlapped windows.Overlapped
	err := windows.LockFileEx(windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return err == nil, err
}

// Unlock releases the lock on file. Windows releases it when the handle
// closes too, but the documentation leaves the moment of that release
// to the system, so a remover unlocks first.
func Unlock(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlapped)
}
