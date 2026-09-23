package reasonix

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// Reasonix sends the STANDARD Agent Client Protocol elicitation, so its requests
// reach the shared dispatcher rather than its own extra-method hook. Its identity
// rule is therefore the shared one, exercised here through the same path a live
// request takes.
func TestReasonixControlRequestsKeepNumericAndStringIdentitiesSeparate(t *testing.T) {
	sink := &agenttest.ControlSink{}
	a := &Agent{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	agenttest.AssertControlIdentitiesStaySeparate(t, sink, a.HandleOutput, contracts.MCPElicitationMethodACP)
}
