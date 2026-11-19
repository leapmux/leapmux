package codewhale

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

// codewhaleTestRequestID is the stored id that answerCodewhale gives a payload
// that states no request_id.
const codewhaleTestRequestID = "approval:ap1"

// The stored control requests below have the shape that buildControlPayload
// publishes: the shared `request` header beside the runtime's own event.

// codewhaleTestQuestions are the questions of a stored question.
func codewhaleTestQuestions() []any {
	return []any{
		map[string]any{
			"header":   "Color",
			"id":       "color",
			"question": "Which color?",
			"options": []any{
				map[string]any{"label": "Red", "description": "Warm"},
				map[string]any{"label": "Blue", "description": "Cool"},
			},
			"allow_free_text": true,
			"multi_select":    false,
		},
		map[string]any{
			"header":   "Sizes",
			"id":       "sizes",
			"question": "Which sizes?",
			"options": []any{
				map[string]any{"label": "S"},
				map[string]any{"label": "M"},
			},
			"multi_select": true,
		},
	}
}

func codewhaleTestEvent(event string, payload map[string]any) map[string]any {
	return map[string]any{
		"schema_version": 1, "seq": 1, "event": event, "kind": event,
		"thread_id": "thread-1", "turn_id": "turn-1", "item_id": "item-1", "payload": payload,
	}
}

// codewhaleApprovalPayload is a stored approval of one tool call. fields
// overlay the event payload.
func codewhaleApprovalPayload(toolName string, input map[string]any, fields map[string]any) map[string]any {
	approval := map[string]any{
		"id": "ap1", "approval_id": "ap1", "tool_call_id": "call-1", "tool_name": toolName,
		"description": "The tool's static description.", "intent_summary": nil,
	}
	for key, value := range fields {
		approval[key] = value
	}
	return map[string]any{
		"type":       "control_request",
		"request_id": "approval:ap1",
		"request":    map[string]any{"tool_name": toolName, "tool_use_id": "call-1", "input": input},
		"event":      codewhaleTestEvent(contracts.CodewhaleEventApprovalRequired, approval),
	}
}

// codewhaleQuestionPayload is a stored question.
func codewhaleQuestionPayload(questions []any) map[string]any {
	request := map[string]any{"questions": questions}
	return map[string]any{
		"type":       "control_request",
		"request_id": "user_input:q1",
		"request":    map[string]any{"tool_name": contracts.CodewhaleToolRequestUserInput, "tool_use_id": "q1", "input": request},
		"event":      codewhaleTestEvent(contracts.CodewhaleEventUserInputRequired, map[string]any{"id": "q1", "request": request}),
	}
}

func codewhaleJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return encoded
}

func codewhalePrompt(t *testing.T, payload any) *leapmuxv1.ControlPrompt {
	t.Helper()
	return agenttest.ControlPromptOf(codewhaleProvider{}, codewhaleJSON(t, payload))
}

// requireCodewhalePermission compares a permission prompt with its input
// apart, as JSON.
func requireCodewhalePermission(t *testing.T, want *leapmuxv1.PermissionPrompt, wantInput string, got *leapmuxv1.ControlPrompt) {
	t.Helper()
	require.NotNil(t, got.GetPermission(), "a permission prompt")
	assert.JSONEq(t, wantInput, got.GetPermission().GetInputJson())
	compared := proto.Clone(got).(*leapmuxv1.ControlPrompt)
	compared.GetPermission().InputJson = ""
	want.Options = agent.AllowDenyOptions()
	agenttest.RequireProto(t, agenttest.PermissionPrompt(want), compared)
}

func TestReadControlRequest_CodewhaleCommandApprovalStatesTheCommand(t *testing.T) {
	requireCodewhalePermission(t, &leapmuxv1.PermissionPrompt{Title: contracts.CodewhaleToolBash, Command: "touch x"}, `{"command":"touch x"}`,
		codewhalePrompt(t, codewhaleApprovalPayload(contracts.CodewhaleToolBash, map[string]any{"command": "touch x"}, nil)))
}

func TestReadControlRequest_CodewhaleStatesTheIntentAndNeverTheStaticDescription(t *testing.T) {
	requireCodewhalePermission(t, &leapmuxv1.PermissionPrompt{Title: contracts.CodewhaleToolApplyPatch, Reason: "Rename the helper"}, `{"patch":"x"}`,
		codewhalePrompt(t, codewhaleApprovalPayload(contracts.CodewhaleToolApplyPatch, map[string]any{"patch": "x"}, map[string]any{"intent_summary": "Rename the helper"})))
}

func TestReadControlRequest_CodewhaleIgnoresTheObsoleteSummary(t *testing.T) {
	requireCodewhalePermission(t, &leapmuxv1.PermissionPrompt{Title: contracts.CodewhaleToolWrite}, `{"path":"a"}`,
		codewhalePrompt(t, codewhaleApprovalPayload(contracts.CodewhaleToolWrite, map[string]any{"path": "a"}, map[string]any{"summary": "Write a"})))
	assert.Equal(t, "Create the file", codewhalePrompt(t, codewhaleApprovalPayload(contracts.CodewhaleToolWrite, map[string]any{"path": "a"},
		map[string]any{"intent_summary": "Create the file", "summary": "Write a"})).GetPermission().GetReason())
	// Blank words are no reason.
	requireCodewhalePermission(t, &leapmuxv1.PermissionPrompt{Title: contracts.CodewhaleToolWrite}, `{"path":"a"}`,
		codewhalePrompt(t, codewhaleApprovalPayload(contracts.CodewhaleToolWrite, map[string]any{"path": "a"}, map[string]any{"intent_summary": "", "summary": ""})))
}

// The command-tool table decides which tools run a command. A command tool
// that states its input in another argument, and a file tool that carries a
// `command` key, both state their arguments alone.
func TestReadControlRequest_CodewhaleStatesACommandOnlyForACommandToolWithACommandArgument(t *testing.T) {
	requireCodewhalePermission(t, &leapmuxv1.PermissionPrompt{Title: contracts.CodewhaleToolTerminalSend}, `{"terminal_id":"t1","input":"y\n"}`,
		codewhalePrompt(t, codewhaleApprovalPayload(contracts.CodewhaleToolTerminalSend, map[string]any{"terminal_id": "t1", "input": "y\n"}, nil)))
	requireCodewhalePermission(t, &leapmuxv1.PermissionPrompt{Title: contracts.CodewhaleToolWrite}, `{"path":"a","command":"rm -rf /"}`,
		codewhalePrompt(t, codewhaleApprovalPayload(contracts.CodewhaleToolWrite, map[string]any{"path": "a", "command": "rm -rf /"}, nil)))
	requireCodewhalePermission(t, &leapmuxv1.PermissionPrompt{Title: contracts.CodewhaleToolBash}, `{"command":""}`,
		codewhalePrompt(t, codewhaleApprovalPayload(contracts.CodewhaleToolBash, map[string]any{"command": ""}, nil)))
	for _, tool := range []string{
		contracts.CodewhaleToolBashAlias, contracts.CodewhaleToolTerminalRun, contracts.CodewhaleToolCodeExecution,
		contracts.CodewhaleToolJsExecution, contracts.CodewhaleToolPandocConvert, contracts.CodewhaleToolGit, contracts.CodewhaleToolRun,
	} {
		assert.Equal(t, "go test", codewhalePrompt(t, codewhaleApprovalPayload(tool, map[string]any{"command": "go test"}, nil)).GetPermission().GetCommand(), tool)
	}
}

func TestReadControlRequest_CodewhaleHeaderWithNoRequestStatesAnEmptyInput(t *testing.T) {
	payload := codewhaleApprovalPayload(contracts.CodewhaleToolBash, map[string]any{}, nil)
	delete(payload, "request")
	requireCodewhalePermission(t, &leapmuxv1.PermissionPrompt{Title: contracts.CodewhaleToolBash}, `{}`, codewhalePrompt(t, payload))
}

func TestReadControlRequest_CodewhaleTakesTheToolNameFromTheEventWhenTheHeaderLostIt(t *testing.T) {
	payload := codewhaleApprovalPayload(contracts.CodewhaleToolBash, map[string]any{}, nil)
	payload["request"] = map[string]any{"input": map[string]any{}}
	assert.Equal(t, contracts.CodewhaleToolBash, codewhalePrompt(t, payload).GetPermission().GetTitle())
}

func TestReadControlRequest_CodewhaleReadsNoPermissionOutOfAQuestionOrAPayloadWithNoEvent(t *testing.T) {
	_, ok := codewhalePermissionPrompt(codewhaleJSON(t, codewhaleQuestionPayload(codewhaleTestQuestions())))
	assert.False(t, ok, "a question is no permission")
	approvalWithTextEvent := codewhaleApprovalPayload(contracts.CodewhaleToolBash, map[string]any{}, nil)
	approvalWithTextEvent["event"] = contracts.CodewhaleEventApprovalRequired
	for _, payload := range []any{map[string]any{}, approvalWithTextEvent} {
		_, ok := codewhalePermissionPrompt(codewhaleJSON(t, payload))
		assert.False(t, ok)
		agenttest.RequireProto(t, agent.GenericPermissionPrompt(), codewhalePrompt(t, payload))
	}
	agenttest.RequireProto(t, agent.GenericPermissionPrompt(), agenttest.ControlPromptOf(codewhaleProvider{}, []byte("not json")))
}

func TestReadControlRequest_CodewhaleRecognizesAQuestionByItsEventAndItsTool(t *testing.T) {
	assert.NotNil(t, codewhalePrompt(t, codewhaleQuestionPayload(codewhaleTestQuestions())).GetQuestion())

	assert.Nil(t, codewhalePrompt(t, codewhaleApprovalPayload(contracts.CodewhaleToolBash, map[string]any{}, nil)).GetQuestion())
	assert.Nil(t, codewhalePrompt(t, codewhaleApprovalPayload(contracts.CodewhaleToolRequestUserInput, map[string]any{}, nil)).GetQuestion(),
		"an approval of the question tool is no question")
	otherTool := codewhaleQuestionPayload(codewhaleTestQuestions())
	otherTool["request"] = map[string]any{"tool_name": contracts.CodewhaleToolBash}
	agenttest.RequireProto(t, agent.GenericPermissionPrompt(), codewhalePrompt(t, otherTool))
}

func TestReadControlRequest_CodewhaleReadsEachQuestionWithItsIdOptionsAndMultipleChoice(t *testing.T) {
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
		Questions: []*leapmuxv1.ControlQuestion{
			{
				Id: "color", Question: "Which color?", Header: "Color",
				Options: []*leapmuxv1.QuestionOption{{Label: "Red", Description: "Warm"}, {Label: "Blue", Description: "Cool"}},
			},
			{
				Id: "sizes", Question: "Which sizes?", Header: "Sizes", MultiSelect: true,
				Options: []*leapmuxv1.QuestionOption{{Label: "S"}, {Label: "M"}},
			},
		},
	}}}, codewhalePrompt(t, codewhaleQuestionPayload(codewhaleTestQuestions())))
}

func TestReadControlRequest_CodewhaleDropsAQuestionThatTheRuntimeCouldNeverMatch(t *testing.T) {
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
		Questions: []*leapmuxv1.ControlQuestion{{Id: "h", Question: "Header only", Options: []*leapmuxv1.QuestionOption{}}},
	}}}, codewhalePrompt(t, codewhaleQuestionPayload([]any{
		map[string]any{"question": "No id?"},
		map[string]any{"id": "x"},
		"x",
		map[string]any{"id": "h", "header": "Header only", "options": []any{map[string]any{"description": "no label"}}},
	})))
}

func codewhaleQuestionIDs(t *testing.T, payload any) []string {
	t.Helper()
	ids := []string{}
	for _, question := range codewhalePrompt(t, payload).GetQuestion().GetQuestions() {
		ids = append(ids, question.GetId())
	}
	return ids
}

func TestReadControlRequest_CodewhaleFallsBackToTheStoredEventWhenTheHeaderLostTheQuestions(t *testing.T) {
	payload := codewhaleQuestionPayload(codewhaleTestQuestions())
	payload["request"] = map[string]any{"tool_name": contracts.CodewhaleToolRequestUserInput, "input": map[string]any{}}
	assert.Equal(t, []string{"color", "sizes"}, codewhaleQuestionIDs(t, payload))
	assert.Empty(t, codewhaleQuestionRecords([]byte(`{}`)))
}

// The header is what the Worker stored for the control to answer, so it wins
// over the event whenever it states a list, even an empty one.
func TestReadControlRequest_CodewhaleReadsTheHeadersListOverTheStoredEvents(t *testing.T) {
	payload := codewhaleQuestionPayload(codewhaleTestQuestions())
	payload["request"] = map[string]any{
		"tool_name": contracts.CodewhaleToolRequestUserInput,
		"input":     map[string]any{"questions": []any{map[string]any{"id": "only", "question": "Only this?"}}},
	}
	assert.Equal(t, []string{"only"}, codewhaleQuestionIDs(t, payload))

	payload["request"] = map[string]any{"tool_name": contracts.CodewhaleToolRequestUserInput, "input": map[string]any{"questions": []any{}}}
	question := codewhalePrompt(t, payload).GetQuestion()
	require.NotNil(t, question)
	assert.Empty(t, question.GetQuestions())
}

func TestReadControlRequest_CodewhaleDropsARecordThatIsNoObject(t *testing.T) {
	first := codewhaleTestQuestions()[0]
	assert.Equal(t, []string{"color"}, codewhaleQuestionIDs(t, codewhaleQuestionPayload([]any{first, nil, 7, "x"})))
	assert.Empty(t, codewhaleQuestionRecords(codewhaleJSON(t, map[string]any{
		"request": map[string]any{"tool_name": contracts.CodewhaleToolRequestUserInput, "input": map[string]any{"questions": "Which color?"}},
	})), "a list in another shape reads as none")
}

func answerCodewhale(t *testing.T, payload any, decision *leapmuxv1.ControlDecision) (agent.ControlAnswer, error) {
	t.Helper()
	return agenttest.AnswerControl(codewhaleProvider{}, codewhaleJSON(t, payload), codewhaleStoredRequestID(payload), decision)
}

// codewhaleStoredRequestID is the id that the Worker stores payload under: its
// request_id, or codewhaleTestRequestID for a payload that states none.
func codewhaleStoredRequestID(payload any) string {
	if fields, ok := payload.(map[string]any); ok {
		if id, ok := fields["request_id"].(string); ok {
			return id
		}
	}
	return codewhaleTestRequestID
}

// codewhaleSentAnswers reads `updatedInput.answers` of an answer.
func codewhaleSentAnswers(t *testing.T, content []byte) []map[string]string {
	t.Helper()
	var envelope struct {
		Response struct {
			Response struct {
				Behavior     string `json:"behavior"`
				UpdatedInput struct {
					Answers []map[string]string `json:"answers"`
				} `json:"updatedInput"`
			} `json:"response"`
		} `json:"response"`
	}
	require.NoError(t, json.Unmarshal(content, &envelope))
	assert.Equal(t, contracts.ControlAnswerBehaviorAllow, envelope.Response.Response.Behavior)
	return envelope.Response.Response.UpdatedInput.Answers
}

func TestAnswerControlRequest_CodewhaleSendsTheRuntimesOwnAnswerList(t *testing.T) {
	payload := codewhaleQuestionPayload(codewhaleTestQuestions())
	got, err := answerCodewhale(t, payload, agenttest.QuestionDecision(
		&leapmuxv1.QuestionAnswer{Selected: []string{"Blue"}},
		&leapmuxv1.QuestionAnswer{Selected: []string{"S", "M"}},
	))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"user_input:q1","response":{
		"behavior":"allow",
		"updatedInput":{
			"questions":[
				{"header":"Color","id":"color","question":"Which color?","options":[{"label":"Red","description":"Warm"},{"label":"Blue","description":"Cool"}],"allow_free_text":true,"multi_select":false},
				{"header":"Sizes","id":"sizes","question":"Which sizes?","options":[{"label":"S"},{"label":"M"}],"multi_select":true}
			],
			"answers":[{"id":"color","label":"Blue","value":"Blue"},{"id":"sizes","label":"S","value":"S"},{"id":"sizes","label":"M","value":"M"}]
		}}}}`, string(got.Content))
	assert.Nil(t, got.PlanApproval)
	assert.Empty(t, got.FollowUp)
}

func TestAnswerControlRequest_CodewhaleSendsOneEntryForEachChosenOptionAndTextAsOther(t *testing.T) {
	got, err := answerCodewhale(t, codewhaleQuestionPayload(codewhaleTestQuestions()), agenttest.QuestionDecision(
		&leapmuxv1.QuestionAnswer{CustomText: " Green "},
		&leapmuxv1.QuestionAnswer{Selected: []string{"S", "M"}},
	))
	require.NoError(t, err)
	assert.Equal(t, []map[string]string{
		{"id": "color", "label": "Other", "value": "Green"},
		{"id": "sizes", "label": "S", "value": "S"},
		{"id": "sizes", "label": "M", "value": "M"},
	}, codewhaleSentAnswers(t, got.Content))
}

// A multiple choice takes its options and its typed text together: the user
// chose both, and the text answers as one more entry, labelled Other.
func TestAnswerControlRequest_CodewhaleMultipleChoiceSendsItsOptionsAndItsText(t *testing.T) {
	got, err := answerCodewhale(t, codewhaleQuestionPayload(codewhaleTestQuestions()), agenttest.QuestionDecision(
		&leapmuxv1.QuestionAnswer{Selected: []string{"Red"}},
		&leapmuxv1.QuestionAnswer{Selected: []string{"S", "M"}, CustomText: "  XL  "},
	))
	require.NoError(t, err)
	assert.Equal(t, []map[string]string{
		{"id": "color", "label": "Red", "value": "Red"},
		{"id": "sizes", "label": "S", "value": "S"},
		{"id": "sizes", "label": "M", "value": "M"},
		{"id": "sizes", "label": "Other", "value": "XL"},
	}, codewhaleSentAnswers(t, got.Content))
}

// A single choice takes an option or typed text, never both, and text with
// no words is no answer.
func TestAnswerControlRequest_CodewhaleSingleChoiceTakesAnOptionOrTextAndRefusesAnUnansweredQuestion(t *testing.T) {
	_, err := answerCodewhale(t, codewhaleQuestionPayload(codewhaleTestQuestions()), agenttest.QuestionDecision(
		&leapmuxv1.QuestionAnswer{Selected: []string{"Red"}, CustomText: "Crimson"},
	))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)

	_, err = answerCodewhale(t, codewhaleQuestionPayload(codewhaleTestQuestions()), agenttest.QuestionDecision(
		&leapmuxv1.QuestionAnswer{Selected: []string{"Red"}, CustomText: " \t"},
		&leapmuxv1.QuestionAnswer{CustomText: "  "},
	))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a question with blank text alone has no answer")
}

// An answer that leaves every question open is no answer: the browser never
// sends one, so the Worker refuses it. A cancel leaves the questions.
func TestAnswerControlRequest_CodewhaleRefusesNoAnswer(t *testing.T) {
	_, err := answerCodewhale(t, codewhaleQuestionPayload(codewhaleTestQuestions()), agenttest.QuestionDecision())
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
}

func TestAnswerControlRequest_CodewhaleDeclinesAQuestionWithTheReason(t *testing.T) {
	got, err := answerCodewhale(t, codewhaleQuestionPayload(codewhaleTestQuestions()), agenttest.CancelQuestions("User stopped"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"user_input:q1","response":{"behavior":"deny","message":"User stopped"}}}`, string(got.Content))
	assert.Empty(t, got.FollowUp)
}

// The composer's typed text refuses, and the Allow button approves. The Deny
// button refuses with the placeholder, which the Worker reads as no reason.
func TestAnswerControlRequest_CodewhaleAnswersAnApproval(t *testing.T) {
	payload := codewhaleApprovalPayload(contracts.CodewhaleToolBash, map[string]any{"command": "ls"}, nil)
	got, err := answerCodewhale(t, payload, agenttest.PermissionDecision(contracts.ControlAnswerOptionDeny, "Use a dry run."))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"approval:ap1","response":{"behavior":"deny","message":"Use a dry run."}}}`, string(got.Content))
	assert.Empty(t, got.FollowUp, "ResolveControlResponse sends the feedback as the next message, so the answer states none")

	got, err = answerCodewhale(t, payload, agenttest.PermissionDecision(contracts.ControlAnswerOptionDeny, ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"approval:ap1","response":{"behavior":"deny","message":"Rejected by user."}}}`, string(got.Content))

	got, err = answerCodewhale(t, payload, agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"approval:ap1","response":{"behavior":"allow","updatedInput":{"command":"ls"}}}}`, string(got.Content))
}

func TestAnswerControlRequest_CodewhaleRefusesADecisionThatThePromptDoesNotTake(t *testing.T) {
	approval := codewhaleApprovalPayload(contracts.CodewhaleToolBash, map[string]any{}, nil)
	question := codewhaleQuestionPayload(codewhaleTestQuestions())
	for name, test := range map[string]struct {
		payload  any
		decision *leapmuxv1.ControlDecision
	}{
		"an approval with feedback":                  {approval, agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, "a note")},
		"an unknown option":                          {approval, agenttest.PermissionDecision("always", "")},
		"a question decision for a permission":       {approval, agenttest.QuestionDecision()},
		"a permission decision for a question":       {question, agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, "")},
		"an option that the question does not offer": {question, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"Green"}})},
		"two options of a single choice":             {question, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"Red", "Blue"}})},
		"a plan decision":                            {approval, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true})},
	} {
		_, err := answerCodewhale(t, test.payload, test.decision)
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}
}

// ResolveControlResponse reads the answer into the runtime's reply frame, so
// the answer is one that the Worker accepts.
func TestAnswerControlRequest_CodewhaleAnswersResolveIntoTheRuntimesReply(t *testing.T) {
	resolve := func(payload any, answer agent.ControlAnswer) (codewhaleReplyFrame, agent.ControlResponseResolution) {
		t.Helper()
		resolution := agenttest.ResolveAnswer(codewhaleProvider{}, codewhaleJSON(t, payload), codewhaleStoredRequestID(payload), answer)
		require.NoError(t, resolution.Refusal())
		var frame codewhaleReplyFrame
		require.NoError(t, json.Unmarshal(resolution.Content, &frame))
		return frame, resolution
	}

	approval := codewhaleApprovalPayload(contracts.CodewhaleToolBash, map[string]any{"command": "ls"}, nil)
	answer, err := answerCodewhale(t, approval, agenttest.PermissionDecision(contracts.ControlAnswerOptionDeny, "Use a dry run."))
	require.NoError(t, err)
	frame, resolution := resolve(approval, answer)
	assert.Equal(t, codewhaleReplyFrame{Frame: contracts.CodewhaleReplyFrameApproval, ApprovalID: "ap1", Decision: contracts.CodewhaleDecisionDeny}, frame)
	assert.Equal(t, agent.FeedbackFollowUp("Use a dry run."), resolution.FollowUp)

	question := codewhaleQuestionPayload(codewhaleTestQuestions())
	answer, err = answerCodewhale(t, question, agenttest.QuestionDecision(
		&leapmuxv1.QuestionAnswer{CustomText: "Green"},
		&leapmuxv1.QuestionAnswer{Selected: []string{"M"}, CustomText: "XL"},
	))
	require.NoError(t, err)
	frame, _ = resolve(question, answer)
	assert.Equal(t, []userInputAnswer{{ID: "color", Label: "Other", Value: "Green"}, {ID: "sizes", Label: "M", Value: "M"}, {ID: "sizes", Label: "Other", Value: "XL"}}, frame.Answers)
	assert.Equal(t, "q1", frame.InputID)
}
