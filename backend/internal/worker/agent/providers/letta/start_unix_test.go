//go:build unix

package letta

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// Letta Code runs `npm install -g @letta-ai/letta-code` from its startup path
// unless DISABLE_AUTOUPDATER is exactly "1" (letta.js isAutoUpdateEnabled). The
// App Server (`letta server`) skips that path, but each subagent is a `letta`
// child that takes it, and the child inherits the App Server's environment
// (composeSubagentChildEnv). An update replaces the files of the running CLI and
// the operator's global install, so the launch pins the switch off after the
// user's profile.
func TestStartPinsTheUpdaterOff(t *testing.T) {
	agenttest.RequireLaunchPins(t, agenttest.LaunchProbe{
		Binary: lettaBinaryName, Start: Start, Pins: []string{"DISABLE_AUTOUPDATER=1"},
	})
}
