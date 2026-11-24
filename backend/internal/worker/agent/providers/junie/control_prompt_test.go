package junie

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// Junie adds no control request of its own: the embedded Agent Client
// Protocol base reads and answers every one, its plan review included. These
// tests prove it through Junie's registration, with the shapes that Junie
// sends.

const junieTestRequestID = "jsonrpc:7"

// juniePlanReview is Junie's plan review: a permission with Junie's own
// options, whose reject option is `revise`.
const juniePlanReview = `{"jsonrpc":"2.0","id":7,"method":"session/request_permission","params":{
	"sessionId":"s",
	"toolCall":{"toolCallId":"junie-plan","kind":"other","title":"Implement this plan?"},
	"options":[{"optionId":"implement","name":"Implement","kind":"allow_once"},{"optionId":"revise","name":"Revise","kind":"reject_once"}]}}`

// Junie's plan review is a permission with Junie's own options, not a plan:
// the plan answers are Junie's options, and the base selects them.
func TestReadControlRequest_JuniePlanReviewIsAPermissionWithJuniesOptions(t *testing.T) {
	t.Parallel()
	got := agenttest.ControlPromptOf(Registration().Plugin, []byte(juniePlanReview))
	assert.Nil(t, got.GetPlan())
	permission := got.GetPermission()
	require.NotNil(t, permission)
	assert.Equal(t, "Implement this plan?", permission.GetTitle())
	require.Len(t, permission.GetOptions(), 2)
	assert.Equal(t, "implement", permission.GetOptions()[0].GetOptionId())
	assert.Equal(t, "Implement", permission.GetOptions()[0].GetName())
	assert.Equal(t, "revise", permission.GetOptions()[1].GetOptionId())
	assert.Equal(t, leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, permission.GetOptions()[1].GetKind())
}

// Junie names each option of a shell approval after its own id, and the base
// keeps that name. A shell call states its command.
func TestReadControlRequest_JunieReadsAShellPermissionThroughTheBase(t *testing.T) {
	t.Parallel()
	permission := agenttest.ControlPromptOf(Registration().Plugin, []byte(`{"jsonrpc":"2.0","id":7,"method":"session/request_permission","params":{
		"toolCall":{"toolCallId":"call-1","kind":"execute","title":"Run","rawInput":{"command":"echo junie"}},
		"options":[{"optionId":"yes","name":"yes","kind":"allow_once"},{"optionId":"no","name":"no","kind":"reject_once"}]}}`)).GetPermission()
	require.NotNil(t, permission)
	assert.Equal(t, "echo junie", permission.GetCommand())
	require.Len(t, permission.GetOptions(), 2)
	assert.Equal(t, "yes", permission.GetOptions()[0].GetName())
	assert.Equal(t, leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, permission.GetOptions()[0].GetKind())
}

// The plan review's answers are Junie's options: approving selects the
// allow option, and revising selects `revise`, with the revision text after
// it as a message, because Junie states no field for it.
func TestAnswerControlRequest_JuniePlanReviewSelectsJuniesOption(t *testing.T) {
	t.Parallel()
	got, err := agenttest.AnswerControl(Registration().Plugin, []byte(juniePlanReview), junieTestRequestID, agenttest.PermissionDecision("implement", ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:7","result":{"outcome":{"outcome":"selected","optionId":"implement"}}}`, string(got.Content))
	assert.Nil(t, got.PlanApproval, "a permission carries no plan settings")

	got, err = agenttest.AnswerControl(Registration().Plugin, []byte(juniePlanReview), junieTestRequestID, agenttest.PermissionDecision("revise", "Split the second step."))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:7","result":{"outcome":{"outcome":"selected","optionId":"revise"}}}`, string(got.Content))
	assert.Equal(t, "Split the second step.", got.FollowUp)
	resolved := agenttest.ResolveAnswer(Registration().Plugin, []byte(juniePlanReview), junieTestRequestID, got)
	assert.False(t, resolved.Withhold)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":7,"result":{"outcome":{"outcome":"selected","optionId":"revise"}}}`, string(resolved.Content))
	assert.Equal(t, agent.ControlFollowUp{}, resolved.FollowUp, "the Worker queues the answer's follow-up, not a second one")

	_, err = agenttest.AnswerControl(Registration().Plugin, []byte(juniePlanReview), junieTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a permission takes a permission decision")
	_, err = agenttest.AnswerControl(Registration().Plugin, []byte(juniePlanReview), junieTestRequestID, agenttest.PermissionDecision("implement", "and add tests"))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "an approval carries no feedback")
}

func TestControlRequest_JunieAnswersAnElicitationThroughTheBase(t *testing.T) {
	t.Parallel()
	payload := `{"jsonrpc":"2.0","id":7,"method":"elicitation/create","params":{"mode":"form","message":"Pick a branch.","server":"git","requestedSchema":{"type":"object","properties":{"branch":{"type":"string"}}}}}`
	elicitation := agenttest.ControlPromptOf(Registration().Plugin, []byte(payload)).GetElicitation()
	require.NotNil(t, elicitation)
	assert.Equal(t, "Pick a branch.", elicitation.GetMessage())
	assert.Equal(t, "git", elicitation.GetServer())
	assert.JSONEq(t, `{"type":"object","properties":{"branch":{"type":"string"}}}`, elicitation.GetSchemaJson())

	got, err := agenttest.AnswerControl(Registration().Plugin, []byte(payload), junieTestRequestID, agenttest.ElicitationDecision(leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT, `{"branch":"main"}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"jsonrpc:7","response":{"action":"accept","content":{"branch":"main"}}}}`, string(got.Content))
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":7,"result":{"action":"accept","content":{"branch":"main"}}}`, string(agenttest.ResolveAnswer(Registration().Plugin, []byte(payload), junieTestRequestID, got).Content))
}
