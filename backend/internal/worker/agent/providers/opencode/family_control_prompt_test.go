package opencode

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

// These cases read and answer the control requests of the family's Agent
// Client Protocol transport, which Kilo speaks (FamilyProvider). Kilo's own
// package states the same promises against kiloProvider, and
// control_prompt_test.go states OpenCode's native ones.

const familyShellCommand = "npm test -- --runInBand"

// familyPermissionRequest is a permission request with options of the
// runtime's own vocabulary.
const familyPermissionRequest = `{"jsonrpc":"2.0","id":3,"method":"session/request_permission","params":{"sessionId":"s",
	"toolCall":{"toolCallId":"c","kind":"execute","title":"Run","rawInput":{"command":"ls"}},
	"options":[{"optionId":"allow-1","name":"Allow","kind":"allow_once"},{"optionId":"reject-1","name":"Reject","kind":"reject_once"}]}}`

// familyOptionLessRequest is the same request with no option list.
const familyOptionLessRequest = `{"jsonrpc":"2.0","id":3,"method":"session/request_permission","params":{"sessionId":"s",
	"toolCall":{"toolCallId":"c","kind":"execute","title":"Run","rawInput":{"command":"ls"}}}}`

// familyDaemonOptionsRequest offers the daemon's own three answers.
const familyDaemonOptionsRequest = `{"method":"session/request_permission","params":{
	"toolCall":{"toolCallId":"c","kind":"other","title":"Write"},
	"options":[{"optionId":"once","kind":"allow_once","name":"Allow once"},{"optionId":"always","kind":"allow_always","name":"Always allow"},{"optionId":"reject","kind":"reject_once","name":"Reject"}]}}`

func familyOptionIDs(prompt *leapmuxv1.ControlPrompt) []string {
	var ids []string
	for _, option := range prompt.GetPermission().GetOptions() {
		ids = append(ids, option.GetOptionId())
	}
	return ids
}

// The question bridge publishes the daemon's own event, and this reader turns
// it back into a question. The two must agree exactly, or the banner never
// opens.
func TestFamilyReadControlRequest_ReadsTheQuestionThatTheBridgePublishes(t *testing.T) {
	got := agenttest.ControlPromptOf(FamilyProvider{}, []byte(`{"type":"question.asked","properties":{"id":"que_1","sessionID":"ses_9","questions":[
		{"question":"Which one?","header":"Task","options":[{"label":"Inspect","description":"Look only"}],"multiple":true}]}}`))
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
		Questions: []*leapmuxv1.ControlQuestion{{
			Question: "Which one?", Header: "Task", MultiSelect: true,
			Options: []*leapmuxv1.QuestionOption{{Label: "Inspect", Description: "Look only"}},
		}},
	}}}, got)

	got = agenttest.ControlPromptOf(FamilyProvider{}, []byte(`{"type":"question.asked","properties":{"questions":[]}}`))
	require.NotNil(t, got.GetQuestion(), "a question event with no question is still a question")
	assert.Empty(t, got.GetQuestion().GetQuestions())
}

func TestFamilyReadControlRequest_ANonQuestionIsNoQuestion(t *testing.T) {
	for _, payload := range []string{
		`{"type":"question.replied","properties":{"requestID":"que_1"}}`,
		`{"type":"permission.asked","properties":{"id":"per_1"}}`,
		`{}`,
	} {
		got := agenttest.ControlPromptOf(FamilyProvider{}, []byte(payload))
		require.NotNil(t, got.GetPermission(), payload)
		assert.Equal(t, []string{contracts.ControlAnswerOptionAllow, contracts.ControlAnswerOptionDeny}, familyOptionIDs(got), "%s reads as the generic permission", payload)
	}

	got := agenttest.ControlPromptOf(FamilyProvider{}, []byte(`{"method":"requestPermission","params":{"toolCall":{"toolCallId":"tc-1"}}}`))
	assert.Nil(t, got.GetQuestion())
	assert.Equal(t, []string{openCodeOptionOnce, openCodeOptionReject}, familyOptionIDs(got), "a permission request is a permission")
}

func TestFamilyReadControlRequest_AShellPermissionStatesItsCommandAndItsArguments(t *testing.T) {
	got := agenttest.ControlPromptOf(FamilyProvider{}, []byte(`{"method":"session/request_permission","params":{"toolCall":{"toolCallId":"command","kind":"execute","title":"Run command",
		"rawInput":{"command":"printf \"first line\\n\"\nprintf \"second line\\n\"","timeout":0,"quiet":false,"note":""}}}}`))
	permission := got.GetPermission()
	require.NotNil(t, permission)
	assert.Equal(t, "Run command", permission.GetTitle(), "the request identifies its operation")
	assert.Equal(t, "printf \"first line\\n\"\nprintf \"second line\\n\"", permission.GetCommand())
	assert.JSONEq(t, `{"command":"printf \"first line\\n\"\nprintf \"second line\\n\"","timeout":0,"quiet":false,"note":""}`, permission.GetInputJson())

	got = agenttest.ControlPromptOf(FamilyProvider{}, []byte(`{"params":{"toolCall":{"toolCallId":"c","kind":"execute","title":"Run","rawInput":{"command":"`+familyShellCommand+`"}}}}`))
	assert.Equal(t, familyShellCommand, got.GetPermission().GetCommand())
	assert.Equal(t, "Run", got.GetPermission().GetTitle())
}

// The daemon answers a request with no option list with its own two ids, and
// reads any other id as a refusal, so the prompt offers exactly those two.
func TestFamilyReadControlRequest_ARequestWithNoOptionsOffersTheDaemonPair(t *testing.T) {
	want := []*leapmuxv1.PermissionOption{
		{OptionId: "once", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, Name: "Allow"},
		{OptionId: "reject", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, Name: "Deny"},
	}
	for _, payload := range []string{
		`{"params":{"toolCall":{"toolCallId":"c","kind":"other","title":"Write"}}}`,
		`{"params":{"toolCall":{"toolCallId":"c","kind":"other","title":"Write"},"options":[]}}`,
		`{"params":{"toolCall":{"toolCallId":"c","kind":"other","title":"Write"},"options":[null,"allow_once",7]}}`,
		familyOptionLessRequest,
	} {
		permission := agenttest.ControlPromptOf(FamilyProvider{}, []byte(payload)).GetPermission()
		require.NotNil(t, permission, payload)
		require.Len(t, permission.GetOptions(), len(want), payload)
		for i := range want {
			assert.Truef(t, proto.Equal(want[i], permission.GetOptions()[i]), "%s: option %d is %v", payload, i, permission.GetOptions()[i])
		}
	}
}

func TestFamilyReadControlRequest_KeepsTheOptionsThatTheRequestOffers(t *testing.T) {
	prompt := agenttest.ControlPromptOf(FamilyProvider{}, []byte(familyDaemonOptionsRequest))
	permission := prompt.GetPermission()
	require.NotNil(t, permission)
	assert.Equal(t, []string{"once", "always", "reject"}, familyOptionIDs(prompt))
	assert.Equal(t, []leapmuxv1.PermissionOptionKind{
		leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE,
		leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS,
		leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE,
	}, []leapmuxv1.PermissionOptionKind{permission.GetOptions()[0].GetKind(), permission.GetOptions()[1].GetKind(), permission.GetOptions()[2].GetKind()})
	assert.Equal(t, "Always allow", permission.GetOptions()[1].GetName())

	assert.Equal(t, []string{"allow-1", "reject-1"}, familyOptionIDs(agenttest.ControlPromptOf(FamilyProvider{}, []byte(familyPermissionRequest))))
}

// The composer can still refuse a request that offers no reject option, with
// the protocol's cancelled outcome, so the prompt offers that refusal.
func TestFamilyReadControlRequest_AListWithNoRejectOptionGainsTheCancel(t *testing.T) {
	got := agenttest.ControlPromptOf(FamilyProvider{}, []byte(`{"params":{"toolCall":{"toolCallId":"c","title":"Write"},"options":[{"optionId":"once","kind":"allow_once"}]}}`))
	assert.Equal(t, []string{"once", acp.CancelOptionID}, familyOptionIDs(got))
}

// The daemon states the arguments as it received them, a string included.
func TestFamilyReadControlRequest_KeepsTheArgumentsAsTheRequestStatesThem(t *testing.T) {
	got := agenttest.ControlPromptOf(FamilyProvider{}, []byte(`{"params":{"toolCall":{"title":"lookup","kind":"other","rawInput":"{\"filters\":{\"limit\":0,\"enabled\":false}}"}}}`))
	assert.Equal(t, `"{\"filters\":{\"limit\":0,\"enabled\":false}}"`, got.GetPermission().GetInputJson(), "a string argument stays a string")

	got = agenttest.ControlPromptOf(FamilyProvider{}, []byte(`{"params":{"toolCall":{"title":"apply_patch","kind":"edit","rawInput":"*** Begin Patch\n*** Add File: file.ts\n+const value = 42\n*** End Patch"}}}`))
	assert.Contains(t, got.GetPermission().GetInputJson(), "const value = 42")
	assert.Empty(t, got.GetPermission().GetCommand(), "only an execute call states a command")
}

// The request states its tool call in a compact form, and the arguments live
// on the call's own request row.
func TestFamilyReadControlRequest_MergesTheArgumentsOfTheToolCallRow(t *testing.T) {
	input := agent.NewControlRequestInput([]byte(`{"params":{"toolCall":{"toolCallId":"call","title":"Run checks","rawInput":{"description":"Current description"}}}}`))
	var asked string
	input.SpanRequest = func(spanID string) (agent.MessageContent, bool) {
		asked = spanID
		return agent.MessageContent{Original: []byte(`{"sessionUpdate":"tool_call","toolCallId":"call","kind":"execute",
			"rawInput":{"command":"` + familyShellCommand + `","description":"Old description"}}`)}, true
	}
	permission := FamilyProvider{}.ReadControlRequest(input).GetPermission()
	require.NotNil(t, permission)
	assert.Equal(t, "call", asked, "the prompt loads the row of the call that it is about")
	assert.Equal(t, "Run checks", permission.GetTitle())
	assert.Equal(t, familyShellCommand, permission.GetCommand(), "the row states the kind and the command")
	assert.JSONEq(t, `{"command":"`+familyShellCommand+`","description":"Current description"}`, permission.GetInputJson(),
		"the request's own arguments win over the row's")
}

// A history with no request row keeps what the request itself states.
func TestFamilyReadControlRequest_KeepsTheRequestDetailsWithoutItsRow(t *testing.T) {
	payload := []byte(`{"params":{"toolCall":{"toolCallId":"call","title":"Permission from provider","rawInput":{"command":"pwd"}}}}`)
	for name, row := range map[string]func(string) (agent.MessageContent, bool){
		"no row": func(string) (agent.MessageContent, bool) { return agent.MessageContent{}, false },
		"a result row only": func(string) (agent.MessageContent, bool) {
			return agent.MessageContent{Original: []byte(`{"sessionUpdate":"tool_call_update","toolCallId":"call","status":"completed"}`)}, true
		},
	} {
		input := agent.NewControlRequestInput(payload)
		input.SpanRequest = row
		permission := FamilyProvider{}.ReadControlRequest(input).GetPermission()
		require.NotNil(t, permission, name)
		assert.Equal(t, "Permission from provider", permission.GetTitle(), name)
		assert.JSONEq(t, `{"command":"pwd"}`, permission.GetInputJson(), name)
	}
}

func TestFamilyReadControlRequest_AnAbsentOrMalformedToolCall(t *testing.T) {
	for _, toolCall := range []string{`null`, `[]`} {
		got := agenttest.ControlPromptOf(FamilyProvider{}, []byte(`{"params":{"toolCall":`+toolCall+`}}`))
		assert.Equal(t, []string{contracts.ControlAnswerOptionAllow, contracts.ControlAnswerOptionDeny}, familyOptionIDs(got), "%s with no option is no permission request", toolCall)
		assert.Empty(t, got.GetPermission().GetTitle(), toolCall)
	}
	got := agenttest.ControlPromptOf(FamilyProvider{}, []byte(`{"params":{}}`))
	assert.Equal(t, []string{contracts.ControlAnswerOptionAllow, contracts.ControlAnswerOptionDeny}, familyOptionIDs(got))

	got = agenttest.ControlPromptOf(FamilyProvider{}, []byte(`{"params":{"toolCall":{"title":{"invalid":true},"kind":["execute"]}}}`))
	require.NotNil(t, got.GetPermission(), "a tool call of the wrong shape is still a permission request")
	assert.Empty(t, got.GetPermission().GetTitle(), "a title of the wrong type reads as absent")
	assert.Empty(t, got.GetPermission().GetCommand())
	assert.Equal(t, []string{openCodeOptionOnce, openCodeOptionReject}, familyOptionIDs(got))
}

func TestFamilyReadControlRequest_AnElicitationIsAForm(t *testing.T) {
	got := agenttest.ControlPromptOf(FamilyProvider{}, []byte(`{"jsonrpc":"2.0","id":4,"method":"elicitation/create","params":{"message":"Sign in","server":"github","requestedSchema":{"type":"object"},"title":"Login"}}`))
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Elicitation{Elicitation: &leapmuxv1.ElicitationPrompt{
		Mode: "form", Message: "Sign in", Server: "github", Title: "Login", SchemaJson: `{"type":"object"}`,
	}}}, got)
}

func TestFamilyAnswerControlRequest_SelectsTheChosenOptionUnderTheStoredRequestID(t *testing.T) {
	for _, tc := range []struct{ requestID, optionID string }{
		{"5", "once"}, {"7", "reject"}, {"9", "always"}, {"abc", "once"},
	} {
		got, err := agenttest.AnswerControl(FamilyProvider{}, []byte(familyDaemonOptionsRequest), tc.requestID, agenttest.PermissionDecision(tc.optionID, ""))
		require.NoError(t, err, tc.optionID)
		assert.JSONEq(t, `{"jsonrpc":"2.0","id":"`+tc.requestID+`","result":{"outcome":{"outcome":"selected","optionId":"`+tc.optionID+`"}}}`, string(got.Content))
		assert.Empty(t, got.FollowUp)
	}
}

// The composer refuses with one of the agent's own options, because the
// agent reads nothing else, and the reason follows as a message.
func TestFamilyAnswerControlRequest_ARefusalSelectsARejectOptionAndTheReasonFollows(t *testing.T) {
	got, err := agenttest.AnswerControl(FamilyProvider{}, []byte(familyPermissionRequest), "jsonrpc:3", agenttest.PermissionDecision("reject-1", "use the other file"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:3","result":{"outcome":{"outcome":"selected","optionId":"reject-1"}}}`, string(got.Content))
	assert.Equal(t, "use the other file", got.FollowUp)

	got, err = agenttest.AnswerControl(FamilyProvider{}, []byte(familyPermissionRequest), "jsonrpc:3", agenttest.PermissionDecision("allow-1", ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:3","result":{"outcome":{"outcome":"selected","optionId":"allow-1"}}}`, string(got.Content))

	_, err = agenttest.AnswerControl(FamilyProvider{}, []byte(familyPermissionRequest), "jsonrpc:3", agenttest.PermissionDecision("allow-1", "a note"))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "an approval carries no reason")
}

func TestFamilyAnswerControlRequest_AnswersAnOptionLessRequestWithTheDaemonPair(t *testing.T) {
	got, err := agenttest.AnswerControl(FamilyProvider{}, []byte(familyOptionLessRequest), "jsonrpc:3", agenttest.PermissionDecision(openCodeOptionReject, "use the other file"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:3","result":{"outcome":{"outcome":"selected","optionId":"reject"}}}`, string(got.Content))
	assert.Equal(t, "use the other file", got.FollowUp)

	got, err = agenttest.AnswerControl(FamilyProvider{}, []byte(familyOptionLessRequest), "jsonrpc:3", agenttest.PermissionDecision(openCodeOptionOnce, ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:3","result":{"outcome":{"outcome":"selected","optionId":"once"}}}`, string(got.Content))

	_, err = agenttest.AnswerControl(FamilyProvider{}, []byte(familyOptionLessRequest), "jsonrpc:3", agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "LeapMux's own allow is no answer the daemon reads")
}

func TestFamilyAnswerControlRequest_TheCancelAnswersWithTheCancelledOutcome(t *testing.T) {
	payload := `{"params":{"toolCall":{"toolCallId":"c","title":"Write"},"options":[{"optionId":"once","kind":"allow_once"}]}}`
	got, err := agenttest.AnswerControl(FamilyProvider{}, []byte(payload), "jsonrpc:3", agenttest.PermissionDecision(acp.CancelOptionID, "not this file"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:3","result":{"outcome":{"outcome":"cancelled"}}}`, string(got.Content))
	assert.Equal(t, "not this file", got.FollowUp)
}

// A request that is no permission takes LeapMux's allow and deny in the
// neutral envelope, and the deny carries its own message.
func TestFamilyAnswerControlRequest_TheGenericPromptTakesTheNeutralEnvelope(t *testing.T) {
	got, err := agenttest.AnswerControl(FamilyProvider{}, []byte(`{}`), "r", agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"r","response":{"behavior":"allow","updatedInput":{}}}}`, string(got.Content))

	got, err = agenttest.AnswerControl(FamilyProvider{}, []byte(`{}`), "r", agenttest.PermissionDecision(contracts.ControlAnswerOptionDeny, "no"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"r","response":{"behavior":"deny","message":"no"}}}`, string(got.Content))
	assert.Empty(t, got.FollowUp)
}

// The question bridge reads the answer back by its JSON-RPC id and its
// result (openCodeAnswerResult), so the answer is checked in that shape.
func TestFamilyAnswerControlRequest_AnswersAQuestionInTheShapeThatTheBridgeReads(t *testing.T) {
	payload := `{"type":"question.asked","properties":{"id":"que_1","questions":[
		{"header":"Task","question":"Pick a task","options":[{"label":"Build"}]},
		{"header":"Env","question":"Pick an env","options":[{"label":"Dev"}],"custom":true}]}}`
	requestID := openCodeQuestionControlID("que_1")
	decode := func(t *testing.T, content []byte) openCodeAnswerResult {
		t.Helper()
		_, id, ok := agent.ExtractJSONRPCID(content)
		require.True(t, ok)
		assert.Equal(t, requestID, id, "the bridge finds the question by the stored request id")
		var frame struct {
			Result openCodeAnswerResult `json:"result"`
		}
		require.NoError(t, json.Unmarshal(content, &frame))
		return frame.Result
	}

	got, err := agenttest.AnswerControl(FamilyProvider{}, []byte(payload), requestID, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"Build"}}, &leapmuxv1.QuestionAnswer{CustomText: "Dev"}))
	require.NoError(t, err)
	assert.Equal(t, openCodeAnswerResult{Answers: [][]string{{"Build"}, {"Dev"}}}, decode(t, got.Content))

	got, err = agenttest.AnswerControl(FamilyProvider{}, []byte(payload), requestID, agenttest.CancelQuestions("User stopped"))
	require.NoError(t, err)
	assert.Equal(t, openCodeAnswerResult{Rejected: true}, decode(t, got.Content))
	assert.Empty(t, got.FollowUp)

	// A multiple-choice answer of two options and typed text chose all three,
	// and the typed text is one more value of the answer.
	multiple := `{"type":"question.asked","properties":{"id":"que_1","questions":[
		{"header":"Steps","question":"Which steps?","multiple":true,"options":[{"label":"Build"},{"label":"Test"}]}]}}`
	got, err = agenttest.AnswerControl(FamilyProvider{}, []byte(multiple), requestID, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"Build", "Test"}, CustomText: " Lint "}))
	require.NoError(t, err)
	assert.Equal(t, openCodeAnswerResult{Answers: [][]string{{"Build", "Test", "Lint"}}}, decode(t, got.Content))

	_, err = agenttest.AnswerControl(FamilyProvider{}, []byte(payload), requestID, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"Build"}, CustomText: "Deploy"}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a single choice takes an option or typed text, not both")
}

// The protocol's elicitation answer passes the provider's own resolution,
// which turns it into the JSON-RPC result of the stored request.
func TestFamilyAnswerControlRequest_AnswersAnElicitationThatTheResolutionAccepts(t *testing.T) {
	payload := `{"jsonrpc":"2.0","id":4,"method":"elicitation/create","params":{"message":"Sign in","requestedSchema":{"type":"object"}}}`
	got, err := agenttest.AnswerControl(FamilyProvider{}, []byte(payload), "jsonrpc:4", agenttest.ElicitationDecision(leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT, `{"token":"t"}`))
	require.NoError(t, err)
	resolved := agenttest.ResolveAnswer(FamilyProvider{}, []byte(payload), "jsonrpc:4", got)
	require.False(t, resolved.Withhold, "%v", resolved.Refusal())
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":4,"result":{"action":"accept","content":{"token":"t"}}}`, string(resolved.Content))
}

func TestFamilyAnswerControlRequest_RefusesADecisionOfAnotherKind(t *testing.T) {
	plan := agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true})
	question := agenttest.QuestionDecision()
	for name, tc := range map[string]struct {
		payload  string
		decision *leapmuxv1.ControlDecision
	}{
		"a plan decision to a permission":      {familyDaemonOptionsRequest, plan},
		"a question decision to a permission":  {familyDaemonOptionsRequest, question},
		"a permission decision to a question":  {`{"type":"question.asked","properties":{"questions":[]}}`, agenttest.PermissionDecision("once", "")},
		"an option the request does not offer": {familyDaemonOptionsRequest, agenttest.PermissionDecision("forever", "")},
		"no decision":                          {familyDaemonOptionsRequest, nil},
	} {
		_, err := agenttest.AnswerControl(FamilyProvider{}, []byte(tc.payload), "r", tc.decision)
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}

	input := agent.NewControlRequestInput([]byte(`{}`))
	_, err := FamilyProvider{}.AnswerControlRequest(agent.ControlAnswerInput{
		Request: input, RequestID: "r",
		Prompt:   &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Dialog{Dialog: &leapmuxv1.DialogPrompt{}}},
		Decision: &leapmuxv1.ControlDecision{Kind: &leapmuxv1.ControlDecision_Dialog{Dialog: &leapmuxv1.DialogDecision{}}},
	})
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "the family states no dialog")
}
