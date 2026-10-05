//go:build unix

package claude

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// Claude Code updates its own install from the auto-updater components of its
// interactive UI unless a truthy DISABLE_AUTOUPDATER says otherwise (gEe in the
// 2.1.289 binary). The stream-json mode that the worker starts renders no UI,
// but a `claude` that the agent's own tool starts inherits the environment, so
// the launch pins the switch off after the user's profile.
func TestStartPinsTheUpdaterOff(t *testing.T) {
	agenttest.RequireLaunchPins(t, agenttest.LaunchProbe{
		Binary: "claude", Start: Start, Pins: []string{"DISABLE_AUTOUPDATER=1"},
	})
}
