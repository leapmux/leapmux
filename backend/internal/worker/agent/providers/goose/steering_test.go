package goose

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGooseSessionUpdateTracksActiveRunForSteering(t *testing.T) {
	t.Parallel()

	agent := &Agent{}
	agent.captureSteerRunID("session_info_update", map[string]json.RawMessage{"goose": json.RawMessage(`{"activeRunId":"run-7"}`)}, nil)
	assert.Equal(t, "run-7", agent.SteerRunIDForTest())
	agent.captureSteerRunID("session_info_update", map[string]json.RawMessage{"goose": json.RawMessage(`{"activeRunId":null}`)}, nil)
	assert.Empty(t, agent.SteerRunIDForTest())
}

// Goose ACKNOWLEDGES a steer on an update of its own, in the shape a live `goose acp`
// probe recorded: `{"messageId":"steer_...","runId":"run_..."}`, with no `activeRunId`
// beside it. The update must be CLAIMED -- an unclaimed one falls through and is
// persisted as a row the browser then hides -- and it must leave the tracked run
// alone, because the run it identifies is the one the steer already targets.
func TestGooseQueuedSteerIsClaimedAndKeepsTheActiveRun(t *testing.T) {
	t.Parallel()

	agent := &Agent{}
	agent.captureSteerRunID("session_info_update", map[string]json.RawMessage{"goose": json.RawMessage(`{"activeRunId":"run-7"}`)}, nil)

	claimed := agent.captureSteerRunID("session_info_update", map[string]json.RawMessage{
		"goose": json.RawMessage(`{"queuedSteer":{"messageId":"steer_1","runId":"run-7"}}`),
	}, nil)
	assert.True(t, claimed, "the acknowledgement is read here, not persisted as a row")
	assert.Equal(t, "run-7", agent.SteerRunIDForTest(), "a queued steer does not end the run it belongs to")
}

// An update whose `_meta.goose` holds neither field is NOT this handler's, so it must
// fall through to the dispatcher rather than being swallowed.
func TestGooseSessionUpdateLeavesAnUnrelatedMetaAlone(t *testing.T) {
	t.Parallel()

	agent := &Agent{}
	claimed := agent.captureSteerRunID("session_info_update", map[string]json.RawMessage{
		"goose": json.RawMessage(`{"messageCount":3,"userSetName":"a session"}`),
	}, nil)
	assert.False(t, claimed)
}

// Goose steers only through the method that its handshake advertises, so an agent
// with no advertised method does not claim the capability.
func TestGooseSupportsSteeringOnlyThroughAnAdvertisedMethod(t *testing.T) {
	t.Parallel()
	assert.False(t, (&Agent{}).SupportsSteering())
}

func TestGooseAdvertisedSteeringCapability(t *testing.T) {
	t.Parallel()

	goose, requests := acptest.NewAgentForRPC(t,
		func() *Agent { return &Agent{} },
		func(agent *Agent) *acp.Base { return &agent.Base },
	)
	assert.False(t, goose.SupportsSteering())
	goose.SetSteerMethodForTest("_goose/unstable/session/steer")
	goose.SetPromptActiveForTest(true)
	goose.SetSteerRunIDForTest("run-1")
	assert.True(t, goose.SupportsSteering())
	require.NoError(t, goose.SteerInput("guide", nil))
	require.Len(t, requests(), 1)
	assert.Equal(t, "_goose/unstable/session/steer", requests()[0].Method)
	assert.Equal(t, "run-1", requests()[0].Params["expectedRunId"])
}

func TestGooseAdvertisedSteerMethodDetection(t *testing.T) {
	t.Parallel()

	assert.Equal(t, gooseSteerMethod, acp.ParseAdvertisedMethod([]byte(`{"agentCapabilities":{"_meta":{"goose":{"sessionSteer":{"method":"_goose/unstable/session/steer"}}}}}`), gooseSteerNamespace, gooseSteerMethod))
	assert.Empty(t, acp.ParseAdvertisedMethod([]byte(`{"agentCapabilities":{"_meta":{"goose":{}}}}`), gooseSteerNamespace, gooseSteerMethod))
	assert.Empty(t, acp.ParseAdvertisedMethod([]byte(`{"description":"_goose/unstable/session/steer"}`), gooseSteerNamespace, gooseSteerMethod))
}

// A steer that reaches a turn that already ended maps to ErrNoActiveTurn.
func TestGooseAdvertisedSteerMapsEndedTurnResponse(t *testing.T) {
	t.Parallel()

	goose, _ := acptest.NewAgentForRPCWithResponder(t,
		func() *Agent { return &Agent{} },
		func(agent *Agent) *acp.Base { return &agent.Base },
		func(string) agenttest.RPCReply {
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32602,"message":"session has no active prompt"}`)}
		},
	)
	goose.SetSteerMethodForTest("_goose/unstable/session/steer")
	goose.SetPromptActiveForTest(true)
	goose.SetSteerRunIDForTest("run-1")
	assert.ErrorIs(t, goose.SteerInput("guide", nil), agent.ErrNoActiveTurn)
}
