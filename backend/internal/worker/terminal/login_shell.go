package terminal

import (
	"regexp"
	"strings"
)

var (
	pwshPattern = regexp.MustCompile(`^(?:pwsh|powershell)(?:-preview)?$`)
	// pwshCorePattern matches only PowerShell Core (pwsh 6+), excluding
	// Windows PowerShell 5.1 (powershell.exe). Used to gate the -Login
	// switch, which Core introduced and 5.1 does not understand.
	pwshCorePattern = regexp.MustCompile(`^pwsh(?:-preview)?$`)
)

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
//   - pwsh/pwsh-preview:        ["-Login"]  — PowerShell Core 6+
//   - powershell(-preview):     nil  — Windows PowerShell 5.1 has no -Login
//     and parses the unknown switch as a command name, raising
//     "ObjectNotFound: (-Login:String), CommandNotFoundException".
//   - cmd.exe:                  nil  — no login concept; rejects POSIX flags
//   - tcsh/csh:                 ["-l"]  — tcsh requires -l as the only flag
//   - all others:               ["-i", "-l"]
func LoginShellArgs(shellPath string) []string {
	name := ShellBaseName(shellPath)
	switch {
	case pwshCorePattern.MatchString(name):
		return []string{"-Login"}
	case IsPwsh(name):
		return nil
	case name == "cmd":
		return nil
	case name == "tcsh" || name == "csh":
		return []string{"-l"}
	default:
		return []string{"-i", "-l"}
	}
}
