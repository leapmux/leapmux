package kimi

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

// kimiTestRequestID is the stored id of the request that each test answers.
const kimiTestRequestID = "req-1"

// kimiStoredUnder is the stored request, with its interaction id replaced by
// requestID: the key that the request then gives, and so the key that the
// Worker stores it under.
func kimiStoredUnder(t *testing.T, stored, requestID string) []byte {
	t.Helper()
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(stored), &fields))
	encoded, err := json.Marshal(requestID)
	require.NoError(t, err)
	fields["interaction_id"] = encoded
	payload, err := json.Marshal(fields)
	require.NoError(t, err)
	return payload
}

// kimiApprovalPayload builds one pending approval, as the control surface
// receives it. fields replace the frame's own fields, such as agent_id, and
// requestFields replace the nested request's.
func kimiApprovalPayload(t *testing.T, toolName string, display any, fields, requestFields map[string]any) []byte {
	t.Helper()
	request := map[string]any{"tool_name": toolName, "action": "Running: " + toolName, "tool_input_display": display}
	for key, value := range requestFields {
		if value == nil {
			delete(request, key)
			continue
		}
		request[key] = value
	}
	frame := map[string]any{
		"type": "interaction", "kind": "approval", "status": "pending", "interaction_id": "approval_1",
		"session_id": "session_1", "agent_id": "main", "tool_call_id": "call_1", "request": request,
	}
	for key, value := range fields {
		frame[key] = value
	}
	payload, err := json.Marshal(frame)
	require.NoError(t, err)
	return payload
}

// kimiQuestionPayload builds one pending question request.
func kimiQuestionPayload(t *testing.T, questions any) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"type": "interaction", "kind": "question", "status": "pending", "interaction_id": "question_1",
		"session_id": "session_1", "agent_id": "main", "tool_call_id": "call_ask", "request": map[string]any{"questions": questions},
	})
	require.NoError(t, err)
	return payload
}

// kimiSentResponse reads the inner response of an answer.
func kimiSentResponse(t *testing.T, content []byte) map[string]any {
	t.Helper()
	var envelope struct {
		Type     string `json:"type"`
		Response struct {
			Subtype   string         `json:"subtype"`
			RequestID string         `json:"request_id"`
			Response  map[string]any `json:"response"`
		} `json:"response"`
	}
	require.NoError(t, json.Unmarshal(content, &envelope))
	require.Equal(t, "control_response", envelope.Type)
	require.Equal(t, "success", envelope.Response.Subtype)
	require.Equal(t, "req-1", envelope.Response.RequestID)
	return envelope.Response.Response
}

var (
	kimiAllowOnce   = leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE
	kimiAllowAlways = leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS
	kimiRejectOnce  = leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE
)

func kimiThreeAnswers() []*leapmuxv1.PermissionOption {
	return []*leapmuxv1.PermissionOption{
		{OptionId: "approve", Kind: kimiAllowOnce, Name: "Allow"},
		{OptionId: "approve_for_session", Kind: kimiAllowAlways, Name: "Allow for this session"},
		{OptionId: "reject", Kind: kimiRejectOnce, Name: "Deny"},
	}
}

func TestKimiReadControlRequest_ReadsACommandApprovalAsAPermissionWithTheThreeAnswers(t *testing.T) {
	t.Parallel()
	got := agenttest.ControlPromptOf(kimiProvider{}, kimiApprovalPayload(t, contracts.KimiToolBash, map[string]any{
		"kind": "command", "command": "rm -rf build", "cwd": "/work", "language": "bash",
	}, nil, nil))
	permission := got.GetPermission()
	require.NotNil(t, permission)
	assert.JSONEq(t, `{"kind":"command","command":"rm -rf build","cwd":"/work","language":"bash"}`, permission.GetInputJson(),
		"the display is the whole statement of the call")
	permission.InputJson = ""
	agenttest.RequireProto(t, agenttest.PermissionPrompt(&leapmuxv1.PermissionPrompt{
		Title:            "Bash",
		Reason:           "Running: Bash",
		Command:          "rm -rf build",
		WorkingDirectory: "/work",
		Options:          kimiThreeAnswers(),
	}), got)
}

func TestKimiReadControlRequest_ReadsTheNestedRequestOfANativeFrame(t *testing.T) {
	t.Parallel()
	got := agenttest.ControlPromptOf(kimiProvider{}, kimiApprovalPayload(t, "Bash", map[string]any{"kind": "command", "command": "pwd", "cwd": "/work"}, nil, map[string]any{"action": "Run pwd"}))
	assert.Equal(t, "Bash", got.GetPermission().GetTitle())
	assert.Equal(t, "pwd", got.GetPermission().GetCommand())
	assert.Equal(t, "/work", got.GetPermission().GetWorkingDirectory())
	assert.Equal(t, "Run pwd", got.GetPermission().GetReason())
}

// The display of a command states its working directory and the model's
// description, and the arguments keep both.
func TestKimiReadControlRequest_KeepsTheWholeDisplayAsTheArguments(t *testing.T) {
	t.Parallel()
	got := agenttest.ControlPromptOf(kimiProvider{}, kimiApprovalPayload(t, "Bash", map[string]any{
		"kind": "command", "command": "ls", "cwd": "/workspace", "description": "Inspect the command.", "language": "bash",
	}, nil, nil))
	assert.Contains(t, got.GetPermission().GetInputJson(), "/workspace")
	assert.Contains(t, got.GetPermission().GetInputJson(), "Inspect the command.")
}

func TestKimiReadControlRequest_ReadsAPlanReviewAsThePlanApprovalWithItsChoices(t *testing.T) {
	t.Parallel()
	got := agenttest.ControlPromptOf(kimiProvider{}, kimiApprovalPayload(t, contracts.KimiToolExitPlanMode, map[string]any{
		"kind": contracts.KimiDisplayPlanReview, "plan": "# Plan\n- A\n- B",
		"options": []any{
			map[string]any{"label": "Option A", "description": "Simple"},
			map[string]any{"label": "Option B"},
			map[string]any{"description": "no label"},
			"not an option",
		},
	}, nil, nil))
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: &leapmuxv1.PlanPrompt{
		Text: "# Plan\n- A\n- B",
		Choices: []*leapmuxv1.PlanChoice{
			{Id: "Option A", Label: "Approve: Option A", Description: "Simple", Effect: leapmuxv1.PlanChoiceEffect_PLAN_CHOICE_EFFECT_APPROVE},
			{Id: "Option B", Label: "Approve: Option B", Effect: leapmuxv1.PlanChoiceEffect_PLAN_CHOICE_EFFECT_APPROVE},
			{Id: "Revise", Label: "Request revisions", Effect: leapmuxv1.PlanChoiceEffect_PLAN_CHOICE_EFFECT_REFUSE},
			{Id: "Reject and Exit", Label: "Reject and exit plan mode", Effect: leapmuxv1.PlanChoiceEffect_PLAN_CHOICE_EFFECT_REFUSE},
		},
	}}}, got)
}

func TestKimiReadControlRequest_ReadsAPlanReviewWithNoPlanTextAndNoOptions(t *testing.T) {
	t.Parallel()
	for _, display := range []map[string]any{
		{"kind": contracts.KimiDisplayPlanReview, "plan": " "},
		{"kind": contracts.KimiDisplayPlanReview, "options": "not a list"},
	} {
		agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: &leapmuxv1.PlanPrompt{
			Choices: []*leapmuxv1.PlanChoice{
				{Id: "Revise", Label: "Request revisions", Effect: leapmuxv1.PlanChoiceEffect_PLAN_CHOICE_EFFECT_REFUSE},
				{Id: "Reject and Exit", Label: "Reject and exit plan mode", Effect: leapmuxv1.PlanChoiceEffect_PLAN_CHOICE_EFFECT_REFUSE},
			},
		}}}, agenttest.ControlPromptOf(kimiProvider{}, kimiApprovalPayload(t, contracts.KimiToolExitPlanMode, display, nil, nil)))
	}
}

// A subagent's plan review is the subagent's own. The plan approval would
// leave the MAIN agent's plan mode, so the review reads as a permission with
// the plan's answers.
func TestKimiReadControlRequest_ReadsASubagentPlanReviewAsAPermissionWithThePlanAndItsAnswers(t *testing.T) {
	t.Parallel()
	got := agenttest.ControlPromptOf(kimiProvider{}, kimiApprovalPayload(t, contracts.KimiToolExitPlanMode, map[string]any{
		"kind": contracts.KimiDisplayPlanReview, "plan": "# A subagent plan",
		"options": []any{map[string]any{"label": "Option A", "description": "Simple"}, map[string]any{"label": "Option B"}},
	}, map[string]any{"agent_id": "agent-0"}, nil))
	permission := got.GetPermission()
	require.NotNil(t, permission)
	assert.JSONEq(t, `{"kind":"plan_review","options":[{"label":"Option A","description":"Simple"},{"label":"Option B"}]}`, permission.GetInputJson(),
		"the plan reads as text, and the arguments keep what the text does not state")
	permission.InputJson = ""
	agenttest.RequireProto(t, agenttest.PermissionPrompt(&leapmuxv1.PermissionPrompt{
		Title:  "ExitPlanMode",
		Reason: "Running: ExitPlanMode",
		Text:   "# A subagent plan",
		Options: []*leapmuxv1.PermissionOption{
			{OptionId: "approve", Kind: kimiAllowOnce, Name: "Approve the plan"},
			{OptionId: "plan_approve:Option A", Kind: kimiAllowOnce, Name: "Approve: Option A"},
			{OptionId: "plan_approve:Option B", Kind: kimiAllowOnce, Name: "Approve: Option B"},
			{OptionId: "reject", Kind: kimiRejectOnce, Name: "Reject"},
			{OptionId: "plan_reject:Revise", Kind: kimiRejectOnce, Name: "Request revisions"},
			{OptionId: "plan_reject:Reject and Exit", Kind: kimiRejectOnce, Name: "Reject and exit plan mode"},
		},
	}), got)
}

func TestKimiReadControlRequest_KeepsTheWholeDisplayOfASubagentPlanThatStatesNoText(t *testing.T) {
	t.Parallel()
	got := agenttest.ControlPromptOf(kimiProvider{}, kimiApprovalPayload(t, contracts.KimiToolExitPlanMode, map[string]any{
		"kind": contracts.KimiDisplayPlanReview, "plan": "  ", "options": []any{map[string]any{"label": "Option A"}},
	}, map[string]any{"agent_id": "agent-0"}, nil))
	assert.Empty(t, got.GetPermission().GetText())
	assert.JSONEq(t, `{"kind":"plan_review","plan":"  ","options":[{"label":"Option A"}]}`, got.GetPermission().GetInputJson())
}

func TestKimiReadControlRequest_ReadsTheAgentTagAndDefaultsAnAbsentTagToTheMainAgent(t *testing.T) {
	t.Parallel()
	display := map[string]any{"kind": contracts.KimiDisplayPlanReview, "plan": "# Plan"}
	assert.NotNil(t, agenttest.ControlPromptOf(kimiProvider{}, kimiApprovalPayload(t, contracts.KimiToolExitPlanMode, display, map[string]any{"agent_id": "agent-1"}, nil)).GetPermission())
	for _, tag := range []any{"", 7, nil} {
		assert.NotNil(t, agenttest.ControlPromptOf(kimiProvider{}, kimiApprovalPayload(t, contracts.KimiToolExitPlanMode, display, map[string]any{"agent_id": tag}, nil)).GetPlan(), "%v", tag)
	}
}

func TestKimiReadControlRequest_ReadsAGoalStartAsAPermissionWhoseOptionsPickTheMode(t *testing.T) {
	t.Parallel()
	got := agenttest.ControlPromptOf(kimiProvider{}, kimiApprovalPayload(t, contracts.KimiToolCreateGoal, map[string]any{"kind": contracts.KimiDisplayGoalStart, "objective": "Ship it", "mode": "manual"}, nil, nil))
	agenttest.RequireProto(t, agenttest.PermissionPrompt(&leapmuxv1.PermissionPrompt{Options: []*leapmuxv1.PermissionOption{
		{OptionId: "approve", Kind: kimiAllowOnce, Name: "Start the goal"},
		{OptionId: "goal_mode:manual", Kind: kimiAllowOnce, Name: "Start in Always Ask"},
		{OptionId: "goal_mode:yolo", Kind: kimiAllowOnce, Name: "Start in Ask When Needed"},
		{OptionId: "goal_mode:auto", Kind: kimiAllowOnce, Name: "Start in Never Ask"},
		{OptionId: "reject", Kind: kimiRejectOnce, Name: "Decline"},
	}}), agenttest.PermissionPrompt(&leapmuxv1.PermissionPrompt{Options: got.GetPermission().GetOptions()}))
}

// The server words every approval through its display, and a bare one states
// only the tool that it asks about.
func TestKimiReadControlRequest_ReadsAnApprovalWithNoDisplayAndNoActionAsABarePermission(t *testing.T) {
	t.Parallel()
	for _, display := range []any{nil, "not an object"} {
		payload := kimiApprovalPayload(t, contracts.KimiToolWrite, display, nil, map[string]any{"action": ""})
		agenttest.RequireProto(t, agenttest.PermissionPrompt(&leapmuxv1.PermissionPrompt{Title: "Write", Options: kimiThreeAnswers()}), agenttest.ControlPromptOf(kimiProvider{}, payload))
	}
	payload := kimiApprovalPayload(t, contracts.KimiToolWrite, nil, nil, map[string]any{"tool_input_display": nil, "action": nil})
	agenttest.RequireProto(t, agenttest.PermissionPrompt(&leapmuxv1.PermissionPrompt{Title: "Write", Options: kimiThreeAnswers()}), agenttest.ControlPromptOf(kimiProvider{}, payload))
}

func TestKimiReadControlRequest_ReadsTheQuestionsOfAQuestionRequest(t *testing.T) {
	t.Parallel()
	got := agenttest.ControlPromptOf(kimiProvider{}, kimiQuestionPayload(t, []any{
		map[string]any{
			"id": "q_0", "question": "Which color?", "header": "Color",
			"options":      []any{map[string]any{"id": "opt_0_0", "label": "Red", "description": "Warm"}, map[string]any{"id": "opt_0_1", "label": "Blue"}},
			"multi_select": false, "allow_other": true,
		},
		map[string]any{
			"id": "q_1", "question": "Which sizes?", "header": "Which sizes?",
			"options":      []any{map[string]any{"id": "opt_1_0", "label": "S"}, map[string]any{"id": "opt_1_1", "label": "M"}},
			"multi_select": true,
		},
		map[string]any{"id": "", "question": "No id"},
		map[string]any{"id": "q_3", "question": "", "header": ""},
		map[string]any{"id": "q_4", "question": "Broken options", "options": []any{map[string]any{"id": "opt_4_0"}, map[string]any{"label": "no id"}, "x"}},
		map[string]any{"id": "q_5", "question": "", "header": "Pick a color"},
		map[string]any{"id": "q_6", "question": "Q", "multi_select": "true"},
		"not a question",
		nil,
	}))
	value := func(v string) *string { return &v }
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
		Questions: []*leapmuxv1.ControlQuestion{
			{Id: "q_0", Question: "Which color?", Header: "Color", AllowEmpty: true, Options: []*leapmuxv1.QuestionOption{
				{Value: value("opt_0_0"), Label: "Red", Description: "Warm"},
				{Value: value("opt_0_1"), Label: "Blue"},
			}},
			{Id: "q_1", Question: "Which sizes?", MultiSelect: true, AllowEmpty: true, Options: []*leapmuxv1.QuestionOption{
				{Value: value("opt_1_0"), Label: "S"},
				{Value: value("opt_1_1"), Label: "M"},
			}},
			{Id: "q_4", Question: "Broken options", AllowEmpty: true},
			{Id: "q_5", Question: "Pick a color", AllowEmpty: true},
			{Id: "q_6", Question: "Q", AllowEmpty: true},
		},
	}}}, got)
}

func TestKimiReadControlRequest_ReadsNoQuestionFromAListThatIsNoList(t *testing.T) {
	t.Parallel()
	got := agenttest.ControlPromptOf(kimiProvider{}, kimiQuestionPayload(t, map[string]any{"id": "q_0", "question": "Q"}))
	require.NotNil(t, got.GetQuestion())
	assert.Empty(t, got.GetQuestion().GetQuestions())
}

// A frame that is neither a question nor an approval is no Kimi request, and
// so is a frame whose request is no object. The resolution refuses an answer
// to either, so it reads as unrecognized.
func TestKimiReadControlRequest_ReadsAnyOtherPayloadAsUnrecognized(t *testing.T) {
	t.Parallel()
	for _, payload := range []string{
		`{}`,
		`{"type":"event.approval.requested"}`,
		`{"type":"interaction","kind":"approval","request":"not an object"}`,
		`{"type":"interaction","kind":"question","request":null}`,
		`{"type":"interaction","kind":"other","request":{}}`,
		`not json`,
	} {
		agenttest.RequireProto(t, agent.UnrecognizedControlPrompt(), agenttest.ControlPromptOf(kimiProvider{}, []byte(payload)), payload)
	}
}

func kimiTrustQuestion(t *testing.T) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		contracts.WorkspaceTrustFieldType:          contracts.WorkspaceTrustControlTypeQuestion,
		contracts.WorkspaceTrustFieldPath:          "/work/project",
		contracts.WorkspaceTrustFieldConfiguration: []any{"MCP servers", "hooks"},
	})
	require.NoError(t, err)
	return payload
}

func TestKimiReadControlRequest_DrawsTheWorkspaceTrustQuestionWithTheAgentName(t *testing.T) {
	t.Parallel()
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{
		Title:  "Trust the folder /work/project",
		Reason: "Kimi Code loads the MCP servers and hooks of a project only from a folder that you trust, and it keeps your answer for the folder.",
		Options: []*leapmuxv1.PermissionOption{
			{OptionId: "workspace-trust", Kind: kimiAllowOnce, Name: "Trust this folder"},
			{OptionId: "workspace-reject", Kind: kimiRejectOnce, Name: "Do not trust"},
		},
	}}, EditorPurpose: leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_NONE}, agenttest.ControlPromptOf(kimiProvider{}, kimiTrustQuestion(t)))
}

func TestKimiAnswerControlRequest_SendsEachPermissionAnswerInTheNeutralEnvelope(t *testing.T) {
	t.Parallel()
	command := kimiApprovalPayload(t, "Bash", map[string]any{"kind": "command", "command": "ls"}, nil, nil)
	goal := kimiApprovalPayload(t, contracts.KimiToolCreateGoal, map[string]any{"kind": contracts.KimiDisplayGoalStart, "objective": "Ship it"}, nil, nil)
	subagentPlan := kimiApprovalPayload(t, contracts.KimiToolExitPlanMode, map[string]any{
		"kind": contracts.KimiDisplayPlanReview, "plan": "# P", "options": []any{map[string]any{"label": "Option A"}, map[string]any{"label": "Option B"}},
	}, map[string]any{"agent_id": "agent-0"}, nil)
	for _, tc := range []struct {
		payload  []byte
		option   string
		response map[string]any
	}{
		{command, "approve", map[string]any{"behavior": "allow"}},
		{command, "approve_for_session", map[string]any{"behavior": "allow", "scope": "session"}},
		{command, "reject", map[string]any{"behavior": "deny", "message": "Rejected by user."}},
		{goal, "approve", map[string]any{"behavior": "allow"}},
		{goal, "goal_mode:auto", map[string]any{"behavior": "allow", "choice": "auto"}},
		{goal, "goal_mode:yolo", map[string]any{"behavior": "allow", "choice": "yolo"}},
		{goal, "reject", map[string]any{"behavior": "deny", "message": "Rejected by user."}},
		{subagentPlan, "plan_approve:Option B", map[string]any{"behavior": "allow", "choice": "Option B"}},
		{subagentPlan, "plan_reject:Revise", map[string]any{"behavior": "deny", "message": "Rejected by user.", "choice": "Revise"}},
		{subagentPlan, "plan_reject:Reject and Exit", map[string]any{"behavior": "deny", "message": "Rejected by user.", "choice": "Reject and Exit"}},
	} {
		got, err := agenttest.AnswerControl(kimiProvider{}, tc.payload, kimiTestRequestID, agenttest.PermissionDecision(tc.option, ""))
		require.NoError(t, err, tc.option)
		assert.Equal(t, tc.response, kimiSentResponse(t, got.Content), tc.option)
		assert.Nil(t, got.PlanApproval, tc.option)
		assert.Empty(t, got.FollowUp, tc.option)
	}
}

// Three permission answers, five goal answers, and five subagent plan answers:
// each allow option allows, and each reject option denies.
func TestKimiAnswerControlRequest_SendsEveryOptionThatEachKimiControlOffers(t *testing.T) {
	t.Parallel()
	payloads := [][]byte{
		kimiApprovalPayload(t, "Bash", map[string]any{"kind": "command", "command": "ls"}, nil, nil),
		kimiApprovalPayload(t, contracts.KimiToolCreateGoal, map[string]any{"kind": contracts.KimiDisplayGoalStart, "objective": "Ship it"}, nil, nil),
		kimiApprovalPayload(t, contracts.KimiToolExitPlanMode, map[string]any{"kind": contracts.KimiDisplayPlanReview, "plan": "# P", "options": []any{map[string]any{"label": "Option A"}}}, map[string]any{"agent_id": "agent-0"}, nil),
	}
	count := 0
	for _, payload := range payloads {
		for _, option := range agenttest.ControlPromptOf(kimiProvider{}, payload).GetPermission().GetOptions() {
			count++
			got, err := agenttest.AnswerControl(kimiProvider{}, payload, kimiTestRequestID, agenttest.PermissionDecision(option.GetOptionId(), ""))
			require.NoError(t, err, option.GetOptionId())
			want := contracts.ControlAnswerBehaviorDeny
			if agent.IsAllowOption(option) {
				want = contracts.ControlAnswerBehaviorAllow
			}
			assert.Equal(t, want, kimiSentResponse(t, got.Content)["behavior"], option.GetOptionId())
		}
	}
	assert.Equal(t, 13, count)
}

// The answer must be the bytes that the provider's resolution reads.
func TestKimiAnswerControlRequest_EachPermissionAnswerResolvesToTheServerBody(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		stored   string
		option   string
		feedback string
		body     map[string]any
	}{
		{kimiStoredApproval, "approve", "", map[string]any{"kind": "approval", "decision": "approved"}},
		{kimiStoredApproval, "approve_for_session", "", map[string]any{"kind": "approval", "decision": "approved", "scope": "session"}},
		{kimiStoredApproval, "reject", "", map[string]any{"kind": "approval", "decision": "rejected"}},
		{kimiStoredApproval, "reject", "Use rg instead.", map[string]any{"kind": "approval", "decision": "rejected", "feedback": "Use rg instead."}},
		{kimiStoredGoal, "goal_mode:yolo", "", map[string]any{"kind": "approval", "decision": "approved", "selected_label": "yolo", "permission_mode": "yolo"}},
		{kimiStoredSubagentPlan, "plan_approve:Approach B", "", map[string]any{"kind": "approval", "decision": "approved", "selected_label": "Approach B"}},
		{kimiStoredSubagentPlan, "plan_reject:Revise", "Split step 1.", map[string]any{"kind": "approval", "decision": "rejected", "selected_label": "Revise", "feedback": "Split step 1."}},
	} {
		// Each request is stored under its own interaction id.
		requestID, ok := kimiProvider{}.ControlRequestKey([]byte(tc.stored), "")
		require.True(t, ok, tc.option)
		got, err := agenttest.AnswerControl(kimiProvider{}, []byte(tc.stored), requestID, agenttest.PermissionDecision(tc.option, tc.feedback))
		require.NoError(t, err, tc.option)
		res := agenttest.ResolveAnswer(kimiProvider{}, []byte(tc.stored), requestID, got)
		require.False(t, res.Withhold, tc.option)
		assert.Equal(t, tc.body, nativeResponse(t, res.Content), tc.option)
	}
}

// The composer's send is a refusal, and its text rides the refusal as the
// reason that the server hands the model.
func TestKimiAnswerControlRequest_ARefusalWithFeedbackDeniesWithIt(t *testing.T) {
	t.Parallel()
	payload := kimiApprovalPayload(t, "Bash", map[string]any{"kind": "command", "command": "ls"}, nil, nil)
	got, err := agenttest.AnswerControl(kimiProvider{}, payload, kimiTestRequestID, agenttest.PermissionDecision("reject", "Use rg."))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"deny","message":"Use rg."}}}`, string(got.Content))
	assert.Empty(t, got.FollowUp)

	subagentPlan := kimiApprovalPayload(t, contracts.KimiToolExitPlanMode, map[string]any{"kind": contracts.KimiDisplayPlanReview, "plan": "# P"}, map[string]any{"agent_id": "agent-0"}, nil)
	got, err = agenttest.AnswerControl(kimiProvider{}, subagentPlan, kimiTestRequestID, agenttest.PermissionDecision("plan_reject:Revise", "Split step 1."))
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"behavior": "deny", "message": "Split step 1.", "choice": "Revise"}, kimiSentResponse(t, got.Content),
		"the label stays beside the reason, which states what to revise")
}

func TestKimiAnswerControlRequest_RefusesAPermissionDecisionThatThePromptDoesNotOffer(t *testing.T) {
	t.Parallel()
	command := kimiApprovalPayload(t, "Bash", map[string]any{"kind": "command", "command": "ls"}, nil, nil)
	// The option IDs are LeapMux's own, so an ID that no Kimi control offers
	// must never read as an approval.
	for _, option := range []string{"", "rejec", "allow", "approve_once", "goal_mode:auto", "plan_reject:Revise"} {
		_, err := agenttest.AnswerControl(kimiProvider{}, command, kimiTestRequestID, agenttest.PermissionDecision(option, ""))
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, option)
	}
	for _, option := range []string{"approve", "approve_for_session"} {
		_, err := agenttest.AnswerControl(kimiProvider{}, command, kimiTestRequestID, agenttest.PermissionDecision(option, "Why?"))
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "feedback goes with a refusal: %s", option)
	}
	goal := kimiApprovalPayload(t, contracts.KimiToolCreateGoal, map[string]any{"kind": contracts.KimiDisplayGoalStart}, nil, nil)
	_, err := agenttest.AnswerControl(kimiProvider{}, goal, kimiTestRequestID, agenttest.PermissionDecision("goal_mode:plan", ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a goal starts in no plan mode")
	_, err = agenttest.AnswerControl(kimiProvider{}, command, kimiTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a permission takes no plan decision")
}

// The main agent's plan approval answers through the shared plan row: the
// plain approve, a choice that approves or refuses, and the composer's typed
// refusal.
func TestKimiAnswerControlRequest_AnswersThePlanApprovalAndItsChoices(t *testing.T) {
	t.Parallel()
	payload := kimiStoredUnder(t, kimiStoredPlan, kimiTestRequestID)
	got, err := agenttest.AnswerControl(kimiProvider{}, payload, kimiTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, PermissionMode: contracts.KimiModeYolo}))
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"behavior": "allow", "updatedInput": map[string]any{}}, kimiSentResponse(t, got.Content))
	assert.True(t, proto.Equal(&leapmuxv1.PlanApprovalSettings{PermissionMode: contracts.KimiModeYolo}, got.PlanApproval))
	res := agenttest.ResolveAnswer(kimiProvider{}, payload, kimiTestRequestID, got)
	assert.Equal(t, map[string]any{"kind": "approval", "decision": "approved", "permission_mode": "yolo"}, nativeResponse(t, res.Content))

	got, err = agenttest.AnswerControl(kimiProvider{}, payload, kimiTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, ChoiceId: "Approach A"}))
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"behavior": "allow", "updatedInput": map[string]any{}, "choice": "Approach A"}, kimiSentResponse(t, got.Content))
	assert.NotNil(t, got.PlanApproval, "an approving choice carries the plan settings, as Approve does")

	got, err = agenttest.AnswerControl(kimiProvider{}, payload, kimiTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{ChoiceId: "Reject and Exit"}))
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"behavior": "deny", "message": "Rejected by user.", "choice": "Reject and Exit"}, kimiSentResponse(t, got.Content))
	assert.Nil(t, got.PlanApproval, "a refusal applies no settings")
	res = agenttest.ResolveAnswer(kimiProvider{}, payload, kimiTestRequestID, got)
	assert.Equal(t, map[string]any{"kind": "approval", "decision": "rejected", "selected_label": "Reject and Exit"}, nativeResponse(t, res.Content))

	got, err = agenttest.AnswerControl(kimiProvider{}, payload, kimiTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Feedback: "Split step 1."}))
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"behavior": "deny", "message": "Split step 1."}, kimiSentResponse(t, got.Content))

	_, err = agenttest.AnswerControl(kimiProvider{}, payload, kimiTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, ChoiceId: "Revise"}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "Revise refuses the plan")
	_, err = agenttest.AnswerControl(kimiProvider{}, payload, kimiTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, ChoiceId: "Approach C"}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "the plan offers no such approach")
}

func TestKimiAnswerControlRequest_AnswersEveryQuestionByItsID(t *testing.T) {
	t.Parallel()
	payload := kimiQuestionPayload(t, []any{
		map[string]any{"id": "q_0", "question": "Which color?", "options": []any{map[string]any{"id": "opt_0_0", "label": "Red"}, map[string]any{"id": "opt_0_1", "label": "Blue"}}},
		map[string]any{"id": "q_1", "question": "Which sizes?", "options": []any{map[string]any{"id": "opt_1_0", "label": "S"}, map[string]any{"id": "opt_1_1", "label": "M"}}, "multi_select": true},
		map[string]any{"id": "q_2", "question": "Anything else?"},
	})
	got, err := agenttest.AnswerControl(kimiProvider{}, payload, "question_1", agenttest.QuestionDecision(
		&leapmuxv1.QuestionAnswer{Selected: []string{"opt_0_1"}},
		&leapmuxv1.QuestionAnswer{Selected: []string{"opt_1_0", "opt_1_1"}, CustomText: "XL"},
	))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"question_1","response":{"behavior":"allow","answers":{
		"q_0":{"kind":"single","option_id":"opt_0_1"},
		"q_1":{"kind":"multi_with_other","option_ids":["opt_1_0","opt_1_1"],"other_text":"XL"},
		"q_2":{"kind":"skipped"}
	}}}}`, string(got.Content))
	assert.Empty(t, got.FollowUp)

	res := agenttest.ResolveAnswer(kimiProvider{}, payload, "question_1", got)
	require.False(t, res.Withhold)
	assert.Equal(t, "question", nativeResponse(t, res.Content)["kind"])
}

func TestKimiAnswerControlRequest_AnswersTheSelectedOptionIDsOfASingleQuestion(t *testing.T) {
	t.Parallel()
	got, err := agenttest.AnswerControl(kimiProvider{}, []byte(kimiStoredQuestion), kimiTestRequestID, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"opt_0_1"}}))
	require.NoError(t, err)
	assert.Equal(t, map[string]any{
		"behavior": "allow",
		"answers": map[string]any{
			"q_0": map[string]any{"kind": "single", "option_id": "opt_0_1"},
			"q_1": map[string]any{"kind": "skipped"},
		},
	}, kimiSentResponse(t, got.Content))
}

// A refusal dismisses the question. The resolution, not the answer, sends its
// text as the user's next message, so the answer states no follow-up of its
// own and the text is sent once.
func TestKimiAnswerControlRequest_StopsAQuestionAsARefusalThatCarriesTheReason(t *testing.T) {
	t.Parallel()
	got, err := agenttest.AnswerControl(kimiProvider{}, kimiStoredUnder(t, kimiStoredQuestion, kimiTestRequestID), kimiTestRequestID, agenttest.CancelQuestions("User stopped"))
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"behavior": "deny", "message": "User stopped"}, kimiSentResponse(t, got.Content))
	assert.Empty(t, got.FollowUp)
	res := agenttest.ResolveAnswer(kimiProvider{}, kimiStoredUnder(t, kimiStoredQuestion, kimiTestRequestID), kimiTestRequestID, got)
	assert.Equal(t, map[string]any{"kind": "question", "dismissed": true}, nativeResponse(t, res.Content))
	assert.Equal(t, agent.FeedbackFollowUp("User stopped"), res.FollowUp)

	got, err = agenttest.AnswerControl(kimiProvider{}, kimiStoredUnder(t, kimiStoredQuestion, kimiTestRequestID), kimiTestRequestID, agenttest.CancelQuestions(""))
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"behavior": "deny", "message": "Rejected by user."}, kimiSentResponse(t, got.Content))
}

func TestKimiAnswerControlRequest_RefusesAQuestionAnswerThatTheRequestDoesNotOffer(t *testing.T) {
	t.Parallel()
	payload := []byte(kimiStoredQuestion)
	for _, answers := range [][]*leapmuxv1.QuestionAnswer{
		{{Selected: []string{"Red"}}},
		{{Selected: []string{"opt_0_0", "opt_0_1"}}},
		{{Selected: []string{"opt_0_0"}, CustomText: "Green"}},
		{{}, {}, {}},
	} {
		_, err := agenttest.AnswerControl(kimiProvider{}, payload, kimiTestRequestID, agenttest.QuestionDecision(answers...))
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "%v", answers)
	}
	_, err := agenttest.AnswerControl(kimiProvider{}, payload, kimiTestRequestID, agenttest.PermissionDecision("approve", ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a question takes a question decision")
}

// A question with no ID has no answer that the resolution can check, so the
// answer leaves it out. The reader drops such a question, so only a prompt
// built by hand reaches this.
func TestKimiAnswerControlRequest_AnswersNoQuestionThatStatesNoID(t *testing.T) {
	t.Parallel()
	payload := kimiQuestionPayload(t, []any{})
	got, err := kimiProvider{}.AnswerControlRequest(agent.ControlAnswerInput{
		Request: agent.NewControlRequestInput(payload), RequestID: "req-1",
		Prompt: &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{Questions: []*leapmuxv1.ControlQuestion{
			{Question: "No id", Options: []*leapmuxv1.QuestionOption{{Label: "A"}}},
			{Id: "q_1", Question: "Q"},
		}}}},
		Decision: agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"A"}}, &leapmuxv1.QuestionAnswer{CustomText: "Mine"}),
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"behavior": "allow", "answers": map[string]any{"q_1": map[string]any{"kind": "other", "text": "Mine"}}}, kimiSentResponse(t, got.Content))
}

// The browser sends nothing for a request of no question, so only a cancel
// answers one: an answer would send Kimi an empty answer map, which its
// resolver withholds.
func TestKimiAnswerControlRequest_RefusesAnAnswerToARequestWithNoQuestions(t *testing.T) {
	t.Parallel()
	_, err := agenttest.AnswerControl(kimiProvider{}, kimiQuestionPayload(t, []any{}), kimiTestRequestID, agenttest.QuestionDecision())
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
}

func TestKimiAnswer_AnswersEachKindThatTheServerTakes(t *testing.T) {
	t.Parallel()
	a, b := "a", "b"
	single := &leapmuxv1.ControlQuestion{Id: "q_0", Question: "Q", Options: []*leapmuxv1.QuestionOption{{Value: &a, Label: "A"}, {Value: &b, Label: "B"}}}
	multi := proto.Clone(single).(*leapmuxv1.ControlQuestion)
	multi.MultiSelect = true
	for _, tc := range []struct {
		name     string
		question *leapmuxv1.ControlQuestion
		selected []string
		typed    string
		want     map[string]any
	}{
		{"a single choice", single, []string{"a"}, "", map[string]any{"kind": "single", "option_id": "a"}},
		{"typed text", single, nil, " Green ", map[string]any{"kind": "other", "text": "Green"}},
		{"several choices", multi, []string{"a", "b"}, "", map[string]any{"kind": "multi", "option_ids": []string{"a", "b"}}},
		{"choices and text", multi, []string{"a"}, "c", map[string]any{"kind": "multi_with_other", "option_ids": []string{"a"}, "other_text": "c"}},
		// The user chose both options AND the text, so all three reach the server.
		{"two choices and text", multi, []string{"a", "b"}, " X ", map[string]any{"kind": "multi_with_other", "option_ids": []string{"a", "b"}, "other_text": "X"}},
		{"choices and blank text", multi, []string{"a", "b"}, "  ", map[string]any{"kind": "multi", "option_ids": []string{"a", "b"}}},
		{"text alone on a multiple choice", multi, nil, "c", map[string]any{"kind": "other", "text": "c"}},
		{"blank text", single, nil, "  ", map[string]any{"kind": "skipped"}},
		// QuestionDecisionOf refuses a choice beside text for a single choice.
		// When both still reach this, the choice is the answer.
		{"a choice beside text", single, []string{"a"}, "typed", map[string]any{"kind": "single", "option_id": "a"}},
		// A selection that the question does not offer is dropped.
		{"an unknown choice", single, []string{"z"}, "", map[string]any{"kind": "skipped"}},
		{"an unknown choice before a known one", single, []string{"z", "b"}, "", map[string]any{"kind": "single", "option_id": "b"}},
		{"an unknown choice among several", multi, []string{"z", "b"}, "", map[string]any{"kind": "multi", "option_ids": []string{"b"}}},
		{"an unknown choice beside text", multi, []string{"z"}, "typed", map[string]any{"kind": "other", "text": "typed"}},
		{"a question with no options", &leapmuxv1.ControlQuestion{Id: "q_0", Question: "Q"}, nil, "", map[string]any{"kind": "skipped"}},
		{"a choice of a question with no options", &leapmuxv1.ControlQuestion{Id: "q_0", Question: "Q", MultiSelect: true}, []string{"x"}, "", map[string]any{"kind": "skipped"}},
		// An option that states no value sends its label.
		{"a choice by its label", &leapmuxv1.ControlQuestion{Id: "q_0", Question: "Q", Options: []*leapmuxv1.QuestionOption{{Label: "A"}}}, []string{"A"}, "", map[string]any{"kind": "single", "option_id": "A"}},
	} {
		assert.Equal(t, tc.want, kimiAnswer(tc.question, &leapmuxv1.QuestionAnswer{Selected: tc.selected, CustomText: tc.typed}), tc.name)
	}
}

func TestKimiAnswerControlRequest_AnswersTheWorkspaceTrustQuestion(t *testing.T) {
	t.Parallel()
	payload := kimiTrustQuestion(t)
	for option, response := range map[string]map[string]any{
		"workspace-trust":  {"behavior": "allow"},
		"workspace-reject": {"behavior": "deny"},
	} {
		got, err := agenttest.AnswerControl(kimiProvider{}, payload, kimiTestRequestID, agenttest.PermissionDecision(option, ""))
		require.NoError(t, err, option)
		assert.Equal(t, response, kimiSentResponse(t, got.Content), option)
	}
	// The question is stored under the key of its session.
	trustRequestID := providerkit.WorkspaceTrustRequestID("session_1")
	got, err := agenttest.AnswerControl(kimiProvider{}, payload, trustRequestID, agenttest.PermissionDecision("workspace-reject", ""))
	require.NoError(t, err)
	res := agenttest.ResolveAnswerInSession(kimiProvider{}, payload, "session_1", trustRequestID, got)
	assert.Equal(t, map[string]any{"decision": "reject"}, nativeResponse(t, res.Content))
	_, err = agenttest.AnswerControl(kimiProvider{}, payload, kimiTestRequestID, agenttest.PermissionDecision("workspace-reject", "Not here."))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "the trust question takes no reason")

	_, err = agenttest.AnswerControl(kimiProvider{}, payload, kimiTestRequestID, agenttest.PermissionDecision("approve", ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "the question offers its own two answers only")
}

// A payload that is no Kimi request reads as unrecognized, so no decision
// answers it, and a permission decision drawn for it anyway answers nothing.
func TestKimiAnswerControlRequest_AnswersNoUnrecognizedRequest(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"request":{"tool_name":"Bash","input":{"command":"ls"}}}`)
	for _, decision := range []*leapmuxv1.ControlDecision{
		agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""),
		agenttest.PermissionDecision(contracts.ControlAnswerOptionDeny, "No."),
	} {
		_, err := agenttest.AnswerControl(kimiProvider{}, payload, kimiTestRequestID, decision)
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
		_, err = kimiProvider{}.AnswerControlRequest(agent.ControlAnswerInput{
			Request: agent.NewControlRequestInput(payload), RequestID: kimiTestRequestID,
			Prompt: agent.GenericPermissionPrompt(), Decision: decision,
		})
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
	}
}

func TestKimiAnswerControlRequest_RefusesAPromptKindThatKimiNeverStates(t *testing.T) {
	t.Parallel()
	_, err := kimiProvider{}.AnswerControlRequest(agent.ControlAnswerInput{
		Request: agent.NewControlRequestInput([]byte(kimiStoredApproval)), RequestID: "req-1",
		Prompt:   &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Elicitation{Elicitation: &leapmuxv1.ElicitationPrompt{}}},
		Decision: agenttest.ElicitationDecision(leapmuxv1.ElicitationAction_ELICITATION_ACTION_DECLINE, ""),
	})
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
}
