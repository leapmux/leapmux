//go:build linux

package atomicfile

import (
	"errors"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

// renameNoReplace renames oldpath to newpath and fails when newpath
// already exists, so a concurrent publisher cannot silently replace the
// winner. Linux spells this renameat2 with RENAME_NOREPLACE.
func renameNoReplace(oldpath, newpath string) error {
	if err := unix.Renameat2(unix.AT_FDCWD, oldpath, unix.AT_FDCWD, newpath, unix.RENAME_NOREPLACE); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return os.ErrExist
		}
		return &fs.PathError{Op: "renameat2", Path: newpath, Err: err}
	}
	return nil
}
