package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/shellquote"
	"github.com/leapmux/leapmux/lifecycle"
)

func TestEnablePrintsInstanceManagementCommands(t *testing.T) {
	for _, mode := range []lifecycle.Role{lifecycle.RoleHub, lifecycle.RoleWorker, lifecycle.RoleSolo, lifecycle.RoleDev} {
		t.Run(mode.String(), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "instance's data")
			configuration := filepath.Join(t.TempDir(), "instance's configuration.yaml")
			require.NoError(t, os.WriteFile(configuration, []byte("{}\n"), 0o600))
			manager := &nativeStartRecorder{t: t}
			output, err := captureStdout(t, func() error {
				return runModeEnableWithController(mode.String(), nativeRecorderController(manager))(cmdCtx{}, []string{
					"--config", configuration, "--data-dir", root, "--startup=login", "--yes",
				})
			})
			require.NoError(t, err)
			require.Len(t, manager.installs, 1)
			assert.Contains(t, output, "  Start:  ")
			assert.Contains(t, output, "  Status: ")
			assert.Empty(t, manager.starts, "configuration must not start the instance")
		})
	}
}

// The printed commands are exact. A shell reads each one back as the saved
// program, configuration file, and data directory, and the printed start
// command starts the instance without asking to reconfigure it: it runs
// without --yes and without a terminal, so a configuration that differed from
// the saved one would need consent and fail.
func TestEnablePrintedCommandsSelectTheConfiguredInstance(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("this test reads the commands back through a POSIX shell; TestPrintInstanceCommandsForPowerShell covers Windows")
	}
	root := filepath.Join(t.TempDir(), "instance's data")
	configuration := filepath.Join(t.TempDir(), "instance's configuration.yaml")
	require.NoError(t, os.WriteFile(configuration, []byte("{}\n"), 0o600))
	manager := &nativeStartRecorder{t: t}
	output, err := captureStdout(t, func() error {
		return runModeEnableWithController("hub", nativeRecorderController(manager))(cmdCtx{}, []string{
			"--config", configuration, "--data-dir", root, "--startup=login", "--yes",
		})
	})
	require.NoError(t, err)
	spec, err := lifecycle.ReadServiceSpec(lifecycle.PathsOf(mustCanonicalDir(t, root)).ServiceSpec)
	require.NoError(t, err)

	var startWords []string
	for verb, label := range map[string]string{"start": "  Start:  ", "status": "  Status: "} {
		words := readCommandBackThroughShell(t, commandAfterLabel(t, output, label))
		assert.Equal(t, []string{spec.Executable, "hub", verb, "--config", spec.ConfigFile, "--data-dir", spec.DataDirectory}, words)
		if verb == "start" {
			startWords = words
		}
	}

	_, err = captureStdout(t, func() error {
		return executeNativeStartWithController("hub", "start", startWords[3:], nativeRecorderController(manager))
	})
	require.NoError(t, err)
	assert.Len(t, manager.installs, 1, "the printed command must match the saved configuration")
	assert.Len(t, manager.starts, 1)
}

func TestPrintInstanceCommandsForPowerShell(t *testing.T) {
	spec := lifecycle.ServiceSpec{
		Mode:          lifecycle.RoleWorker,
		Executable:    `C:\Program Files\LeapMux\leapmux.exe`,
		ConfigFile:    `C:\Users\alice\it's\worker.yaml`,
		DataDirectory: `C:\Users\alice\worker`,
	}
	var output strings.Builder
	printInstanceCommands(&output, spec, shellquote.PowerShell, func(string) (string, error) { return "", exec.ErrNotFound })
	assert.Equal(t, "\nCommands for this instance:\n"+
		`  Start:  & 'C:\Program Files\LeapMux\leapmux.exe' worker start --config 'C:\Users\alice\it''s\worker.yaml' --data-dir C:\Users\alice\worker`+"\n"+
		`  Status: & 'C:\Program Files\LeapMux\leapmux.exe' worker status --config 'C:\Users\alice\it''s\worker.yaml' --data-dir C:\Users\alice\worker`+"\n",
		output.String())
}

// The printed program is `leapmux` only when that name on PATH runs the same
// executable as the service. Otherwise it is the executable's own path, so the
// command never runs another copy of LeapMux.
func TestInstanceCommandProgram(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "leapmux-service")
	require.NoError(t, os.WriteFile(executable, []byte("binary"), 0o600))
	sameFile := filepath.Join(dir, "same-file")
	require.NoError(t, os.Link(executable, sameFile))
	other := filepath.Join(dir, "other")
	require.NoError(t, os.WriteFile(other, []byte("binary"), 0o600))
	found := func(path string) func(string) (string, error) {
		return func(name string) (string, error) {
			assert.Equal(t, "leapmux", name)
			return path, nil
		}
	}

	assert.Equal(t, "leapmux", instanceCommandProgram(executable, found(executable)))
	assert.Equal(t, "leapmux", instanceCommandProgram(executable, found(sameFile)), "another name of the same file is the same program")
	assert.Equal(t, executable, instanceCommandProgram(executable, found(other)))
	assert.Equal(t, executable, instanceCommandProgram(executable, found(filepath.Join(dir, "missing"))))
	assert.Equal(t, executable, instanceCommandProgram(executable, func(string) (string, error) { return "", exec.ErrNotFound }))
	missing := filepath.Join(dir, "removed-service")
	assert.Equal(t, missing, instanceCommandProgram(missing, found(executable)), "an executable that cannot be read is printed as it is")
}

// commandAfterLabel returns the command that follows a label at the start of
// a line.
func commandAfterLabel(t *testing.T, output, label string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if command, found := strings.CutPrefix(line, label); found {
			return command
		}
	}
	require.FailNow(t, "the output has no "+strings.TrimSpace(label)+" line", output)
	return ""
}

// readCommandBackThroughShell returns the words that a POSIX shell reads from
// a printed command line.
func readCommandBackThroughShell(t *testing.T, command string) []string {
	t.Helper()
	output, err := exec.Command("sh", "-c", `printf '%s\0' `+command).Output()
	require.NoError(t, err)
	return strings.Split(strings.TrimSuffix(string(output), "\x00"), "\x00")
}
