package terminal

import (
	"regexp"
	"strings"
)

var pwshPattern = regexp.MustCompile(`^(?:pwsh|powershell)(?:-preview)?$`)

// IsPwsh returns true if name is one of the PowerShell kind-names
// (pwsh, powershell, pwsh-preview, powershell-preview). The input must be
// already normalized — feed paths through ShellBaseName first.
func IsPwsh(name string) bool {
	return pwshPattern.MatchString(name)
}

// ShellBaseName returns the canonical kind-name of a shell path: lowercased,
// `.exe`-stripped, splitting on both "/" and "\" regardless of host OS so
// Windows paths compare correctly on Unix builds (where filepath.Base would
// leave `C:\...\pwsh.exe` intact).
func ShellBaseName(p string) string {
	base := p
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		base = p[i+1:]
	}
	return strings.TrimSuffix(strings.ToLower(base), ".exe")
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
	name := ShellBaseName(shellPath)
	switch {
	case IsPwsh(name):
		return []string{"-Login"}
	case name == "cmd":
		return nil
	case name == "tcsh" || name == "csh":
		return []string{"-l"}
	default:
		return []string{"-i", "-l"}
	}
}
