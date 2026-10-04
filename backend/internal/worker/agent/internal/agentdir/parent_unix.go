//go:build unix

package agentdir

import (
	"io/fs"
	"os"
	"syscall"
)

// mkdirPrivate creates dir with mode 0700.
func mkdirPrivate(dir string) error {
	return os.Mkdir(dir, 0o700)
}

// ownedByCurrentUser reports whether the user who runs this process owns the
// file that info describes.
func ownedByCurrentUser(_ string, info fs.FileInfo) (bool, error) {
	owner, ok := FileOwner(info)
	return ok && owner == os.Getuid(), nil
}

// restrictParent makes a parent of the user private when other users can reach
// it.
func restrictParent(dir string, info fs.FileInfo) error {
	if info.Mode().Perm()&0o077 == 0 {
		return nil
	}
	return os.Chmod(dir, 0o700)
}

// FileOwner returns the user id that owns a file, from the result of a stat.
func FileOwner(info fs.FileInfo) (int, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(stat.Uid), true
}
