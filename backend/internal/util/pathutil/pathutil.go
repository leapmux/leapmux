// Package pathutil provides OS-aware helpers for comparing filesystem paths.
package pathutil

import (
	"path/filepath"
	"runtime"
	"strings"
)

// SamePath reports whether a and b name the same filesystem location after
// filepath.Clean. Comparison is case-insensitive on Windows, byte-exact on
// POSIX. Callers that need symlink resolution should filepath.EvalSymlinks
// both inputs first.
func SamePath(a, b string) bool {
	ca, cb := filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(ca, cb)
	}
	return ca == cb
}

// Canonicalize returns filepath.EvalSymlinks(p) if it succeeds, otherwise p
// unchanged. Use this when you want a best-effort canonical path but don't
// want to fail the caller when resolution isn't possible.
func Canonicalize(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}
