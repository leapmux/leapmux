package zcode

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// ZCode inherits nothing: it OVERRIDES the method and delegates to the same
// validator, so the whole token contract is asserted against the override.
func TestZCodeResumeHandleIsAToken(t *testing.T) {
	t.Parallel()
	agenttest.AssertTokenResumeRule(t, Registration().Plugin)
}
