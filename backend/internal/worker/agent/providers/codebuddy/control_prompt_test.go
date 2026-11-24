package codebuddy

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// codebuddyTestRequestID is the stored id of the request that each test answers.
const codebuddyTestRequestID = "approval:ap1"

// codebuddyToolPayload is a stored can_use_tool request in the shape that the
// Worker publishes it.
func codebuddyToolPayload(t *testing.T, toolName string, input any) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"type":       "control_request",
		"request_id": "approval:ap1",
		"request":    map[string]any{"subtype": "can_use_tool", "tool_name": toolName, "tool_use_id": "call-1", "input": input},
	})
	require.NoError(t, err)
	return payload
}

func TestCodebuddyReadControlRequest_AskUserQuestionReadsEachQuestionAndItsOptions(t *testing.T) {
	got := agenttest.ControlPromptOf(codebuddyProvider{}, codebuddyToolPayload(t, "AskUserQuestion", map[string]any{"questions": []any{
		map[string]any{"question": "Which one?", "header": "Pick", "multiSelect": true, "options": []any{
			map[string]any{"label": "A", "description": "first"},
		}},
	}}))
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
		Questions: []*leapmuxv1.ControlQuestion{{
			Question: "Which one?", Header: "Pick", MultiSelect: true,
			Options: []*leapmuxv1.QuestionOption{{Label: "A", Description: "first"}},
		}},
	}}}, got)
}

// The field is a boolean on the wire. A model that writes anything else must
// not open the multiple-choice control.
func TestCodebuddyReadControlRequest_OpensTheMultipleChoiceControlOnlyForARealTrue(t *testing.T) {
	got := agenttest.ControlPromptOf(codebuddyProvider{}, codebuddyToolPayload(t, "AskUserQuestion", map[string]any{"questions": []any{
		map[string]any{"question": "One?", "options": []any{}, "multiSelect": false},
		map[string]any{"question": "Two?", "options": []any{}, "multiSelect": "yes"},
		map[string]any{"question": "Three?", "options": []any{}},
	}}))
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
		Questions: []*leapmuxv1.ControlQuestion{{Question: "One?"}, {Question: "Two?"}, {Question: "Three?"}},
	}}}, got)
}

// A null or a bare string among the questions cannot be drawn, so it is
// dropped.
func TestCodebuddyReadControlRequest_DropsAQuestionThatCannotBeDrawn(t *testing.T) {
	got := agenttest.ControlPromptOf(codebuddyProvider{}, codebuddyToolPayload(t, "AskUserQuestion", map[string]any{"questions": []any{
		nil, "plain text", 7, map[string]any{"question": "Which one?", "options": []any{}},
	}}))
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
		Questions: []*leapmuxv1.ControlQuestion{{Question: "Which one?"}},
	}}}, got)
}

func TestCodebuddyReadControlRequest_AQuestionCallThatStatesNoListAsksNoQuestion(t *testing.T) {
	for _, input := range []any{map[string]any{}, map[string]any{"questions": "nope"}, nil} {
		got := agenttest.ControlPromptOf(codebuddyProvider{}, codebuddyToolPayload(t, "AskUserQuestion", input))
		require.NotNil(t, got.GetQuestion(), "%v", input)
		assert.Empty(t, got.GetQuestion().GetQuestions(), "%v", input)
	}
}

// Only the question tool asks a question. Any other request, a payload with
// no tool included, is a permission.
func TestCodebuddyReadControlRequest_OnlyTheQuestionToolIsAQuestion(t *testing.T) {
	assert.NotNil(t, agenttest.ControlPromptOf(codebuddyProvider{}, codebuddyToolPayload(t, "Bash", map[string]any{})).GetPermission())
	got := agenttest.ControlPromptOf(codebuddyProvider{}, []byte(`{}`))
	require.NotNil(t, got.GetPermission())
	assert.Empty(t, got.GetPermission().GetTitle())
	assert.Equal(t, "{}", got.GetPermission().GetInputJson())
}

// CodeBuddy's request states no options, so the permission offers LeapMux's
// allow and deny, which the shared Allow and Deny buttons answer.
func TestCodebuddyReadControlRequest_AShellCallIsAPermissionWithItsCommand(t *testing.T) {
	got := agenttest.ControlPromptOf(codebuddyProvider{}, codebuddyToolPayload(t, "Bash", map[string]any{"command": "ls"}))
	permission := got.GetPermission()
	require.NotNil(t, permission)
	assert.Equal(t, "Bash", permission.GetTitle())
	assert.JSONEq(t, `{"command":"ls"}`, permission.GetInputJson())
	assert.Equal(t, "ls", permission.GetCommand())
	require.Len(t, permission.GetOptions(), 2)
	assert.Equal(t, contracts.ControlAnswerOptionAllow, permission.GetOptions()[0].GetOptionId())
	assert.Equal(t, leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, permission.GetOptions()[0].GetKind())
	assert.Equal(t, contracts.ControlAnswerOptionDeny, permission.GetOptions()[1].GetOptionId())
	assert.Equal(t, leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, permission.GetOptions()[1].GetKind())
	assert.Empty(t, permission.GetReason())
	assert.Empty(t, permission.GetText())
	assert.False(t, permission.GetIndependentOptions())

	other := agenttest.ControlPromptOf(codebuddyProvider{}, codebuddyToolPayload(t, "Edit", map[string]any{"command": "not a shell"})).GetPermission()
	assert.Empty(t, other.GetCommand(), "only a shell call states a command")
}

func TestCodebuddyReadControlRequest_ExitPlanModeIsAPlan(t *testing.T) {
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: &leapmuxv1.PlanPrompt{}}},
		agenttest.ControlPromptOf(codebuddyProvider{}, codebuddyToolPayload(t, "ExitPlanMode", map[string]any{"plan": "Do the thing."})))
}

// The Allow button, and the composer's send with no text, allow with the
// tool's own input.
func TestCodebuddyAnswerControlRequest_TheAllowSendsTheToolInputBack(t *testing.T) {
	got, err := agenttest.AnswerControl(codebuddyProvider{}, codebuddyToolPayload(t, "Bash", map[string]any{"command": "ls"}), codebuddyTestRequestID, agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"approval:ap1","response":{"behavior":"allow","updatedInput":{"command":"ls"}}}}`, string(got.Content))
	assert.Nil(t, got.PlanApproval)
	assert.Empty(t, got.FollowUp)
}

func TestCodebuddyAnswerControlRequest_ARefusalDeniesWithItsFeedback(t *testing.T) {
	payload := codebuddyToolPayload(t, "Bash", map[string]any{"command": "ls"})
	got, err := agenttest.AnswerControl(codebuddyProvider{}, payload, codebuddyTestRequestID, agenttest.PermissionDecision(contracts.ControlAnswerOptionDeny, "Use a dry run."))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"approval:ap1","response":{"behavior":"deny","message":"Use a dry run."}}}`, string(got.Content))
	assert.Empty(t, got.FollowUp, "the deny carries the feedback itself")

	got, err = agenttest.AnswerControl(codebuddyProvider{}, payload, codebuddyTestRequestID, agenttest.PermissionDecision(contracts.ControlAnswerOptionDeny, ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"approval:ap1","response":{"behavior":"deny","message":"`+contracts.ControlAnswerMessageRejectedByUser+`"}}}`, string(got.Content),
		"the Deny button states the placeholder, never an empty message")
}

func TestCodebuddyAnswerControlRequest_RefusesADecisionThatDoesNotAnswerThePrompt(t *testing.T) {
	payload := codebuddyToolPayload(t, "Bash", map[string]any{"command": "ls"})
	for name, decision := range map[string]*leapmuxv1.ControlDecision{
		"feedback beside the allow": agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, "Why not."),
		"an option not offered":     agenttest.PermissionDecision("session", ""),
		"a plan decision":           agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true}),
		"no kind":                   {},
	} {
		_, err := agenttest.AnswerControl(codebuddyProvider{}, payload, codebuddyTestRequestID, decision)
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}
}

// The composer's text on a plan always refuses it: the Approve button owns the
// allow path.
func TestCodebuddyAnswerControlRequest_APlanRefusalDeniesWithItsFeedback(t *testing.T) {
	payload := codebuddyToolPayload(t, "ExitPlanMode", map[string]any{"plan": "Do the thing."})
	got, err := agenttest.AnswerControl(codebuddyProvider{}, payload, codebuddyTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Feedback: "Revise step 2."}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"approval:ap1","response":{"behavior":"deny","message":"Revise step 2."}}}`, string(got.Content))
	assert.Nil(t, got.PlanApproval, "a refusal applies no settings")

	got, err = agenttest.AnswerControl(codebuddyProvider{}, payload, codebuddyTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"approval:ap1","response":{"behavior":"deny","message":"`+contracts.ControlAnswerMessageRejectedByUser+`"}}}`, string(got.Content))
}

func TestCodebuddyAnswerControlRequest_APlanApprovalAllowsWithTheToolInputAndTheSettings(t *testing.T) {
	payload := codebuddyToolPayload(t, "ExitPlanMode", map[string]any{"plan": "Do the thing."})
	got, err := agenttest.AnswerControl(codebuddyProvider{}, payload, codebuddyTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, PermissionMode: "acceptEdits", ClearContext: true}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"approval:ap1","response":{"behavior":"allow","updatedInput":{"plan":"Do the thing."}}}}`, string(got.Content))
	assert.True(t, proto.Equal(&leapmuxv1.PlanApprovalSettings{PermissionMode: "acceptEdits", ClearContext: true}, got.PlanApproval))

	_, err = agenttest.AnswerControl(codebuddyProvider{}, payload, codebuddyTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, ChoiceId: "revise"}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "CodeBuddy's plan offers no choices")
}

var codebuddyColorQuestions = []any{
	map[string]any{"question": "Color?", "header": "Color", "options": []any{
		map[string]any{"label": "Red", "description": "Warm"},
		map[string]any{"label": "Blue", "description": "Cool"},
	}},
}

// The reply is the whole tool input with `answers` added, keyed by the
// question text: the Worker folds that object into CodeBuddy's
// `updatedInput`.
func TestCodebuddyAnswerControlRequest_AQuestionAnswerFoldsTheAnswersIntoTheToolInput(t *testing.T) {
	payload := codebuddyToolPayload(t, "AskUserQuestion", map[string]any{"questions": codebuddyColorQuestions})
	got, err := agenttest.AnswerControl(codebuddyProvider{}, payload, codebuddyTestRequestID, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"Red"}}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"approval:ap1","response":{"behavior":"allow","updatedInput":{
		"questions":[{"question":"Color?","header":"Color","options":[{"label":"Red","description":"Warm"},{"label":"Blue","description":"Cool"}]}],
		"answers":{"Color?":"Red"}
	}}}}`, string(got.Content))

	_, err = agenttest.AnswerControl(codebuddyProvider{}, payload, codebuddyTestRequestID, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"Green"}}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "the question offers no Green")
}

// A multiple-choice answer states each picked option and the typed words, as
// one more value: the user chose the options AND the words.
func TestCodebuddyAnswerControlRequest_AMultipleChoiceAnswerSendsEveryPickAndTheTypedWords(t *testing.T) {
	payload := codebuddyToolPayload(t, "AskUserQuestion", map[string]any{"questions": []any{
		map[string]any{"question": "Toppings?", "multiSelect": true, "options": []any{
			map[string]any{"label": "Cheese"}, map[string]any{"label": "Olives"},
		}},
	}})
	got, err := agenttest.AnswerControl(codebuddyProvider{}, payload, codebuddyTestRequestID, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"Cheese", "Olives"}, CustomText: " Basil "}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"approval:ap1","response":{"behavior":"allow","updatedInput":{
		"questions":[{"question":"Toppings?","multiSelect":true,"options":[{"label":"Cheese"},{"label":"Olives"}]}],
		"answers":{"Toppings?":"Cheese, Olives, Basil"}
	}}}}`, string(got.Content))
}

func TestCodebuddyAnswerControlRequest_ACancelledQuestionDeniesWithTheFeedback(t *testing.T) {
	payload := codebuddyToolPayload(t, "AskUserQuestion", map[string]any{"questions": codebuddyColorQuestions})
	got, err := agenttest.AnswerControl(codebuddyProvider{}, payload, codebuddyTestRequestID, agenttest.CancelQuestions("Ask again tomorrow."))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"approval:ap1","response":{"behavior":"deny","message":"Ask again tomorrow."}}}`, string(got.Content))
}

// The answer is the neutral envelope that ResolveControlResponse reads today,
// so it turns into CodeBuddy's own `allowed` answer.
func TestCodebuddyAnswerControlRequest_ResolvesIntoCodeBuddysOwnAnswer(t *testing.T) {
	resolve := func(payload []byte, answer agent.ControlAnswer) map[string]any {
		t.Helper()
		resolution := agenttest.ResolveAnswer(codebuddyProvider{}, payload, codebuddyTestRequestID, answer)
		require.False(t, resolution.Withhold)
		return nativeAnswer(t, resolution.Content)
	}

	payload := codebuddyToolPayload(t, "Bash", map[string]any{"command": "ls"})
	allow, err := agenttest.AnswerControl(codebuddyProvider{}, payload, codebuddyTestRequestID, agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""))
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"allowed": true, "updatedInput": map[string]any{"command": "ls"}}, resolve(payload, allow))

	deny, err := agenttest.AnswerControl(codebuddyProvider{}, payload, codebuddyTestRequestID, agenttest.PermissionDecision(contracts.ControlAnswerOptionDeny, "Use a dry run."))
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"allowed": false, "reason": "Use a dry run."}, resolve(payload, deny))

	question := codebuddyToolPayload(t, "AskUserQuestion", map[string]any{"questions": codebuddyColorQuestions})
	answer, err := agenttest.AnswerControl(codebuddyProvider{}, question, codebuddyTestRequestID, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{CustomText: "Teal"}))
	require.NoError(t, err)
	native := resolve(question, answer)
	assert.Equal(t, true, native["allowed"])
	updated, ok := native["updatedInput"].(map[string]any)
	require.True(t, ok, "an answered question allows with the updated input")
	assert.Equal(t, map[string]any{"Color?": "Teal"}, updated["answers"])
}
