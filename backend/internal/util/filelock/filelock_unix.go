//go:build unix

// Package filelock provides the platform-native advisory lock every
// LeapMux directory ownership scheme builds on: one open file
// description, one exclusive lock, no inheritance across exec.
package filelock

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// Open opens the lock file at path for reading and writing, and creates
// it when it does not exist. exclusive fails with fs.ErrExist when it
// exists. Go opens every file close-on-exec, so no process that a
// holder starts inherits the descriptor, and with it the lock.
// O_NOFOLLOW refuses a link in place of the lock file.
//
// Open itself rejects a symlink or non-regular file at path before the
// caller can lock or modify the target: a lock is an ownership claim on
// one regular file, and a caller that had to re-check that after every
// open would eventually skip it.
func Open(path string, exclusive bool) (*os.File, error) {
	flags := os.O_RDWR | os.O_CREATE | unix.O_NOFOLLOW
	if exclusive {
		flags |= os.O_EXCL
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		// O_NOFOLLOW reports ELOOP; map it to the fs.ErrNotExist the
		// callers of a fresh creation path already handle, and give
		// everyone else a recognizable refusal.
		return nil, err
	}
	if err := requireRegular(f, path); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// requireRegular rejects everything but a regular file on the handle.
func requireRegular(f *os.File, path string) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("filelock: %s is not a regular file", path)
	}
	return nil
}

// TryLock takes an exclusive flock on file without waiting. It reports
// false when another open file holds the lock.
func TryLock(file *os.File) (bool, error) {
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

// Unlock releases the flock on file. Close releases it too; the explicit
// release keeps a remover's order the same on each platform.
func Unlock(file *os.File) error {
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
