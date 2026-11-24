package commandcode

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// commandcodeTestRequestID is the stored id of the request that each test
// answers.
const commandcodeTestRequestID = "r1"

// Command Code has no control channel: the host asks the user nothing, and
// the agent publishes no control request (its permission mode is a launch
// option). So the provider keeps the generic defaults, and a request that
// reached it would read as LeapMux's allow and deny.
func TestReadControlRequest_CommandCodeKeepsTheGenericPrompt(t *testing.T) {
	for _, payload := range []string{`{}`, `{"request":{"tool_name":"Bash","input":{"command":"ls"}}}`, `not json`} {
		got := agenttest.ControlPromptOf(commandcodeProvider{}, []byte(payload))
		assert.True(t, proto.Equal(agent.GenericPermissionPrompt(), got), payload)
	}
}

func TestAnswerControlRequest_CommandCodeKeepsTheGenericAnswers(t *testing.T) {
	answer := func(optionID, feedback string) agent.ControlAnswer {
		t.Helper()
		got, err := agenttest.AnswerControl(commandcodeProvider{}, []byte(`{"request":{"input":{"command":"ls"}}}`), commandcodeTestRequestID, agenttest.PermissionDecision(optionID, feedback))
		require.NoError(t, err)
		return got
	}
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"r1","response":{"behavior":"allow","updatedInput":{"command":"ls"}}}}`,
		string(answer(contracts.ControlAnswerOptionAllow, "").Content))
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"r1","response":{"behavior":"deny","message":"No."}}}`,
		string(answer(contracts.ControlAnswerOptionDeny, "No.").Content))
}
