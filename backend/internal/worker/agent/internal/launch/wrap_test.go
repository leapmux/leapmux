package launch

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wrapShellCmd is a positional adapter over Wrap(ctx,
// WrapSpec) for the table-style tests below, which predate the struct and
// vary mostly by shell path. It names the positional arguments in one place so the
// tests stay compact; production call sites use the WrapSpec literal directly.
func wrapShellCmd(ctx context.Context, shell string, loginShell bool, binaryName string,
	stripEnvKeys, baseArgs []string, gate *EnvGatedArgs, workingDir string) (*exec.Cmd, string, string) {
	return Wrap(ctx, WrapSpec{
		Shell:        shell,
		LoginShell:   loginShell,
		Launch:       Spec{Program: binaryName},
		StripEnvKeys: stripEnvKeys,
		BaseArgs:     baseArgs,
		EnvGated:     gate,
		WorkingDir:   workingDir,
	})
}

// testEnvGate is an env gate over two neutral variables. It stands in for a
// provider's own gate, so these tests pin the wrapper and no provider's names.
func testEnvGate(args ...string) *EnvGatedArgs {
	return &EnvGatedArgs{
		EnvVars: []string{"LEAPMUX_TEST_GATE_A", "LEAPMUX_TEST_GATE_B"},
		MetaKey: "test_gate_open",
		Args:    args,
	}
}

func TestBuildShellWrappedCommand_Bash_Interactive(t *testing.T) {
	cmd, delimiter, metaPrefix := wrapShellCmd(
		context.Background(), "/bin/bash", true, "claude",
		[]string{"CLAUDECODE"}, []string{"--output-format", "stream-json"}, testEnvGate("--model", "opus"), "/tmp",
	)
	require.NotEmpty(t, delimiter)
	assert.True(t, strings.HasPrefix(delimiter, "__LEAPMUX_READY_"))
	assert.True(t, strings.HasSuffix(delimiter, "__"))
	require.NotEmpty(t, metaPrefix)
	assert.True(t, strings.HasPrefix(metaPrefix, "__LEAPMUX_META_"))

	assert.Equal(t, "/bin/bash", cmd.Path)
	assert.Equal(t, "/tmp", cmd.Dir)
	require.Len(t, cmd.Args, 5) // bash -i -l -c <cmd>
	assert.Equal(t, "-i", cmd.Args[1])
	assert.Equal(t, "-l", cmd.Args[2])
	assert.Equal(t, "-c", cmd.Args[3])
	assert.Contains(t, cmd.Args[4], "echo '"+delimiter+"'")
	assert.Contains(t, cmd.Args[4], "unset CLAUDECODE")
	assert.Contains(t, cmd.Args[4], "exec 'claude'")
	assert.Contains(t, cmd.Args[4], "'--output-format'")
	assert.Contains(t, cmd.Args[4], "'--model'")
	assert.Contains(t, cmd.Args[4], "'opus'")
	// Verify conditional structure
	assert.Contains(t, cmd.Args[4], "LEAPMUX_TEST_GATE_A")
	assert.Contains(t, cmd.Args[4], "test_gate_open=false")
	assert.Contains(t, cmd.Args[4], "test_gate_open=true")
	assert.Contains(t, cmd.Args[4], metaPrefix+"test_gate_open=")
}

func TestBuildShellWrappedCommand_Bash_NonInteractive(t *testing.T) {
	cmd, _, _ := wrapShellCmd(
		context.Background(), "/bin/bash", false, "claude",
		[]string{"CLAUDECODE"}, []string{"--output-format", "stream-json"}, testEnvGate("--model", "opus"), "/tmp",
	)
	assert.Equal(t, "/bin/bash", cmd.Path)
	require.Len(t, cmd.Args, 3) // bash -c <cmd>
	assert.Equal(t, "-c", cmd.Args[1])
	assert.Contains(t, cmd.Args[2], "exec 'claude'")
}

func TestBuildShellWrappedCommand_Zsh(t *testing.T) {
	cmd, delimiter, _ := wrapShellCmd(
		context.Background(), "/bin/zsh", true, "claude",
		[]string{"CLAUDECODE"}, []string{"--verbose"}, testEnvGate("--model", "sonnet"), "/home/user",
	)
	assert.Equal(t, "/bin/zsh", cmd.Path)
	require.Len(t, cmd.Args, 5) // zsh -i -l -c <cmd>
	assert.Equal(t, "-i", cmd.Args[1])
	assert.Equal(t, "-l", cmd.Args[2])
	assert.Equal(t, "-c", cmd.Args[3])
	assert.Contains(t, cmd.Args[4], "unset CLAUDECODE")
	assert.Contains(t, cmd.Args[4], "exec 'claude'")
	assert.Contains(t, cmd.Args[4], delimiter)
}

func TestBuildShellWrappedCommand_Fish(t *testing.T) {
	cmd, _, _ := wrapShellCmd(
		context.Background(), "/usr/bin/fish", true, "claude",
		[]string{"CLAUDECODE"}, []string{"--model", "sonnet"}, nil, "/tmp",
	)
	assert.Equal(t, "/usr/bin/fish", cmd.Path)
	require.Len(t, cmd.Args, 5) // fish -i -l -c <cmd>
	assert.Equal(t, "-i", cmd.Args[1])
	assert.Equal(t, "-l", cmd.Args[2])
	assert.Equal(t, "-c", cmd.Args[3])
	assert.Contains(t, cmd.Args[4], "unset CLAUDECODE")
	assert.Contains(t, cmd.Args[4], "exec 'claude'")
	// No conditional (no env gate)
	assert.NotContains(t, cmd.Args[4], "LEAPMUX_TEST_GATE_A")
}

func TestBuildShellWrappedCommand_Tcsh_Interactive(t *testing.T) {
	cmd, delimiter, _ := wrapShellCmd(
		context.Background(), "/bin/tcsh", true, "claude",
		[]string{"CLAUDECODE"}, []string{"--output-format", "stream-json"}, testEnvGate("--model", "opus"), "/tmp",
	)
	assert.Equal(t, "/bin/tcsh", cmd.Path)
	require.Len(t, cmd.Args, 3) // tcsh -ic <cmd>
	assert.Equal(t, "-ic", cmd.Args[1])
	assert.Contains(t, cmd.Args[2], "echo '"+delimiter+"'")
	// `unsetenv`, not `unset`: csh's `unset` removes a SHELL variable, so the key would
	// survive into the launched agent's environment.
	assert.Contains(t, cmd.Args[2], "unsetenv CLAUDECODE")
	assert.NotContains(t, cmd.Args[2], "unset CLAUDECODE")
	assert.Contains(t, cmd.Args[2], "exec 'claude'")
	// csh's own conditional. `[ -n "$VAR" ]` on an unset name is a hard error there
	// ("VAR: Undefined variable."), which killed every env-gated launch.
	assert.NotContains(t, cmd.Args[2], `[ -n "$`)
	assert.Contains(t, cmd.Args[2], "endif")
	assert.Contains(t, cmd.Args[2], "if ( $?LEAPMUX_TEST_GATE_B ) then")
	// The guard and the read it guards must be on SEPARATE lines: csh substitutes a
	// whole line's variables before it evaluates any of that line.
	assert.NotContains(t, cmd.Args[2], `$?LEAPMUX_TEST_GATE_B && `)
}

func TestBuildShellWrappedCommand_Tcsh_NonInteractive(t *testing.T) {
	cmd, _, _ := wrapShellCmd(
		context.Background(), "/bin/tcsh", false, "claude",
		[]string{"CLAUDECODE"}, []string{"--output-format", "stream-json"}, testEnvGate("--model", "opus"), "/tmp",
	)
	assert.Equal(t, "/bin/tcsh", cmd.Path)
	require.Len(t, cmd.Args, 3) // tcsh -c <cmd>
	assert.Equal(t, "-c", cmd.Args[1])
}

func TestBuildShellWrappedCommand_Csh(t *testing.T) {
	cmd, _, _ := wrapShellCmd(
		context.Background(), "/bin/csh", true, "claude",
		[]string{"CLAUDECODE"}, []string{"--verbose"}, testEnvGate("--model", "opus"), "/tmp",
	)
	assert.Equal(t, "/bin/csh", cmd.Path)
	require.Len(t, cmd.Args, 3) // csh -ic <cmd>
	assert.Equal(t, "-ic", cmd.Args[1])
	assert.Contains(t, cmd.Args[2], "unsetenv CLAUDECODE")
	assert.NotContains(t, cmd.Args[2], "unset CLAUDECODE")
	assert.Contains(t, cmd.Args[2], "exec 'claude'")
}

// A csh launch with no probe emits no conditional at all, so the simple path must stay
// free of the seed lines the conditional path needs.
func TestBuildShellWrappedCommand_Csh_NoProbeEmitsNoConditional(t *testing.T) {
	cmd, delimiter, meta := wrapShellCmd(
		context.Background(), "/bin/tcsh", false, "/usr/local/bin/node",
		[]string{"CLAUDECODE"}, []string{"app-server", "--stdio"}, nil, "/tmp",
	)
	assert.Empty(t, meta)
	assert.Equal(t,
		"unsetenv CLAUDECODE; echo '"+delimiter+"' && exec '/usr/local/bin/node' 'app-server' '--stdio'",
		cmd.Args[2])
}

func TestBuildShellWrappedCommand_Nu_Interactive(t *testing.T) {
	cmd, delimiter, _ := wrapShellCmd(
		context.Background(), "/usr/bin/nu", true, "claude",
		[]string{"CLAUDECODE"}, []string{"--output-format", "stream-json"}, testEnvGate("--model", "opus"), "/tmp",
	)
	assert.Equal(t, "/usr/bin/nu", cmd.Path)
	require.Len(t, cmd.Args, 5) // nu -i -l -c <cmd>
	assert.Equal(t, "-i", cmd.Args[1])
	assert.Equal(t, "-l", cmd.Args[2])
	assert.Equal(t, "-c", cmd.Args[3])
	assert.Contains(t, cmd.Args[4], "echo '"+delimiter+"'")
	assert.Contains(t, cmd.Args[4], "hide-env CLAUDECODE")
	assert.Contains(t, cmd.Args[4], `^"claude"`)
	assert.NotContains(t, cmd.Args[4], "exec")
	assert.Contains(t, cmd.Args[4], "LEAPMUX_TEST_GATE_A")
	// Args should be double-quoted (nuQuote), not single-quoted (posixQuote)
	assert.Contains(t, cmd.Args[4], `"--output-format"`)
	assert.Contains(t, cmd.Args[4], `"stream-json"`)
	assert.Contains(t, cmd.Args[4], `"--model"`)
	assert.Contains(t, cmd.Args[4], `"opus"`)
}

func TestBuildNuCommand_SingleQuoteInArgs(t *testing.T) {
	inner := buildNuCommand(WrapSpec{
		Launch:       Spec{Program: "claude"},
		StripEnvKeys: []string{"CLAUDECODE"},
		BaseArgs:     []string{"--output-format", "stream-json"},
		EnvGated:     testEnvGate("--model", "it's-a-model"),
	}, "__DELIM__", "__META__ ")
	// Single quotes in args must be safely double-quoted, not POSIX-quoted.
	assert.Contains(t, inner, `"it's-a-model"`)
	assert.NotContains(t, inner, `'\''`)
}

func TestBuildShellWrappedCommand_Nu_NonInteractive(t *testing.T) {
	cmd, _, _ := wrapShellCmd(
		context.Background(), "/usr/bin/nu", false, "claude",
		[]string{"CLAUDECODE"}, []string{"--output-format", "stream-json"}, testEnvGate("--model", "opus"), "/tmp",
	)
	assert.Equal(t, "/usr/bin/nu", cmd.Path)
	require.Len(t, cmd.Args, 3) // nu -c <cmd>
	assert.Equal(t, "-c", cmd.Args[1])
}

func TestBuildShellWrappedCommand_PwshCore_Interactive(t *testing.T) {
	for _, shell := range []string{"/usr/bin/pwsh", "/usr/bin/pwsh-preview"} {
		t.Run(shell, func(t *testing.T) {
			cmd, delimiter, _ := wrapShellCmd(
				context.Background(), shell, true, "claude",
				[]string{"CLAUDECODE"}, []string{"--output-format", "stream-json"}, testEnvGate("--model", "opus"), "/tmp",
			)
			assert.Equal(t, shell, cmd.Path)
			require.Len(t, cmd.Args, 4) // pwsh -Login -Command <cmd>
			assert.Equal(t, "-Login", cmd.Args[1])
			assert.Equal(t, "-Command", cmd.Args[2])
			assert.Contains(t, cmd.Args[3], "Write-Output '"+delimiter+"'")
			assert.Contains(t, cmd.Args[3], "Remove-Item Env:CLAUDECODE")
			assert.Contains(t, cmd.Args[3], "& 'claude'")
			assert.NotContains(t, cmd.Args[3], "exec")
			assert.Contains(t, cmd.Args[3], "LEAPMUX_TEST_GATE_A")
		})
	}
}

// Windows PowerShell 5.1 (powershell.exe) does not understand -Login; its
// CLI parser treats unknown leading switches as command names, raising
// "ObjectNotFound: (-Login:String), CommandNotFoundException". Interactive
// invocations must therefore omit -Login for powershell(-preview) and
// invoke -Command directly.
func TestBuildShellWrappedCommand_WindowsPowerShell_Interactive(t *testing.T) {
	for _, shell := range []string{"/usr/bin/powershell", "/usr/bin/powershell-preview"} {
		t.Run(shell, func(t *testing.T) {
			cmd, delimiter, _ := wrapShellCmd(
				context.Background(), shell, true, "claude",
				[]string{"CLAUDECODE"}, []string{"--output-format", "stream-json"}, testEnvGate("--model", "opus"), "/tmp",
			)
			assert.Equal(t, shell, cmd.Path)
			require.Len(t, cmd.Args, 3) // powershell -Command <cmd>
			assert.Equal(t, "-Command", cmd.Args[1])
			assert.NotContains(t, cmd.Args, "-Login")
			assert.Contains(t, cmd.Args[2], "Write-Output '"+delimiter+"'")
			assert.Contains(t, cmd.Args[2], "Remove-Item Env:CLAUDECODE")
			assert.Contains(t, cmd.Args[2], "& 'claude'")
			assert.Contains(t, cmd.Args[2], "LEAPMUX_TEST_GATE_A")
		})
	}
}

func TestBuildShellWrappedCommand_Pwsh_NonInteractive(t *testing.T) {
	cmd, _, _ := wrapShellCmd(
		context.Background(), "/usr/bin/pwsh", false, "claude",
		[]string{"CLAUDECODE"}, []string{"--output-format", "stream-json"}, testEnvGate("--model", "opus"), "/tmp",
	)
	assert.Equal(t, "/usr/bin/pwsh", cmd.Path)
	require.Len(t, cmd.Args, 3) // pwsh -Command <cmd>
	assert.Equal(t, "-Command", cmd.Args[1])
}

func TestBuildShellWrappedCommand_UnknownShell(t *testing.T) {
	cmd, _, _ := wrapShellCmd(
		context.Background(), "/usr/bin/xonsh", true, "claude",
		[]string{"CLAUDECODE"}, []string{"--verbose"}, testEnvGate("--model", "opus"), "/tmp",
	)
	assert.Equal(t, "/usr/bin/xonsh", cmd.Path)
	require.Len(t, cmd.Args, 5) // defaults to -i -l -c
	assert.Equal(t, "-i", cmd.Args[1])
	assert.Equal(t, "-l", cmd.Args[2])
	assert.Equal(t, "-c", cmd.Args[3])
	assert.Contains(t, cmd.Args[4], "unset CLAUDECODE")
	assert.Contains(t, cmd.Args[4], "exec 'claude'")
}

// When the desktop app runs as a Linux AppImage, the runtime exports
// ARGV0 (= AppImage filename), and zsh interprets that env var as the
// argv[0] to use when execing every external command (AppImageKit#852).
// The result is mise's shim — invoked when zsh runs `claude` — sees
// argv[0] = the AppImage filename and bails with "<file>.AppImage is
// not a valid shim" (jdx/mise#3537). The fix is to scrub the AppImage
// runtime's env vars from the agent shell. We also strip APPIMAGE,
// APPDIR, and OWD because they expose AppImage internals to user-space
// tools that have no business knowing.
func TestBuildShellWrappedCommand_AppImage_ScrubsEnv(t *testing.T) {
	t.Setenv("APPIMAGE", "/path/to/leapmux-desktop_0.0.1-dev_amd64.AppImage")
	t.Setenv("APPDIR", "/tmp/.mount_xxxxxx")
	t.Setenv("ARGV0", "leapmux-desktop_0.0.1-dev_amd64.AppImage")
	t.Setenv("OWD", "/home/user")
	t.Setenv("PATH_SHOULD_SURVIVE", "/usr/bin:/bin")

	cmd, _, _ := wrapShellCmd(
		context.Background(), "/bin/zsh", true, "claude",
		[]string{"CLAUDECODE"}, []string{"--output-format", "stream-json"}, nil, "/tmp",
	)

	// Shell invocation itself must not change — the bug is in the env,
	// not in argv[0] of the spawned shell.
	assert.Equal(t, "/bin/zsh", cmd.Path)
	require.Len(t, cmd.Args, 5)
	assert.Equal(t, "-i", cmd.Args[1])
	assert.Equal(t, "-l", cmd.Args[2])
	assert.Equal(t, "-c", cmd.Args[3])

	// AppImage-injected vars must be absent from cmd.Env so the user's
	// shell — and any tools it sources, including mise — never sees them.
	for _, key := range []string{"ARGV0", "APPIMAGE", "APPDIR", "OWD"} {
		assert.False(t, envutil.HasKey(cmd.Env, key), "env var %q should be scrubbed when running inside an AppImage", key)
	}
	// Unrelated env vars must survive the scrub.
	assert.True(t, envutil.HasKey(cmd.Env, "PATH_SHOULD_SURVIVE"), "non-AppImage env vars must not be touched")
}

// When APPIMAGE is unset, env is left untouched (cmd.Env stays nil so the
// child inherits the parent's environment unchanged) — no behavior
// change for .deb installs, dev runs, or unpacked AppDir launches.
func TestBuildShellWrappedCommand_NoAppImage_PreservesEnv(t *testing.T) {
	t.Setenv("APPIMAGE", "")
	t.Setenv("ARGV0", "should-survive-when-not-in-appimage")

	cmd, _, _ := wrapShellCmd(
		context.Background(), "/bin/zsh", true, "claude",
		[]string{"CLAUDECODE"}, []string{"--output-format", "stream-json"}, nil, "/tmp",
	)

	assert.Equal(t, "/bin/zsh", cmd.Path)
	require.Len(t, cmd.Args, 5)
	// cmd.Env stays nil so exec inherits os.Environ() verbatim — no
	// blanket scrubbing when we're not inside an AppImage.
	assert.Nil(t, cmd.Env)
}

func TestBuildShellWrappedCommand_NoModelEffort(t *testing.T) {
	// A launch with no env gate (Claude when settings already flagged a third-party
	// provider) generates no conditional logic.
	cmd, _, metaPrefix := wrapShellCmd(
		context.Background(), "/bin/bash", true, "claude",
		[]string{"CLAUDECODE"}, []string{"--output-format", "stream-json"}, nil, "/tmp",
	)
	assert.Empty(t, metaPrefix)
	assert.Contains(t, cmd.Args[4], "'--output-format'")
	assert.NotContains(t, cmd.Args[4], "'--model'")
	assert.NotContains(t, cmd.Args[4], "LEAPMUX_TEST_GATE_A")
	assert.NotContains(t, cmd.Args[4], "test_gate_open")
}

func TestBuildShellWrappedCommand_NoModelEffort_Nu(t *testing.T) {
	cmd, _, metaPrefix := wrapShellCmd(
		context.Background(), "/usr/bin/nu", true, "claude",
		[]string{"CLAUDECODE"}, []string{"--output-format", "stream-json"}, nil, "/tmp",
	)
	assert.Empty(t, metaPrefix)
	assert.NotContains(t, cmd.Args[4], "LEAPMUX_TEST_GATE_A")
}

func TestBuildShellWrappedCommand_NoModelEffort_Pwsh(t *testing.T) {
	cmd, _, metaPrefix := wrapShellCmd(
		context.Background(), "/usr/bin/pwsh", true, "claude",
		[]string{"CLAUDECODE"}, []string{"--output-format", "stream-json"}, nil, "/tmp",
	)
	assert.Empty(t, metaPrefix)
	assert.NotContains(t, cmd.Args[3], "LEAPMUX_TEST_GATE_A")
}

// TestBuildShellWrappedCommand_ProbeThirdPartyDefaultModel covers an env gate with
// no arguments to send: the runtime check and its metadata line still run. Claude's
// account-default ("sentinel") launch takes this path -- it sends no --model/--effort
// but must still learn whether the user's shell profile configures a third-party
// provider, or AvailableModels would wrongly show the model/effort UI.
func TestBuildShellWrappedCommand_ProbeThirdPartyDefaultModel(t *testing.T) {
	cmd, _, metaPrefix := wrapShellCmd(
		context.Background(), "/bin/bash", true, "claude",
		[]string{"CLAUDECODE"}, []string{"--output-format", "stream-json"}, testEnvGate(), "/tmp",
	)
	require.NotEmpty(t, metaPrefix, "a gate writes a metadata prefix even with no gated args")
	inner := cmd.Args[4]
	// The probe and both capability lines are emitted.
	assert.Contains(t, inner, "LEAPMUX_TEST_GATE_A")
	assert.Contains(t, inner, metaPrefix+"test_gate_open=false")
	assert.Contains(t, inner, metaPrefix+"test_gate_open=true")
	// But neither branch forwards a --model/--effort, since there are none.
	assert.NotContains(t, inner, "'--model'")
	assert.NotContains(t, inner, "'--effort'")
	// Both branches still exec the program with the base args.
	parts := strings.SplitN(inner, "else", 2)
	require.Len(t, parts, 2, "expected if/else probe structure")
	assert.Contains(t, parts[0], "exec 'claude'")
	assert.Contains(t, parts[1], "exec 'claude'")
}

// TestBuildShellWrappedCommand_ProbeThirdPartyDefaultModel_NuAndPwsh checks the
// same forced-probe behavior in the other two shell dialects.
func TestBuildShellWrappedCommand_ProbeThirdPartyDefaultModel_NuAndPwsh(t *testing.T) {
	nuCmd, _, nuMeta := wrapShellCmd(
		context.Background(), "/usr/bin/nu", true, "claude",
		[]string{"CLAUDECODE"}, []string{"--output-format", "stream-json"}, testEnvGate(), "/tmp",
	)
	require.NotEmpty(t, nuMeta)
	assert.Contains(t, nuCmd.Args[4], "LEAPMUX_TEST_GATE_A")
	assert.Contains(t, nuCmd.Args[4], nuMeta+"test_gate_open=true")
	assert.NotContains(t, nuCmd.Args[4], `"--model"`)

	pwshCmd, _, pwshMeta := wrapShellCmd(
		context.Background(), "/usr/bin/pwsh", true, "claude",
		[]string{"CLAUDECODE"}, []string{"--output-format", "stream-json"}, testEnvGate(), "/tmp",
	)
	require.NotEmpty(t, pwshMeta)
	assert.Contains(t, pwshCmd.Args[3], "LEAPMUX_TEST_GATE_A")
	assert.Contains(t, pwshCmd.Args[3], pwshMeta+"test_gate_open=true")
	assert.NotContains(t, pwshCmd.Args[3], "'--model'")
}

func TestBuildShellWrappedCommand_ModelEffortInElseBranch(t *testing.T) {
	inner := buildPosixCommand(WrapSpec{
		Launch:       Spec{Program: "claude"},
		StripEnvKeys: []string{"CLAUDECODE"},
		BaseArgs:     []string{"--output-format", "stream-json"},
		EnvGated:     testEnvGate("--model", "opus", "--effort", "high"),
	}, "__DELIM__", "__META__ ")

	// The else branch should contain the gated args
	parts := strings.SplitN(inner, "else", 2)
	require.Len(t, parts, 2, "expected if/else structure")
	assert.Contains(t, parts[1], "'--model'")
	assert.Contains(t, parts[1], "'--effort'")
	assert.Contains(t, parts[1], "'opus'")
	assert.Contains(t, parts[1], "'high'")

	// The if branch (a gate variable is set) should NOT contain the gated args
	assert.NotContains(t, parts[0], "'--model'")
	assert.NotContains(t, parts[0], "'--effort'")
}

func TestBuildShellWrappedCommand_CodexUsesCodexEnvMarkers(t *testing.T) {
	cmd, delimiter, metaPrefix := wrapShellCmd(
		context.Background(), "/bin/zsh", true, "codex",
		[]string{"CODEX_CI"}, []string{"app-server"}, nil, "/tmp",
	)
	assert.Empty(t, metaPrefix)
	assert.Contains(t, cmd.Args[4], "unset CODEX_CI")
	assert.NotContains(t, cmd.Args[4], "CLAUDECODE")
	assert.Contains(t, cmd.Args[4], "echo '"+delimiter+"'")
	assert.Contains(t, cmd.Args[4], "exec 'codex'")
}

func TestBuildShellWrappedCommand_CodexUsesCodexEnvMarkers_NuAndPwsh(t *testing.T) {
	nuCmd, _, _ := wrapShellCmd(
		context.Background(), "/usr/bin/nu", true, "codex",
		[]string{"CODEX_CI"}, []string{"app-server"}, nil, "/tmp",
	)
	assert.Contains(t, nuCmd.Args[4], "hide-env CODEX_CI")
	assert.NotContains(t, nuCmd.Args[4], "CLAUDECODE")

	pwshCmd, _, _ := wrapShellCmd(
		context.Background(), "/usr/bin/pwsh", true, "codex",
		[]string{"CODEX_CI"}, []string{"app-server"}, nil, "/tmp",
	)
	assert.Contains(t, pwshCmd.Args[3], "Remove-Item Env:CODEX_CI")
	assert.NotContains(t, pwshCmd.Args[3], "CLAUDECODE")
}

// A Launch.Program that is an absolute path -- ZCode's resolved Node interpreter, and
// on Windows commonly `C:\Program Files\nodejs\node.exe` -- must reach the shell as
// ONE word in every dialect. Unquoted, the space split it and the shell tried to
// run `C:\Program` with `Files\nodejs\node.exe` as its first argument.
func TestBuildShellWrappedCommand_QuotesProgramPathWithSpace(t *testing.T) {
	const program = `/Applications/My Tools/node`

	posix := buildPosixCommand(WrapSpec{
		Launch:   Spec{Program: program},
		BaseArgs: []string{"/opt/zcode.cjs", "app-server", "--stdio"},
	}, "__DELIM__", "")
	assert.Contains(t, posix, "exec '"+program+"'")

	nu := buildNuCommand(WrapSpec{
		Launch:   Spec{Program: program},
		BaseArgs: []string{"/opt/zcode.cjs"},
	}, "__DELIM__", "")
	assert.Contains(t, nu, `^"`+program+`"`)

	pwsh := buildPwshCommand(WrapSpec{
		Launch:   Spec{Program: program},
		BaseArgs: []string{"/opt/zcode.cjs"},
	}, "__DELIM__", "")
	assert.Contains(t, pwsh, "& '"+program+"'")
}

// The quoting must also survive a quote character inside the path, using each
// dialect's own escape (POSIX '\”, PowerShell doubled ”, Nushell \").
func TestBuildShellWrappedCommand_EscapesQuoteInProgramPath(t *testing.T) {
	posix := buildPosixCommand(WrapSpec{Launch: Spec{Program: "/opt/it's/node"}}, "__DELIM__", "")
	assert.Contains(t, posix, `exec '/opt/it'\''s/node'`)

	pwsh := buildPwshCommand(WrapSpec{Launch: Spec{Program: "/opt/it's/node"}}, "__DELIM__", "")
	assert.Contains(t, pwsh, `& '/opt/it''s/node'`)

	nu := buildNuCommand(WrapSpec{Launch: Spec{Program: `/opt/say"hi"/node`}}, "__DELIM__", "")
	assert.Contains(t, nu, `^"/opt/say\"hi\"/node"`)
}

func TestPosixQuote(t *testing.T) {
	assert.Equal(t, "'hello'", posixQuote("hello"))
	assert.Equal(t, "'it'\\''s'", posixQuote("it's"))
	assert.Equal(t, "''", posixQuote(""))
}

func TestNuQuote(t *testing.T) {
	assert.Equal(t, `"hello"`, nuQuote("hello"))
	assert.Equal(t, `"it's"`, nuQuote("it's"))
	assert.Equal(t, `"say \"hi\""`, nuQuote(`say "hi"`))
	assert.Equal(t, `"back\\slash"`, nuQuote(`back\slash`))
	assert.Equal(t, `""`, nuQuote(""))
}

func TestPwshQuote(t *testing.T) {
	assert.Equal(t, "'hello'", pwshQuote("hello"))
	assert.Equal(t, "'it''s'", pwshQuote("it's"))
	assert.Equal(t, "''", pwshQuote(""))
}

func TestIsPwsh(t *testing.T) {
	assert.True(t, terminal.IsPwsh("pwsh"))
	assert.True(t, terminal.IsPwsh("powershell"))
	assert.True(t, terminal.IsPwsh("pwsh-preview"))
	assert.True(t, terminal.IsPwsh("powershell-preview"))
	assert.False(t, terminal.IsPwsh("bash"))
	assert.False(t, terminal.IsPwsh("zsh"))
	assert.False(t, terminal.IsPwsh("pwsh-extra-stuff"))
}

// --- the launch spec ---
//
// Wrap owns the PrefixArgs and Env merge, which is the whole point
// of threading a Spec rather than a bare program name. ZCode was the only caller
// that needed either, and it merged both by hand -- so a second bundled provider would
// have had to remember two steps, and losing one is silent: a missing PrefixArgs runs
// the interpreter with no script, and a missing Env starts Electron's desktop
// application instead of its Node runtime.

// The interpreter's arguments come FIRST, before the provider's own.
func TestBuildShellWrappedCommand_PrefixArgsPrecedeTheBaseArgs(t *testing.T) {
	cmd, delimiter, _ := Wrap(context.Background(), WrapSpec{
		Shell: "/bin/bash",
		Launch: Spec{
			Program:    "/usr/local/bin/node",
			PrefixArgs: []string{"/Applications/ZCode.app/zcode.cjs"},
		},
		BaseArgs:   []string{"app-server", "--stdio"},
		WorkingDir: "/tmp",
	})

	assert.Equal(t,
		"echo '"+delimiter+"' && exec '/usr/local/bin/node' '/Applications/ZCode.app/zcode.cjs' 'app-server' '--stdio'",
		cmd.Args[2])
}

// The launch env reaches the process. The caller's own FinalizeAgentEnv keeps
// it; TestFinalizeAgentEnv_KeepsTheLaunchEnv pins that half.
func TestBuildShellWrappedCommand_LaunchEnvReachesTheProcess(t *testing.T) {
	cmd, _, _ := Wrap(context.Background(), WrapSpec{
		Shell:      "/bin/bash",
		Launch:     Spec{Program: "/opt/electron", Env: []string{"ELECTRON_RUN_AS_NODE=1"}},
		BaseArgs:   []string{"app-server"},
		WorkingDir: "/tmp",
	})

	assert.Contains(t, cmd.Env, "ELECTRON_RUN_AS_NODE=1")
}

// A launch that needs neither leaves the environment inherited, which is what every
// provider but ZCode gets. cmd.Env stays nil so cmd.Environ() reports the parent's.
func TestBuildShellWrappedCommand_NoLaunchEnvLeavesTheEnvironmentInherited(t *testing.T) {
	cmd, _, _ := Wrap(context.Background(), WrapSpec{
		Shell:      "/bin/bash",
		Launch:     Spec{Program: "claude"},
		BaseArgs:   []string{"--print"},
		WorkingDir: "/tmp",
	})

	assert.Nil(t, cmd.Env)
	assert.NotEmpty(t, cmd.Environ(), "a nil Env means the parent's environment, not an empty one")
}

// Every dialect prepends the same way, or a bundled provider would work on one shell
// and start with no script on another.
func TestBuildShellWrappedCommand_EveryDialectPrependsThePrefixArgs(t *testing.T) {
	for _, shell := range []string{"/bin/bash", "/bin/zsh", "/usr/bin/nu", "/usr/bin/pwsh", "/bin/tcsh"} {
		t.Run(shell, func(t *testing.T) {
			cmd, _, _ := Wrap(context.Background(), WrapSpec{
				Shell:      shell,
				Launch:     Spec{Program: "/opt/node", PrefixArgs: []string{"/opt/zcode.cjs"}},
				BaseArgs:   []string{"app-server"},
				WorkingDir: "/tmp",
			})
			inner := cmd.Args[len(cmd.Args)-1]
			script := strings.Index(inner, "zcode.cjs")
			serve := strings.Index(inner, "app-server")
			require.NotEqual(t, -1, script, "the script must reach the command line")
			require.NotEqual(t, -1, serve)
			assert.Less(t, script, serve, "the interpreter's script comes before the provider's own args")
		})
	}
}

// TestBuildShellWrappedCommand_EnvGateWithoutVarsIsUnconditional pins the empty
// gate: with no variable to check, the gated arguments join the base arguments,
// and no conditional or metadata line is written.
func TestBuildShellWrappedCommand_EnvGateWithoutVarsIsUnconditional(t *testing.T) {
	cmd, delimiter, metaPrefix := wrapShellCmd(
		context.Background(), "/bin/bash", false, "prog",
		nil, []string{"--base"}, &EnvGatedArgs{MetaKey: "unused", Args: []string{"--gated"}}, "/tmp",
	)
	assert.Empty(t, metaPrefix)
	assert.Equal(t, "echo '"+delimiter+"' && exec 'prog' '--base' '--gated'", cmd.Args[2])
}

// TestBuildShellWrappedCommand_EnvGateRejectsUnsafeNames pins that the wrapper
// refuses a gate whose names it would write into shell code unquoted.
func TestBuildShellWrappedCommand_EnvGateRejectsUnsafeNames(t *testing.T) {
	for _, gate := range []*EnvGatedArgs{
		{EnvVars: []string{"OK"}, MetaKey: "bad key"},
		{EnvVars: []string{"OK"}, MetaKey: ""},
		{EnvVars: []string{"BAD;echo pwned"}, MetaKey: "ok"},
		{EnvVars: []string{"1STARTS_WITH_DIGIT"}, MetaKey: "ok"},
		{EnvVars: []string{"$HOME"}, MetaKey: "ok"},
	} {
		assert.Panics(t, func() {
			wrapShellCmd(context.Background(), "/bin/bash", false, "prog", nil, nil, gate, "/tmp")
		}, "gate %+v", gate)
	}
}

// Every dialect sets the SetEnv entries after it strips the StripEnvKeys and
// before the program starts, each in its own syntax and with the value quoted.
func TestBuildShellWrappedCommand_EveryDialectSetsTheSetEnvEntries(t *testing.T) {
	cases := []struct {
		shell string
		want  []string
	}{
		{"/bin/bash", []string{"export LEAPMUX_TEST_PORT='4321' && ", "export LEAPMUX_TEST_PATH='/tmp/a b' && "}},
		{"/usr/bin/fish", []string{"export LEAPMUX_TEST_PORT='4321' && "}},
		{"/bin/tcsh", []string{"setenv LEAPMUX_TEST_PORT '4321'; ", "setenv LEAPMUX_TEST_PATH '/tmp/a b'; "}},
		{"/usr/bin/nu", []string{`$env.LEAPMUX_TEST_PORT = "4321"; `, `$env.LEAPMUX_TEST_PATH = "/tmp/a b"; `}},
		{"/usr/bin/pwsh", []string{"$env:LEAPMUX_TEST_PORT = '4321'; ", "$env:LEAPMUX_TEST_PATH = '/tmp/a b'; "}},
	}
	for _, c := range cases {
		t.Run(c.shell, func(t *testing.T) {
			cmd, _, _ := Wrap(context.Background(), WrapSpec{
				Shell:        c.shell,
				Launch:       Spec{Program: "cline"},
				StripEnvKeys: []string{"LEAPMUX_TEST_STRIP"},
				SetEnv:       []string{"LEAPMUX_TEST_PORT=4321", "LEAPMUX_TEST_PATH=/tmp/a b"},
				BaseArgs:     []string{"--port", "4321"},
				WorkingDir:   "/tmp",
			})
			inner := cmd.Args[len(cmd.Args)-1]
			for _, want := range c.want {
				assert.Contains(t, inner, want)
			}
			strip := strings.Index(inner, "LEAPMUX_TEST_STRIP")
			set := strings.Index(inner, "LEAPMUX_TEST_PORT")
			program := strings.Index(inner, "cline")
			require.NotEqual(t, -1, strip)
			assert.Less(t, strip, set, "the strip runs before the set, so a key in both keeps the set value")
			assert.Less(t, set, program, "the set runs before the program starts")
		})
	}
}

// A value that the wrapper sets wins over the value that the program inherits
// from the shell, which is what a profile export would change.
func TestBuildShellWrappedCommand_SetEnvWinsOverTheInheritedValue(t *testing.T) {
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skip("the test needs /bin/sh")
	}
	cmd, delimiter, _ := Wrap(context.Background(), WrapSpec{
		Shell:      "/bin/sh",
		Launch:     Spec{Program: "/bin/sh"},
		SetEnv:     []string{"LEAPMUX_TEST_VALUE=private value"},
		BaseArgs:   []string{"-c", `printf '%s\n' "$LEAPMUX_TEST_VALUE"`},
		WorkingDir: t.TempDir(),
	})
	cmd.Env = append(cmd.Environ(), "LEAPMUX_TEST_VALUE=inherited")
	out, err := cmd.Output()
	require.NoError(t, err)
	_, after, found := strings.Cut(string(out), delimiter+"\n")
	require.True(t, found, "the wrapper prints its delimiter: %q", out)
	assert.Equal(t, "private value\n", after)
}

func TestBuildShellWrappedCommand_SetEnvRejectsUnsafeEntries(t *testing.T) {
	for _, entry := range []string{"NO_VALUE", "BAD NAME=1", "BAD;rm -rf=1", "=1"} {
		t.Run(entry, func(t *testing.T) {
			assert.Panics(t, func() {
				Wrap(context.Background(), WrapSpec{
					Shell:  "/bin/bash",
					Launch: Spec{Program: "cline"},
					SetEnv: []string{entry},
				})
			})
		})
	}
}

// runWrapped runs a wrapped command with extraEnv after the inherited
// environment, and returns what the program printed after the delimiter. HOME
// points at an empty directory, so a shell such as tcsh, which reads its own
// startup file for every shell, reads none of the user's.
func runWrapped(t *testing.T, spec WrapSpec, extraEnv ...string) (meta, program string) {
	t.Helper()
	cmd, delimiter, _ := Wrap(context.Background(), spec)
	cmd.Env = append(append(cmd.Environ(), "HOME="+t.TempDir()), extraEnv...)
	out, err := cmd.Output()
	require.NoError(t, err, "output: %q", out)
	before, after, found := strings.Cut(string(out), delimiter+"\n")
	require.True(t, found, "the wrapper prints its delimiter: %q", out)
	return before, after
}

// requireShell skips the test when the shell at path does not exist.
func requireShell(t *testing.T, path string) {
	t.Helper()
	if _, err := exec.LookPath(path); err != nil {
		t.Skipf("the test needs %s", path)
	}
}

// A value may be empty and may hold an equals sign, and a key that the wrapper
// both strips and sets keeps the set value. Each one reaches the program as
// SetEnv states it, whatever the program inherits.
func TestBuildShellWrappedCommand_SetEnvKeepsTheValueAsStated(t *testing.T) {
	requireShell(t, "/bin/sh")
	_, out := runWrapped(t, WrapSpec{
		Shell:        "/bin/sh",
		Launch:       Spec{Program: "/bin/sh"},
		StripEnvKeys: []string{"LEAPMUX_TEST_BOTH"},
		SetEnv:       []string{"LEAPMUX_TEST_EMPTY=", "LEAPMUX_TEST_EQUALS=a=b", "LEAPMUX_TEST_BOTH=set"},
		BaseArgs:     []string{"-c", `printf '%s|%s|%s\n' "${LEAPMUX_TEST_EMPTY-unset}" "$LEAPMUX_TEST_EQUALS" "$LEAPMUX_TEST_BOTH"`},
		WorkingDir:   t.TempDir(),
	}, "LEAPMUX_TEST_EMPTY=inherited", "LEAPMUX_TEST_BOTH=inherited")
	assert.Equal(t, "|a=b|set\n", out, "an empty value is set and empty, not unset")
}

// The env gate reads the environment after the wrapper sets SetEnv, so a value
// that the wrapper sets withholds the gated arguments, as an inherited one does.
func TestBuildShellWrappedCommand_TheEnvGateReadsTheSetEnvValue(t *testing.T) {
	requireShell(t, "/bin/sh")
	for _, tc := range []struct {
		name     string
		setEnv   []string
		wantMeta string
		wantArg  string
	}{
		{"the wrapper sets the gate variable", []string{"LEAPMUX_TEST_GATE_A=on"}, "test_gate_open=false", ""},
		{"nothing sets the gate variable", nil, "test_gate_open=true", "gated"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			meta, out := runWrapped(t, WrapSpec{
				Shell:      "/bin/sh",
				Launch:     Spec{Program: "/bin/sh"},
				SetEnv:     tc.setEnv,
				BaseArgs:   []string{"-c", `printf '%s\n' "$1"`, "sh"},
				EnvGated:   testEnvGate("gated"),
				WorkingDir: t.TempDir(),
			}, "LEAPMUX_TEST_GATE_A=", "LEAPMUX_TEST_GATE_B=")
			assert.Contains(t, meta, tc.wantMeta)
			assert.Equal(t, tc.wantArg+"\n", out)
		})
	}
}

// csh sets an environment variable with setenv, and a real tcsh proves that
// the value reaches the program on both paths: the simple one, and the
// multi-line gate that reads the value the wrapper set.
func TestBuildShellWrappedCommand_CshSetsTheSetEnvValue(t *testing.T) {
	requireShell(t, "/bin/tcsh")
	for _, tc := range []struct {
		name    string
		gate    *EnvGatedArgs
		setEnv  []string
		wantOut string
	}{
		{"the simple path", nil, []string{"LEAPMUX_TEST_VALUE=private value"}, "private value|\n"},
		{
			"the gated path",
			testEnvGate("gated"),
			[]string{"LEAPMUX_TEST_VALUE=private value", "LEAPMUX_TEST_GATE_B=on"},
			"private value|\n",
		},
		{"the gated path with no gate variable", testEnvGate("gated"), []string{"LEAPMUX_TEST_VALUE=private value"}, "private value|gated\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, out := runWrapped(t, WrapSpec{
				Shell:      "/bin/tcsh",
				Launch:     Spec{Program: "/bin/sh"},
				SetEnv:     tc.setEnv,
				BaseArgs:   []string{"-c", `printf '%s|%s\n' "$LEAPMUX_TEST_VALUE" "$1"`, "sh"},
				EnvGated:   tc.gate,
				WorkingDir: t.TempDir(),
			}, "LEAPMUX_TEST_VALUE=inherited", "LEAPMUX_TEST_GATE_A=", "LEAPMUX_TEST_GATE_B=")
			assert.Equal(t, tc.wantOut, out)
		})
	}
}
