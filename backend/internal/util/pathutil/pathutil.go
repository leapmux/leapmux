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

// HasPathPrefix reports whether path is equal to or nested inside prefix,
// matching the host's filesystem case rules (case-insensitive on Windows,
// byte-exact on POSIX). Both inputs are cleaned; a trailing separator is
// appended to prefix so `/foo` does not match `/foobar`.
func HasPathPrefix(path, prefix string) bool {
	cp := filepath.Clean(path) + string(filepath.Separator)
	pp := filepath.Clean(prefix) + string(filepath.Separator)
	if len(cp) < len(pp) {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(cp[:len(pp)], pp)
	}
	return cp[:len(pp)] == pp
}

// Canonicalize returns filepath.EvalSymlinks(p) if it succeeds, otherwise
// filepath.Clean(p). Use this when you want a best-effort canonical path but
// don't want to fail the caller when resolution isn't possible.
//
// The fallback is CLEANED rather than returned verbatim because the result is
// used as a map key and as a stored column (gitIndexLock, worktrees.worktree_path,
// FileTabPathStore). EvalSymlinks cleans on the success path, so returning the
// raw string on failure made the same directory hash two ways depending on
// whether it happened to resolve -- and for gitIndexLock that means two mutexes
// for one repository, which is precisely the failure the lock exists to prevent.
// Clean is not a full canonicalization (it cannot collapse a symlink), but it
// makes the two paths agree on everything that does not require the filesystem.
func Canonicalize(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return filepath.Clean(p)
}
