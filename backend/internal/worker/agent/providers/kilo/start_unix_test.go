//go:build unix

package kilo

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// Kilo CLI is a fork of OpenCode. Its TUI checks for a release and installs a
// patch release through npm, yarn, pnpm or bun, unless a truthy
// KILO_DISABLE_AUTOUPDATE (`true` or `1`) says otherwise. `kilo acp` never starts
// that check, but a `kilo` that the agent's own tool starts inherits the
// environment, so the launch pins the switch after the user's profile.
func TestStartPinsTheUpdaterOff(t *testing.T) {
	agenttest.RequireLaunchPins(t, agenttest.LaunchProbe{
		Binary: "kilo", Start: Start, Pins: []string{"KILO_DISABLE_AUTOUPDATE=1"},
	})
}
