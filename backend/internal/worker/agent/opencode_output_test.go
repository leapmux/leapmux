package agent

import (
	"encoding/json"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newOpenCodeAgentWithSink(sink OutputSink) *OpenCodeAgent {
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
	a.sink = newThinkingResetSink(a.sink, &a.thinkingTokens)
	return a
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

	// A single thought chunk is buffered, not persisted immediately. Only
	// once an interrupting event (here: end-of-turn) arrives does it flush.
	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"thinking..."}}}}`
	agent.HandleOutput([]byte(input))
	require.Equal(t, 0, sink.StreamChunkCount())
	require.Equal(t, 0, sink.MessageCount(), "thought chunk should buffer, not persist immediately")

	resp := json.RawMessage(`{"stopReason":"end_turn"}`)
	agent.handleACPPromptResponse(resp, nil)

	// End-of-turn flushes thought buffer, then persists the (empty) assistant
	// text — `persistTextMessage` skips empty — then the result divider.
	require.Equal(t, 2, sink.MessageCount())
	thoughtMsg := sink.Messages()[0]
	require.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, thoughtMsg.Source)
	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(thoughtMsg.Content, &parsed))
	require.Equal(t, "agent_thought_chunk", parsed["sessionUpdate"])
	require.Equal(t, "thinking...", parsed["content"].(map[string]interface{})["text"])
	require.True(t, sink.Messages()[1].TurnEnd)
}

// Live token-by-token streaming (one notification per reasoning delta in
// opencode/src/acp/agent.ts:513) used to produce one "Thinking" box per
// token. They must coalesce into a single message.
func TestHandleOpenCodeOutput_AgentThoughtChunk_TokenCoalescing(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	tokens := []string{"paths ", "while ", "using ", "multi", "_tool", "_use"}
	for _, tok := range tokens {
		payload, err := json.Marshal(map[string]interface{}{
			"jsonrpc": "2.0",
			"method":  "session/update",
			"params": map[string]interface{}{
				"sessionId": "s1",
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
	toolCall := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"read","kind":"read","status":"pending"}}}`
	agent.HandleOutput([]byte(toolCall))

	require.Equal(t, 2, sink.MessageCount())
	var thoughtParsed map[string]interface{}
	require.NoError(t, json.Unmarshal(sink.Messages()[0].Content, &thoughtParsed))
	require.Equal(t, "agent_thought_chunk", thoughtParsed["sessionUpdate"])
	require.Equal(t, "paths while using multi_tool_use", thoughtParsed["content"].(map[string]interface{})["text"])
	require.Equal(t, "tc-1", sink.Messages()[1].SpanID)
}

// Replay (opencode/src/acp/agent.ts:1087) sends each complete reasoning part
// as one notification with no leading newline. Coalescing must insert a
// paragraph break between adjacent markdown-heading sections, otherwise the
// next title gets glued onto the previous body's last sentence.
func TestHandleOpenCodeOutput_AgentThoughtChunk_MultipleNotifications(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	first := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"**Analyzing tiles**\n\nbody one"}}}}`
	second := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"**Refining grid**\n\nbody two"}}}}`
	agent.HandleOutput([]byte(first))
	agent.HandleOutput([]byte(second))

	resp := json.RawMessage(`{"stopReason":"end_turn"}`)
	agent.handleACPPromptResponse(resp, nil)

	require.Equal(t, 2, sink.MessageCount())
	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(sink.Messages()[0].Content, &parsed))
	require.Equal(t, "agent_thought_chunk", parsed["sessionUpdate"])
	require.Equal(t,
		"**Analyzing tiles**\n\nbody one\n\n**Refining grid**\n\nbody two",
		parsed["content"].(map[string]interface{})["text"],
	)
	require.True(t, sink.Messages()[1].TurnEnd)
}

// Sentence-end + capital letter at the seam ("feedback.The") is the other
// replay-style boundary that needs separation.
func TestHandleOpenCodeOutput_AgentThoughtChunk_SentenceBoundary(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	first := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"I'll validate before giving feedback."}}}}`
	second := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"The proposed hook point exists."}}}}`
	agent.HandleOutput([]byte(first))
	agent.HandleOutput([]byte(second))

	agent.handleACPPromptResponse(json.RawMessage(`{"stopReason":"end_turn"}`), nil)

	require.GreaterOrEqual(t, sink.MessageCount(), 1)
	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(sink.Messages()[0].Content, &parsed))
	require.Equal(t,
		"I'll validate before giving feedback.\n\nThe proposed hook point exists.",
		parsed["content"].(map[string]interface{})["text"],
	)
}

func TestHandleOpenCodeOutput_ThoughtThenToolCallPreservesOrder(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	// Mid-turn tool calls used to wipe the in-flight thinking display
	// because thinking sat in a builder until end-of-turn. The buffer now
	// flushes whenever a non-thought event arrives, so chronological order
	// is thought → tool_call.
	thought := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"about to read a file"}}}}`
	toolCall := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"read","kind":"read","status":"pending"}}}`
	agent.HandleOutput([]byte(thought))
	agent.HandleOutput([]byte(toolCall))

	require.Equal(t, 2, sink.MessageCount())

	var thoughtParsed map[string]interface{}
	require.NoError(t, json.Unmarshal(sink.Messages()[0].Content, &thoughtParsed))
	require.Equal(t, "agent_thought_chunk", thoughtParsed["sessionUpdate"])

	require.Equal(t, "tc-1", sink.Messages()[1].SpanID)
	require.Equal(t, "read", sink.Messages()[1].SpanType)
}

// Trailing thoughts (no interrupting event before end-of-turn) must flush
// before the assistant text + result divider, otherwise they would either
// be lost or persisted out of order after the reply.
func TestHandleOpenCodeOutput_TrailingThoughtFlushedBeforeReply(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	thought := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"final thought"}}}}`
	agent.HandleOutput([]byte(thought))

	agent.turnAssistantText.WriteString("Here is the answer.")
	agent.handleACPPromptResponse(json.RawMessage(`{"stopReason":"end_turn"}`), nil)

	require.Equal(t, 3, sink.MessageCount())

	var thoughtParsed map[string]interface{}
	require.NoError(t, json.Unmarshal(sink.Messages()[0].Content, &thoughtParsed))
	require.Equal(t, "agent_thought_chunk", thoughtParsed["sessionUpdate"])

	var assistantParsed map[string]interface{}
	require.NoError(t, json.Unmarshal(sink.Messages()[1].Content, &assistantParsed))
	require.Equal(t, "agent_message_chunk", assistantParsed["sessionUpdate"])

	require.True(t, sink.Messages()[2].TurnEnd)
}

func TestHandleOpenCodePromptResponse_PersistsAssistantText(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	// The end-of-turn flush is responsible only for the assistant-text buffer
	// and the result divider — thought chunks persist per notification, not here.
	agent.turnAssistantText.WriteString("Here is the answer.")

	resp := json.RawMessage(`{"stopReason":"end_turn","usage":{"totalTokens":100}}`)
	agent.handleACPPromptResponse(resp, nil)

	// Expect 2 messages: assistant text, result divider.
	require.Equal(t, 2, sink.MessageCount())

	assistantMsg := sink.Messages()[0]
	require.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, assistantMsg.Source)
	var assistantParsed map[string]interface{}
	require.NoError(t, json.Unmarshal(assistantMsg.Content, &assistantParsed))
	require.Equal(t, "agent_message_chunk", assistantParsed["sessionUpdate"])

	resultMsg := sink.Messages()[1]
	require.True(t, resultMsg.TurnEnd, "prompt response must route through PersistTurnEnd")

	agent.mu.Lock()
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
	require.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, msg.Source)
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

	// Status-only in_progress (no content) — must not broadcast a stream
	// chunk, since shipping the raw envelope would let the frontend
	// concatenate it into the command-stream buffer.
	statusOnly := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"in_progress","kind":"execute","title":"bash"}}}`
	agent.HandleOutput([]byte(statusOnly))
	require.Equal(t, 0, sink.StreamChunkCount(), "in_progress without content must not broadcast")
	require.Equal(t, 0, sink.MessageCount())

	// in_progress with cumulative text content — broadcast just the new
	// delta, not the raw envelope.
	first := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"in_progress","kind":"execute","title":"bash","content":[{"type":"content","content":{"type":"text","text":"line1\n"}}]}}}`
	second := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"in_progress","kind":"execute","title":"bash","content":[{"type":"content","content":{"type":"text","text":"line1\nline2\n"}}]}}}`
	agent.HandleOutput([]byte(first))
	agent.HandleOutput([]byte(second))
	chunks := sink.StreamChunks()
	require.Equal(t, 2, len(chunks))
	require.Equal(t, "line1\n", string(chunks[0].Content))
	require.Equal(t, "line2\n", string(chunks[1].Content))
	require.Equal(t, "tool_call_update", chunks[0].Method)
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
	usage, ok := info["context_usage"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, int64(1000), usage["input_tokens"])
	require.Equal(t, int64(128000), usage["context_window"])
	require.Equal(t, 0.05, info["total_cost_usd"])
	require.Equal(t, 0, sink.MessageCount())
}

func TestHandleOpenCodeOutput_UsageUpdateNoCost(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"usage_update","used":500,"size":64000,"cost":{"amount":0,"currency":"USD"}}}}`
	agent.HandleOutput([]byte(input))

	require.Equal(t, 1, sink.SessionInfoCount())
	info := sink.LastSessionInfo()
	_, hasCost := info["total_cost_usd"]
	require.False(t, hasCost)
}

func TestHandleOpenCodeOutput_Plan(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"plan","entries":[{"priority":"medium","status":"pending","content":"Step 1"},{"priority":"medium","status":"completed","content":"Step 2"}]}}}`
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
	sink := &recordingControlSink{}
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
	sink := &recordingControlSink{}
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
	require.True(t, msg.TurnEnd, "wrapped prompt response must route through PersistTurnEnd")

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

// acpChunk builds a session/update envelope carrying one streamed chunk of the
// given sessionUpdate kind and text. acpMessageChunk / acpThoughtChunk are the
// two kinds the thinking-token tests drive.
func acpChunk(sessionUpdate, text string) []byte {
	return []byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"` + sessionUpdate + `","content":{"type":"text","text":"` + text + `"}}}}`)
}

func acpMessageChunk(text string) []byte { return acpChunk("agent_message_chunk", text) }
func acpThoughtChunk(text string) []byte { return acpChunk("agent_thought_chunk", text) }

func TestHandleACPOutput_MessageChunkAccumulatesThinkingTokens(t *testing.T) {
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
			agent.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"read","kind":"read","status":"pending"}}}`))

			agent.HandleOutput(src.chunk("abcdefgh"))
			assert.Equal(t, int64(2), lastThinkingTokens(sink), "the next phase restarts at 8/4")
		})
	}
}

func TestHandleACPOutput_TurnEndResetsThinkingTokens(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	agent.HandleOutput(acpMessageChunk("abcdefghijklmnop"))
	require.Equal(t, int64(4), lastThinkingTokens(sink))

	// End of turn: the assistant message + result divider commit, and the
	// per-turn estimate resets.
	agent.handleACPPromptResponse(json.RawMessage(`{"stopReason":"end_turn"}`), nil)

	agent.HandleOutput(acpMessageChunk("abcdefgh"))
	assert.Equal(t, int64(2), lastThinkingTokens(sink), "the next turn restarts the estimate")
}

func TestHandleACPOutput_ReasoningAfterAssistantTextStartsFreshPhase(t *testing.T) {
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
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	agent.HandleOutput(acpMessageChunk("abcdefghijklmnop"))
	require.Equal(t, int64(4), lastThinkingTokens(sink))

	// A nil result (errored/aborted turn) still ends the turn. No result divider is
	// persisted, so the frontend gets no turn-end clear of its own -- the abort must
	// broadcast an explicit 0 so the live counter drops now instead of freezing on 4
	// until the next turn streams. The estimate must also not leak into the next
	// turn (ACP has no turn-start reset).
	agent.handleACPPromptResponse(nil, nil)
	assert.Equal(t, int64(0), lastThinkingTokens(sink), "the abort broadcasts an explicit clear")

	agent.HandleOutput(acpMessageChunk("abcdefgh"))
	assert.Equal(t, int64(2), lastThinkingTokens(sink), "a nil-result turn end restarts the estimate")
	assert.Equal(t, []interface{}{int64(4), int64(0), int64(2)}, sessionInfoValues(sink, "thinking_tokens"),
		"assistant total, then the abort's explicit clear, then the next turn counted fresh")
}

func TestHandleACPOutput_NilResultDropsBufferedAssistantText(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	// Turn 1 streams assistant text, then aborts with a nil result. The buffered
	// text was never committed; it must NOT survive into the next turn's reply.
	agent.HandleOutput(acpMessageChunk("STALE-TURN-1"))
	agent.handleACPPromptResponse(nil, nil)

	// Turn 2 streams its own assistant text and ends normally.
	agent.HandleOutput(acpMessageChunk("turn-2-text"))
	agent.handleACPPromptResponse(json.RawMessage(`{"stopReason":"end_turn"}`), nil)

	// The persisted assistant message for turn 2 must carry only turn 2's text --
	// the aborted turn's buffer was dropped, not prepended.
	assert.Equal(t, "turn-2-text", persistedACPAssistantText(t, sink),
		"the aborted turn's buffered assistant text does not leak into the next reply")
}

func TestHandleACPOutput_ReasoningAfterCommittedToolDoesNotReclear(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	// Assistant text streams (buffered), then a reasoning segment opens -> a real
	// hand-off clear (the assistant chars were never committed).
	agent.HandleOutput(acpMessageChunk("abcdefghijklmnop")) // 16 -> 4
	agent.HandleOutput(acpThoughtChunk("abcdefgh"))         // hand-off 0, then 2

	// A tool call commits an AGENT message: the estimator resets and the frontend
	// clears.
	agent.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"read","kind":"read","status":"pending"}}}`))

	// The next reasoning segment must NOT re-fire the hand-off clear: the tool call
	// already cleared the frontend, so a second 0 would be redundant wire traffic.
	// The hand-off gates on the estimator's pending chars (now 0), not the
	// turn-scoped assistant builder (still non-empty), so it stays quiet and the
	// reasoning just climbs from the post-tool reset.
	agent.HandleOutput(acpThoughtChunk("abcdefgh")) // 8 -> 2

	assert.Equal(t, []interface{}{int64(4), int64(0), int64(2), int64(2)}, sessionInfoValues(sink, "thinking_tokens"),
		"no redundant clear after a committed tool call between assistant text and later reasoning")
}

// persistedACPAssistantText returns the text of the single persisted
// agent_message_chunk (the turn-end assistant reply), failing the test if none or
// more than one is present.
func persistedACPAssistantText(t *testing.T, sink *testSink) string {
	t.Helper()
	var found []string
	for _, m := range sink.Messages() {
		if m.TurnEnd {
			continue
		}
		var parsed struct {
			SessionUpdate string `json:"sessionUpdate"`
			Content       struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(m.Content, &parsed) != nil {
			continue
		}
		if parsed.SessionUpdate == "agent_message_chunk" {
			found = append(found, parsed.Content.Text)
		}
	}
	require.Len(t, found, 1, "expected exactly one persisted assistant message")
	return found[0]
}

func TestHandleACPOutput_PermissionRequestResetsThinkingTokens(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	agent.HandleOutput(acpMessageChunk("abcdefghijklmnop"))
	require.Equal(t, int64(4), lastThinkingTokens(sink))

	// The agent paused for permission -- the frontend clears its counter on the
	// control request, so the backend resets to mirror it.
	agent.HandleOutput([]byte(`{"jsonrpc":"2.0","id":5,"method":"session/request_permission","params":{"sessionId":"s1","toolCall":{"toolCallId":"tc-1","title":"Run","kind":"execute","status":"pending"},"options":[{"optionId":"once","kind":"allow_once","name":"Allow"}]}}`))

	agent.HandleOutput(acpMessageChunk("abcdefgh"))
	assert.Equal(t, int64(2), lastThinkingTokens(sink), "a permission prompt restarts the estimate")
}

func TestHandleACPOutput_UnknownMethodResetsThinkingTokens(t *testing.T) {
	sink := &testSink{}
	agent := newOpenCodeAgentWithSink(sink)

	agent.HandleOutput(acpMessageChunk("abcdefghijklmnop"))
	require.Equal(t, int64(4), lastThinkingTokens(sink))

	// An unknown method persists as an AGENT message the frontend clears on.
	agent.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"some/unknown/method","params":{"foo":"bar"}}`))

	agent.HandleOutput(acpMessageChunk("abcdefgh"))
	assert.Equal(t, int64(2), lastThinkingTokens(sink), "an unknown AGENT-message method restarts the estimate")
}
