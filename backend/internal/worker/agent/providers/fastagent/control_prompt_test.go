package fastagent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// fast-agent adds no control request of its own: the embedded Agent Client
// Protocol base reads and answers every one. These tests prove it through
// fast-agent's registration.

const fastAgentTestRequestID = "jsonrpc:7"

// fastAgentReadPermission asks to read a file, with the protocol's four
// option kinds.
const fastAgentReadPermission = `{"jsonrpc":"2.0","id":7,"method":"session/request_permission","params":{
	"sessionId":"s",
	"toolCall":{"toolCallId":"call-read","kind":"read","title":"read_text_file","rawInput":{"path":"/w/notes.txt"}},
	"options":[
		{"optionId":"allow_once","name":"Allow Once","kind":"allow_once"},
		{"optionId":"allow_always","name":"Always Allow","kind":"allow_always"},
		{"optionId":"reject_once","name":"Reject Once","kind":"reject_once"},
		{"optionId":"reject_always","name":"Never Allow","kind":"reject_always"}]}}`

func fastAgentPrompt(input agent.ControlRequestInput) *leapmuxv1.ControlPrompt {
	return Registration().Plugin.ReadControlRequest(input)
}

// The request states its call in a compact form, and the arguments live on
// the call's own row, which the base loads and merges.
func TestReadControlRequest_FastAgentReadsAPermissionThroughTheBase(t *testing.T) {
	t.Parallel()
	input := agent.NewControlRequestInput([]byte(fastAgentReadPermission))
	var asked string
	input.SpanRequest = func(spanID string) (agent.MessageContent, bool) {
		asked = spanID
		return agent.MessageContent{Original: []byte(`{"toolCallId":"call-read","kind":"read","title":"Read","rawInput":{"path":"/old","line":3}}`)}, true
	}
	permission := fastAgentPrompt(input).GetPermission()
	require.NotNil(t, permission)
	assert.Equal(t, "call-read", asked, "the prompt loads the row of the call that it is about")
	assert.Equal(t, "read_text_file", permission.GetTitle(), "the compact call's own title wins")
	assert.JSONEq(t, `{"path":"/w/notes.txt","line":3}`, permission.GetInputJson())
	assert.Empty(t, permission.GetCommand(), "only a shell call states a command")
	kinds := make([]leapmuxv1.PermissionOptionKind, 0, len(permission.GetOptions()))
	for _, option := range permission.GetOptions() {
		kinds = append(kinds, option.GetKind())
	}
	assert.Equal(t, []leapmuxv1.PermissionOptionKind{
		leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE,
		leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS,
		leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE,
		leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ALWAYS,
	}, kinds)
	assert.Equal(t, "Never Allow", permission.GetOptions()[3].GetName())
}

// The chosen option is selected as it is, and the Worker restores
// fast-agent's own id when it delivers the answer. A refusal's reason follows
// the answer as a message, because fast-agent states no field for it.
func TestAnswerControlRequest_FastAgentSelectsTheChosenOption(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"allow_once", "allow_always", "reject_once", "reject_always"} {
		got, err := agenttest.AnswerControl(Registration().Plugin, []byte(fastAgentReadPermission), fastAgentTestRequestID, agenttest.PermissionDecision(id, ""))
		require.NoError(t, err, id)
		assert.JSONEq(t, `{"jsonrpc":"2.0","id":7,"result":{"outcome":{"outcome":"selected","optionId":"`+id+`"}}}`,
			string(agenttest.ResolveAnswer(Registration().Plugin, []byte(fastAgentReadPermission), fastAgentTestRequestID, got).Content), id)
	}

	got, err := agenttest.AnswerControl(Registration().Plugin, []byte(fastAgentReadPermission), fastAgentTestRequestID, agenttest.PermissionDecision("reject_always", "that file is private"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:7","result":{"outcome":{"outcome":"selected","optionId":"reject_always"}}}`, string(got.Content))
	assert.Equal(t, "that file is private", got.FollowUp)

	_, err = agenttest.AnswerControl(Registration().Plugin, []byte(fastAgentReadPermission), fastAgentTestRequestID, agenttest.PermissionDecision(acp.CancelOptionID, ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a request with its own reject option offers no cancel")
}

func TestControlRequest_FastAgentAnswersAnElicitationThroughTheBase(t *testing.T) {
	t.Parallel()
	payload := `{"jsonrpc":"2.0","id":7,"method":"elicitation/create","params":{"mode":"form","message":"Enter the API key.","requestedSchema":{"type":"object","properties":{"key":{"type":"string"}}}}}`
	elicitation := fastAgentPrompt(agent.NewControlRequestInput([]byte(payload))).GetElicitation()
	require.NotNil(t, elicitation)
	assert.Equal(t, "Enter the API key.", elicitation.GetMessage())

	got, err := agenttest.AnswerControl(Registration().Plugin, []byte(payload), fastAgentTestRequestID, agenttest.ElicitationDecision(leapmuxv1.ElicitationAction_ELICITATION_ACTION_DECLINE, ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"jsonrpc:7","response":{"action":"decline"}}}`, string(got.Content))
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":7,"result":{"action":"decline"}}`, string(agenttest.ResolveAnswer(Registration().Plugin, []byte(payload), fastAgentTestRequestID, got).Content))

	_, err = agenttest.AnswerControl(Registration().Plugin, []byte(payload), fastAgentTestRequestID, agenttest.ElicitationDecision(leapmuxv1.ElicitationAction_ELICITATION_ACTION_DECLINE, `{"key":"k"}`))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "only an accept carries form content")
}
