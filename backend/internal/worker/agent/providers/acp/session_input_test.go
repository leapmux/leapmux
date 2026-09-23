package acp

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestACPSessionInputRejectsMissingAndReplacedTargets(t *testing.T) {
	agenttest.AssertRejectsMissingAndReplacedSessions(t, &Base{sessionID: "current"})
}
