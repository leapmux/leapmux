package agent

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/leapmux/leapmux/util/procutil"
)

// shellWrapSpec describes how to wrap an agent binary launch inside the user's
// login shell. Grouping the knobs as one value keeps the nine provider call sites
// readable (named fields instead of a long positional tail like `..., nil, false,
// dir`) and makes the next per-launch knob a new field rather than another argument
// threaded through buildShellWrappedCommand and the three dialect builders.
type shellWrapSpec struct {
	Shell      string // the user's shell path (terminal.ShellBaseName picks the dialect)
	LoginShell bool   // invoke the shell with interactive+login flags so profile scripts are sourced
	BinaryName string // the executable to invoke (e.g. "claude", "codex")
	// StripEnvKeys are removed by the shell wrapper before the binary is started.
	StripEnvKeys []string
	BaseArgs     []string // always passed to the binary
	// ModelEffortArgs (--model/--effort) are included only when no third-party LLM
	// provider env vars are detected at shell runtime.
	ModelEffortArgs []string
	// ProbeThirdParty forces the runtime third-party-provider probe (and its
	// can_change_model_and_effort metadata line) to be emitted even when
	// ModelEffortArgs is empty. Claude passes this so a default-model launch that
	// sends no --model/--effort still detects a provider configured in the user's
	// shell profile (which detectThirdPartyProvider, reading only the worker's own
	// env, cannot see). Other providers pass false and keep the simple no-probe path.
	ProbeThirdParty bool
	WorkingDir      string // cmd.Dir for the launched process
}

// buildShellWrappedCommand constructs an exec.Cmd that launches spec.BinaryName
// inside the user's shell. When spec.LoginShell is true, the shell is invoked with
// interactive+login flags (e.g. -i -l -c) so that profile scripts are sourced. When
// false, only -c is used (no profile sourcing). When both spec.ModelEffortArgs is
// empty and spec.ProbeThirdParty is false, no conditional logic is emitted.
//
// It returns the command, a unique delimiter string, and a metadata line prefix.
// The caller should scan stdout for lines starting with metaPrefix to extract
// key=value metadata, then for the delimiter to detect the end of preamble.
func buildShellWrappedCommand(ctx context.Context, spec shellWrapSpec) (*exec.Cmd, string, string) {
	id := generateRequestID()
	delimiter := "__LEAPMUX_READY_" + id + "__"
	metaPrefix := ""
	if len(spec.ModelEffortArgs) > 0 || spec.ProbeThirdParty {
		metaPrefix = "__LEAPMUX_META_" + id + "__ "
	}
	shellName := terminal.ShellBaseName(spec.Shell)

	var cmdArgs []string
	switch {
	case terminal.IsPwsh(shellName):
		inner := buildPwshCommand(spec, delimiter, metaPrefix)
		if spec.LoginShell {
			cmdArgs = append(terminal.LoginShellArgs(spec.Shell), "-Command", inner)
		} else {
			cmdArgs = []string{"-Command", inner}
		}
	case shellName == "tcsh" || shellName == "csh":
		inner := buildPosixCommand(spec, delimiter, metaPrefix)
		if spec.LoginShell {
			cmdArgs = []string{"-ic", inner} // tcsh: -l must be the only flag
		} else {
			cmdArgs = []string{"-c", inner}
		}
	case shellName == "nu":
		inner := buildNuCommand(spec, delimiter, metaPrefix)
		if spec.LoginShell {
			cmdArgs = append(terminal.LoginShellArgs(spec.Shell), "-c", inner)
		} else {
			cmdArgs = []string{"-c", inner}
		}
	default:
		// bash, zsh, fish, sh, ash, dash, ksh, xonsh, and unknown shells
		inner := buildPosixCommand(spec, delimiter, metaPrefix)
		if spec.LoginShell {
			cmdArgs = append(terminal.LoginShellArgs(spec.Shell), "-c", inner)
		} else {
			cmdArgs = []string{"-c", inner}
		}
	}

	cmd := exec.CommandContext(ctx, spec.Shell, cmdArgs...)
	cmd.Dir = spec.WorkingDir
	envutil.ScrubAppImageEnv(cmd)
	procutil.HideConsoleWindow(cmd)
	return cmd, delimiter, metaPrefix
}

// buildPosixCommand builds the inner command string for POSIX-like shells.
// The command is always prefixed with `exec` so the shell process is
// replaced. When metaPrefix is set, a conditional is emitted to check for
// third-party provider env vars at runtime.
func buildPosixCommand(spec shellWrapSpec, delimiter, metaPrefix string) string {
	quotedBase := make([]string, len(spec.BaseArgs))
	for i, arg := range spec.BaseArgs {
		quotedBase[i] = posixQuote(arg)
	}

	baseArgsStr := strings.Join(quotedBase, " ")
	clearEnvPrefix := posixClearEnv(spec.StripEnvKeys)

	// Simple path: no probe wanted (empty metaPrefix means neither model/effort
	// args nor a forced third-party probe). When metaPrefix is set but
	// ModelEffortArgs is empty (Claude's default-model launch), the conditional
	// path below still runs: both branches exec the binary with no extra args,
	// differing only in the can_change_model_and_effort line.
	if metaPrefix == "" {
		return fmt.Sprintf("%secho '%s' && exec %s %s",
			clearEnvPrefix, delimiter, spec.BinaryName, baseArgsStr)
	}

	// Conditional path: check env vars at runtime.
	quotedME := make([]string, len(spec.ModelEffortArgs))
	for i, arg := range spec.ModelEffortArgs {
		quotedME[i] = posixQuote(arg)
	}
	meArgsStr := strings.Join(quotedME, " ")

	return fmt.Sprintf(
		"%s"+
			"if "+posixEnvCondition()+"; then "+
			"echo '%scan_change_model_and_effort=false' && "+
			"echo '%s' && exec %s %s; "+
			"else "+
			"echo '%scan_change_model_and_effort=true' && "+
			"echo '%s' && exec %s %s %s; "+
			"fi",
		clearEnvPrefix,
		metaPrefix, delimiter, spec.BinaryName, baseArgsStr,
		metaPrefix, delimiter, spec.BinaryName, baseArgsStr, meArgsStr,
	)
}

// buildNuCommand builds the inner command string for Nushell.
func buildNuCommand(spec shellWrapSpec, delimiter, metaPrefix string) string {
	quotedBase := make([]string, len(spec.BaseArgs))
	for i, arg := range spec.BaseArgs {
		quotedBase[i] = nuQuote(arg)
	}

	baseArgsStr := strings.Join(quotedBase, " ")
	clearEnvPrefix := nuClearEnv(spec.StripEnvKeys)

	// Simple path: no probe wanted (empty metaPrefix). See buildPosixCommand.
	if metaPrefix == "" {
		return fmt.Sprintf("%secho '%s'; ^%s %s",
			clearEnvPrefix, delimiter, spec.BinaryName, baseArgsStr)
	}

	// Conditional path.
	quotedME := make([]string, len(spec.ModelEffortArgs))
	for i, arg := range spec.ModelEffortArgs {
		quotedME[i] = nuQuote(arg)
	}
	meArgsStr := strings.Join(quotedME, " ")

	return fmt.Sprintf(
		"%s"+
			"if ("+nuEnvCondition()+") { "+
			"echo '%scan_change_model_and_effort=false'; "+
			"echo '%s'; ^%s %s "+
			"} else { "+
			"echo '%scan_change_model_and_effort=true'; "+
			"echo '%s'; ^%s %s %s "+
			"}",
		clearEnvPrefix,
		metaPrefix, delimiter, spec.BinaryName, baseArgsStr,
		metaPrefix, delimiter, spec.BinaryName, baseArgsStr, meArgsStr,
	)
}

// buildPwshCommand builds the inner command string for PowerShell.
func buildPwshCommand(spec shellWrapSpec, delimiter, metaPrefix string) string {
	quotedBase := make([]string, len(spec.BaseArgs))
	for i, arg := range spec.BaseArgs {
		quotedBase[i] = pwshQuote(arg)
	}

	baseArgsStr := strings.Join(quotedBase, " ")
	clearEnvPrefix := pwshClearEnv(spec.StripEnvKeys)

	// Simple path: no probe wanted (empty metaPrefix). See buildPosixCommand.
	if metaPrefix == "" {
		return fmt.Sprintf("%sWrite-Output '%s'; & %s %s",
			clearEnvPrefix, delimiter, spec.BinaryName, baseArgsStr)
	}

	// Conditional path.
	quotedME := make([]string, len(spec.ModelEffortArgs))
	for i, arg := range spec.ModelEffortArgs {
		quotedME[i] = pwshQuote(arg)
	}
	meArgsStr := strings.Join(quotedME, " ")

	return fmt.Sprintf(
		"%s"+
			"if ("+pwshEnvCondition()+") { "+
			"Write-Output '%scan_change_model_and_effort=false'; "+
			"Write-Output '%s'; & %s %s "+
			"} else { "+
			"Write-Output '%scan_change_model_and_effort=true'; "+
			"Write-Output '%s'; & %s %s %s "+
			"}",
		clearEnvPrefix,
		metaPrefix, delimiter, spec.BinaryName, baseArgsStr,
		metaPrefix, delimiter, spec.BinaryName, baseArgsStr, meArgsStr,
	)
}

func posixClearEnv(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	return "unset " + strings.Join(keys, " ") + " && "
}

func nuClearEnv(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	parts := make([]string, len(keys))
	for i, key := range keys {
		parts[i] = "hide-env " + key
	}
	return strings.Join(parts, "; ") + "; "
}

func pwshClearEnv(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	parts := make([]string, len(keys))
	for i, key := range keys {
		parts[i] = "Remove-Item Env:" + key + " -ErrorAction SilentlyContinue"
	}
	return strings.Join(parts, "; ") + "; "
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
