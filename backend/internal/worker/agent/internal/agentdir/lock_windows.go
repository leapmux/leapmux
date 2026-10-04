//go:build windows

package agentdir

import (
	"errors"
	"io/fs"
	"os"

	"golang.org/x/sys/windows"
)

// openLockFile opens the lock file at path for reading and writing, and
// creates it when it does not exist. exclusive fails when it exists.
//
// It calls CreateFile itself for three properties:
//
//   - The handle is not inheritable, so no process that the worker starts
//     keeps it, and with it the lock.
//   - The share mode admits a delete, so another process can remove the lock
//     file while this handle is open.
//   - FILE_FLAG_OPEN_REPARSE_POINT opens a link itself and not its target, so
//     a link in place of the lock file fails the check of stillAt.
func openLockFile(path string, exclusive bool) (*os.File, error) {
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
	return os.NewFile(uintptr(handle), path), nil
}

// tryLock takes an exclusive lock on the first byte of file without waiting.
// It reports false when another handle holds the lock.
func tryLock(file *os.File) (bool, error) {
	var overlapped windows.Overlapped
	err := windows.LockFileEx(windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return err == nil, err
}

// unlock releases the lock on file. Windows releases it when the handle
// closes too, but the documentation leaves the moment of that release to the
// system, so removeLocked unlocks first.
func unlock(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlapped)
}
