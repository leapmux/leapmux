//go:build darwin

package atomicfile

import (
	"errors"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

// renameNoReplace renames oldpath to newpath and fails when newpath
// already exists, so a concurrent publisher cannot silently replace the
// winner. macOS spells this renamex_np with RENAME_EXCL.
func renameNoReplace(oldpath, newpath string) error {
	if err := unix.RenamexNp(oldpath, newpath, unix.RENAME_EXCL); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return os.ErrExist
		}
		return &fs.PathError{Op: "renamex_np", Path: newpath, Err: err}
	}
	return nil
}
