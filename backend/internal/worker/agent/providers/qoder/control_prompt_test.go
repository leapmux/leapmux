package qoder

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// qoderTestRequestID is the stored id of the request that each test answers.
const qoderTestRequestID = "approval:1"

// qoderToolPayload is a stored can_use_tool request in the shape that the
// Worker publishes it.
func qoderToolPayload(t *testing.T, toolName string, input any) []byte {
	t.Helper()
	return agenttest.CanUseToolPayload(t, qoderTestRequestID, toolName, input)
}

// qoderElicitationPayload is a stored MCP elicitation with the given request
// fields beside its subtype.
func qoderElicitationPayload(t *testing.T, fields map[string]any) []byte {
	t.Helper()
	request := map[string]any{"subtype": "elicitation"}
	for key, value := range fields {
		request[key] = value
	}
	return agenttest.MustJSON(t, map[string]any{"type": "control_request", "request_id": "form-1", "request": request})
}

func TestQoderReadControlRequest_AskUserQuestionReadsEachQuestionAndItsOptions(t *testing.T) {
	got := agenttest.ControlPromptOf(qoderProvider{}, qoderToolPayload(t, "AskUserQuestion", map[string]any{"questions": []any{
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
func TestQoderReadControlRequest_OpensTheMultipleChoiceControlOnlyForARealTrue(t *testing.T) {
	got := agenttest.ControlPromptOf(qoderProvider{}, qoderToolPayload(t, "AskUserQuestion", map[string]any{"questions": []any{
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
func TestQoderReadControlRequest_DropsAQuestionThatCannotBeDrawn(t *testing.T) {
	got := agenttest.ControlPromptOf(qoderProvider{}, qoderToolPayload(t, "AskUserQuestion", map[string]any{"questions": []any{
		nil, "plain text", 7, map[string]any{"question": "Which one?", "options": []any{}},
	}}))
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
		Questions: []*leapmuxv1.ControlQuestion{{Question: "Which one?"}},
	}}}, got)
}

func TestQoderReadControlRequest_AQuestionCallThatStatesNoListAsksNoQuestion(t *testing.T) {
	for _, input := range []any{map[string]any{}, map[string]any{"questions": "nope"}, nil} {
		got := agenttest.ControlPromptOf(qoderProvider{}, qoderToolPayload(t, "AskUserQuestion", input))
		require.NotNil(t, got.GetQuestion(), "%v", input)
		assert.Empty(t, got.GetQuestion().GetQuestions(), "%v", input)
	}
}

func TestQoderReadControlRequest_OnlyTheQuestionToolIsAQuestion(t *testing.T) {
	assert.NotNil(t, agenttest.ControlPromptOf(qoderProvider{}, qoderToolPayload(t, "WriteTodos", map[string]any{})).GetPermission())
	got := agenttest.ControlPromptOf(qoderProvider{}, []byte(`{}`))
	require.NotNil(t, got.GetPermission(), "a payload with no tool is a permission")
	assert.Empty(t, got.GetPermission().GetTitle())
}

// Qoder's runtime accepts a scope on an allow, and LeapMux offers the session
// tier beside the once answer and the deny.
func TestQoderReadControlRequest_AShellCallStatesItsCommandBesideTheScopeOptions(t *testing.T) {
	got := agenttest.ControlPromptOf(qoderProvider{}, qoderToolPayload(t, "Bash", map[string]any{"command": "ls"}))
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{
		Title:     "Bash",
		Command:   "ls",
		InputJson: `{"command":"ls"}`,
		Options: []*leapmuxv1.PermissionOption{
			{OptionId: "once", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, Name: "Allow once"},
			{OptionId: "session", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS, Name: "Allow for this session"},
			{OptionId: "deny", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, Name: "Deny"},
		},
	}}}, got)

	other := agenttest.ControlPromptOf(qoderProvider{}, qoderToolPayload(t, "Write", map[string]any{"command": "not a shell"})).GetPermission()
	assert.Empty(t, other.GetCommand(), "only a shell call states a command")
	assert.Len(t, other.GetOptions(), 3, "every permission offers the scope options")
}

func TestQoderReadControlRequest_ExitPlanModeIsAPlan(t *testing.T) {
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: &leapmuxv1.PlanPrompt{}}},
		agenttest.ControlPromptOf(qoderProvider{}, qoderToolPayload(t, "ExitPlanMode", map[string]any{})))
}

func TestQoderReadControlRequest_AnMCPElicitationIsAForm(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"count": map[string]any{"type": "integer", "title": "Count"}}}
	got := agenttest.ControlPromptOf(qoderProvider{}, qoderElicitationPayload(t, map[string]any{
		"mcp_server_name": "form_probe", "mode": "form", "message": "Choose the probe settings.", "requested_schema": schema,
	}))
	elicitation := got.GetElicitation()
	require.NotNil(t, elicitation)
	assert.Equal(t, "form", elicitation.GetMode())
	assert.Equal(t, "Choose the probe settings.", elicitation.GetMessage())
	assert.Equal(t, "form_probe", elicitation.GetServer())
	assert.JSONEq(t, `{"type":"object","properties":{"count":{"type":"integer","title":"Count"}}}`, elicitation.GetSchemaJson())
	assert.Empty(t, elicitation.GetUrl())
	assert.Empty(t, elicitation.GetTitle())
	assert.Empty(t, elicitation.GetDescription())
	assert.Equal(t, leapmuxv1.ElicitationPurpose_ELICITATION_PURPOSE_UNSPECIFIED, elicitation.GetPurpose())
	assert.Empty(t, elicitation.GetAcceptChoices())

	assert.NotNil(t, agenttest.ControlPromptOf(qoderProvider{}, []byte(`{"request":{"subtype":"can_use_tool"}}`)).GetPermission(), "a can_use_tool request is no form")
	assert.NotNil(t, agenttest.ControlPromptOf(qoderProvider{}, []byte(`{"request":{"subtype":7}}`)).GetPermission(), "a subtype that is no text specifies no form")
}

// Each field reads as absent when it is not text: a missing mode is a form, and
// the server's own name stands in for a missing display name. A field that is
// text stays as Qoder sent it, an empty one included.
func TestQoderReadControlRequest_AnElicitationReadsEachFieldAsQoderStatesIt(t *testing.T) {
	got := agenttest.ControlPromptOf(qoderProvider{}, qoderElicitationPayload(t, map[string]any{
		"display_name": "GitHub", "mcp_server_name": "github", "url": "https://example.com/auth", "mode": "url",
		"title": "Login", "description": "Sign in to continue.",
	})).GetElicitation()
	assert.Equal(t, "url", got.GetMode())
	assert.Equal(t, "GitHub", got.GetServer(), "the display name wins over the server's name")
	assert.Equal(t, "https://example.com/auth", got.GetUrl())
	assert.Equal(t, "Login", got.GetTitle())
	assert.Equal(t, "Sign in to continue.", got.GetDescription())
	assert.Empty(t, got.GetSchemaJson(), "a form that states no schema has none")

	got = agenttest.ControlPromptOf(qoderProvider{}, qoderElicitationPayload(t, map[string]any{
		"mode": 3, "display_name": 4, "mcp_server_name": "github", "message": true, "requested_schema": nil,
	})).GetElicitation()
	assert.Equal(t, contracts.MCPElicitationModeForm, got.GetMode(), "a mode that is no text reads as a form")
	assert.Equal(t, "github", got.GetServer(), "a display name that is no text gives way to the server's name")
	assert.Empty(t, got.GetMessage())
	assert.Empty(t, got.GetSchemaJson(), "a null schema is none")

	got = agenttest.ControlPromptOf(qoderProvider{}, qoderElicitationPayload(t, map[string]any{"mode": "", "display_name": "", "mcp_server_name": "github"})).GetElicitation()
	assert.Empty(t, got.GetMode(), "a mode that is text stays, an empty one included")
	assert.Empty(t, got.GetServer(), "a display name that is text wins, an empty one included")

	assert.Nil(t, agenttest.ControlPromptOf(qoderProvider{}, []byte(`{"REQUEST":{"subtype":"elicitation","message":"Upper"}}`)).GetElicitation(), "uppercase request key must not match an elicitation")
	assert.Nil(t, agenttest.ControlPromptOf(qoderProvider{}, []byte(`{"request":{"SUBTYPE":"elicitation","message":"Upper"}}`)).GetElicitation(), "uppercase subtype key must not match an elicitation")
	assert.Nil(t, agenttest.ControlPromptOf(qoderProvider{}, []byte(`null`)).GetElicitation(), "null payload must not match an elicitation")
	assert.Nil(t, agenttest.ControlPromptOf(qoderProvider{}, []byte(`{"request":null}`)).GetElicitation(), "null request must not match an elicitation")

	got = agenttest.ControlPromptOf(qoderProvider{}, []byte(`{"request":{"subtype":"elicitation","MODE":"url","DISPLAY_NAME":"Upper","mcp_server_name":"github","MESSAGE":"Ignored"}}`)).GetElicitation()
	require.NotNil(t, got)
	assert.Equal(t, contracts.MCPElicitationModeForm, got.GetMode(), "uppercase mode key must be ignored")
	assert.Equal(t, "github", got.GetServer(), "uppercase display_name key must fall back to mcp_server_name")
	assert.Empty(t, got.GetMessage(), "uppercase message key must be ignored")
}

// The question tool's request is a question even when it also states the
// elicitation subtype: the question reader runs first.
func TestQoderReadControlRequest_AQuestionComesBeforeAnElicitation(t *testing.T) {
	got := agenttest.ControlPromptOf(qoderProvider{}, []byte(`{"request":{"subtype":"elicitation","tool_name":"AskUserQuestion","input":{"questions":[]}}}`))
	assert.NotNil(t, got.GetQuestion())
}

// The Allow button, and the composer's send with no text, choose an allowing
// option. Each option rides the neutral envelope as the choice, and
// ResolveControlResponse turns the session choice into Qoder's scope.
func TestQoderAnswerControlRequest_AnOptionRidesTheNeutralEnvelopeAsItsChoice(t *testing.T) {
	payload := qoderToolPayload(t, "Bash", map[string]any{"command": "ls"})
	for option, behavior := range map[string]string{"once": "allow", "session": "allow", "deny": "deny"} {
		got, err := agenttest.AnswerControl(qoderProvider{}, payload, qoderTestRequestID, agenttest.PermissionDecision(option, ""))
		require.NoError(t, err, option)
		assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"approval:1","response":{"behavior":"`+behavior+`","choice":"`+option+`"}}}`, string(got.Content), option)
		assert.Nil(t, got.PlanApproval, option)
		assert.Empty(t, got.FollowUp, option)
	}
}

// The composer's typed refusal denies with the text and states no choice.
func TestQoderAnswerControlRequest_ARefusalWithFeedbackDeniesWithIt(t *testing.T) {
	got, err := agenttest.AnswerControl(qoderProvider{}, qoderToolPayload(t, "Bash", map[string]any{"command": "ls"}), qoderTestRequestID, agenttest.PermissionDecision("deny", "Use a dry run."))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"approval:1","response":{"behavior":"deny","message":"Use a dry run."}}}`, string(got.Content))
	assert.Empty(t, got.FollowUp, "the deny carries the feedback itself")
}

// An option that the prompt does not offer never reaches Qoder, so no answer
// can grant a scope that the runtime was never offered.
func TestQoderAnswerControlRequest_RefusesADecisionThatDoesNotAnswerThePrompt(t *testing.T) {
	payload := qoderToolPayload(t, "Bash", map[string]any{"command": "ls"})
	for name, decision := range map[string]*leapmuxv1.ControlDecision{
		"an option not offered":       agenttest.PermissionDecision("persist", ""),
		"LeapMux's own allow":         agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""),
		"feedback beside once":        agenttest.PermissionDecision("once", "Why not."),
		"feedback beside the session": agenttest.PermissionDecision("session", "Why not."),
		"a plan decision":             agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true}),
		"no kind":                     {},
	} {
		_, err := agenttest.AnswerControl(qoderProvider{}, payload, qoderTestRequestID, decision)
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}
}

// The composer's text on a plan always refuses it: the Approve button owns the
// allow path.
func TestQoderAnswerControlRequest_APlanRefusalDeniesWithItsFeedback(t *testing.T) {
	payload := qoderToolPayload(t, "ExitPlanMode", map[string]any{"plan": "Do the thing."})
	got, err := agenttest.AnswerControl(qoderProvider{}, payload, qoderTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Feedback: "Revise step 2."}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"approval:1","response":{"behavior":"deny","message":"Revise step 2."}}}`, string(got.Content))

	got, err = agenttest.AnswerControl(qoderProvider{}, payload, qoderTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"approval:1","response":{"behavior":"deny","message":"`+contracts.ControlAnswerMessageRejectedByUser+`"}}}`, string(got.Content))
}

func TestQoderAnswerControlRequest_APlanApprovalAllowsWithTheToolInputAndTheSettings(t *testing.T) {
	payload := qoderToolPayload(t, "ExitPlanMode", map[string]any{"plan": "Do the thing."})
	got, err := agenttest.AnswerControl(qoderProvider{}, payload, qoderTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, PermissionMode: contracts.QoderModeAuto}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"approval:1","response":{"behavior":"allow","updatedInput":{"plan":"Do the thing."}}}}`, string(got.Content))
	assert.True(t, proto.Equal(&leapmuxv1.PlanApprovalSettings{PermissionMode: contracts.QoderModeAuto}, got.PlanApproval))

	_, err = agenttest.AnswerControl(qoderProvider{}, payload, qoderTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, ChoiceId: "revise"}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "Qoder's plan offers no choices")
}

var qoderColorQuestions = []any{
	map[string]any{"question": "Color?", "header": "Color", "options": []any{
		map[string]any{"label": "Red", "description": "Warm"},
		map[string]any{"label": "Blue", "description": "Cool"},
	}},
}

// The reply is the whole tool input with `answers` added: the Worker folds
// that object into Qoder's `updatedInput`, and Qoder's own reader matches the
// answers back to the questions by their text.
func TestQoderAnswerControlRequest_AQuestionAnswerFoldsTheAnswersIntoTheToolInput(t *testing.T) {
	payload := qoderToolPayload(t, "AskUserQuestion", map[string]any{"questions": qoderColorQuestions})
	got, err := agenttest.AnswerControl(qoderProvider{}, payload, qoderTestRequestID, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"Red"}}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"approval:1","response":{"behavior":"allow","updatedInput":{
		"questions":[{"question":"Color?","header":"Color","options":[{"label":"Red","description":"Warm"},{"label":"Blue","description":"Cool"}]}],
		"answers":{"Color?":"Red"}
	}}}}`, string(got.Content))
}

// A multiple-choice answer states each picked option and the typed words, as
// one more value: the user chose the options AND the words.
func TestQoderAnswerControlRequest_AMultipleChoiceAnswerSendsEveryPickAndTheTypedWords(t *testing.T) {
	payload := qoderToolPayload(t, "AskUserQuestion", map[string]any{"questions": []any{
		map[string]any{"question": "Toppings?", "multiSelect": true, "options": []any{
			map[string]any{"label": "Cheese"}, map[string]any{"label": "Olives"},
		}},
	}})
	got, err := agenttest.AnswerControl(qoderProvider{}, payload, qoderTestRequestID, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"Cheese", "Olives"}, CustomText: " Basil "}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"approval:1","response":{"behavior":"allow","updatedInput":{
		"questions":[{"question":"Toppings?","multiSelect":true,"options":[{"label":"Cheese"},{"label":"Olives"}]}],
		"answers":{"Toppings?":"Cheese, Olives, Basil"}
	}}}}`, string(got.Content))
}

func TestQoderAnswerControlRequest_ACancelledQuestionDeniesWithTheFeedback(t *testing.T) {
	payload := qoderToolPayload(t, "AskUserQuestion", map[string]any{"questions": qoderColorQuestions})
	got, err := agenttest.AnswerControl(qoderProvider{}, payload, qoderTestRequestID, agenttest.CancelQuestions("Ask again tomorrow."))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"approval:1","response":{"behavior":"deny","message":"Ask again tomorrow."}}}`, string(got.Content))
}

func TestQoderAnswerControlRequest_AnElicitationAnswersWithItsActionAndContent(t *testing.T) {
	payload := qoderElicitationPayload(t, map[string]any{"mode": "form", "requested_schema": map[string]any{"type": "object"}})
	got, err := agenttest.AnswerControl(qoderProvider{}, payload, qoderTestRequestID, agenttest.ElicitationDecision(leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT, `{"count":0}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"approval:1","response":{"action":"accept","content":{"count":0}}}}`, string(got.Content))

	for action, token := range map[leapmuxv1.ElicitationAction]string{
		leapmuxv1.ElicitationAction_ELICITATION_ACTION_DECLINE: "decline",
		leapmuxv1.ElicitationAction_ELICITATION_ACTION_CANCEL:  "cancel",
	} {
		got, err := agenttest.AnswerControl(qoderProvider{}, payload, qoderTestRequestID, agenttest.ElicitationDecision(action, ""))
		require.NoError(t, err, token)
		assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"approval:1","response":{"action":"`+token+`"}}}`, string(got.Content), token)
	}

	_, err = agenttest.AnswerControl(qoderProvider{}, payload, qoderTestRequestID, agenttest.ElicitationChoiceDecision("", "always"))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "Qoder's form offers no accept choices")
	_, err = agenttest.AnswerControl(qoderProvider{}, payload, qoderTestRequestID, agenttest.PermissionDecision("once", ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a form takes no permission decision")
}

// The answer is the neutral envelope that ResolveControlResponse reads today,
// so it turns into Qoder's own decision object.
func TestQoderAnswerControlRequest_ResolvesIntoQodersOwnDecision(t *testing.T) {
	resolve := func(payload []byte, answer agent.ControlAnswer) map[string]any {
		t.Helper()
		resolution := agenttest.ResolveAnswer(qoderProvider{}, payload, qoderTestRequestID, answer)
		require.False(t, resolution.Withhold)
		return nativeAnswer(t, resolution.Content)
	}
	payload := qoderToolPayload(t, "Bash", map[string]any{"command": "ls"})
	for option, want := range map[string]map[string]any{
		"once":    {"behavior": "allow", "outcome": contracts.QoderPermissionOutcomeProceedOnce},
		"session": {"behavior": "allow", "permissionScope": "session"},
		"deny":    {"behavior": "deny"},
	} {
		answer, err := agenttest.AnswerControl(qoderProvider{}, payload, qoderTestRequestID, agenttest.PermissionDecision(option, ""))
		require.NoError(t, err, option)
		assert.Equal(t, want, resolve(payload, answer), option)
	}
	refusal, err := agenttest.AnswerControl(qoderProvider{}, payload, qoderTestRequestID, agenttest.PermissionDecision("deny", "Use a dry run."))
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"behavior": "deny", "message": "Use a dry run.", "reason": "Use a dry run."}, resolve(payload, refusal))
}
