package launch

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/shellquote"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const windowsDisposableProfileEnv = "LEAPMUX_TEST_WINDOWS_DISPOSABLE_PROFILE"
const windowsProfileGuardEnv = "LEAPMUX_TEST_WINDOWS_PROFILE_GUARD"

func TestProfileCommandWindowsPowerShellAutomaticProfile(t *testing.T) {
	for _, name := range []string{"powershell", "pwsh"} {
		t.Run(name, func(t *testing.T) {
			githubHosted := os.Getenv("GITHUB_ACTIONS") == "true" && os.Getenv("RUNNER_ENVIRONMENT") == "github-hosted"
			if !githubHosted && os.Getenv(windowsDisposableProfileEnv) != "1" {
				const reason = "set " + windowsDisposableProfileEnv + "=1 only on a disposable or dedicated Windows account with no PowerShell profiles and a policy that permits local profiles"
				if slices.Contains(strings.Split(os.Getenv(requiredProfileShellsEnv), ","), name) {
					t.Fatal(reason)
				}
				t.Skip(reason)
			}
			shell := requireProfileShell(t, name, name)
			dir := t.TempDir()
			native := installWindowsProfilePeer(t, dir, windowsProfileNative)
			witness := filepath.Join(dir, "profile.log")
			t.Setenv(profilePeerEnv, "1")
			t.Setenv(profilePeerStripEnv, "profile fixture value")
			t.Setenv(profilePeerLiteralEnv, "before pin")
			t.Setenv(windowsProfileGuardEnv, dir)

			profiles := windowsPowerShellProfilePaths(t, shell)
			for _, profile := range profiles {
				_, err := os.Lstat(profile)
				require.ErrorIs(t, err, os.ErrNotExist, "keep the existing PowerShell profile at %q", profile)
			}
			installWindowsPowerShellDefaultProfile(t, profiles[0], powerShellProfileSource(native, witness, dir))

			require.Equal(t, ProbeYes, CheckBinary(t.Context(), shell, true, windowsProfileAlias))
			assertWindowsNativeProfileLaunch(t, shell, dir, true, ShellCommands(windowsProfileAlias), func(*exec.Cmd) {})
			mode := "default"
			if name == "pwsh" {
				mode = "login"
			}
			assertWindowsPowerShellProfileReads(t, witness, name, mode, 3)
		})
	}
}

func windowsPowerShellProfilePaths(t *testing.T, shell string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	const script = `
[Console]::OutputEncoding = [Text.UTF8Encoding]::new($false)
$paths = @($PROFILE.CurrentUserCurrentHost, $PROFILE.CurrentUserAllHosts,
	$PROFILE.AllUsersCurrentHost, $PROFILE.AllUsersAllHosts)
[Console]::Out.WriteLine((ConvertTo-Json -Compress -InputObject $paths))
`
	cmd := exec.CommandContext(ctx, shell, "-NoProfile", "-NonInteractive", "-Command", script)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	var profiles []string
	require.NoError(t, json.Unmarshal(output, &profiles), "%s", output)
	require.Len(t, profiles, 4)
	for _, profile := range profiles {
		require.True(t, filepath.IsAbs(profile), "%q", profile)
	}
	return profiles
}

func makeWindowsProfileDirectory(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err == nil {
		require.True(t, info.IsDir(), "%q", path)
		return
	}
	require.ErrorIs(t, err, os.ErrNotExist)
	parent := filepath.Dir(path)
	require.NotEqual(t, parent, path)
	makeWindowsProfileDirectory(t, parent)
	require.NoError(t, os.Mkdir(path, 0o700))
	t.Cleanup(func() { assert.NoError(t, os.Remove(path)) })
}

// Exclusive creation prevents a race with another process that creates the profile.
// The fixture removes only the file it created.
func installWindowsPowerShellDefaultProfile(t *testing.T, path, source string) {
	t.Helper()
	makeWindowsProfileDirectory(t, filepath.Dir(path))
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	require.NoError(t, err)
	info, statErr := file.Stat()
	_, writeErr := file.Write(windowsPowerShellProfileBytes(source))
	closeErr := file.Close()
	require.NoError(t, statErr)
	t.Cleanup(func() {
		current, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		if assert.NoError(t, err) && assert.True(t, os.SameFile(info, current), "keep the replacement profile at %q", path) {
			assert.NoError(t, os.Remove(path))
		}
	})
	require.NoError(t, writeErr)
	require.NoError(t, closeErr)
}

func powerShellProfileSource(native, witness, guard string) string {
	quote := shellquote.PowerShell.Quote
	source := `
[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)
$mode = 'default'
$commandLine = [Environment]::GetCommandLineArgs()
if ($commandLine -contains '-Login') { $mode = 'login' }
if ($commandLine -contains '-NoProfile') { $mode = 'explicit script block' }
$exe = [Diagnostics.Process]::GetCurrentProcess().MainModule.FileName
$entry = @($PSVersionTable.PSVersion.ToString(), $PSVersionTable.PSEdition, $exe, $mode) -join [char]9
[IO.File]::AppendAllText(` + quote(witness) + `, $entry + [Environment]::NewLine)
Set-Alias -Name ` + windowsProfileAlias + ` -Value ` + quote(native) + `
$PSNativeCommandUseErrorActionPreference = $true
$global:LASTEXITCODE = 91
`
	if guard != "" {
		return "if ($env:" + windowsProfileGuardEnv + " -ceq " + quote(guard) + ") {\n" + source + "}\n"
	}
	return source
}

func assertWindowsPowerShellProfileReads(t *testing.T, witness, shell, mode string, count int) {
	t.Helper()
	edition, prefix := "Core", "7."
	if shell == "powershell" {
		edition, prefix = "Desktop", "5.1."
	}
	rows := readWindowsProfileWitness(t, witness, count)
	t.Run("version "+rows[0][0]+" "+mode, func(t *testing.T) {
		for _, fields := range rows {
			assert.True(t, strings.HasPrefix(fields[0], prefix), "interpreter version %q", fields[0])
			assert.Equal(t, edition, fields[1])
			assert.True(t, filepath.IsAbs(fields[2]), "%q", fields[2])
			assert.Equal(t, shell, terminal.ShellBaseName(fields[2]))
			assert.Equal(t, mode, fields[3])
		}
		evidence := "native alias argv, environment, live streams, exit and cancellation"
		if mode == "explicit script block" {
			evidence = "native direct and alias argv, environment, live streams, exit and cancellation plus pure-function argv and live streams with environment-controlled optional arguments"
		}
		t.Logf("windows %s %s, %s, %d profile reads, %s", shell, rows[0][0], mode, count, evidence)
	})
}
