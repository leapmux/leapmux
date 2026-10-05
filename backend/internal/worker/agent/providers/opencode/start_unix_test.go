//go:build unix

package opencode

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// OpenCode upgrades its own install (curl, npm, brew and the other methods) from
// the check that its TUI starts one second after it opens, unless a truthy
// OPENCODE_DISABLE_AUTOUPDATE (`true` or `1`) says otherwise. `opencode acp`
// never starts that check, but an `opencode` that the agent's own tool starts
// inherits the environment, so the launch pins the switch after the user's
// profile. An inline OPENCODE_CONFIG_CONTENT `autoupdate` key cannot carry the
// switch: the check reads only the global config files.
func TestStartPinsTheUpdaterOff(t *testing.T) {
	agenttest.RequireLaunchPins(t, agenttest.LaunchProbe{
		Binary: "opencode", Start: Start, Pins: []string{"OPENCODE_DISABLE_AUTOUPDATE=1"},
	})
}
