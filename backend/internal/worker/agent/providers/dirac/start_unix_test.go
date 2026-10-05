//go:build unix

package dirac

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// Dirac starts a detached update of its own install from its startup path
// unless DIRAC_NO_AUTO_UPDATE is exactly "1" (dist/cli.mjs autoUpdateOnStartup),
// and the ACP mode takes that path. An update replaces the files of a running
// CLI, so the launch pins the switch off after the user's profile.
func TestStartPinsTheUpdaterOff(t *testing.T) {
	agenttest.RequireLaunchPins(t, agenttest.LaunchProbe{
		Binary: "dirac", Start: Start, Pins: []string{"DIRAC_NO_AUTO_UPDATE=1"},
	})
}
