package agent

import (
	"encoding/json"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newPiAgentWithSink(sink OutputSink) *PiAgent {
	return &PiAgent{
		processBase: processBase{agentID: "test-agent"},
		sink:        sink,
		sessionFile: "/tmp/pi-session.jsonl",
	}
}

func TestHandlePiOutput_AgentStart_SetsTurnFlag(t *testing.T) {
	sink := &recordingControlSink{}
	a := newPiAgentWithSink(sink)

	handlePiOutput(a, parseLine([]byte(`{"type":"agent_start"}`)))

	a.mu.Lock()
	turnActive := a.currentTurnActive
	a.mu.Unlock()
	assert.True(t, turnActive, "agent_start should mark turn active")

	sink.mu.Lock()
	statusActiveCount := len(sink.statusActives)
	sink.mu.Unlock()
	assert.Equal(t, 0, statusActiveCount, "agent_start must NOT re-broadcast full status — that's a startup-only call")
}

func TestHandlePiOutput_AgentEnd_PersistsResultDividerAndResets(t *testing.T) {
	sink := &recordingControlSink{}
	a := newPiAgentWithSink(sink)
	a.currentTurnActive = true
	a.turnToolUses = 3

	handlePiOutput(a, parseLine([]byte(`{"type":"agent_end","messages":[]}`)))

	a.mu.Lock()
	turnActive := a.currentTurnActive
	toolUses := a.turnToolUses
	a.mu.Unlock()
	assert.False(t, turnActive)
	assert.Equal(t, 0, toolUses)

	require.Equal(t, 1, sink.MessageCount())
	msg := sink.Messages()[0]
	assert.Equal(t, leapmuxv1.MessageRole_MESSAGE_ROLE_TURN_END, msg.Role)

	assert.Equal(t, 1, sink.ResetSpanCount(), "agent_end should reset spans")
	require.Equal(t, 1, sink.SessionInfoCount())
	info := sink.LastSessionInfo()
	assert.Equal(t, false, info["pi_turn_active"])
}

func TestHandlePiOutput_MessageUpdate_TextDelta_StreamsChunk(t *testing.T) {
	sink := &recordingControlSink{}
	a := newPiAgentWithSink(sink)

	handlePiOutput(a, parseLine([]byte(
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"Hello "}}`,
	)))

	require.Equal(t, 1, sink.StreamChunkCount())
	chunk := sink.LastStreamChunk()
	assert.Equal(t, "Hello ", string(chunk.Content))
	assert.Equal(t, "text_delta", chunk.Method)
	assert.Equal(t, "", chunk.SpanID)
	assert.Equal(t, 0, sink.MessageCount(), "deltas should not persist messages")
}

func TestHandlePiOutput_MessageUpdate_ThinkingDelta_StreamsChunk(t *testing.T) {
	sink := &recordingControlSink{}
	a := newPiAgentWithSink(sink)

	handlePiOutput(a, parseLine([]byte(
		`{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","contentIndex":0,"delta":"reasoning"}}`,
	)))

	require.Equal(t, 1, sink.StreamChunkCount())
	chunk := sink.LastStreamChunk()
	assert.Equal(t, "reasoning", string(chunk.Content))
	assert.Equal(t, "thinking_delta", chunk.Method)
}

func TestHandlePiOutput_MessageUpdate_OtherDeltaTypesIgnored(t *testing.T) {
	sink := &recordingControlSink{}
	a := newPiAgentWithSink(sink)

	for _, deltaType := range []string{
		"start", "text_start", "text_end",
		"thinking_start", "thinking_end",
		"toolcall_start", "toolcall_delta", "toolcall_end",
		"done", "error",
	} {
		raw := []byte(`{"type":"message_update","assistantMessageEvent":{"type":"` + deltaType + `"}}`)
		handlePiOutput(a, parseLine(raw))
	}

	assert.Equal(t, 0, sink.StreamChunkCount())
	assert.Equal(t, 0, sink.MessageCount())
}

func TestHandlePiOutput_MessageEnd_PersistsAssistantMessage(t *testing.T) {
	sink := &recordingControlSink{}
	a := newPiAgentWithSink(sink)

	raw := []byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Hello"}]}}`)
	handlePiOutput(a, parseLine(raw))

	require.Equal(t, 1, sink.MessageCount())
	msg := sink.Messages()[0]
	assert.Equal(t, leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, msg.Role)
	assert.JSONEq(t, string(raw), string(msg.Content))
}

func TestHandlePiOutput_MessageEnd_AugmentsUsageAndBroadcastsSessionInfo(t *testing.T) {
	sink := &recordingControlSink{}
	a := newPiAgentWithSink(sink)
	a.model = "gpt-5.5"
	a.availableModels = []*leapmuxv1.AvailableModel{{Id: "gpt-5.5", ContextWindow: 200000}}

	raw := []byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Hello"}],"usage":{"input":100,"output":10,"cacheRead":20,"cacheWrite":5,"totalTokens":135,"cost":{"input":0.0001,"output":0.0002,"cacheRead":0.00001,"cacheWrite":0.00002,"total":0.00033}}}}`)
	handlePiOutput(a, parseLine(raw))

	require.Equal(t, 1, sink.MessageCount())
	msg := sink.Messages()[0]
	assert.Equal(t, leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, msg.Role)

	var persisted map[string]any
	require.NoError(t, json.Unmarshal(msg.Content, &persisted))
	assert.Equal(t, 0.00033, persisted["total_cost_usd"])
	usage, ok := persisted["context_usage"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(100), usage["input_tokens"])
	assert.Equal(t, float64(5), usage["cache_creation_input_tokens"])
	assert.Equal(t, float64(20), usage["cache_read_input_tokens"])
	assert.Equal(t, float64(10), usage["output_tokens"])
	assert.Equal(t, float64(200000), usage["context_window"])

	require.Equal(t, 1, sink.SessionInfoCount())
	info := sink.LastSessionInfo()
	assert.Equal(t, 0.00033, info["total_cost_usd"])
	broadcastUsage, ok := info["context_usage"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, int64(100), broadcastUsage["input_tokens"])
	assert.Equal(t, int64(5), broadcastUsage["cache_creation_input_tokens"])
	assert.Equal(t, int64(20), broadcastUsage["cache_read_input_tokens"])
	assert.Equal(t, int64(10), broadcastUsage["output_tokens"])
	assert.Equal(t, int64(200000), broadcastUsage["context_window"])
}

func TestHandlePiOutput_AgentEnd_AugmentsWithLatestUsageSnapshot(t *testing.T) {
	sink := &recordingControlSink{}
	a := newPiAgentWithSink(sink)
	a.model = "gpt-5.5"
	a.availableModels = []*leapmuxv1.AvailableModel{{Id: "gpt-5.5", ContextWindow: 200000}}

	handlePiOutput(a, parseLine([]byte(`{"type":"message_end","message":{"role":"assistant","usage":{"input":100,"output":10,"cacheRead":20,"cacheWrite":5,"totalTokens":135,"cost":{"total":0.00033}}}}`)))
	handlePiOutput(a, parseLine([]byte(`{"type":"agent_end","messages":[]}`)))

	msgs := sink.Messages()
	require.Equal(t, 2, len(msgs))
	result := msgs[1]
	assert.Equal(t, leapmuxv1.MessageRole_MESSAGE_ROLE_TURN_END, result.Role)

	var persisted map[string]any
	require.NoError(t, json.Unmarshal(result.Content, &persisted))
	assert.Equal(t, "agent_end", persisted["type"])
	assert.Equal(t, 0.00033, persisted["total_cost_usd"])
	usage, ok := persisted["context_usage"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(100), usage["input_tokens"])
	assert.Equal(t, float64(5), usage["cache_creation_input_tokens"])
	assert.Equal(t, float64(20), usage["cache_read_input_tokens"])
	assert.Equal(t, float64(10), usage["output_tokens"])
	assert.Equal(t, float64(200000), usage["context_window"])
}

func TestHandlePiOutput_ToolExecutionLifecycle(t *testing.T) {
	sink := &recordingControlSink{}
	a := newPiAgentWithSink(sink)

	startRaw := []byte(`{"type":"tool_execution_start","toolCallId":"call-1","toolName":"bash","args":{"command":"ls"}}`)
	updateRaw := []byte(`{"type":"tool_execution_update","toolCallId":"call-1","toolName":"bash","partialResult":{"content":[{"type":"text","text":"file1\n"}]}}`)
	endRaw := []byte(`{"type":"tool_execution_end","toolCallId":"call-1","toolName":"bash","result":{"content":[{"type":"text","text":"file1\nfile2\n"}],"details":{"exitCode":0}},"isError":false}`)

	handlePiOutput(a, parseLine(startRaw))
	handlePiOutput(a, parseLine(updateRaw))
	handlePiOutput(a, parseLine(endRaw))

	// Two persisted messages: start (open) and end (closing).
	msgs := sink.Messages()
	require.Equal(t, 2, len(msgs), "tool_execution start/end should persist two messages")
	assert.Equal(t, "call-1", msgs[0].SpanID)
	assert.Equal(t, "bash", msgs[0].SpanType)
	assert.False(t, msgs[0].Closing, "start should not be marked closing")
	assert.Equal(t, "call-1", msgs[1].SpanID)
	assert.True(t, msgs[1].Closing, "end should be marked closing")

	// Span lifecycle: open then close.
	assert.Equal(t, []testSinkSpanOpen{{SpanID: "call-1", ParentSpanID: ""}}, sink.OpenSpans())
	assert.Equal(t, []string{"call-1"}, sink.ClosedSpans())

	// Stream update fanned out into chunk + end. The chunk carries the
	// extracted text delta — not the raw envelope.
	require.Equal(t, 1, sink.StreamChunkCount())
	chunk := sink.LastStreamChunk()
	assert.Equal(t, "call-1", chunk.SpanID)
	assert.Equal(t, "tool_execution_update", chunk.Method)
	assert.Equal(t, "file1\n", string(chunk.Content))
	assert.Equal(t, 1, sink.StreamEndCount())
	assert.Equal(t, "call-1", sink.LastStreamEnd())

	// SetSpanType recorded.
	assert.Equal(t, "bash", sink.GetSpanType("call-1"))

	// Tool count incremented.
	a.mu.Lock()
	defer a.mu.Unlock()
	assert.Equal(t, 1, a.turnToolUses)
}

// Pi's tool_execution_update events carry the *cumulative* partialResult.
// Verify that successive updates only broadcast the new delta, and that the
// per-span tracking is reset when the tool ends so a fresh tool with the same
// id starts from empty.
func TestHandlePiOutput_ToolExecutionUpdate_BroadcastsDeltaOnly(t *testing.T) {
	sink := &recordingControlSink{}
	a := newPiAgentWithSink(sink)

	startRaw := []byte(`{"type":"tool_execution_start","toolCallId":"call-1","toolName":"bash"}`)
	update1 := []byte(`{"type":"tool_execution_update","toolCallId":"call-1","partialResult":{"content":[{"type":"text","text":"line1\n"}]}}`)
	update2 := []byte(`{"type":"tool_execution_update","toolCallId":"call-1","partialResult":{"content":[{"type":"text","text":"line1\nline2\n"}]}}`)
	updateNoNew := []byte(`{"type":"tool_execution_update","toolCallId":"call-1","partialResult":{"content":[{"type":"text","text":"line1\nline2\n"}]}}`)
	endRaw := []byte(`{"type":"tool_execution_end","toolCallId":"call-1","toolName":"bash","result":{"content":[{"type":"text","text":"line1\nline2\n"}]}}`)

	handlePiOutput(a, parseLine(startRaw))
	handlePiOutput(a, parseLine(update1))
	handlePiOutput(a, parseLine(update2))
	handlePiOutput(a, parseLine(updateNoNew))
	handlePiOutput(a, parseLine(endRaw))

	chunks := sink.StreamChunks()
	require.Equal(t, 2, len(chunks), "third update was a no-op delta and must not broadcast")
	assert.Equal(t, "line1\n", string(chunks[0].Content))
	assert.Equal(t, "line2\n", string(chunks[1].Content))

	// After tool_execution_end, per-span state should be cleared so that a new
	// tool reusing the same id starts fresh.
	a.mu.Lock()
	_, present := a.cumulativeBroadcast["call-1"]
	a.mu.Unlock()
	assert.False(t, present, "tool_execution_end should clear cumulativeBroadcast entry")
}

func TestHandlePiOutput_ToolExecutionStart_DropsLineWithoutToolCallID(t *testing.T) {
	sink := &recordingControlSink{}
	a := newPiAgentWithSink(sink)

	handlePiOutput(a, parseLine([]byte(`{"type":"tool_execution_start","toolName":"bash"}`)))

	assert.Equal(t, 0, sink.MessageCount())
	assert.Empty(t, sink.OpenSpans())
}

func TestHandlePiOutput_QueueUpdate_BroadcastsDepth(t *testing.T) {
	sink := &recordingControlSink{}
	a := newPiAgentWithSink(sink)

	handlePiOutput(a, parseLine([]byte(
		`{"type":"queue_update","steering":["a","b"],"followUp":["c"]}`,
	)))

	require.Equal(t, 1, sink.SessionInfoCount())
	info := sink.LastSessionInfo()
	assert.Equal(t, 3, info["pi_queue_depth"])
	assert.Equal(t, 2, info["pi_steering_depth"])
	assert.Equal(t, 1, info["pi_follow_up_depth"])
}

func TestHandlePiOutput_CompactionEvents_PersistAsSystemNotification(t *testing.T) {
	sink := &recordingControlSink{}
	a := newPiAgentWithSink(sink)

	for _, line := range []string{
		`{"type":"compaction_start","reason":"threshold"}`,
		`{"type":"compaction_end","reason":"threshold","aborted":false,"willRetry":false,"result":{"summary":"...","tokensBefore":150000}}`,
	} {
		handlePiOutput(a, parseLine([]byte(line)))
	}

	require.Equal(t, 2, sink.NotificationCount())
	for _, n := range sink.PersistedNotifications() {
		assert.Equal(t, leapmuxv1.MessageRole_MESSAGE_ROLE_SYSTEM, n.Role,
			"Pi-emitted lifecycle events must persist as SYSTEM (LEAPMUX is reserved for worker-synthesized envelopes)")
	}
}

func TestHandlePiOutput_AutoRetryEvents_PersistAsSystemNotification(t *testing.T) {
	sink := &recordingControlSink{}
	a := newPiAgentWithSink(sink)

	handlePiOutput(a, parseLine([]byte(
		`{"type":"auto_retry_start","attempt":1,"maxAttempts":3,"delayMs":2000,"errorMessage":"overloaded"}`,
	)))
	handlePiOutput(a, parseLine([]byte(
		`{"type":"auto_retry_end","success":true,"attempt":2}`,
	)))

	require.Equal(t, 2, sink.NotificationCount())
	for _, n := range sink.PersistedNotifications() {
		assert.Equal(t, leapmuxv1.MessageRole_MESSAGE_ROLE_SYSTEM, n.Role)
	}
}

func TestHandlePiOutput_ExtensionError_PersistAsSystemNotification(t *testing.T) {
	sink := &recordingControlSink{}
	a := newPiAgentWithSink(sink)

	handlePiOutput(a, parseLine([]byte(
		`{"type":"extension_error","extensionPath":"/path/ext.ts","event":"tool_call","error":"boom"}`,
	)))

	require.Equal(t, 1, sink.NotificationCount())
	assert.Equal(t, leapmuxv1.MessageRole_MESSAGE_ROLE_SYSTEM, sink.LastNotification().Role)
}

func TestHandlePiOutput_ExtensionUIRequest_DialogPersistsControlRequest(t *testing.T) {
	cases := []struct {
		method string
		id     string
		raw    string
	}{
		{
			"select", "uuid-select",
			`{"type":"extension_ui_request","id":"uuid-select","method":"select","title":"Allow?","options":["Allow","Block"],"timeout":10000}`,
		},
		{
			"confirm", "uuid-confirm",
			`{"type":"extension_ui_request","id":"uuid-confirm","method":"confirm","title":"Clear?","message":"all gone","timeout":5000}`,
		},
		{
			"input", "uuid-input",
			`{"type":"extension_ui_request","id":"uuid-input","method":"input","title":"Enter","placeholder":"text"}`,
		},
		{
			"editor", "uuid-editor",
			`{"type":"extension_ui_request","id":"uuid-editor","method":"editor","title":"Edit","prefill":"line1\nline2"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			sink := &recordingControlSink{}
			a := newPiAgentWithSink(sink)

			handlePiOutput(a, parseLine([]byte(tc.raw)))

			persisted := sink.PersistedControls()
			require.Equal(t, 1, len(persisted), "should persist one control request")
			assert.Equal(t, tc.id, persisted[0].RequestID)
			// Payload must round-trip the raw line verbatim.
			assert.JSONEq(t, tc.raw, string(persisted[0].Payload))

			broadcast := sink.BroadcastControls()
			require.Equal(t, 1, len(broadcast))
			assert.JSONEq(t, tc.raw, string(broadcast[0].Payload))

			// Should NOT be persisted as a regular message or notification.
			assert.Equal(t, 0, sink.MessageCount())
			assert.Equal(t, 0, sink.NotificationCount())
		})
	}
}

func TestHandlePiOutput_ExtensionUIRequest_DialogWithoutIDIsDropped(t *testing.T) {
	sink := &recordingControlSink{}
	a := newPiAgentWithSink(sink)

	handlePiOutput(a, parseLine([]byte(
		`{"type":"extension_ui_request","method":"select","title":"x","options":["a"]}`,
	)))

	assert.Empty(t, sink.PersistedControls(), "missing id must not persist a control request")
	assert.Empty(t, sink.BroadcastControls())
}

func TestHandlePiOutput_ExtensionUIRequest_NotifyPersistsRawAsSystem(t *testing.T) {
	sink := &recordingControlSink{}
	a := newPiAgentWithSink(sink)

	rawLine := `{"type":"extension_ui_request","id":"x","method":"notify","message":"Hello","notifyType":"warning"}`
	handlePiOutput(a, parseLine([]byte(rawLine)))

	// Single raw passthrough — no synthesized agent_notify wrapper.
	require.Equal(t, 1, sink.NotificationCount(), "single raw extension_ui_request notification persisted")
	last := sink.LastNotification()
	assert.Equal(t, leapmuxv1.MessageRole_MESSAGE_ROLE_SYSTEM, last.Role,
		"Pi-emitted notify must persist as SYSTEM (the agent is the source)")
	assert.JSONEq(t, rawLine, string(last.Content),
		"raw envelope must be preserved verbatim so renderers can read every method-specific field")

	// Synthesis dropped — the frontend renderer derives level/message from the raw envelope.
	assert.Empty(t, sink.Notifications(),
		"synthesized agent_notify must no longer be emitted; raw passthrough alone carries the same info")

	// Should NOT be a control request.
	assert.Empty(t, sink.PersistedControls())
}

func TestHandlePiOutput_ExtensionUIRequest_NotifyMissingNotifyTypePreservesRaw(t *testing.T) {
	sink := &recordingControlSink{}
	a := newPiAgentWithSink(sink)

	handlePiOutput(a, parseLine([]byte(
		`{"type":"extension_ui_request","id":"x","method":"notify","message":"hi"}`,
	)))

	require.Equal(t, 1, sink.NotificationCount())
	last := sink.LastNotification()
	assert.Equal(t, leapmuxv1.MessageRole_MESSAGE_ROLE_SYSTEM, last.Role)
	// notifyType absent in the raw — renderer is responsible for defaulting to "info".
	assert.NotContains(t, string(last.Content), `"notifyType"`)
	assert.Empty(t, sink.Notifications(), "no synthesized agent_notify on the side channel")
}

func TestHandlePiOutput_ExtensionUIRequest_SetStatus_BroadcastsSessionInfo(t *testing.T) {
	sink := &recordingControlSink{}
	a := newPiAgentWithSink(sink)

	statusValue := "Turn 3 running…"
	body := map[string]any{
		"type":       "extension_ui_request",
		"id":         "x",
		"method":     "setStatus",
		"statusKey":  "my-ext",
		"statusText": statusValue,
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)

	handlePiOutput(a, parseLine(raw))

	require.Equal(t, 1, sink.SessionInfoCount())
	status, ok := sink.LastSessionInfo()["pi_status"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, statusValue, status["my-ext"])
}

func TestHandlePiOutput_ExtensionUIRequest_SetStatus_NilClearsKey(t *testing.T) {
	sink := &recordingControlSink{}
	a := newPiAgentWithSink(sink)

	// statusText omitted → broadcast a nil value so the frontend clears the key.
	handlePiOutput(a, parseLine([]byte(
		`{"type":"extension_ui_request","id":"x","method":"setStatus","statusKey":"my-ext"}`,
	)))

	require.Equal(t, 1, sink.SessionInfoCount())
	status, ok := sink.LastSessionInfo()["pi_status"].(map[string]any)
	require.True(t, ok)
	val, present := status["my-ext"]
	assert.True(t, present, "key should be present so the frontend can clear it")
	assert.Nil(t, val)
}

func TestHandlePiOutput_ExtensionUIRequest_SetWidget_BroadcastsSessionInfo(t *testing.T) {
	sink := &recordingControlSink{}
	a := newPiAgentWithSink(sink)

	handlePiOutput(a, parseLine([]byte(
		`{"type":"extension_ui_request","id":"x","method":"setWidget","widgetKey":"my-ext","widgetLines":["a","b"],"widgetPlacement":"belowEditor"}`,
	)))

	require.Equal(t, 1, sink.SessionInfoCount())
	widgets, ok := sink.LastSessionInfo()["pi_widget"].(map[string]any)
	require.True(t, ok)
	widget, ok := widgets["my-ext"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "belowEditor", widget["placement"])
	assert.NotNil(t, widget["lines"])
}

func TestHandlePiOutput_ExtensionUIRequest_SetTitle_BroadcastsSessionInfo(t *testing.T) {
	sink := &recordingControlSink{}
	a := newPiAgentWithSink(sink)

	handlePiOutput(a, parseLine([]byte(
		`{"type":"extension_ui_request","id":"x","method":"setTitle","title":"pi - my project"}`,
	)))

	require.Equal(t, 1, sink.SessionInfoCount())
	assert.Equal(t, "pi - my project", sink.LastSessionInfo()["pi_terminal_title"])
}

func TestHandlePiOutput_ExtensionUIRequest_SetEditorText_BroadcastsSessionInfo(t *testing.T) {
	sink := &recordingControlSink{}
	a := newPiAgentWithSink(sink)

	handlePiOutput(a, parseLine([]byte(
		`{"type":"extension_ui_request","id":"x","method":"set_editor_text","text":"prefilled"}`,
	)))

	require.Equal(t, 1, sink.SessionInfoCount())
	assert.Equal(t, "prefilled", sink.LastSessionInfo()["pi_editor_text"])
}

func TestHandlePiOutput_ExtensionUIRequest_UnknownMethod_PersistAsNotification(t *testing.T) {
	sink := &recordingControlSink{}
	a := newPiAgentWithSink(sink)

	handlePiOutput(a, parseLine([]byte(
		`{"type":"extension_ui_request","id":"x","method":"someFutureMethod","payload":"opaque"}`,
	)))

	assert.Equal(t, 1, sink.NotificationCount())
	assert.Empty(t, sink.PersistedControls())
}

func TestHandlePiOutput_ResponseLineWithoutPendingID_LoggedNotPersisted(t *testing.T) {
	sink := &recordingControlSink{}
	a := newPiAgentWithSink(sink)

	// A response that wasn't intercepted (no pending caller). Should be
	// logged and dropped, not persisted.
	handlePiOutput(a, parseLine([]byte(
		`{"type":"response","id":"orphan","command":"prompt","success":true}`,
	)))

	assert.Equal(t, 0, sink.MessageCount())
	assert.Equal(t, 0, sink.NotificationCount())
}

func TestHandlePiOutput_UnknownEventType_PersistedAsAssistant(t *testing.T) {
	sink := &recordingControlSink{}
	a := newPiAgentWithSink(sink)

	handlePiOutput(a, parseLine([]byte(`{"type":"future_event","stuff":1}`)))

	require.Equal(t, 1, sink.MessageCount())
	assert.Equal(t, leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, sink.Messages()[0].Role)
}
