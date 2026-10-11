//go:build unix

package copilot

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// Copilot CLI 1.0.87 starts its updater one second after any start that is not a
// subcommand, `--server --stdio` included. The updater downloads the newest
// package and then replaces the installed executable itself. It stops only for
// COPILOT_AUTO_UPDATE=false (case-insensitive; `0` and `off` do not count), the
// flag `--no-auto-update`, `--prefer-version` or COPILOT_OFFLINE. The variable is
// the one that children inherit, and both the loader and the app read it.
func TestStartPinsTheUpdaterOff(t *testing.T) {
	agenttest.RequireLaunchPins(t, agenttest.LaunchProbe{
		Binary: copilotBinaryName, Start: Start, Pins: []string{"COPILOT_AUTO_UPDATE=false"},
	})
}
