package goose

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

// Goose adds no control request of its own: the embedded Agent Client
// Protocol base reads and answers every one. These tests prove it through
// Goose's registration, with the shapes that Goose sends.

const gooseTestRequestID = "jsonrpc:7"

// gooseKindNamedPermission is Goose's permission shape: four options, each
// named after its own kind, allow first.
const gooseKindNamedPermission = `{"jsonrpc":"2.0","id":7,"method":"session/request_permission","params":{
	"sessionId":"s",
	"toolCall":{"toolCallId":"c","kind":"edit","title":"Write"},
	"options":[
		{"optionId":"allow_always","kind":"allow_always","name":"allow_always"},
		{"optionId":"allow_once","kind":"allow_once","name":"allow_once"},
		{"optionId":"reject_once","kind":"reject_once","name":"reject_once"},
		{"optionId":"reject_always","kind":"reject_always","name":"reject_always"}]}}`

// A shell call states its command. The protocol answers a call with no
// options only with the cancelled outcome, so LeapMux's cancel is its one
// option.
func TestReadControlRequest_GooseReadsAShellPermissionThroughTheBase(t *testing.T) {
	t.Parallel()
	command := "npm test -- --runInBand"
	agenttest.RequireProto(t, agenttest.PermissionPrompt(&leapmuxv1.PermissionPrompt{
		Title: "Run", Command: command, InputJson: `{"command":"npm test -- --runInBand"}`,
		Options: []*leapmuxv1.PermissionOption{{
			OptionId: acp.CancelOptionID, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, Name: "Cancel", EndsTurn: true,
		}},
	}), agenttest.ControlPromptOf(Registration().Plugin, []byte(`{"params":{"toolCall":{"toolCallId":"c","kind":"execute","title":"Run","rawInput":{"command":"npm test -- --runInBand"}}}}`)))
}

// The options are Goose's own, in its order. A list with no reject option
// gains LeapMux's cancel, and a kind outside the four keeps its name.
func TestReadControlRequest_GooseReadsTheOptionsOfAPermission(t *testing.T) {
	t.Parallel()
	agenttest.RequireProto(t, agenttest.PermissionPrompt(&leapmuxv1.PermissionPrompt{
		Title: "Write",
		Options: []*leapmuxv1.PermissionOption{
			{OptionId: "allow_always", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS, Name: "allow_always"},
			{OptionId: "allow_once", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, Name: "allow_once"},
			{OptionId: "reject_once", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, Name: "reject_once"},
			{OptionId: "reject_always", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ALWAYS, Name: "reject_always"},
		},
	}), agenttest.ControlPromptOf(Registration().Plugin, []byte(gooseKindNamedPermission)))

	agenttest.RequireProto(t, agenttest.PermissionPrompt(&leapmuxv1.PermissionPrompt{
		Title: "Run",
		Options: []*leapmuxv1.PermissionOption{
			{OptionId: "once", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, Name: "Allow once"},
			{OptionId: acp.CancelOptionID, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, Name: "Cancel", EndsTurn: true},
		},
	}), agenttest.ControlPromptOf(Registration().Plugin, []byte(`{"params":{"toolCall":{"toolCallId":"c","kind":"other","title":"Run"},"options":[{"optionId":"once","kind":"allow_once","name":"Allow once"}]}}`)))

	options := agenttest.ControlPromptOf(Registration().Plugin, []byte(`{"params":{"options":[
		{"optionId":"once","kind":"allow_once","name":"Allow"},
		{"optionId":"reject","kind":"reject_once","name":"Reject"},
		{"optionId":"ask","kind":"ask_user","name":"Ask the user"}]}}`)).GetPermission().GetOptions()
	require.Len(t, options, 3)
	assert.Equal(t, leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_OTHER, options[2].GetKind())
	assert.Equal(t, "Ask the user", options[2].GetName())
}

func TestReadControlRequest_GooseReadsAnElicitationThroughTheBase(t *testing.T) {
	t.Parallel()
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Elicitation{Elicitation: &leapmuxv1.ElicitationPrompt{
		Mode: "form", Message: "Choose.", SchemaJson: `{"type":"object","properties":{"count":{"type":"integer"}}}`,
	}}}, agenttest.ControlPromptOf(Registration().Plugin, []byte(`{"method":"elicitation/create","params":{"mode":"form","message":"Choose.","requestedSchema":{"type":"object","properties":{"count":{"type":"integer"}}}}}`)))

	url := agenttest.ControlPromptOf(Registration().Plugin, []byte(`{"method":"elicitation/create","params":{"mode":"url","message":"Complete the provider setup.","url":"https://example.com/setup?flow=1"}}`)).GetElicitation()
	require.NotNil(t, url)
	assert.Equal(t, "url", url.GetMode())
	assert.Equal(t, "https://example.com/setup?flow=1", url.GetUrl())
	assert.Empty(t, url.GetSchemaJson())

	future := agenttest.ControlPromptOf(Registration().Plugin, []byte(`{"method":"elicitation/create","params":{"mode":"future-mode","message":"Unknown interaction"}}`)).GetElicitation()
	require.NotNil(t, future)
	assert.Equal(t, "future-mode", future.GetMode(), "an unknown mode stays as the server sent it")
}

// A payload that Goose's reader does not recognize reads as unrecognized, so
// it offers no answer that Goose would not read.
func TestReadControlRequest_GooseReadsAnUnknownRequestAsUnrecognized(t *testing.T) {
	t.Parallel()
	agenttest.RequireProto(t, agent.UnrecognizedControlPrompt(), agenttest.ControlPromptOf(Registration().Plugin, []byte(`{"unknown_envelope":{"nothing":"here"}}`)))
}

// Each option is selected as it is, under the Worker's key of the request,
// and the Worker restores Goose's own id when it delivers the answer.
func TestAnswerControlRequest_GooseSelectsTheChosenOption(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"allow_always", "allow_once", "reject_once", "reject_always"} {
		got, err := agenttest.AnswerControl(Registration().Plugin, []byte(gooseKindNamedPermission), gooseTestRequestID, agenttest.PermissionDecision(id, ""))
		require.NoError(t, err, id)
		assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:7","result":{"outcome":{"outcome":"selected","optionId":"`+id+`"}}}`, string(got.Content), id)
		assert.Empty(t, got.FollowUp, id)
		resolved := agenttest.ResolveAnswer(Registration().Plugin, []byte(gooseKindNamedPermission), gooseTestRequestID, got)
		assert.False(t, resolved.Withhold, id)
		assert.JSONEq(t, `{"jsonrpc":"2.0","id":7,"result":{"outcome":{"outcome":"selected","optionId":"`+id+`"}}}`, string(resolved.Content), id)
	}

	proceed := `{"jsonrpc":"2.0","id":7,"params":{"options":[{"optionId":"proceed_once","kind":"allow_once"},{"optionId":"ask","kind":"ask_user","name":"Ask the user"}]}}`
	for _, id := range []string{"proceed_once", "ask"} {
		got, err := agenttest.AnswerControl(Registration().Plugin, []byte(proceed), gooseTestRequestID, agenttest.PermissionDecision(id, ""))
		require.NoError(t, err, id)
		assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:7","result":{"outcome":{"outcome":"selected","optionId":"`+id+`"}}}`, string(got.Content), id)
	}

	_, err := agenttest.AnswerControl(Registration().Plugin, []byte(gooseKindNamedPermission), gooseTestRequestID, agenttest.PermissionDecision("proceed_once", ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "an option that the request does not offer")
	_, err = agenttest.AnswerControl(Registration().Plugin, []byte(gooseKindNamedPermission), gooseTestRequestID, agenttest.PermissionDecision(acp.CancelOptionID, ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a request with its own reject option offers no cancel")
}

// The composer's typed refusal selects a reject option, and the reason
// follows the answer as a message, because Goose states no field for it. An
// approval carries no reason.
func TestAnswerControlRequest_GooseDeliversTheReasonOfARefusalAsAMessage(t *testing.T) {
	t.Parallel()
	payload := `{"jsonrpc":"2.0","id":3,"method":"session/request_permission","params":{"sessionId":"s",
		"toolCall":{"toolCallId":"c","kind":"execute","title":"Run","rawInput":{"command":"ls"}},
		"options":[{"optionId":"allow-1","name":"Allow","kind":"allow_once"},{"optionId":"reject-1","name":"Reject","kind":"reject_once"}]}}`
	got, err := agenttest.AnswerControl(Registration().Plugin, []byte(payload), gooseTestRequestID, agenttest.PermissionDecision("reject-1", "use the other file"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:7","result":{"outcome":{"outcome":"selected","optionId":"reject-1"}}}`, string(got.Content))
	assert.Equal(t, "use the other file", got.FollowUp)

	got, err = agenttest.AnswerControl(Registration().Plugin, []byte(payload), gooseTestRequestID, agenttest.PermissionDecision("allow-1", ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:7","result":{"outcome":{"outcome":"selected","optionId":"allow-1"}}}`, string(got.Content))

	_, err = agenttest.AnswerControl(Registration().Plugin, []byte(payload), gooseTestRequestID, agenttest.PermissionDecision("allow-1", "a note"))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
}

// A call with no options takes LeapMux's cancel, which reaches Goose as the
// protocol's cancelled outcome under Goose's own id. Goose's reply has no
// field for the reason, so the reason follows the answer as a message.
func TestAnswerControlRequest_GooseCancelsAnOptionlessPermission(t *testing.T) {
	t.Parallel()
	payload := `{"jsonrpc":"2.0","id":7,"params":{"toolCall":{"toolCallId":"c","kind":"edit","title":"Edit"}}}`
	got, err := agenttest.AnswerControl(Registration().Plugin, []byte(payload), gooseTestRequestID, agenttest.PermissionDecision(acp.CancelOptionID, "not this file"))
	require.NoError(t, err)
	resolved := agenttest.ResolveAnswer(Registration().Plugin, []byte(payload), gooseTestRequestID, got)
	require.NoError(t, resolved.Refusal())
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":7,"result":{"outcome":{"outcome":"cancelled"}}}`, string(resolved.Content))
	assert.Equal(t, "not this file", got.FollowUp)

	for _, id := range []string{contracts.ControlAnswerOptionAllow, contracts.ControlAnswerOptionDeny} {
		_, err := agenttest.AnswerControl(Registration().Plugin, []byte(payload), gooseTestRequestID, agenttest.PermissionDecision(id, ""))
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "Goose cannot read LeapMux's %s", id)
	}
}

// Each elicitation answer is the shared form's: an accept with the filled
// form, an accept of a link with no form, a decline, and a cancel.
func TestAnswerControlRequest_GooseAnswersAnElicitationThroughTheBase(t *testing.T) {
	t.Parallel()
	form := `{"jsonrpc":"2.0","id":7,"method":"elicitation/create","params":{"mode":"form","message":"Choose the settings.","requestedSchema":{"type":"object"}}}`
	got, err := agenttest.AnswerControl(Registration().Plugin, []byte(form), gooseTestRequestID, agenttest.ElicitationDecision(leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT, `{"count":0,"enabled":false,"color":"b"}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"jsonrpc:7","response":{
		"action":"accept","content":{"count":0,"enabled":false,"color":"b"}}}}`, string(got.Content))
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":7,"result":{"action":"accept","content":{"count":0,"enabled":false,"color":"b"}}}`, string(agenttest.ResolveAnswer(Registration().Plugin, []byte(form), gooseTestRequestID, got).Content))

	got, err = agenttest.AnswerControl(Registration().Plugin, []byte(form), gooseTestRequestID, agenttest.ElicitationDecision(leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT, `{}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"jsonrpc:7","response":{"action":"accept","content":{}}}}`, string(got.Content),
		"a form left empty sends an empty object")

	link := `{"jsonrpc":"2.0","id":7,"method":"elicitation/create","params":{"mode":"url","message":"Complete the provider setup.","url":"https://example.com/setup?flow=1"}}`
	got, err = agenttest.AnswerControl(Registration().Plugin, []byte(link), gooseTestRequestID, agenttest.ElicitationDecision(leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT, ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"jsonrpc:7","response":{"action":"accept"}}}`, string(got.Content))

	for action, token := range map[leapmuxv1.ElicitationAction]string{
		leapmuxv1.ElicitationAction_ELICITATION_ACTION_DECLINE: "decline",
		leapmuxv1.ElicitationAction_ELICITATION_ACTION_CANCEL:  "cancel",
	} {
		got, err = agenttest.AnswerControl(Registration().Plugin, []byte(link), gooseTestRequestID, agenttest.ElicitationDecision(action, ""))
		require.NoError(t, err, token)
		assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"jsonrpc:7","response":{"action":"`+token+`"}}}`, string(got.Content), token)
		assert.JSONEq(t, `{"jsonrpc":"2.0","id":7,"result":{"action":"`+token+`"}}`, string(agenttest.ResolveAnswer(Registration().Plugin, []byte(link), gooseTestRequestID, got).Content), token)
	}

	_, err = agenttest.AnswerControl(Registration().Plugin, []byte(link), gooseTestRequestID, agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "an elicitation takes an elicitation decision")
}
