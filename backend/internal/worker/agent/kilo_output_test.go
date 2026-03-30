package agent

import (
	"encoding/json"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func newKiloAgentWithSink(sink OutputSink) *KiloAgent {
	return &KiloAgent{
		acpBase: acpBase{
			jsonrpcBase: jsonrpcBase{processBase: processBase{
				agentID: "test-agent",
			}},
			sink:         sink,
			providerName: "kilo",
			sessionID:    "test-session",
		},
	}
}

func TestHandleKiloOutput_AgentMessageChunk(t *testing.T) {
	sink := &testSink{}
	agent := newKiloAgentWithSink(sink)

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

func TestHandleKiloOutput_AgentThoughtChunk(t *testing.T) {
	sink := &testSink{}
	agent := newKiloAgentWithSink(sink)

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
	if sink.MessageCount() != 0 {
		t.Fatalf("expected 0 persisted messages, got %d", sink.MessageCount())
	}
	agent.mu.Lock()
	if agent.turnThinkingText.String() != "thinking..." {
		t.Fatalf("expected accumulated thinking text 'thinking...', got %q", agent.turnThinkingText.String())
	}
	agent.mu.Unlock()
}

func TestHandleKiloPromptResponse_PersistsThinkingText(t *testing.T) {
	sink := &testSink{}
	agent := newKiloAgentWithSink(sink)

	agent.turnThinkingText.WriteString("let me think about this")
	agent.turnAssistantText.WriteString("Here is the answer.")

	resp := json.RawMessage(`{"stopReason":"end_turn","usage":{"totalTokens":100}}`)
	agent.handleACPPromptResponse(resp, nil)

	if sink.MessageCount() != 3 {
		t.Fatalf("expected 3 persisted messages, got %d", sink.MessageCount())
	}

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

	assistantMsg := sink.Messages()[1]
	var assistantParsed map[string]interface{}
	if err := json.Unmarshal(assistantMsg.Content, &assistantParsed); err != nil {
		t.Fatalf("failed to unmarshal assistant: %v", err)
	}
	if assistantParsed["sessionUpdate"] != "agent_message_chunk" {
		t.Fatalf("expected sessionUpdate 'agent_message_chunk', got %v", assistantParsed["sessionUpdate"])
	}

	resultMsg := sink.Messages()[2]
	if resultMsg.Role != leapmuxv1.MessageRole_MESSAGE_ROLE_RESULT {
		t.Fatalf("expected RESULT role, got %v", resultMsg.Role)
	}

	agent.mu.Lock()
	if agent.turnThinkingText.String() != "" {
		t.Fatalf("expected empty thinking text after prompt response, got %q", agent.turnThinkingText.String())
	}
	if agent.turnAssistantText.String() != "" {
		t.Fatalf("expected empty assistant text after prompt response, got %q", agent.turnAssistantText.String())
	}
	agent.mu.Unlock()
}

func TestHandleKiloOutput_ToolCallOpensSpan(t *testing.T) {
	sink := &testSink{}
	agent := newKiloAgentWithSink(sink)

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

func TestHandleKiloOutput_ToolCallUpdateCompleted(t *testing.T) {
	sink := &testSink{}
	agent := newKiloAgentWithSink(sink)

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

func TestHandleKiloOutput_UsageUpdate(t *testing.T) {
	sink := &testSink{}
	agent := newKiloAgentWithSink(sink)

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

func TestHandleKiloOutput_Plan(t *testing.T) {
	sink := &testSink{}
	agent := newKiloAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"plan","entries":[{"priority":"medium","status":"pending","content":"Step 1"},{"priority":"medium","status":"completed","content":"Step 2"}]}}}`
	agent.HandleOutput([]byte(input))

	if sink.MessageCount() != 1 {
		t.Fatalf("expected 1 persisted message, got %d", sink.MessageCount())
	}
	msg := sink.Messages()[0]
	if msg.Role != leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT {
		t.Fatalf("expected assistant role, got %v", msg.Role)
	}
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

func TestHandleKiloOutput_RequestPermission(t *testing.T) {
	sink := &controlTestSink{}
	agent := newKiloAgentWithSink(sink)

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

	if sink.MessageCount() != 0 {
		t.Errorf("expected 0 messages, got %d", sink.MessageCount())
	}
}
