//go:build unix

package droid

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// Factory Droid downloads a release and replaces its own executable unless
// FACTORY_DROID_AUTO_UPDATE_ENABLED is exactly "0" or "false" (any other value
// takes the default of the build, which is off in the npm build and may be on in
// another). The `droid exec` mode that the worker starts never reaches the
// updater, but a `droid` that the agent's own tool starts inherits the
// environment, so the launch pins the switch off after the user's profile.
func TestStartPinsTheUpdaterOff(t *testing.T) {
	agenttest.RequireLaunchPins(t, agenttest.LaunchProbe{
		Binary: droidBinaryName, Start: Start, Pins: []string{"FACTORY_DROID_AUTO_UPDATE_ENABLED=0"},
	})
}
