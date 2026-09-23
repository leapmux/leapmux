package pi

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func TestPiSendInputDuringActiveTurnReportsAgentBusy(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := &Agent{
		Process:           providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "test-agent"}),
		currentTurnActive: true,
		sink:              agent.NewProviderServices(sink),
	}
	agenttest.AssertBusyRefusalRepublishesTheTurn(t, sink, agent, agent.SendInput("later turn", nil))
}
