package agentdir

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// lockFileName is the lock file of each agent directory. The worker that owns
// the directory holds an exclusive lock on it.
const lockFileName = ".owner.lock"

// Dir is the private directory of one agent. The worker holds its lock until
// Close.
type Dir struct {
	path string
	lock *lockFile

	closeOnce sync.Once
	closeErr  error
}

// Path returns the directory's path.
func (d *Dir) Path() string {
	return d.path
}

// Close removes the directory and everything in it, and releases its lock. It
// is safe to call more than once and on a nil Dir. Later calls return the
// result of the first.
func (d *Dir) Close() error {
	if d == nil {
		return nil
	}
	d.closeOnce.Do(func() {
		d.closeErr = removeLocked(d.path, d.lock)
	})
	return d.closeErr
}

// removeLocked removes dir, whose lock the caller holds, and releases the lock.
//
// It removes every entry except the lock file first, so the directory stays
// locked while it empties. Then it releases the lock, and last it removes the
// lock file and the directory. Windows cannot remove a file that a handle
// holds open unless that handle allows it, and a directory that holds such a
// file is not empty. So the lock goes before the lock file on each platform.
// The sweep of another worker can take the free lock in the moment between;
// that sweep then finds an empty directory and removes it too.
func removeLocked(dir string, lock *lockFile) error {
	var errs []error
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	for _, entry := range entries {
		if entry.Name() == lockFileName {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			errs = append(errs, err)
		}
	}
	if err := lock.release(); err != nil {
		errs = append(errs, fmt.Errorf("release the lock of %s: %w", dir, err))
	}
	for _, path := range []string{filepath.Join(dir, lockFileName), dir} {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// errLockTaken reports a lock file that another process took first, or that
// the sweep of another worker removed.
var errLockTaken = errors.New("another process took the lock of the agent directory")

// lockFile is an open lock file whose exclusive lock this process holds.
type lockFile struct {
	file *os.File
}

// createLock creates the lock file of a new directory and takes its lock.
//
// It fails with errLockTaken when the sweep of another worker came first. That
// sweep reads a directory with no held lock as stale, so it can create the
// lock file itself, take the lock, or remove the directory, the lock file
// included, and release the lock again. The last case leaves this process with
// a lock on a file that no longer has a name, which is why the check at the
// end compares the file under the lock with the file at the path.
func createLock(path string) (*lockFile, error) {
	file, err := openLockFile(path, true)
	if errors.Is(err, fs.ErrExist) || errors.Is(err, fs.ErrNotExist) {
		return nil, errLockTaken
	}
	if err != nil {
		return nil, err
	}
	held, err := tryLock(file)
	if err == nil && !held {
		err = errLockTaken
	}
	if err == nil && !stillAt(file, path) {
		err = errLockTaken
	}
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return &lockFile{file: file}, nil
}

// claimLock takes the lock of an existing directory for the sweep. It reports
// false when another process holds the lock, which makes the directory live,
// and when the directory went away.
//
// A directory whose lock file does not exist yet is new, or its worker ended
// before it created the lock file. claimLock creates the file and takes the
// lock, so createLock of a worker that still creates the directory fails, and
// that worker makes another directory.
func claimLock(path string) (*lockFile, bool, error) {
	file, err := openLockFile(path, false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	held, err := tryLock(file)
	if err != nil || !held || !stillAt(file, path) {
		_ = file.Close()
		return nil, false, err
	}
	return &lockFile{file: file}, true, nil
}

// stillAt reports whether file is the regular file at path now: no process
// removed or replaced it since this process opened it.
func stillAt(file *os.File, path string) bool {
	held, err := file.Stat()
	if err != nil || !held.Mode().IsRegular() {
		return false
	}
	current, err := os.Lstat(path)
	if err != nil {
		return false
	}
	return os.SameFile(held, current)
}

// release releases the lock and closes the file. It is safe on a nil lock.
func (l *lockFile) release() error {
	if l == nil {
		return nil
	}
	return errors.Join(unlock(l.file), l.file.Close())
}
