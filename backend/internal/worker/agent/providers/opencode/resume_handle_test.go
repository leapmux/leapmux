package opencode

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestOpenCodeResumeHandleIsAToken(t *testing.T) {
	t.Parallel()
	agenttest.AssertTokenResumeRule(t, Registration().Plugin)
}
