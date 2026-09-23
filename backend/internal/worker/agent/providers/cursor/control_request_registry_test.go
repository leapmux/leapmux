package cursor

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestCursorControlRequestsKeepNumericAndStringIdentitiesSeparate(t *testing.T) {
	sink := &agenttest.ControlSink{}
	agenttest.AssertControlIdentitiesStaySeparate(t, sink, newCursorAgentWithSink(agent.NewProviderServices(sink)).HandleOutput,
		contracts.CursorMethodAskQuestion)
}
