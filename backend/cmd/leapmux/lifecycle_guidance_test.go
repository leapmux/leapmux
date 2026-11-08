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

	"github.com/leapmux/leapmux/internal/servicelaunch"
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
			assert.Contains(t, output, "  Start:   ")
			assert.Contains(t, output, "  Status:  ")
			assert.Contains(t, output, "  Disable: ")
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
	for verb, label := range map[string]string{"start": "  Start:   ", "status": "  Status:  ", "disable": "  Disable: "} {
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
	printInstanceCommands(&output, serviceSelection(spec), shellquote.PowerShell, func(string) (string, error) { return "", exec.ErrNotFound }, "start", "status")
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

// The labels align on the longest verb, and a call without verbs prints no
// block at all.
func TestPrintInstanceCommandsAlignsTheLabels(t *testing.T) {
	selection := instanceSelection{
		mode: "solo", executable: "/opt/leapmux/bin/leapmux",
		configFile: "/home/alice/solo.yaml", dataDirectory: "/home/alice/solo",
	}
	notFound := func(string) (string, error) { return "", exec.ErrNotFound }
	var output strings.Builder
	printInstanceCommands(&output, selection, shellquote.POSIX, notFound, "status", "stop", "disable")
	assert.Equal(t, "\nCommands for this instance:\n"+
		"  Status:  /opt/leapmux/bin/leapmux solo status --config /home/alice/solo.yaml --data-dir /home/alice/solo\n"+
		"  Stop:    /opt/leapmux/bin/leapmux solo stop --config /home/alice/solo.yaml --data-dir /home/alice/solo\n"+
		"  Disable: /opt/leapmux/bin/leapmux solo disable --config /home/alice/solo.yaml --data-dir /home/alice/solo\n",
		output.String())

	output.Reset()
	printInstanceCommands(&output, selection, shellquote.POSIX, notFound)
	assert.Empty(t, output.String())
}

// A successful start states where to open the instance and what to run next.
// A new dev instance asks for its first administrator, and an instance that
// reports no URL prints no address.
func TestStartPrintsTheURLAndTheNextCommands(t *testing.T) {
	for _, test := range []struct {
		name      string
		mode      lifecycle.Role
		accessURL string
		readiness string
		wantOpen  string
	}{
		{"a ready hub", lifecycle.RoleHub, "http://localhost:4327", lifecycle.ReadinessReady,
			"Open http://localhost:4327 in your browser.\n"},
		{"a dev instance that waits for setup", lifecycle.RoleDev, "http://localhost:4327", lifecycle.ReadinessReadyForSetup,
			"Open http://localhost:4327 in your browser to create the first administrator.\n"},
		{"an instance without a URL", lifecycle.RoleHub, "", lifecycle.ReadinessReady, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec := managedTestSpec(t)
			spec.Mode = test.mode
			spec.InstanceID = lifecycle.NewInstanceID(spec.AccountID, spec.Mode, spec.DataDirectory).String()
			saveManagedTestSpec(t, spec)
			manager := &readinessExecutionManager{
				nativeStartRecorder: &nativeStartRecorder{t: t, installed: true, accessURL: test.accessURL},
				execution:           lifecycle.ExecutionService, readiness: test.readiness,
			}
			output, err := captureStdout(t, func() error {
				return executeNativeStartWithController(spec.Mode.String(), "start",
					[]string{"--data-dir", spec.DataDirectory, "--yes"}, nativeRecorderController(manager))
			})
			require.NoError(t, err)
			assert.Contains(t, output, capitalize(spec.Mode.String())+" started.\n"+test.wantOpen+"\nCommands for this instance:\n")
			if test.wantOpen == "" {
				assert.NotContains(t, output, "Open ")
			}
			assertNextCommands(t, output, serviceSelection(spec), "status", "stop", "disable")
		})
	}
}

// After stop, the next commands start the instance again, and disable its
// service when one exists. A saved service selects its own configuration
// file, not the default that the stop command read.
func TestStopPrintsTheNextCommands(t *testing.T) {
	t.Run("without a service", func(t *testing.T) {
		root := mustCanonicalDir(t, t.TempDir())
		configuration := filepath.Join(t.TempDir(), "hub.yaml")
		require.NoError(t, os.WriteFile(configuration, []byte("{}\n"), 0o600))
		output, err := captureStdout(t, func() error {
			return runModeStopWithController("hub", nativeRecorderController(&nativeStartRecorder{t: t}))(cmdCtx{},
				[]string{"--config", configuration, "--data-dir", root})
		})
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(output, "Not running.\n"), output)
		assertNextCommands(t, output, currentSelection(t, "hub", configuration, root), "start")
	})
	t.Run("with a service", func(t *testing.T) {
		spec := managedTestSpec(t)
		saveManagedTestSpec(t, spec)
		output, err := captureStdout(t, func() error {
			return runModeStopWithController("hub", nativeRecorderController(&nativeStartRecorder{t: t, installed: true}))(cmdCtx{},
				[]string{"--data-dir", spec.DataDirectory})
		})
		require.NoError(t, err)
		assertNextCommands(t, output, serviceSelection(spec), "start", "disable")
	})
}

// After disable, the next commands create the service again: start does so
// and starts the instance, enable does so without starting it. The removed
// specification still selects the configuration file that the service used.
func TestDisablePrintsTheNextCommands(t *testing.T) {
	t.Run("a removed service", func(t *testing.T) {
		spec := managedTestSpec(t)
		saveManagedTestSpec(t, spec)
		output, err := captureStdout(t, func() error {
			return runModeDisableWithController("hub", nativeRecorderController(&nativeStartRecorder{t: t, installed: true}))(cmdCtx{},
				[]string{"--data-dir", spec.DataDirectory})
		})
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(output, "Startup configuration removed."), output)
		assert.NoFileExists(t, lifecycle.PathsOf(spec.DataDirectory).ServiceSpec)
		assertNextCommands(t, output, serviceSelection(spec), "start", "enable")
	})
	t.Run("no service", func(t *testing.T) {
		root := mustCanonicalDir(t, t.TempDir())
		configuration := filepath.Join(t.TempDir(), "hub.yaml")
		require.NoError(t, os.WriteFile(configuration, []byte("{}\n"), 0o600))
		output, err := captureStdout(t, func() error {
			return runModeDisableWithController("hub", nativeRecorderController(&nativeStartRecorder{t: t}))(cmdCtx{},
				[]string{"--config", configuration, "--data-dir", root})
		})
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(output, "No startup configuration.\n"), output)
		assertNextCommands(t, output, currentSelection(t, "hub", configuration, root), "start", "enable")
	})
}

// The status report gives the URL of a running instance and the commands that
// fit its state. JSON output for a script carries neither prose nor commands.
func TestStatusPrintsTheURLAndTheNextCommands(t *testing.T) {
	t.Run("a running instance", func(t *testing.T) {
		spec := managedTestSpec(t)
		saveManagedTestSpec(t, spec)
		manager := &nativeStartRecorder{t: t, installed: true, accessURL: "http://localhost:4327"}
		require.NoError(t, manager.Start(t.Context(), servicelaunch.LaunchSpec(spec)))
		output, err := captureStdout(t, func() error {
			return executeModeStatusWithController("hub", []string{"--data-dir", spec.DataDirectory}, nativeRecorderController(manager))
		})
		require.NoError(t, err)
		assert.Contains(t, output, "URL:          http://localhost:4327\n")
		assertNextCommands(t, output, serviceSelection(spec), "stop", "restart")

		json, err := captureStdout(t, func() error {
			return executeModeStatusWithController("hub", []string{"--data-dir", spec.DataDirectory, "--json"}, nativeRecorderController(manager))
		})
		require.NoError(t, err)
		assert.Contains(t, json, `"url":"http://localhost:4327"`)
		assert.NotContains(t, json, "Commands for this instance")
	})
	t.Run("a stopped instance with a service", func(t *testing.T) {
		spec := managedTestSpec(t)
		saveManagedTestSpec(t, spec)
		output, err := captureStdout(t, func() error {
			return executeModeStatusWithController("hub", []string{"--data-dir", spec.DataDirectory}, nativeRecorderController(&nativeStartRecorder{t: t, installed: true}))
		})
		require.ErrorIs(t, err, errStoppedExit)
		assert.NotContains(t, output, "URL:")
		assertNextCommands(t, output, serviceSelection(spec), "start", "disable")
	})
	t.Run("a stopped instance without a service", func(t *testing.T) {
		root := mustCanonicalDir(t, t.TempDir())
		configuration := filepath.Join(t.TempDir(), "hub.yaml")
		require.NoError(t, os.WriteFile(configuration, []byte("{}\n"), 0o600))
		output, err := captureStdout(t, func() error {
			return executeModeStatusWithController("hub", []string{"--config", configuration, "--data-dir", root}, nativeRecorderController(&nativeStartRecorder{t: t}))
		})
		require.ErrorIs(t, err, errStoppedExit)
		assertNextCommands(t, output, currentSelection(t, "hub", configuration, root), "start")
	})
}

// Every lifecycle command prints the banner of its mode once, after its flags
// parse. --version, --help, a parse failure, and `status --json` print none.
func TestLifecycleCommandsPrintTheBanner(t *testing.T) {
	var printed []string
	original := printModeBanner
	printModeBanner = func(mode string) { printed = append(printed, mode) }
	t.Cleanup(func() { printModeBanner = original })
	for _, test := range []struct {
		name string
		args []string
		want []string
	}{
		{"status", []string{"status"}, []string{"solo"}},
		{"stop", []string{"stop"}, []string{"solo"}},
		{"disable", []string{"disable"}, []string{"solo"}},
		{"status --json", []string{"status", "--json"}, nil},
		{"--version", []string{"status", "--version"}, nil},
		{"--help", []string{"status", "--help"}, nil},
		{"a parse failure", []string{"status", "--no-such-flag"}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			printed = nil
			_, _ = captureStdout(t, func() error {
				return runModeCommand("solo", append(test.args, "--data-dir", t.TempDir()))
			})
			assert.Equal(t, test.want, printed)
		})
	}
}

// assertNextCommands asserts that the output ends with the block of next
// commands: one line for each verb, in this order, each selecting the
// instance. The exact line format has its own test above.
func assertNextCommands(t *testing.T, output string, selection instanceSelection, verbs ...string) {
	t.Helper()
	var want strings.Builder
	printInstanceCommands(&want, selection, shellquote.ForOS(runtime.GOOS), exec.LookPath, verbs...)
	assert.True(t, strings.HasSuffix(output, want.String()), "want the output to end with:\n%s\ngot:\n%s", want.String(), output)
}

// currentSelection is the selection of a command without a saved service:
// the running executable and the command's own configuration file.
func currentSelection(t *testing.T, mode, configFile, dataDirectory string) instanceSelection {
	t.Helper()
	executable, err := currentExecutable()
	require.NoError(t, err)
	return instanceSelection{mode: mode, executable: executable, configFile: configFile, dataDirectory: dataDirectory}
}
