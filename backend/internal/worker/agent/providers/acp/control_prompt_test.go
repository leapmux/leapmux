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

// wantCancelOption is LeapMux's cancel option, as a client reads it.
func wantCancelOption() *leapmuxv1.PermissionOption {
	return &leapmuxv1.PermissionOption{
		OptionId: CancelOptionID,
		Kind:     leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE,
		Name:     "Cancel",
		EndsTurn: true,
	}
}

// The protocol selects only an option that the runtime offered, so a request
// that offers none has one answer: the `cancelled` outcome. The prompt still
// states the tool call, so the reader sees what the agent asked for.
func TestReadControlRequest_APermissionWithNoOptionsOffersOnlyCancel(t *testing.T) {
	for name, options := range map[string]string{
		"no option list":           ``,
		"an empty list":            `,"options":[]`,
		"a null list":              `,"options":null`,
		"only unreadable elements": `,"options":[null,"reject",42]`,
		"a list that is no list":   `,"options":"allow_once"`,
	} {
		payload := `{"params":{"toolCall":{"toolCallId":"call-1","kind":"execute","title":"Run","rawInput":{"command":"ls"}}` + options + `}}`
		agenttest.RequireProto(t, agenttest.PermissionPrompt(&leapmuxv1.PermissionPrompt{
			Title: "Run", Command: "ls", InputJson: `{"command":"ls"}`, Options: []*leapmuxv1.PermissionOption{wantCancelOption()},
		}), agenttest.ControlPromptOf(Provider{}, []byte(payload)), name)
	}
}

// A runtime reads only a JSON-RPC result for its request, and nothing turns
// LeapMux's Allow and Deny into one, so a payload that is no permission and no
// elicitation reads as unrecognized: it offers no answer that the runtime
// would not read.
func TestReadControlRequest_ANonPermissionPayloadIsUnrecognized(t *testing.T) {
	for _, payload := range []string{`{"params":{"sessionId":"s"}}`, `{}`, `not json`,
		`{"params":{"options":"allow_once"}}`, `{"params":{"options":7}}`, `{"params":{"options":{"optionId":"once"}}}`, `{"params":{"options":true}}`} {
		agenttest.RequireProto(t, agent.UnrecognizedControlPrompt(), agenttest.ControlPromptOf(Provider{}, []byte(payload)), payload)
	}
}

// A malformed option list must not hide the tool call beside it.
func TestReadControlRequest_AMalformedOptionListKeepsTheToolCall(t *testing.T) {
	permission := agenttest.ControlPromptOf(Provider{}, []byte(`{"params":{"toolCall":{"toolCallId":"call-1","title":"Run"},"options":"allow_once"}}`)).GetPermission()
	assert.Equal(t, "Run", permission.GetTitle())
}

// An option is an answer only through its optionId: the runtime accepts the
// id of an option that it offered and no other. So an option that states no
// id, an empty id or an id that is not text is no answer, and the prompt drops
// it rather than send an id that the runtime never offered.
func TestReadControlRequest_DropsAnOptionThatStatesNoIDAndReadsEachField(t *testing.T) {
	permission := agenttest.ControlPromptOf(Provider{}, []byte(`{"params":{"options":[null,"allow_once",42,{"optionId":"once","kind":"allow_once","name":""},[],{"optionId":7,"kind":null,"name":{"text":"x"}},{"optionId":"","kind":"allow_always","name":"Always"},{"kind":"reject_once","name":"No"},{"optionId":"later","kind":"defer","name":""}]}}`)).GetPermission()
	require.NotNil(t, permission)
	options := permission.GetOptions()
	require.Len(t, options, 3, "two readable options, and LeapMux's cancel because none rejects")
	assert.Equal(t, "once", options[0].GetOptionId())
	assert.Empty(t, options[0].GetName(), "an empty name is no name")
	assert.Equal(t, "later", options[1].GetOptionId())
	assert.Equal(t, "defer", options[1].GetName(), "an unknown kind labels an option that states no name")
	assert.Equal(t, leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_OTHER, options[1].GetKind())
	assert.Equal(t, CancelOptionID, options[2].GetOptionId())
	assert.True(t, options[2].GetEndsTurn(), "the cancelled outcome cancels the prompt turn")
	assert.Equal(t, leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, options[2].GetKind())
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

// optionlessPermission is a permission request that offers no option.
const optionlessPermission = `{"jsonrpc":"2.0","id":7,"method":"session/request_permission","params":{"sessionId":"s",
	"toolCall":{"toolCallId":"call-1","kind":"edit","title":"Edit"}}}`

// The runtime reads only the protocol's own reply, so every answer that the
// prompt of a request with no option offers must reach it as that reply, under
// the runtime's own id. The only such reply is the `cancelled` outcome.
func TestAnswerPermission_EveryAnswerToAPermissionWithNoOptionsIsTheCancelledOutcome(t *testing.T) {
	options := agenttest.ControlPromptOf(Provider{}, []byte(optionlessPermission)).GetPermission().GetOptions()
	require.NotEmpty(t, options, "the reader must have an answer to give")
	for _, option := range options {
		id := option.GetOptionId()
		got, err := agenttest.AnswerControl(Provider{}, []byte(optionlessPermission), "jsonrpc:7", agenttest.PermissionDecision(id, ""))
		require.NoError(t, err, id)
		resolved := agenttest.ResolveAnswer(Provider{}, []byte(optionlessPermission), "jsonrpc:7", got)
		require.NoError(t, resolved.Refusal(), id)
		assert.JSONEq(t, `{"jsonrpc":"2.0","id":7,"result":{"outcome":{"outcome":"cancelled"}}}`, string(resolved.Content), id)
		assert.Empty(t, got.FollowUp, id)
	}

	// The reason of the refusal follows the answer as a message, because the
	// base protocol's reply has no field for it.
	got, err := agenttest.AnswerControl(Provider{}, []byte(optionlessPermission), "jsonrpc:7", agenttest.PermissionDecision(CancelOptionID, "use the other file"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":7,"result":{"outcome":{"outcome":"cancelled"}}}`,
		string(agenttest.ResolveAnswer(Provider{}, []byte(optionlessPermission), "jsonrpc:7", got).Content))
	assert.Equal(t, "use the other file", got.FollowUp)

	// LeapMux's allow and deny answer in an envelope that the runtime cannot
	// read, so the prompt offers neither.
	for _, id := range []string{contracts.ControlAnswerOptionAllow, contracts.ControlAnswerOptionDeny} {
		_, err := agenttest.AnswerControl(Provider{}, []byte(optionlessPermission), "jsonrpc:7", agenttest.PermissionDecision(id, ""))
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, id)
	}
}

// A runtime whose own options have the ids allow and deny gets its own answer,
// not LeapMux's neutral envelope.
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
		Decision: agenttest.DialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_UNSPECIFIED, ""),
	}, nil)
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
}

func TestSelectedOptionResult(t *testing.T) {
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:7","result":{"outcome":{"outcome":"selected","optionId":"allow-once"}}}`, string(SelectedOptionResult("jsonrpc:7", "allow-once")))
}
