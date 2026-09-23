package copilot

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestCopilotSessionInputRejectsMissingAndReplacedTargets(t *testing.T) {
	agenttest.AssertRejectsMissingAndReplacedSessions(t, &copilotAgent{sessionID: "current", copilotConnection: &copilotConnection{}})
}
