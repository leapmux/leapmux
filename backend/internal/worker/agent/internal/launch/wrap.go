package launch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
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
	// SetEnv are set by the shell wrapper after the user's profile runs and
	// after StripEnvKeys are removed, just before the binary starts. Each entry
	// is `NAME=VALUE`. A value set here wins over one that the profile exports,
	// so a provider states here a value that must reach the program unchanged:
	// the private address of a daemon, for example. A value on cmd.Env instead
	// reaches the shell first, and the profile can replace it. Wrap panics on a
	// name that is not a plain identifier, because the wrapper writes the name
	// into shell code unquoted. The value is quoted for the shell's dialect.
	SetEnv []string
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
	checkSetEnv(spec.SetEnv)
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
	clearEnvPrefix := posixClearEnv(spec.StripEnvKeys) + posixSetEnv(spec.SetEnv)
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
	clearEnvPrefix := nuClearEnv(spec.StripEnvKeys) + nuSetEnv(spec.SetEnv)
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
	clearEnvPrefix := pwshClearEnv(spec.StripEnvKeys) + pwshSetEnv(spec.SetEnv)
	baseCommand := pwshInvocation(spec.Launch.Program, spec.BaseArgs)

	// Preserve native exit codes and report an absent command as 127, as the
	// POSIX wrapper does. Native argument encoding belongs to the invocation.
	gate := spec.EnvGated
	if gate == nil {
		return fmt.Sprintf("$ErrorActionPreference = 'Stop'; %stry { Write-Output '%s'; %s } catch [System.Management.Automation.CommandNotFoundException] { exit 127 }",
			clearEnvPrefix, delimiter, baseCommand)
	}

	commandWithOptionalArgs := pwshInvocation(spec.Launch.Program, slices.Concat(spec.BaseArgs, gate.Args))
	return fmt.Sprintf(
		"$ErrorActionPreference = 'Stop'; %s"+
			"try { "+
			"if (%s) { "+
			"Write-Output '%s%s=false'; "+
			"Write-Output '%s'; %s "+
			"} else { "+
			"Write-Output '%s%s=true'; "+
			"Write-Output '%s'; %s "+
			"} "+
			"} catch [System.Management.Automation.CommandNotFoundException] { exit 127 }",
		clearEnvPrefix, pwshEnvCondition(gate.EnvVars),
		metaPrefix, gate.MetaKey, delimiter, baseCommand,
		metaPrefix, gate.MetaKey, delimiter, commandWithOptionalArgs,
	)
}

// pwshInvocation preserves native arguments under every PowerShell argument
// passing mode. Legacy passing concatenates argument values into a command
// line without escaping embedded quotes, and drops an empty argument. Supply
// complete native command-line tokens and select Legacy within this invocation
// so its parser reverses that encoding on both Windows and Unix.
//
// A profile may supply a function or an alias instead of an executable. Resolve
// aliases before deciding whether the target is native. Functions and scripts
// receive the original arguments and keep the profile's argument passing mode.
func pwshInvocation(program string, arguments []string) string {
	original := make([]string, len(arguments))
	native := make([]string, len(arguments))
	for index, argument := range arguments {
		original[index] = pwshQuote(argument)
		native[index] = pwshQuote(legacyNativeArgument(argument))
	}
	command := pwshQuote(program)
	// Metadata lookup parses filesystem paths as wildcard patterns. Read an
	// existing literal file directly instead. Namespace-only GetCommands keeps
	// loaded aliases, functions and cmdlets ahead of files without importing a
	// module that would otherwise lose to a native executable. Full lookup for
	// bare names retains module discovery and implicit .ps1 resolution on PATH.
	return fmt.Sprintf(
		"& { $leapmuxTarget = %s; "+
			"$leapmuxAliases = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::OrdinalIgnoreCase); "+
			"while ($true) { if (-not $leapmuxAliases.Add($leapmuxTarget)) { exit 127 }; "+
			"$leapmuxCommands = @($ExecutionContext.InvokeCommand.GetCommands($leapmuxTarget, ([System.Management.Automation.CommandTypes]::Alias -bor [System.Management.Automation.CommandTypes]::Function -bor [System.Management.Automation.CommandTypes]::Filter -bor [System.Management.Automation.CommandTypes]::Cmdlet), $false)); "+
			"if ($leapmuxCommands.Count -gt 0) { $leapmuxCommand = $leapmuxCommands[0] } else { "+
			"$leapmuxPath = $null; "+
			"if ([System.IO.Path]::IsPathRooted($leapmuxTarget)) { $leapmuxPath = $leapmuxTarget } "+
			"elseif ($leapmuxTarget.Contains('/') -or $leapmuxTarget.IndexOf([char]0x5c) -ge 0) { $leapmuxPath = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($leapmuxTarget) }; "+
			"if ($null -ne $leapmuxPath -and [System.IO.File]::Exists($leapmuxPath)) { $leapmuxNative = [System.IO.Path]::GetExtension($leapmuxPath) -ine '.ps1'; break }; "+
			"if ($null -ne $leapmuxPath -and [System.IO.File]::Exists($leapmuxPath + '.ps1')) { $leapmuxNative = $false; break }; "+
			"$leapmuxCommand = $ExecutionContext.InvokeCommand.GetCommand($leapmuxTarget, [System.Management.Automation.CommandTypes]::All); "+
			"if ($null -eq $leapmuxCommand) { exit 127 } }; "+
			"if ($leapmuxCommand -is [System.Management.Automation.AliasInfo]) { $leapmuxTarget = $leapmuxCommand.Definition; continue }; "+
			"$leapmuxNative = $leapmuxCommand.CommandType -eq [System.Management.Automation.CommandTypes]::Application; break }; "+
			"if ($leapmuxNative) { "+
			"$PSNativeCommandArgumentPassing = 'Legacy'; & %s %s "+
			"} else { & %s %s }; exit $LASTEXITCODE }",
		command, command, strings.Join(native, " "), command, strings.Join(original, " "),
	)
}

// legacyNativeArgument quotes one token for the Windows native command-line
// parser, which .NET also uses for ProcessStartInfo.Arguments on Unix. A run of
// backslashes doubles before a quote or the closing delimiter, and a literal
// quote needs one additional backslash.
func legacyNativeArgument(argument string) string {
	var token strings.Builder
	token.Grow(len(argument) + 2)
	token.WriteByte('"')
	backslashes := 0
	for index := 0; index < len(argument); index++ {
		character := argument[index]
		if character == '\\' {
			backslashes++
			continue
		}
		if character == '"' {
			token.WriteString(strings.Repeat("\\", 2*backslashes+1))
		} else {
			token.WriteString(strings.Repeat("\\", backslashes))
		}
		token.WriteByte(character)
		backslashes = 0
	}
	token.WriteString(strings.Repeat("\\", 2*backslashes))
	token.WriteByte('"')
	return token.String()
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
// The program word and the arguments use posixQuote. Both csh and tcsh accept
// its concatenation of quoted segments and escaped literal single quotes.
func buildCshCommand(spec WrapSpec, delimiter, metaPrefix string) string {
	quotedBase := make([]string, len(spec.BaseArgs))
	for i, arg := range spec.BaseArgs {
		quotedBase[i] = posixQuote(arg)
	}

	baseArgsStr := strings.Join(quotedBase, " ")
	clearEnvPrefix := cshClearEnv(spec.StripEnvKeys) + cshSetEnv(spec.SetEnv)
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

// checkSetEnv panics when an entry of SetEnv is not `NAME=VALUE` with a plain
// identifier as its name. Each provider states the names as constants, so a
// bad one is a programming error that no input can reach.
func checkSetEnv(entries []string) {
	for _, entry := range entries {
		name, _, found := strings.Cut(entry, "=")
		if !found || !shellIdentifier.MatchString(name) {
			panic(fmt.Sprintf("shell wrapper: SetEnv entry %q is not NAME=VALUE with a plain identifier", entry))
		}
	}
}

// posixSetEnv exports each entry. One export for each entry, because fish
// takes `export` as a function of one assignment at a time.
func posixSetEnv(entries []string) string {
	var b strings.Builder
	for _, entry := range entries {
		name, value, _ := strings.Cut(entry, "=")
		b.WriteString("export " + name + "=" + posixQuote(value) + " && ")
	}
	return b.String()
}

// cshSetEnv sets each entry the csh way. `set` touches shell variables only, so
// `setenv` is the one that reaches the launched program.
func cshSetEnv(entries []string) string {
	var b strings.Builder
	for _, entry := range entries {
		name, value, _ := strings.Cut(entry, "=")
		b.WriteString("setenv " + name + " " + posixQuote(value) + "; ")
	}
	return b.String()
}

func nuSetEnv(entries []string) string {
	var b strings.Builder
	for _, entry := range entries {
		name, value, _ := strings.Cut(entry, "=")
		b.WriteString("$env." + name + " = " + nuQuote(value) + "; ")
	}
	return b.String()
}

func pwshSetEnv(entries []string) string {
	var b strings.Builder
	for _, entry := range entries {
		name, value, _ := strings.Cut(entry, "=")
		b.WriteString("$env:" + name + " = " + pwshQuote(value) + "; ")
	}
	return b.String()
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
// It closes the quoted segment, emits a backslash-escaped single quote, and
// starts another quoted segment for each single quote in the input.
func posixQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// nuQuote wraps a string in double quotes for Nushell.
// In Nushell double-quoted strings, only \ and " need escaping.
var nuReplacer = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

func nuQuote(s string) string {
	return `"` + nuReplacer.Replace(s) + `"`
}

// pwshQuoteReplacer escapes every single-quote delimiter PowerShell accepts.
var pwshQuoteReplacer = strings.NewReplacer(
	"'", "''",
	"\u2018", "\u2018\u2018",
	"\u2019", "\u2019\u2019",
	"\u201a", "\u201a\u201a",
	"\u201b", "\u201b\u201b",
)

// pwshQuote wraps a string in single quotes for PowerShell. Its parser treats
// ASCII and typographic single quotes as delimiters, so double each delimiter.
func pwshQuote(s string) string {
	return "'" + pwshQuoteReplacer.Replace(s) + "'"
}
