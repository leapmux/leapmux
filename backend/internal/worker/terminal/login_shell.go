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
// append to. The PTY path in Start uses this list; go-pty sets Setsid.
//
// A caller that runs a command string through a plain exec.Cmd (no pty)
// must use CommandArgs and procutil.DetachFromTerminal. An interactive
// shell otherwise inherits the parent's process group and controlling
// terminal, and bash job control then sends SIGTTIN to that whole group.
//
//   - pwsh/pwsh-preview:        ["-Login"]  — PowerShell Core 6+
//   - powershell(-preview):     nil  — Windows PowerShell 5.1 has no -Login
//     and parses the unknown switch as a command name, raising
//     "ObjectNotFound: (-Login:String), CommandNotFoundException".
//   - cmd.exe:                  ["/D"] — no login concept; /D suppresses
//     the registry AutoRun command (HKLM/HKCU\Software\Microsoft\Command
//     Processor\AutoRun) so spawned cmd sessions behave the same on every
//     machine. Without this a user-set AutoRun runs before every prompt
//     and can leave %ERRORLEVEL% non-zero or otherwise mutate state in
//     ways that surface as flaky `exit N` parsing.
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
		return []string{"/D"}
	case name == "tcsh" || name == "csh":
		return []string{"-l"}
	default:
		return []string{"-i", "-l"}
	}
}

// CommandArgs is the argv after the shell path for one command string.
// loginShell adds LoginShellArgs, except tcsh/csh login which uses -ic
// because tcsh rejects -l combined with any other flag.
//
// A caller that starts this argv through a plain exec.Cmd (no pty) must
// call procutil.DetachFromTerminal on that command.
func CommandArgs(shellPath string, loginShell bool, commandFlag, command string) []string {
	name := ShellBaseName(shellPath)
	if loginShell && (name == "tcsh" || name == "csh") {
		return []string{"-ic", command}
	}
	if loginShell {
		return append(LoginShellArgs(shellPath), commandFlag, command)
	}
	return []string{commandFlag, command}
}
