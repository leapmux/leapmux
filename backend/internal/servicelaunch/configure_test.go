package servicelaunch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/lifecycle"
)

// recordingManager records every native call and answers scripted
// states, so the controller's flow is tested against observed calls
// rather than mocked return values alone.
type recordingManager struct {
	mu sync.Mutex

	inspectResult NativeStatus
	inspectErr    error
	installErr    error
	removeErr     error

	calls []string
}

func (m *recordingManager) Inspect(context.Context, LaunchSpec) (NativeStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "inspect")
	return m.inspectResult, m.inspectErr
}

func (m *recordingManager) Install(context.Context, LaunchSpec) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "install")
	return m.installErr
}

func (m *recordingManager) Start(context.Context, LaunchSpec) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "start")
	return nil
}

func (m *recordingManager) Stop(context.Context, LaunchSpec) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "stop")
	return nil
}

func (m *recordingManager) Remove(context.Context, LaunchSpec) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "remove")
	return m.removeErr
}

func (m *recordingManager) Diagnose(context.Context, LaunchSpec) ([]string, error) {
	return []string{"native diagnostic"}, nil
}

func (m *recordingManager) recorded() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.calls...)
}

// newTestController wires a controller over a recording manager and an
// in-memory specification store.
func newTestController(t *testing.T, mgr Manager) (*Controller, *lifecycle.ServiceSpec) {
	t.Helper()
	stored := &lifecycle.ServiceSpec{}
	present := false
	c := &Controller{
		Manager:  mgr,
		Platform: "darwin",
		SaveSpec: func(spec lifecycle.ServiceSpec) error {
			*stored, present = spec, true
			return nil
		},
		ReadSpec: func() (lifecycle.ServiceSpec, error) {
			if !present {
				return lifecycle.ServiceSpec{}, os.ErrNotExist
			}
			return *stored, nil
		},
		RemoveSpec: func() error {
			present = false
			return nil
		},
	}
	return c, stored
}

// TestConfigurationFailureIsRepairable pins the failure posture: a
// denial or a failure between the specification save and the native
// install leaves consistent inspection output, and the next Apply
// repairs what the failure left.
func TestConfigurationFailureIsRepairable(t *testing.T) {
	t.Run("a denied install leaves the saved specification for the repair", func(t *testing.T) {
		mgr := &recordingManager{installErr: errors.New("authorization denied")}
		c, _ := newTestController(t, mgr)

		err := c.Apply(context.Background(), testSpec())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "authorization denied")

		// The saved specification exists; inspection reports the native
		// definition as not installed -- drift, not a complete
		// configuration.
		_, native, inspectErr := c.Inspect(context.Background())
		require.NoError(t, inspectErr)
		assert.False(t, native.Installed)
		assert.False(t, native.DefinitionMatches)

		// The repair: a clean Apply installs from the saved
		// specification.
		mgr.installErr = nil
		mgr.inspectResult = NativeStatus{Installed: true, DefinitionMatches: true}
		require.NoError(t, c.Apply(context.Background(), testSpec()))
		assert.Contains(t, mgr.recorded(), "install")
	})

	t.Run("a cancellation between the halves changes no native state", func(t *testing.T) {
		mgr := &recordingManager{installErr: context.Canceled}
		c, _ := newTestController(t, mgr)

		err := c.Apply(context.Background(), testSpec())
		require.ErrorIs(t, err, context.Canceled)
		assert.Equal(t, []string{"install"}, mgr.recorded(),
			"nothing beyond the denied install ran")
	})

	t.Run("a changed native definition reports as drift", func(t *testing.T) {
		mgr := &recordingManager{inspectResult: NativeStatus{Installed: true, DefinitionMatches: false}}
		c, _ := newTestController(t, mgr)
		require.NoError(t, c.Apply(context.Background(), testSpec()))

		_, native, err := c.Inspect(context.Background())
		require.NoError(t, err)
		assert.True(t, native.Installed)
		assert.False(t, native.DefinitionMatches, "a changed definition is drift, not a complete configuration")
	})

	t.Run("Preview identifies the change and the elevation", func(t *testing.T) {
		mgr := &recordingManager{}
		c, _ := newTestController(t, mgr)

		spec := testSpec()
		change, err := c.Preview(context.Background(), spec)
		require.NoError(t, err)
		assert.False(t, change.RequiresElevation, "login startup on this account needs no elevation")
		assert.Contains(t, change.NativeSummary, "LaunchAgent")
		assert.NotEmpty(t, change.Changes)

		spec.Startup = lifecycle.StartupBoot
		change, err = c.Preview(context.Background(), spec)
		require.NoError(t, err)
		assert.True(t, change.RequiresElevation)
		assert.NotEmpty(t, change.ElevationReason)
	})

	t.Run("Remove uninstalls the definition and the specification", func(t *testing.T) {
		mgr := &recordingManager{}
		c, _ := newTestController(t, mgr)
		require.NoError(t, c.Apply(context.Background(), testSpec()))

		require.NoError(t, c.Remove(context.Background()))
		assert.Contains(t, mgr.recorded(), "remove")
		_, err := c.ReadSpec()
		assert.ErrorIs(t, err, os.ErrNotExist, "the saved specification is gone")
	})
}

func TestConfigurationValidationPrecedesPublication(t *testing.T) {
	t.Run("invalid preview returns an error without panic", func(t *testing.T) {
		manager := &recordingManager{}
		controller, _ := newTestController(t, manager)
		invalid := testSpec()
		invalid.Executable = ""
		require.NotPanics(t, func() {
			_, err := controller.Preview(t.Context(), invalid)
			require.Error(t, err)
		})
		assert.Empty(t, manager.recorded())
	})
	t.Run("invalid saved record is explicit repair input", func(t *testing.T) {
		manager := &recordingManager{}
		controller, _ := newTestController(t, manager)
		invalid := testSpec()
		invalid.Startup = "invalid"
		require.NoError(t, controller.SaveSpec(invalid))
		require.NotPanics(t, func() {
			change, err := controller.Preview(t.Context(), testSpec())
			require.NoError(t, err)
			assert.Contains(t, strings.Join(change.Changes, "\n"), "Repair")
		})
		assert.Empty(t, manager.recorded())
	})
	t.Run("invalid apply preserves the saved specification", func(t *testing.T) {
		manager := &recordingManager{}
		controller, saved := newTestController(t, manager)
		original := testSpec()
		require.NoError(t, controller.SaveSpec(original))
		invalid := original
		invalid.Executable = "relative"
		require.Error(t, controller.Apply(t.Context(), invalid))
		assert.Equal(t, original, *saved)
		assert.Empty(t, manager.recorded())
	})
	t.Run("canceled apply changes nothing", func(t *testing.T) {
		manager := &recordingManager{}
		controller, saved := newTestController(t, manager)
		original := testSpec()
		require.NoError(t, controller.SaveSpec(original))
		changed := original
		changed.Executable = testutil.NativeAbsPath("/another/leapmux")
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		require.ErrorIs(t, controller.Apply(ctx, changed), context.Canceled)
		assert.Equal(t, original, *saved)
		assert.Empty(t, manager.recorded())
	})
}

func TestStartupModeChangeIsRecoverable(t *testing.T) {
	for _, startup := range []lifecycle.StartupKind{lifecycle.StartupLogin, lifecycle.StartupBoot} {
		for _, failure := range []string{"remove", "save", "install", "running", "none"} {
			t.Run(string(startup)+"/"+failure, func(t *testing.T) {
				manager := &recordingManager{}
				controller, saved := newTestController(t, manager)
				old := testSpec()
				old.Startup = startup
				require.NoError(t, controller.SaveSpec(old))
				desired := old
				desired.Startup = lifecycle.StartupBoot
				if startup == lifecycle.StartupBoot {
					desired.Startup = lifecycle.StartupLogin
				}
				fault := errors.New("configuration fault")
				switch failure {
				case "remove":
					manager.removeErr = fault
				case "save":
					controller.SaveSpec = func(lifecycle.ServiceSpec) error { return fault }
				case "install":
					manager.installErr = fault
				case "running":
					manager.inspectResult = NativeStatus{Installed: true, Running: true}
				}
				err := controller.Apply(t.Context(), desired)
				if failure == "none" {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
				if failure == "remove" || failure == "save" || failure == "running" {
					assert.Equal(t, old, *saved)
					assert.NotContains(t, manager.recorded(), "install")
				} else {
					assert.Equal(t, desired, *saved)
					assert.Equal(t, []string{"inspect", "remove", "install"}, manager.recorded())
				}
			})
		}
	}
}

func TestRemoveDoesNotRequireRuntimeEnvironmentFile(t *testing.T) {
	manager := &recordingManager{}
	controller, _ := newTestController(t, manager)
	spec := testSpec()
	missing := filepath.Join(t.TempDir(), "removed-environment.yaml")
	spec.EnvironmentFile = &missing
	require.NoError(t, controller.SaveSpec(spec))
	require.NoError(t, controller.Remove(t.Context()))
	assert.Equal(t, []string{"inspect", "remove"}, manager.recorded())
	_, err := controller.ReadSpec()
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestRemovePreservesConfigurationWithoutProofNativeProcessStopped(t *testing.T) {
	for _, scenario := range []string{"starting", "query failure", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			manager := &recordingManager{}
			controller, saved := newTestController(t, manager)
			original := testSpec()
			require.NoError(t, controller.SaveSpec(original))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch scenario {
			case "starting":
				manager.inspectResult = NativeStatus{Installed: true, Running: true, State: "start-pending"}
			case "query failure":
				manager.inspectErr = assert.AnError
			case "canceled":
				cancel()
			}
			err := controller.Remove(ctx)
			require.Error(t, err)
			if scenario == "starting" {
				assert.Contains(t, err.Error(), "stop")
			}
			if scenario == "query failure" {
				assert.ErrorIs(t, err, assert.AnError)
			}
			if scenario == "canceled" {
				assert.ErrorIs(t, err, context.Canceled)
			}
			assert.NotContains(t, manager.recorded(), "remove")
			assert.Equal(t, original, *saved)
		})
	}
}

func TestApplyRefusesLiveNativeConfigurationChange(t *testing.T) {
	manager := &recordingManager{inspectResult: NativeStatus{Installed: true, Running: true}}
	controller, saved := newTestController(t, manager)
	original := testSpec()
	require.NoError(t, controller.SaveSpec(original))
	desired := original
	desired.RuntimeArguments = []string{"--log-level", "debug"}
	require.ErrorContains(t, controller.Apply(t.Context(), desired), "stop")
	assert.Equal(t, original, *saved)
	assert.Equal(t, []string{"inspect"}, manager.recorded())
}

func TestPreviewDescribesSelectedNativeMechanism(t *testing.T) {
	for _, test := range []struct {
		platform  string
		startup   lifecycle.StartupKind
		mechanism string
	}{
		{"darwin", lifecycle.StartupLogin, "LaunchAgent"}, {"darwin", lifecycle.StartupBoot, "LaunchDaemon"},
		{"linux", lifecycle.StartupLogin, "systemd user service"}, {"linux", lifecycle.StartupBoot, "systemd system service"},
		{"windows", lifecycle.StartupLogin, "noninteractive scheduled task"}, {"windows", lifecycle.StartupBoot, "Windows service"},
		{"plan9", lifecycle.StartupLogin, "native startup mechanism"},
	} {
		t.Run(test.platform+"/"+string(test.startup), func(t *testing.T) {
			controller, _ := newTestController(t, &recordingManager{})
			controller.Platform = test.platform
			spec := testSpec()
			spec.Startup = test.startup
			change, err := controller.Preview(t.Context(), spec)
			require.NoError(t, err)
			assert.Contains(t, change.NativeSummary, test.mechanism)
			preview := strings.Join(change.Changes, "\n")
			assert.Contains(t, preview, spec.InstanceID)
			assert.Contains(t, preview, spec.WorkingDirectory)
			assert.Contains(t, preview, spec.AccountID)
			if test.platform == "windows" {
				assert.Contains(t, preview, "password")
				if test.startup == lifecycle.StartupLogin {
					assert.Contains(t, preview, "batch")
				} else {
					assert.Contains(t, change.ElevationReason, "service")
				}
			}
		})
	}
}

// A failed install identifies the mechanism of the selected startup kind,
// the same one the preview showed, never a fixed per-platform label.
func TestApplyFailureIdentifiesTheSelectedMechanism(t *testing.T) {
	for _, test := range []struct {
		platform  string
		startup   lifecycle.StartupKind
		mechanism string
	}{
		{"darwin", lifecycle.StartupLogin, "install the launchd LaunchAgent"},
		{"darwin", lifecycle.StartupBoot, "install the launchd LaunchDaemon"},
		{"linux", lifecycle.StartupBoot, "install the systemd system service"},
		{"windows", lifecycle.StartupLogin, "install the Windows noninteractive scheduled task"},
	} {
		t.Run(test.platform+"/"+string(test.startup), func(t *testing.T) {
			controller, _ := newTestController(t, &recordingManager{installErr: errors.New("authorization denied")})
			controller.Platform = test.platform
			spec := testSpec()
			spec.Startup = test.startup
			err := controller.Apply(t.Context(), spec)
			require.ErrorContains(t, err, test.mechanism)
			assert.ErrorContains(t, err, "authorization denied")
		})
	}
}

func TestNativeInspectionDoesNotLoadRuntimeEnvironment(t *testing.T) {
	malformed := filepath.Join(t.TempDir(), "malformed.yaml")
	require.NoError(t, os.WriteFile(malformed, []byte("TOKEN: [invalid"), 0o600))
	for _, path := range []string{filepath.Join(t.TempDir(), "missing.yaml"), malformed, "relative.yaml"} {
		t.Run(path, func(t *testing.T) {
			manager := &recordingManager{inspectResult: NativeStatus{Installed: true, DefinitionMatches: true}}
			controller, _ := newTestController(t, manager)
			spec := testSpec()
			spec.EnvironmentFile = &path
			require.NoError(t, controller.SaveSpec(spec))
			actual, native, err := controller.Inspect(t.Context())
			require.NoError(t, err, "native inspection must remain usable when a runtime file needs repair")
			assert.Equal(t, []string{"inspect"}, manager.recorded())
			assert.True(t, native.Installed)
			assert.True(t, spec.Equal(actual))
		})
	}
}

func invalidEnvironmentFiles(t *testing.T) map[string]string {
	t.Helper()
	malformed := filepath.Join(t.TempDir(), "malformed.yaml")
	require.NoError(t, os.WriteFile(malformed, []byte("TOKEN: [invalid"), 0o600))
	return map[string]string{"missing": filepath.Join(t.TempDir(), "missing.yaml"), "malformed": malformed, "relative": "relative.yaml"}
}

func TestRuntimeEnvironmentIsValidatedBeforeDesiredMutations(t *testing.T) {
	for kind, path := range invalidEnvironmentFiles(t) {
		for _, operation := range []string{"preview", "apply"} {
			t.Run(kind+"/"+operation, func(t *testing.T) {
				manager := &recordingManager{inspectResult: NativeStatus{Installed: true, DefinitionMatches: true}}
				controller, stored := newTestController(t, manager)
				previous := testSpec()
				require.NoError(t, controller.SaveSpec(previous))
				desired := previous
				desired.Startup = lifecycle.StartupBoot
				desired.EnvironmentFile = &path
				var err error
				if operation == "preview" {
					_, err = controller.Preview(t.Context(), desired)
				} else {
					err = controller.Apply(t.Context(), desired)
				}
				require.Error(t, err)
				assert.Empty(t, manager.recorded(), "invalid runtime data must precede native inspection, removal, and installation")
				assert.True(t, previous.Equal(*stored), "a failed desired validation must preserve the saved specification")
			})
		}
	}
}

func TestNativeRemovalIgnoresBrokenRuntimeSelectors(t *testing.T) {
	for kind, path := range invalidEnvironmentFiles(t) {
		for _, running := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/running=%v", kind, running), func(t *testing.T) {
				manager := &recordingManager{inspectResult: NativeStatus{Installed: true, DefinitionMatches: true, Running: running}}
				controller, _ := newTestController(t, manager)
				spec := testSpec()
				spec.EnvironmentFile = &path
				require.NoError(t, controller.SaveSpec(spec))
				err := controller.Remove(t.Context())
				if running {
					require.ErrorContains(t, err, "running")
					assert.Equal(t, []string{"inspect"}, manager.recorded())
					saved, err := controller.ReadSpec()
					require.NoError(t, err)
					assert.True(t, spec.Equal(saved))
				} else {
					require.NoError(t, err)
					assert.Equal(t, []string{"inspect", "remove"}, manager.recorded())
					_, err = controller.ReadSpec()
					assert.ErrorIs(t, err, os.ErrNotExist)
				}
			})
		}
	}
}
