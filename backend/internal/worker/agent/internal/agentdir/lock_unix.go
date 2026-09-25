//go:build unix

package agentdir

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// openLockFile opens the lock file at path for reading and writing, and
// creates it when it does not exist. exclusive fails when it exists. Go opens
// every file close-on-exec, so no process that the worker starts inherits the
// descriptor, and with it the lock. O_NOFOLLOW refuses a link in place of the
// lock file.
func openLockFile(path string, exclusive bool) (*os.File, error) {
	flags := os.O_RDWR | os.O_CREATE | unix.O_NOFOLLOW
	if exclusive {
		flags |= os.O_EXCL
	}
	return os.OpenFile(path, flags, 0o600)
}

// tryLock takes an exclusive flock on file without waiting. It reports false
// when another open file holds the lock.
func tryLock(file *os.File) (bool, error) {
	raw, err := file.SyscallConn()
	if err != nil {
		return false, err
	}
	var lockErr error
	if err := raw.Control(func(fd uintptr) {
		for {
			lockErr = unix.Flock(int(fd), unix.LOCK_EX|unix.LOCK_NB)
			if !errors.Is(lockErr, unix.EINTR) {
				return
			}
		}
	}); err != nil {
		return false, err
	}
	if errors.Is(lockErr, unix.EWOULDBLOCK) {
		return false, nil
	}
	return lockErr == nil, lockErr
}

// unlock releases the flock on file. Close releases it too; the explicit
// release keeps the order of removeLocked the same on each platform.
func unlock(file *os.File) error {
	raw, err := file.SyscallConn()
	if err != nil {
		return err
	}
	var unlockErr error
	if err := raw.Control(func(fd uintptr) {
		unlockErr = unix.Flock(int(fd), unix.LOCK_UN)
	}); err != nil {
		return err
	}
	return unlockErr
}
