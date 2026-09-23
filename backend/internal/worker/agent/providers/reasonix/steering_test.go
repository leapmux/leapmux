package reasonix

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdvertisedACPSteerTimeoutIsDeliveryUncertain(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	defer close(release)
	reasonix, _ := acptest.NewAgentForRPCWithResponder(t,
		func() *Agent { return &Agent{} },
		func(agent *Agent) *acp.Base { return &agent.Base },
		func(string) agenttest.RPCReply {
			<-release
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		},
	)
	reasonix.SetSteerMethodForTest("_reasonix.io/session/steer")
	reasonix.SetPromptActiveForTest(true)
	reasonix.SetAPITimeoutForTest(10 * time.Millisecond)

	assert.ErrorIs(t, reasonix.SteerInput("guide", nil), agent.ErrDeliveryUncertain)
}

func TestReasonixAdvertisedSteeringCapability(t *testing.T) {
	t.Parallel()

	reasonix, requests := acptest.NewAgentForRPC(t,
		func() *Agent { return &Agent{} },
		func(agent *Agent) *acp.Base { return &agent.Base },
	)
	assert.False(t, reasonix.SupportsSteering())
	reasonix.SetSteerMethodForTest("_reasonix.io/session/steer")
	reasonix.SetPromptActiveForTest(true)
	require.NoError(t, reasonix.SteerInput("guide", nil))
	require.Len(t, requests(), 1)
	assert.Equal(t, "_reasonix.io/session/steer", requests()[0].Method)
}

func TestReasonixAdvertisedSteerMethodDetection(t *testing.T) {
	t.Parallel()

	assert.Equal(t, reasonixSteerMethod, acp.ParseAdvertisedMethod([]byte(`{"agentCapabilities":{"_meta":{"reasonix.io":{"sessionSteer":{"method":"_reasonix.io/session/steer"}}}}}`), reasonixSteerNamespace, reasonixSteerMethod))
	assert.Empty(t, acp.ParseAdvertisedMethod([]byte(`{"agentCapabilities":{"_meta":{"goose":{"sessionSteer":{"method":"_goose/unstable/session/steer"}}}}}`), reasonixSteerNamespace, reasonixSteerMethod),
		"Goose's capability is not Reasonix's")
}

// A steer that reaches a turn that already ended maps to ErrNoActiveTurn.
func TestReasonixAdvertisedSteerMapsEndedTurnResponse(t *testing.T) {
	t.Parallel()

	reasonix, _ := acptest.NewAgentForRPCWithResponder(t,
		func() *Agent { return &Agent{} },
		func(agent *Agent) *acp.Base { return &agent.Base },
		func(string) agenttest.RPCReply {
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32602,"message":"session has no active prompt"}`)}
		},
	)
	reasonix.SetSteerMethodForTest("_reasonix.io/session/steer")
	reasonix.SetPromptActiveForTest(true)
	assert.ErrorIs(t, reasonix.SteerInput("guide", nil), agent.ErrNoActiveTurn)
}
