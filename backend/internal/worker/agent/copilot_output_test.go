package agent

import "testing"

func newCopilotAgentWithSink(sink OutputSink) *CopilotCLIAgent {
	return &CopilotCLIAgent{
		acpBase: acpBase{
			jsonrpcBase: jsonrpcBase{processBase: processBase{
				agentID: "test-agent",
			}},
			sink:      sink,
			sessionID: "test-session",
		},
	}
}

func TestHandleCopilotOutput_AgentMessageChunk(t *testing.T) {
	sink := &testSink{}
	agent := newCopilotAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Hello Copilot"}}}}`
	agent.HandleOutput([]byte(input))

	if sink.StreamChunkCount() != 1 {
		t.Fatalf("expected 1 stream chunk, got %d", sink.StreamChunkCount())
	}
	got := sink.LastStreamChunk()
	if got.Method != "agent_message_chunk" {
		t.Fatalf("expected method agent_message_chunk, got %q", got.Method)
	}
	if string(got.Content) != "Hello Copilot" {
		t.Fatalf("expected content 'Hello Copilot', got %q", string(got.Content))
	}
}

func TestHandleCopilotOutput_RequestPermission(t *testing.T) {
	sink := &controlTestSink{}
	agent := newCopilotAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","id":7,"method":"session/request_permission","params":{"sessionId":"s1","options":[{"optionId":"proceed_once","name":"Allow","kind":"allow_once"}],"toolCall":{"toolCallId":"tc-1","title":"shell","kind":"execute"}}}`
	agent.HandleOutput([]byte(input))

	if sink.PersistedControlCount() != 1 {
		t.Fatalf("expected 1 persisted control request, got %d", sink.PersistedControlCount())
	}
	if got := sink.LastPersistedControl().RequestID; got != "7" {
		t.Fatalf("expected control request id 7, got %q", got)
	}
}

func TestHandleCopilotOutput_ConfigOptionUpdateBroadcastsPermissionMode(t *testing.T) {
	sink := &testSink{}
	agent := newCopilotAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"mode","currentValue":"https://agentclientprotocol.com/protocol/session-modes#plan","options":[{"value":"https://agentclientprotocol.com/protocol/session-modes#agent","name":"Agent"},{"value":"https://agentclientprotocol.com/protocol/session-modes#plan","name":"Plan"}]},{"id":"model","currentValue":"gpt-5.4-mini","options":[{"value":"gpt-5.4","name":"GPT-5.4"},{"value":"gpt-5.4-mini","name":"GPT-5.4 mini"}]}]}}}`
	agent.HandleOutput([]byte(input))

	if agent.permissionMode != CopilotCLIModePlan {
		t.Fatalf("expected mode plan, got %q", agent.permissionMode)
	}
	if agent.model != "gpt-5.4-mini" {
		t.Fatalf("expected model gpt-5.4-mini, got %q", agent.model)
	}
	if got := sink.PermissionMode(); got != CopilotCLIModePlan {
		t.Fatalf("expected sink permission mode plan, got %q", got)
	}
	if len(agent.availableModels) != 2 {
		t.Fatalf("expected 2 available models, got %d", len(agent.availableModels))
	}
}
