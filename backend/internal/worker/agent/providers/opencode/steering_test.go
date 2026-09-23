package opencode

import (
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenCodeSteerUsesConcurrentACPPrompt(t *testing.T) {
	t.Parallel()

	agent, requests := acptest.NewAgentForRPC(t,
		func() *Agent { return &Agent{} },
		func(agent *Agent) *acp.Base { return &agent.Base },
	)
	agent.SetPromptActiveForTest(true)
	require.NoError(t, agent.SteerInput("guide the turn", nil))
	require.Eventually(t, func() bool { return len(requests()) == 1 }, time.Second, time.Millisecond)
	assert.Equal(t, acp.MethodSessionPrompt, requests()[0].Method)
	assert.Equal(t, "session-1", requests()[0].Params["sessionId"])
}

// OpenCode steers with a second session/prompt, so it steers although it
// advertises no steer method.
func TestOpenCodeSupportsSteeringWithoutAnAdvertisedMethod(t *testing.T) {
	t.Parallel()
	assert.True(t, (&Agent{}).SupportsSteering())
}

func TestOpenCodeSendInputDuringActiveTurnReportsAgentBusy(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	ag, requests := acptest.NewAgentForRPC(t,
		func() *Agent { return &Agent{} },
		func(agent *Agent) *acp.Base { return &agent.Base },
	)
	ag.SetSinkForTest(agent.NewProviderServices(sink))
	ag.WireTurnActiveForTest()
	ag.SetPromptActiveForTest(true)
	err := ag.SendInput("later turn", nil)
	assert.Empty(t, requests(), "a refused send must reach no RPC")
	agenttest.AssertBusyRefusalRepublishesTheTurn(t, sink, ag, err)
}
