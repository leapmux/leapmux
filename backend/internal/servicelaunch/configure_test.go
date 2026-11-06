package servicelaunch

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
		Manager:         mgr,
		Platform:        "darwin",
		NativeMechanism: "launchd user agent",
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

	t.Run("Preview names the change and the elevation", func(t *testing.T) {
		mgr := &recordingManager{}
		c, _ := newTestController(t, mgr)

		spec := testSpec()
		change, err := c.Preview(context.Background(), spec)
		require.NoError(t, err)
		assert.False(t, change.RequiresElevation, "login startup on this account needs no elevation")
		assert.Contains(t, change.NativeSummary, "launchd user agent")
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
