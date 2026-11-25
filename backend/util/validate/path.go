package validate

import (
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
)

// Sentinel errors returned by SanitizePath and ResolveLiteralPath.
var (
	ErrEmptyPath          = errors.New("path is empty")
	ErrNotAbsolute        = errors.New("path must be absolute")
	ErrTraversal          = errors.New("path traversal not allowed")
	ErrNoHomeDir          = errors.New("cannot expand tilde: home directory not set")
	ErrReservedName       = errors.New("path component is a reserved device name")
	ErrReservedChar       = errors.New("path contains a reserved character")
	ErrTrailingDotOrSpace = errors.New("a Windows path component cannot end in a dot or space")
)

// SanitizePath validates and normalizes a filesystem path for the host OS.
//
// On Windows it additionally rejects DOS reserved device names (CON, PRN,
// AUX, NUL, COM0-9, LPT0-9 — matched case-insensitively, ignoring trailing
// dots/spaces and the extension), the Win32 forbidden filename characters
// < > " | ? *, and non-"." components ending in a dot or space. Paths in the
// \\?\ extended-length namespace skip these Win32 name-translation checks,
// while \\.\ device-namespace paths are still rejected.
//
// Accepted on POSIX:   /home/user, /Users/john, ~, ~/projects
// Accepted on Windows: C:\Users\u, c:/Users/u, \\server\share\x,
//
//	\\?\C:\foo, ~\foo, ~/foo
//
// Rejected on both:    relative paths, "..", empty/whitespace input.
func SanitizePath(value, homeDir string) (string, error) {
	// Drop an invalid byte BEFORE the control-character loop below.
	//
	// `for range` cannot report an invalid byte: it yields U+FFFD, which is not
	// a control character, so `WriteRune` writes the full 3-byte encoding and
	// the result is LONGER than the input. That is the same growth that
	// CleanNameChars decodes by hand to avoid, and this loop is the copy of the
	// old pattern that it left behind. A path has no byte limit for the growth
	// to overflow today, so this is a repair of the mechanism and not of a
	// reported failure.
	//
	// WireString rather than a hand decode, because this function has no scan
	// limit for a whole-string copy to defeat: it already builds one. It is also
	// the one statement of what a proto-safe string is, shared with the bgtask
	// and session-goal projections.
	s := WireString(value)
	if strings.IndexFunc(s, unicode.IsControl) >= 0 {
		var b strings.Builder
		b.Grow(len(s))
		for _, r := range s {
			if !unicode.IsControl(r) {
				b.WriteRune(r)
			}
		}
		s = b.String()
	}
	return resolveAbsolutePath(strings.TrimSpace(s), homeDir)
}

// ResolveLiteralPath validates a path without discarding spaces or changing characters.
func ResolveLiteralPath(value, homeDir string) (string, error) {
	if WireString(value) != value || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return "", errors.New("the path contains an invalid character")
	}
	return resolveAbsolutePath(value, homeDir)
}

func resolveAbsolutePath(s, homeDir string) (string, error) {
	if s == "" {
		return "", ErrEmptyPath
	}

	if s == "~" || strings.HasPrefix(s, "~/") || (runtime.GOOS == "windows" && strings.HasPrefix(s, `~\`)) {
		if homeDir == "" {
			return "", ErrNoHomeDir
		}
		if s == "~" {
			s = homeDir
		} else {
			separators := "/"
			if runtime.GOOS == "windows" {
				separators = `/\`
			}
			rest := strings.TrimLeft(s[2:], separators)
			if rest == "" {
				s = homeDir
			} else {
				// Plain concatenation, not filepath.Join: Join would Clean the
				// result and collapse ".." components before the traversal
				// check below has a chance to reject them.
				s = strings.TrimRight(homeDir, separators) + string(filepath.Separator) + rest
			}
		}
	}

	// filepath.IsAbs on Windows only recognises backslash; fold forward slashes.
	if runtime.GOOS == "windows" {
		s = filepath.FromSlash(s)
	}

	if !filepath.IsAbs(s) {
		return "", ErrNotAbsolute
	}

	// Check for ".." before Clean, which would collapse "a/../b" escapes.
	for _, comp := range splitPathComponents(s) {
		if comp == ".." {
			return "", ErrTraversal
		}
	}

	if runtime.GOOS == "windows" {
		if err := checkWindowsReserved(s); err != nil {
			return "", err
		}
	}

	return filepath.Clean(s), nil
}

// Split on both / and the OS separator, dropping empty components (e.g. the
// leading empties from a UNC \\server\share prefix).
func splitPathComponents(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == '/' || r == filepath.Separator
	})
}

// Caller must only invoke this on Windows.
func checkWindowsReserved(s string) error {
	return checkWindowsPath(s, filepath.VolumeName(s))
}

// checkWindowsPath validates Win32 filename restrictions after stripping volume
// (so UNC FQDN hosts such as \\fileserver.corp.example.\share\repo are allowed).
// Separating volume lets cross-OS unit tests exercise Windows rules directly.
func checkWindowsPath(s, volume string) error {
	// \\?\ disables Win32 name translation, so reserved device names and
	// trailing dots/spaces passed through it are legal filenames.
	if strings.HasPrefix(s, `\\?\`) {
		return nil
	}
	// \\.\ is always a device reference — filesystem callers never want this.
	if strings.HasPrefix(s, `\\.\`) {
		return ErrReservedName
	}

	rest := strings.TrimPrefix(s, volume)
	for _, comp := range strings.FieldsFunc(rest, func(r rune) bool { return r == '/' || r == '\\' }) {
		// Colon is only legal in the volume name, which was stripped above.
		if strings.ContainsAny(comp, `<>"|?*:`) {
			return ErrReservedChar
		}
		if IsReservedDeviceName(comp) {
			return ErrReservedName
		}
		// Skip "." because checkWindowsReserved runs before filepath.Clean.
		if comp != "." && strings.TrimRight(comp, " .") != comp {
			return ErrTrailingDotOrSpace
		}
	}
	return nil
}

// IsReservedDeviceName reports whether comp names a DOS reserved device
// (CON/PRN/AUX/NUL/COM0-9/LPT0-9), case-insensitively, after stripping the
// extension and any trailing dots or spaces — so NUL, nul.txt, CON., and
// "aux .log" all match.
//
// Exported because a path is not the only thing Windows resolves this way. Any
// code that MINTS a file name has to ask, and Windows resolves the name in every
// directory and with any extension, so `con.md` opens the console device rather
// than a file. `agent.SanitizePlanFilenameTitle` is the other caller.
func IsReservedDeviceName(comp string) bool {
	stem := comp
	if dot := strings.IndexByte(stem, '.'); dot >= 0 {
		stem = stem[:dot]
	}
	stem = strings.TrimRight(stem, ". ")
	if stem == "" {
		return false
	}
	switch strings.ToUpper(stem) {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	if len(stem) == 4 {
		prefix := strings.ToUpper(stem[:3])
		last := stem[3]
		if (prefix == "COM" || prefix == "LPT") && last >= '0' && last <= '9' {
			return true
		}
	}
	return false
}
