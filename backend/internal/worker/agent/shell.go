package agent

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/terminal"
)

// buildShellWrappedCommand constructs an exec.Cmd that launches a binary
// inside the user's shell. When interactive is true, the shell is invoked
// with interactive+login flags (e.g. -i -l -c) so that profile scripts
// are sourced. When false, only -c is used (no profile sourcing).
//
// binaryName is the executable to invoke (e.g. "claude", "codex").
// baseArgs are always passed to the binary. modelEffortArgs (--model/--effort)
// are conditionally included only when no third-party LLM provider env vars
// are detected at shell runtime. When modelEffortArgs is empty, no conditional
// logic is emitted.
//
// It returns the command, a unique delimiter string, and a metadata line prefix.
// The caller should scan stdout for lines starting with metaPrefix to extract
// key=value metadata, then for the delimiter to detect the end of preamble.
func buildShellWrappedCommand(ctx context.Context, shellPath string, interactive bool,
	binaryName string, baseArgs []string, modelEffortArgs []string, workingDir string) (*exec.Cmd, string, string) {

	id := generateRequestID()
	delimiter := "__LEAPMUX_READY_" + id + "__"
	metaPrefix := ""
	if len(modelEffortArgs) > 0 {
		metaPrefix = "__LEAPMUX_META_" + id + "__ "
	}
	shellName := filepath.Base(shellPath)

	var cmdArgs []string
	switch {
	case terminal.IsPwsh(shellName):
		inner := buildPwshCommand(binaryName, delimiter, metaPrefix, baseArgs, modelEffortArgs)
		if interactive {
			cmdArgs = append(terminal.LoginShellArgs(shellPath), "-Command", inner)
		} else {
			cmdArgs = []string{"-Command", inner}
		}
	case shellName == "tcsh" || shellName == "csh":
		inner := buildPosixCommand(binaryName, delimiter, metaPrefix, baseArgs, modelEffortArgs, true)
		if interactive {
			cmdArgs = []string{"-ic", inner} // tcsh: -l must be the only flag
		} else {
			cmdArgs = []string{"-c", inner}
		}
	case shellName == "nu":
		inner := buildNuCommand(binaryName, delimiter, metaPrefix, baseArgs, modelEffortArgs)
		if interactive {
			cmdArgs = append(terminal.LoginShellArgs(shellPath), "-c", inner)
		} else {
			cmdArgs = []string{"-c", inner}
		}
	default:
		// bash, zsh, fish, sh, ash, dash, ksh, xonsh, and unknown shells
		inner := buildPosixCommand(binaryName, delimiter, metaPrefix, baseArgs, modelEffortArgs, true)
		if interactive {
			cmdArgs = append(terminal.LoginShellArgs(shellPath), "-c", inner)
		} else {
			cmdArgs = []string{"-c", inner}
		}
	}

	cmd := exec.CommandContext(ctx, shellPath, cmdArgs...)
	cmd.Dir = workingDir
	return cmd, delimiter, metaPrefix
}

// buildPosixCommand builds the inner command string for POSIX-like shells.
// If useExec is true, the command uses exec to replace the shell process.
// When modelEffortArgs is non-empty, a conditional is emitted to check for
// third-party provider env vars at runtime.
func buildPosixCommand(binaryName, delimiter, metaPrefix string, baseArgs, modelEffortArgs []string, useExec bool) string {
	quotedBase := make([]string, len(baseArgs))
	for i, arg := range baseArgs {
		quotedBase[i] = posixQuote(arg)
	}

	execPrefix := ""
	if useExec {
		execPrefix = "exec "
	}

	baseArgsStr := strings.Join(quotedBase, " ")

	// Simple path: no model/effort args (third-party detected from settings).
	if len(modelEffortArgs) == 0 {
		return fmt.Sprintf("unset CLAUDECODE && echo '%s' && %s%s %s",
			delimiter, execPrefix, binaryName, baseArgsStr)
	}

	// Conditional path: check env vars at runtime.
	quotedME := make([]string, len(modelEffortArgs))
	for i, arg := range modelEffortArgs {
		quotedME[i] = posixQuote(arg)
	}
	meArgsStr := strings.Join(quotedME, " ")

	return fmt.Sprintf(
		"unset CLAUDECODE && "+
			"if "+posixEnvCondition()+"; then "+
			"echo '%scan_change_model_and_effort=false' && "+
			"echo '%s' && %s%s %s; "+
			"else "+
			"echo '%scan_change_model_and_effort=true' && "+
			"echo '%s' && %s%s %s %s; "+
			"fi",
		metaPrefix, delimiter, execPrefix, binaryName, baseArgsStr,
		metaPrefix, delimiter, execPrefix, binaryName, baseArgsStr, meArgsStr,
	)
}

// buildNuCommand builds the inner command string for Nushell.
func buildNuCommand(binaryName, delimiter, metaPrefix string, baseArgs, modelEffortArgs []string) string {
	quotedBase := make([]string, len(baseArgs))
	for i, arg := range baseArgs {
		quotedBase[i] = nuQuote(arg)
	}

	baseArgsStr := strings.Join(quotedBase, " ")

	// Simple path: no model/effort args.
	if len(modelEffortArgs) == 0 {
		return fmt.Sprintf("hide-env CLAUDECODE; echo '%s'; ^%s %s",
			delimiter, binaryName, baseArgsStr)
	}

	// Conditional path.
	quotedME := make([]string, len(modelEffortArgs))
	for i, arg := range modelEffortArgs {
		quotedME[i] = nuQuote(arg)
	}
	meArgsStr := strings.Join(quotedME, " ")

	return fmt.Sprintf(
		"hide-env CLAUDECODE; "+
			"if ("+nuEnvCondition()+") { "+
			"echo '%scan_change_model_and_effort=false'; "+
			"echo '%s'; ^%s %s "+
			"} else { "+
			"echo '%scan_change_model_and_effort=true'; "+
			"echo '%s'; ^%s %s %s "+
			"}",
		metaPrefix, delimiter, binaryName, baseArgsStr,
		metaPrefix, delimiter, binaryName, baseArgsStr, meArgsStr,
	)
}

// buildPwshCommand builds the inner command string for PowerShell.
func buildPwshCommand(binaryName, delimiter, metaPrefix string, baseArgs, modelEffortArgs []string) string {
	quotedBase := make([]string, len(baseArgs))
	for i, arg := range baseArgs {
		quotedBase[i] = pwshQuote(arg)
	}

	baseArgsStr := strings.Join(quotedBase, " ")

	// Simple path: no model/effort args.
	if len(modelEffortArgs) == 0 {
		return fmt.Sprintf("Remove-Item Env:CLAUDECODE -ErrorAction SilentlyContinue; "+
			"Write-Output '%s'; & %s %s",
			delimiter, binaryName, baseArgsStr)
	}

	// Conditional path.
	quotedME := make([]string, len(modelEffortArgs))
	for i, arg := range modelEffortArgs {
		quotedME[i] = pwshQuote(arg)
	}
	meArgsStr := strings.Join(quotedME, " ")

	return fmt.Sprintf(
		"Remove-Item Env:CLAUDECODE -ErrorAction SilentlyContinue; "+
			"if ("+pwshEnvCondition()+") { "+
			"Write-Output '%scan_change_model_and_effort=false'; "+
			"Write-Output '%s'; & %s %s "+
			"} else { "+
			"Write-Output '%scan_change_model_and_effort=true'; "+
			"Write-Output '%s'; & %s %s %s "+
			"}",
		metaPrefix, delimiter, binaryName, baseArgsStr,
		metaPrefix, delimiter, binaryName, baseArgsStr, meArgsStr,
	)
}

// posixEnvCondition builds a POSIX shell conditional expression that checks
// whether any third-party provider env var is set.
// e.g. `[ -n "$VAR1" ] || [ -n "$VAR2" ] || [ -n "$VAR3" ]`
func posixEnvCondition() string {
	parts := make([]string, len(thirdPartyProviderEnvVars))
	for i, v := range thirdPartyProviderEnvVars {
		parts[i] = fmt.Sprintf(`[ -n "$%s" ]`, v)
	}
	return strings.Join(parts, " || ")
}

// nuEnvCondition builds a Nushell conditional expression that checks
// whether any third-party provider env var is set.
// e.g. `($env | get -i VAR1 | default ”) != ” or ...`
func nuEnvCondition() string {
	parts := make([]string, len(thirdPartyProviderEnvVars))
	for i, v := range thirdPartyProviderEnvVars {
		parts[i] = fmt.Sprintf("($env | get -i %s | default '') != ''", v)
	}
	return strings.Join(parts, " or ")
}

// pwshEnvCondition builds a PowerShell conditional expression that checks
// whether any third-party provider env var is set.
// e.g. `$env:VAR1 -or $env:VAR2 -or $env:VAR3`
func pwshEnvCondition() string {
	parts := make([]string, len(thirdPartyProviderEnvVars))
	for i, v := range thirdPartyProviderEnvVars {
		parts[i] = "$env:" + v
	}
	return strings.Join(parts, " -or ")
}

// posixQuote wraps a string in single quotes for POSIX shells.
// Single quotes within the string are escaped as '\" (end quote, escaped
// literal quote, start quote).
func posixQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// nuQuote wraps a string in double quotes for Nushell.
// In Nushell double-quoted strings, only \ and " need escaping.
var nuReplacer = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

func nuQuote(s string) string {
	return `"` + nuReplacer.Replace(s) + `"`
}

// pwshQuote wraps a string in single quotes for PowerShell.
// Single quotes within the string are escaped by doubling them (").
func pwshQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
