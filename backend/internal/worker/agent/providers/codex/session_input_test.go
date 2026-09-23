package codex

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestCodexSessionInputRejectsMissingAndReplacedTargets(t *testing.T) {
	agenttest.AssertRejectsMissingAndReplacedSessions(t, &Agent{threadID: "current"})
}
