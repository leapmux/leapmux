package claude

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestClaudeSessionInputRejectsMissingAndReplacedTargets(t *testing.T) {
	claude := &Agent{sink: agent.NewProviderServices(&agenttest.Sink{})}
	claude.claudeCodeHandleSystemInit([]byte(`{"session_id":"current"}`))
	agenttest.AssertRejectsMissingAndReplacedSessions(t, claude)
}
