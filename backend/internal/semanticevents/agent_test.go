package semanticevents

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func TestAgentFactKeepsZeroToolCallsAndSkipsCatchUp(t *testing.T) {
	t.Parallel()
	var calls int32
	event := &leapmuxv1.AgentEvent{Event: &leapmuxv1.AgentEvent_ActivityChanged{
		ActivityChanged: &leapmuxv1.AgentActivityChanged{State: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE, NumToolUses: &calls},
	}}
	activity := AgentFact(event, nil).GetAgentActivity()
	require.NotNil(t, activity)
	require.NotNil(t, activity.ToolCalls)
	assert.Zero(t, activity.GetToolCalls())
	calls = 5
	assert.Zero(t, activity.GetToolCalls())
	event.Replay = &leapmuxv1.ReplayOrigin{ReplayId: 1}
	assert.Nil(t, AgentFact(event, nil))
}

func TestAgentFactProjectsRequestDetailsWithoutStoringTheClaimOrPayload(t *testing.T) {
	t.Parallel()
	request := &leapmuxv1.AgentControlRequest{
		RequestId: "provider request", ClaimToken: "secret live claim", Payload: []byte("raw provider request"),
		ResponseState: leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_READY,
		Prompt: &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Unreadable{
			Unreadable: leapmuxv1.ControlPayloadFault_CONTROL_PAYLOAD_FAULT_MALFORMED,
		}},
	}
	event := &leapmuxv1.AgentEvent{Event: &leapmuxv1.AgentEvent_ControlRequest{ControlRequest: request}}
	derive := func(claim string) []byte {
		assert.Equal(t, request.ClaimToken, claim)
		return []byte("derived identity")
	}
	fact := AgentFact(event, derive)
	record := fact.GetAgentRequest()
	require.NotNil(t, record)
	assert.Equal(t, request.RequestId, record.GetRequestId())
	assert.Equal(t, leapmuxv1.SemanticControlPromptKind_SEMANTIC_CONTROL_PROMPT_KIND_UNREADABLE, record.GetPromptKind())
	assert.Equal(t, "LeapMux cannot read it (malformed)", record.GetDetail())
	assert.True(t, record.GetBlocking())
	raw, err := proto.Marshal(fact)
	require.NoError(t, err)
	assert.False(t, bytes.Contains(raw, request.GetPayload()))
	assert.False(t, bytes.Contains(raw, []byte(request.GetClaimToken())))
	assert.Contains(t, string(raw), "derived identity")

	request.ResponseState = leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_PENDING
	assert.Nil(t, AgentFact(event, derive))
	ended := AgentFact(&leapmuxv1.AgentEvent{Event: &leapmuxv1.AgentEvent_ControlCancel{
		ControlCancel: &leapmuxv1.AgentControlCancelRequest{
			RequestId: request.RequestId, ClaimToken: request.ClaimToken,
			ResponseState: leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED,
		},
	}}, derive).GetAgentRequestEnded()
	require.NotNil(t, ended)
	assert.Equal(t, record.GetRequestInstance(), ended.GetRequestInstance())
	assert.Equal(t, leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED, ended.GetState())
}
