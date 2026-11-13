package keystore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/leapmux/leapmux/internal/util/atomicfile"
	"github.com/leapmux/leapmux/internal/util/canonicalpath"
	"github.com/leapmux/leapmux/internal/util/filelock"
)

var errKeyRingMutationBusy = errors.New("another process is changing the key ring; retry when it finishes")

// keyRingMutationPath preserves relative-path semantics and follows an existing
// key-file symlink before choosing both the publication and persistent lock paths.
func keyRingMutationPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(absolute)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		absolute, err = filepath.EvalSymlinks(absolute)
		if err != nil {
			return "", fmt.Errorf("resolve key-ring symlink: %w", err)
		}
	}
	parent, err := canonicalpath.Directory(filepath.Dir(absolute))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(absolute)), nil
}

// withKeyRingMutation locks a stable sibling, never the replaced key inode.
// Read-only loading does not enter this mutation boundary.
func withKeyRingMutation(path string, write func(string) error) (err error) {
	path, err = keyRingMutationPath(path)
	if err != nil {
		return err
	}
	if err = atomicfile.MkdirAllDurable(filepath.Dir(path)); err != nil {
		return err
	}
	lock, err := filelock.Open(path+".lock", false)
	if err != nil {
		return fmt.Errorf("open key-ring lock: %w", err)
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	held, err := filelock.TryLock(lock)
	if err != nil {
		return err
	}
	if !held {
		return errKeyRingMutationBusy
	}
	defer func() { err = errors.Join(err, filelock.Unlock(lock)) }()
	return write(path)
}
