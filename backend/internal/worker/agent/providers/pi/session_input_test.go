package pi

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestPiSessionInputRejectsMissingAndReplacedTargets(t *testing.T) {
	agenttest.AssertRejectsMissingAndReplacedSessions(t, &Agent{sessionID: "runtime", sessionFile: "current"})
}
