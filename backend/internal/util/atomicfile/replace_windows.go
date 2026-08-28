//go:build windows

package atomicfile

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// replaceAttempts is how many times a sharing conflict is retried.
// Concurrent writers of one credential file hit ERROR_ACCESS_DENIED or
// ERROR_SHARING_VIOLATION because Windows will not replace a destination
// that still has an open handle. The conflict is transient: the other
// writer's rename finishes and this one can replace. A directory or a
// read-only destination is permanent and is not retried.
const replaceAttempts = 8

func replaceFile(oldpath, newpath string) error {
	var err error
	for i := range replaceAttempts {
		err = os.Rename(oldpath, newpath)
		if err == nil {
			return nil
		}
		if destCannotReplace(newpath) || !isSharingConflict(err) {
			return err
		}
		if i+1 < replaceAttempts {
			time.Sleep(time.Millisecond << uint(i))
		}
	}
	return err
}

func destCannotReplace(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if info.IsDir() {
		return true
	}
	// Windows maps FILE_ATTRIBUTE_READONLY to a mode with no write bit.
	return info.Mode().Perm()&0o200 == 0
}

func isSharingConflict(err error) bool {
	var link *os.LinkError
	if errors.As(err, &link) {
		err = link.Err
	}
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) ||
		errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
		errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}
