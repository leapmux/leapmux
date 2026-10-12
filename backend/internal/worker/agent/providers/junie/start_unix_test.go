//go:build unix

package junie

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// Junie's own update check and download stop for a true JUNIE_SKIP_UPDATE_CHECK
// or for `--skip-update-check`. The flag reaches the program as an argument, so
// no profile can replace it. The variable is the switch that a `junie` started
// by the agent's own tool inherits, so the launch pins it after the user's
// profile. Neither stops the shim, which applies a staged update before it
// starts the CLI.
func TestStartPinsTheUpdaterOff(t *testing.T) {
	runs := agenttest.RequireLaunchPins(t, agenttest.LaunchProbe{
		Binary: "junie", Start: Start, Pins: []string{"JUNIE_SKIP_UPDATE_CHECK=1"},
	})
	for _, run := range runs {
		assert.Contains(t, run.Args, "--skip-update-check")
	}
}
