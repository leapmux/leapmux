package launch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/leapmux/leapmux/internal/util/id"

	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/leapmux/leapmux/util/procutil"
)

// WrapSpec describes how to wrap an agent binary launch inside the user's
// login shell. Grouping the knobs as one value keeps the nine provider call sites
// readable (named fields instead of a long positional tail like `..., nil, false,
// dir`) and makes the next per-launch knob a new field rather than another argument
// threaded through Wrap and the three dialect builders.
type WrapSpec struct {
	Shell      string // the user's shell path (terminal.ShellBaseName picks the dialect)
	LoginShell bool   // invoke the shell with interactive+login flags so profile scripts are sourced
	// Launch is how to start the provider's program, as Locator.Resolve settled
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
	Launch Spec
	// StripEnvKeys are removed by the shell wrapper before the binary is started.
	StripEnvKeys []string
	// BaseArgs are always passed to the program, after Launch.PrefixArgs.
	BaseArgs []string
	// EnvGated, when set, makes part of the launch depend on the environment the
	// user's shell sets up. nil keeps the simple path, with no runtime check and no
	// metadata line.
	EnvGated   *EnvGatedArgs
	WorkingDir string // cmd.Dir for the launched process
}

// EnvGatedArgs makes arguments depend on the environment that the user's shell
// sets up. The worker cannot answer that question from its own environment: a
// profile script can export a variable that only the shell sees.
//
// The wrapper checks, inside the shell and after the profile runs, whether any of
// EnvVars is set and non-empty. When one is, the program starts WITHOUT Args, and
// the preamble reports `MetaKey=false`. When none is, the program starts with
// Args after BaseArgs, and the preamble reports `MetaKey=true`. The caller reads
// the answer back through the preamble metadata. The report comes even when Args
// is empty, so a caller can probe the environment without sending anything.
//
// The wrapper writes EnvVars and MetaKey into shell code unquoted, so each must
// be a plain identifier; Wrap panics on any other value. An
// empty EnvVars has nothing to check: Args then join BaseArgs, and no metadata
// line is written.
type EnvGatedArgs struct {
	EnvVars []string // the variables whose presence withholds Args
	MetaKey string   // the preamble metadata key that reports whether Args were sent
	Args    []string // the arguments sent only when no variable in EnvVars is set
}

// shellIdentifier matches the names the wrapper writes into shell code unquoted.
var shellIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// checkEnvGate panics when gate holds a name that is unsafe to write into shell
// code unquoted. Every gate is a constant that its provider declares, so a bad
// name is a programming error that no input can reach.
func checkEnvGate(gate *EnvGatedArgs) {
	if !shellIdentifier.MatchString(gate.MetaKey) {
		panic(fmt.Sprintf("shell wrapper: env gate metadata key %q is not a plain identifier", gate.MetaKey))
	}
	for _, v := range gate.EnvVars {
		if !shellIdentifier.MatchString(v) {
			panic(fmt.Sprintf("shell wrapper: env gate variable %q is not a plain identifier", v))
		}
	}
}

// Wrap constructs an exec.Cmd that launches spec.Launch.Program
// inside the user's shell. When spec.LoginShell is true, the shell is invoked with
// interactive+login flags (e.g. -i -l -c) so that profile scripts are sourced. When
// false, only -c is used (no profile sourcing). When spec.EnvGated is nil, no
// conditional logic is emitted.
//
// It returns the command, a unique delimiter string, and a metadata line prefix.
// The caller should scan stdout for lines starting with metaPrefix to extract
// key=value metadata, then for the delimiter to detect the end of preamble.
func Wrap(ctx context.Context, spec WrapSpec) (*exec.Cmd, string, string) {
	// The interpreter arguments come FIRST: `node zcode.cjs app-server --stdio`, never
	// the reverse. Merging here rather than at each caller is what stops the next
	// bundled provider from losing them silently.
	spec.BaseArgs = append(append([]string{}, spec.Launch.PrefixArgs...), spec.BaseArgs...)
	if spec.EnvGated != nil {
		checkEnvGate(spec.EnvGated)
		if len(spec.EnvGated.EnvVars) == 0 {
			// Nothing to check, so the gated arguments are unconditional.
			spec.BaseArgs = append(spec.BaseArgs, spec.EnvGated.Args...)
			spec.EnvGated = nil
		}
	}
	token := id.Short()
	delimiter := "__LEAPMUX_READY_" + token + "__"
	metaPrefix := ""
	if spec.EnvGated != nil {
		metaPrefix = "__LEAPMUX_META_" + token + "__ "
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
// replaced. When spec.EnvGated is set, a conditional is emitted to check its
// env vars at runtime.
func buildPosixCommand(spec WrapSpec, delimiter, metaPrefix string) string {
	quotedBase := make([]string, len(spec.BaseArgs))
	for i, arg := range spec.BaseArgs {
		quotedBase[i] = posixQuote(arg)
	}

	baseArgsStr := strings.Join(quotedBase, " ")
	clearEnvPrefix := posixClearEnv(spec.StripEnvKeys)
	program := posixQuote(spec.Launch.Program)

	// Simple path: no env gate. When the gate is set but its Args are empty (Claude's
	// default-model launch), the conditional path below still runs: both branches exec
	// the binary with no extra args, differing only in the metadata line.
	gate := spec.EnvGated
	if gate == nil {
		return fmt.Sprintf("%secho '%s' && exec %s %s",
			clearEnvPrefix, delimiter, program, baseArgsStr)
	}

	// Conditional path: check env vars at runtime.
	quotedGated := make([]string, len(gate.Args))
	for i, arg := range gate.Args {
		quotedGated[i] = posixQuote(arg)
	}
	gatedArgsStr := strings.Join(quotedGated, " ")

	return fmt.Sprintf(
		"%s"+
			"if %s; then "+
			"echo '%s%s=false' && "+
			"echo '%s' && exec %s %s; "+
			"else "+
			"echo '%s%s=true' && "+
			"echo '%s' && exec %s %s %s; "+
			"fi",
		clearEnvPrefix, posixEnvCondition(gate.EnvVars),
		metaPrefix, gate.MetaKey, delimiter, program, baseArgsStr,
		metaPrefix, gate.MetaKey, delimiter, program, baseArgsStr, gatedArgsStr,
	)
}

// buildNuCommand builds the inner command string for Nushell.
func buildNuCommand(spec WrapSpec, delimiter, metaPrefix string) string {
	quotedBase := make([]string, len(spec.BaseArgs))
	for i, arg := range spec.BaseArgs {
		quotedBase[i] = nuQuote(arg)
	}

	baseArgsStr := strings.Join(quotedBase, " ")
	clearEnvPrefix := nuClearEnv(spec.StripEnvKeys)
	// `^"<program>"` is Nushell's documented form for running an external
	// command whose path holds a space.
	program := nuQuote(spec.Launch.Program)

	// Simple path: no env gate. See buildPosixCommand.
	gate := spec.EnvGated
	if gate == nil {
		return fmt.Sprintf("%secho '%s'; ^%s %s",
			clearEnvPrefix, delimiter, program, baseArgsStr)
	}

	// Conditional path.
	quotedGated := make([]string, len(gate.Args))
	for i, arg := range gate.Args {
		quotedGated[i] = nuQuote(arg)
	}
	gatedArgsStr := strings.Join(quotedGated, " ")

	return fmt.Sprintf(
		"%s"+
			"if (%s) { "+
			"echo '%s%s=false'; "+
			"echo '%s'; ^%s %s "+
			"} else { "+
			"echo '%s%s=true'; "+
			"echo '%s'; ^%s %s %s "+
			"}",
		clearEnvPrefix, nuEnvCondition(gate.EnvVars),
		metaPrefix, gate.MetaKey, delimiter, program, baseArgsStr,
		metaPrefix, gate.MetaKey, delimiter, program, baseArgsStr, gatedArgsStr,
	)
}

// buildPwshCommand builds the inner command string for PowerShell.
func buildPwshCommand(spec WrapSpec, delimiter, metaPrefix string) string {
	quotedBase := make([]string, len(spec.BaseArgs))
	for i, arg := range spec.BaseArgs {
		quotedBase[i] = pwshQuote(arg)
	}

	baseArgsStr := strings.Join(quotedBase, " ")
	clearEnvPrefix := pwshClearEnv(spec.StripEnvKeys)
	// The call operator takes a quoted string, which is PowerShell's documented
	// form for running a path that holds a space.
	program := pwshQuote(spec.Launch.Program)

	// Simple path: no env gate. See buildPosixCommand.
	gate := spec.EnvGated
	if gate == nil {
		return fmt.Sprintf("%sWrite-Output '%s'; & %s %s",
			clearEnvPrefix, delimiter, program, baseArgsStr)
	}

	// Conditional path.
	quotedGated := make([]string, len(gate.Args))
	for i, arg := range gate.Args {
		quotedGated[i] = pwshQuote(arg)
	}
	gatedArgsStr := strings.Join(quotedGated, " ")

	return fmt.Sprintf(
		"%s"+
			"if (%s) { "+
			"Write-Output '%s%s=false'; "+
			"Write-Output '%s'; & %s %s "+
			"} else { "+
			"Write-Output '%s%s=true'; "+
			"Write-Output '%s'; & %s %s %s "+
			"}",
		clearEnvPrefix, pwshEnvCondition(gate.EnvVars),
		metaPrefix, gate.MetaKey, delimiter, program, baseArgsStr,
		metaPrefix, gate.MetaKey, delimiter, program, baseArgsStr, gatedArgsStr,
	)
}

// buildCshCommand builds the inner command string for tcsh and csh.
//
// csh is NOT a POSIX shell, and the POSIX builder emits three things it cannot run.
// `unset` there removes a shell variable, not an environment one, so a StripEnvKeys
// entry survived into the agent's environment; `unsetenv` is the csh spelling.
// `if ... then ... fi` needs csh's own `if (...) then ... else ... endif` form. And
// `[ -n "$VAR" ]` on an UNSET name is a hard error -- csh answers
// `VAR: Undefined variable.` -- which every env-gated launch hit, because a gate
// always takes the conditional path.
//
// The command is MULTI-LINE on purpose, and cshEnvSeed says why: csh substitutes each
// line's variables before it evaluates that line, so a guard and the read it guards
// cannot share one line.
//
// The program word and the arguments keep POSIX quoting: csh reads the same single
// quotes, and it has no `'\”` escape, which is why posixQuote's output is used as-is
// and an argument holding a single quote is out of reach for both shells alike.
func buildCshCommand(spec WrapSpec, delimiter, metaPrefix string) string {
	quotedBase := make([]string, len(spec.BaseArgs))
	for i, arg := range spec.BaseArgs {
		quotedBase[i] = posixQuote(arg)
	}

	baseArgsStr := strings.Join(quotedBase, " ")
	clearEnvPrefix := cshClearEnv(spec.StripEnvKeys)
	program := posixQuote(spec.Launch.Program)

	// Simple path: no env gate. See buildPosixCommand.
	gate := spec.EnvGated
	if gate == nil {
		return fmt.Sprintf("%secho '%s' && exec %s %s",
			clearEnvPrefix, delimiter, program, baseArgsStr)
	}

	// Conditional path: check env vars at runtime.
	quotedGated := make([]string, len(gate.Args))
	for i, arg := range gate.Args {
		quotedGated[i] = posixQuote(arg)
	}
	gatedArgsStr := strings.Join(quotedGated, " ")

	return fmt.Sprintf(
		"%s%s"+
			"if ( $"+cshEnvGateVar+" ) then\n"+
			"echo '%s%s=false' && "+
			"echo '%s' && exec %s %s\n"+
			"else\n"+
			"echo '%s%s=true' && "+
			"echo '%s' && exec %s %s %s\n"+
			"endif",
		clearEnvPrefix, cshEnvSeed(gate.EnvVars),
		metaPrefix, gate.MetaKey, delimiter, program, baseArgsStr,
		metaPrefix, gate.MetaKey, delimiter, program, baseArgsStr, gatedArgsStr,
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
// whether any of vars is set and non-empty.
// e.g. `[ -n "$VAR1" ] || [ -n "$VAR2" ] || [ -n "$VAR3" ]`
func posixEnvCondition(vars []string) string {
	parts := make([]string, len(vars))
	for i, v := range vars {
		parts[i] = fmt.Sprintf(`[ -n "$%s" ]`, v)
	}
	return strings.Join(parts, " || ")
}

// cshEnvGateVar is the csh SHELL variable that carries the env gate's answer. `set`
// creates a shell variable, never an environment one, so it does not reach the agent.
const cshEnvGateVar = "_leapmux_env_gate"

// cshEnvSeed builds the csh lines that set cshEnvGateVar to 1 when any of vars is set
// AND non-empty -- the same question POSIX asks with `[ -n "$VAR" ]`.
//
// It takes several LINES, and a one-line form is impossible here. csh substitutes every
// variable on a line before it evaluates any of that line, so `$?VAR` cannot guard a
// `"$VAR"` beside it: `if ( $?VAR && "$VAR" != "" )` still expands `$VAR` on an unset
// name, prints `VAR: Undefined variable.` and answers wrongly. Only a separate line puts
// the read after the guard, which is why each variable costs a three-line block.
func cshEnvSeed(vars []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "set %s = 0\n", cshEnvGateVar)
	for _, v := range vars {
		fmt.Fprintf(&b, "if ( $?%s ) then\n", v)
		fmt.Fprintf(&b, "if ( \"$%s\" != \"\" ) set %s = 1\n", v, cshEnvGateVar)
		b.WriteString("endif\n")
	}
	return b.String()
}

// nuEnvCondition builds a Nushell conditional expression that checks
// whether any of vars is set and non-empty.
// e.g. `($env | get -i VAR1 | default "") != "" or ...`
func nuEnvCondition(vars []string) string {
	parts := make([]string, len(vars))
	for i, v := range vars {
		parts[i] = fmt.Sprintf("($env | get -i %s | default '') != ''", v)
	}
	return strings.Join(parts, " or ")
}

// pwshEnvCondition builds a PowerShell conditional expression that checks
// whether any of vars is set and non-empty.
// e.g. `$env:VAR1 -or $env:VAR2 -or $env:VAR3`
func pwshEnvCondition(vars []string) string {
	parts := make([]string, len(vars))
	for i, v := range vars {
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
