//go:build windows

package launch

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWindowsShellPathReadsWorkerAccount(t *testing.T) {
	t.Parallel()
	_, err := readWindowsAccountPath()
	require.NoError(t, err)
}

func TestWindowsShellPathPreservesTheRestOfTheEnvironment(t *testing.T) {
	t.Parallel()
	saved := []string{"C:\\Old"}
	refresher := newWindowsPathRefresh(func() ([]string, error) { return saved, nil })
	saved = []string{"C:\\New;Tools"}
	original := []string{"USERPROFILE=C:\\Test", "PaTh=C:\\Launcher;C:\\Old"}
	cmd := exec.CommandContext(t.Context(), "unused")
	cmd.Env = original
	require.NoError(t, refresher.apply(cmd))

	path, present := windowsEnvironmentPath(cmd.Environ())
	require.True(t, present)
	assert.Equal(t, "C:\\Launcher;\"C:\\New;Tools\"", path)
	assert.Contains(t, cmd.Environ(), "USERPROFILE=C:\\Test")
	assert.Equal(t, []string{"USERPROFILE=C:\\Test", "PaTh=C:\\Launcher;C:\\Old"}, original)
}

func TestWindowsShellPathKeepsExplicitEnvironmentWithoutPath(t *testing.T) {
	t.Parallel()
	saved := []string{"C:\\Old"}
	refresher := newWindowsPathRefresh(func() ([]string, error) { return saved, nil })
	saved = []string{"C:\\New"}
	cmd := exec.CommandContext(t.Context(), "unused")
	cmd.Env = []string{}
	require.NoError(t, refresher.apply(cmd))
	_, present := windowsEnvironmentPath(cmd.Env)
	assert.False(t, present)
}

func TestWindowsScanAndLaunchUseUpdatedSavedPath(t *testing.T) {
	powershell, err := exec.LookPath("powershell")
	require.NoError(t, err)
	previous := workerWindowsPath
	t.Cleanup(func() { workerWindowsPath = previous })
	oldDir, newDir, launcher := t.TempDir(), t.TempDir(), t.TempDir()
	const oldProvider = "leapmux-old-provider.cmd"
	const newProvider = "leapmux-new-provider.cmd"
	testutil.WriteExecutable(t, filepath.Join(oldDir, oldProvider), "@echo off\r\necho old-provider\r\n")
	testutil.WriteExecutable(t, filepath.Join(newDir, newProvider), "@echo off\r\necho new-provider\r\n")
	t.Setenv("PATH", strings.Join([]string{launcher, oldDir, os.Getenv("PATH")}, ";"))

	saved := []string{oldDir}
	var readErr error
	workerWindowsPath = newWindowsPathRefresh(func() ([]string, error) { return saved, readErr })
	list := []Locator{Binaries(oldProvider), Binaries(newProvider)}
	assert.Equal(t, []Resolution{Found, Missing}, ScanAvailability(t.Context(), powershell, false, list))

	saved = []string{newDir}
	assert.Equal(t, []Resolution{Missing, Found}, ScanAvailability(t.Context(), powershell, false, list))
	snapshot, result := ShellEnv(t.Context(), ShellEnvOptions{
		Shell: "powershell", Env: []string{"Path=" + oldDir}, Variables: []string{"PATH"},
	})
	require.Equal(t, ProbeYes, result)
	assert.Equal(t, oldDir, snapshot.Values["PATH"])

	readErr = errors.New("Windows environment block unavailable")
	assert.Equal(t, []Resolution{Unknown, Unknown}, ScanAvailability(t.Context(), powershell, false, list))
	spec, err := Binaries(oldProvider, newProvider).Resolve(t.Context(), powershell, false, "Test provider")
	require.NoError(t, err)
	custom := Custom(func(ctx context.Context, shell string, login bool) (Spec, Resolution) {
		if CheckBinary(ctx, shell, login, newProvider) == ProbeYes {
			return Spec{Program: newProvider}, Found
		}
		return Spec{}, Unknown
	})
	assert.Equal(t, []Resolution{Unknown}, ScanAvailability(t.Context(), powershell, false, []Locator{custom}))
	customSpec, err := custom.Resolve(t.Context(), powershell, false, "Custom test provider")
	require.NoError(t, err)
	assert.Equal(t, newProvider, customSpec.Program)

	cmd, _, _ := wrapTestCommand(t, t.Context(), WrapSpec{Shell: powershell, Launch: spec})
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	assert.Contains(t, string(output), "new-provider")
}

func TestPreparedWindowsShellResolvesTheRefreshedPath(t *testing.T) {
	oldDir, newDir := t.TempDir(), t.TempDir()
	const name = "leapmux-test-shell.exe"
	testutil.WriteExecutable(t, filepath.Join(oldDir, name), "old")
	testutil.WriteExecutable(t, filepath.Join(newDir, name), "new")
	t.Setenv("PATH", oldDir+";"+os.Getenv("PATH"))
	cmd := &exec.Cmd{Env: []string{"Path=" + newDir}}

	path, err := resolvePreparedShell(cmd, name)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(newDir, name), path)

	cmd.Env = []string{"Path=" + t.TempDir()}
	_, err = resolvePreparedShell(cmd, name)
	require.ErrorIs(t, err, exec.ErrNotFound)
}

func TestProgramPathDoesNotStopAtAShellOnlyWindowsShim(t *testing.T) {
	for _, name := range []string{"powershell", "git-bash", "nu", "xonsh"} {
		t.Run(name, func(t *testing.T) {
			var shell string
			switch name {
			case "powershell":
				shell = requireProfileShell(t, name, name)
			case "git-bash":
				shell = requireWindowsGitBash(t)
				t.Setenv("BASH_ENV", "")
			default:
				var err error
				shell, err = exec.LookPath(name)
				if err != nil {
					t.Skipf("%s is unavailable: %v", name, err)
				}
			}

			shims, programs := t.TempDir(), t.TempDir()
			original := workerWindowsPath
			t.Cleanup(func() { workerWindowsPath = original })
			const candidate = "leapmux-probe-native-interpreter"
			testutil.WriteExecutable(t, filepath.Join(shims, candidate+".cmd"), "@echo off\r\necho batch shim\r\n")
			testutil.WriteExecutable(t, filepath.Join(shims, candidate+".ps1"), "exit 0\r\n")
			if name != "powershell" {
				testutil.WriteExecutable(t, filepath.Join(shims, candidate), "#!/bin/sh\nprintf 'shell shim\\n'\n")
			}
			executable := installWindowsProfilePeer(t, programs, candidate+".exe")
			saved := []string{shims, programs}
			workerWindowsPath = newWindowsPathRefresh(func() ([]string, error) { return saved, nil })
			t.Setenv("PATH", strings.Join(append(saved, os.Getenv("PATH")), ";"))
			t.Setenv("PATHEXT", ".CMD;.BAT;.EXE;.COM")

			path, result := ProgramPath(t.Context(), shell, false, candidate)
			require.Equal(t, ProbeYes, result)
			assert.True(t, strings.EqualFold(executable, filepath.Clean(path)), "expected %q, got %q", executable, path)
			require.NoError(t, os.Remove(executable))
			path, result = ProgramPath(t.Context(), shell, false, candidate)
			require.Equal(t, ProbeNo, result)
			assert.Empty(t, path)

			if name == "powershell" || name == "git-bash" {
				command, message := candidate, "shell shim"
				if name == "powershell" {
					command, message = candidate+".cmd", "batch shim"
				}
				require.Equal(t, ProbeYes, CheckBinary(t.Context(), shell, false, command))
				cmd, _, _ := wrapTestCommand(t, t.Context(), WrapSpec{Shell: shell, Launch: ShellCommands(command)})
				output, err := cmd.CombinedOutput()
				require.NoError(t, err, "%s", output)
				assert.Contains(t, string(output), message)
			}
		})
	}
}
