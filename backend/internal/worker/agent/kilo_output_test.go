package agent

import (
	"encoding/json"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newKiloAgentWithSink(sink OutputSink) *KiloAgent {
	return &KiloAgent{
		acpBase: acpBase{
			jsonrpcBase: jsonrpcBase{processBase: processBase{
				agentID:      "test-agent",
				providerName: "kilo",
			}},
			sink:      sink,
			sessionID: "test-session",
		},
	}
}

func TestHandleKiloOutput_AgentMessageChunk(t *testing.T) {
	sink := &testSink{}
	agent := newKiloAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Hello world"}}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 1, sink.StreamChunkCount())
	got := sink.LastStreamChunk()
	require.Equal(t, "agent_message_chunk", got.Method)
	require.Equal(t, "Hello world", string(got.Content))
	require.Equal(t, "", got.SpanID)
	require.Equal(t, 0, sink.MessageCount())
}

func TestHandleKiloOutput_AgentThoughtChunk(t *testing.T) {
	sink := &testSink{}
	agent := newKiloAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"thinking..."}}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 1, sink.StreamChunkCount())
	got := sink.LastStreamChunk()
	require.Equal(t, "agent_thought_chunk", got.Method)
	require.Equal(t, "thinking...", string(got.Content))
	require.Equal(t, 0, sink.MessageCount())
	agent.mu.Lock()
	require.Equal(t, "thinking...", agent.turnThinkingText.String())
	agent.mu.Unlock()
}

func TestHandleKiloPromptResponse_PersistsThinkingText(t *testing.T) {
	sink := &testSink{}
	agent := newKiloAgentWithSink(sink)

	agent.turnThinkingText.WriteString("let me think about this")
	agent.turnAssistantText.WriteString("Here is the answer.")

	resp := json.RawMessage(`{"stopReason":"end_turn","usage":{"totalTokens":100}}`)
	agent.handleACPPromptResponse(resp, nil)

	require.Equal(t, 3, sink.MessageCount())

	thinkingMsg := sink.Messages()[0]
	require.Equal(t, leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, thinkingMsg.Role)
	var thinkingParsed map[string]interface{}
	require.NoError(t, json.Unmarshal(thinkingMsg.Content, &thinkingParsed))
	require.Equal(t, "agent_thought_chunk", thinkingParsed["sessionUpdate"])
	content := thinkingParsed["content"].(map[string]interface{})
	require.Equal(t, "let me think about this", content["text"])

	assistantMsg := sink.Messages()[1]
	var assistantParsed map[string]interface{}
	require.NoError(t, json.Unmarshal(assistantMsg.Content, &assistantParsed))
	require.Equal(t, "agent_message_chunk", assistantParsed["sessionUpdate"])

	resultMsg := sink.Messages()[2]
	require.Equal(t, leapmuxv1.MessageRole_MESSAGE_ROLE_TURN_END, resultMsg.Role)

	agent.mu.Lock()
	require.Equal(t, "", agent.turnThinkingText.String())
	require.Equal(t, "", agent.turnAssistantText.String())
	agent.mu.Unlock()
}

func TestHandleKiloOutput_ToolCallOpensSpan(t *testing.T) {
	sink := &testSink{}
	agent := newKiloAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"bash","kind":"execute","status":"pending","locations":[],"rawInput":{}}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 1, sink.MessageCount())
	msg := sink.Messages()[0]
	require.Equal(t, leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, msg.Role)
	require.Equal(t, "tc-1", msg.SpanID)
	require.Equal(t, "execute", msg.SpanType)

	spans := sink.OpenSpans()
	require.Len(t, spans, 1)
	require.Equal(t, "tc-1", spans[0].SpanID)
	require.Equal(t, 0, sink.ClosedSpanCount())
}

func TestHandleKiloOutput_ToolCallUpdateCompleted(t *testing.T) {
	sink := &testSink{}
	agent := newKiloAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"completed","kind":"execute","title":"bash","content":[{"type":"content","content":{"type":"text","text":"output"}}],"rawOutput":{"output":"output"}}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 1, sink.MessageCount())
	msg := sink.Messages()[0]
	require.Equal(t, "tc-1", msg.SpanID)
	require.True(t, msg.Closing)

	require.Equal(t, 1, sink.StreamEndCount())
	require.Equal(t, "tc-1", sink.LastStreamEnd())

	closed := sink.ClosedSpans()
	require.Len(t, closed, 1)
	require.Equal(t, "tc-1", closed[0])
}

func TestHandleKiloOutput_UsageUpdate(t *testing.T) {
	sink := &testSink{}
	agent := newKiloAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"usage_update","used":1000,"size":128000,"cost":{"amount":0.05,"currency":"USD"}}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 1, sink.SessionInfoCount())
	info := sink.LastSessionInfo()
	usage, ok := info["context_usage"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, int64(1000), usage["input_tokens"])
	require.Equal(t, int64(128000), usage["context_window"])
	require.Equal(t, 0.05, info["total_cost_usd"])
	require.Equal(t, 0, sink.MessageCount())
}

func TestHandleKiloOutput_Plan(t *testing.T) {
	sink := &testSink{}
	agent := newKiloAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"plan","entries":[{"priority":"medium","status":"pending","content":"Step 1"},{"priority":"medium","status":"completed","content":"Step 2"}]}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 1, sink.MessageCount())
	msg := sink.Messages()[0]
	require.Equal(t, leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, msg.Role)
	var plan struct {
		SessionUpdate string `json:"sessionUpdate"`
		Entries       []struct {
			Status  string `json:"status"`
			Content string `json:"content"`
		} `json:"entries"`
	}
	require.NoError(t, json.Unmarshal(msg.Content, &plan))
	require.Equal(t, "plan", plan.SessionUpdate)
	require.Len(t, plan.Entries, 2)
}

func TestHandleKiloOutput_RequestPermission(t *testing.T) {
	sink := &recordingControlSink{}
	agent := newKiloAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","id":5,"method":"session/request_permission","params":{"sessionId":"s1","toolCall":{"toolCallId":"tc-1","title":"Run command: ls","kind":"execute","status":"pending"},"options":[{"optionId":"once","kind":"allow_once","name":"Allow once"},{"optionId":"always","kind":"allow_always","name":"Always allow"},{"optionId":"reject","kind":"reject_once","name":"Reject"}]}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 1, sink.PersistedControlCount())
	require.Equal(t, 1, sink.BroadcastControlCount())

	rec := sink.LastPersistedControl()
	assert.Equal(t, "5", rec.RequestID)

	var parsed struct {
		Method string `json:"method"`
		ID     int    `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Payload, &parsed))
	assert.Equal(t, "session/request_permission", parsed.Method)
	assert.Equal(t, 5, parsed.ID)

	assert.Equal(t, 0, sink.MessageCount())
}
