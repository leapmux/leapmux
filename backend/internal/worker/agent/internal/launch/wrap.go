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

	"github.com/leapmux/leapmux/internal/util/envprobe"
	"github.com/leapmux/leapmux/internal/util/id"

	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/util/shellquote"
	"github.com/leapmux/leapmux/internal/worker/terminal"
)

// WrapSpec describes a provider invocation in the Worker's configured shell.
type WrapSpec struct {
	Shell      string // the user's shell path (terminal.ShellBaseName picks the dialect)
	LoginShell bool   // invoke the shell with interactive+login flags so profile scripts are sourced
	// Environment supplies the shell's initial environment. Nil inherits the
	// Worker environment. A non-nil empty slice supplies an empty environment.
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
	// ObserveEnvironment requests an environment observation record in the
	// preamble. The probe runs after profile and environment pins, before
	// provider exec. The observation is the launch-specific metadata prefix
	// followed by `environment=<base64url JSON Snapshot>`.
	ObserveEnvironment *ObservationRequest
}

// ObservationRequest configures the environment observation emitted in the
// launch preamble. Dirac uses this to discover the effective profile root.
type ObservationRequest struct {
	// Variables are the environment variable names to capture.
	Variables []string
	// PathVariables maps an environment variable to trim characters for
	// resolved path output.
	PathVariables map[string]string
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
	Cmd              *exec.Cmd
	Delimiter        string
	MetaPrefix       string
	candidates       []string
	exhaustedMarker  string
	closeFiles       func() error
	pinSources       []string
	environmentProbe []string
}

// ReusableEnvironment returns the shell environment without this launch's
// private pin variables.
func (p *PreparedLaunch) ReusableEnvironment() []string {
	return envutil.FilterEnv(p.Cmd.Environ(), p.pinSources...)
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
	if len(spec.Launch.RequiredEnv) != 0 {
		spec.SetEnv = append(slices.Clone(spec.SetEnv), spec.Launch.RequiredEnv...)
	}
	checkSetEnv(spec.SetEnv)
	token := id.Short()
	prepared := &PreparedLaunch{
		Delimiter:       "__LEAPMUX_READY_" + token + "__",
		exhaustedMarker: "__LEAPMUX_EXHAUSTED_" + token + "__",
		candidates:      slices.Clone(spec.Launch.candidates),
	}
	if spec.EnvGated != nil || spec.ObserveEnvironment != nil {
		prepared.MetaPrefix = "__LEAPMUX_META_" + token + "__ "
	}
	if observation := spec.ObserveEnvironment; observation != nil {
		request := envprobe.Request{
			Variables: observation.Variables, PathVariables: observation.PathVariables,
			ObservationPrefix: prepared.MetaPrefix,
		}
		arguments, err := request.Arguments()
		if err != nil {
			return nil, err
		}
		if executable, err := os.Executable(); err == nil {
			prepared.environmentProbe = append([]string{executable}, arguments...)
		}
	}
	shell := terminal.ShellBaseName(spec.Shell)
	if shell == "cmd" {
		return nil, errors.New("cmd.exe cannot launch agents. Configure a PowerShell or Bash shell for the Worker")
	}
	dialect := resolveDialect(shell)
	// Native lookup errors remain on cmd.Err. A launch can use the last known
	// Windows PATH when the refresh fails.
	cmd, _ := newShellCommand(ctx, spec.Shell, spec.LoginShell, "", spec.WorkingDir, spec.Environment)
	// Seed profile-overridable provider defaults in the shell environment.
	if len(spec.Launch.Env) > 0 {
		cmd.Env = append(cmd.Environ(), spec.Launch.Env...)
	}
	envutil.ScrubAppImageEnv(cmd)
	shellSpec := shellLaunchSpec{WrapSpec: spec}
	if len(spec.SetEnv) != 0 {
		environment := cmd.Environ()
		shellSpec = prepareShellLaunch(dialect, spec, token, environment)
		cmd.Env = shellSpec.pinEnvironment(environment)
		for _, pin := range shellSpec.pins {
			prepared.pinSources = append(prepared.pinSources, pin.source)
		}
	}

	script, err := dialect.buildCommand(shellSpec, prepared)
	if err != nil {
		return nil, err
	}
	// CommandArgs places the command text after all shell flags.
	cmd.Args[len(cmd.Args)-1] = script
	prepared.Cmd = cmd
	return prepared, nil
}

type nuInvocationFiles struct {
	base     string
	optional string
}

func (p *PreparedLaunch) makeInvocationDirectory(prefix string) (string, error) {
	dir, err := os.MkdirTemp("", prefix)
	if err != nil {
		return "", err
	}
	p.closeFiles = sync.OnceValue(func() error { return os.RemoveAll(dir) })
	return dir, nil
}

const cshInvocationPrefix = "if ( 1 ) source "

// BSD csh prints continuation prompts for multiline -ic commands. Source a
// file in the profile-loaded shell so the provider keeps clean output and stdin.
func (p *PreparedLaunch) writeCshInvocation(script string) (string, error) {
	dir, err := p.makeInvocationDirectory("leapmux-csh-launch-")
	if err != nil {
		return "", fmt.Errorf("create the C shell invocation directory: %w", err)
	}
	path := filepath.Join(dir, "invocation.csh")
	if err := os.WriteFile(path, []byte(script+"\n"), 0o600); err != nil {
		return "", errors.Join(fmt.Errorf("write the C shell invocation: %w", err), p.Close())
	}
	// A single-line if calls the builtin without expanding a source alias.
	return cshInvocationPrefix + cshQuote(path), nil
}

func (cshDialect) prepareProbeScript(script string) (string, func(), error) {
	scriptFiles := &PreparedLaunch{}
	wrapped, err := scriptFiles.writeCshInvocation(script)
	if err != nil {
		return "", nil, err
	}
	return wrapped, func() { _ = scriptFiles.Close() }, nil
}

func (p *PreparedLaunch) writeNuInvocations(spec WrapSpec) (map[string]nuInvocationFiles, error) {
	dir, err := p.makeInvocationDirectory("leapmux-nu-launch-")
	if err != nil {
		return nil, fmt.Errorf("create Nushell invocation directory: %w", err)
	}
	var optionalDir string
	optionalCount := 0
	if spec.EnvGated != nil && len(spec.EnvGated.Args) != 0 {
		optionalDir = filepath.Join(dir, "optional")
		optionalCount = len(spec.BaseArgs) + len(spec.EnvGated.Args)
		if err := os.Mkdir(optionalDir, 0o700); err != nil {
			return nil, errors.Join(fmt.Errorf("create Nushell optional invocation directory: %w", err), p.Close())
		}
	}
	scripts := make(map[string]nuInvocationFiles, len(p.candidates))
	for _, name := range p.candidates {
		files := nuInvocationFiles{base: filepath.Join(dir, name+".nu")}
		if err := writeNuInvocation(files.base, name, len(spec.BaseArgs)); err != nil {
			return nil, errors.Join(fmt.Errorf("write Nushell invocation: %w", err), p.Close())
		}
		if optionalDir != "" {
			files.optional = filepath.Join(optionalDir, name+".nu")
			if err := writeNuInvocation(files.optional, name, optionalCount); err != nil {
				return nil, errors.Join(fmt.Errorf("write Nushell optional invocation: %w", err), p.Close())
			}
		}
		scripts[name] = files
	}
	return scripts, nil
}

// Nushell checks positional requirements when it parses this call. A spread
// argument alone cannot satisfy a required position or a fixed signature.
func writeNuInvocation(path, name string, count int) error {
	var script strings.Builder
	script.WriteString("def main [arguments: list<string>] { " + name)
	for index := range count {
		fmt.Fprintf(&script, " $arguments.%d", index)
	}
	script.WriteString(" }\n")
	return os.WriteFile(path, []byte(script.String()), 0o600)
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

// cshSetEnv exports each pin and removes its private transport variable.
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
		b.WriteString("$env." + pin.target + " = $env." + pin.source + "; %hide-env " + pin.source + "; ")
	}
	return b.String()
}

func pwshSetEnv(pins []environmentPin) string {
	var b strings.Builder
	for _, pin := range pins {
		b.WriteString("$env:" + pin.target + " = $env:" + pin.source + "; Microsoft.PowerShell.Management\\Remove-Item -LiteralPath 'Env:" + pin.source + "' -ErrorAction SilentlyContinue; ")
	}
	return b.String()
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

// nuClearEnv removes environment variables the Nushell way. The `%` sigil calls
// the builtin even when the profile defines a command of the same name.
func nuClearEnv(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	parts := make([]string, len(keys))
	for i, key := range keys {
		parts[i] = "%hide-env -i " + key
	}
	return strings.Join(parts, "; ") + "; "
}

// pwshClearEnv removes environment variables the PowerShell way. PowerShell
// runs a function or an alias before a cmdlet of the same name, and a profile
// can define either, so the step calls the cmdlet by its module-qualified
// name, as pwshSetEnv does.
func pwshClearEnv(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	parts := make([]string, len(keys))
	for i, key := range keys {
		parts[i] = "Microsoft.PowerShell.Management\\Remove-Item -LiteralPath " + shellquote.PowerShell.Quote("Env:"+key) + " -ErrorAction SilentlyContinue"
	}
	return strings.Join(parts, "; ") + "; "
}

// posixEnvCondition builds a POSIX shell conditional expression that checks
// whether any of vars is set and non-empty.
// e.g. `[ -n "$VAR1" ] || [ -n "$VAR2" ] || [ -n "$VAR3" ]`
func posixEnvCondition(vars []string) string {
	parts := make([]string, len(vars))
	for i, v := range vars {
		parts[i] = fmt.Sprintf(`[ -n "${%s-}" ]`, v)
	}
	return strings.Join(parts, " || ")
}

// cshEnvGateVar is the csh SHELL variable that carries the env gate's answer. `set`
// creates a shell variable, never an environment one, so it does not reach the agent.
const cshEnvGateVar = "_leapmux_env_gate"

const cshVariableNameLimit = 30

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
	var longKeys []string
	fmt.Fprintf(&b, "set %s = 0\n", cshEnvGateVar)
	for _, v := range vars {
		if len(v) > cshVariableNameLimit {
			longKeys = append(longKeys, v)
			continue
		}
		fmt.Fprintf(&b, "if ( $?%s ) then\n", v)
		fmt.Fprintf(&b, "if ( \"$%s\" != \"\" ) set %s = 1\n", v, cshEnvGateVar)
		b.WriteString("endif\n")
	}
	if len(longKeys) != 0 {
		// BSD csh can export longer names but cannot expand them with $name.
		fmt.Fprintf(&b, "if ( { '/bin/sh' '-c' %s } ) set %s = 1\n",
			cshQuote(posixEnvCondition(longKeys)), cshEnvGateVar)
	}
	return b.String()
}

// nuEnvCondition builds a Nushell conditional expression that checks
// whether any of vars is set and non-empty. The `%` sigil calls each builtin
// even when the profile defines a command of the same name. For example:
//
//	($env | %get -o VAR1 | %default '') != '' or ...
func nuEnvCondition(vars []string) string {
	parts := make([]string, len(vars))
	for i, v := range vars {
		parts[i] = fmt.Sprintf("($env | %%get -o %s | %%default '') != ''", v)
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
