package agent

import (
	"context"
	"fmt"
	"os"
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
	// Launch is how to start the provider's program, as resolveProviderLaunch settled
	// it. Every caller passes one, so a bundled provider's interpreter arguments and
	// environment cannot be forgotten at a call site: this function applies both.
	//
	// Launch.Program is either a bare name the shell resolves through PATH ("claude",
	// "codex") or an absolute path (ZCode's resolved Node interpreter). Each dialect
	// builder QUOTES it, so a path that holds a space -- `C:\Program Files\nodejs\node.exe`
	// is the common one -- reaches the program as one word.
	//
	// Quoting costs nothing for a bare name: every dialect still resolves it through
	// PATH. What quoting does NOT settle is whether a user's own alias or shell
	// function of the same name wins, and the answer differs per dialect. POSIX
	// `exec 'claude'` runs an executable file, and a quoted word is never
	// alias-expanded. Nushell `^"claude"` forces an external command, which `^` alone
	// already did. PowerShell `& 'claude'` still resolves through the session command
	// table -- alias, then function, then cmdlet, then the PATH executable -- and the
	// profile IS sourced, because neither terminal.CommandArgs nor LoginShellArgs
	// passes -NoProfile. So on PowerShell a profile-defined `claude` wrapper is what
	// starts, exactly as before the quoting.
	Launch launchSpec
	// StripEnvKeys are removed by the shell wrapper before the binary is started.
	StripEnvKeys []string
	// BaseArgs are always passed to the program, after Launch.PrefixArgs.
	BaseArgs []string
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

// buildShellWrappedCommand constructs an exec.Cmd that launches spec.Launch.Program
// inside the user's shell. When spec.LoginShell is true, the shell is invoked with
// interactive+login flags (e.g. -i -l -c) so that profile scripts are sourced. When
// false, only -c is used (no profile sourcing). When both spec.ModelEffortArgs is
// empty and spec.ProbeThirdParty is false, no conditional logic is emitted.
//
// It returns the command, a unique delimiter string, and a metadata line prefix.
// The caller should scan stdout for lines starting with metaPrefix to extract
// key=value metadata, then for the delimiter to detect the end of preamble.
func buildShellWrappedCommand(ctx context.Context, spec shellWrapSpec) (*exec.Cmd, string, string) {
	// The interpreter arguments come FIRST: `node zcode.cjs app-server --stdio`, never
	// the reverse. Merging here rather than at each caller is what stops the next
	// bundled provider from losing them silently.
	spec.BaseArgs = append(append([]string{}, spec.Launch.PrefixArgs...), spec.BaseArgs...)
	id := generateRequestID()
	delimiter := "__LEAPMUX_READY_" + id + "__"
	metaPrefix := ""
	if len(spec.ModelEffortArgs) > 0 || spec.ProbeThirdParty {
		metaPrefix = "__LEAPMUX_META_" + id + "__ "
	}
	shellName := terminal.ShellBaseName(spec.Shell)

	var inner, flag string
	switch {
	case terminal.IsPwsh(shellName):
		inner = buildPwshCommand(spec, delimiter, metaPrefix)
		flag = "-Command"
	case shellName == "nu":
		inner = buildNuCommand(spec, delimiter, metaPrefix)
		flag = "-c"
	case shellName == "tcsh" || shellName == "csh":
		inner = buildCshCommand(spec, delimiter, metaPrefix)
		flag = "-c"
	default:
		// bash, zsh, fish, sh, ash, dash, ksh, xonsh, and unknown shells
		inner = buildPosixCommand(spec, delimiter, metaPrefix)
		flag = "-c"
	}
	cmdArgs := terminal.CommandArgs(spec.Shell, spec.LoginShell, flag, inner)

	cmd := exec.CommandContext(ctx, spec.Shell, cmdArgs...)
	cmd.Dir = spec.WorkingDir
	// Seed the environment with what the LAUNCH requires (ELECTRON_RUN_AS_NODE for
	// ZCode's Electron-as-Node runtime). Every caller builds its own env from
	// cmd.Environ() or cmd.Env and finishes with FinalizeAgentEnv, so seeding it here
	// carries the requirement through all of them -- and FinalizeAgentEnv touches only
	// the agent-identity and LEAPMUX_CONTROL_ keys, so it cannot drop one.
	if len(spec.Launch.Env) > 0 {
		cmd.Env = append(os.Environ(), spec.Launch.Env...)
	}
	envutil.ScrubAppImageEnv(cmd)
	procutil.HideConsoleWindow(cmd)
	procutil.DetachFromTerminal(cmd)
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
	program := posixQuote(spec.Launch.Program)

	// Simple path: no probe wanted (empty metaPrefix means neither model/effort
	// args nor a forced third-party probe). When metaPrefix is set but
	// ModelEffortArgs is empty (Claude's default-model launch), the conditional
	// path below still runs: both branches exec the binary with no extra args,
	// differing only in the can_change_model_and_effort line.
	if metaPrefix == "" {
		return fmt.Sprintf("%secho '%s' && exec %s %s",
			clearEnvPrefix, delimiter, program, baseArgsStr)
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
		metaPrefix, delimiter, program, baseArgsStr,
		metaPrefix, delimiter, program, baseArgsStr, meArgsStr,
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
	// `^"<program>"` is Nushell's documented form for running an external
	// command whose path holds a space.
	program := nuQuote(spec.Launch.Program)

	// Simple path: no probe wanted (empty metaPrefix). See buildPosixCommand.
	if metaPrefix == "" {
		return fmt.Sprintf("%secho '%s'; ^%s %s",
			clearEnvPrefix, delimiter, program, baseArgsStr)
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
		metaPrefix, delimiter, program, baseArgsStr,
		metaPrefix, delimiter, program, baseArgsStr, meArgsStr,
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
	// The call operator takes a quoted string, which is PowerShell's documented
	// form for running a path that holds a space.
	program := pwshQuote(spec.Launch.Program)

	// Simple path: no probe wanted (empty metaPrefix). See buildPosixCommand.
	if metaPrefix == "" {
		return fmt.Sprintf("%sWrite-Output '%s'; & %s %s",
			clearEnvPrefix, delimiter, program, baseArgsStr)
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
		metaPrefix, delimiter, program, baseArgsStr,
		metaPrefix, delimiter, program, baseArgsStr, meArgsStr,
	)
}

// buildCshCommand builds the inner command string for tcsh and csh.
//
// csh is NOT a POSIX shell, and the POSIX builder emits three things it cannot run.
// `unset` there removes a shell variable, not an environment one, so a StripEnvKeys
// entry survived into the agent's environment; `unsetenv` is the csh spelling.
// `if ... then ... fi` needs csh's own `if (...) then ... else ... endif` form. And
// `[ -n "$VAR" ]` on an UNSET name is a hard error -- csh answers
// `VAR: Undefined variable.` -- which every default Claude launch hit, because
// ProbeThirdParty always takes the conditional path.
//
// The command is MULTI-LINE on purpose, and cshEnvSeed says why: csh substitutes each
// line's variables before it evaluates that line, so a guard and the read it guards
// cannot share one line.
//
// The program word and the arguments keep POSIX quoting: csh reads the same single
// quotes, and it has no `'\”` escape, which is why posixQuote's output is used as-is
// and an argument holding a single quote is out of reach for both shells alike.
func buildCshCommand(spec shellWrapSpec, delimiter, metaPrefix string) string {
	quotedBase := make([]string, len(spec.BaseArgs))
	for i, arg := range spec.BaseArgs {
		quotedBase[i] = posixQuote(arg)
	}

	baseArgsStr := strings.Join(quotedBase, " ")
	clearEnvPrefix := cshClearEnv(spec.StripEnvKeys)
	program := posixQuote(spec.Launch.Program)

	// Simple path: no probe wanted. See buildPosixCommand.
	if metaPrefix == "" {
		return fmt.Sprintf("%secho '%s' && exec %s %s",
			clearEnvPrefix, delimiter, program, baseArgsStr)
	}

	// Conditional path: check env vars at runtime.
	quotedME := make([]string, len(spec.ModelEffortArgs))
	for i, arg := range spec.ModelEffortArgs {
		quotedME[i] = posixQuote(arg)
	}
	meArgsStr := strings.Join(quotedME, " ")

	return fmt.Sprintf(
		"%s"+cshEnvSeed()+
			"if ( $"+cshThirdPartyVar+" ) then\n"+
			"echo '%scan_change_model_and_effort=false' && "+
			"echo '%s' && exec %s %s\n"+
			"else\n"+
			"echo '%scan_change_model_and_effort=true' && "+
			"echo '%s' && exec %s %s %s\n"+
			"endif",
		clearEnvPrefix,
		metaPrefix, delimiter, program, baseArgsStr,
		metaPrefix, delimiter, program, baseArgsStr, meArgsStr,
	)
}

func posixClearEnv(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	return "unset " + strings.Join(keys, " ") + " && "
}

// cshClearEnv removes environment variables the csh way. `unset` there touches shell
// variables only, so `unsetenv` is the one that reaches the launched program. csh takes
// ONE name per unsetenv, unlike the POSIX form.
func cshClearEnv(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	parts := make([]string, len(keys))
	for i, key := range keys {
		parts[i] = "unsetenv " + key
	}
	return strings.Join(parts, "; ") + "; "
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

// cshThirdPartyVar is the csh SHELL variable that carries the probe's answer. `set`
// creates a shell variable, never an environment one, so it does not reach the agent.
const cshThirdPartyVar = "_leapmux_third_party"

// cshEnvSeed builds the csh lines that set cshThirdPartyVar to 1 when any third-party
// provider env var is set AND non-empty -- the same question POSIX asks with
// `[ -n "$VAR" ]`.
//
// It takes several LINES, and a one-line form is impossible here. csh substitutes every
// variable on a line before it evaluates any of that line, so `$?VAR` cannot guard a
// `"$VAR"` beside it: `if ( $?VAR && "$VAR" != "" )` still expands `$VAR` on an unset
// name, prints `VAR: Undefined variable.` and answers wrongly. Only a separate line puts
// the read after the guard, which is why each variable costs a three-line block.
func cshEnvSeed() string {
	var b strings.Builder
	fmt.Fprintf(&b, "set %s = 0\n", cshThirdPartyVar)
	for _, v := range thirdPartyProviderEnvVars {
		fmt.Fprintf(&b, "if ( $?%s ) then\n", v)
		fmt.Fprintf(&b, "if ( \"$%s\" != \"\" ) set %s = 1\n", v, cshThirdPartyVar)
		b.WriteString("endif\n")
	}
	return b.String()
}

// nuEnvCondition builds a Nushell conditional expression that checks
// whether any third-party provider env var is set.
// e.g. `($env | get -i VAR1 | default "") != "" or ...`
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
