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

func TestZCodeSendInputSendsPlainSessionSend(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	a.Mu.Lock()
	a.sessionID = "session-1"
	a.model = "provider/model"
	a.Mu.Unlock()
	answerZCodeRequest(t, a, stdin, MethodSessionSend, `{"accepted":true}`)
	require.NoError(t, a.SendInput("guide", nil))

	requests := stdin.Requests(t)
	require.Len(t, requests, 1)
	var params map[string]any
	require.NoError(t, json.Unmarshal(requests[0].Params, &params))
	assert.Equal(t, "guide", params["content"])
	assert.NotContains(t, params, "requestedDelivery", "the strict session/send schema rejects delivery hints")
}

func TestZCodeSendInputTimeoutIsDeliveryUncertain(t *testing.T) {
	t.Parallel()

	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	a.Mu.Lock()
	a.sessionID = "session-1"
	a.model = "provider/model"
	a.SetAPITimeoutForTest(10 * time.Millisecond)
	a.Mu.Unlock()

	assert.ErrorIs(t, a.SendInput("guide", nil), agent.ErrDeliveryUncertain)
}

func TestZCodeSendInputDuringActiveTurnReportsAgentBusy(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), &zcodeRecordedStdin{})
	a.Mu.Lock()
	a.sessionID = "session-1"
	a.model = "provider/model"
	a.turnActive = true
	a.Mu.Unlock()
	agenttest.AssertBusyRefusalRepublishesTheTurn(t, sink, a, a.SendInput("later turn", nil))
}
