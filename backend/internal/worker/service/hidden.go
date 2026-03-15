//go:build !windows

package service

import "strings"

// isHidden reports whether the given file name should be considered hidden.
// On Unix-like systems, files starting with "." are hidden.
func isHidden(name string) bool {
	return strings.HasPrefix(name, ".")
}
