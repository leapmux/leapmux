//go:build !windows

package atomicfile

import "os"

func replaceFile(oldpath, newpath string) error {
	return os.Rename(oldpath, newpath)
}

func replaceFileInRoot(root *os.Root, oldRel, newRel string) error {
	return root.Rename(oldRel, newRel)
}
