//go:build !windows

package pathutil

// FilesystemRoots returns the roots of this host's filesystem.
//
// One root on POSIX, always. A mounted volume is a directory under "/", not a
// second root, so an enumeration of /Volumes or /media would answer with
// something every caller then has to special-case as "not really a root".
//
// A fresh slice per call: the caller owns what it gets, so a caller that sorts
// or truncates the result in place cannot corrupt a later call.
func FilesystemRoots() []string {
	return []string{"/"}
}
