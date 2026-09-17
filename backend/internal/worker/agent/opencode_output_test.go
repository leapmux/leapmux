package agent

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newOpenCodeAgentWithSink(sink ProviderServices) *OpenCodeAgent {
	a := &OpenCodeAgent{
		acpBase: acpBase{
			jsonrpcBase: jsonrpcBase{processBase: processBase{
				agentID:      "test-agent",
				providerName: "opencode",
			}},
			sink:      sink,
			sessionID: "test-session",
		},
	}
	a.modeChannel = modeChannelPrimaryAgent
	a.primaryAgentHiddenFilter = isHiddenPrimaryAgent
	a.sink = newModelProgressResetSink(a.sink)
	return a
}

func TestHandleOpenCodeOutput_AgentThoughtChunk(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	// A single thought chunk is buffered, not persisted immediately. Only
	// once an interrupting event (here: end-of-turn) arrives does it flush.
	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"thinking..."}}}}`
	agent.HandleOutput([]byte(input))
	require.Equal(t, 0, sink.MessageCount(), "thought chunk should buffer, not persist immediately")

	resp := json.RawMessage(`{"stopReason":"end_turn"}`)
	agent.handleACPPromptResponse(resp)

	// End-of-turn flushes the thought buffer, then persists the (empty) assistant
	// text -- an empty segment writes no row -- then the result divider.
	require.Equal(t, 2, sink.MessageCount())
	thoughtMsg := sink.Messages()[0]
	require.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, thoughtMsg.Source)
	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(thoughtMsg.Content, &parsed))
	require.Equal(t, contracts.AssembledMessageType, parsed["type"])
	require.Equal(t, contracts.AssembledMessageKindReasoning, parsed["kind"])
	require.Equal(t, "thinking...", parsed["text"])
	require.True(t, sink.Messages()[1].TurnEnd)
}

// Live token-by-token streaming (one notification per reasoning delta in
// opencode/src/acp/agent.ts:513) used to produce one "Thinking" box per
// token. They must coalesce into a single message.
func TestHandleOpenCodeOutput_AgentThoughtChunk_TokenCoalescing(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	tokens := []string{"paths ", "while ", "using ", "multi", "_tool", "_use"}
	for _, tok := range tokens {
		payload, err := json.Marshal(map[string]interface{}{
			"jsonrpc": "2.0",
			"method":  "session/update",
			"params": map[string]interface{}{
				"sessionId": "test-session",
				"update": map[string]interface{}{
					"sessionUpdate": "agent_thought_chunk",
					"content":       map[string]interface{}{"type": "text", "text": tok},
				},
			},
		})
		require.NoError(t, err)
		agent.HandleOutput(payload)
	}
	require.Equal(t, 0, sink.MessageCount(), "tokens buffer until interrupted")

	// Tool call interrupts and flushes the thought buffer.
	toolCall := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"read","kind":"read","status":"pending"}}}`
	agent.HandleOutput([]byte(toolCall))

	require.Equal(t, 2, sink.MessageCount())
	var thoughtParsed map[string]interface{}
	require.NoError(t, json.Unmarshal(sink.Messages()[0].Content, &thoughtParsed))
	require.Equal(t, contracts.AssembledMessageKindReasoning, thoughtParsed["kind"])
	require.Equal(t, "paths while using multi_tool_use", thoughtParsed["text"])
	require.Equal(t, "tc-1", sink.Messages()[1].SpanID)
}

// Replay and live streaming use the same ACP chunk variant. The live variant
// can split anywhere, so assembly must preserve both paths verbatim.
func TestHandleOpenCodeOutput_AgentThoughtChunk_ReplayUsesDeltaJoining(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	first := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"**Analyzing tiles**\n\nbody one"}}}}`
	second := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"**Refining grid**\n\nbody two"}}}}`
	agent.HandleOutput([]byte(first))
	agent.HandleOutput([]byte(second))

	resp := json.RawMessage(`{"stopReason":"end_turn"}`)
	agent.handleACPPromptResponse(resp)

	require.Equal(t, 2, sink.MessageCount())
	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(sink.Messages()[0].Content, &parsed))
	require.Equal(t, contracts.AssembledMessageKindReasoning, parsed["kind"])
	require.Equal(t,
		"**Analyzing tiles**\n\nbody one**Refining grid**\n\nbody two",
		parsed["text"],
	)
	require.True(t, sink.Messages()[1].TurnEnd)
}

func TestHandleOpenCodeOutput_AgentThoughtChunk_DoesNotInferAParagraph(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	first := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"I'll validate before giving feedback."}}}}`
	second := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"The proposed hook point exists."}}}}`
	agent.HandleOutput([]byte(first))
	agent.HandleOutput([]byte(second))

	agent.handleACPPromptResponse(json.RawMessage(`{"stopReason":"end_turn"}`))

	require.GreaterOrEqual(t, sink.MessageCount(), 1)
	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(sink.Messages()[0].Content, &parsed))
	require.Equal(t, contracts.AssembledMessageKindReasoning, parsed["kind"])
	require.Equal(t,
		"I'll validate before giving feedback.The proposed hook point exists.",
		parsed["text"],
	)
}

func TestHandleOpenCodeOutput_ThoughtThenToolCallPreservesOrder(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	// Mid-turn tool calls used to wipe the in-flight thinking display
	// because thinking sat in a builder until end-of-turn. The buffer now
	// flushes whenever a non-thought event arrives, so chronological order
	// is thought → tool_call.
	thought := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"about to read a file"}}}}`
	toolCall := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"read","kind":"read","status":"pending"}}}`
	agent.HandleOutput([]byte(thought))
	agent.HandleOutput([]byte(toolCall))

	require.Equal(t, 2, sink.MessageCount())

	var thoughtParsed map[string]interface{}
	require.NoError(t, json.Unmarshal(sink.Messages()[0].Content, &thoughtParsed))
	require.Equal(t, contracts.AssembledMessageKindReasoning, thoughtParsed["kind"])

	require.Equal(t, "tc-1", sink.Messages()[1].SpanID)
	require.Equal(t, "read", sink.Messages()[1].SpanType)
}

func TestHandleOpenCodeOutput_AssistantTextThenToolCallPreservesOrder(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)
	agent.HandleOutput(acpMessageChunk("I will inspect the file."))
	agent.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"read","kind":"read","status":"pending"}}}`))

	require.Len(t, sink.Messages(), 2)
	var assistant map[string]interface{}
	require.NoError(t, json.Unmarshal(sink.Messages()[0].Content, &assistant))
	assert.Equal(t, contracts.AssembledMessageKindText, assistant["kind"])
	assert.Equal(t, "I will inspect the file.", assistant["text"])
	assert.Equal(t, "tc-1", sink.Messages()[1].SpanID)
}

// Trailing thoughts (no interrupting event before end-of-turn) must flush
// before the assistant text + result divider, otherwise they would either
// be lost or persisted out of order after the reply.
func TestHandleOpenCodeOutput_TrailingThoughtFlushedBeforeReply(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	thought := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"final thought"}}}}`
	agent.HandleOutput([]byte(thought))

	agent.turnAssistantText.WriteString("Here is the answer.")
	agent.handleACPPromptResponse(json.RawMessage(`{"stopReason":"end_turn"}`))

	require.Equal(t, 3, sink.MessageCount())

	var thoughtParsed map[string]interface{}
	require.NoError(t, json.Unmarshal(sink.Messages()[0].Content, &thoughtParsed))
	require.Equal(t, contracts.AssembledMessageKindReasoning, thoughtParsed["kind"])

	var assistantParsed map[string]interface{}
	require.NoError(t, json.Unmarshal(sink.Messages()[1].Content, &assistantParsed))
	require.Equal(t, contracts.AssembledMessageKindText, assistantParsed["kind"])

	require.True(t, sink.Messages()[2].TurnEnd)
}

func TestHandleOpenCodePromptResponse_PersistsAssistantText(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	// The end-of-turn flush is responsible only for the assistant-text buffer
	// and the result divider — thought chunks persist per notification, not here.
	agent.turnAssistantText.WriteString("Here is the answer.")

	resp := json.RawMessage(`{"stopReason":"end_turn","usage":{"totalTokens":100}}`)
	agent.handleACPPromptResponse(resp)

	// Expect 2 messages: assistant text, result divider.
	require.Equal(t, 2, sink.MessageCount())

	assistantMsg := sink.Messages()[0]
	require.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, assistantMsg.Source)
	var assistantParsed map[string]interface{}
	require.NoError(t, json.Unmarshal(assistantMsg.Content, &assistantParsed))
	require.Equal(t, contracts.AssembledMessageKindText, assistantParsed["kind"])
	require.Equal(t, contracts.AssembledMessageCompletionComplete, assistantParsed["completion"])

	resultMsg := sink.Messages()[1]
	require.True(t, resultMsg.TurnEnd, "prompt response must route through PersistTurnEnd")

	agent.mu.Lock()
	require.Equal(t, "", agent.turnAssistantText.String())
	agent.mu.Unlock()
}

func TestHandleOpenCodeOutput_ToolCallOpensSpan(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"bash","kind":"execute","status":"pending","locations":[],"rawInput":{}}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 1, sink.MessageCount())
	msg := sink.Messages()[0]
	require.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, msg.Source)
	require.Equal(t, "tc-1", msg.SpanID)
	require.Equal(t, "execute", msg.SpanType)

	spans := sink.OpenSpans()
	require.Len(t, spans, 1)
	require.Equal(t, "tc-1", spans[0].SpanID)
	require.Equal(t, 0, sink.ClosedSpanCount())
}

func TestHandleOpenCodeOutput_ToolCallUpdateInProgress(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	// Status-only in_progress (no content) — must not broadcast a stream
	// chunk, since shipping the raw envelope would let the frontend
	// concatenate it into the command-stream buffer.
	statusOnly := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"in_progress","kind":"execute","title":"bash"}}}`
	agent.HandleOutput([]byte(statusOnly))
	require.Equal(t, 0, sink.MessageCount())

	// in_progress with cumulative text content — broadcast just the new
	// delta, not the raw envelope.
	first := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"in_progress","kind":"execute","title":"bash","content":[{"type":"content","content":{"type":"text","text":"line1\n"}}]}}}`
	second := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"in_progress","kind":"execute","title":"bash","content":[{"type":"content","content":{"type":"text","text":"line1\nline2\n"}}]}}}`
	agent.HandleOutput([]byte(first))
	agent.HandleOutput([]byte(second))
	// Each update reports TWO things: the byte total the meter counts, and the
	// cumulative text the running row draws. The Agent Client Protocol sends the
	// whole output every time, so the second tail carries both lines.
	updates := sink.ProgressUpdates()
	require.Len(t, updates, 4)
	require.Equal(t, int64(6), updates[0].Value)
	require.Equal(t, OutputTailProgress("tc-1", "line1\n", false), updates[1])
	require.Equal(t, int64(12), updates[2].Value)
	require.Equal(t, OutputTailProgress("tc-1", "line1\nline2\n", false), updates[3])
}

func TestHandleOpenCodeOutput_ToolCallUpdateCompleted(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"completed","kind":"execute","title":"bash","content":[{"type":"content","content":{"type":"text","text":"output"}}],"rawOutput":{"output":"output"}}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 1, sink.MessageCount())
	msg := sink.Messages()[0]
	require.Equal(t, "tc-1", msg.SpanID)
	require.True(t, msg.Closing)

	assert.Contains(t, sink.ProgressUpdates(), CompleteOutputProgress("tc-1"))

	closed := sink.ClosedSpans()
	require.Len(t, closed, 1)
	require.Equal(t, "tc-1", closed[0])
}

func TestHandleOpenCodeOutput_ToolCallUpdateFailed(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"failed","kind":"execute","title":"bash","content":[{"type":"content","content":{"type":"text","text":"error"}}],"rawOutput":{"error":"error"}}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 1, sink.MessageCount())
	msg := sink.Messages()[0]
	require.True(t, msg.Closing)

	assert.Contains(t, sink.ProgressUpdates(), CompleteOutputProgress("tc-1"))
	closed := sink.ClosedSpans()
	require.Len(t, closed, 1)
	require.Equal(t, "tc-1", closed[0])
}

func TestHandleOpenCodeOutput_UsageUpdate(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"usage_update","used":1000,"size":128000,"cost":{"amount":0.05,"currency":"USD"}}}}`
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

func TestHandleOpenCodeOutput_UsageUpdateNoCost(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"usage_update","used":500,"size":64000,"cost":{"amount":0,"currency":"USD"}}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 1, sink.SessionInfoCount())
	info := sink.LastSessionInfo()
	_, hasCost := info["total_cost_usd"]
	require.False(t, hasCost)
}

func TestHandleOpenCodeOutput_Plan(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"plan","entries":[{"priority":"medium","status":"pending","content":"Step 1"},{"priority":"medium","status":"completed","content":"Step 2"}]}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 1, sink.MessageCount())
	msg := sink.Messages()[0]
	require.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, msg.Source)
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
	t.Parallel()

	sink := &recordingControlSink{}
	agent := newOpenCodeAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","id":5,"method":"session/request_permission","params":{"sessionId":"test-session","toolCall":{"toolCallId":"tc-1","title":"Run command: ls","kind":"execute","status":"pending"},"options":[{"optionId":"once","kind":"allow_once","name":"Allow once"},{"optionId":"always","kind":"allow_always","name":"Always allow"},{"optionId":"reject","kind":"reject_once","name":"Reject"}]}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 1, sink.PublishedControlCount())

	rec := sink.LastPublishedControl()
	assert.Equal(t, "jsonrpc:5", rec.RequestID)

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
	t.Parallel()

	sink := &recordingControlSink{}
	agent := newOpenCodeAgentWithSink(sink)

	// Missing "id" field — should be ignored (logged as warning).
	input := `{"method":"session/request_permission","params":{"sessionId":"test-session","toolCall":{"toolCallId":"tc-1"}}}`
	agent.HandleOutput([]byte(input))

	assert.Equal(t, 0, sink.PublishedControlCount())
}

func TestHandleOpenCodeOutput_UserMessageChunkIgnored(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"replayed input"}}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 0, sink.MessageCount())
}

func TestHandleOpenCodeOutput_AvailableCommandsUpdateIgnored(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"compact","description":"compact the session"}]}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 0, sink.MessageCount())
}

func TestHandleOpenCodeOutput_UnknownMethod(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"someUnknownMethod","params":{"data":"test"}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 1, sink.MessageCount())
}

func TestHandleOpenCodeOutput_ToolCallThenCompleted(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	// tool_call opens a span.
	toolCall := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"read","kind":"read","status":"pending","locations":[{"path":"file.txt"}],"rawInput":{"filePath":"file.txt"}}}}`
	agent.HandleOutput([]byte(toolCall))

	// tool_call_update completes it.
	toolUpdate := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"completed","kind":"read","title":"read","content":[{"type":"content","content":{"type":"text","text":"file contents"}}],"rawOutput":{"output":"file contents"}}}}`
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

func TestHandlePromptResponse_WrappedFormat(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)
	agent.turnToolUses = 2

	// Simulate a wrapped prompt response with {role: "result", content: {...}}.
	resp := json.RawMessage(`{"id":"msg-1","role":"result","seq":4,"created_at":"2026-03-26T10:46:48.015Z","content":{"_meta":{},"stopReason":"end_turn","usage":{"totalTokens":100}}}`)
	agent.handleACPPromptResponse(resp)

	require.Equal(t, 1, sink.MessageCount())
	msg := sink.Messages()[0]
	require.True(t, msg.TurnEnd, "wrapped prompt response must route through PersistTurnEnd")

	// The renderer resolves the wrapper. Persistence keeps all native fields and bytes.
	require.Equal(t, []byte(resp), msg.Content)
	// The worker count stays outside the native result.
	assert.NotContains(t, string(msg.Content), "num_tool_uses")
	var metadata map[string]any
	require.NoError(t, json.Unmarshal(msg.Metadata, &metadata))
	require.Equal(t, float64(2), metadata["num_tool_uses"])
	assert.Equal(t, 0, agent.turnToolUses)
}

func TestACPTurnCounterPreservesOriginalBytes(t *testing.T) {
	t.Parallel()
	sink := &testSink{}
	a := newOpenCodeAgentWithSink(sink)
	a.turnToolUses = 2
	raw := json.RawMessage(`{"stopReason":"end_turn", "future":9007199254740993}`)
	a.handleACPPromptResponse(raw)
	messages := sink.Messages()
	require.Len(t, messages, 1)
	assert.Equal(t, []byte(raw), messages[0].Content)
	assert.Contains(t, string(messages[0].Metadata), `"num_tool_uses":2`)
}

func TestHandleOpenCodeOutput_SessionUpdateResultRoleIgnored(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	// A session/update with role "result" should be ignored (handled by handlePromptResponse).
	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"role":"result","id":"msg-1","seq":4,"created_at":"2026-03-26T10:46:48.015Z","content":{"_meta":{},"stopReason":"end_turn","usage":{"totalTokens":100}}}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 0, sink.MessageCount())
}

func TestHandleOpenCodeOutput_ToolCallUpdateCompletedIncrementsToolUses(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	for i := 0; i < 3; i++ {
		input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"tool_call_update","toolCallId":"tc-` + string(rune('1'+i)) + `","status":"completed","kind":"execute","title":"bash"}}}`
		agent.HandleOutput([]byte(input))
	}

	agent.mu.Lock()
	count := agent.turnToolUses
	agent.mu.Unlock()

	require.Equal(t, 3, count)
}

// acpChunk builds a streamed chunk for the specified provider session.
func acpChunk(sessionID, sessionUpdate, text string) []byte {
	content, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "method": "session/update",
		"params": map[string]any{
			"sessionId": sessionID,
			"update": map[string]any{
				"sessionUpdate": sessionUpdate,
				"content":       map[string]string{"type": "text", "text": text},
			},
		},
	})
	if err != nil {
		panic(err)
	}
	return content
}

func acpMessageChunk(text string) []byte {
	return acpChunk("test-session", "agent_message_chunk", text)
}

func acpThoughtChunk(text string) []byte {
	return acpChunk("test-session", "agent_thought_chunk", text)
}

func TestHandleACPOutput_MessageChunkAccumulatesThinkingTokens(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	// 8-char chunk -> 2 tokens; a second 8-char chunk accumulates to 4. Assistant
	// text streams here; thought chunks accumulate the same way but buffer.
	agent.HandleOutput(acpMessageChunk("abcdefgh"))
	assert.Equal(t, int64(2), lastThinkingTokens(sink))
	agent.HandleOutput(acpMessageChunk("ijklmnop"))
	assert.Equal(t, int64(4), lastThinkingTokens(sink))

	// A live estimate is broadcast, never persisted (assistant text persists only
	// at end-of-turn).
	assert.Equal(t, 0, sink.MessageCount(), "streamed chunks must not persist")
}

func TestHandleACPOutput_ThoughtChunkAccumulatesThinkingTokens(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	agent.HandleOutput(acpThoughtChunk("abcdefgh"))
	assert.Equal(t, int64(2), lastThinkingTokens(sink))
	agent.HandleOutput(acpThoughtChunk("ijklmnop"))
	assert.Equal(t, int64(4), lastThinkingTokens(sink))

	assert.Equal(t, 0, sink.MessageCount(), "thought chunks buffer, not persist")
	// Reasoning that is NOT preceded by assistant text must NOT trigger the
	// assistant->reasoning hand-off (no spurious 0 clear); it just climbs.
	assert.Equal(t, []interface{}{int64(2), int64(4)}, sessionInfoValues(sink, "thinking_tokens"),
		"a leading reasoning segment climbs without an explicit clear")
}

func TestHandleACPOutput_AssistantTextAfterReasoningResetsViaFlush(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	// Reasoning streams first (buffered).
	agent.HandleOutput(acpThoughtChunk("abcdefghijklmnop")) // 16 chars -> 4
	require.Equal(t, int64(4), lastThinkingTokens(sink))

	// An assistant message chunk flushes the buffered thought as a committed
	// AGENT message (the decorator resets on that persist), so the assistant text
	// is counted on its own from zero -- the thought->message direction of the
	// per-phase reset.
	agent.HandleOutput(acpMessageChunk("abcdefgh")) // 8 chars -> 2

	require.Equal(t, 1, sink.MessageCount(), "the buffered thought is flushed as one message")
	assert.Equal(t, int64(2), lastThinkingTokens(sink), "assistant text restarts from zero after the reasoning flush")
}

func TestHandleACPOutput_ToolCallResetsThinkingTokens(t *testing.T) {
	t.Parallel()

	// Covers both sources: a tool call after assistant text (message->tool, no
	// buffered thought to flush) and after reasoning (thought->tool). Either way
	// the next phase's estimate must restart from zero.
	for _, src := range []struct {
		name  string
		chunk func(string) []byte
	}{
		{"after message", acpMessageChunk},
		{"after thought", acpThoughtChunk},
	} {
		t.Run(src.name, func(t *testing.T) {
			sink := &testSink{}
			agent := newOpenCodeAgentWithSink(sink)

			agent.HandleOutput(src.chunk("abcdefghijklmnop"))
			require.Equal(t, int64(4), lastThinkingTokens(sink))

			// A tool call commits an AGENT message the frontend clears on.
			agent.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"read","kind":"read","status":"pending"}}}`))

			agent.HandleOutput(src.chunk("abcdefgh"))
			assert.Equal(t, int64(2), lastThinkingTokens(sink), "the next phase restarts at 8/4")
		})
	}
}

func TestHandleACPOutput_TurnEndResetsThinkingTokens(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	agent.HandleOutput(acpMessageChunk("abcdefghijklmnop"))
	require.Equal(t, int64(4), lastThinkingTokens(sink))

	// End of turn: the assistant message + result divider commit, and the
	// per-turn estimate resets.
	agent.handleACPPromptResponse(json.RawMessage(`{"stopReason":"end_turn"}`))

	agent.HandleOutput(acpMessageChunk("abcdefgh"))
	assert.Equal(t, int64(2), lastThinkingTokens(sink), "the next turn restarts the estimate")
}

func TestHandleACPOutput_ReasoningAfterAssistantTextStartsFreshPhase(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	// Assistant text streams first; it is buffered until turn end, so it never
	// commits an AGENT message and never triggers a frontend clear.
	agent.HandleOutput(acpMessageChunk("abcdefghijklmnop")) // 16 chars -> 4
	require.Equal(t, int64(4), lastThinkingTokens(sink))

	// A reasoning chunk then opens a new phase. Because the assistant chars were
	// never committed, the backend must explicitly clear the frontend counter (0)
	// and restart, so the reasoning is counted on its own rather than stacked on
	// the assistant total (which would also spin the forward-only odometer back).
	agent.HandleOutput(acpThoughtChunk("abcdefgh")) // 8 chars -> 2

	assert.Equal(t, []interface{}{int64(4), int64(0), int64(2)}, sessionInfoValues(sink, "thinking_tokens"),
		"assistant total, then an explicit clear, then the reasoning counted fresh")
}

func TestHandleACPOutput_NilResultResetsThinkingTokens(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	agent.HandleOutput(acpMessageChunk("abcdefghijklmnop"))
	require.Equal(t, int64(4), lastThinkingTokens(sink))

	// A nil result (errored/aborted turn) still ends the turn. No result divider is
	// persisted, so the frontend gets no turn-end clear of its own -- the abort must
	// broadcast an explicit 0 so the live counter drops now instead of freezing on 4
	// until the next turn streams. The estimate must also not leak into the next
	// turn (ACP has no turn-start reset).
	agent.handleACPPromptResponse(nil)
	assert.Equal(t, int64(0), lastThinkingTokens(sink), "the abort broadcasts an explicit clear")

	agent.HandleOutput(acpMessageChunk("abcdefgh"))
	assert.Equal(t, int64(2), lastThinkingTokens(sink), "a nil-result turn end restarts the estimate")
	assert.Equal(t, []interface{}{int64(4), int64(0), int64(2)}, sessionInfoValues(sink, "thinking_tokens"),
		"assistant total, then the abort's explicit clear, then the next turn counted fresh")
}

func TestHandleACPOutput_NilResultDropsBufferedAssistantText(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	// Turn 1 streams assistant text, then aborts with a nil result. The buffered
	// text was never committed; it must NOT survive into the next turn's reply.
	agent.HandleOutput(acpMessageChunk("STALE-TURN-1"))
	agent.handleACPPromptResponse(nil)

	// Turn 2 streams its own assistant text and ends normally.
	agent.HandleOutput(acpMessageChunk("turn-2-text"))
	agent.handleACPPromptResponse(json.RawMessage(`{"stopReason":"end_turn"}`))

	// The aborted turn keeps its own partial text, marked interrupted.
	assert.Equal(t, "STALE-TURN-1", persistedACPAssistantText(t, sink, MessageCompletionInterrupted),
		"the aborted turn stores the text it did stream")

	// The persisted assistant message for turn 2 must carry only turn 2's text --
	// the aborted turn's buffer was dropped, not prepended.
	assert.Equal(t, "turn-2-text", persistedACPAssistantText(t, sink, MessageCompletionComplete),
		"the aborted turn's buffered assistant text does not leak into the next reply")
}

func TestHandleACPPrompt_FailedRequestMarksTheBufferedTextFailed(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	// A prompt whose REQUEST fails ends the turn with an error rather than an
	// interruption, so the reader can tell a failed model call from a stop the
	// reader asked for. The partial text the agent did stream is kept.
	agent.HandleOutput(acpThoughtChunk("weighing the two call sites"))
	agent.HandleOutput(acpMessageChunk("The change is safe because"))
	agent.finishPromptRequest("test-session", nil, errors.New("transport closed"))

	assert.Equal(t, "The change is safe because", persistedACPAssistantText(t, sink, MessageCompletionError),
		"the failed turn keeps the assistant text it did stream")
	// The reasoning segment ended when the assistant text began, so it is complete.
	// Only the segment the failure cut short carries the error.
	assert.Equal(t, "weighing the two call sites",
		persistedACPText(t, sink, contracts.AssembledMessageKindReasoning, MessageCompletionComplete),
		"a reasoning segment the assistant text already closed stays complete")

	assert.Equal(t, []map[string]interface{}{{
		"type":  contracts.NotificationTypeAgentError,
		"error": "prompt failed: transport closed",
	}}, sink.LeapMuxNotifications(),
		"the failure also reaches the transcript as one agent-error notification")
}

func TestHandleACPOutput_NilResultPersistsInterruptedToolOutput(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	agent.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"command","kind":"execute","status":"pending"}}}`))
	agent.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"in_progress","content":[{"type":"content","content":{"type":"text","text":"partial output"}}]}}}`))
	agent.handleACPPromptResponse(nil)

	require.Len(t, sink.Messages(), 2)
	result := sink.Messages()[1]
	assert.True(t, result.Closing)
	assert.Equal(t, "tc-1", result.SpanID)
	// The stored row is the LAST frame the agent sent, byte for byte. The title and
	// the kind arrived on the opening frame, so they ride the supplement instead of
	// joining a merged object LeapMux would have to invent.
	assert.JSONEq(t, `{
		"sessionUpdate":"tool_call_update",
		"toolCallId":"tc-1",
		"status":"in_progress",
		"content":[{"type":"content","content":{"type":"text","text":"partial output"}}]
	}`, string(result.Content))
	assert.JSONEq(t, `{
		"sessionUpdate":"tool_call_update",
		"toolCallId":"tc-1",
		"status":"in_progress",
		"protocol":{"title":"command","kind":"execute"}
	}`, string(result.SupplementalContent))
	assert.Equal(t, MessageCompletionInterrupted, result.Completion)
}

func TestHandleACPOutput_NilResultClosesAToolWithoutAnUpdate(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	agent.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"command","kind":"execute","status":"pending","rawInput":{"command":"printf partial"}}}}`))
	agent.handleACPPromptResponse(nil)

	require.Len(t, sink.Messages(), 2)
	result := sink.Messages()[1]
	assert.True(t, result.Closing)
	assert.Equal(t, "tc-1", result.SpanID)
	// The opening frame is the only frame the agent sent, so the row is that frame
	// and it keeps the agent's own pending status. The interruption lives in the
	// LeapMux completion column, never in the provider object.
	assert.JSONEq(t, `{
		"sessionUpdate":"tool_call",
		"toolCallId":"tc-1",
		"title":"command",
		"kind":"execute",
		"status":"pending",
		"rawInput":{"command":"printf partial"}
	}`, string(result.Content))
	assert.Empty(t, result.SupplementalContent, "one frame needs no recovered field")
	assert.Equal(t, MessageCompletionInterrupted, result.Completion)
}

func TestHandleACPOutput_CompletedPromptClosesAToolWithoutAFinalUpdate(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	agent.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"command","kind":"execute","status":"pending"}}}`))
	agent.handleACPPromptResponse(json.RawMessage(`{"stopReason":"end_turn"}`))

	require.Len(t, sink.Messages(), 3)
	result := sink.Messages()[1]
	assert.True(t, result.Closing)
	assert.Equal(t, MessageCompletionError, result.Completion)
	assert.True(t, sink.Messages()[2].TurnEnd)
}

func TestHandleACPOutput_FinalToolCallClosesItsEarlierSpan(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)
	pending := []byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"command","kind":"execute","status":"pending"}}}`)
	completed := []byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"command","kind":"execute","status":"completed"}}}`)
	agent.HandleOutput(pending)
	agent.HandleOutput(completed)

	assert.Equal(t, []string{"tc-1"}, sink.ClosedSpans())
	assert.Equal(t, 1, agent.turnToolUses)
	agent.handleACPPromptResponse(nil)
	require.Len(t, sink.Messages(), 2, "the final tool call must remove incomplete state")
}

// persistedACPAssistantText returns the text of the single assembled ASSISTANT
// message that carries the given completion. See {@link persistedACPText}.
func persistedACPAssistantText(t *testing.T, sink *testSink, completion MessageCompletion) string {
	t.Helper()
	return persistedACPText(t, sink, contracts.AssembledMessageKindText, completion)
}

// persistedACPText returns the text of the single assembled message of one kind
// that carries the given completion, and fails the test when the sink holds none
// or more than one. An interrupted or failed turn stores its partial text under the
// SAME envelope as a completed one, so the kind and the completion together are what
// separate the rows.
func persistedACPText(t *testing.T, sink *testSink, kind string, completion MessageCompletion) string {
	t.Helper()
	var found []string
	for _, m := range sink.Messages() {
		if m.TurnEnd {
			continue
		}
		var parsed struct {
			Type       string `json:"type"`
			Kind       string `json:"kind"`
			Text       string `json:"text"`
			Completion string `json:"completion"`
		}
		if json.Unmarshal(m.Content, &parsed) != nil {
			continue
		}
		if parsed.Type != contracts.AssembledMessageType || parsed.Kind != kind {
			continue
		}
		if MessageCompletion(parsed.Completion) == completion {
			found = append(found, parsed.Text)
		}
	}
	require.Len(t, found, 1, "expected exactly one %s %s message", completion, kind)
	return found[0]
}

func TestHandleACPOutput_PermissionRequestResetsThinkingTokens(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	agent.HandleOutput(acpMessageChunk("abcdefghijklmnop"))
	require.Equal(t, int64(4), lastThinkingTokens(sink))

	// The agent paused for permission -- the frontend clears its counter on the
	// control request, so the backend resets to mirror it.
	agent.HandleOutput([]byte(`{"jsonrpc":"2.0","id":5,"method":"session/request_permission","params":{"sessionId":"test-session","toolCall":{"toolCallId":"tc-1","title":"Run","kind":"execute","status":"pending"},"options":[{"optionId":"once","kind":"allow_once","name":"Allow"}]}}`))

	agent.HandleOutput(acpMessageChunk("abcdefgh"))
	assert.Equal(t, int64(2), lastThinkingTokens(sink), "a permission prompt restarts the estimate")
}

func TestHandleACPOutput_UnknownMethodResetsThinkingTokens(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	agent.HandleOutput(acpMessageChunk("abcdefghijklmnop"))
	require.Equal(t, int64(4), lastThinkingTokens(sink))

	// An unknown method persists as an AGENT message the frontend clears on.
	agent.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"some/unknown/method","params":{"foo":"bar"}}`))

	agent.HandleOutput(acpMessageChunk("abcdefgh"))
	assert.Equal(t, int64(2), lastThinkingTokens(sink), "an unknown AGENT-message method restarts the estimate")
}

// The runtime's own session metadata -- its title and its modified time -- is not
// conversation, and one update arrives for every turn. Persisting it put a raw-JSON
// row in the transcript of every Agent Client Protocol provider that sends one.
func TestHandleACPOutput_SessionInfoUpdateReachesNoTranscriptRow(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	agent.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"session_info_update","title":"Math Question","updatedAt":"2026-09-14T02:00:00Z"}}}`))

	assert.Empty(t, sink.Messages(), "session metadata belongs in no transcript row")
}

// An update this build does not recognize still reaches the transcript: a frame that
// carries conversation is worse lost than shown as raw JSON.
func TestHandleACPOutput_AnUnknownUpdateStillReachesTheTranscript(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	agent.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test-session","update":{"sessionUpdate":"an_update_a_later_build_adds","detail":"keep me"}}}`))

	require.Len(t, sink.Messages(), 1)
	assert.JSONEq(t, `{"sessionUpdate":"an_update_a_later_build_adds","detail":"keep me"}`, string(sink.Messages()[0].Content))
}
