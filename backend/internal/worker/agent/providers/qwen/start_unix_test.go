//go:build unix

package qwen

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// Qwen Code checks for an update, and installs it, from its interactive UI
// unless the exact value "true" of QWEN_CODE_SKIP_UPDATE_CHECK_ONCE says
// otherwise. `--acp` never reaches the UI, but a `qwen` that the agent's own
// shell tool starts inherits the environment (QWEN_CODE_CLI), so the launch pins
// the switch after the user's profile.
func TestStartPinsTheUpdaterOff(t *testing.T) {
	agenttest.RequireLaunchPins(t, agenttest.LaunchProbe{
		Binary: "qwen", Start: Start, Pins: []string{"QWEN_CODE_SKIP_UPDATE_CHECK_ONCE=true"},
	})
}
