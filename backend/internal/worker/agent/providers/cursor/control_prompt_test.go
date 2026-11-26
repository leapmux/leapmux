package cursor

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
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// cursorRequestID is the Worker's key of the stored request that each answer
// echoes: the native JSON-RPC id 7 under the Worker's prefix.
const cursorRequestID = "jsonrpc:7"

const cursorCreatePlan = `{"jsonrpc":"2.0","id":7,"method":"cursor/create_plan","params":{
	"name":"Review changes","overview":"Review without edits.","plan":"# Plan\n\n1. Inspect the files."}}`

const cursorQuestion = `{"jsonrpc":"2.0","id":7,"method":"cursor/ask_question","params":{"questions":[
	{"id":"q","prompt":"Choose","options":[{"id":"first","label":"First"}]}]}}`

// cursorPermission is a plain Agent Client Protocol permission, which Cursor
// sends beside its own two requests.
const cursorPermission = `{"jsonrpc":"2.0","id":7,"method":"session/request_permission","params":{
	"sessionId":"s",
	"toolCall":{"toolCallId":"c","kind":"execute","title":"Run","rawInput":{"command":"ls"}},
	"options":[{"optionId":"allow-1","name":"Allow","kind":"allow_once"},{"optionId":"reject-1","name":"Reject","kind":"reject_once"}]}}`

func createPlanPrompt(title, reason, text string) *leapmuxv1.ControlPrompt {
	return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{
		Title: title, Reason: reason, Text: text,
		Options: []*leapmuxv1.PermissionOption{
			{OptionId: contracts.ControlAnswerOptionAllow, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, Name: "Approve"},
			{OptionId: contracts.ControlAnswerOptionDeny, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, Name: "Reject"},
		},
	}}}
}

// The reader must see the whole plan before an approval, so the prompt states
// its name, its overview and its body. The request states no options, and the
// prompt offers the two that Cursor's plan buttons send.
func TestReadControlRequest_CursorCreatePlanStatesItsNameOverviewAndPlan(t *testing.T) {
	t.Parallel()
	agenttest.RequireProto(t, createPlanPrompt("Create Plan: Review changes", "Review without edits.", "# Plan\n\n1. Inspect the files."), agenttest.ControlPromptOf(Registration().Plugin, []byte(cursorCreatePlan)))
}

func TestReadControlRequest_CursorCreatePlanKeepsAnAbsentPartAbsent(t *testing.T) {
	t.Parallel()
	agenttest.RequireProto(t, createPlanPrompt("Create Plan: Review", "", ""),
		agenttest.ControlPromptOf(Registration().Plugin, []byte(`{"method":"cursor/create_plan","params":{"name":"Review"}}`)))
	for _, payload := range []string{
		`{"method":"cursor/create_plan","params":{}}`,
		`{"method":"cursor/create_plan"}`,
		`{"method":"cursor/create_plan","params":[]}`,
		`{"method":"cursor/create_plan","params":{"name":7,"overview":null,"plan":{"text":"x"}}}`,
	} {
		agenttest.RequireProto(t, createPlanPrompt("Create Plan", "", ""), agenttest.ControlPromptOf(Registration().Plugin, []byte(payload)))
	}
}

func TestReadControlRequest_CursorAskQuestionReadsEachQuestionAndItsOptions(t *testing.T) {
	t.Parallel()
	got := agenttest.ControlPromptOf(Registration().Plugin, []byte(`{"method":"cursor/ask_question","params":{"questions":[
		{"id":"q","prompt":"Choose","allowMultiple":true,"options":[{"id":"first","label":"Same"},{"id":"second","label":"Same"}]}]}}`))
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
		Questions: []*leapmuxv1.ControlQuestion{{
			Id: "q", Header: "Choose", Question: "Choose", MultiSelect: true, AllowCustomText: proto.Bool(false),
			Options: []*leapmuxv1.QuestionOption{
				{Value: proto.String("first"), Label: "Same"},
				{Value: proto.String("second"), Label: "Same"},
			},
		}},
	}}}, got)
}

// A null or a number among the questions or the options cannot be drawn, so
// it is dropped, and the valid choices beside it stay.
func TestReadControlRequest_CursorAskQuestionDropsMalformedQuestionsAndOptions(t *testing.T) {
	t.Parallel()
	got := agenttest.ControlPromptOf(Registration().Plugin, []byte(`{"method":"cursor/ask_question","params":{"questions":[null,7,
		{"id":"q","prompt":"Choose","options":[null,7,{"id":"valid","label":"Valid"}]}]}}`))
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
		Questions: []*leapmuxv1.ControlQuestion{{
			Id: "q", Header: "Choose", Question: "Choose", AllowCustomText: proto.Bool(false),
			Options: []*leapmuxv1.QuestionOption{{Value: proto.String("valid"), Label: "Valid"}},
		}},
	}}}, got)

	for _, payload := range []string{
		`{"method":"cursor/ask_question","params":{"questions":{}}}`,
		`{"method":"cursor/ask_question","params":{"questions":null}}`,
		`{"method":"cursor/ask_question","params":"questions"}`,
		`{"method":"cursor/ask_question"}`,
	} {
		question := agenttest.ControlPromptOf(Registration().Plugin, []byte(payload)).GetQuestion()
		require.NotNil(t, question, "an ask_question request is a question even when it states none: %s", payload)
		assert.Empty(t, question.GetQuestions(), payload)
	}
}

func TestReadControlRequest_CursorAskQuestionFallsBackForEachLabel(t *testing.T) {
	t.Parallel()
	got := agenttest.ControlPromptOf(Registration().Plugin, []byte(`{"method":"cursor/ask_question","params":{"questions":[
		{"id":"color","allowMultiple":"true","options":[
			{"label":"Red"},
			{"id":"blue"},
			{"id":"","label":""},
			{"id":7,"label":null},
			{"id":"green","label":"Green"}
		]},
		{"prompt":7,"options":"none"}
	]}}`))
	questions := got.GetQuestion().GetQuestions()
	require.Len(t, questions, 2)

	color := questions[0]
	assert.Equal(t, "color", color.GetHeader(), "a question with no words of its own is headed by its id")
	assert.Empty(t, color.GetQuestion())
	assert.False(t, color.GetMultiSelect(), "only the boolean true allows a multiple selection")
	require.Len(t, color.GetOptions(), 3, "an option with neither a label nor an id is dropped")
	assert.Equal(t, "Red", color.GetOptions()[0].GetLabel())
	assert.Equal(t, "Red", color.GetOptions()[0].GetValue(), "an option with no id sends its label")
	assert.Equal(t, "blue", color.GetOptions()[1].GetLabel(), "an option with no label shows its id")
	assert.Equal(t, "blue", color.GetOptions()[1].GetValue())
	assert.Equal(t, "Green", color.GetOptions()[2].GetLabel())
	assert.Equal(t, "green", color.GetOptions()[2].GetValue())
	for _, option := range color.GetOptions() {
		assert.NotNil(t, option.Value, "every Cursor option states the value that its answer sends")
	}

	unnamed := questions[1]
	assert.Empty(t, unnamed.GetId())
	assert.Empty(t, unnamed.GetHeader(), "a question with neither words nor an id has no header")
	assert.Empty(t, unnamed.GetOptions())
	require.NotNil(t, unnamed.AllowCustomText)
	assert.False(t, unnamed.GetAllowCustomText(), "Cursor's ACP adapter drops typed text, so no question takes it")
}

// Cursor speaks the Agent Client Protocol for everything but its own two
// requests, and the base reads those: the options that the runtime offers,
// the command of a shell call, an MCP form, and the unrecognized prompt of a
// request that nothing recognizes.
func TestReadControlRequest_CursorReadsEveryOtherRequestThroughTheBase(t *testing.T) {
	t.Parallel()
	permission := agenttest.ControlPromptOf(Registration().Plugin, []byte(cursorPermission)).GetPermission()
	require.NotNil(t, permission)
	assert.Equal(t, "Run", permission.GetTitle())
	assert.Equal(t, "ls", permission.GetCommand())
	assert.JSONEq(t, `{"command":"ls"}`, permission.GetInputJson())
	require.Len(t, permission.GetOptions(), 2)
	assert.Equal(t, "allow-1", permission.GetOptions()[0].GetOptionId())
	assert.Equal(t, leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, permission.GetOptions()[0].GetKind())
	assert.Equal(t, "reject-1", permission.GetOptions()[1].GetOptionId())
	assert.Equal(t, leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, permission.GetOptions()[1].GetKind())

	command := "printf \"first line\\n\"\nprintf \"second line\\n\""
	input := map[string]any{"command": command, "timeout": 0, "quiet": false, "note": ""}
	encodedInput, err := json.Marshal(input)
	require.NoError(t, err)
	shell, err := json.Marshal(map[string]any{"method": "session/request_permission", "params": map[string]any{
		"toolCall": map[string]any{"toolCallId": "command", "kind": "execute", "title": "Run command", "rawInput": input},
	}})
	require.NoError(t, err)
	permission = agenttest.ControlPromptOf(Registration().Plugin, []byte(string(shell))).GetPermission()
	assert.Equal(t, "Run command", permission.GetTitle())
	assert.Equal(t, command, permission.GetCommand())
	assert.JSONEq(t, string(encodedInput), permission.GetInputJson())
	assert.Equal(t, []string{acp.CancelOptionID}, agenttest.PermissionOptionIDs(permission.GetOptions()),
		"the protocol answers a call with no options only with the cancelled outcome")

	elicitation := agenttest.ControlPromptOf(Registration().Plugin, []byte(`{"jsonrpc":"2.0","id":7,"method":"elicitation/create","params":{"mode":"form","message":"Sign in"}}`)).GetElicitation()
	require.NotNil(t, elicitation)
	assert.Equal(t, "Sign in", elicitation.GetMessage())

	for _, payload := range []string{`{"method":"cursor/update_todos","params":{}}`, `{"method":7}`, `not json`, `{}`} {
		agenttest.RequireProto(t, agent.UnrecognizedControlPrompt(), agenttest.ControlPromptOf(Registration().Plugin, []byte(payload)), payload)
	}
}

// Approve sends the empty input that Cursor's plan button sends, even when the
// request states an input of its own, and the Worker turns it into Cursor's
// `accepted` outcome under the native id.
func TestAnswerControlRequest_CursorCreatePlanApprovesWithAnEmptyInput(t *testing.T) {
	t.Parallel()
	for _, payload := range []string{cursorCreatePlan, `{"jsonrpc":"2.0","id":7,"method":"cursor/create_plan","params":{},"request":{"input":{"plan":"x"}}}`} {
		got, err := agenttest.AnswerControl(Registration().Plugin, []byte(payload), cursorRequestID, agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""))
		require.NoError(t, err, payload)
		assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"jsonrpc:7","response":{"behavior":"allow","updatedInput":{}}}}`, string(got.Content), payload)
		assert.Nil(t, got.PlanApproval, "a create-plan approval is a permission, and carries no plan settings")
		assert.Empty(t, got.FollowUp)

		resolved := agenttest.ResolveAnswer(Registration().Plugin, []byte(payload), cursorRequestID, got)
		assert.False(t, resolved.Withhold)
		assert.JSONEq(t, `{"jsonrpc":"2.0","id":7,"result":{"outcome":{"outcome":"accepted"}}}`, string(resolved.Content), payload)
	}
}

// Reject denies with the user's feedback, which the Worker delivers as the
// reason of Cursor's `rejected` outcome, so no follow-up message repeats it.
// A bare reject states LeapMux's placeholder, which the Worker drops.
func TestAnswerControlRequest_CursorCreatePlanRejectsWithTheFeedbackAsTheReason(t *testing.T) {
	t.Parallel()
	got, err := agenttest.AnswerControl(Registration().Plugin, []byte(cursorCreatePlan), cursorRequestID, agenttest.PermissionDecision(contracts.ControlAnswerOptionDeny, "Needs tests."))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"jsonrpc:7","response":{"behavior":"deny","message":"Needs tests."}}}`, string(got.Content))
	assert.Empty(t, got.FollowUp)
	resolved := agenttest.ResolveAnswer(Registration().Plugin, []byte(cursorCreatePlan), cursorRequestID, got)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":7,"result":{"outcome":{"outcome":"rejected","reason":"Needs tests."}}}`, string(resolved.Content))
	assert.Equal(t, agent.ControlFollowUp{}, resolved.FollowUp)

	got, err = agenttest.AnswerControl(Registration().Plugin, []byte(cursorCreatePlan), cursorRequestID, agenttest.PermissionDecision(contracts.ControlAnswerOptionDeny, ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"jsonrpc:7","response":{"behavior":"deny","message":"Rejected by user."}}}`, string(got.Content))
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":7,"result":{"outcome":{"outcome":"rejected"}}}`, string(agenttest.ResolveAnswer(Registration().Plugin, []byte(cursorCreatePlan), cursorRequestID, got).Content))
}

func TestAnswerControlRequest_CursorCreatePlanRefusesADecisionItDoesNotOffer(t *testing.T) {
	t.Parallel()
	for name, decision := range map[string]*leapmuxv1.ControlDecision{
		"an approval with feedback":   agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, "and add tests"),
		"an option it does not offer": agenttest.PermissionDecision("allow-1", ""),
		"the base protocol's cancel":  agenttest.PermissionDecision(acp.CancelOptionID, ""),
		"a plan decision":             agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true}),
		"a question decision":         agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"first"}}),
	} {
		_, err := agenttest.AnswerControl(Registration().Plugin, []byte(cursorCreatePlan), cursorRequestID, decision)
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}
}

// Each answer states the question's id and the values of its selected
// options. Two options with the same label still send different ids.
func TestAnswerControlRequest_CursorQuestionSendsTheSelectedOptionIDs(t *testing.T) {
	t.Parallel()
	payload := `{"jsonrpc":"2.0","id":7,"method":"cursor/ask_question","params":{"questions":[
		{"id":"q","prompt":"Choose","allowMultiple":true,"options":[{"id":"first","label":"Same"},{"id":"second","label":"Same"}]}]}}`
	got, err := agenttest.AnswerControl(Registration().Plugin, []byte(payload), cursorRequestID, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"second"}}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:7","result":{"outcome":{"outcome":"answered","answers":[
		{"questionId":"q","selectedOptionIds":["second"]}]}}}`, string(got.Content))
	assert.Empty(t, got.FollowUp)

	got, err = agenttest.AnswerControl(Registration().Plugin, []byte(payload), cursorRequestID, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"second", "first"}}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:7","result":{"outcome":{"outcome":"answered","answers":[
		{"questionId":"q","selectedOptionIds":["second","first"]}]}}}`, string(got.Content), "a multiple selection keeps its order")

	resolved := agenttest.ResolveAnswer(Registration().Plugin, []byte(payload), cursorRequestID, got)
	assert.False(t, resolved.Withhold)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":7,"result":{"outcome":{"outcome":"answered","answers":[
		{"questionId":"q","selectedOptionIds":["second","first"]}]}}}`, string(resolved.Content), "the Worker restores the native id")
}

// An answer states no typed text when the reader gave none, and a question
// with no id takes its index.
func TestAnswerControlRequest_CursorQuestionNumbersAQuestionWithNoID(t *testing.T) {
	t.Parallel()
	payload := `{"method":"cursor/ask_question","params":{"questions":[
		{"id":"q","prompt":"Choose","options":[{"id":"first","label":"First"}]},
		{"prompt":"Size","options":[{"id":"small","label":"Small"}]},
		{"id":"","prompt":"Shape","options":[{"label":"Round"}]}]}}`
	got, err := agenttest.AnswerControl(Registration().Plugin, []byte(payload), cursorRequestID, agenttest.QuestionDecision(
		&leapmuxv1.QuestionAnswer{Selected: []string{"first"}},
		&leapmuxv1.QuestionAnswer{Selected: []string{"small"}},
		&leapmuxv1.QuestionAnswer{Selected: []string{"Round"}},
	))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:7","result":{"outcome":{"outcome":"answered","answers":[
		{"questionId":"q","selectedOptionIds":["first"]},
		{"questionId":"q1","selectedOptionIds":["small"]},
		{"questionId":"q2","selectedOptionIds":["Round"]}]}}}`, string(got.Content))
}

func TestAnswerControlRequest_CursorQuestionNeedsAnOptionForEveryQuestion(t *testing.T) {
	t.Parallel()
	payload := `{"method":"cursor/ask_question","params":{"questions":[
		{"id":"a","prompt":"A","options":[{"id":"a1","label":"A1"}]},
		{"id":"b","prompt":"B","options":[{"id":"b1","label":"B1"}]}]}}`
	for name, decision := range map[string]*leapmuxv1.ControlDecision{
		"no answer at all":                agenttest.QuestionDecision(),
		"an answer for the first only":    agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"a1"}}),
		"an empty answer for the second":  agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"a1"}}, &leapmuxv1.QuestionAnswer{}),
		"an option of the other question": agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"b1"}}, &leapmuxv1.QuestionAnswer{Selected: []string{"b1"}}),
		"two options of a single choice":  agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"a1", "a1"}}, &leapmuxv1.QuestionAnswer{Selected: []string{"b1"}}),
	} {
		_, err := agenttest.AnswerControl(Registration().Plugin, []byte(payload), cursorRequestID, decision)
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}

	_, err := agenttest.AnswerControl(Registration().Plugin, []byte(`{"method":"cursor/ask_question","params":{"questions":[]}}`), cursorRequestID, agenttest.QuestionDecision())
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a request with no question takes no answer")
}

// Cursor's terminal interface takes typed text, but its ACP adapter discards
// it, so a typed answer fails rather than reaching a model that cannot read
// it. A selection beside the text does not make the text acceptable, on a
// multiple-choice question either.
func TestAnswerControlRequest_CursorQuestionRefusesTypedText(t *testing.T) {
	t.Parallel()
	multiple := `{"method":"cursor/ask_question","params":{"questions":[
		{"id":"q","prompt":"Choose","allowMultiple":true,"options":[{"id":"first","label":"First"},{"id":"second","label":"Second"}]}]}}`
	for payload, answers := range map[string][]*leapmuxv1.QuestionAnswer{
		cursorQuestion: {
			{CustomText: "Keep this answer."},
			{Selected: []string{"first"}, CustomText: "  a reason of my own  "},
		},
		multiple: {
			{Selected: []string{"first", "second"}, CustomText: "and a third"},
		},
	} {
		for _, answer := range answers {
			_, err := agenttest.AnswerControl(Registration().Plugin, []byte(payload), cursorRequestID, agenttest.QuestionDecision(answer))
			assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, answer.GetCustomText())
		}
	}
}

// Text that is only white space is no text, as the browser's composer trimmed
// it: the selection answers alone, and no selection answers nothing.
func TestAnswerControlRequest_CursorQuestionIgnoresBlankText(t *testing.T) {
	t.Parallel()
	got, err := agenttest.AnswerControl(Registration().Plugin, []byte(cursorQuestion), cursorRequestID, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"first"}, CustomText: " \n\t "}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:7","result":{"outcome":{"outcome":"answered","answers":[
		{"questionId":"q","selectedOptionIds":["first"]}]}}}`, string(got.Content))

	_, err = agenttest.AnswerControl(Registration().Plugin, []byte(cursorQuestion), cursorRequestID, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{CustomText: "   "}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "blank text is no option")
}

// A refused question takes the protocol's `cancelled` outcome, and the reason
// that the reader typed rides beside it only when there is one.
func TestAnswerControlRequest_CursorQuestionRefusalIsTheCancelledOutcome(t *testing.T) {
	t.Parallel()
	got, err := agenttest.AnswerControl(Registration().Plugin, []byte(cursorQuestion), cursorRequestID, agenttest.CancelQuestions("changed mind"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:7","result":{"outcome":{"outcome":"cancelled","reason":"changed mind"}}}`, string(got.Content))
	assert.Empty(t, got.FollowUp, "the reason rides in the answer")

	got, err = agenttest.AnswerControl(Registration().Plugin, []byte(cursorQuestion), cursorRequestID, agenttest.CancelQuestions(""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:7","result":{"outcome":{"outcome":"cancelled"}}}`, string(got.Content))

	got, err = agenttest.AnswerControl(Registration().Plugin, []byte(`{"method":"cursor/ask_question","params":{"questions":[]}}`), cursorRequestID, agenttest.CancelQuestions("User stopped"))
	require.NoError(t, err, "a request with no question can still be refused")
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:7","result":{"outcome":{"outcome":"cancelled","reason":"User stopped"}}}`, string(got.Content))

	_, err = agenttest.AnswerControl(Registration().Plugin, []byte(cursorQuestion), cursorRequestID, agenttest.PermissionDecision(contracts.ControlAnswerOptionDeny, ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a question takes a question decision")
}

// Every other request takes the base protocol's answer: the chosen option of
// the runtime, and a refusal's reason as a message after it, because Cursor
// states no field for it.
func TestAnswerControlRequest_CursorAnswersEveryOtherRequestThroughTheBase(t *testing.T) {
	t.Parallel()
	got, err := agenttest.AnswerControl(Registration().Plugin, []byte(cursorPermission), cursorRequestID, agenttest.PermissionDecision("reject-1", "use the other file"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:7","result":{"outcome":{"outcome":"selected","optionId":"reject-1"}}}`, string(got.Content))
	assert.Equal(t, "use the other file", got.FollowUp)

	got, err = agenttest.AnswerControl(Registration().Plugin, []byte(cursorPermission), cursorRequestID, agenttest.PermissionDecision("allow-1", ""))
	require.NoError(t, err)
	assert.Empty(t, got.FollowUp)
	resolved := agenttest.ResolveAnswer(Registration().Plugin, []byte(cursorPermission), cursorRequestID, got)
	assert.False(t, resolved.Withhold)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":7,"result":{"outcome":{"outcome":"selected","optionId":"allow-1"}}}`, string(resolved.Content))

	elicitation := `{"jsonrpc":"2.0","id":7,"method":"elicitation/create","params":{"mode":"form","message":"Sign in","requestedSchema":{"type":"object"}}}`
	got, err = agenttest.AnswerControl(Registration().Plugin, []byte(elicitation), cursorRequestID, agenttest.ElicitationDecision(leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT, `{"token":"t"}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"jsonrpc:7","response":{"action":"accept","content":{"token":"t"}}}}`, string(got.Content))
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":7,"result":{"action":"accept","content":{"token":"t"}}}`, string(agenttest.ResolveAnswer(Registration().Plugin, []byte(elicitation), cursorRequestID, got).Content))
}
