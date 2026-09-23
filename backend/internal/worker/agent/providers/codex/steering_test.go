package codex

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexSteerUsesExpectedActiveTurn(t *testing.T) {
	t.Parallel()

	agent, _, requests := newCodexAgentForRPC(t, func(string) agenttest.RPCReply { return agenttest.RPCReply{Result: json.RawMessage(`{}`)} })
	agent.threadID = "thread-1"
	agent.turnID = "turn-1"
	require.NoError(t, agent.SteerInput("guide", nil))
	require.Len(t, requests(), 1)
	assert.Equal(t, "turn/steer", requests()[0].Method)
	assert.Equal(t, "turn-1", requests()[0].Params["expectedTurnId"])
}

func TestCodexSteerMapsEndedTurnResponse(t *testing.T) {
	t.Parallel()

	a, _, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
		return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32602,"message":"turn is no longer active"}`)}
	})
	a.threadID = "thread-1"
	a.turnID = "turn-1"
	assert.ErrorIs(t, a.SteerInput("guide", nil), agent.ErrNoActiveTurn)
}

func TestCodexSteerProcessExitIsDeliveryUncertain(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	defer close(release)
	a, _, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
		<-release
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.threadID = "thread-1"
	a.turnID = "turn-1"
	a.SimulateExitForTest()

	assert.ErrorIs(t, a.SteerInput("guide", nil), agent.ErrDeliveryUncertain)
}

func TestCodexSendInputDuringActiveTurnReportsAgentBusy(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	ag, _, requests := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	ag.sink = agent.NewProviderServices(sink)
	ag.threadID = "thread-1"
	ag.turnID = "turn-1"
	err := ag.SendInput("later turn", nil)
	assert.Empty(t, requests(), "a refused send must reach no RPC")
	agenttest.AssertBusyRefusalRepublishesTheTurn(t, sink, ag, err)
}
