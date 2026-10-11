//go:build unix

package grok

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// `grok agent stdio` starts a background `grok update` when the running
// executable is the managed install ($GROK_HOME/bin/grok, which the install
// script creates), and the update rewrites that install and its symlinks in the
// middle of the session. It stops for any value of GROK_DISABLE_AUTOUPDATER that
// is not empty, `0`, `false`, `off` or `no`. `--no-auto-update` is a flag of the
// top-level command only, so `agent` does not accept it.
func TestStartPinsTheUpdaterOff(t *testing.T) {
	agenttest.RequireLaunchPins(t, agenttest.LaunchProbe{
		Binary: "grok", Start: Start, Pins: []string{"GROK_DISABLE_AUTOUPDATER=1"},
	})
}
