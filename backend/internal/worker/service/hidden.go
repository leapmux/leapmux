//go:build !windows

package service

import "strings"

// isHidden reports whether the given file name should be considered hidden.
// On Unix-like systems, files starting with "." are hidden.
// The absPath parameter is unused on Unix but required for the Windows implementation.
func isHidden(absPath, name string) bool {
	return strings.HasPrefix(name, ".")
}
