package qwen

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// qwenQuestions are two questions of Qwen's question dialog.
const qwenQuestions = `[
	{"question":"Which color do you want?","header":"Color","multiSelect":false,"options":[{"label":"Red","description":"The red one"},{"label":"Blue","description":"The blue one"}]},
	{"question":"Which extras?","header":"Extras","multiSelect":true,"options":[{"label":"Cache","description":"c"},{"label":"Metrics","description":"m"}]}]`

// qwenQuestionPayloadWith is Qwen's question dialog as the probe recorded it,
// with the options that each case states.
func qwenQuestionPayloadWith(options string) string {
	return `{"jsonrpc":"2.0","id":3,"method":"session/request_permission","params":{"sessionId":"s","options":` + options + `,
		"toolCall":{"toolCallId":"call_dd960f1f5c","status":"pending","title":"Ask user 2 questions","kind":"think",
			"rawInput":{"questions":` + qwenQuestions + `},
			"_meta":{"toolName":"ask_user_question","qwenInteractionKind":"user_question","qwenQuestions":` + qwenQuestions + `}}}}`
}

// qwenQuestionPayload is the dialog with Qwen's own submit and cancel options.
var qwenQuestionPayload = qwenQuestionPayloadWith(`[{"optionId":"proceed_once","name":"Submit","kind":"allow_once"},{"optionId":"cancel","name":"Cancel","kind":"reject_once"}]`)

// qwenComposerPermission is a permission with one allow and one reject option,
// which every provider of the family reads alike.
const qwenComposerPermission = `{"jsonrpc":"2.0","id":3,"method":"session/request_permission","params":{"sessionId":"s",
	"toolCall":{"toolCallId":"c","kind":"execute","title":"Run","rawInput":{"command":"ls"}},
	"options":[{"optionId":"allow-1","name":"Allow","kind":"allow_once"},{"optionId":"reject-1","name":"Reject","kind":"reject_once"}]}}`

func qwenAnswer(custom string, selected ...string) *leapmuxv1.QuestionAnswer {
	return &leapmuxv1.QuestionAnswer{Selected: selected, CustomText: custom}
}

func qwenOptionIDs(options []*leapmuxv1.PermissionOption) []string {
	ids := make([]string, 0, len(options))
	for _, option := range options {
		ids = append(ids, option.GetOptionId())
	}
	return ids
}

// qwenPlanWith is a plan approval whose tool call states toolCall.
func qwenPlanWith(toolCall string) string {
	return `{"method":"session/request_permission","params":{"toolCall":` + toolCall + `}}`
}

func TestReadControlRequest_QwenPlanApprovalCarriesThePlanOfTheToolCall(t *testing.T) {
	plan := agenttest.ControlPromptOf(qwenProvider{}, []byte(planApprovalPayload)).GetPlan()
	require.NotNil(t, plan)
	assert.Equal(t, "1. Do X", plan.GetText())
	assert.Empty(t, plan.GetChoices(), "the reader answers with a plain approve or reject")

	plan = agenttest.ControlPromptOf(qwenProvider{}, []byte(`{"method":"session/request_permission","params":{"options":[],"toolCall":{"toolCallId":"c","kind":"switch_mode","rawInput":{"plan":"# Ship it"},"_meta":{"toolName":"exit_plan_mode"}}}}`)).GetPlan()
	assert.Equal(t, "# Ship it", plan.GetText())

	// The plan is wire data. A plan that is empty or no string states no text.
	for _, toolCall := range []string{
		`{"_meta":{"toolName":"exit_plan_mode"}}`,
		`{"rawInput":{"plan":""},"_meta":{"toolName":"exit_plan_mode"}}`,
		`{"rawInput":{"plan":["1. X"]},"_meta":{"toolName":"exit_plan_mode"}}`,
		`{"rawInput":{"plan":5},"_meta":{"toolName":"exit_plan_mode"}}`,
		`{"rawInput":"1. X","_meta":{"toolName":"exit_plan_mode"}}`,
	} {
		plan := agenttest.ControlPromptOf(qwenProvider{}, []byte(qwenPlanWith(toolCall))).GetPlan()
		require.NotNil(t, plan, toolCall)
		assert.Empty(t, plan.GetText(), toolCall)
	}
}

// A plan approval is a permission request: the same tool call under another
// method is no plan, because its answer would not resolve as one.
func TestReadControlRequest_QwenPlanApprovalNeedsThePermissionMethod(t *testing.T) {
	for _, payload := range []string{
		`{"params":{"toolCall":{"rawInput":{"plan":"1. X"},"_meta":{"toolName":"exit_plan_mode"}}}}`,
		`{"method":"session/update","params":{"toolCall":{"rawInput":{"plan":"1. X"},"_meta":{"toolName":"exit_plan_mode"}}}}`,
	} {
		assert.Nil(t, agenttest.ControlPromptOf(qwenProvider{}, []byte(payload)).GetPlan(), payload)
	}
}

// The mark is the tool name in `_meta`. A title or an ACP kind that reads like
// a plan does not make a plan approval.
func TestReadControlRequest_QwenReadsOnlyTheToolNameAsThePlanMark(t *testing.T) {
	permission := agenttest.ControlPromptOf(qwenProvider{}, []byte(`{"params":{"options":[{"optionId":"proceed_once","kind":"allow_once"}],
		"toolCall":{"toolCallId":"p","title":"exit_plan_mode","kind":"switch_mode","rawInput":{"plan":"1. X"}}}}`)).GetPermission()
	require.NotNil(t, permission)
	assert.Equal(t, "exit_plan_mode", permission.GetTitle())

	for _, toolCall := range []string{`{"_meta":{"toolName":"edit"}}`, `{"_meta":{"toolName":7}}`, `{"_meta":"exit_plan_mode"}`, `{}`} {
		assert.Nil(t, agenttest.ControlPromptOf(qwenProvider{}, []byte(qwenPlanWith(toolCall))).GetPlan(), toolCall)
	}
	assert.Nil(t, agenttest.ControlPromptOf(qwenProvider{}, []byte(`{"params":{}}`)).GetPlan())
	assert.Nil(t, agenttest.ControlPromptOf(qwenProvider{}, []byte(`{}`)).GetPlan())
}

func TestReadControlRequest_QwenReadsEveryOtherRequestThroughTheBaseReader(t *testing.T) {
	permission := agenttest.ControlPromptOf(qwenProvider{}, []byte(shellPermissionPayload)).GetPermission()
	require.NotNil(t, permission)
	assert.Equal(t, "touch x", permission.GetCommand())
	assert.Equal(t, []string{"proceed_once", "cancel"}, qwenOptionIDs(permission.GetOptions()))

	const command = "npm test -- --runInBand"
	permission = agenttest.ControlPromptOf(qwenProvider{}, []byte(`{"method":"session/request_permission","params":{"toolCall":{"toolCallId":"c","kind":"execute","title":"Shell",
		"rawInput":{"command":"`+command+`"},"_meta":{"toolName":"run_shell_command"}}}}`)).GetPermission()
	assert.Equal(t, command, permission.GetCommand())
	assert.Equal(t, "Shell", permission.GetTitle())

	permission = agenttest.ControlPromptOf(qwenProvider{}, []byte(`{"method":"session/request_permission","params":{"toolCall":{"toolCallId":"command","kind":"execute","title":"Run command",
		"rawInput":{"command":"printf x","timeout":0,"quiet":false,"note":""}}}}`)).GetPermission()
	assert.JSONEq(t, `{"command":"printf x","timeout":0,"quiet":false,"note":""}`, permission.GetInputJson())

	permission = agenttest.ControlPromptOf(qwenProvider{}, []byte(`{"params":{"toolCall":{"toolCallId":"c","kind":"other","title":"Write"},
		"options":[{"optionId":"once","kind":"allow_once","name":"Allow once"},{"optionId":"reject","kind":"reject_once","name":"Reject"}]}}`)).GetPermission()
	assert.Equal(t, []string{"once", "reject"}, qwenOptionIDs(permission.GetOptions()), "a request that states its options keeps them")

	permission = agenttest.ControlPromptOf(qwenProvider{}, []byte(`{"method":"session/request_permission","params":{"options":[{"optionId":"proceed_once","name":"Allow","kind":"allow_once"}],
		"toolCall":{"toolCallId":"e","kind":"edit","_meta":{"toolName":"edit"}}}}`)).GetPermission()
	assert.Equal(t, []string{"proceed_once", acp.CancelOptionID}, qwenOptionIDs(permission.GetOptions()), "a request with no reject option gains LeapMux's cancel")

	for _, payload := range []string{`{"method":"x"}`, `{}`, `not json`} {
		generic := agenttest.ControlPromptOf(qwenProvider{}, []byte(payload)).GetPermission()
		require.NotNil(t, generic, payload)
		assert.Equal(t, []string{contracts.ControlAnswerOptionAllow, contracts.ControlAnswerOptionDeny}, qwenOptionIDs(generic.GetOptions()), payload)
	}

	elicitation := agenttest.ControlPromptOf(qwenProvider{}, []byte(`{"method":"elicitation/create","params":{"message":"Hi","server":"s"}}`)).GetElicitation()
	require.NotNil(t, elicitation)
	assert.Equal(t, "Hi", elicitation.GetMessage())
}

func TestReadControlRequest_QwenQuestionDialog(t *testing.T) {
	prompt := agenttest.ControlPromptOf(qwenProvider{}, []byte(qwenQuestionPayload))
	question := prompt.GetQuestion()
	require.NotNil(t, question, "a permission request that its _meta marks as a question")
	require.Len(t, question.GetQuestions(), 2)
	assert.Equal(t, []string{"Color", "Extras"}, []string{question.GetQuestions()[0].GetHeader(), question.GetQuestions()[1].GetHeader()})
	assert.Equal(t, []bool{false, true}, []bool{question.GetQuestions()[0].GetMultiSelect(), question.GetQuestions()[1].GetMultiSelect()})
	assert.False(t, question.GetPreservesSelectionNotes(), "Qwen takes one string for each question, so a note and a choice are alternatives")

	assert.Nil(t, agenttest.ControlPromptOf(qwenProvider{}, []byte(`{"method":"session/request_permission","params":{"toolCall":{"_meta":{"toolName":"ask_user_question"}}}}`)).GetQuestion(),
		"the tool name alone does not mark a question")
	assert.Nil(t, agenttest.ControlPromptOf(qwenProvider{}, []byte(`{"params":{}}`)).GetQuestion())
	assert.Nil(t, agenttest.ControlPromptOf(qwenProvider{}, []byte(`{"params":{"toolCall":{"_meta":{"qwenInteractionKind":7}}}}`)).GetQuestion())
}

func TestReadControlRequest_QwenQuestionsFallBackToTheArguments(t *testing.T) {
	headers := func(payload string) []string {
		question := agenttest.ControlPromptOf(qwenProvider{}, []byte(payload)).GetQuestion()
		require.NotNil(t, question, payload)
		texts := make([]string, 0, len(question.GetQuestions()))
		for _, q := range question.GetQuestions() {
			texts = append(texts, q.GetQuestion())
		}
		return texts
	}
	assert.Equal(t, []string{"Which color do you want?"}, headers(`{"params":{"toolCall":{
		"rawInput":{"questions":[{"question":"Which color do you want?","options":[]}]},"_meta":{"qwenInteractionKind":"user_question"}}}}`))
	assert.Equal(t, []string{"Which extras?"}, headers(`{"params":{"toolCall":{
		"rawInput":{"questions":[{"question":"Which extras?","options":[]}]},"_meta":{"qwenInteractionKind":"user_question","qwenQuestions":"Which extras?"}}}}`),
		"a _meta copy that is no list is no copy")
	assert.Equal(t, []string{}, headers(`{"params":{"toolCall":{"_meta":{"qwenInteractionKind":"user_question"}}}}`))
	assert.Equal(t, []string{}, headers(`{"params":{"toolCall":{"rawInput":{"questions":[{"question":"X"}]},"_meta":{"qwenInteractionKind":"user_question","qwenQuestions":[]}}}}`),
		"an empty _meta list is the list")
}

// A client draws checkboxes only for `true`. A multiSelect that is absent or
// is no boolean states a single choice.
func TestReadControlRequest_QwenReadsMultiSelectAsTrueOnlyForTrue(t *testing.T) {
	question := agenttest.ControlPromptOf(qwenProvider{}, []byte(`{"params":{"toolCall":{"_meta":{"qwenInteractionKind":"user_question","qwenQuestions":[
		{"question":"A?","options":[]},{"question":"B?","options":[],"multiSelect":"yes"},{"question":"C?","options":[],"multiSelect":true}]}}}}`)).GetQuestion()
	require.Len(t, question.GetQuestions(), 3)
	assert.Equal(t, []bool{false, false, true}, []bool{question.GetQuestions()[0].GetMultiSelect(), question.GetQuestions()[1].GetMultiSelect(), question.GetQuestions()[2].GetMultiSelect()})
}

func TestAnswerControlRequest_QwenQuestionSelectsTheSubmitOptionWithTheAnswers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answers []*leapmuxv1.QuestionAnswer
		want    string
	}{
		{
			name:    "keys each answer by the question index",
			answers: []*leapmuxv1.QuestionAnswer{qwenAnswer("", "Blue"), qwenAnswer("", "Cache", "Metrics")},
			want:    `{"0":"Blue","1":"Cache, Metrics"}`,
		},
		{
			name:    "sends typed words as the answer",
			answers: []*leapmuxv1.QuestionAnswer{qwenAnswer("  Green  "), qwenAnswer("", "Cache")},
			want:    `{"0":"Green","1":"Cache"}`,
		},
		{
			name:    "sends the typed words of a multiple choice as one more entry after the chosen options",
			answers: []*leapmuxv1.QuestionAnswer{qwenAnswer("", "Red"), qwenAnswer(" Logs ", "Cache", "Metrics")},
			want:    `{"0":"Red","1":"Cache, Metrics, Logs"}`,
		},
		{
			name:    "sends the typed words of a multiple choice with no option chosen",
			answers: []*leapmuxv1.QuestionAnswer{qwenAnswer("", "Red"), qwenAnswer("Logs")},
			want:    `{"0":"Red","1":"Logs"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := agenttest.AnswerControl(qwenProvider{}, []byte(qwenQuestionPayload), "jsonrpc:3", agenttest.QuestionDecision(tc.answers...))
			require.NoError(t, err)
			assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:3","result":{"outcome":{"outcome":"selected","optionId":"proceed_once"},"answers":`+tc.want+`}}`, string(got.Content))
			assert.Empty(t, got.FollowUp)
		})
	}
	// The browser sends an answer only when every question has one.
	for name, answers := range map[string][]*leapmuxv1.QuestionAnswer{
		"a blank question":  {qwenAnswer("  Green  "), qwenAnswer("   ")},
		"an empty question": {qwenAnswer(""), qwenAnswer("", "Cache")},
		"nothing":           nil,
	} {
		_, err := agenttest.AnswerControl(qwenProvider{}, []byte(qwenQuestionPayload), "jsonrpc:3", agenttest.QuestionDecision(answers...))
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}
}

// An option whose value is empty states nothing, so its question is omitted.
func TestAnswerControlRequest_QwenQuestionSelectionThatJoinsToNothingStatesNoAnswer(t *testing.T) {
	payload := `{"params":{"options":[{"optionId":"submit","kind":"allow_once"}],"toolCall":{"_meta":{"qwenInteractionKind":"user_question","qwenQuestions":[{"question":"Q","options":[{"label":""}]}]}}}}`
	got, err := agenttest.AnswerControl(qwenProvider{}, []byte(payload), "r", agenttest.QuestionDecision(qwenAnswer("", "")))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"r","result":{"outcome":{"outcome":"selected","optionId":"submit"},"answers":{}}}`, string(got.Content))
}

// An option with an empty id is no answer that Qwen can read, so the reply
// takes the next allow option, and Qwen's own id when none is left.
func TestAnswerControlRequest_QwenQuestionPicksTheSubmitOption(t *testing.T) {
	answer := agenttest.QuestionDecision(qwenAnswer("", "Red"), qwenAnswer("", "Cache"))
	for _, tc := range []struct {
		name, options, want string
	}{
		{name: "the request states no option", options: `[]`, want: "proceed_once"},
		{name: "a submit option whose id is empty is skipped", options: `[{"optionId":"","kind":"allow_once"},{"optionId":"submit","kind":"allow_once"}]`, want: "submit"},
		{name: "only a blank submit option", options: `[{"optionId":"","kind":"allow_once"}]`, want: "proceed_once"},
		{name: "an always option is no submit option", options: `[{"optionId":"always","kind":"allow_always"}]`, want: "proceed_once"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := agenttest.AnswerControl(qwenProvider{}, []byte(qwenQuestionPayloadWith(tc.options)), "jsonrpc:3", answer)
			require.NoError(t, err)
			assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:3","result":{"outcome":{"outcome":"selected","optionId":"`+tc.want+`"},"answers":{"0":"Red","1":"Cache"}}}`, string(got.Content))
		})
	}
}

func TestAnswerControlRequest_QwenQuestionCancelSelectsTheCancelOption(t *testing.T) {
	for _, tc := range []struct {
		name, options, want string
	}{
		{name: "Qwen's own cancel option", options: `[{"optionId":"proceed_once","kind":"allow_once"},{"optionId":"cancel","kind":"reject_once"}]`, want: "cancel"},
		{name: "a reject option under an id of its own", options: `[{"optionId":"submit","kind":"allow_once"},{"optionId":"dismiss","kind":"reject_once"}]`, want: "dismiss"},
		{name: "no reject option", options: `[{"optionId":"submit","kind":"allow_once"}]`, want: "cancel"},
		{name: "an always-reject option is no cancel option", options: `[{"optionId":"never","kind":"reject_always"}]`, want: "cancel"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, feedback := range []string{"", "User stopped"} {
				got, err := agenttest.AnswerControl(qwenProvider{}, []byte(qwenQuestionPayloadWith(tc.options)), "jsonrpc:3", agenttest.CancelQuestions(feedback))
				require.NoError(t, err)
				assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:3","result":{"outcome":{"outcome":"selected","optionId":"`+tc.want+`"}}}`, string(got.Content), feedback)
				assert.Empty(t, got.FollowUp, "the cancel option carries no reason, and none follows")
			}
		})
	}
}

func TestAnswerControlRequest_QwenQuestionRefusesAnAnswerThatThePromptDoesNotOffer(t *testing.T) {
	for name, decision := range map[string]*leapmuxv1.ControlDecision{
		"an unknown value":          agenttest.QuestionDecision(qwenAnswer("", "Green")),
		"two values for one choice": agenttest.QuestionDecision(qwenAnswer("", "Red", "Blue")),
		// Qwen takes one string for a single choice, so it cannot be an option
		// and typed words at once.
		"an option and typed words for one choice": agenttest.QuestionDecision(qwenAnswer("Green", "Red")),
		"an answer for no question":                agenttest.QuestionDecision(qwenAnswer(""), qwenAnswer(""), qwenAnswer("x")),
		"a decision of another kind":               agenttest.PermissionDecision("proceed_once", ""),
	} {
		_, err := agenttest.AnswerControl(qwenProvider{}, []byte(qwenQuestionPayload), "jsonrpc:3", decision)
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}
	_, err := agenttest.AnswerControl(qwenProvider{}, []byte(`{"params":{"toolCall":{"_meta":{"qwenInteractionKind":"user_question","qwenQuestions":[]}}}}`), "r",
		agenttest.QuestionDecision(qwenAnswer("", "Red")))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "an answer for a question that the dialog did not ask")
}

// Qwen forwards its own question reply unchanged, under the native id.
func TestAnswerControlRequest_QwenQuestionReplyPassesTheResolution(t *testing.T) {
	payload := qwenQuestionPayloadWith(`[{"optionId":"proceed_once","name":"Submit","kind":"allow_once"},{"optionId":"cancel","name":"Cancel","kind":"reject_once"}]`)
	got, err := agenttest.AnswerControl(qwenProvider{}, []byte(payload), "jsonrpc:3", agenttest.QuestionDecision(qwenAnswer("", "Blue"), qwenAnswer("", "Cache")))
	require.NoError(t, err)
	resolved := agenttest.ResolveAnswer(qwenProvider{}, []byte(payload), "jsonrpc:3", got)
	require.False(t, resolved.Withhold)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":3,"result":{"outcome":{"outcome":"selected","optionId":"proceed_once"},"answers":{"0":"Blue","1":"Cache"}}}`, string(resolved.Content))
}

func TestAnswerControlRequest_QwenPlanApprovalTakesTheNeutralEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name     string
		decision *leapmuxv1.PlanDecision
		content  string
		option   string
		feedback string
	}{
		{
			name:     "approve",
			decision: &leapmuxv1.PlanDecision{Approve: true},
			content:  `{"type":"control_response","response":{"subtype":"success","request_id":"jsonrpc:5","response":{"behavior":"allow","updatedInput":{}}}}`,
			option:   contracts.QwenPermissionOptionProceedOnce,
		},
		{
			name:     "approve into auto edit",
			decision: &leapmuxv1.PlanDecision{Approve: true, PermissionMode: contracts.QwenModeAutoEdit},
			content:  `{"type":"control_response","response":{"subtype":"success","request_id":"jsonrpc:5","response":{"behavior":"allow","updatedInput":{}}}}`,
			option:   contracts.QwenPermissionOptionProceedAlways,
		},
		{
			name:     "reject with a reason",
			decision: &leapmuxv1.PlanDecision{Feedback: "Split step 2"},
			content:  `{"type":"control_response","response":{"subtype":"success","request_id":"jsonrpc:5","response":{"behavior":"deny","message":"Split step 2"}}}`,
			option:   contracts.QwenPermissionOptionCancel,
			feedback: "Split step 2",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := agenttest.AnswerControl(qwenProvider{}, []byte(planApprovalPayload), qwenStoredRequestID, agenttest.PlanDecision(tc.decision))
			require.NoError(t, err)
			assert.JSONEq(t, tc.content, string(got.Content))
			assert.Empty(t, got.FollowUp, "the resolution sends the reason itself, so the answer states none")
			resolved := agenttest.ResolveAnswer(qwenProvider{}, []byte(planApprovalPayload), qwenStoredRequestID, got)
			require.False(t, resolved.Withhold)
			id, option := selectedOptionOf(t, resolved.Content)
			assert.Equal(t, "5", id)
			assert.Equal(t, tc.option, option)
			assert.Equal(t, agent.FeedbackFollowUp(tc.feedback), resolved.FollowUp)
		})
	}

	_, err := agenttest.AnswerControl(qwenProvider{}, []byte(planApprovalPayload), qwenStoredRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, ChoiceId: contracts.QwenPermissionOptionProceedAlways}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "the plan prompt offers no choice")
	_, err = agenttest.AnswerControl(qwenProvider{}, []byte(planApprovalPayload), qwenStoredRequestID, agenttest.PermissionDecision(contracts.QwenPermissionOptionCancel, ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a plan prompt takes a plan decision, not one of Qwen's four options")
}

// Qwen's permission reply has no field for a reason, so the reason follows as
// a message of its own.
func TestAnswerControlRequest_QwenPermissionSendsTheReasonAsAFollowUp(t *testing.T) {
	got, err := agenttest.AnswerControl(qwenProvider{}, []byte(shellPermissionPayload), "jsonrpc:9", agenttest.PermissionDecision(contracts.QwenPermissionOptionCancel, "Use the other file"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:9","result":{"outcome":{"outcome":"selected","optionId":"cancel"}}}`, string(got.Content))
	assert.Equal(t, "Use the other file", got.FollowUp)

	got, err = agenttest.AnswerControl(qwenProvider{}, []byte(qwenComposerPermission), "jsonrpc:3", agenttest.PermissionDecision("reject-1", "use the other file"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:3","result":{"outcome":{"outcome":"selected","optionId":"reject-1"}}}`, string(got.Content))
	assert.Equal(t, "use the other file", got.FollowUp)

	for _, optionID := range []string{"allow-1", "reject-1"} {
		got, err = agenttest.AnswerControl(qwenProvider{}, []byte(qwenComposerPermission), "jsonrpc:3", agenttest.PermissionDecision(optionID, ""))
		require.NoError(t, err)
		assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:3","result":{"outcome":{"outcome":"selected","optionId":"`+optionID+`"}}}`, string(got.Content))
		assert.Empty(t, got.FollowUp, optionID)
	}

	_, err = agenttest.AnswerControl(qwenProvider{}, []byte(qwenComposerPermission), "jsonrpc:3", agenttest.PermissionDecision("allow-1", "a note"))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "an approval carries no reason")
}

// A request with no options takes LeapMux's allow and deny, whose neutral deny
// carries the reason itself.
func TestAnswerControlRequest_QwenPermissionWithNoOptionsTakesTheNeutralEnvelope(t *testing.T) {
	payload := `{"params":{"toolCall":{"toolCallId":"c","title":"Edit"}}}`
	got, err := agenttest.AnswerControl(qwenProvider{}, []byte(payload), "r", agenttest.PermissionDecision(contracts.ControlAnswerOptionDeny, "no"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"r","response":{"behavior":"deny","message":"no"}}}`, string(got.Content))
	assert.Empty(t, got.FollowUp)
}

func TestAnswerControlRequest_QwenElicitationTakesTheNeutralEnvelope(t *testing.T) {
	got, err := agenttest.AnswerControl(qwenProvider{}, []byte(`{"method":"elicitation/create","params":{"message":"Sign in"}}`), "r", agenttest.ElicitationDecision(leapmuxv1.ElicitationAction_ELICITATION_ACTION_CANCEL, ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"r","response":{"action":"cancel"}}}`, string(got.Content))
}
