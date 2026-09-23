package zcode

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestZCodeSessionInputRejectsMissingAndReplacedTargets(t *testing.T) {
	agenttest.AssertRejectsMissingAndReplacedSessions(t, &Agent{sessionID: "current"})
}
