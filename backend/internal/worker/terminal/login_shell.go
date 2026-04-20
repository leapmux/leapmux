package terminal

import (
	"regexp"
	"strings"
)

var pwshPattern = regexp.MustCompile(`^(?:pwsh|powershell)(?:-preview)?(?:\.exe)?$`)

// IsPwsh returns true if the shell name matches PowerShell variants
// (pwsh, powershell, pwsh-preview, powershell-preview, plus the ".exe"
// suffix forms surfaced when paths are split on Windows).
func IsPwsh(name string) bool {
	return pwshPattern.MatchString(name)
}

// baseName extracts the trailing component of a file path, treating both
// "/" and "\" as separators regardless of the runtime OS. This is needed
// because filepath.Base is platform-dependent: on Unix it does not split
// on "\", so a Windows-style path like `C:\...\pwsh.exe` is returned as-is
// and downstream shell-kind detection fails.
func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// LoginShellArgs returns the flags needed to start the given shell as an
// interactive login shell (no -c command). The returned slice is safe to
// append to.
//
//   - pwsh/powershell: ["-Login"]
//   - cmd.exe:         []  — cmd has no login concept and rejects POSIX flags
//   - tcsh/csh:        ["-l"]  — tcsh requires -l as the only flag
//   - all others:      ["-i", "-l"]
func LoginShellArgs(shellPath string) []string {
	name := strings.ToLower(baseName(shellPath))
	switch {
	case IsPwsh(name):
		return []string{"-Login"}
	case name == "cmd" || name == "cmd.exe":
		return nil
	case name == "tcsh" || name == "csh":
		return []string{"-l"}
	default:
		return []string{"-i", "-l"}
	}
}
