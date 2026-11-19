//go:build unix

package opencode

import (
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
)

// The launch pins the update switch after the user's profile. OpenCode tools
// that start another OpenCode process inherit the same protected value.
func TestStartPinsTheUpdaterOff(t *testing.T) {
	agenttest.RequireLaunchPins(t, agenttest.LaunchProbe{
		Binary: "opencode", Start: Start, Pins: []string{"OPENCODE_DISABLE_AUTOUPDATE=1"},
	})
}

func TestStartPreservesTheNormalProfileWithoutAHandoff(t *testing.T) {
	profile := t.TempDir()
	values := map[string]string{
		"OPENCODE_DB":     filepath.Join(profile, "native.db"),
		"XDG_CONFIG_HOME": filepath.Join(profile, "config"),
		"XDG_DATA_HOME":   filepath.Join(profile, "data"),
		"XDG_CACHE_HOME":  filepath.Join(profile, "cache"),
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
	t.Setenv("OPENCODE_PTY_HANDOFF", "another-server-handoff")
	runs := agenttest.RunLaunch(t, agenttest.LaunchProbe{
		Binary: "opencode", Start: Start, Pins: []string{openCodeDisableAutoUpdateEnv + "=1"},
	})
	for _, run := range runs {
		assert.Equal(t, openCodeServeArgs, run.Args)
		assert.Empty(t, run.Values("OPENCODE_PTY_HANDOFF"))
		for key, value := range values {
			assert.Equal(t, []string{value}, run.Values(key))
		}
	}
}
