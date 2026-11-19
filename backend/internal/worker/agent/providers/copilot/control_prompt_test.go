package copilot

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
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// copilotTestRequestID is the stored id of the request that each test answers.
const copilotTestRequestID = "req-1"

// copilotPermissionFrame is one stored permission.requested.
func copilotPermissionFrame(t *testing.T, request any) []byte {
	t.Helper()
	return copilotStoredFrame(t, contracts.CopilotEventPermissionRequested, map[string]any{"requestId": "native-1", "permissionRequest": request})
}

func copilotOptionIDs(prompt *leapmuxv1.ControlPrompt) []string {
	var ids []string
	for _, option := range prompt.GetPermission().GetOptions() {
		ids = append(ids, option.GetOptionId())
	}
	return ids
}

// copilotTestSession is the session of each stored frame (copilotStoredFrame).
const copilotTestSession = "session-1"

// copilotStoredKey is the key that the Worker stores payload under: the key
// that the provider reads from it in copilotTestSession.
func copilotStoredKey(t *testing.T, payload []byte) string {
	t.Helper()
	key, ok := copilotPlugin(t).ControlRequestKey(payload, copilotTestSession)
	require.True(t, ok, "the stored request gives no key: %s", payload)
	return key
}

// copilotResolveDecision answers payload with decision under its stored key,
// and resolves the answer through the provider's own resolution, as the
// service does after AnswerControlRequest.
func copilotResolveDecision(t *testing.T, payload []byte, decision *leapmuxv1.ControlDecision) map[string]any {
	t.Helper()
	key := copilotStoredKey(t, payload)
	answer, err := agenttest.AnswerControl(copilotPlugin(t), payload, key, decision)
	require.NoError(t, err)
	return copilotNativeResponse(t, agenttest.ResolveAnswerInSession(copilotPlugin(t), payload, copilotTestSession, key, answer))
}

func TestCopilotReadControlRequest_AQuestionReadsItsChoicesAndAcceptsAnEmptyAnswer(t *testing.T) {
	t.Parallel()
	got := agenttest.ControlPromptOf(copilotPlugin(t), copilotStoredFrame(t, contracts.CopilotEventUserInputRequested, map[string]any{
		"requestId": "native-1", "question": "Which one?", "choices": []any{"A", 7, nil, "B"}, "allowFreeform": true,
	}))
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
		Questions: []*leapmuxv1.ControlQuestion{{
			Question:   "Which one?",
			Options:    []*leapmuxv1.QuestionOption{{Label: "A"}, {Label: "B"}},
			AllowEmpty: true,
		}},
	}}}, got)
}

// The empty answer is the shortest answer that is none of the choices, so a
// request that refuses one refuses the other.
func TestCopilotReadControlRequest_AQuestionOffersNoEmptyAnswerUnlessTheRequestTakesOne(t *testing.T) {
	t.Parallel()
	for _, data := range []map[string]any{
		{"question": "Which one?", "choices": []any{"A"}, "allowFreeform": false},
		{"question": "Which one?", "choices": []any{"A"}},
		{"question": "Which one?", "choices": []any{"A"}, "allowFreeform": "true"},
	} {
		got := agenttest.ControlPromptOf(copilotPlugin(t), copilotStoredFrame(t, contracts.CopilotEventUserInputRequested, data))
		require.Len(t, got.GetQuestion().GetQuestions(), 1)
		assert.False(t, got.GetQuestion().GetQuestions()[0].GetAllowEmpty(), "%v", data)
	}
	got := agenttest.ControlPromptOf(copilotPlugin(t), copilotStoredFrame(t, contracts.CopilotEventUserInputRequested, "not an object"))
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
		Questions: []*leapmuxv1.ControlQuestion{{Options: []*leapmuxv1.QuestionOption{}}},
	}}}, got)
}

// `allowFreeform: false` beside choices takes a choice alone, so typed text
// is refused before it reaches the runtime. Anything else permits typed
// text: a question with no choice has no other answer.
func TestCopilotReadControlRequest_AQuestionTakesTypedTextUnlessTheRequestTakesChoicesAlone(t *testing.T) {
	t.Parallel()
	choicesAlone := copilotStoredFrame(t, contracts.CopilotEventUserInputRequested, map[string]any{"question": "Which one?", "choices": []any{"A"}, "allowFreeform": false})
	question := agenttest.ControlPromptOf(copilotPlugin(t), choicesAlone).GetQuestion().GetQuestions()[0]
	require.NotNil(t, question.AllowCustomText)
	assert.False(t, question.GetAllowCustomText())
	_, err := agenttest.AnswerControl(copilotPlugin(t), choicesAlone, copilotTestRequestID, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{CustomText: "C"}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "typed text where the request takes choices alone")
	_, err = agenttest.AnswerControl(copilotPlugin(t), choicesAlone, copilotTestRequestID, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"A"}}))
	assert.NoError(t, err)

	for _, data := range []map[string]any{
		{"question": "Which one?", "choices": []any{}, "allowFreeform": false},
		{"question": "Which one?", "choices": []any{"A"}, "allowFreeform": true},
		{"question": "Which one?", "choices": []any{"A"}},
		{"question": "Which one?", "choices": []any{"A"}, "allowFreeform": nil},
		{"question": "Which one?", "choices": []any{"A"}, "allowFreeform": "false"},
	} {
		question := agenttest.ControlPromptOf(copilotPlugin(t), copilotStoredFrame(t, contracts.CopilotEventUserInputRequested, data)).GetQuestion().GetQuestions()[0]
		assert.Nil(t, question.AllowCustomText, "%v", data)
	}
}

func TestCopilotReadControlRequest_AnElicitationIsAForm(t *testing.T) {
	t.Parallel()
	got := agenttest.ControlPromptOf(copilotPlugin(t), copilotStoredFrame(t, contracts.CopilotEventElicitationRequested, map[string]any{
		"message": "Sign in", "elicitationSource": "github", "requestedSchema": map[string]any{"type": "object"},
	}))
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Elicitation{Elicitation: &leapmuxv1.ElicitationPrompt{
		Mode: "form", Message: "Sign in", Server: "github", SchemaJson: `{"type":"object"}`,
	}}}, got)

	got = agenttest.ControlPromptOf(copilotPlugin(t), copilotStoredFrame(t, contracts.CopilotEventElicitationRequested, map[string]any{
		"mode": "url", "url": "https://example.com/auth", "message": 7, "requestedSchema": nil,
	}))
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Elicitation{Elicitation: &leapmuxv1.ElicitationPrompt{
		Mode: "url", Url: "https://example.com/auth",
	}}}, got)
}

// Copilot sends the whole plan in its approval request.
func TestCopilotReadControlRequest_APlanApprovalCarriesThePlanText(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		data map[string]any
		text string
	}{
		{map[string]any{"planContent": "# Ship it", "summary": "Ship"}, "# Ship it"},
		{map[string]any{"planContent": "", "summary": "Ship"}, "Ship"},
		{map[string]any{"planContent": 7}, ""},
	} {
		got := agenttest.ControlPromptOf(copilotPlugin(t), copilotStoredFrame(t, contracts.CopilotEventExitPlanModeRequested, tc.data))
		agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: &leapmuxv1.PlanPrompt{Text: tc.text}}}, got)
	}
}

func TestCopilotReadControlRequest_TheAutoModeSwitchHasThreeIndependentAnswers(t *testing.T) {
	t.Parallel()
	got := agenttest.ControlPromptOf(copilotPlugin(t), copilotStoredFrame(t, contracts.CopilotEventAutoModeSwitchRequested, map[string]any{
		"requestId": "switch", "errorCode": "user_model_rate_limited",
	}))
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{
		Title:  "Copilot asks to switch to Auto",
		Reason: "The current model reached its rate limit.",
		Options: []*leapmuxv1.PermissionOption{
			{OptionId: "once", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, Name: "Switch this turn"},
			{OptionId: "session", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS, Name: "Always switch"},
			{OptionId: "reject", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, Name: "Decline"},
		},
		IndependentOptions: true,
	}}}, got)
}

// An unknown switch reason stays in the raw request only.
func TestCopilotReadControlRequest_TheAutoModeSwitchStatesNoUnknownReason(t *testing.T) {
	t.Parallel()
	for _, code := range []any{"", "future_code", 7} {
		got := agenttest.ControlPromptOf(copilotPlugin(t), copilotStoredFrame(t, contracts.CopilotEventAutoModeSwitchRequested, map[string]any{"requestId": "switch", "errorCode": code}))
		assert.Empty(t, got.GetPermission().GetReason(), "%v", code)
		assert.Empty(t, got.GetPermission().GetInputJson(), "%v", code)
	}
}

func TestCopilotReadControlRequest_ASamplingRequestStatesItsServerAndRequest(t *testing.T) {
	t.Parallel()
	request := map[string]any{"messages": []any{map[string]any{"role": "user", "content": map[string]any{"type": "text", "text": "Reply with SAMPLED."}}}, "maxTokens": 20}
	got := agenttest.ControlPromptOf(copilotPlugin(t), copilotStoredFrame(t, contracts.CopilotEventSamplingRequested, map[string]any{"serverName": "sampler", "request": request}))
	permission := got.GetPermission()
	require.NotNil(t, permission)
	assert.Equal(t, "The MCP server sampler asks the model to answer", permission.GetTitle())
	want, err := json.Marshal(request)
	require.NoError(t, err)
	assert.JSONEq(t, string(want), permission.GetInputJson())
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{
		Options: []*leapmuxv1.PermissionOption{
			{OptionId: "once", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, Name: "Allow"},
			{OptionId: "reject", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, Name: "Deny"},
		},
	}}}, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{Options: permission.GetOptions()}}})

	got = agenttest.ControlPromptOf(copilotPlugin(t), copilotStoredFrame(t, contracts.CopilotEventSamplingRequested, map[string]any{"request": "not an object"}))
	assert.Equal(t, "The MCP server asks the model to answer", got.GetPermission().GetTitle())
	assert.Equal(t, "{}", got.GetPermission().GetInputJson(), "a request that is no object states an empty one")
}

func TestCopilotReadControlRequest_AShellPermissionStatesItsCommandAndRequest(t *testing.T) {
	t.Parallel()
	request := map[string]any{
		"kind": "shell", "intention": "Run command", "fullCommandText": "npm test -- --runInBand",
		"commands": []any{map[string]any{"identifier": "npm", "readOnly": false}}, "canOfferSessionApproval": true,
	}
	got := agenttest.ControlPromptOf(copilotPlugin(t), copilotPermissionFrame(t, request)).GetPermission()
	assert.Equal(t, "Run command", got.GetTitle())
	assert.Equal(t, "npm test -- --runInBand", got.GetCommand())
	want, err := json.Marshal(request)
	require.NoError(t, err)
	assert.JSONEq(t, string(want), got.GetInputJson(), "the request's own fields are the detail")
	assert.Empty(t, got.GetReason())
	assert.False(t, got.GetIndependentOptions())

	got = agenttest.ControlPromptOf(copilotPlugin(t), copilotPermissionFrame(t, map[string]any{"kind": "write", "fullCommandText": "ls"})).GetPermission()
	assert.Empty(t, got.GetCommand(), "only a shell request states a command")
}

func TestCopilotReadControlRequest_ThePermissionTitleFallsBackInOrder(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		request map[string]any
		title   string
	}{
		{map[string]any{"kind": "read", "intention": "Read it", "toolTitle": "Viewer", "toolName": "view"}, "Read it"},
		{map[string]any{"kind": "read", "intention": "", "toolTitle": "Viewer", "toolName": "view"}, "Viewer"},
		{map[string]any{"kind": "read", "toolName": "view"}, "view"},
		{map[string]any{"kind": "read", "intention": 7}, "read"},
	} {
		got := agenttest.ControlPromptOf(copilotPlugin(t), copilotPermissionFrame(t, tc.request))
		assert.Equal(t, tc.title, got.GetPermission().GetTitle(), "%v", tc.request)
	}
}

func TestCopilotReadControlRequest_OffersBothWiderScopesWhereTheRuleIsExpressible(t *testing.T) {
	t.Parallel()
	for _, request := range []map[string]any{
		{"kind": "read", "path": "/project/main.go"},
		{"kind": "write", "canOfferSessionApproval": true},
		{"kind": "shell", "canOfferSessionApproval": true, "commands": []any{map[string]any{"identifier": "ls"}, map[string]any{"identifier": "wc"}}},
		{"kind": "mcp", "serverName": "github", "toolName": "search"},
	} {
		got := agenttest.ControlPromptOf(copilotPlugin(t), copilotPermissionFrame(t, request))
		agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{
			Options: []*leapmuxv1.PermissionOption{
				{OptionId: "once", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, Name: "Allow once"},
				{OptionId: "session", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS, Name: "Allow for this session"},
				{OptionId: "project", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS, Name: "Allow for this project"},
				{OptionId: "reject", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, Name: "Reject"},
			},
		}}}, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{Options: got.GetPermission().GetOptions()}}})
	}
}

// A rule this build cannot construct would claim a scope that the runtime
// never applies, so only the single approval is offered.
func TestCopilotReadControlRequest_OffersTheSingleApprovalAloneWhereNoRuleIsExpressible(t *testing.T) {
	t.Parallel()
	for _, request := range []any{
		map[string]any{"kind": "write", "canOfferSessionApproval": false},
		map[string]any{"kind": "write", "canOfferSessionApproval": "true"},
		map[string]any{"kind": "shell", "canOfferSessionApproval": true, "commands": []any{}},
		map[string]any{"kind": "shell", "canOfferSessionApproval": true, "commands": []any{map[string]any{"identifier": ""}}},
		map[string]any{"kind": "shell", "canOfferSessionApproval": true, "commands": []any{map[string]any{"identifier": "ls"}, "pwd"}},
		map[string]any{"kind": "shell", "canOfferSessionApproval": false, "commands": []any{map[string]any{"identifier": "ls"}}},
		map[string]any{"kind": "mcp"},
		map[string]any{"kind": "future_kind"},
		"not an object",
	} {
		got := agenttest.ControlPromptOf(copilotPlugin(t), copilotPermissionFrame(t, request))
		assert.Equal(t, []string{"once", "reject"}, copilotOptionIDs(got), "%v", request)
	}
}

// A permission request whose request the runtime states in no readable
// shape is still a permission: a bare one with the single approval and the
// refusal.
func TestCopilotReadControlRequest_ReadsAnUnreadablePermissionRequestAsABarePermission(t *testing.T) {
	t.Parallel()
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{
		Options: []*leapmuxv1.PermissionOption{
			{OptionId: "once", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, Name: "Allow once"},
			{OptionId: "reject", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, Name: "Reject"},
		},
	}}}, agenttest.ControlPromptOf(copilotPlugin(t), copilotPermissionFrame(t, "not an object")))
}

// The runtime raises no other request, and no answer of the agent reaches
// the runtime for one, so any other payload reads as unrecognized.
func TestCopilotReadControlRequest_ReadsAnUnknownPayloadAsUnrecognized(t *testing.T) {
	t.Parallel()
	for _, payload := range [][]byte{
		[]byte(`{}`),
		[]byte(`not json`),
		copilotStoredFrame(t, contracts.CopilotEventToolStarted, map[string]any{"toolName": "view"}),
	} {
		agenttest.RequireProto(t, agent.UnrecognizedControlPrompt(), agenttest.ControlPromptOf(copilotPlugin(t), payload), "%s", payload)
	}
}

func copilotTrustQuestion(t *testing.T) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		contracts.WorkspaceTrustFieldType:          contracts.WorkspaceTrustControlTypeQuestion,
		contracts.WorkspaceTrustFieldPath:          "/work/project",
		contracts.WorkspaceTrustFieldConfiguration: []any{"MCP servers"},
	})
	require.NoError(t, err)
	return payload
}

func TestCopilotReadControlRequest_DrawsTheWorkspaceTrustQuestionWithTheAgentName(t *testing.T) {
	t.Parallel()
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{
		Title:  "Trust the folder /work/project",
		Reason: "GitHub Copilot loads the MCP servers of a project only from a folder that you trust, and it keeps your answer for the folder.",
		Options: []*leapmuxv1.PermissionOption{
			{OptionId: "workspace-trust", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, Name: "Trust this folder"},
			{OptionId: "workspace-reject", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, Name: "Do not trust"},
		},
	}}, EditorPurpose: leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_NONE}, agenttest.ControlPromptOf(copilotPlugin(t), copilotTrustQuestion(t)))
}

func TestCopilotAnswerControlRequest_SendsTheChosenScopeWithTheApproval(t *testing.T) {
	t.Parallel()
	payload := copilotPermissionFrame(t, map[string]any{"kind": "read", "path": "/project/main.go"})
	for _, scope := range []string{"once", "session", "project"} {
		got, err := agenttest.AnswerControl(copilotPlugin(t), payload, copilotTestRequestID, agenttest.PermissionDecision(scope, ""))
		require.NoError(t, err, scope)
		assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"allow","scope":"`+scope+`"}}}`, string(got.Content), scope)
		assert.Nil(t, got.PlanApproval)
		assert.Empty(t, got.FollowUp)
	}
	got, err := agenttest.AnswerControl(copilotPlugin(t), payload, copilotTestRequestID, agenttest.PermissionDecision("reject", ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"deny","message":"Rejected by user."}}}`, string(got.Content))
}

// Each answer reaches the runtime as its own decision word, so the answer is
// the bytes that the provider's resolution reads.
func TestCopilotAnswerControlRequest_EachPermissionAnswerResolvesToTheRuntimeDecision(t *testing.T) {
	t.Parallel()
	payload := copilotPermissionFrame(t, map[string]any{"kind": "read", "path": "/project/main.go"})
	for option, want := range map[string]map[string]any{
		"once":    {"kind": contracts.CopilotDecisionApproveOnce},
		"session": {"kind": contracts.CopilotDecisionApproveForSession, "approval": map[string]any{"kind": "read"}},
		"project": {"kind": contracts.CopilotDecisionApproveForLocation, "approval": map[string]any{"kind": "read"}},
		"reject":  {"kind": contracts.CopilotDecisionReject},
	} {
		assert.Equal(t, want, copilotResolveDecision(t, payload, agenttest.PermissionDecision(option, "")), option)
	}
	assert.Equal(t, map[string]any{"kind": contracts.CopilotDecisionReject, "feedback": "Use the other file."},
		copilotResolveDecision(t, payload, agenttest.PermissionDecision("reject", "Use the other file.")))
}

// The composer's send is a refusal, and its text rides the deny.
func TestCopilotAnswerControlRequest_ARefusalWithFeedbackDeniesWithIt(t *testing.T) {
	t.Parallel()
	got, err := agenttest.AnswerControl(copilotPlugin(t), copilotPermissionFrame(t, map[string]any{"kind": "read"}), copilotTestRequestID, agenttest.PermissionDecision("reject", "Use the other file."))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"deny","message":"Use the other file."}}}`, string(got.Content))
	assert.Empty(t, got.FollowUp, "the runtime takes the feedback in its own answer")
}

func TestCopilotAnswerControlRequest_TheAutoModeSwitchSendsEachIndependentDecision(t *testing.T) {
	t.Parallel()
	payload := copilotStoredFrame(t, contracts.CopilotEventAutoModeSwitchRequested, map[string]any{"requestId": "switch", "errorCode": "user_model_rate_limited"})
	for option, response := range map[string]string{
		"once":    `{"behavior":"allow","scope":"once"}`,
		"session": `{"behavior":"allow","scope":"session"}`,
		"reject":  `{"behavior":"deny","message":"Rejected by user."}`,
	} {
		got, err := agenttest.AnswerControl(copilotPlugin(t), payload, copilotTestRequestID, agenttest.PermissionDecision(option, ""))
		require.NoError(t, err, option)
		assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":`+response+`}}`, string(got.Content), option)
	}
	_, err := agenttest.AnswerControl(copilotPlugin(t), payload, copilotTestRequestID, agenttest.PermissionDecision("project", ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "the switch offers no project scope")
}

func TestCopilotAnswerControlRequest_ASamplingDecisionResolvesToTheSavedDecision(t *testing.T) {
	t.Parallel()
	payload := copilotStoredFrame(t, contracts.CopilotEventSamplingRequested, map[string]any{"requestId": "native-1", "serverName": "sampler", "request": map[string]any{}})
	for option, decision := range map[string]string{"once": contracts.CopilotSamplingDecisionAllow, "reject": contracts.CopilotSamplingDecisionDeny} {
		assert.Equal(t, map[string]any{"decision": decision}, copilotResolveDecision(t, payload, agenttest.PermissionDecision(option, "")), option)
	}
}

func TestCopilotAnswerControlRequest_RefusesAPermissionDecisionThatThePromptDoesNotOffer(t *testing.T) {
	t.Parallel()
	payload := copilotPermissionFrame(t, map[string]any{"kind": "write", "canOfferSessionApproval": false})
	_, err := agenttest.AnswerControl(copilotPlugin(t), payload, copilotTestRequestID, agenttest.PermissionDecision("session", ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a write without a session rule offers no session scope")
	_, err = agenttest.AnswerControl(copilotPlugin(t), payload, copilotTestRequestID, agenttest.PermissionDecision("once", "Only this time."))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "feedback goes with a refusal")
	_, err = agenttest.AnswerControl(copilotPlugin(t), payload, copilotTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a permission takes no plan decision")
	_, err = agenttest.AnswerControl(copilotPlugin(t), payload, copilotTestRequestID, &leapmuxv1.ControlDecision{})
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a decision of no kind answers nothing")
}

func TestCopilotAnswerControlRequest_AnswersTheWorkspaceTrustQuestion(t *testing.T) {
	t.Parallel()
	payload := copilotTrustQuestion(t)
	got, err := agenttest.AnswerControl(copilotPlugin(t), payload, copilotTestRequestID, agenttest.PermissionDecision("workspace-trust", ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"allow"}}}`, string(got.Content))
	// The question is stored under the key of its session.
	trustRequestID := providerkit.WorkspaceTrustRequestID(copilotTestSession)
	trust, err := agenttest.AnswerControl(copilotPlugin(t), payload, trustRequestID, agenttest.PermissionDecision("workspace-trust", ""))
	require.NoError(t, err)
	resolved := agenttest.ResolveAnswerInSession(copilotPlugin(t), payload, copilotTestSession, trustRequestID, trust)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"`+trustRequestID+`","response":{"decision":"trust"}}}`, string(resolved.Content))

	got, err = agenttest.AnswerControl(copilotPlugin(t), payload, copilotTestRequestID, agenttest.PermissionDecision("workspace-reject", ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"deny"}}}`, string(got.Content))

	_, err = agenttest.AnswerControl(copilotPlugin(t), payload, copilotTestRequestID, agenttest.PermissionDecision("workspace-reject", "Not here."))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "the trust question takes no reason")

	_, err = agenttest.AnswerControl(copilotPlugin(t), payload, copilotTestRequestID, agenttest.PermissionDecision("session", ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "the question offers its own two answers only")
}

// The runtime tells a selected choice from a typed answer, and it accepts an
// explicit empty answer where the request says so.
func TestCopilotAnswerControlRequest_AQuestionAnswerStatesWhetherTheUserTypedIt(t *testing.T) {
	t.Parallel()
	payload := copilotStoredFrame(t, contracts.CopilotEventUserInputRequested, map[string]any{
		"requestId": "native-1", "question": "Which one?", "choices": []any{"A", "B"}, "allowFreeform": true,
	})
	for _, tc := range []struct {
		name     string
		decision *leapmuxv1.ControlDecision
		response string
	}{
		{"a selected choice", agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"B"}}), `{"behavior":"allow","answer":"B","wasFreeform":false}`},
		{"a typed answer", agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{CustomText: "  neither  "}), `{"behavior":"allow","answer":"neither","wasFreeform":true}`},
		{"an explicit empty answer", agenttest.QuestionDecision(), `{"behavior":"allow","answer":"","wasFreeform":true}`},
		{"text of only white space", agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{CustomText: "   "}), `{"behavior":"allow","answer":"","wasFreeform":true}`},
	} {
		got, err := agenttest.AnswerControl(copilotPlugin(t), payload, copilotTestRequestID, tc.decision)
		require.NoError(t, err, tc.name)
		assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":`+tc.response+`}}`, string(got.Content), tc.name)
	}

	assert.Equal(t, map[string]any{"answer": "B", "wasFreeform": false},
		copilotResolveDecision(t, payload, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"B"}})))
}

// The reader states no multiple-choice question for Copilot, so only a prompt
// built by hand reaches this. Its answer string lists the typed text after the
// choices, and the text makes it a typed answer.
func TestCopilotAnswerControlRequest_AMultipleChoiceAnswerSendsEachChoiceAndTheTypedText(t *testing.T) {
	t.Parallel()
	payload := copilotStoredFrame(t, contracts.CopilotEventUserInputRequested, map[string]any{"question": "Which ones?", "choices": []any{"A", "B", "C"}})
	prompt := &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{Questions: []*leapmuxv1.ControlQuestion{{
		Question: "Which ones?", MultiSelect: true,
		Options: []*leapmuxv1.QuestionOption{{Label: "A"}, {Label: "B"}, {Label: "C"}},
	}}}}}
	answer := func(answer *leapmuxv1.QuestionAnswer) string {
		got, err := copilotPlugin(t).AnswerControlRequest(agent.ControlAnswerInput{
			Request: agent.NewControlRequestInput(payload), RequestID: "req-1", Prompt: prompt, Decision: agenttest.QuestionDecision(answer),
		})
		require.NoError(t, err)
		return string(got.Content)
	}
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"allow","answer":"A, B, X","wasFreeform":true}}}`,
		answer(&leapmuxv1.QuestionAnswer{Selected: []string{"A", "B"}, CustomText: " X "}))
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"allow","answer":"A, B","wasFreeform":false}}}`,
		answer(&leapmuxv1.QuestionAnswer{Selected: []string{"A", "B"}, CustomText: "  "}))
}

// A refusal has no native decline: the runtime receives the user's words as
// a typed answer.
func TestCopilotAnswerControlRequest_ACancelledQuestionDeniesWithItsFeedback(t *testing.T) {
	t.Parallel()
	payload := copilotStoredFrame(t, contracts.CopilotEventUserInputRequested, map[string]any{"requestId": "native-1", "question": "Which one?", "choices": []any{"A"}})
	got, err := agenttest.AnswerControl(copilotPlugin(t), payload, copilotTestRequestID, agenttest.CancelQuestions("User stopped"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"deny","message":"User stopped"}}}`, string(got.Content))
	assert.Equal(t, map[string]any{"answer": "User stopped", "wasFreeform": true}, copilotResolveDecision(t, payload, agenttest.CancelQuestions("User stopped")))

	got, err = agenttest.AnswerControl(copilotPlugin(t), payload, copilotTestRequestID, agenttest.CancelQuestions(""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"deny","message":"Rejected by user."}}}`, string(got.Content))
}

func TestCopilotAnswerControlRequest_RefusesAQuestionAnswerThatTheRequestDoesNotTake(t *testing.T) {
	t.Parallel()
	payload := copilotStoredFrame(t, contracts.CopilotEventUserInputRequested, map[string]any{"question": "Which one?", "choices": []any{"A", "B"}})
	_, err := agenttest.AnswerControl(copilotPlugin(t), payload, copilotTestRequestID, agenttest.QuestionDecision())
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "the request takes no empty answer")
	_, err = agenttest.AnswerControl(copilotPlugin(t), payload, copilotTestRequestID, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{CustomText: "   "}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "blank text is an empty answer")
	_, err = agenttest.AnswerControl(copilotPlugin(t), payload, copilotTestRequestID, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"C"}}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "the question offers no such choice")
	_, err = agenttest.AnswerControl(copilotPlugin(t), payload, copilotTestRequestID, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"A", "B"}}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "the question takes one choice")
	_, err = agenttest.AnswerControl(copilotPlugin(t), payload, copilotTestRequestID, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"A"}, CustomText: "note"}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "the one answer is a choice or typed text, not both")
	_, err = agenttest.AnswerControl(copilotPlugin(t), payload, copilotTestRequestID, agenttest.PermissionDecision("once", ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a question takes a question decision")

	// A prompt with no question, which no reader states, answers nothing.
	_, err = copilotPlugin(t).AnswerControlRequest(agent.ControlAnswerInput{
		Request: agent.NewControlRequestInput(payload), RequestID: "req-1",
		Prompt:   &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{}}},
		Decision: agenttest.QuestionDecision(),
	})
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
}

func TestCopilotAnswerControlRequest_APlanApprovalAllowsWithItsSettings(t *testing.T) {
	t.Parallel()
	payload := copilotStoredFrame(t, contracts.CopilotEventExitPlanModeRequested, map[string]any{"requestId": "native-1", "planContent": "# Ship it", "recommendedAction": "autopilot"})
	got, err := agenttest.AnswerControl(copilotPlugin(t), payload, copilotTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{
		Approve: true, PermissionMode: "allow-all", ClearContext: true,
	}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"allow","updatedInput":{}}}}`, string(got.Content),
		"the frame states no tool input, so the approval sends an empty one, as the browser does")
	assert.True(t, proto.Equal(&leapmuxv1.PlanApprovalSettings{PermissionMode: "allow-all", ClearContext: true}, got.PlanApproval))
	assert.Equal(t, map[string]any{"approved": true, "selectedAction": "autopilot"}, copilotResolveDecision(t, payload, agenttest.PlanDecision(&leapmuxv1.PlanDecision{
		Approve: true, PermissionMode: "allow-all", ClearContext: true,
	})))

	got, err = agenttest.AnswerControl(copilotPlugin(t), payload, copilotTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Feedback: "Split step 1."}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"deny","message":"Split step 1."}}}`, string(got.Content))
	assert.Nil(t, got.PlanApproval, "a refusal applies no settings")
	assert.Equal(t, map[string]any{"approved": false, "feedback": "Split step 1."}, copilotResolveDecision(t, payload, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Feedback: "Split step 1."})))

	_, err = agenttest.AnswerControl(copilotPlugin(t), payload, copilotTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, ChoiceId: "autopilot"}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "Copilot's plan offers no choices")
}

func TestCopilotAnswerControlRequest_AnElicitationAnswersWithItsActionAndContent(t *testing.T) {
	t.Parallel()
	payload := copilotStoredFrame(t, contracts.CopilotEventElicitationRequested, map[string]any{"requestId": "native-1", "message": "Sign in", "requestedSchema": map[string]any{"type": "object"}})
	got, err := agenttest.AnswerControl(copilotPlugin(t), payload, copilotTestRequestID, agenttest.ElicitationDecision(leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT, `{"user":"ada"}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"action":"accept","content":{"user":"ada"}}}}`, string(got.Content))
	assert.Equal(t, map[string]any{"action": "accept", "content": map[string]any{"user": "ada"}},
		copilotResolveDecision(t, payload, agenttest.ElicitationDecision(leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT, `{"user":"ada"}`)))

	got, err = agenttest.AnswerControl(copilotPlugin(t), payload, copilotTestRequestID, agenttest.ElicitationDecision(leapmuxv1.ElicitationAction_ELICITATION_ACTION_DECLINE, ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"action":"decline"}}}`, string(got.Content))

	_, err = agenttest.AnswerControl(copilotPlugin(t), payload, copilotTestRequestID, agenttest.ElicitationChoiceDecision("", "always"))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "Copilot's form offers no accept choices")
}

func TestCopilotAnswerControlRequest_RefusesAPromptKindThatCopilotNeverStates(t *testing.T) {
	t.Parallel()
	_, err := copilotPlugin(t).AnswerControlRequest(agent.ControlAnswerInput{
		Request: agent.NewControlRequestInput([]byte(`{}`)), RequestID: "req-1",
		Prompt:   &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Dialog{Dialog: &leapmuxv1.DialogPrompt{Title: "T"}}},
		Decision: agenttest.DialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_CONFIRM, ""),
	})
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
}

// MCP's URL mode asks the user to visit a page, so its accept carries no
// content, and the runtime receives the bare accept. A form's accept still
// needs its content, and a URL's accept never carries one.
func TestCopilotResolveControlResponse_AURLElicitationAcceptsWithoutContent(t *testing.T) {
	t.Parallel()
	url := copilotStoredFrame(t, contracts.CopilotEventElicitationRequested, map[string]any{
		"requestId": "native-1", "mode": "url", "url": "https://example.com/auth", "message": "Sign in",
	})
	key := copilotStoredKey(t, url)
	answer, err := agenttest.AnswerControl(copilotPlugin(t), url, key, agenttest.ElicitationDecision(leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT, ""))
	require.NoError(t, err)
	resolution := agenttest.ResolveAnswerInSession(copilotPlugin(t), url, copilotTestSession, key, answer)
	require.NoError(t, resolution.Refusal(), "the accept of a URL reaches the runtime")
	assert.Contains(t, string(resolution.Content), `"action":"accept"`)
	assert.NotContains(t, string(resolution.Content), `"content"`)

	native, ok := copilotElicitationAnswer([]byte(`{"response":{"response":{"action":"accept","content":{"a":1}}}}`), contracts.MCPElicitationModeURL)
	assert.False(t, ok, "a URL's accept never carries content: %v", native)
	_, ok = copilotElicitationAnswer([]byte(`{"response":{"response":{"action":"accept"}}}`), contracts.MCPElicitationModeForm)
	assert.False(t, ok, "a form's accept needs its content")
	native, ok = copilotElicitationAnswer([]byte(`{"response":{"response":{"action":"accept","content":{"a":1}}}}`), contracts.MCPElicitationModeForm)
	assert.True(t, ok)
	assert.Equal(t, map[string]any{"action": "accept", "content": json.RawMessage(`{"a":1}`)}, native)
}
