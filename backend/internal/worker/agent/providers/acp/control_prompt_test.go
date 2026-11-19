package acp

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

const fullPermission = `{"jsonrpc":"2.0","id":7,"method":"session/request_permission","params":{
	"toolCall":{"toolCallId":"call-1","kind":"execute","title":"Run","rawInput":{"command":"ls -al"}},
	"options":[
		{"optionId":"always","kind":"allow_always","name":"Always"},
		{"optionId":"once","kind":"allow_once","name":"Once"},
		{"optionId":"no","kind":"reject_once","name":"No"},
		{"optionId":"never","kind":"reject_always","name":"Never"}
	]}}`

func optionIDs(options []*leapmuxv1.PermissionOption) []string {
	ids := make([]string, 0, len(options))
	for _, option := range options {
		ids = append(ids, option.GetOptionId())
	}
	return ids
}

func TestReadControlRequest_ReadsTheToolCallAndTheOptionsOfAPermission(t *testing.T) {
	permission := agenttest.ControlPromptOf(Provider{}, []byte(fullPermission)).GetPermission()
	require.NotNil(t, permission)
	assert.Equal(t, "Run", permission.GetTitle())
	assert.JSONEq(t, `{"command":"ls -al"}`, permission.GetInputJson())
	assert.Equal(t, "ls -al", permission.GetCommand(), "an execute call states its command")
	assert.Equal(t, []string{"always", "once", "no", "never"}, optionIDs(permission.GetOptions()))
	assert.Equal(t, []leapmuxv1.PermissionOptionKind{
		leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS,
		leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE,
		leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE,
		leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ALWAYS,
	}, []leapmuxv1.PermissionOptionKind{
		permission.GetOptions()[0].GetKind(), permission.GetOptions()[1].GetKind(), permission.GetOptions()[2].GetKind(), permission.GetOptions()[3].GetKind(),
	})
}

// A request that states its answers and no tool call is still a permission:
// the option ids are the only thing its answer can carry.
func TestReadControlRequest_ReadsOptionsWithNoToolCall(t *testing.T) {
	permission := agenttest.ControlPromptOf(Provider{}, []byte(`{"params":{"options":[{"optionId":"once","kind":"allow_once"},{"optionId":"reject","kind":"reject_once"}]}}`)).GetPermission()
	require.NotNil(t, permission)
	assert.Empty(t, permission.GetTitle())
	assert.Empty(t, permission.GetCommand())
	assert.Empty(t, permission.GetInputJson())
	assert.Equal(t, []string{"once", "reject"}, optionIDs(permission.GetOptions()))
}

func TestReadControlRequest_AToolCallWithNoOptionsTakesLeapMuxAllowAndDeny(t *testing.T) {
	permission := agenttest.ControlPromptOf(Provider{}, []byte(`{"params":{"toolCall":{"toolCallId":"call-1","kind":"edit","title":"Edit"}}}`)).GetPermission()
	require.NotNil(t, permission)
	assert.Equal(t, []string{contracts.ControlAnswerOptionAllow, contracts.ControlAnswerOptionDeny}, optionIDs(permission.GetOptions()))
	assert.Empty(t, permission.GetCommand(), "only an execute call states a command")

	permission = agenttest.ControlPromptOf(Provider{}, []byte(`{"params":{"toolCall":{"toolCallId":"call-1","title":"Run"},"options":[null,"reject"]}}`)).GetPermission()
	require.NotNil(t, permission, "a tool call carries the request when every option is unreadable")
	assert.Equal(t, []string{contracts.ControlAnswerOptionAllow, contracts.ControlAnswerOptionDeny}, optionIDs(permission.GetOptions()))
}

func TestReadControlRequest_ANonPermissionPayloadIsGeneric(t *testing.T) {
	for _, payload := range []string{`{"params":{"sessionId":"s"}}`, `{}`, `not json`,
		`{"params":{"options":"allow_once"}}`, `{"params":{"options":7}}`, `{"params":{"options":{"optionId":"once"}}}`, `{"params":{"options":true}}`} {
		permission := agenttest.ControlPromptOf(Provider{}, []byte(payload)).GetPermission()
		require.NotNil(t, permission, payload)
		assert.Empty(t, permission.GetTitle(), payload)
		assert.Equal(t, []string{contracts.ControlAnswerOptionAllow, contracts.ControlAnswerOptionDeny}, optionIDs(permission.GetOptions()), payload)
	}
}

// A malformed option list must not hide the tool call beside it.
func TestReadControlRequest_AMalformedOptionListKeepsTheToolCall(t *testing.T) {
	permission := agenttest.ControlPromptOf(Provider{}, []byte(`{"params":{"toolCall":{"toolCallId":"call-1","title":"Run"},"options":"allow_once"}}`)).GetPermission()
	assert.Equal(t, "Run", permission.GetTitle())
}

func TestReadControlRequest_DropsAnOptionThatStatesNoKindAndReadsEachField(t *testing.T) {
	permission := agenttest.ControlPromptOf(Provider{}, []byte(`{"params":{"options":[null,"allow_once",42,{"optionId":"once","kind":"allow_once","name":""},[],{"optionId":7,"kind":null,"name":{"text":"x"}},{"optionId":"later","kind":"defer","name":""}]}}`)).GetPermission()
	require.NotNil(t, permission)
	options := permission.GetOptions()
	require.Len(t, options, 4, "three readable options, and LeapMux's cancel because none rejects")
	assert.Equal(t, "once", options[0].GetOptionId())
	assert.Empty(t, options[0].GetName(), "an empty name is no name")
	assert.Equal(t, "", options[1].GetOptionId(), "a value of the wrong type reads as empty")
	assert.Equal(t, leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_OTHER, options[1].GetKind())
	assert.Equal(t, "defer", options[2].GetName(), "an unknown kind labels an option that states no name")
	assert.Equal(t, CancelOptionID, options[3].GetOptionId())
	assert.True(t, options[3].GetEndsTurn(), "the cancelled outcome cancels the prompt turn")
	assert.Equal(t, leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, options[3].GetKind())
}

func TestReadControlRequest_MergesTheArgumentsOfTheToolCallRow(t *testing.T) {
	input := agent.NewControlRequestInput([]byte(`{"params":{"toolCall":{"toolCallId":"call-1","kind":"execute","title":"Run","rawInput":{"cwd":"/w"}}}}`))
	var asked string
	input.SpanRequest = func(spanID string) (agent.MessageContent, bool) {
		asked = spanID
		return agent.MessageContent{Original: []byte(`{"toolCallId":"call-1","kind":"execute","title":"Old","rawInput":{"command":"ls -al","cwd":"/old"}}`)}, true
	}
	permission := Provider{}.ReadControlRequest(input).GetPermission()
	assert.Equal(t, "call-1", asked, "the prompt loads the row of the call that it is about")
	assert.Equal(t, "Run", permission.GetTitle(), "the compact call's own fields win")
	assert.Equal(t, "ls -al", permission.GetCommand())
	assert.JSONEq(t, `{"command":"ls -al","cwd":"/w"}`, permission.GetInputJson(), "the two argument objects merge")

	input.SpanRequest = func(string) (agent.MessageContent, bool) {
		return agent.MessageContent{Original: []byte(`{"toolCallId":"call-2","rawInput":{"command":"rm -rf /"}}`)}, true
	}
	permission = Provider{}.ReadControlRequest(input).GetPermission()
	assert.Empty(t, permission.GetCommand(), "a row of another call is not merged")
}

func TestPermissionToolSpanID(t *testing.T) {
	assert.Equal(t, "call-1", PermissionToolSpanID([]byte(fullPermission)))
	assert.Empty(t, PermissionToolSpanID([]byte(`{"params":{"options":[]}}`)))
}

func TestReadControlRequest_AnElicitationIsAForm(t *testing.T) {
	elicitation := agenttest.ControlPromptOf(Provider{}, []byte(`{"method":"elicitation/create","params":{"message":"Sign in","server":"github","requestedSchema":{"type":"object"},"title":"Login"}}`)).GetElicitation()
	require.NotNil(t, elicitation)
	assert.Equal(t, "form", elicitation.GetMode())
	assert.Equal(t, "Sign in", elicitation.GetMessage())
	assert.Equal(t, "github", elicitation.GetServer())
	assert.JSONEq(t, `{"type":"object"}`, elicitation.GetSchemaJson())
}

func answerBase(t *testing.T, payload string, decision *leapmuxv1.ControlDecision, rejectReason RejectReason) (agent.ControlAnswer, error) {
	t.Helper()
	input := agent.NewControlRequestInput([]byte(payload))
	return AnswerBaseControlRequest(agent.ControlAnswerInput{
		Request: input, RequestID: "jsonrpc:7", Prompt: Provider{}.ReadControlRequest(input), Decision: decision,
	}, rejectReason)
}

func TestAnswerPermission_SelectsTheChosenOption(t *testing.T) {
	for _, id := range []string{"always", "once", "no", "never"} {
		got, err := answerBase(t, fullPermission, agenttest.PermissionDecision(id, ""), nil)
		require.NoError(t, err, id)
		assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:7","result":{"outcome":{"outcome":"selected","optionId":"`+id+`"}}}`, string(got.Content), id)
		assert.Empty(t, got.FollowUp, id)
	}
}

// An option carries no text, so the reason of a refusal follows the answer
// as a message of its own, unless the provider has a field for it.
func TestAnswerPermission_DeliversTheReasonOfARefusal(t *testing.T) {
	got, err := answerBase(t, fullPermission, agenttest.PermissionDecision("no", "use the other file"), nil)
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:7","result":{"outcome":{"outcome":"selected","optionId":"no"}}}`, string(got.Content))
	assert.Equal(t, "use the other file", got.FollowUp)

	note := func(result map[string]any, reason string) map[string]any {
		result["_meta"] = map[string]any{"note": reason}
		return result
	}
	got, err = answerBase(t, fullPermission, agenttest.PermissionDecision("no", "try again"), note)
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:7","result":{"outcome":{"outcome":"selected","optionId":"no"},"_meta":{"note":"try again"}}}`, string(got.Content))
	assert.Empty(t, got.FollowUp, "a reason in the provider's own field is not sent twice")

	_, err = answerBase(t, fullPermission, agenttest.PermissionDecision("once", "a note"), nil)
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "an approval carries no note")
}

func TestAnswerPermission_TheCancelOptionAnswersWithTheCancelledOutcome(t *testing.T) {
	payload := `{"params":{"options":[{"optionId":"once","kind":"allow_once"}]}}`
	got, err := answerBase(t, payload, agenttest.PermissionDecision(CancelOptionID, ""), nil)
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:7","result":{"outcome":{"outcome":"cancelled"}}}`, string(got.Content))

	note := func(result map[string]any, reason string) map[string]any {
		result["reason"] = reason
		return result
	}
	got, err = answerBase(t, payload, agenttest.PermissionDecision(CancelOptionID, "no"), note)
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:7","result":{"outcome":{"outcome":"cancelled"},"reason":"no"}}`, string(got.Content))

	_, err = answerBase(t, fullPermission, agenttest.PermissionDecision(CancelOptionID, ""), nil)
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a request with its own reject option offers no cancel")
}

func TestAnswerPermission_LeapMuxAllowAndDenyTakeTheNeutralEnvelope(t *testing.T) {
	payload := `{"params":{"toolCall":{"toolCallId":"call-1","title":"Edit"}}}`
	got, err := answerBase(t, payload, agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""), nil)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"jsonrpc:7","response":{"behavior":"allow","updatedInput":{}}}}`, string(got.Content))

	got, err = answerBase(t, payload, agenttest.PermissionDecision(contracts.ControlAnswerOptionDeny, "no"), nil)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"jsonrpc:7","response":{"behavior":"deny","message":"no"}}}`, string(got.Content))
	assert.Empty(t, got.FollowUp, "the neutral deny carries its own message")
}

// A runtime that names its own options allow and deny still gets its own
// answer: the neutral envelope is only for LeapMux's options.
func TestAnswerPermission_ARuntimeOptionNamedAllowIsSelected(t *testing.T) {
	payload := `{"params":{"options":[{"optionId":"allow","kind":"allow_once"},{"optionId":"deny","kind":"reject_once"},{"optionId":"later","kind":"allow_always"}]}}`
	got, err := answerBase(t, payload, agenttest.PermissionDecision("allow", ""), nil)
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:7","result":{"outcome":{"outcome":"selected","optionId":"allow"}}}`, string(got.Content))
}

func TestAnswerBaseControlRequest_AnswersAnElicitationAndRefusesAnotherKind(t *testing.T) {
	payload := `{"method":"elicitation/create","params":{"message":"Sign in"}}`
	got, err := answerBase(t, payload, agenttest.ElicitationDecision(leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT, `{"token":"t"}`), nil)
	require.NoError(t, err)
	var envelope map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(got.Content, &envelope))
	assert.Contains(t, string(got.Content), `"action":"accept"`)

	input := agent.NewControlRequestInput([]byte(payload))
	_, err = AnswerBaseControlRequest(agent.ControlAnswerInput{
		Request: input, RequestID: "r",
		Prompt:   &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Dialog{Dialog: &leapmuxv1.DialogPrompt{}}},
		Decision: &leapmuxv1.ControlDecision{Kind: &leapmuxv1.ControlDecision_Dialog{Dialog: &leapmuxv1.DialogDecision{}}},
	}, nil)
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
}
