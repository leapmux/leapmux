package validate

import (
	"path"
	"strings"
)

// SanitizePath sanitizes a filesystem path.
// It strips control characters, trims whitespace, rejects path traversal,
// and normalizes the result. Empty input returns "".
//
// When homeDir is non-empty, tilde paths (~, ~/...) are expanded using it.
// When homeDir is empty, tilde paths are rejected.
//
// Accepted forms:
//   - Absolute paths: /home/user, /Users/john
//   - Tilde paths (when homeDir is provided): ~, ~/projects
//
// Rejected forms:
//   - Relative paths: home/user, ./foo
//   - Path traversal: /home/../etc/passwd
//   - Tilde paths (when homeDir is empty)
//   - Empty or whitespace-only strings
func SanitizePath(value, homeDir string) string {
	// Strip control characters (< 0x20 and 0x7F).
	var b strings.Builder
	for _, r := range value {
		if r < 0x20 || r == 0x7F {
			continue
		}
		b.WriteRune(r)
	}
	s := strings.TrimSpace(b.String())
	if s == "" {
		return ""
	}

	// Expand or reject tilde paths.
	if s == "~" || strings.HasPrefix(s, "~/") {
		if homeDir == "" {
			return ""
		}
		if s == "~" {
			s = homeDir
		} else {
			rest := strings.TrimLeft(s[2:], "/")
			if rest == "" {
				s = homeDir
			} else {
				s = homeDir + "/" + rest
			}
		}
	}

	// Must be absolute.
	if !strings.HasPrefix(s, "/") {
		return ""
	}

	// Reject path traversal before normalizing (Clean resolves ".." components).
	for _, comp := range strings.Split(s, "/") {
		if comp == ".." {
			return ""
		}
	}

	// Normalize.
	return path.Clean(s)
}
