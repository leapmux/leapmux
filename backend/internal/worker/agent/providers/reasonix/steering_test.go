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

// The disposition of Reasonix's steer reply decides what the queue hears.
// steer_accepted and queued_followup both deliver: Reasonix runs a
// queued_followup as the follow-up turn when the active one ends. Every
// rejected_* word answers ErrNoActiveTurn, so the queue keeps the input for
// the next turn.
func TestReasonixSteerDisposition(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		disposition string
		wantErr     error
	}{
		{"steer_accepted", nil},
		{"queued_followup", nil},
		{"rejected_busy", agent.ErrNoActiveTurn},
		{"rejected_rotating", agent.ErrNoActiveTurn},
		{"rejected_closed", agent.ErrNoActiveTurn},
		{"rejected_capacity", agent.ErrNoActiveTurn},
	} {
		t.Run(tc.disposition, func(t *testing.T) {
			t.Parallel()
			disposition := tc.disposition
			a, requests := acptest.NewAgentForRPCWithResponder(t,
				func() *Agent { return &Agent{} },
				func(a *Agent) *acp.Base { return &a.Base },
				func(method string) agenttest.RPCReply {
					if method == reasonixSteerMethod {
						return agenttest.RPCReply{Result: json.RawMessage(`{"disposition":"` + disposition + `"}`)}
					}
					return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
				})
			a.SetSinkForTest(agent.NewProviderServices(&agenttest.Sink{}))
			a.SetSessionIDForTest("reasonix-session")
			a.SetSteerMethodForTest(reasonixSteerMethod)
			a.SetPromptActiveForTest(true)
			err := a.SteerInput("more context", nil)
			assert.Equal(t, tc.wantErr, err)
			require.NotEmpty(t, requests(), "the steer request left the agent")
		})
	}
}
