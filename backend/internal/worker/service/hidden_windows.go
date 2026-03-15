//go:build windows

package service

import (
	"strings"
	"syscall"
)

// isHidden reports whether the given file name should be considered hidden.
// On Windows, a file is hidden if its name starts with "." or it has the
// FILE_ATTRIBUTE_HIDDEN attribute set.
func isHidden(name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	// Check the Windows hidden attribute.
	p, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return false
	}
	attrs, err := syscall.GetFileAttributes(p)
	if err != nil {
		return false
	}
	return attrs&syscall.FILE_ATTRIBUTE_HIDDEN != 0
}
