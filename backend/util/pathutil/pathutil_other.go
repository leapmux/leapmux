//go:build !windows

package pathutil

import "path/filepath"

// NormalizeNative returns filepath.Clean(p). On POSIX hosts there is no
// separator or drive-path translation to do — `/c/foo` is a legitimate
// directory, not an MSYS drive reference.
func NormalizeNative(p string) string {
	if p == "" {
		return p
	}
	return filepath.Clean(p)
}
