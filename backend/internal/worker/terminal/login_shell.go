package terminal

import (
	"path/filepath"
	"regexp"
)

var pwshPattern = regexp.MustCompile(`^(?:pwsh|powershell)(?:-preview)?$`)

// IsPwsh returns true if the shell name matches PowerShell variants
// (pwsh, powershell, pwsh-preview, powershell-preview).
func IsPwsh(name string) bool {
	return pwshPattern.MatchString(name)
}

// LoginShellArgs returns the flags needed to start the given shell as an
// interactive login shell (no -c command). The returned slice is safe to
// append to.
//
//   - pwsh/powershell: ["-Login"]
//   - tcsh/csh:        ["-l"]  — tcsh requires -l as the only flag
//   - all others:      ["-i", "-l"]
func LoginShellArgs(shellPath string) []string {
	name := filepath.Base(shellPath)
	switch {
	case IsPwsh(name):
		return []string{"-Login"}
	case name == "tcsh" || name == "csh":
		return []string{"-l"}
	default:
		return []string{"-i", "-l"}
	}
}
