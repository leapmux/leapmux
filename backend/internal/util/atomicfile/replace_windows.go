//go:build windows

package atomicfile

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// The sharing-conflict retry budget: a wall-clock deadline with a capped
// per-attempt sleep, not a fixed attempt count. Concurrent writers of one
// credential file hit ERROR_ACCESS_DENIED or ERROR_SHARING_VIOLATION because
// Windows will not replace a destination that still has an open handle; the
// conflict is transient, and an antivirus or search indexer holding the
// destination routinely outlasts a short budget. The exponential backoff
// starts at 1ms and caps at replaceSleepCap so the tail stays even, and the
// whole window is capped by replaceDeadline. A directory or a read-only
// destination is permanent and is not retried.
const (
	replaceDeadline = 2 * time.Second
	replaceSleepCap = 100 * time.Millisecond
)

func replaceFile(oldpath, newpath string) error {
	deadline := time.Now().Add(replaceDeadline)
	sleep := time.Millisecond
	var err error
	for {
		err = os.Rename(oldpath, newpath)
		if err == nil {
			return nil
		}
		if destCannotReplace(newpath) || !isSharingConflict(err) {
			return err
		}
		if !time.Now().Before(deadline) {
			return err
		}
		time.Sleep(sleep)
		if sleep < replaceSleepCap {
			sleep *= 2
			if sleep > replaceSleepCap {
				sleep = replaceSleepCap
			}
		}
	}
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
