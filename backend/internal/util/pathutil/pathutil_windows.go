//go:build windows

package pathutil

import (
	"path/filepath"
	"strings"
)

// NormalizeNative returns p with backslash separators and converts MSYS /
// Git-Bash-style drive paths (`/c/foo`, `/C/foo`) to `C:\foo` so paths from
// git / bash tooling compare equal to paths from native Go APIs. Callers
// that want symlink resolution too should feed the result into Canonicalize.
func NormalizeNative(p string) string {
	if p == "" {
		return p
	}
	if len(p) >= 2 && p[0] == '/' {
		c := p[1]
		isLetter := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		if isLetter && (len(p) == 2 || p[2] == '/' || p[2] == '\\') {
			// Bare "/c" means drive root; make it "/c/" so Clean produces
			// "C:\" rather than the drive-relative "C:.".
			rest := p[2:]
			if rest == "" {
				rest = "/"
			}
			p = strings.ToUpper(string(c)) + ":" + rest
		}
	}
	return filepath.Clean(p)
}
