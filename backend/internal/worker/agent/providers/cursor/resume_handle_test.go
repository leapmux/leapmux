package cursor

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestCursorResumeHandleIsAToken(t *testing.T) {
	t.Parallel()
	agenttest.AssertTokenResumeRule(t, Registration().Plugin)
}
