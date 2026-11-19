package letta

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// lettaPromptQuestionRequest is the question request that the Worker
// publishes for one Letta Code receipt. The questions are the native
// AskUserQuestion arguments, verbatim.
const lettaPromptQuestionRequest = `{
	"type":"ask_user","requestId":"letta-question-ask-1","tool_name":"AskUserQuestion","tool_call_id":"ask-1",
	"tool_input":{"questions":[
		{"question":"Which color do you prefer?","header":"Color","options":[
			{"label":"Blue","description":"The color blue"},{"label":"Red","description":"The color red"}]},
		{"question":"Which sizes?","header":"Sizes","multiSelect":true,"options":[
			{"label":"Small","description":"S"},{"label":"Medium","description":"M"},{"label":"Large","description":"L"}]}
	]},
	"suggestions":null
}`

// lettaPermissionPayload is a permission request in the shape that
// onControlRequest publishes it.
func lettaPermissionPayload(t *testing.T, toolName any, toolInput any) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"type": "permission", "requestId": "perm-call-1", "tool_name": toolName, "tool_call_id": "call-1",
		"tool_input": toolInput, "suggestions": []any{},
	})
	require.NoError(t, err)
	return payload
}

func lettaLabelOption(label string) *leapmuxv1.QuestionOption {
	return &leapmuxv1.QuestionOption{Value: &label, Label: label}
}

// lettaSentAnswer is the neutral answer that the question decision builds.
type lettaSentAnswer struct {
	Response struct {
		RequestID string `json:"request_id"`
		Response  struct {
			Behavior     string `json:"behavior"`
			UpdatedInput struct {
				Questions json.RawMessage   `json:"questions"`
				Answers   map[string]string `json:"answers"`
			} `json:"updatedInput"`
		} `json:"response"`
	} `json:"response"`
}

// lettaAnswerQuestions answers lettaPromptQuestionRequest with answers and
// decodes what the answer sends.
func lettaAnswerQuestions(t *testing.T, answers ...*leapmuxv1.QuestionAnswer) lettaSentAnswer {
	t.Helper()
	got, err := agenttest.AnswerControl(lettaProvider{}, []byte(lettaPromptQuestionRequest), "letta-question-ask-1", agenttest.QuestionDecision(answers...))
	require.NoError(t, err)
	assert.Empty(t, got.FollowUp)
	var sent lettaSentAnswer
	require.NoError(t, json.Unmarshal(got.Content, &sent), string(got.Content))
	return sent
}

func TestLettaReadControlRequest_AQuestionRequestReadsEachQuestionWithItsOptionLabelsAndItsKind(t *testing.T) {
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
		Questions: []*leapmuxv1.ControlQuestion{
			{Question: "Which color do you prefer?", Options: []*leapmuxv1.QuestionOption{lettaLabelOption("Blue"), lettaLabelOption("Red")}},
			{Question: "Which sizes?", MultiSelect: true, Options: []*leapmuxv1.QuestionOption{
				lettaLabelOption("Small"), lettaLabelOption("Medium"), lettaLabelOption("Large"),
			}},
		},
	}}}, agenttest.ControlPromptOf(lettaProvider{}, []byte(lettaPromptQuestionRequest)))
}

func TestLettaReadControlRequest_AQuestionRequestThatHoldsNoQuestionAsksNone(t *testing.T) {
	for _, payload := range []string{
		`{"type":"ask_user","tool_input":{}}`,
		`{"type":"ask_user"}`,
		`{"type":"ask_user","tool_input":[{"question":"In a list?"}]}`,
		`{"type":"ask_user","tool_input":{"questions":"nope"}}`,
		`{"type":"ask_user","tool_input":{"questions":null}}`,
	} {
		got := agenttest.ControlPromptOf(lettaProvider{}, []byte(payload))
		require.NotNil(t, got.GetQuestion(), payload)
		assert.Empty(t, got.GetQuestion().GetQuestions(), payload)
	}
}

// Letta's options are `{label, description}` objects; a bare string is read
// as its label. An option with no label, or one that is no text, is dropped,
// and so is a question that is no object. A question's text that is no text
// reads as empty, and only a real true opens the multiple-choice control.
func TestLettaReadControlRequest_AQuestionRequestReadsWhatTheControlCanDraw(t *testing.T) {
	got := agenttest.ControlPromptOf(lettaProvider{}, []byte(`{"type":"ask_user","tool_input":{"questions":[
		null, "plain text", ["a list"],
		{"question":"Pick?","multiSelect":"yes","options":["Plain",{"label":""},{"label":7},{"description":"no label"},null,3,{"label":"Labeled"}]},
		{"question":5,"options":"none"}
	]}}`))
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
		Questions: []*leapmuxv1.ControlQuestion{
			{Question: "Pick?", Options: []*leapmuxv1.QuestionOption{lettaLabelOption("Plain"), lettaLabelOption("Labeled")}},
			{Question: ""},
		},
	}}}, got)
}

// A permission for the question tool asks whether the tool may run, which
// Strict mode asks; only a question request is a question.
func TestLettaReadControlRequest_APermissionForTheQuestionToolIsAPermission(t *testing.T) {
	got := agenttest.ControlPromptOf(lettaProvider{}, lettaPermissionPayload(t, "AskUserQuestion", map[string]any{}))
	require.NotNil(t, got.GetPermission())
	assert.Equal(t, "AskUserQuestion", got.GetPermission().GetTitle())
}

func TestLettaReadControlRequest_APermissionStatesItsToolInputAndLeapMuxsAllowAndDeny(t *testing.T) {
	got := agenttest.ControlPromptOf(lettaProvider{}, lettaPermissionPayload(t, contracts.LettaToolBash, map[string]any{"command": "ls -la", "timeout": 5}))
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{
		Title:     "Bash",
		Command:   "ls -la",
		InputJson: `{"command":"ls -la","timeout":5}`,
		Options:   agent.AllowDenyOptions(),
	}}}, got)

	other := agenttest.ControlPromptOf(lettaProvider{}, lettaPermissionPayload(t, contracts.LettaToolEdit, map[string]any{"command": "not a shell"})).GetPermission()
	assert.Empty(t, other.GetCommand(), "only a shell call states a command")
	assert.JSONEq(t, `{"command":"not a shell"}`, other.GetInputJson())

	noText := agenttest.ControlPromptOf(lettaProvider{}, lettaPermissionPayload(t, contracts.LettaToolBash, map[string]any{"command": 7})).GetPermission()
	assert.Empty(t, noText.GetCommand(), "a command that is no text states none")
}

func TestLettaReadControlRequest_APermissionWithNoToolNameIsTitledTool(t *testing.T) {
	for _, toolName := range []any{"", nil, 7} {
		got := agenttest.ControlPromptOf(lettaProvider{}, lettaPermissionPayload(t, toolName, map[string]any{})).GetPermission()
		require.NotNil(t, got, "%v", toolName)
		assert.Equal(t, "Tool", got.GetTitle(), "%v", toolName)
	}
}

func TestLettaReadControlRequest_AToolInputThatIsNoObjectReadsAsEmpty(t *testing.T) {
	for _, toolInput := range []any{nil, []any{1}, "text", 3} {
		got := agenttest.ControlPromptOf(lettaProvider{}, lettaPermissionPayload(t, contracts.LettaToolBash, toolInput)).GetPermission()
		require.NotNil(t, got, "%v", toolInput)
		assert.Equal(t, "{}", got.GetInputJson(), "%v", toolInput)
		assert.Empty(t, got.GetCommand(), "%v", toolInput)
	}
}

func TestLettaReadControlRequest_AnyOtherPayloadIsTheGenericPermission(t *testing.T) {
	for _, payload := range []string{`{}`, `{"type":"control_request","tool_name":"Bash"}`, `{"type":7}`} {
		agenttest.RequireProto(t, agent.GenericPermissionPrompt(), agenttest.ControlPromptOf(lettaProvider{}, []byte(payload)))
	}
}

// Letta's payload states no `request.input`, so the answer to the Allow
// button carries an empty updated input, and ResolveControlResponse turns
// either decision into the flat approval_response.
func TestLettaAnswerControlRequest_APermissionAllowsOrDeniesWithTheFeedback(t *testing.T) {
	payload := lettaPermissionPayload(t, contracts.LettaToolBash, map[string]any{"command": "ls"})
	allow, err := agenttest.AnswerControl(lettaProvider{}, payload, "perm-call-1", agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"perm-call-1","response":{"behavior":"allow","updatedInput":{}}}}`, string(allow.Content))

	deny, err := agenttest.AnswerControl(lettaProvider{}, payload, "perm-call-1", agenttest.PermissionDecision(contracts.ControlAnswerOptionDeny, "Use a dry run."))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"perm-call-1","response":{"behavior":"deny","message":"Use a dry run."}}}`, string(deny.Content))
	assert.Empty(t, deny.FollowUp, "the deny carries the feedback itself")

	bare, err := agenttest.AnswerControl(lettaProvider{}, payload, "perm-call-1", agenttest.PermissionDecision(contracts.ControlAnswerOptionDeny, ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"perm-call-1","response":{"behavior":"deny","message":"`+contracts.ControlAnswerMessageRejectedByUser+`"}}}`, string(bare.Content))

	for name, answer := range map[string]agent.ControlAnswer{"allow": allow, "deny": deny} {
		resolution := agenttest.ResolveAnswer(lettaProvider{}, payload, "perm-call-1", answer)
		require.False(t, resolution.Withhold, name)
		var reply struct {
			Kind      string `json:"kind"`
			RequestID string `json:"request_id"`
			Decision  struct {
				Behavior string `json:"behavior"`
				Message  string `json:"message"`
			} `json:"decision"`
		}
		require.NoError(t, json.Unmarshal(resolution.Content, &reply), name)
		assert.Equal(t, contracts.LettaReplyKindApprovalResponse, reply.Kind, name)
		assert.Equal(t, "perm-call-1", reply.RequestID, name)
		assert.Equal(t, name, reply.Decision.Behavior)
	}
}

func TestLettaAnswerControlRequest_RefusesADecisionThatDoesNotAnswerThePrompt(t *testing.T) {
	payload := lettaPermissionPayload(t, contracts.LettaToolBash, map[string]any{"command": "ls"})
	for name, decision := range map[string]*leapmuxv1.ControlDecision{
		"feedback beside the allow": agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, "Why not."),
		"an option not offered":     agenttest.PermissionDecision("session", ""),
		"a question decision":       agenttest.CancelQuestions(""),
		"no kind":                   {},
	} {
		_, err := agenttest.AnswerControl(lettaProvider{}, payload, "perm-call-1", decision)
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}
	_, err := agenttest.AnswerControl(lettaProvider{}, []byte(lettaPromptQuestionRequest), "letta-question-ask-1", agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a question takes no permission decision")
}

func TestLettaAnswerControlRequest_AQuestionAnswerFoldsTheAnswersIntoTheCallsInput(t *testing.T) {
	sent := lettaAnswerQuestions(t, &leapmuxv1.QuestionAnswer{Selected: []string{"Red"}}, &leapmuxv1.QuestionAnswer{Selected: []string{"Small"}})
	assert.Equal(t, "letta-question-ask-1", sent.Response.RequestID)
	assert.Equal(t, "allow", sent.Response.Response.Behavior)
	var request struct {
		ToolInput struct {
			Questions json.RawMessage `json:"questions"`
		} `json:"tool_input"`
	}
	require.NoError(t, json.Unmarshal([]byte(lettaPromptQuestionRequest), &request))
	assert.JSONEq(t, string(request.ToolInput.Questions), string(sent.Response.Response.UpdatedInput.Questions), "the input of the question call stays")
	assert.Equal(t, map[string]string{"Which color do you prefer?": "Red", "Which sizes?": "Small"}, sent.Response.Response.UpdatedInput.Answers)
}

// Letta Code joins the picks of one multiple-choice answer with a comma and a
// space.
func TestLettaAnswerControlRequest_AMultipleChoiceAnswerSendsEveryPick(t *testing.T) {
	sent := lettaAnswerQuestions(t, &leapmuxv1.QuestionAnswer{Selected: []string{"Red"}}, &leapmuxv1.QuestionAnswer{Selected: []string{"Small", "Large"}})
	assert.Equal(t, "Small, Large", sent.Response.Response.UpdatedInput.Answers["Which sizes?"])
}

// A multiple-choice answer states each pick and the typed words, as one more
// value, and ResolveControlResponse hands all three to Letta Code.
func TestLettaAnswerControlRequest_AMultipleChoiceAnswerSendsEveryPickAndTheTypedWords(t *testing.T) {
	sent := lettaAnswerQuestions(t, &leapmuxv1.QuestionAnswer{Selected: []string{"Red"}}, &leapmuxv1.QuestionAnswer{Selected: []string{"Small", "Large"}, CustomText: " XL "})
	assert.Equal(t, "Small, Large, XL", sent.Response.Response.UpdatedInput.Answers["Which sizes?"])
}

// The single-choice question takes a pick or typed words, never both.
func TestLettaAnswerControlRequest_RefusesAPickAndTypedWordsForASingleChoice(t *testing.T) {
	_, err := agenttest.AnswerControl(lettaProvider{}, []byte(lettaPromptQuestionRequest), "letta-question-ask-1", agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"Red"}, CustomText: "Green"}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
}

func TestLettaAnswerControlRequest_AnAnswerWithNoPickSendsTheTypedText(t *testing.T) {
	sent := lettaAnswerQuestions(t, &leapmuxv1.QuestionAnswer{CustomText: "Green"}, &leapmuxv1.QuestionAnswer{Selected: []string{"Medium"}})
	assert.Equal(t, map[string]string{"Which color do you prefer?": "Green", "Which sizes?": "Medium"}, sent.Response.Response.UpdatedInput.Answers)
}

func TestLettaAnswerControlRequest_RefusesAPickThatTheQuestionDoesNotOffer(t *testing.T) {
	_, err := agenttest.AnswerControl(lettaProvider{}, []byte(lettaPromptQuestionRequest), "letta-question-ask-1", agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"Green"}}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
	_, err = agenttest.AnswerControl(lettaProvider{}, []byte(lettaPromptQuestionRequest), "letta-question-ask-1", agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"Blue", "Red"}}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "the color question takes one pick")
}

func TestLettaAnswerControlRequest_ADismissalDeniesWithTheTypedReason(t *testing.T) {
	got, err := agenttest.AnswerControl(lettaProvider{}, []byte(lettaPromptQuestionRequest), "letta-question-ask-1", agenttest.CancelQuestions("Ask me later."))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"letta-question-ask-1","response":{"behavior":"deny","message":"Ask me later."}}}`, string(got.Content))
	assert.Empty(t, got.FollowUp, "ResolveControlResponse sends the reason after the dismissal")
}

// The answer is the neutral envelope that ResolveControlResponse reads today:
// an answer becomes Letta Code's answered response, and a dismissal its
// dismissed response with the reason as the next message.
func TestLettaAnswerControlRequest_AQuestionResolvesIntoLettasOwnResponse(t *testing.T) {
	resolve := func(answer agent.ControlAnswer) (contracts.LettaQuestionResponse, agent.ControlResponseResolution) {
		t.Helper()
		resolution := agenttest.ResolveAnswer(lettaProvider{}, []byte(lettaPromptQuestionRequest), "letta-question-ask-1", answer)
		require.False(t, resolution.Withhold, "%v", resolution.Refusal())
		var response contracts.LettaQuestionResponse
		require.NoError(t, json.Unmarshal(resolution.Content, &response))
		return response, resolution
	}

	answer, err := agenttest.AnswerControl(lettaProvider{}, []byte(lettaPromptQuestionRequest), "letta-question-ask-1", agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"Red"}}, &leapmuxv1.QuestionAnswer{Selected: []string{"Small", "Large"}, CustomText: "XL"}))
	require.NoError(t, err)
	response, _ := resolve(answer)
	assert.Equal(t, contracts.LettaQuestionStatusAnswered, response.FieldStatus)
	assert.Equal(t, "ask-1", response.FieldToolCallID)
	assert.Equal(t, map[string]string{"Which color do you prefer?": "Red", "Which sizes?": "Small, Large, XL"}, response.FieldAnswers)

	dismissal, err := agenttest.AnswerControl(lettaProvider{}, []byte(lettaPromptQuestionRequest), "letta-question-ask-1", agenttest.CancelQuestions("Ask me later."))
	require.NoError(t, err)
	response, resolution := resolve(dismissal)
	assert.Equal(t, contracts.LettaQuestionStatusDismissed, response.FieldStatus)
	assert.Equal(t, agent.FeedbackFollowUp("Ask me later."), resolution.FollowUp)
}
