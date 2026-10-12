//go:build unix

package codebuddy

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// CodeBuddy Code updates its own install unless a truthy DISABLE_AUTOUPDATER
// says otherwise (BooleanUtils.isTruthy). The `-p` mode that the worker starts
// skips the updater today, but a daemon (CODEBUDDY_SESSION_KIND=daemon) runs it,
// so the launch pins the switch off after the user's profile.
func TestStartPinsTheUpdaterOff(t *testing.T) {
	agenttest.RequireLaunchPins(t, agenttest.LaunchProbe{
		Binary: "codebuddy", Start: Start, Pins: []string{"DISABLE_AUTOUPDATER=1"},
	})
}
