//go:build windows

package atomicfile

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// The sharing-conflict retry budget for the durable replace matches the
// atomic one: a wall-clock deadline with a capped per-attempt sleep. See
// replace_windows.go for why the budget exists and which failures are
// permanent.
const (
	durableReplaceDeadline = 2 * time.Second
	durableReplaceSleepCap = 100 * time.Millisecond
)

// moveFileExDurable is the native move call, a variable so a test can
// observe the flags and failures the durable replace passes through.
var moveFileExDurable = windows.MoveFileEx

// replaceFileDurable replaces newpath with oldpath through MoveFileExW
// carrying MOVEFILE_REPLACE_EXISTING | MOVEFILE_WRITE_THROUGH. The
// write-through flag is what makes this the durable variant: the system
// returns only after the rename reached the disk. There is no copy
// fallback, and a sharing conflict is retried exactly as the atomic
// replace retries it.
func replaceFileDurable(oldpath, newpath string) error {
	from, err := windows.UTF16PtrFromString(oldpath)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(newpath)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(durableReplaceDeadline)
	sleep := time.Millisecond
	for {
		err := moveFileExDurable(from, to,
			windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
		if err == nil {
			return nil
		}
		if destCannotReplace(newpath) || !isSharingConflict(err) {
			return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: err}
		}
		if !time.Now().Before(deadline) {
			return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: err}
		}
		time.Sleep(sleep)
		if sleep < durableReplaceSleepCap {
			sleep *= 2
			if sleep > durableReplaceSleepCap {
				sleep = durableReplaceSleepCap
			}
		}
	}
}

// publishFileEntry is a no-op on Windows: the write-through replacement
// above is the documented persistence operation, and a directory handle
// has no flush to add to it.
func publishFileEntry(string) error { return nil }

func (defaultFileOps) syncDir(string) error { return nil }

// renameNoReplace renames oldpath to newpath and fails when newpath
// already exists. MoveFileExW with neither replacement nor copy flags
// publishes the move write-through, so the subtree becomes visible only
// once its metadata reached the disk.
func renameNoReplace(oldpath, newpath string) error {
	from, err := windows.UTF16PtrFromString(oldpath)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(newpath)
	if err != nil {
		return err
	}
	if err := moveFileExDurable(from, to, windows.MOVEFILE_WRITE_THROUGH); err != nil {
		if errors.Is(err, windows.ERROR_ALREADY_EXISTS) || errors.Is(err, windows.ERROR_FILE_EXISTS) {
			return os.ErrExist
		}
		return &os.LinkError{Op: "renameat", Old: oldpath, New: newpath, Err: err}
	}
	return nil
}

// currentAccountID reports the current account's security identifier, the
// Windows spelling of the identity a publication record pins.
func currentAccountID() string {
	token := windows.GetCurrentProcessToken()
	sid, err := token.GetTokenUser()
	if err != nil {
		return ""
	}
	return sid.User.Sid.String()
}
