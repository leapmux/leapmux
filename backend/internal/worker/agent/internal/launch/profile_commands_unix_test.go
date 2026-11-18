//go:build unix

package launch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/shellquote"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func useBashProfile(t *testing.T, content string) string {
	t.Helper()
	shell, err := exec.LookPath("bash")
	require.NoError(t, err)
	file := filepath.Join(t.TempDir(), "profile")
	require.NoError(t, os.WriteFile(file, []byte(content), 0o600))
	t.Setenv("BASH_ENV", file)
	return shell
}

func TestProfileCommandUsesTheAliasOrFunctionItDiscovers(t *testing.T) {
	const name = "leapmux-profile-command"
	quote := shellquote.POSIX.Quote
	for _, kind := range []string{"alias", "function"} {
		t.Run(kind, func(t *testing.T) {
			witness := filepath.Join(t.TempDir(), "invocations")
			peer := quote(profileCommandExecutable(t)) + " -test.run=^TestProfileCommandPeer$ --"
			profile := "profile_peer() { printf 'ran\\n' >> " + quote(witness) + "; " + peer + " \"$@\"; return $?; }\n"
			if kind == "alias" {
				profile += "shopt -s expand_aliases\nalias " + name + "=profile_peer\n"
			} else {
				profile += name + "() { profile_peer \"$@\"; }\n"
			}
			shell := useBashProfile(t, profile)
			bin := t.TempDir()
			testutil.WriteExecutable(t, filepath.Join(bin, name), "#!/bin/sh\nprintf 'external should not run\\n'\nexit 42\n")
			t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			t.Setenv(profilePeerEnv, "1")
			require.Equal(t, ProbeYes, CheckBinary(t.Context(), shell, false, name))
			require.NoFileExists(t, witness, "discovery must not invoke the wrapper")

			args := []string{"", "a b", `a"b`, `double\\backslash\`, "*.txt", "!event", "--flag", "한글"}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			spec, err := Binaries(name).Resolve(ctx, shell, false, "Profile command")
			require.NoError(t, err)
			cmd, delimiter, _ := wrapTestCommand(t, ctx, WrapSpec{Shell: shell, Launch: spec, BaseArgs: args})
			assertProfileCommandExchange(t, cmd, delimiter, args)
			data, err := os.ReadFile(witness)
			require.NoError(t, err)
			assert.Equal(t, "ran\n", string(data))
		})
	}
}

func TestProfileCandidateLaunchReadsTheProfileOnce(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "profile-count")
	shell := useBashProfile(t, "printf 'profile\\n' >> "+shellquote.POSIX.Quote(log)+"\n")
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	testutil.WriteExecutable(t, filepath.Join(dir, "leapmux-profile-second"), "#!/bin/sh\nprintf 'second\\n'\n")
	spec, err := Binaries("leapmux-profile-first", "leapmux-profile-second").Resolve(t.Context(), shell, false, "Profile command")
	require.NoError(t, err)
	cmd, delimiter, _ := wrapTestCommand(t, t.Context(), WrapSpec{Shell: shell, Launch: spec, WorkingDir: dir})
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	require.Contains(t, string(output), delimiter+"\nsecond\n")
	data, err := os.ReadFile(log)
	require.NoError(t, err)
	assert.Equal(t, "profile\n", string(data))
}

func TestProfileCandidateFindsCheckoutLocalFallback(t *testing.T) {
	const first = "leapmux-checkout-first"
	const second = "leapmux-checkout-second"
	shell := useBashProfile(t, "export PATH=./bin\n")
	checkout := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(checkout, "bin"), 0o700))
	testutil.WriteExecutable(t, filepath.Join(checkout, "bin", second), "#!/bin/sh\nprintf 'checkout fallback\\n'\n")
	locator := Binaries(first, second)
	require.Equal(t, []Resolution{Missing}, ScanAvailability(t.Context(), shell, false, []Locator{locator}))
	spec, err := locator.Resolve(t.Context(), shell, false, "Checkout command")
	require.NoError(t, err)
	cmd, delimiter, _ := wrapTestCommand(t, t.Context(), WrapSpec{Shell: shell, Launch: spec, WorkingDir: checkout})
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	assert.Contains(t, string(output), delimiter+"\ncheckout fallback\n")
}

func TestProfileCandidateExhaustionReportsEveryName(t *testing.T) {
	const first = "leapmux-profile-absent-first"
	const second = "leapmux-profile-absent-second"
	shell := useBashProfile(t, "printf 'profile stderr' >&2\n")
	spec, err := Binaries(first, second).Resolve(t.Context(), shell, false, "Missing candidate test")
	require.NoError(t, err)
	prepared, err := Wrap(t.Context(), WrapSpec{Shell: shell, Launch: spec})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, prepared.Close()) })
	diagnostics := prepared.NewDiagnostics(1024)
	prepared.Cmd.Stderr = diagnostics
	output, err := prepared.Cmd.Output()
	diagnostics.Finish()
	err = diagnostics.ExitError(err, false)
	require.ErrorIs(t, err, ErrNoCandidate)
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit)
	assert.Equal(t, 127, exit.ExitCode())
	assert.ErrorContains(t, err, first)
	assert.ErrorContains(t, err, second)
	assert.NotContains(t, string(output), prepared.Delimiter)
}

func TestProgramPathFindsTheExternalBehindAProfileFunction(t *testing.T) {
	const name = "leapmux-profile-interpreter"
	witness := filepath.Join(t.TempDir(), "wrapper-called")
	shell := useBashProfile(t, name+"() { printf 'called' > "+shellquote.POSIX.Quote(witness)+"; }\n")
	t.Setenv("PATH", t.TempDir())
	require.Equal(t, ProbeYes, CheckBinary(t.Context(), shell, false, name))
	path, result := ProgramPath(t.Context(), shell, false, name)
	require.Equal(t, ProbeNo, result)
	require.Empty(t, path)
	bin := t.TempDir()
	executable := filepath.Join(bin, name)
	testutil.WriteExecutable(t, executable, "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", bin)
	path, result = ProgramPath(t.Context(), shell, false, name)
	require.Equal(t, ProbeYes, result)
	assert.Equal(t, executable, path)
	assert.NoFileExists(t, witness)
}

func TestProfileCommandDialectMatrix(t *testing.T) {
	for _, name := range []string{"bash", "zsh", "sh", "ash", "dash", "ksh", "fish", "csh", "tcsh", "nu", "xonsh", "pwsh"} {
		t.Run(name, func(t *testing.T) {
			shell := requireProfileShell(t, name, name)
			profile := t.TempDir()
			witness := filepath.Join(profile, "profile-read")
			program := profileCommandExecutable(t)
			installDialectProfile(t, name, profile, program, witness)
			t.Setenv(profilePeerEnv, "1")

			const candidate = "leapmux_profile_matrix"
			locator := Binaries("leapmux_profile_matrix_missing", candidate)
			login := name != "bash" && name != "zsh" && name != "fish" && name != "xonsh" && name != "pwsh"
			require.Equal(t, []Resolution{Found}, ScanAvailability(t.Context(), shell, login, []Locator{locator}))
			reads, err := os.ReadFile(witness)
			require.NoError(t, err)
			require.Equal(t, "profile\n", string(reads))
			require.NoError(t, os.Remove(witness))

			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			spec, err := locator.Resolve(ctx, shell, login, "Shell matrix")
			require.NoError(t, err)
			args := []string{"", "a b", `a"b`, "it's", `two\\slashes\`, "*.txt", "!event", "--flag", "한글"}
			cmd, delimiter, _ := wrapTestCommand(t, ctx, WrapSpec{Shell: shell, LoginShell: login, Launch: spec, BaseArgs: args})
			assertProfileCommandExchange(t, cmd, delimiter, args)
			reads, err = os.ReadFile(witness)
			require.NoError(t, err)
			require.Equal(t, "profile\n", string(reads))

			stopCtx, stop := context.WithTimeout(t.Context(), 20*time.Second)
			defer stop()
			stopped, _, _ := wrapTestCommand(t, stopCtx, WrapSpec{Shell: shell, LoginShell: login, Launch: spec})
			assertProfileCommandCancellation(t, stopCtx, stop, stopped, true)
		})
	}
}

func installDialectProfile(t *testing.T, shell, dir, peer, witness string) {
	t.Helper()
	const candidate = "leapmux_profile_matrix"
	const helperArgs = "-test.run=^TestProfileCommandPeer --"
	posix := shellquote.POSIX.Quote
	file := filepath.Join(dir, "profile")
	body := "printf 'profile\\n' >> " + posix(witness) + "\n" +
		candidate + "() { " + posix(peer) + " " + helperArgs + " \"$@\"; return $?; }\n"
	switch shell {
	case "bash":
		t.Setenv("BASH_ENV", file)
	case "zsh":
		t.Setenv("ZDOTDIR", dir)
		file = filepath.Join(dir, ".zshenv")
	case "fish":
		t.Setenv("XDG_CONFIG_HOME", dir)
		file = filepath.Join(dir, "fish", "config.fish")
		body = "builtin printf 'profile\\n' >> " + fishQuote(witness) + "\nfunction " + candidate + "\n" + fishQuote(peer) + " '-test.run=^TestProfileCommandPeer$' -- $argv\nreturn $status\nend\n"
	case "csh", "tcsh":
		t.Setenv("HOME", dir)
		file = filepath.Join(dir, ".cshrc")
		if shell == "tcsh" {
			file = filepath.Join(dir, ".tcshrc")
		}
		body = "echo profile >> " + cshQuote(witness) + "\nalias " + candidate + " '" + peer + " " + helperArgs + " \\!*'\n"
	case "nu":
		t.Setenv("XDG_CONFIG_HOME", dir)
		file = filepath.Join(dir, "nushell", "config.nu")
		body = "\"profile\\n\" | save --append " + nuQuote(witness) + "\ndef --wrapped " + candidate + " [...arguments: string] { ^" + nuQuote(peer) + " '-test.run=^TestProfileCommandPeer$' '--' ...$arguments }\n"
	case "xonsh":
		t.Setenv("XONSHRC", file)
		body = "import subprocess\nwith open(" + pythonString(witness) + ", 'a') as witness:\n    witness.write('profile\\n')\n" +
			"def profile_peer(args):\n    return subprocess.call([" + pythonString(peer) + ", '-test.run=^TestProfileCommandPeer$', '--', *args])\n" +
			"aliases[" + pythonString(candidate) + "] = profile_peer\n"
	case "pwsh":
		t.Setenv("HOME", dir)
		t.Setenv("XDG_CONFIG_HOME", dir)
		file = filepath.Join(dir, "powershell", "Microsoft.PowerShell_profile.ps1")
		quote := shellquote.PowerShell.Quote
		body = "[System.IO.File]::AppendAllText(" + quote(witness) + ", \"profile`n\")\n" +
			"$PSNativeCommandArgumentPassing = 'Standard'\n" +
			"function " + candidate + " { & " + quote(peer) + " '-test.run=^TestProfileCommandPeer$' '--' @args }\n"
	default:
		t.Setenv("ENV", file)
	}
	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o700))
	require.NoError(t, os.WriteFile(file, []byte(body), 0o600))
}

func TestProfileCandidateNushellWithoutDeferredParsingUsesExecutables(t *testing.T) {
	shell := os.Getenv("LEAPMUX_TEST_OLD_NUSHELL")
	if shell == "" {
		t.Skip("set LEAPMUX_TEST_OLD_NUSHELL to a Nushell executable before 0.114")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	require.NoError(t, os.Mkdir(bin, 0o700))
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("PATH", bin)
	require.NoError(t, os.Mkdir(filepath.Join(dir, "nushell"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "nushell", "config.nu"),
		[]byte("def leapmux-old-nu-wrapper [] { exit 38 }\n"), 0o600))
	testutil.WriteExecutable(t, filepath.Join(bin, "leapmux-old-nu-executable"), "#!/bin/sh\nprintf 'native fallback\\n'\n")
	require.Equal(t, ProbeNo, CheckBinary(t.Context(), shell, true, "leapmux-old-nu-wrapper"))
	cmd, delimiter, _ := wrapTestCommand(t, t.Context(), WrapSpec{
		Shell: shell, LoginShell: true, Launch: ShellCommands("leapmux-old-nu-wrapper", "leapmux-old-nu-executable"),
	})
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	assert.Contains(t, string(output), delimiter+"\nnative fallback\n")
}

func TestProfileCandidateFailureDoesNotInvokeTheFallback(t *testing.T) {
	dir := t.TempDir()
	fallback := filepath.Join(dir, "fallback")
	shell := useBashProfile(t, "leapmux_failing_candidate() { printf 'wrapper started\\n'; return 127; }\n")
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	testutil.WriteExecutable(t, filepath.Join(dir, "leapmux_fallback_candidate"),
		"#!/bin/sh\nprintf 'started' > "+shellquote.POSIX.Quote(fallback)+"\n")
	prepared, err := Wrap(t.Context(), WrapSpec{
		Shell: shell, Launch: ShellCommands("leapmux_failing_candidate", "leapmux_fallback_candidate"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, prepared.Close()) })
	diagnostics := prepared.NewDiagnostics(1024)
	prepared.Cmd.Stderr = diagnostics
	output, err := prepared.Cmd.Output()
	diagnostics.Finish()
	require.Contains(t, string(output), prepared.Delimiter+"\nwrapper started\n")
	var nativeErr *exec.ExitError
	require.ErrorAs(t, diagnostics.ExitError(err, true), &nativeErr)
	assert.Equal(t, 127, nativeErr.ExitCode())
	assert.NotErrorIs(t, diagnostics.ExitError(err, true), ErrNoCandidate)
	assert.NoFileExists(t, fallback)
}

func TestProfileCandidateNushellDefersAnUnselectedSignature(t *testing.T) {
	shell := requireProfileShell(t, "nu", "nu")
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	bin := filepath.Join(dir, "bin")
	require.NoError(t, os.Mkdir(bin, 0o700))
	t.Setenv("PATH", bin)
	const preferred = "leapmux-nu-preferred"
	const incompatible = "leapmux-nu-incompatible"
	file := filepath.Join(dir, "nushell", "config.nu")
	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o700))
	require.NoError(t, os.WriteFile(file, []byte("def "+incompatible+" [] { exit 48 }\n"), 0o600))
	testutil.WriteExecutable(t, filepath.Join(bin, preferred), "#!/bin/sh\nprintf 'preferred %s\\n' \"$1\"\n")
	cmd, delimiter, _ := wrapTestCommand(t, t.Context(), WrapSpec{
		Shell: shell, LoginShell: true, Launch: ShellCommands(preferred, incompatible), BaseArgs: []string{"--stdio"},
	})
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	assert.Contains(t, string(output), delimiter+"\npreferred --stdio\n")
}

func TestProfileCandidatePowerShellSkipsUnresolvableAliases(t *testing.T) {
	shell, home := testutil.RequirePowerShellArgumentPassing(t, "Standard", "LEAPMUX_PS_PROFILE_MODE")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	appendPowerShellProfile(t, home, "& /bin/sh -c 'exit 41'\n"+
		"Set-Alias -Name leapmux-dangling -Value leapmux-missing-test-target\n"+
		"Set-Alias -Name leapmux-cycle-a -Value leapmux-cycle-b\n"+
		"Set-Alias -Name leapmux-cycle-b -Value leapmux-cycle-a\n"+
		"function leapmux-successful { [Console]::Out.WriteLine('chosen') }\n")
	for _, earlier := range []string{"leapmux-dangling", "leapmux-cycle-a"} {
		t.Run(earlier, func(t *testing.T) {
			require.Equal(t, ProbeNo, CheckBinary(t.Context(), shell, false, earlier))
			locator := Binaries(earlier, "leapmux-successful")
			require.Equal(t, []Resolution{Found}, ScanAvailability(t.Context(), shell, false, []Locator{locator}))
			spec, err := locator.Resolve(t.Context(), shell, false, "PowerShell aliases")
			require.NoError(t, err)
			cmd, delimiter, _ := wrapTestCommand(t, t.Context(), WrapSpec{Shell: shell, Launch: spec})
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", output)
			assert.Contains(t, string(output), delimiter+"\nchosen\n")
		})
	}
}

func TestProfileCandidateCanReplaceThePosixShellWithAnExecutable(t *testing.T) {
	for _, name := range []string{"bash", "zsh", "sh", "ash", "dash", "ksh"} {
		t.Run(name, func(t *testing.T) {
			shell := requireProfileShell(t, name, name)
			dir := t.TempDir()
			t.Setenv("PATH", dir)
			t.Setenv("ZDOTDIR", dir)
			t.Setenv("BASH_ENV", "")
			t.Setenv("ENV", "")
			const candidate = "leapmux_test_external"
			testutil.WriteExecutable(t, filepath.Join(dir, candidate), "#!/bin/sh\nprintf 'external started\\n'\nexit 37\n")
			spec, err := Binaries("leapmux_other_external", candidate).Resolve(t.Context(), shell, false, "Profile command")
			require.NoError(t, err)
			cmd, delimiter, _ := wrapTestCommand(t, t.Context(), WrapSpec{Shell: shell, Launch: spec})
			output, err := cmd.CombinedOutput()
			var exitErr *exec.ExitError
			require.ErrorAs(t, err, &exitErr)
			assert.Equal(t, 37, exitErr.ExitCode(), "%s", output)
			assert.Contains(t, string(output), delimiter+"\nexternal started\n")
		})
	}
}

func TestProfileCommandZshFindsAnAliasWithoutAnExternalPrintf(t *testing.T) {
	shell := requireProfileShell(t, "zsh", "zsh")
	dir := t.TempDir()
	t.Setenv("ZDOTDIR", dir)
	t.Setenv("PATH", dir)
	const name = "leapmux-zsh-alias"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".zshenv"),
		[]byte("alias "+name+"='builtin print -r -- chosen'\n"), 0o600))
	require.Equal(t, ProbeYes, CheckBinary(t.Context(), shell, false, name))
	cmd, delimiter, _ := wrapTestCommand(t, t.Context(), WrapSpec{Shell: shell, Launch: ShellCommands(name)})
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	assert.Contains(t, string(output), delimiter+"\nchosen\n")
}
