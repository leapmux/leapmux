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
	shell, err := exec.LookPath("powershell")
	require.NoError(t, err)
	shims, programs := t.TempDir(), t.TempDir()
	original := workerWindowsPath
	t.Cleanup(func() { workerWindowsPath = original })
	const candidate = "leapmux-probe-native-interpreter"
	testutil.WriteExecutable(t, filepath.Join(shims, candidate+".cmd"), "@exit /b 0\r\n")
	testutil.WriteExecutable(t, filepath.Join(shims, candidate+".ps1"), "exit 0\r\n")
	executable := filepath.Join(programs, candidate+".exe")
	peer := profileCommandExecutable(t)
	contents, err := os.ReadFile(peer)
	require.NoError(t, err)
	testutil.WriteExecutable(t, executable, string(contents))
	saved := []string{shims, programs}
	workerWindowsPath = newWindowsPathRefresh(func() ([]string, error) { return saved, nil })
	t.Setenv("PATH", strings.Join(saved, ";"))
	t.Setenv("PATHEXT", ".CMD;.EXE;.COM")
	path, result := ProgramPath(t.Context(), shell, false, candidate)
	require.Equal(t, ProbeYes, result)
	assert.Equal(t, executable, path)
	require.NoError(t, os.Remove(executable))
	path, result = ProgramPath(t.Context(), shell, false, candidate)
	require.Equal(t, ProbeNo, result)
	assert.Empty(t, path)
	assert.Equal(t, ProbeYes, CheckBinary(t.Context(), shell, false, candidate), "batch files are shell commands")
}
