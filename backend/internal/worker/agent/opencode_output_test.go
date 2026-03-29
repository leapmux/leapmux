package agent

import (
	"encoding/json"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func newOpenCodeAgentWithSink(sink OutputSink) *OpenCodeAgent {
	return &OpenCodeAgent{
		acpBase: acpBase{
			jsonrpcBase: jsonrpcBase{processBase: processBase{
				agentID: "test-agent",
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

	if sink.StreamChunkCount() != 1 {
		t.Fatalf("expected 1 stream chunk, got %d", sink.StreamChunkCount())
	}
	got := sink.LastStreamChunk()
	if got.Method != "agent_message_chunk" {
		t.Fatalf("expected method agent_message_chunk, got %q", got.Method)
	}
	if string(got.Content) != "Hello world" {
		t.Fatalf("expected content 'Hello world', got %q", string(got.Content))
	}
	if got.SpanID != "" {
		t.Fatalf("expected empty spanID, got %q", got.SpanID)
	}
	if sink.MessageCount() != 0 {
		t.Fatalf("expected 0 persisted messages, got %d", sink.MessageCount())
	}
}

func TestHandleOpenCodeOutput_AgentThoughtChunk(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"thinking..."}}}}`
	agent.HandleOutput([]byte(input))

	if sink.StreamChunkCount() != 1 {
		t.Fatalf("expected 1 stream chunk, got %d", sink.StreamChunkCount())
	}
	got := sink.LastStreamChunk()
	if got.Method != "agent_thought_chunk" {
		t.Fatalf("expected method agent_thought_chunk, got %q", got.Method)
	}
	if string(got.Content) != "thinking..." {
		t.Fatalf("expected content 'thinking...', got %q", string(got.Content))
	}
	// Thought chunks are streamed but not persisted immediately — they accumulate.
	if sink.MessageCount() != 0 {
		t.Fatalf("expected 0 persisted messages, got %d", sink.MessageCount())
	}
	agent.mu.Lock()
	if agent.turnThinkingText.String() != "thinking..." {
		t.Fatalf("expected accumulated thinking text 'thinking...', got %q", agent.turnThinkingText.String())
	}
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
	if sink.MessageCount() != 3 {
		t.Fatalf("expected 3 persisted messages, got %d", sink.MessageCount())
	}

	// First message: thinking text.
	thinkingMsg := sink.Messages()[0]
	if thinkingMsg.Role != leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT {
		t.Fatalf("expected ASSISTANT role for thinking, got %v", thinkingMsg.Role)
	}
	var thinkingParsed map[string]interface{}
	if err := json.Unmarshal(thinkingMsg.Content, &thinkingParsed); err != nil {
		t.Fatalf("failed to unmarshal thinking: %v", err)
	}
	if thinkingParsed["sessionUpdate"] != "agent_thought_chunk" {
		t.Fatalf("expected sessionUpdate 'agent_thought_chunk', got %v", thinkingParsed["sessionUpdate"])
	}
	content := thinkingParsed["content"].(map[string]interface{})
	if content["text"] != "let me think about this" {
		t.Fatalf("expected thinking text, got %v", content["text"])
	}

	// Second message: assistant text.
	assistantMsg := sink.Messages()[1]
	var assistantParsed map[string]interface{}
	if err := json.Unmarshal(assistantMsg.Content, &assistantParsed); err != nil {
		t.Fatalf("failed to unmarshal assistant: %v", err)
	}
	if assistantParsed["sessionUpdate"] != "agent_message_chunk" {
		t.Fatalf("expected sessionUpdate 'agent_message_chunk', got %v", assistantParsed["sessionUpdate"])
	}

	// Third message: result divider.
	resultMsg := sink.Messages()[2]
	if resultMsg.Role != leapmuxv1.MessageRole_MESSAGE_ROLE_RESULT {
		t.Fatalf("expected RESULT role, got %v", resultMsg.Role)
	}

	// Accumulated text should be reset.
	agent.mu.Lock()
	if agent.turnThinkingText.String() != "" {
		t.Fatalf("expected empty thinking text after prompt response, got %q", agent.turnThinkingText.String())
	}
	if agent.turnAssistantText.String() != "" {
		t.Fatalf("expected empty assistant text after prompt response, got %q", agent.turnAssistantText.String())
	}
	agent.mu.Unlock()
}

func TestHandleOpenCodeOutput_ToolCallOpensSpan(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"bash","kind":"execute","status":"pending","locations":[],"rawInput":{}}}}`
	agent.HandleOutput([]byte(input))

	if sink.MessageCount() != 1 {
		t.Fatalf("expected 1 persisted message, got %d", sink.MessageCount())
	}
	msg := sink.Messages()[0]
	if msg.Role != leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT {
		t.Fatalf("expected assistant role, got %v", msg.Role)
	}
	if msg.SpanID != "tc-1" {
		t.Fatalf("expected spanID tc-1, got %q", msg.SpanID)
	}
	if msg.SpanType != "execute" {
		t.Fatalf("expected spanType 'execute', got %q", msg.SpanType)
	}

	spans := sink.OpenSpans()
	if len(spans) != 1 || spans[0].SpanID != "tc-1" {
		t.Fatalf("expected 1 open span tc-1, got %v", spans)
	}
	if sink.ClosedSpanCount() != 0 {
		t.Fatalf("expected no closed spans, got %d", sink.ClosedSpanCount())
	}
}

func TestHandleOpenCodeOutput_ToolCallUpdateInProgress(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"in_progress","kind":"execute","title":"bash"}}}`
	agent.HandleOutput([]byte(input))

	if sink.StreamChunkCount() != 1 {
		t.Fatalf("expected 1 stream chunk, got %d", sink.StreamChunkCount())
	}
	got := sink.LastStreamChunk()
	if got.SpanID != "tc-1" {
		t.Fatalf("expected spanID tc-1, got %q", got.SpanID)
	}
	if got.Method != "tool_call_update" {
		t.Fatalf("expected method tool_call_update, got %q", got.Method)
	}
	if sink.MessageCount() != 0 {
		t.Fatalf("expected 0 persisted messages, got %d", sink.MessageCount())
	}
}

func TestHandleOpenCodeOutput_ToolCallUpdateCompleted(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"completed","kind":"execute","title":"bash","content":[{"type":"content","content":{"type":"text","text":"output"}}],"rawOutput":{"output":"output"}}}}`
	agent.HandleOutput([]byte(input))

	if sink.MessageCount() != 1 {
		t.Fatalf("expected 1 persisted message, got %d", sink.MessageCount())
	}
	msg := sink.Messages()[0]
	if msg.SpanID != "tc-1" {
		t.Fatalf("expected spanID tc-1, got %q", msg.SpanID)
	}
	if !msg.Closing {
		t.Fatalf("expected closing=true")
	}

	if sink.StreamEndCount() != 1 {
		t.Fatalf("expected 1 stream end, got %d", sink.StreamEndCount())
	}
	if got := sink.LastStreamEnd(); got != "tc-1" {
		t.Fatalf("expected stream end for tc-1, got %q", got)
	}

	closed := sink.ClosedSpans()
	if len(closed) != 1 || closed[0] != "tc-1" {
		t.Fatalf("expected 1 closed span tc-1, got %v", closed)
	}
}

func TestHandleOpenCodeOutput_ToolCallUpdateFailed(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"failed","kind":"execute","title":"bash","content":[{"type":"content","content":{"type":"text","text":"error"}}],"rawOutput":{"error":"error"}}}}`
	agent.HandleOutput([]byte(input))

	if sink.MessageCount() != 1 {
		t.Fatalf("expected 1 persisted message, got %d", sink.MessageCount())
	}
	msg := sink.Messages()[0]
	if !msg.Closing {
		t.Fatalf("expected closing=true for failed tool")
	}

	if sink.StreamEndCount() != 1 {
		t.Fatalf("expected 1 stream end, got %d", sink.StreamEndCount())
	}
	closed := sink.ClosedSpans()
	if len(closed) != 1 || closed[0] != "tc-1" {
		t.Fatalf("expected 1 closed span tc-1, got %v", closed)
	}
}

func TestHandleOpenCodeOutput_UsageUpdate(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"usage_update","used":1000,"size":128000,"cost":{"amount":0.05,"currency":"USD"}}}}`
	agent.HandleOutput([]byte(input))

	if sink.SessionInfoCount() != 1 {
		t.Fatalf("expected 1 session info broadcast, got %d", sink.SessionInfoCount())
	}
	info := sink.LastSessionInfo()
	usage, ok := info["contextUsage"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected contextUsage map, got %#v", info["contextUsage"])
	}
	if usage["inputTokens"] != int64(1000) {
		t.Fatalf("expected inputTokens 1000, got %#v", usage["inputTokens"])
	}
	if usage["contextWindow"] != int64(128000) {
		t.Fatalf("expected contextWindow 128000, got %#v", usage["contextWindow"])
	}
	if info["totalCostUsd"] != 0.05 {
		t.Fatalf("expected totalCostUsd 0.05, got %#v", info["totalCostUsd"])
	}
	if sink.MessageCount() != 0 {
		t.Fatalf("expected 0 persisted messages, got %d", sink.MessageCount())
	}
}

func TestHandleOpenCodeOutput_UsageUpdateNoCost(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"usage_update","used":500,"size":64000,"cost":{"amount":0,"currency":"USD"}}}}`
	agent.HandleOutput([]byte(input))

	if sink.SessionInfoCount() != 1 {
		t.Fatalf("expected 1 session info broadcast, got %d", sink.SessionInfoCount())
	}
	info := sink.LastSessionInfo()
	if _, hasCost := info["totalCostUsd"]; hasCost {
		t.Fatalf("expected no totalCostUsd when amount is 0, got %#v", info["totalCostUsd"])
	}
}

func TestHandleOpenCodeOutput_Plan(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"plan","entries":[{"priority":"medium","status":"pending","content":"Step 1"},{"priority":"medium","status":"completed","content":"Step 2"}]}}}`
	agent.HandleOutput([]byte(input))

	if sink.MessageCount() != 1 {
		t.Fatalf("expected 1 persisted message, got %d", sink.MessageCount())
	}
	msg := sink.Messages()[0]
	if msg.Role != leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT {
		t.Fatalf("expected assistant role, got %v", msg.Role)
	}
	// Verify the content contains the plan entries.
	var plan struct {
		SessionUpdate string `json:"sessionUpdate"`
		Entries       []struct {
			Status  string `json:"status"`
			Content string `json:"content"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(msg.Content, &plan); err != nil {
		t.Fatalf("failed to unmarshal plan: %v", err)
	}
	if plan.SessionUpdate != "plan" {
		t.Fatalf("expected sessionUpdate 'plan', got %q", plan.SessionUpdate)
	}
	if len(plan.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(plan.Entries))
	}
}

func TestHandleOpenCodeOutput_RequestPermission(t *testing.T) {
	sink := &controlTestSink{}
	agent := newOpenCodeAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","id":5,"method":"session/request_permission","params":{"sessionId":"s1","toolCall":{"toolCallId":"tc-1","title":"Run command: ls","kind":"execute","status":"pending"},"options":[{"optionId":"once","kind":"allow_once","name":"Allow once"},{"optionId":"always","kind":"allow_always","name":"Always allow"},{"optionId":"reject","kind":"reject_once","name":"Reject"}]}}`
	agent.HandleOutput([]byte(input))

	if sink.PersistedControlCount() != 1 {
		t.Fatalf("expected 1 persisted control request, got %d", sink.PersistedControlCount())
	}
	if sink.BroadcastControlCount() != 1 {
		t.Fatalf("expected 1 broadcast control request, got %d", sink.BroadcastControlCount())
	}

	rec := sink.LastPersistedControl()
	if rec.RequestID != "5" {
		t.Errorf("expected requestID '5', got %q", rec.RequestID)
	}

	// Verify payload is the original content.
	var parsed struct {
		Method string `json:"method"`
		ID     int    `json:"id"`
	}
	if err := json.Unmarshal(rec.Payload, &parsed); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if parsed.Method != "session/request_permission" {
		t.Errorf("expected method 'session/request_permission', got %q", parsed.Method)
	}
	if parsed.ID != 5 {
		t.Errorf("expected id 5, got %d", parsed.ID)
	}

	// Should NOT be persisted as a regular message.
	if sink.MessageCount() != 0 {
		t.Errorf("expected 0 messages, got %d", sink.MessageCount())
	}
}

func TestHandleOpenCodeOutput_RequestPermissionWithoutID(t *testing.T) {
	sink := &controlTestSink{}
	agent := newOpenCodeAgentWithSink(sink)

	// Missing "id" field — should be ignored (logged as warning).
	input := `{"method":"session/request_permission","params":{"sessionId":"s1","toolCall":{"toolCallId":"tc-1"}}}`
	agent.HandleOutput([]byte(input))

	if sink.PersistedControlCount() != 0 {
		t.Errorf("expected 0 persisted control requests (no id), got %d", sink.PersistedControlCount())
	}
	if sink.BroadcastControlCount() != 0 {
		t.Errorf("expected 0 broadcast control requests (no id), got %d", sink.BroadcastControlCount())
	}
}

func TestHandleOpenCodeOutput_UserMessageChunkIgnored(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"replayed input"}}}}`
	agent.HandleOutput([]byte(input))

	if sink.MessageCount() != 0 {
		t.Fatalf("expected 0 persisted messages for user_message_chunk, got %d", sink.MessageCount())
	}
	if sink.StreamChunkCount() != 0 {
		t.Fatalf("expected 0 stream chunks for user_message_chunk, got %d", sink.StreamChunkCount())
	}
}

func TestHandleOpenCodeOutput_AvailableCommandsUpdateIgnored(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"compact","description":"compact the session"}]}}}`
	agent.HandleOutput([]byte(input))

	if sink.MessageCount() != 0 {
		t.Fatalf("expected 0 persisted messages for available_commands_update, got %d", sink.MessageCount())
	}
}

func TestHandleOpenCodeOutput_UnknownMethod(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"someUnknownMethod","params":{"data":"test"}}`
	agent.HandleOutput([]byte(input))

	if sink.MessageCount() != 1 {
		t.Fatalf("expected 1 persisted message for unknown method, got %d", sink.MessageCount())
	}
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

	if sink.MessageCount() != 2 {
		t.Fatalf("expected 2 persisted messages, got %d", sink.MessageCount())
	}

	spans := sink.OpenSpans()
	if len(spans) != 1 || spans[0].SpanID != "tc-1" {
		t.Fatalf("expected 1 open span tc-1, got %v", spans)
	}

	closed := sink.ClosedSpans()
	if len(closed) != 1 || closed[0] != "tc-1" {
		t.Fatalf("expected 1 closed span tc-1, got %v", closed)
	}

	// The completed message should use the span type set by tool_call.
	completedMsg := sink.Messages()[1]
	if completedMsg.SpanType != "read" {
		t.Fatalf("expected spanType 'read', got %q", completedMsg.SpanType)
	}
	if !completedMsg.Closing {
		t.Fatalf("expected closing=true for completed tool_call_update")
	}
}

func TestUnwrapACPResult(t *testing.T) {
	t.Run("unwraps role=result with content", func(t *testing.T) {
		input := json.RawMessage(`{"id":"msg-1","role":"result","seq":4,"created_at":"2026-03-26T10:46:48.015Z","content":{"_meta":{},"stopReason":"end_turn","usage":{"totalTokens":100}}}`)
		got := unwrapACPResult(input)

		var parsed map[string]interface{}
		if err := json.Unmarshal(got, &parsed); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}
		if parsed["stopReason"] != "end_turn" {
			t.Fatalf("expected stopReason 'end_turn' at top level, got %v", parsed["stopReason"])
		}
		// Should NOT have the wrapper fields.
		if _, ok := parsed["role"]; ok {
			t.Fatalf("expected no 'role' field after unwrap, got %v", parsed["role"])
		}
	})

	t.Run("returns original when role is not result", func(t *testing.T) {
		input := json.RawMessage(`{"role":"assistant","content":{"text":"hello"}}`)
		got := unwrapACPResult(input)
		if string(got) != string(input) {
			t.Fatalf("expected unchanged output for non-result role")
		}
	})

	t.Run("returns original when no role field", func(t *testing.T) {
		input := json.RawMessage(`{"stopReason":"end_turn","usage":{"totalTokens":100}}`)
		got := unwrapACPResult(input)
		if string(got) != string(input) {
			t.Fatalf("expected unchanged output when no role field")
		}
	})
}

func TestHandlePromptResponse_WrappedFormat(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	// Simulate a wrapped prompt response with {role: "result", content: {...}}.
	resp := json.RawMessage(`{"id":"msg-1","role":"result","seq":4,"created_at":"2026-03-26T10:46:48.015Z","content":{"_meta":{},"stopReason":"end_turn","usage":{"totalTokens":100}}}`)
	agent.handleACPPromptResponse(resp, nil)

	if sink.MessageCount() != 1 {
		t.Fatalf("expected 1 persisted message, got %d", sink.MessageCount())
	}
	msg := sink.Messages()[0]
	if msg.Role != leapmuxv1.MessageRole_MESSAGE_ROLE_RESULT {
		t.Fatalf("expected RESULT role, got %v", msg.Role)
	}

	// The persisted content should have stopReason at the top level.
	var parsed map[string]interface{}
	if err := json.Unmarshal(msg.Content, &parsed); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if parsed["stopReason"] != "end_turn" {
		t.Fatalf("expected stopReason 'end_turn' at top level, got %v", parsed["stopReason"])
	}
	// num_tool_uses should be injected.
	if parsed["num_tool_uses"] != float64(0) {
		t.Fatalf("expected num_tool_uses 0, got %v", parsed["num_tool_uses"])
	}
}

func TestHandleOpenCodeOutput_SessionUpdateResultRoleIgnored(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	// A session/update with role "result" should be ignored (handled by handlePromptResponse).
	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"role":"result","id":"msg-1","seq":4,"created_at":"2026-03-26T10:46:48.015Z","content":{"_meta":{},"stopReason":"end_turn","usage":{"totalTokens":100}}}}}`
	agent.HandleOutput([]byte(input))

	if sink.MessageCount() != 0 {
		t.Fatalf("expected 0 persisted messages for session/update with role=result, got %d", sink.MessageCount())
	}
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

	if count != 3 {
		t.Fatalf("expected 3 tool uses, got %d", count)
	}
}
