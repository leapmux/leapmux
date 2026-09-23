package codex

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestCodexResumeHandleIsAToken(t *testing.T) {
	t.Parallel()
	agenttest.AssertTokenResumeRule(t, Registration().Plugin)
}
