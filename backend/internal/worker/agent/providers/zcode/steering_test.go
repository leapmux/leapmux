package zcode

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestZCodeSteerSendsPlainSessionSend(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	agent := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	agent.Mu.Lock()
	agent.sessionID = "session-1"
	agent.model = "provider/model"
	agent.turnActive = true
	agent.Mu.Unlock()
	answerZCodeRequest(t, agent, stdin, MethodSessionSend, `{"accepted":true}`)
	require.NoError(t, agent.SteerInput("guide", nil))

	requests := stdin.Requests(t)
	require.Len(t, requests, 1)
	var params map[string]any
	require.NoError(t, json.Unmarshal(requests[0].Params, &params))
	assert.Equal(t, "guide", params["content"])
	// The app-server's session/send schema is strict: any key it does not know
	// fails the whole request with -32602. A steer must therefore be a plain
	// send -- the server itself decides steer-or-queue for a mid-turn send.
	assert.NotContains(t, params, "requestedDelivery")
}

func TestZCodeSteerRefusedWithoutActiveTurn(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	a.Mu.Lock()
	a.sessionID = "session-1"
	a.model = "provider/model"
	a.turnActive = false
	a.Mu.Unlock()

	assert.ErrorIs(t, a.SteerInput("guide", nil), agent.ErrNoActiveTurn)
	assert.Empty(t, stdin.Requests(t))
}

func TestZCodeSteerTimeoutIsDeliveryUncertain(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	a.Mu.Lock()
	a.sessionID = "session-1"
	a.model = "provider/model"
	a.turnActive = true
	a.SetAPITimeoutForTest(10 * time.Millisecond)
	a.Mu.Unlock()

	assert.ErrorIs(t, a.SteerInput("guide", nil), agent.ErrDeliveryUncertain)
}

func TestZCodeSendInputDuringActiveTurnReportsAgentBusy(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), &zcodeRecordedStdin{})
	agent.Mu.Lock()
	agent.sessionID = "session-1"
	agent.model = "provider/model"
	agent.turnActive = true
	agent.Mu.Unlock()
	agenttest.AssertBusyRefusalRepublishesTheTurn(t, sink, agent, agent.SendInput("later turn", nil))
}
