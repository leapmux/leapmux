package agent

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// buildShellWrappedCommand constructs an exec.Cmd that launches the claude
// binary inside the user's login shell with interactive+login flags. This
// ensures that shell profile scripts (e.g. .zshrc, .bash_profile) are sourced,
// providing the correct PATH and environment variables.
//
// It returns the command and a unique delimiter string. The caller should scan
// stdout for this delimiter: everything before it is shell preamble (motd,
// profile output, etc.), and everything after is NDJSON from claude.
func buildShellWrappedCommand(ctx context.Context, shellPath string, claudeArgs []string, workingDir string) (*exec.Cmd, string) {
	delimiter := "__LEAPMUX_READY_" + generateRequestID() + "__"
	shellName := filepath.Base(shellPath)

	var cmdArgs []string
	switch {
	case isPwsh(shellName):
		inner := buildPwshCommand(delimiter, claudeArgs)
		cmdArgs = []string{"-Login", "-Command", inner}
	case shellName == "tcsh" || shellName == "csh":
		inner := buildPosixCommand(delimiter, claudeArgs, true)
		cmdArgs = []string{"-ic", inner}
	case shellName == "nu":
		inner := buildNuCommand(delimiter, claudeArgs)
		cmdArgs = []string{"-i", "-l", "-c", inner}
	default:
		// bash, zsh, fish, sh, ash, dash, ksh, xonsh, and unknown shells
		inner := buildPosixCommand(delimiter, claudeArgs, true)
		cmdArgs = []string{"-i", "-l", "-c", inner}
	}

	cmd := exec.CommandContext(ctx, shellPath, cmdArgs...)
	cmd.Dir = workingDir
	return cmd, delimiter
}

// buildPosixCommand builds the inner command string for POSIX-like shells.
// If useExec is true, the command uses exec to replace the shell process.
func buildPosixCommand(delimiter string, claudeArgs []string, useExec bool) string {
	quoted := make([]string, len(claudeArgs))
	for i, arg := range claudeArgs {
		quoted[i] = posixQuote(arg)
	}

	if useExec {
		return fmt.Sprintf("unset CLAUDECODE && echo '%s' && exec claude %s", delimiter, strings.Join(quoted, " "))
	}
	return fmt.Sprintf("unset CLAUDECODE && echo '%s' && claude %s", delimiter, strings.Join(quoted, " "))
}

// buildNuCommand builds the inner command string for Nushell.
func buildNuCommand(delimiter string, claudeArgs []string) string {
	quoted := make([]string, len(claudeArgs))
	for i, arg := range claudeArgs {
		quoted[i] = posixQuote(arg)
	}
	return fmt.Sprintf("hide-env CLAUDECODE; echo '%s'; ^claude %s", delimiter, strings.Join(quoted, " "))
}

// buildPwshCommand builds the inner command string for PowerShell.
func buildPwshCommand(delimiter string, claudeArgs []string) string {
	quoted := make([]string, len(claudeArgs))
	for i, arg := range claudeArgs {
		quoted[i] = pwshQuote(arg)
	}
	return fmt.Sprintf("Remove-Item Env:CLAUDECODE; Write-Output '%s'; & claude %s", delimiter, strings.Join(quoted, " "))
}

// posixQuote wraps a string in single quotes for POSIX shells.
// Single quotes within the string are escaped as '\” (end quote, escaped
// literal quote, start quote).
func posixQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// pwshQuote wraps a string in single quotes for PowerShell.
// Single quotes within the string are escaped by doubling them (”).
func pwshQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

var pwshPattern = regexp.MustCompile(`^(?:pwsh|powershell)(?:-preview)?$`)

// isPwsh returns true if the shell name matches PowerShell variants.
func isPwsh(name string) bool {
	return pwshPattern.MatchString(name)
}
