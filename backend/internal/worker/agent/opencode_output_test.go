package agent

import (
	"encoding/json"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newOpenCodeAgentWithSink(sink OutputSink) *OpenCodeAgent {
	return &OpenCodeAgent{
		acpBase: acpBase{
			jsonrpcBase: jsonrpcBase{processBase: processBase{
				agentID:      "test-agent",
				providerName: "opencode",
			}},
			sink:      sink,
			sessionID: "test-session",
		},
	}
}

func TestHandleOpenCodeOutput_AgentMessageChunk(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Hello world"}}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 1, sink.StreamChunkCount())
	got := sink.LastStreamChunk()
	require.Equal(t, "agent_message_chunk", got.Method)
	require.Equal(t, "Hello world", string(got.Content))
	require.Equal(t, "", got.SpanID)
	require.Equal(t, 0, sink.MessageCount())
}

func TestHandleOpenCodeOutput_AgentThoughtChunk(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"thinking..."}}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 1, sink.StreamChunkCount())
	got := sink.LastStreamChunk()
	require.Equal(t, "agent_thought_chunk", got.Method)
	require.Equal(t, "thinking...", string(got.Content))
	// Thought chunks are streamed but not persisted immediately — they accumulate.
	require.Equal(t, 0, sink.MessageCount())
	agent.mu.Lock()
	require.Equal(t, "thinking...", agent.turnThinkingText.String())
	agent.mu.Unlock()
}

func TestHandlePromptResponse_PersistsThinkingText(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	// Simulate accumulated thinking and assistant text.
	agent.turnThinkingText.WriteString("let me think about this")
	agent.turnAssistantText.WriteString("Here is the answer.")

	resp := json.RawMessage(`{"stopReason":"end_turn","usage":{"totalTokens":100}}`)
	agent.handleACPPromptResponse(resp, nil)

	// Expect 3 messages: thinking, assistant text, result divider.
	require.Equal(t, 3, sink.MessageCount())

	// First message: thinking text.
	thinkingMsg := sink.Messages()[0]
	require.Equal(t, leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, thinkingMsg.Role)
	var thinkingParsed map[string]interface{}
	require.NoError(t, json.Unmarshal(thinkingMsg.Content, &thinkingParsed))
	require.Equal(t, "agent_thought_chunk", thinkingParsed["sessionUpdate"])
	content := thinkingParsed["content"].(map[string]interface{})
	require.Equal(t, "let me think about this", content["text"])

	// Second message: assistant text.
	assistantMsg := sink.Messages()[1]
	var assistantParsed map[string]interface{}
	require.NoError(t, json.Unmarshal(assistantMsg.Content, &assistantParsed))
	require.Equal(t, "agent_message_chunk", assistantParsed["sessionUpdate"])

	// Third message: result divider.
	resultMsg := sink.Messages()[2]
	require.Equal(t, leapmuxv1.MessageRole_MESSAGE_ROLE_RESULT, resultMsg.Role)

	// Accumulated text should be reset.
	agent.mu.Lock()
	require.Equal(t, "", agent.turnThinkingText.String())
	require.Equal(t, "", agent.turnAssistantText.String())
	agent.mu.Unlock()
}

func TestHandleOpenCodeOutput_ToolCallOpensSpan(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

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

func TestHandleOpenCodeOutput_ToolCallUpdateInProgress(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"in_progress","kind":"execute","title":"bash"}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 1, sink.StreamChunkCount())
	got := sink.LastStreamChunk()
	require.Equal(t, "tc-1", got.SpanID)
	require.Equal(t, "tool_call_update", got.Method)
	require.Equal(t, 0, sink.MessageCount())
}

func TestHandleOpenCodeOutput_ToolCallUpdateCompleted(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

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

func TestHandleOpenCodeOutput_ToolCallUpdateFailed(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"failed","kind":"execute","title":"bash","content":[{"type":"content","content":{"type":"text","text":"error"}}],"rawOutput":{"error":"error"}}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 1, sink.MessageCount())
	msg := sink.Messages()[0]
	require.True(t, msg.Closing)

	require.Equal(t, 1, sink.StreamEndCount())
	closed := sink.ClosedSpans()
	require.Len(t, closed, 1)
	require.Equal(t, "tc-1", closed[0])
}

func TestHandleOpenCodeOutput_UsageUpdate(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"usage_update","used":1000,"size":128000,"cost":{"amount":0.05,"currency":"USD"}}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 1, sink.SessionInfoCount())
	info := sink.LastSessionInfo()
	usage, ok := info["contextUsage"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, int64(1000), usage["inputTokens"])
	require.Equal(t, int64(128000), usage["contextWindow"])
	require.Equal(t, 0.05, info["totalCostUsd"])
	require.Equal(t, 0, sink.MessageCount())
}

func TestHandleOpenCodeOutput_UsageUpdateNoCost(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"usage_update","used":500,"size":64000,"cost":{"amount":0,"currency":"USD"}}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 1, sink.SessionInfoCount())
	info := sink.LastSessionInfo()
	_, hasCost := info["totalCostUsd"]
	require.False(t, hasCost)
}

func TestHandleOpenCodeOutput_Plan(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"plan","entries":[{"priority":"medium","status":"pending","content":"Step 1"},{"priority":"medium","status":"completed","content":"Step 2"}]}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 1, sink.MessageCount())
	msg := sink.Messages()[0]
	require.Equal(t, leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, msg.Role)
	// Verify the content contains the plan entries.
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

func TestHandleOpenCodeOutput_RequestPermission(t *testing.T) {
	sink := &controlTestSink{}
	agent := newOpenCodeAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","id":5,"method":"session/request_permission","params":{"sessionId":"s1","toolCall":{"toolCallId":"tc-1","title":"Run command: ls","kind":"execute","status":"pending"},"options":[{"optionId":"once","kind":"allow_once","name":"Allow once"},{"optionId":"always","kind":"allow_always","name":"Always allow"},{"optionId":"reject","kind":"reject_once","name":"Reject"}]}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 1, sink.PersistedControlCount())
	require.Equal(t, 1, sink.BroadcastControlCount())

	rec := sink.LastPersistedControl()
	assert.Equal(t, "5", rec.RequestID)

	// Verify payload is the original content.
	var parsed struct {
		Method string `json:"method"`
		ID     int    `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Payload, &parsed))
	assert.Equal(t, "session/request_permission", parsed.Method)
	assert.Equal(t, 5, parsed.ID)

	// Should NOT be persisted as a regular message.
	assert.Equal(t, 0, sink.MessageCount())
}

func TestHandleOpenCodeOutput_RequestPermissionWithoutID(t *testing.T) {
	sink := &controlTestSink{}
	agent := newOpenCodeAgentWithSink(sink)

	// Missing "id" field — should be ignored (logged as warning).
	input := `{"method":"session/request_permission","params":{"sessionId":"s1","toolCall":{"toolCallId":"tc-1"}}}`
	agent.HandleOutput([]byte(input))

	assert.Equal(t, 0, sink.PersistedControlCount())
	assert.Equal(t, 0, sink.BroadcastControlCount())
}

func TestHandleOpenCodeOutput_UserMessageChunkIgnored(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"replayed input"}}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 0, sink.MessageCount())
	require.Equal(t, 0, sink.StreamChunkCount())
}

func TestHandleOpenCodeOutput_AvailableCommandsUpdateIgnored(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"compact","description":"compact the session"}]}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 0, sink.MessageCount())
}

func TestHandleOpenCodeOutput_UnknownMethod(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"someUnknownMethod","params":{"data":"test"}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 1, sink.MessageCount())
}

func TestHandleOpenCodeOutput_ToolCallThenCompleted(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	// tool_call opens a span.
	toolCall := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"read","kind":"read","status":"pending","locations":[{"path":"file.txt"}],"rawInput":{"filePath":"file.txt"}}}}`
	agent.HandleOutput([]byte(toolCall))

	// tool_call_update completes it.
	toolUpdate := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"completed","kind":"read","title":"read","content":[{"type":"content","content":{"type":"text","text":"file contents"}}],"rawOutput":{"output":"file contents"}}}}`
	agent.HandleOutput([]byte(toolUpdate))

	require.Equal(t, 2, sink.MessageCount())

	spans := sink.OpenSpans()
	require.Len(t, spans, 1)
	require.Equal(t, "tc-1", spans[0].SpanID)

	closed := sink.ClosedSpans()
	require.Len(t, closed, 1)
	require.Equal(t, "tc-1", closed[0])

	// The completed message should use the span type set by tool_call.
	completedMsg := sink.Messages()[1]
	require.Equal(t, "read", completedMsg.SpanType)
	require.True(t, completedMsg.Closing)
}

func TestUnwrapACPResult(t *testing.T) {
	t.Run("unwraps role=result with content", func(t *testing.T) {
		input := json.RawMessage(`{"id":"msg-1","role":"result","seq":4,"created_at":"2026-03-26T10:46:48.015Z","content":{"_meta":{},"stopReason":"end_turn","usage":{"totalTokens":100}}}`)
		got := unwrapACPResult(input)

		var parsed map[string]interface{}
		require.NoError(t, json.Unmarshal(got, &parsed))
		require.Equal(t, "end_turn", parsed["stopReason"])
		// Should NOT have the wrapper fields.
		_, ok := parsed["role"]
		require.False(t, ok)
	})

	t.Run("returns original when role is not result", func(t *testing.T) {
		input := json.RawMessage(`{"role":"assistant","content":{"text":"hello"}}`)
		got := unwrapACPResult(input)
		require.Equal(t, string(input), string(got))
	})

	t.Run("returns original when no role field", func(t *testing.T) {
		input := json.RawMessage(`{"stopReason":"end_turn","usage":{"totalTokens":100}}`)
		got := unwrapACPResult(input)
		require.Equal(t, string(input), string(got))
	})
}

func TestHandlePromptResponse_WrappedFormat(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	// Simulate a wrapped prompt response with {role: "result", content: {...}}.
	resp := json.RawMessage(`{"id":"msg-1","role":"result","seq":4,"created_at":"2026-03-26T10:46:48.015Z","content":{"_meta":{},"stopReason":"end_turn","usage":{"totalTokens":100}}}`)
	agent.handleACPPromptResponse(resp, nil)

	require.Equal(t, 1, sink.MessageCount())
	msg := sink.Messages()[0]
	require.Equal(t, leapmuxv1.MessageRole_MESSAGE_ROLE_RESULT, msg.Role)

	// The persisted content should have stopReason at the top level.
	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(msg.Content, &parsed))
	require.Equal(t, "end_turn", parsed["stopReason"])
	// num_tool_uses should be injected.
	require.Equal(t, float64(0), parsed["num_tool_uses"])
}

func TestHandleOpenCodeOutput_SessionUpdateResultRoleIgnored(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	// A session/update with role "result" should be ignored (handled by handlePromptResponse).
	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"role":"result","id":"msg-1","seq":4,"created_at":"2026-03-26T10:46:48.015Z","content":{"_meta":{},"stopReason":"end_turn","usage":{"totalTokens":100}}}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 0, sink.MessageCount())
}

func TestHandleOpenCodeOutput_ToolCallUpdateCompletedIncrementsToolUses(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	for i := 0; i < 3; i++ {
		input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call_update","toolCallId":"tc-` + string(rune('1'+i)) + `","status":"completed","kind":"execute","title":"bash"}}}`
		agent.HandleOutput([]byte(input))
	}

	agent.mu.Lock()
	count := agent.turnToolUses
	agent.mu.Unlock()

	require.Equal(t, 3, count)
}
