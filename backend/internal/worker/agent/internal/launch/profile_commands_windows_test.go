package launch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/shellquote"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProfileCandidateWindowsExhaustionReportsEveryName(t *testing.T) {
	for _, name := range []string{"powershell", "pwsh"} {
		t.Run(name, func(t *testing.T) {
			shell := requireProfileShell(t, name, name)
			assertProfileCandidateExhaustion(t, shell, "leapmux-profile-absent-a", "leapmux-profile-absent-b")
		})
	}
}

const windowsProfileNative = "leapmux-windows-native.exe"
const windowsProfileAlias = "leapmux-windows-alias"
const windowsProfileFunction = "leapmux-windows-function"

func TestProfileCommandWindowsPowerShellExplicitProfile(t *testing.T) {
	for _, name := range []string{"powershell", "pwsh"} {
		t.Run(name, func(t *testing.T) {
			shell := requireProfileShell(t, name, name)
			dir := t.TempDir()
			native := installWindowsProfilePeer(t, dir, windowsProfileNative)
			t.Setenv(profilePeerEnv, "1")
			t.Setenv(profilePeerStripEnv, "profile fixture value")
			t.Setenv(profilePeerLiteralEnv, "before pin")
			witness := filepath.Join(dir, "profile.log")
			source := powerShellProfileSource(native, witness, "") + windowsPowerShellFunctionSource
			configure := func(cmd *exec.Cmd) {
				prependPowerShellProfile(cmd, source)
			}

			for _, candidate := range []string{windowsProfileNative, windowsProfileAlias, windowsProfileFunction} {
				t.Run(candidate, func(t *testing.T) {
					target := candidate
					if candidate == windowsProfileNative {
						target = native
					}
					assertWindowsPowerShellExplicitDiscovery(t, shell, dir, target, configure)

					if candidate == windowsProfileFunction {
						assertWindowsPowerShellFunction(t, shell, dir, configure)
						return
					}
					spec := ShellCommands(candidate)
					if candidate == windowsProfileNative {
						spec = Spec{Program: native}
					}
					assertWindowsNativeProfileLaunch(t, shell, dir, false, spec, configure)
				})
			}
			assertWindowsPowerShellProfileReads(t, witness, name, "explicit script block", 8)
		})
	}
}

func TestProfileCommandWindowsGitBash(t *testing.T) {
	shell := requireWindowsGitBash(t)
	for _, mode := range []struct {
		name, profile string
		login         bool
	}{
		{name: "non-login", profile: "bash-env"},
		{name: "login", profile: ".bash_profile", login: true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			home, binaries := t.TempDir(), t.TempDir()
			native := installWindowsProfilePeer(t, binaries, windowsProfileNative)
			witness := filepath.Join(home, "profile.log")
			profile := filepath.Join(home, mode.profile)
			t.Setenv("HOME", filepath.ToSlash(home))
			t.Setenv("BASH_ENV", "")
			t.Setenv("MSYS2_ARG_CONV_EXCL", "")
			t.Setenv("MSYS2_ENV_CONV_EXCL", "")
			t.Setenv(profilePeerEnv, "1")
			t.Setenv(profilePeerStripEnv, "profile fixture value")
			t.Setenv(profilePeerLiteralEnv, "before pin")
			if !mode.login {
				t.Setenv("BASH_ENV", filepath.ToSlash(profile))
			}
			quote := shellquote.POSIX.Quote
			program := quote(filepath.ToSlash(native))
			body := `if shopt -q login_shell; then
    leapmux_profile_mode=login
else
    leapmux_profile_mode=non-login
fi
printf '%s\t%s\t%s\t%s\n' "$BASH_VERSION" "$BASH" "$OSTYPE" "$leapmux_profile_mode" >> ` + quote(filepath.ToSlash(witness)) + "\n" +
				`export PATH="$(/usr/bin/cygpath.exe -u -- ` + quote(filepath.ToSlash(binaries)) + `):$PATH"` + "\n" +
				"alias " + windowsProfileAlias + "=" + quote(program) + "\n" +
				windowsProfileFunction + "() { " + program + " \"$@\"; }\n"
			require.NoError(t, os.WriteFile(profile, []byte(body), 0o600))

			for _, candidate := range []string{windowsProfileNative, windowsProfileAlias, windowsProfileFunction} {
				t.Run(candidate, func(t *testing.T) {
					require.Equal(t, ProbeYes, CheckBinary(t.Context(), shell, mode.login, candidate))
					assertWindowsNativeProfileLaunch(t, shell, home, mode.login, ShellCommands(candidate), func(*exec.Cmd) {})
				})
			}
			assertWindowsGitBashProfileReads(t, witness, mode.name, mode.profile, 9)
		})
	}
}

func requireWindowsGitBash(t *testing.T) string {
	t.Helper()
	program := os.Getenv("LEAPMUX_TEST_GIT_BASH")
	if program == "" {
		program = filepath.Join(os.Getenv("ProgramFiles"), "Git", "bin", "bash.exe")
	}
	return requireProfileShell(t, "git-bash", program)
}

func installWindowsProfilePeer(t *testing.T, directory, filename string) string {
	t.Helper()
	contents, err := os.ReadFile(profileCommandExecutable(t))
	require.NoError(t, err)
	native := filepath.Join(directory, filename)
	testutil.WriteExecutable(t, native, string(contents))
	return native
}

const windowsProfileLiteral = "/literal/first:/literal/second"

func windowsProfileArguments() []string {
	return []string{"", "a b", `a"b`, "it's", `two\\slashes\`, `spaced path\`, "*.txt", "!event", "$a;&|()", "--flag", "/literal/argument", "한글"}
}

func windowsProfileExchangeSpec(shell, directory string, login bool, spec Spec, args []string) WrapSpec {
	return WrapSpec{
		Shell: shell, WorkingDir: directory, LoginShell: login, Launch: spec, BaseArgs: args,
		StripEnvKeys: []string{profilePeerStripEnv},
		SetEnv:       []string{profilePeerLiteralEnv + "=" + windowsProfileLiteral},
		EnvGated: &EnvGatedArgs{
			EnvVars: []string{profilePeerStripEnv},
			MetaKey: "windows_test_gate",
			Args:    []string{"--gated"},
		},
	}
}

func assertWindowsNativeProfileLaunch(t *testing.T, shell, directory string, login bool, spec Spec, configure func(*exec.Cmd)) {
	t.Helper()
	const selectPeer = "-test.run=^TestProfileCommandPeer$"
	args := windowsProfileArguments()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd, delimiter, _ := wrapTestCommand(t, ctx, windowsProfileExchangeSpec(shell, directory, login, spec, append([]string{selectPeer, "--"}, args...)))
	configure(cmd)
	literal := windowsProfileLiteral
	assertProfileCommandExchangeWithEnvironment(t, cmd, delimiter, append(args, "--gated"), &profilePeerEnvironment{Literal: &literal})

	stopCtx, stop := context.WithTimeout(t.Context(), 30*time.Second)
	defer stop()
	stopped, _, _ := wrapTestCommand(t, stopCtx, WrapSpec{
		Shell: shell, WorkingDir: directory, LoginShell: login, Launch: spec, BaseArgs: []string{selectPeer, "--"},
	})
	configure(stopped)
	assertProfileCommandCancellation(t, stopCtx, stop, stopped, true)
}

func assertWindowsPowerShellExplicitDiscovery(t *testing.T, shell, directory, candidate string, configure func(*exec.Cmd)) {
	t.Helper()
	const nonce = "__WINDOWS_PROFILE_FIXTURE__"
	const missing = "leapmux-windows-missing"
	names := []string{missing, candidate}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	probe, err := newShellCommand(ctx, shell, false, renderCommandProbe(terminal.ShellBaseName(shell), nonce, names, false, false), directory, nil)
	require.NoError(t, err)
	configure(probe)
	output, err := probe.CombinedOutput()
	require.NoError(t, err, "%s", output)
	results, _ := parseCommandProbe(string(output), nonce, names, false)
	require.Equal(t, map[string]ProbeResult{missing: ProbeNo, candidate: ProbeYes}, results)
}

func assertWindowsPowerShellFunction(t *testing.T, shell, directory string, configure func(*exec.Cmd)) {
	t.Helper()
	args := windowsProfileArguments()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd, delimiter, _ := wrapTestCommand(t, ctx, windowsProfileExchangeSpec(shell, directory, false, ShellCommands(windowsProfileFunction), args))
	configure(cmd)
	literal := windowsProfileLiteral
	assertProfileCommandExchangeWithEnvironment(t, cmd, delimiter, append(args, "--gated"), &profilePeerEnvironment{Literal: &literal})
}

var gitBashProfileVersion = regexp.MustCompile(`^\d+\.\d+\.\d+(?:\(\d+\))?(?:-[[:alnum:].-]+)?$`)

func readWindowsProfileWitness(t *testing.T, witness string, count int) [][4]string {
	t.Helper()
	data, err := os.ReadFile(witness)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimRight(string(data), "\r\n"), "\n")
	require.Len(t, lines, count)
	rows := make([][4]string, len(lines))
	for index, line := range lines {
		fields := strings.Split(strings.TrimSuffix(line, "\r"), "\t")
		require.Len(t, fields, 4, "%q", line)
		copy(rows[index][:], fields)
	}
	return rows
}

func assertWindowsGitBashProfileReads(t *testing.T, witness, mode, profile string, count int) {
	t.Helper()
	rows := readWindowsProfileWitness(t, witness, count)
	t.Run("version "+rows[0][0]+" "+mode, func(t *testing.T) {
		for _, fields := range rows {
			assert.Regexp(t, gitBashProfileVersion, fields[0])
			assert.Equal(t, "bash", terminal.ShellBaseName(fields[1]))
			assert.Contains(t, []string{"msys", "cygwin"}, fields[2])
			assert.Equal(t, mode, fields[3])
		}
		t.Logf("windows Git Bash %s, %s via %s, %d profile reads, native external/alias/function argv, environment, live streams, exit and cancellation",
			rows[0][0], mode, profile, count)
	})
}

// Windows PowerShell 5.1 treats an unmarked script as ANSI.
func windowsPowerShellProfileBytes(source string) []byte {
	return append([]byte{0xef, 0xbb, 0xbf}, []byte(source)...)
}

var windowsPowerShellFunctionSource = `
function leapmux-windows-function {
	[Console]::Out.WriteLine((ConvertTo-Json -Compress -InputObject @($args)))
	$received = @{
		Stripped = [Environment]::GetEnvironmentVariable('LEAPMUX_TEST_PROFILE_COMMAND_STRIP')
		Literal = [Environment]::GetEnvironmentVariable('LEAPMUX_TEST_PROFILE_COMMAND_LITERAL')
	}
	[Console]::Out.WriteLine((ConvertTo-Json -Compress -InputObject $received))
	$payload = ` + shellquote.PowerShell.Quote(profilePeerPayload) + `
	[Console]::Out.Write("Content-Length: " + [Text.Encoding]::UTF8.GetByteCount($payload) + "` + "`r`n`r`n" + `" + $payload)
	[Console]::Out.Flush()
	for ($index = 0; $index -lt 2; $index++) {
		[Console]::Out.Write('reply:' + [Console]::ReadLine() + "` + "`n" + `")
		[Console]::Out.Flush()
	}
	[Console]::Error.WriteLine('provider diagnostic')
	exit 37
}
`
