package dirac

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// Dirac adds no control request of its own: the embedded Agent Client
// Protocol base reads and answers every one. These tests prove it through
// Dirac's registration, with the shapes that Dirac sends.

const diracTestRequestID = "jsonrpc:7"

// diracCommandPermission is Dirac's approval of a shell command: the tool
// call states Dirac's own tool name beside the protocol's fields.
const diracCommandPermission = `{"jsonrpc":"2.0","id":7,"method":"session/request_permission","params":{
	"sessionId":"session-1",
	"toolCall":{"toolCallId":"call-1","name":"execute_command","title":"Execute command","kind":"execute",
		"rawInput":{"tool":"execute_command","command":"ls -al"}},
	"options":[{"optionId":"allow_once","kind":"allow_once"},{"optionId":"reject_once","kind":"reject_once"}]}}`

// diracQuestion is how Dirac asks the user a question: a standard MCP form
// that states its question as the message.
const diracQuestion = `{"jsonrpc":"2.0","id":7,"method":"elicitation/create","params":{"sessionId":"native-session","mode":"form","message":"Which color should I use?"}}`

func TestReadControlRequest_DiracReadsAPermissionThroughTheBase(t *testing.T) {
	t.Parallel()
	permission := agenttest.ControlPromptOf(Registration().Plugin, []byte(diracCommandPermission)).GetPermission()
	require.NotNil(t, permission)
	assert.Equal(t, "Execute command", permission.GetTitle())
	assert.Equal(t, "ls -al", permission.GetCommand())
	assert.JSONEq(t, `{"tool":"execute_command","command":"ls -al"}`, permission.GetInputJson())
	require.Len(t, permission.GetOptions(), 2)
	assert.Equal(t, "allow_once", permission.GetOptions()[0].GetOptionId())
	assert.Equal(t, leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, permission.GetOptions()[0].GetKind())
	assert.Empty(t, permission.GetOptions()[0].GetName(), "an option with no name takes the label of its kind")
	assert.Equal(t, "reject_once", permission.GetOptions()[1].GetOptionId())
	assert.Equal(t, leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, permission.GetOptions()[1].GetKind())

	assert.True(t, proto.Equal(agent.UnrecognizedControlPrompt(), agenttest.ControlPromptOf(Registration().Plugin, []byte(`{"method":"dev.dirac/unknown","params":{}}`))),
		"a request that nothing recognizes reads as unrecognized")
}

// The chosen option is selected as it is, and the Worker restores Dirac's own
// id when it delivers the answer. A refusal's reason follows the answer as a
// message, because Dirac states no field for it.
func TestAnswerControlRequest_DiracSelectsTheChosenOption(t *testing.T) {
	t.Parallel()
	got, err := agenttest.AnswerControl(Registration().Plugin, []byte(diracCommandPermission), diracTestRequestID, agenttest.PermissionDecision("allow_once", ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:7","result":{"outcome":{"outcome":"selected","optionId":"allow_once"}}}`, string(got.Content))
	assert.Empty(t, got.FollowUp)
	resolved := agenttest.ResolveAnswer(Registration().Plugin, []byte(diracCommandPermission), diracTestRequestID, got)
	assert.False(t, resolved.Withhold)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":7,"result":{"outcome":{"outcome":"selected","optionId":"allow_once"}}}`, string(resolved.Content))

	got, err = agenttest.AnswerControl(Registration().Plugin, []byte(diracCommandPermission), diracTestRequestID, agenttest.PermissionDecision("reject_once", "list the other directory"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"jsonrpc:7","result":{"outcome":{"outcome":"selected","optionId":"reject_once"}}}`, string(got.Content))
	assert.Equal(t, "list the other directory", got.FollowUp)

	for name, decision := range map[string]*leapmuxv1.ControlDecision{
		"an approval with feedback":   agenttest.PermissionDecision("allow_once", "a note"),
		"an option it does not offer": agenttest.PermissionDecision("allow_always", ""),
		"LeapMux's own allow":         agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""),
	} {
		_, err := agenttest.AnswerControl(Registration().Plugin, []byte(diracCommandPermission), diracTestRequestID, decision)
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}
}

// Dirac's question is a form whose message is the question, and the answer
// is the shared form's.
func TestControlRequest_DiracQuestionIsAnElicitation(t *testing.T) {
	t.Parallel()
	elicitation := agenttest.ControlPromptOf(Registration().Plugin, []byte(diracQuestion)).GetElicitation()
	require.NotNil(t, elicitation)
	assert.Equal(t, "form", elicitation.GetMode())
	assert.Equal(t, "Which color should I use?", elicitation.GetMessage())
	assert.Empty(t, elicitation.GetSchemaJson(), "a form that states no schema states none")

	got, err := agenttest.AnswerControl(Registration().Plugin, []byte(diracQuestion), diracTestRequestID, agenttest.ElicitationDecision(leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT, `{"answer":"blue"}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"jsonrpc:7","response":{"action":"accept","content":{"answer":"blue"}}}}`, string(got.Content))
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":7,"result":{"action":"accept","content":{"answer":"blue"}}}`, string(agenttest.ResolveAnswer(Registration().Plugin, []byte(diracQuestion), diracTestRequestID, got).Content))

	got, err = agenttest.AnswerControl(Registration().Plugin, []byte(diracQuestion), diracTestRequestID, agenttest.ElicitationDecision(leapmuxv1.ElicitationAction_ELICITATION_ACTION_CANCEL, ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":7,"result":{"action":"cancel"}}`, string(agenttest.ResolveAnswer(Registration().Plugin, []byte(diracQuestion), diracTestRequestID, got).Content))
}
