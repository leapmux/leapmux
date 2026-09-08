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
	return equalCleaned(filepath.Clean(a), filepath.Clean(b))
}

// equalCleaned compares two ALREADY-cleaned paths under the host's filesystem
// case rules: case-insensitive on Windows, byte-exact on POSIX.
func equalCleaned(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// HasPathPrefix reports whether path is equal to or nested inside prefix,
// matching the host's filesystem case rules (case-insensitive on Windows,
// byte-exact on POSIX). Both inputs are cleaned, and the comparison respects
// the component boundary, so `/foo` does not match `/foobar`.
func HasPathPrefix(path, prefix string) bool {
	cp := filepath.Clean(path)
	pp := filepath.Clean(prefix)
	if equalCleaned(cp, pp) {
		return true
	}
	// The boundary separator is appended only when the prefix does not already
	// end in one. A filesystem ROOT is nothing but that separator -- "/" and
	// `C:\` clean to themselves -- so appending unconditionally built "//" and
	// `C:\\`, which no real path starts with. Every path under a root then
	// answered false, and a directory tree rooted at "/" could not prove that
	// any of its own entries were under it.
	if !strings.HasSuffix(pp, string(filepath.Separator)) {
		pp += string(filepath.Separator)
	}
	if len(cp) < len(pp) {
		return false
	}
	return equalCleaned(cp[:len(pp)], pp)
}

// Canonicalize returns filepath.EvalSymlinks(p) if it succeeds, otherwise
// filepath.Clean(p). Use this when you want a best-effort canonical path but
// don't want to fail the caller when resolution isn't possible.
//
// The fallback is CLEANED rather than returned verbatim because the result is
// used as a map key and as a stored column (gitIndexLock, worktrees.worktree_path,
// TabPayloadStore). EvalSymlinks cleans on the success path, so returning the
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

// CanonicalizeAbsent canonicalizes a path whose final components may not exist.
//
// EvalSymlinks fails outright when any component is missing, so Canonicalize
// falls back to Clean and answers with a spelling that no stored record
// matches: on macOS a directory under /var is recorded as /private/var, and a
// lookup for one the user just deleted misses its own row. This resolves the
// deepest ancestor that still exists and re-appends the rest, so a deleted leaf
// keeps the spelling its parent gives it.
//
// It equals Canonicalize for a path that exists. Use it only where the leaf may
// be absent and the result is matched against a path canonicalized earlier.
func CanonicalizeAbsent(p string) string {
	cleaned := filepath.Clean(p)
	dir, rest := cleaned, ""
	for {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached the volume root without resolving anything.
			return cleaned
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
}
