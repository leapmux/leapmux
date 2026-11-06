//go:build unix

package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// syncDirHandle opens the directory at path and synchronizes it, which
// publishes the directory entries a rename or creation inside it produced.
// A directory the caller cannot open is an error: a caller that asked for
// durability must learn when it cannot have it.
func syncDirHandle(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open the directory to publish it: %w", err)
	}
	defer func() { _ = f.Close() }()
	if err := f.Sync(); err != nil {
		return fmt.Errorf("synchronize the directory %s: %w", path, err)
	}
	return nil
}

func (defaultFileOps) syncDir(path string) error { return syncDirHandle(path) }

// replaceFileDurable is the atomic replace: on Unix a rename is the
// publication, and the directory flush below is what makes it durable.
func replaceFileDurable(oldpath, newpath string) error {
	return ops.rename(oldpath, newpath)
}

// publishFileEntry flushes the directory entry that names a replaced
// file. It runs through the seam so the observed order of a durable
// write ends file sync, rename, directory sync.
func publishFileEntry(path string) error {
	return ops.syncDir(filepath.Dir(path))
}
