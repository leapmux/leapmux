package kilo

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A steer is words the reader typed and watched for. A daemon that declines a
// concurrent prompt -- which the protocol does not oblige it to accept -- otherwise
// swallowed them with nothing anywhere the reader could see.
func TestOpenCodeFamilySteerFailureReachesTheReader(t *testing.T) {
	t.Parallel()

	ag, _ := acptest.NewAgentForRPCWithResponder(t,
		func() *Agent { return &Agent{} },
		func(agent *Agent) *acp.Base { return &agent.Base },
		func(method string) agenttest.RPCReply {
			if method != acp.MethodSessionPrompt {
				return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
			}
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32600,"message":"a turn is already running"}`)}
		},
	)
	sink := &agenttest.ControlSink{}
	ag.SetSinkForTest(agent.NewProviderServices(sink))
	ag.SetPromptActiveForTest(true)

	require.NoError(t, ag.SteerInput("guide the turn", nil))
	require.Eventually(t, func() bool { return len(sink.Notifications()) == 1 }, time.Second, time.Millisecond)
	notice := sink.Notifications()[0]
	assert.Equal(t, contracts.NotificationTypeAgentError, notice["type"])
	assert.Contains(t, notice["error"], "a turn is already running")
}

// Kilo steers through the same second session/prompt on the same daemon. The
// answer lives on the family base, so a fork cannot lose the capability by not
// restating it: Kilo's Steer control was dead while this answer sat one type
// lower.
func TestKiloSteersThroughTheFamily(t *testing.T) {
	t.Parallel()
	_, steers := any(&Agent{}).(agent.InputSteerer)
	assert.True(t, steers, "Kilo steers through the family's second session/prompt")
	assert.True(t, (&Agent{}).SupportsSteering(),
		"every OpenCode-family provider steers with a second session/prompt")
}
