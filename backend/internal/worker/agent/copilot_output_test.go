package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func newCopilotAgentWithSink(sink OutputSink) *CopilotCLIAgent {
	a := &CopilotCLIAgent{
		acpBase: acpBase{
			jsonrpcBase: jsonrpcBase{processBase: processBase{
				agentID: "test-agent",
			}},
			sink:         sink,
			providerName: "copilot",
			sessionID:    "test-session",
		},
	}
	a.extraSessionUpdate = configOptionSessionUpdateHandler(a.handleConfigOptionUpdate)
	return a
}

func TestHandleCopilotOutput_AgentMessageChunk(t *testing.T) {
	sink := &testSink{}
	agent := newCopilotAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Hello Copilot"}}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 1, sink.StreamChunkCount())
	got := sink.LastStreamChunk()
	require.Equal(t, "agent_message_chunk", got.Method)
	require.Equal(t, "Hello Copilot", string(got.Content))
}

func TestHandleCopilotOutput_RequestPermission(t *testing.T) {
	sink := &controlTestSink{}
	agent := newCopilotAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","id":7,"method":"session/request_permission","params":{"sessionId":"s1","options":[{"optionId":"proceed_once","name":"Allow","kind":"allow_once"}],"toolCall":{"toolCallId":"tc-1","title":"shell","kind":"execute"}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 1, sink.PersistedControlCount())
	require.Equal(t, "7", sink.LastPersistedControl().RequestID)
}

func TestHandleCopilotOutput_ConfigOptionUpdateBroadcastsPermissionMode(t *testing.T) {
	sink := &testSink{}
	agent := newCopilotAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"mode","currentValue":"https://agentclientprotocol.com/protocol/session-modes#plan","options":[{"value":"https://agentclientprotocol.com/protocol/session-modes#agent","name":"Agent"},{"value":"https://agentclientprotocol.com/protocol/session-modes#plan","name":"Plan"}]},{"id":"model","currentValue":"gpt-5.4-mini","options":[{"value":"gpt-5.4","name":"GPT-5.4"},{"value":"gpt-5.4-mini","name":"GPT-5.4 mini"}]}]}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, CopilotCLIModePlan, agent.permissionMode)
	require.Equal(t, "gpt-5.4-mini", agent.model)
	require.Equal(t, CopilotCLIModePlan, sink.PermissionMode())
	require.Len(t, agent.availableModels, 2)
}
