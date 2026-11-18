package launch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/leapmux/leapmux/internal/util/id"

	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/util/shellquote"
	"github.com/leapmux/leapmux/internal/worker/terminal"
)

// WrapSpec describes a provider invocation in the Worker's configured shell.
type WrapSpec struct {
	Shell      string // the user's shell path (terminal.ShellBaseName picks the dialect)
	LoginShell bool   // invoke the shell with interactive+login flags so profile scripts are sourced
	// Environment is the initial shell environment. Nil inherits the Worker environment.
	Environment []string
	// Launch selects an ordered shell command or invokes an explicit program.
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
	// into shell code unquoted. Values travel through private environment variables.
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

// PreparedLaunch owns the optional files required by a wrapped process.
type PreparedLaunch struct {
	Cmd             *exec.Cmd
	Delimiter       string
	MetaPrefix      string
	candidates      []string
	exhaustedMarker string
	closeFiles      func() error
}

// Close releases files after the native process finishes or preparation fails.
func (p *PreparedLaunch) Close() error {
	if p == nil || p.closeFiles == nil {
		return nil
	}
	return p.closeFiles()
}

// Wrap prepares an invocation in the configured shell after its profile runs.
func Wrap(ctx context.Context, spec WrapSpec) (*PreparedLaunch, error) {
	if spec.Launch.Program == "" && !validCandidates(spec.Launch.candidates) ||
		spec.Launch.Program != "" && len(spec.Launch.candidates) != 0 {
		return nil, errors.New("the provider launch requires one program or an ordered command list")
	}
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
	prepared := &PreparedLaunch{
		Delimiter:       "__LEAPMUX_READY_" + token + "__",
		exhaustedMarker: "__LEAPMUX_EXHAUSTED_" + token + "__",
		candidates:      slices.Clone(spec.Launch.candidates),
	}
	if spec.EnvGated != nil {
		prepared.MetaPrefix = "__LEAPMUX_META_" + token + "__ "
	}
	shell := terminal.ShellBaseName(spec.Shell)
	if shell == "cmd" {
		return nil, errors.New("cmd.exe cannot launch agents. Configure a PowerShell or Bash shell for the Worker")
	}

	cmd, _ := newShellCommandWithEnvironment(ctx, spec.Shell, spec.LoginShell, "", spec.WorkingDir, spec.Environment)
	if len(spec.Launch.Env) > 0 {
		cmd.Env = append(cmd.Environ(), spec.Launch.Env...)
	}
	envutil.ScrubAppImageEnv(cmd)
	environment := cmd.Environ()
	shellSpec := prepareShellLaunch(spec, token, environment)
	if len(shellSpec.pins) > 0 {
		cmd.Env = shellSpec.pinEnvironment(environment)
	}

	var script string
	switch {
	case terminal.IsPwsh(shell):
		script = buildPwshCommand(shellSpec, prepared)
	case shell == "nu" && len(prepared.candidates) != 0:
		scripts, err := prepared.writeNuInvocations()
		if err != nil {
			return nil, err
		}
		script = buildNuSelection(shellSpec, prepared, scripts)
	case shell == "nu":
		script = "print ''; " + buildNuCommand(shellSpec, prepared.Delimiter, prepared.MetaPrefix)
	case shell == "fish":
		script = buildFishCommand(shellSpec, prepared)
	case shell == "xonsh":
		script = buildXonshCommand(shellSpec, prepared)
	case isCsh(shell) && len(prepared.candidates) != 0:
		script = buildCshSelection(shellSpec, prepared)
	case isCsh(shell):
		script = lookupSetup(shell) + "echo ''\n" + buildCshCommand(shellSpec, prepared.Delimiter, prepared.MetaPrefix)
	case len(prepared.candidates) != 0:
		script = buildPosixSelection(shell, shellSpec, prepared)
	default:
		script = posixBuiltin(shell, "printf") + " '\\n'\n" + buildPosixCommand(shellSpec, prepared.Delimiter, prepared.MetaPrefix)
	}

	cmd.Args[len(cmd.Args)-1] = script
	prepared.Cmd = cmd
	return prepared, nil
}

func (p *PreparedLaunch) writeNuInvocations() (map[string]string, error) {
	dir, err := os.MkdirTemp("", "leapmux-nu-launch-")
	if err != nil {
		return nil, fmt.Errorf("create Nushell invocation directory: %w", err)
	}
	p.closeFiles = sync.OnceValue(func() error { return os.RemoveAll(dir) })
	scripts := make(map[string]string, len(p.candidates))
	for _, name := range p.candidates {
		path := filepath.Join(dir, name+".nu")
		script := "def main [arguments: list<string>] { " + name + " ...$arguments }\n"
		if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
			return nil, errors.Join(fmt.Errorf("write Nushell invocation: %w", err), p.Close())
		}
		scripts[name] = path
	}
	return scripts, nil
}

// buildPosixCommand builds the inner command string for POSIX-like shells.
// The command is always prefixed with `exec` so the shell process is
// replaced. When spec.EnvGated is set, a conditional is emitted to check its
// env vars at runtime.
func buildPosixCommand(spec shellLaunchSpec, delimiter, metaPrefix string) string {
	quotedBase := make([]string, len(spec.BaseArgs))
	for i, arg := range spec.BaseArgs {
		quotedBase[i] = shellquote.POSIX.Quote(arg)
	}

	baseArgsStr := strings.Join(quotedBase, " ")
	clearEnvPrefix := posixClearEnv(spec.StripEnvKeys) + posixSetEnv(spec.pins)
	program := shellquote.POSIX.Quote(spec.Launch.Program)

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
		quotedGated[i] = shellquote.POSIX.Quote(arg)
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
func buildNuCommand(spec shellLaunchSpec, delimiter, metaPrefix string) string {
	quotedBase := make([]string, len(spec.BaseArgs))
	for i, arg := range spec.BaseArgs {
		quotedBase[i] = nuQuote(arg)
	}

	baseArgsStr := strings.Join(quotedBase, " ")
	clearEnvPrefix := nuClearEnv(spec.StripEnvKeys) + nuSetEnv(spec.pins)
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
// The program word and the arguments use shellquote.POSIX.Quote. Both csh and tcsh accept
// its concatenation of quoted segments and escaped literal single quotes.
func buildCshCommand(spec shellLaunchSpec, delimiter, metaPrefix string) string {
	quotedBase := make([]string, len(spec.BaseArgs))
	for i, arg := range spec.BaseArgs {
		quotedBase[i] = cshQuote(arg)
	}

	baseArgsStr := strings.Join(quotedBase, " ")
	clearEnvPrefix := cshClearEnv(spec.StripEnvKeys) + cshSetEnv(spec.pins)
	program := cshQuote(spec.Launch.Program)

	// Simple path: no env gate. See buildPosixCommand.
	gate := spec.EnvGated
	if gate == nil {
		return fmt.Sprintf("%secho '%s' && exec %s %s",
			clearEnvPrefix, delimiter, program, baseArgsStr)
	}

	// Conditional path: check env vars at runtime.
	quotedGated := make([]string, len(gate.Args))
	for i, arg := range gate.Args {
		quotedGated[i] = cshQuote(arg)
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

// posixSetEnv exports each entry with its value quoted for the shell.
func posixSetEnv(pins []environmentPin) string {
	var b strings.Builder
	for _, pin := range pins {
		b.WriteString("export " + pin.target + "=\"$" + pin.source + "\" && unset " + pin.source + " && ")
	}
	return b.String()
}

// cshSetEnv sets each entry the csh way. `set` touches shell variables only, so
// `setenv` is the one that reaches the launched program.
func cshSetEnv(pins []environmentPin) string {
	var b strings.Builder
	for _, pin := range pins {
		b.WriteString("setenv " + pin.target + " ${" + pin.source + ":q}; unsetenv " + pin.source + "; ")
	}
	return b.String()
}

func nuSetEnv(pins []environmentPin) string {
	var b strings.Builder
	for _, pin := range pins {
		b.WriteString("$env." + pin.target + " = $env." + pin.source + "; hide-env " + pin.source + "; ")
	}
	return b.String()
}

func pwshSetEnv(pins []environmentPin) string {
	var b strings.Builder
	for _, pin := range pins {
		b.WriteString("$env:" + pin.target + " = $env:" + pin.source + "; Remove-Item -LiteralPath 'Env:" + pin.source + "' -ErrorAction SilentlyContinue; ")
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
		parts[i] = "hide-env -i " + key
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

// nuQuote wraps a string in double quotes for Nushell.
// In Nushell double-quoted strings, only \ and " need escaping.
var nuReplacer = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

func nuQuote(s string) string {
	return `"` + nuReplacer.Replace(s) + `"`
}
