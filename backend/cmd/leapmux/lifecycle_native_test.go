package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/servicelaunch"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/lifecycle"
)

type nativeStartRecorder struct {
	t          *testing.T
	installed  bool
	installs   []servicelaunch.LaunchSpec
	starts     []servicelaunch.LaunchSpec
	onInstall  func(servicelaunch.LaunchSpec)
	inspection *servicelaunch.NativeStatus
	inspectErr error
	authorize  func(context.Context, servicelaunch.LaunchSpec) error
	inspect    func(context.Context, servicelaunch.LaunchSpec) (servicelaunch.NativeStatus, error)
}

func (m *nativeStartRecorder) AuthorizeStart(ctx context.Context, spec servicelaunch.LaunchSpec) error {
	if m.authorize != nil {
		return m.authorize(ctx, spec)
	}
	return nil
}

func TestNativeStartAuthorizesBeforeRuntimeBudget(t *testing.T) {
	spec := managedTestSpec(t)
	saveManagedTestSpec(t, spec)
	manager := &nativeStartRecorder{t: t, installed: true}
	authorized := false
	manager.authorize = func(ctx context.Context, actual servicelaunch.LaunchSpec) error {
		_, hasDeadline := ctx.Deadline()
		require.False(t, hasDeadline, "interactive authorization must not consume the runtime wait budget")
		require.Empty(t, manager.starts)
		held, err := lifecycle.ProbeLockAt(lifecycle.PathsOf(actual.DataDirectory).DataLock)
		require.NoError(t, err)
		require.True(t, held, "retain preflight ownership through authorization")
		authorized = true
		return nil
	}
	require.NoError(t, executeNativeStartWithController("hub", "start", []string{"--data-dir", spec.DataDirectory, "--yes"}, nativeRecorderController(manager)))
	require.True(t, authorized)
	require.Len(t, manager.starts, 1)
}

type readinessExecutionManager struct {
	*nativeStartRecorder
	execution lifecycle.ExecutionKind
	readiness string
	runtime   *lifecycle.Runtime
}

func (m *readinessExecutionManager) Start(_ context.Context, spec servicelaunch.LaunchSpec) error {
	m.starts = append(m.starts, spec)
	runtime, err := lifecycle.StartRuntime(lifecycle.RuntimeConfig{
		Mode: spec.Mode, DataDir: spec.DataDirectory, Execution: m.execution,
	})
	if err != nil {
		return err
	}
	m.runtime = runtime
	m.t.Cleanup(func() { require.NoError(m.t, runtime.Finish(nil)) })
	runtime.SetReadiness(m.readiness)
	m.inspection = &servicelaunch.NativeStatus{Installed: true, DefinitionMatches: true, Running: true}
	if m.execution == lifecycle.ExecutionForeground {
		// A foreground invocation won after preflight released ownership.
		// The native launch was accepted, but its child could not acquire it.
		m.inspection.Running = false
		m.inspection.Failure = "the native child could not acquire the data directory"
	}
	return nil
}

func TestNativeStartupRequiresServiceExecutionForReadiness(t *testing.T) {
	for _, verb := range []string{"start", "restart"} {
		for _, readiness := range []string{lifecycle.ReadinessReady, lifecycle.ReadinessReadyForSetup} {
			for _, execution := range []lifecycle.ExecutionKind{lifecycle.ExecutionForeground, lifecycle.ExecutionService} {
				t.Run(verb+"/"+readiness+"/"+string(execution), func(t *testing.T) {
					spec := managedTestSpec(t)
					if readiness == lifecycle.ReadinessReadyForSetup {
						spec.Mode = lifecycle.RoleDev
						spec.InstanceID = lifecycle.NewInstanceID(spec.AccountID, spec.Mode, spec.DataDirectory).String()
					}
					saveManagedTestSpec(t, spec)
					manager := &readinessExecutionManager{
						nativeStartRecorder: &nativeStartRecorder{t: t, installed: true},
						execution:           execution, readiness: readiness,
					}
					err := executeNativeStartWithController(spec.Mode.String(), verb,
						[]string{"--data-dir", spec.DataDirectory, "--yes"}, nativeRecorderController(manager))
					if execution == lifecycle.ExecutionService {
						assert.NoError(t, err)
					} else {
						assert.ErrorContains(t, err, "foreground", "native startup must not accept the competing foreground attempt")
					}
					require.Len(t, manager.starts, 1)
					require.NotNil(t, manager.runtime)
					status, observeErr := lifecycle.Observe(t.Context(), spec.DataDirectory)
					require.NoError(t, observeErr)
					assert.Equal(t, new(execution), status.Execution)
					assert.Equal(t, lifecycle.ProcessRunning, status.Process.State)
					assert.Equal(t, new(readiness), status.Readiness)
					assert.False(t, manager.runtime.StopRequested(), "the command must preserve the process that owns the directory")
				})
			}
		}
	}
}

func TestReadinessRejectsMissingOrInvalidExecution(t *testing.T) {
	for _, test := range []struct {
		name      string
		execution *lifecycle.ExecutionKind
	}{
		{name: "missing"},
		{name: "empty", execution: new(lifecycle.ExecutionKind(""))},
		{name: "unknown", execution: new(lifecycle.ExecutionKind("unexpected"))},
	} {
		t.Run(test.name, func(t *testing.T) {
			observations := 0
			err := waitForReadinessObserved("hub", mustCanonicalDir(t, t.TempDir()), 30*time.Second, "", lifecycle.ExecutionService,
				func(context.Context) (lifecycle.Status, error) {
					observations++
					return lifecycle.Status{
						Process:   lifecycle.ProcessStatus{State: lifecycle.ProcessRunning},
						Readiness: new(lifecycle.ReadinessReady), Execution: test.execution,
					}, nil
				})
			require.ErrorContains(t, err, "execution")
			require.NotErrorIs(t, err, context.DeadlineExceeded)
			assert.Equal(t, 1, observations, "unverified execution must refuse immediately")
		})
	}
}

func TestSavedRuntimeOverridesReplaceOnlyExplicitFlags(t *testing.T) {
	spec := managedTestSpec(t)
	spec.RuntimeArguments = []string{"--listen", "127.0.0.1:4100", "--listen", "127.0.0.1:4200", "--log-level", "debug"}
	file := filepath.Join(t.TempDir(), "environment.yaml")
	require.NoError(t, os.WriteFile(file, []byte("TOKEN: private\n"), 0o600))
	spec.EnvironmentFile = &file
	configuration, options, err := loadLifecycleCommand("hub", "start", []string{"--data-dir", spec.DataDirectory, "--listen", "127.0.0.1:4300", "--environment-file="})
	require.NoError(t, err)
	updated, err := commandServiceSpec(configuration, options, spec.Startup, &spec, false)
	require.NoError(t, err)
	require.Nil(t, updated.EnvironmentFile)
	assert.Equal(t, []string{"--listen", "127.0.0.1:4300", "--log-level", "debug"}, updated.RuntimeArguments)
	assert.Equal(t, []string{"--listen", "127.0.0.1:4100", "--listen", "127.0.0.1:4200", "--log-level", "debug"}, spec.RuntimeArguments, "do not mutate the previous snapshot")
	require.NotNil(t, spec.EnvironmentFile)
}

func TestEveryLifecycleLeafResolvesTheSameConfiguredInstance(t *testing.T) {
	for _, mode := range []string{"hub", "worker", "solo", "dev"} {
		t.Run(mode, func(t *testing.T) {
			directory := t.TempDir()
			configuration := filepath.Join(directory, "instance.yaml")
			require.NoError(t, os.WriteFile(configuration, []byte("data_dir: selected-data\n"), 0o600))
			environment := "LEAPMUX_HUB_DATA_DIR"
			if mode == "worker" {
				environment = "LEAPMUX_WORKER_DATA_DIR"
			}
			for _, source := range []string{"configuration", "environment", "explicit"} {
				t.Run(source, func(t *testing.T) {
					selected := filepath.Join(directory, "selected-data")
					t.Setenv(environment, "")
					require.NoError(t, os.Unsetenv(environment))
					args := []string{"--config", configuration}
					if source != "configuration" {
						selected = filepath.Join(directory, "environment-data")
						t.Setenv(environment, selected)
					}
					if source == "explicit" {
						selected = filepath.Join(directory, "explicit-data")
						args = append(args, "--data-dir", selected)
					}
					canonical := mustCanonicalDir(t, selected)
					for _, verb := range []string{"run", "start", "stop", "restart", "status", "enable", "disable"} {
						resolved, _, err := loadLifecycleCommand(mode, verb, args)
						require.NoError(t, err, verb)
						assert.Equal(t, canonical, resolved.dataDirectory, verb)
					}
					assert.NoDirExists(t, selected, "selection itself must remain read-only")
				})
			}
		})
	}
}

func TestConcurrentLifecycleCommandsSerializeNativeMutation(t *testing.T) {
	root := mustCanonicalDir(t, t.TempDir())
	entered, release := make(chan struct{}), make(chan struct{})
	manager := &nativeStartRecorder{t: t, onInstall: func(servicelaunch.LaunchSpec) { close(entered); <-release }}
	controller := nativeRecorderController(manager)
	done := make(chan error, 1)
	go func() {
		done <- executeNativeStartWithController("hub", "start", []string{"--data-dir", root, "--startup=boot", "--yes"}, controller)
	}()
	<-entered
	for _, verb := range []string{"start", "restart", "stop", "disable"} {
		var err error
		if verb == "start" || verb == "restart" {
			err = executeNativeStartWithController("hub", verb, []string{"--data-dir", root, "--yes"}, controller)
		} else {
			err = runMode(t, "hub", verb, "--data-dir", root)
		}
		assert.ErrorContains(t, err, "operation", verb)
	}
	assert.Len(t, manager.installs, 1)
	assert.Empty(t, manager.starts)
	close(release)
	require.NoError(t, <-done)
	assert.Len(t, manager.starts, 1)
}

func TestReadinessWaitReportsCleanExitBeforeReady(t *testing.T) {
	root := mustCanonicalDir(t, t.TempDir())
	runtime, err := lifecycle.StartRuntime(lifecycle.RuntimeConfig{Mode: lifecycle.RoleHub, DataDir: root, Execution: lifecycle.ExecutionForeground})
	require.NoError(t, err)
	require.NoError(t, runtime.Finish(nil))
	require.ErrorContains(t, waitForReadiness("hub", root, time.Second), "exited before it became ready")
}

func (m *nativeStartRecorder) Inspect(ctx context.Context, spec servicelaunch.LaunchSpec) (servicelaunch.NativeStatus, error) {
	if m.inspect != nil {
		return m.inspect(ctx, spec)
	}
	if m.inspection != nil || m.inspectErr != nil {
		if m.inspection == nil {
			return servicelaunch.NativeStatus{}, m.inspectErr
		}
		return *m.inspection, m.inspectErr
	}
	return servicelaunch.NativeStatus{Installed: m.installed, DefinitionMatches: m.installed}, nil
}

func TestRestartWaitsForNativeExitAfterApplicationJoin(t *testing.T) {
	spec := managedTestSpec(t)
	saveManagedTestSpec(t, spec)
	manager := &nativeStartRecorder{t: t, installed: true}
	inspections := 0
	manager.inspect = func(context.Context, servicelaunch.LaunchSpec) (servicelaunch.NativeStatus, error) {
		inspections++
		// The first inspection checks drift. The second still sees native
		// teardown after the application's runtime lock became free.
		return servicelaunch.NativeStatus{Installed: true, DefinitionMatches: true, Running: inspections == 2}, nil
	}
	manager.authorize = func(context.Context, servicelaunch.LaunchSpec) error {
		require.GreaterOrEqual(t, inspections, 3, "native exit must precede another start")
		return nil
	}
	require.NoError(t, executeNativeStartWithController("hub", "restart", []string{"--data-dir", spec.DataDirectory, "--yes"}, nativeRecorderController(manager)))
}

func TestStatusReconcilesNativeDefinitionWithoutMutation(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		native     servicelaunch.NativeStatus
		inspectErr error
		process    string
		configured bool
		diagnostic string
	}{
		{name: "missing", process: lifecycle.ProcessStopped, diagnostic: "missing"},
		{name: "drift", native: servicelaunch.NativeStatus{Installed: true}, process: lifecycle.ProcessStopped, diagnostic: "differs"},
		{name: "failed before application", native: servicelaunch.NativeStatus{Installed: true, DefinitionMatches: true, Failure: "executable missing"}, process: lifecycle.ProcessFailed, configured: true, diagnostic: "executable missing"},
		{name: "native query refusal", inspectErr: assert.AnError, process: lifecycle.ProcessStopped, diagnostic: assert.AnError.Error()},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			spec := managedTestSpec(t)
			saveManagedTestSpec(t, spec)
			configuration, _, err := loadLifecycleCommand("hub", "status", []string{"--data-dir", spec.DataDirectory})
			require.NoError(t, err)
			manager := &nativeStartRecorder{t: t, inspection: &scenario.native, inspectErr: scenario.inspectErr}
			status, _ := observeLifecycleModeWithController(context.Background(), configuration, nativeRecorderController(manager))
			assert.Equal(t, scenario.process, status.Process.State)
			assert.Equal(t, scenario.configured, status.Configured)
			assert.Contains(t, fmt.Sprint(status.Diagnostics), scenario.diagnostic)
			assert.Empty(t, manager.installs)
			assert.Empty(t, manager.starts)
			saved, err := lifecycle.ReadServiceSpec(lifecycle.PathsOf(spec.DataDirectory).ServiceSpec)
			require.NoError(t, err)
			assert.True(t, saved.Equal(spec))
		})
	}
}

func TestStatusJSONPreservesRuntimeObservationFailure(t *testing.T) {
	spec := managedTestSpec(t)
	saveManagedTestSpec(t, spec)
	require.NoError(t, os.WriteFile(lifecycle.PathsOf(spec.DataDirectory).RuntimeRecord, []byte("[invalid YAML"), 0o600))
	manager := &nativeStartRecorder{t: t, installed: true}
	output := testutil.CaptureStdout(t, func() {
		err := executeModeStatusWithController("hub", []string{"--data-dir", spec.DataDirectory, "--json"}, nativeRecorderController(manager))
		require.ErrorIs(t, err, errUnresponsiveExit)
	})
	var status lifecycle.Status
	require.NoError(t, json.Unmarshal([]byte(output), &status), "JSON output must not contain prose")
	assert.Contains(t, fmt.Sprint(status.Diagnostics), "runtime record")
	assert.Empty(t, manager.installs)
	assert.Empty(t, manager.starts)
}
func (m *nativeStartRecorder) Install(_ context.Context, spec servicelaunch.LaunchSpec) error {
	m.installs = append(m.installs, spec)
	if m.onInstall != nil {
		m.onInstall(spec)
	}
	m.installed = true
	return nil
}
func (m *nativeStartRecorder) Start(_ context.Context, spec servicelaunch.LaunchSpec) error {
	m.starts = append(m.starts, spec)
	runtime, err := lifecycle.StartRuntime(lifecycle.RuntimeConfig{Mode: spec.Mode, DataDir: spec.DataDirectory, Execution: lifecycle.ExecutionService})
	if err != nil {
		return err
	}
	m.t.Cleanup(func() { require.NoError(m.t, runtime.Finish(nil)) })
	runtime.SetReadiness(lifecycle.ReadinessReady)
	return nil
}
func (*nativeStartRecorder) Stop(context.Context, servicelaunch.LaunchSpec) error   { return nil }
func (*nativeStartRecorder) Remove(context.Context, servicelaunch.LaunchSpec) error { return nil }
func (*nativeStartRecorder) Diagnose(context.Context, servicelaunch.LaunchSpec) ([]string, error) {
	return nil, nil
}

func nativeRecorderController(manager servicelaunch.Manager) nativeControllerFactory {
	return func(mode, root string) (*servicelaunch.Controller, error) {
		controller, err := nativeController(mode, root)
		if err != nil {
			return nil, err
		}
		controller.Manager = manager
		return controller, nil
	}
}

func TestPlainNativeStartPreservesSavedConfiguration(t *testing.T) {
	for _, verb := range []string{"start", "restart"} {
		t.Run(verb, func(t *testing.T) {
			spec := managedTestSpec(t)
			spec.RuntimeArguments = []string{"--log-level", "debug"}
			spec.RuntimeEnvironment = map[string]string{"PATH": "/saved/provider/bin"}
			environment := filepath.Join(t.TempDir(), "environment.yaml")
			require.NoError(t, os.WriteFile(environment, []byte("API_KEY: private\n"), 0o600))
			spec.EnvironmentFile = &environment
			saveManagedTestSpec(t, spec)
			manager := &nativeStartRecorder{t: t, installed: true}
			err := executeNativeStartWithController("hub", verb, []string{"--data-dir", spec.DataDirectory, "--yes"}, nativeRecorderController(manager))
			require.NoError(t, err)
			assert.Empty(t, manager.installs, "plain execution must not rewrite the saved configuration")
			require.Len(t, manager.starts, 1)
			launch, err := servicelaunch.LaunchSpecFromService(spec)
			require.NoError(t, err)
			assert.Equal(t, launch, manager.starts[0])
			saved, err := lifecycle.ReadServiceSpec(lifecycle.PathsOf(spec.DataDirectory).ServiceSpec)
			require.NoError(t, err)
			assert.Equal(t, spec, saved)
		})
	}
}

func TestEnableRefusesActiveMaterialChangesBeforeNativeAccess(t *testing.T) {
	for _, field := range []string{"startup", "configuration", "arguments", "environment", "executable"} {
		t.Run(field, func(t *testing.T) {
			spec := managedTestSpec(t)
			if field == "executable" {
				spec.Executable = filepath.Join(spec.DataDirectory, "old-leapmux")
			}
			saveManagedTestSpec(t, spec)
			runtime, err := lifecycle.StartRuntime(lifecycle.RuntimeConfig{Mode: spec.Mode, DataDir: spec.DataDirectory, Execution: lifecycle.ExecutionForeground})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, runtime.Finish(nil)) })
			args := []string{"--data-dir", spec.DataDirectory, "--yes"}
			switch field {
			case "startup":
				args = append(args, "--startup=login")
			case "configuration":
				args = append(args, "--config", filepath.Join(t.TempDir(), "different.yaml"))
			case "arguments":
				args = append(args, "--log-level", "debug")
			case "environment":
				file := filepath.Join(t.TempDir(), "environment.yaml")
				require.NoError(t, os.WriteFile(file, []byte("TOKEN: private\n"), 0o600))
				args = append(args, "--environment-file", file)
			}
			manager := &nativeStartRecorder{t: t, inspect: func(context.Context, servicelaunch.LaunchSpec) (servicelaunch.NativeStatus, error) {
				t.Fatal("a refused material change inspected native state")
				return servicelaunch.NativeStatus{}, nil
			}}
			err = runModeEnableWithController("hub", nativeRecorderController(manager))(cmdCtx{}, args)
			require.ErrorContains(t, err, "stop")
			assert.Empty(t, manager.installs)
			assert.Empty(t, manager.starts)
			saved, err := lifecycle.ReadServiceSpec(runtime.Paths().ServiceSpec)
			require.NoError(t, err)
			assert.True(t, saved.Equal(spec))
		})
	}
}

func TestNativeConfigurationRetainsPreflightLeases(t *testing.T) {
	for _, mode := range []lifecycle.Role{lifecycle.RoleHub, lifecycle.RoleSolo, lifecycle.RoleDev} {
		t.Run(mode.String(), func(t *testing.T) {
			root := mustCanonicalDir(t, t.TempDir())
			owner := lifecycle.NewDirectoryOwner(mode, root)
			manager := &nativeStartRecorder{t: t}
			manager.onInstall = func(spec servicelaunch.LaunchSpec) {
				lease, err := lifecycle.AcquireDirectory(owner, mode, root)
				if lease != nil {
					_ = lease.Close()
				}
				assert.Error(t, err, "configuration must retain the root lease")
				if mode.Combined() {
					for _, component := range []lifecycle.Role{lifecycle.RoleHub, lifecycle.RoleWorker} {
						lease, err := lifecycle.AcquireDirectory(owner, component, filepath.Join(root, component.String()))
						if lease != nil {
							_ = lease.Close()
						}
						assert.Error(t, err, "configuration must retain the component lease")
					}
				}
			}
			err := executeNativeStartWithController(mode.String(), "start", []string{"--data-dir", root, "--startup=boot", "--yes"}, nativeRecorderController(manager))
			require.NoError(t, err, "the native child must acquire the released preflight leases")
			require.Len(t, manager.starts, 1)
		})
	}
}
