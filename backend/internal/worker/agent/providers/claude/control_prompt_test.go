package claude

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

// claudeTestRequestID is the stored id of the request that each test answers.
const claudeTestRequestID = "req-1"

func controlPayload(t *testing.T, toolName string, input any) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"type":       "control_request",
		"request_id": "req-1",
		"request":    map[string]any{"subtype": "can_use_tool", "tool_name": toolName, "input": input},
	})
	require.NoError(t, err)
	return payload
}

func TestReadControlRequest_AskUserQuestionReadsEachQuestionAndItsOptions(t *testing.T) {
	got := agenttest.ControlPromptOf(claudeProvider{}, controlPayload(t, ToolNameAskUserQuestion, map[string]any{"questions": []any{
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

// A null or a bare string among the questions cannot be drawn, so it is
// dropped, and a payload that states no list reads as no question.
func TestReadControlRequest_AskUserQuestionDropsAnElementThatCannotBeDrawn(t *testing.T) {
	got := agenttest.ControlPromptOf(claudeProvider{}, controlPayload(t, ToolNameAskUserQuestion, map[string]any{"questions": []any{
		nil, "plain text", 7, map[string]any{"question": "Which one?", "options": []any{}},
	}}))
	require.Len(t, got.GetQuestion().GetQuestions(), 1)
	assert.Equal(t, "Which one?", got.GetQuestion().GetQuestions()[0].GetQuestion())

	for _, input := range []any{map[string]any{}, map[string]any{"questions": "nope"}} {
		got := agenttest.ControlPromptOf(claudeProvider{}, controlPayload(t, ToolNameAskUserQuestion, input))
		require.NotNil(t, got.GetQuestion())
		assert.Empty(t, got.GetQuestion().GetQuestions())
	}
}

func TestReadControlRequest_ExitPlanModeIsAPlanWithItsPermissions(t *testing.T) {
	got := agenttest.ControlPromptOf(claudeProvider{}, controlPayload(t, ToolNameExitPlanMode, map[string]any{"plan": "1. Edit.", "allowedPrompts": []any{
		map[string]any{"tool": "Bash", "prompt": "run the tests"},
		map[string]any{"tool": " ", "prompt": "blank tool"},
		map[string]any{"tool": "Bash", "prompt": ""},
		"not a pair",
	}}))
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: &leapmuxv1.PlanPrompt{
		Permissions: []*leapmuxv1.PlanPermission{{Tool: "Bash", Prompt: "run the tests"}},
	}}}, got)
}

func TestReadControlRequest_AToolIsAPermissionWithLeapMuxScopes(t *testing.T) {
	got := agenttest.ControlPromptOf(claudeProvider{}, controlPayload(t, "Edit", map[string]any{"file_path": "/w/a.go"}))
	require.NotNil(t, got.GetPermission())
	permission := got.GetPermission()
	assert.Equal(t, "Edit", permission.GetTitle())
	assert.JSONEq(t, `{"file_path":"/w/a.go"}`, permission.GetInputJson())
	assert.Empty(t, permission.GetCommand(), "only a shell call states a command")
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{
		Options: claudePermissionOptions(),
	}}}, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{
		Options: permission.GetOptions(),
	}}})
}

func TestReadControlRequest_AShellCallStatesItsCommand(t *testing.T) {
	for _, tool := range []string{toolNameBash, toolNamePowerShell} {
		got := agenttest.ControlPromptOf(claudeProvider{}, controlPayload(t, tool, map[string]any{"command": "go test ./...", "description": "Run the tests"}))
		assert.Equal(t, "go test ./...", got.GetPermission().GetCommand(), tool)
	}
	got := agenttest.ControlPromptOf(claudeProvider{}, controlPayload(t, toolNameBash, map[string]any{"command": 7}))
	assert.Empty(t, got.GetPermission().GetCommand(), "a command that is no text states none")
	got = agenttest.ControlPromptOf(claudeProvider{}, controlPayload(t, toolNameBash, nil))
	assert.Equal(t, "{}", got.GetPermission().GetInputJson(), "a call with no input states an empty object, which the allow sends back")
}

func TestReadControlRequest_AnMCPElicitationIsAForm(t *testing.T) {
	payload := []byte(`{"type":"control_request","request_id":"req-1","request":{"subtype":"elicitation","message":"Sign in","display_name":"GitHub","mcp_server_name":"github","requested_schema":{"type":"object"},"title":"Login"}}`)
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Elicitation{Elicitation: &leapmuxv1.ElicitationPrompt{
		Mode: "form", Message: "Sign in", Server: "GitHub", SchemaJson: `{"type":"object"}`, Title: "Login",
	}}}, agenttest.ControlPromptOf(claudeProvider{}, payload))

	payload = []byte(`{"request":{"subtype":"elicitation","mode":"url","url":"https://example.com/auth","mcp_server_name":"github"}}`)
	got := agenttest.ControlPromptOf(claudeProvider{}, payload).GetElicitation()
	assert.Equal(t, "url", got.GetMode())
	assert.Equal(t, "github", got.GetServer(), "the server's name stands in for a missing display name")
	assert.Empty(t, got.GetSchemaJson())
}

func TestAnswerControlRequest_APermissionOptionRidesTheNeutralEnvelopeAsItsChoice(t *testing.T) {
	payload := controlPayload(t, "Edit", map[string]any{"file_path": "/w/a.go"})
	for option, behavior := range map[string]string{"once": "allow", "session": "allow", "deny": "deny"} {
		got, err := agenttest.AnswerControl(claudeProvider{}, payload, claudeTestRequestID, agenttest.PermissionDecision(option, ""))
		require.NoError(t, err, option)
		assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"`+behavior+`","choice":"`+option+`"}}}`, string(got.Content), option)
		assert.Nil(t, got.PlanApproval)
		assert.Empty(t, got.FollowUp)
	}
}

func TestAnswerControlRequest_ARefusalWithFeedbackDeniesWithIt(t *testing.T) {
	got, err := agenttest.AnswerControl(claudeProvider{}, controlPayload(t, "Edit", map[string]any{}), claudeTestRequestID, agenttest.PermissionDecision("deny", "Use the other file."))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"deny","message":"Use the other file."}}}`, string(got.Content))
}

func TestAnswerControlRequest_RefusesAnOptionThatThePromptDoesNotOffer(t *testing.T) {
	_, err := agenttest.AnswerControl(claudeProvider{}, controlPayload(t, "Edit", map[string]any{}), claudeTestRequestID, agenttest.PermissionDecision("always", ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
	_, err = agenttest.AnswerControl(claudeProvider{}, controlPayload(t, "Edit", map[string]any{}), claudeTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a permission takes no plan decision")
	_, err = agenttest.AnswerControl(claudeProvider{}, controlPayload(t, "Edit", map[string]any{}), claudeTestRequestID, &leapmuxv1.ControlDecision{})
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a decision of no kind answers nothing")
}

func TestAnswerControlRequest_APlanApprovalAllowsWithItsInputAndSettings(t *testing.T) {
	payload := controlPayload(t, ToolNameExitPlanMode, map[string]any{"plan": "1. Edit."})
	got, err := agenttest.AnswerControl(claudeProvider{}, payload, claudeTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{
		Approve: true, PermissionMode: "acceptEdits", ClearContext: true,
	}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"allow","updatedInput":{"plan":"1. Edit."}}}}`, string(got.Content))
	assert.True(t, proto.Equal(&leapmuxv1.PlanApprovalSettings{PermissionMode: "acceptEdits", ClearContext: true}, got.PlanApproval))

	got, err = agenttest.AnswerControl(claudeProvider{}, payload, claudeTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Feedback: "Split step 1."}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"deny","message":"Split step 1."}}}`, string(got.Content))
	assert.Nil(t, got.PlanApproval, "a refusal applies no settings")

	got, err = agenttest.AnswerControl(claudeProvider{}, payload, claudeTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{}))
	require.NoError(t, err)
	assert.Contains(t, string(got.Content), contracts.ControlAnswerMessageRejectedByUser, "a bare refusal states the placeholder, never an empty message")

	_, err = agenttest.AnswerControl(claudeProvider{}, payload, claudeTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, ChoiceId: "revise"}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "Claude's plan offers no choices")
}

func TestAnswerControlRequest_QuestionAnswersJoinTheToolInput(t *testing.T) {
	payload := controlPayload(t, ToolNameAskUserQuestion, map[string]any{"questions": []any{
		map[string]any{"question": "Which color?", "options": []any{map[string]any{"label": "Red"}, map[string]any{"label": "Blue"}}, "multiSelect": true},
		map[string]any{"header": "Name", "question": "", "options": []any{}},
		map[string]any{"question": "", "options": []any{}, "allowEmpty": true},
		map[string]any{"question": "Skipped?", "options": []any{}},
	}})
	got, err := agenttest.AnswerControl(claudeProvider{}, payload, claudeTestRequestID, agenttest.QuestionDecision(
		&leapmuxv1.QuestionAnswer{Selected: []string{"Red", "Blue"}},
		&leapmuxv1.QuestionAnswer{CustomText: "  Ada  "},
		&leapmuxv1.QuestionAnswer{},
		&leapmuxv1.QuestionAnswer{CustomText: "No"},
	))
	require.NoError(t, err)
	var envelope struct {
		Response struct {
			Response struct {
				Behavior     string `json:"behavior"`
				UpdatedInput struct {
					Questions json.RawMessage   `json:"questions"`
					Answers   map[string]string `json:"answers"`
				} `json:"updatedInput"`
			} `json:"response"`
		} `json:"response"`
	}
	require.NoError(t, json.Unmarshal(got.Content, &envelope))
	assert.Equal(t, "allow", envelope.Response.Response.Behavior)
	assert.NotEmpty(t, envelope.Response.Response.UpdatedInput.Questions, "the tool's own input stays")
	assert.Equal(t, map[string]string{"Which color?": "Red, Blue", "Name": "Ada", "q2": "", "Skipped?": "No"}, envelope.Response.Response.UpdatedInput.Answers,
		"the key falls back to the header, then to the index; an empty answer only where the question allows one")
}

func TestAnswerControlRequest_ACancelledQuestionDeniesWithItsFeedback(t *testing.T) {
	payload := controlPayload(t, ToolNameAskUserQuestion, map[string]any{"questions": []any{map[string]any{"question": "Which?", "options": []any{}}}})
	got, err := agenttest.AnswerControl(claudeProvider{}, payload, claudeTestRequestID, agenttest.CancelQuestions("User stopped"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"deny","message":"User stopped"}}}`, string(got.Content))
}

func TestAnswerControlRequest_AnElicitationAnswersWithItsActionAndContent(t *testing.T) {
	payload := []byte(`{"request":{"subtype":"elicitation","message":"Sign in"}}`)
	got, err := agenttest.AnswerControl(claudeProvider{}, payload, claudeTestRequestID, agenttest.ElicitationDecision(leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT, `{"user":"ada"}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"action":"accept","content":{"user":"ada"}}}}`, string(got.Content))

	got, err = agenttest.AnswerControl(claudeProvider{}, payload, claudeTestRequestID, agenttest.ElicitationDecision(leapmuxv1.ElicitationAction_ELICITATION_ACTION_DECLINE, ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"action":"decline"}}}`, string(got.Content))
}
