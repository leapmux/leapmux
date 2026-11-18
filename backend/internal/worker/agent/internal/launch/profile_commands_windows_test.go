package launch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/shellquote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProfileCandidateWindowsExhaustionReportsEveryName(t *testing.T) {
	for _, name := range []string{"powershell", "pwsh"} {
		t.Run(name, func(t *testing.T) {
			shell, err := exec.LookPath(name)
			if err != nil {
				t.Skipf("%s is unavailable: %v", name, err)
			}
			const first = "leapmux-profile-absent-a"
			const second = "leapmux-profile-absent-b"
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
		})
	}
}

func TestProfileCommandWindowsDialectMatrix(t *testing.T) {
	for _, name := range []string{"powershell", "pwsh", "git-bash"} {
		t.Run(name, func(t *testing.T) {
			const candidate = "leapmux_windows_profile"
			dir := t.TempDir()
			args := []string{"", "a b", `a"b`, "it's", `two\\slashes\`, "*.txt", "!event", "--flag", "/literal/argument", "한글"}
			if name == "git-bash" {
				program := filepath.Join(os.Getenv("ProgramFiles"), "Git", "bin", "bash.exe")
				if override := os.Getenv("LEAPMUX_TEST_GIT_BASH"); override != "" {
					program = override
				}
				shell := requireProfileShell(t, name, program)
				profile := filepath.Join(dir, "profile")
				body := candidate + "() { " + shellquote.POSIX.Quote(filepath.ToSlash(profileCommandExecutable(t))) +
					" '-test.run=^TestProfileCommandPeer$' -- \"$@\"; return $?; }\n"
				require.NoError(t, os.WriteFile(profile, []byte(body), 0o600))
				t.Setenv("BASH_ENV", filepath.ToSlash(profile))
				t.Setenv(profilePeerEnv, "1")
				require.Equal(t, ProbeYes, CheckBinary(t.Context(), shell, false, candidate))
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				prepared, err := Wrap(ctx, WrapSpec{Shell: shell, WorkingDir: dir, Launch: ShellCommands(candidate), BaseArgs: args})
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, prepared.Close()) })
				assertProfileCommandExchange(t, prepared.Cmd, prepared.Delimiter, args)
				stopCtx, stop := context.WithTimeout(t.Context(), 30*time.Second)
				defer stop()
				stopped, _, _ := wrapTestCommand(t, stopCtx, WrapSpec{Shell: shell, WorkingDir: dir, Launch: ShellCommands(candidate)})
				assertProfileCommandCancellation(t, stopCtx, stop, stopped, true)
				return
			}

			shell := requireProfileShell(t, name, name)
			profile := filepath.Join(dir, "profile.ps1")
			preamble := `[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)
function Invoke-LeapmuxTestPeer {
	if ($env:LEAPMUX_TEST_PROFILE_COMMAND_IDENTITY -eq '1') { [Console]::Out.WriteLine("peer-pid:" + $PID); [Console]::Out.Flush() }
	[Console]::Out.WriteLine((ConvertTo-Json -Compress -InputObject @($args)))
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
Set-Alias -Name leapmux_windows_profile -Value Invoke-LeapmuxTestPeer
`
			// Windows resolves $PROFILE from the account's Documents folder.
			// Dot-source a private fixture without changing that folder.
			require.NoError(t, os.WriteFile(profile, append([]byte{0xef, 0xbb, 0xbf}, []byte(preamble)...), 0o600))
			source := ". " + shellquote.PowerShell.Quote(profile) + "\n"
			names := []string{"leapmux_windows_missing", candidate}
			const nonce = "__WINDOWS_PROFILE_FIXTURE__"
			probe, err := newShellCommand(t.Context(), shell, false, source+renderCommandProbe(name, nonce, names, false), dir)
			require.NoError(t, err)
			isolateWindowsProfile(probe)
			output, err := probe.Output()
			require.NoError(t, err)
			results, _ := parseCommandProbe(string(output), nonce, names, false)
			require.Equal(t, map[string]ProbeResult{names[0]: ProbeNo, names[1]: ProbeYes}, results)

			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			prepared, err := Wrap(ctx, WrapSpec{Shell: shell, WorkingDir: dir, Launch: ShellCommands(names...), BaseArgs: args})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, prepared.Close()) })
			last := len(prepared.Cmd.Args) - 1
			prepared.Cmd.Args[last] = source + prepared.Cmd.Args[last]
			isolateWindowsProfile(prepared.Cmd)
			assertProfileCommandExchange(t, prepared.Cmd, prepared.Delimiter, args)

			stopCtx, stop := context.WithTimeout(t.Context(), 30*time.Second)
			defer stop()
			stopped, _, _ := wrapTestCommand(t, stopCtx, WrapSpec{Shell: shell, WorkingDir: dir, Launch: ShellCommands(names...)})
			last = len(stopped.Args) - 1
			stopped.Args[last] = source + stopped.Args[last]
			isolateWindowsProfile(stopped)
			assertProfileCommandCancellation(t, stopCtx, stop, stopped, false)
		})
	}
}

func isolateWindowsProfile(cmd *exec.Cmd) {
	cmd.Args = slices.Insert(cmd.Args, 1, "-NoProfile")
}
