package agent

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type startupStatusGuardSink struct {
	testSink
	t *testing.T
}

func (s *startupStatusGuardSink) PersistMessage(source leapmuxv1.MessageSource, content []byte, span SpanInfo) error {
	s.t.Fatalf("startup status notification must not be persisted as a regular message: source=%v content=%s", source, string(content))
	return nil
}

func newCodexAgentWithSink(sink OutputSink) *CodexAgent {
	a := &CodexAgent{
		jsonrpcBase: jsonrpcBase{processBase: processBase{
			agentID: "test-agent",
		}},
		sink:     sink,
		threadID: "main-thread",
	}
	a.sink = newThinkingResetSink(a.sink, &a.thinkingTokens)
	return a
}

func TestHandleCodexOutput_TurnStartedBroadcastsTurnID(t *testing.T) {
	sink := &recordingControlSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","method":"turn/started","params":{"threadId":"t1","turn":{"id":"turn-42"}}}`
	handleCodexOutput(agent, parseLine([]byte(input)))

	sink.mu.Lock()
	statusActiveCount := len(sink.statusActives)
	sessionInfos := append([]map[string]interface{}(nil), sink.sessionInfos...)
	sink.mu.Unlock()
	assert.Equal(t, 0, statusActiveCount, "turn/started must NOT re-broadcast full status")
	require.Equal(t, 1, len(sessionInfos), "turn/started should broadcast the codex_turn_id session info")
	assert.Equal(t, "turn-42", sessionInfos[0]["codex_turn_id"])
}

func TestHandleCodexOutput_TurnStartedFallbackIsNoop(t *testing.T) {
	sink := &recordingControlSink{}
	agent := newCodexAgentWithSink(sink)

	// turn/started with no turn.id has no per-turn state to broadcast;
	// git status now refreshes at turn-end via the sink layer.
	input := `{"jsonrpc":"2.0","method":"turn/started","params":{"threadId":"t1"}}`
	handleCodexOutput(agent, parseLine([]byte(input)))

	sink.mu.Lock()
	statusActiveCount := len(sink.statusActives)
	sessionInfoCount := len(sink.sessionInfos)
	sink.mu.Unlock()
	assert.Equal(t, 0, statusActiveCount, "turn/started fallback must NOT re-broadcast full status")
	assert.Equal(t, 0, sessionInfoCount, "turn/started fallback should not broadcast a turn ID")
}

func TestHandleCodexOutput_RequestUserInput(t *testing.T) {
	sink := &recordingControlSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","id":42,"method":"item/tool/requestUserInput","params":{"threadId":"t1","turnId":"turn1","itemId":"item1","questions":[{"id":"q1","header":"Header","question":"Which option?","options":[{"label":"A"}]}]}}`

	handleCodexOutput(agent, parseLine([]byte(input)))

	require.Equal(t, 1, sink.PersistedControlCount())
	require.Equal(t, 1, sink.BroadcastControlCount())

	rec := sink.LastPersistedControl()
	assert.Equal(t, "42", rec.RequestID)

	// Verify payload is the original content.
	var parsed struct {
		Method string `json:"method"`
		ID     int    `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Payload, &parsed))
	assert.Equal(t, "item/tool/requestUserInput", parsed.Method)
	assert.Equal(t, 42, parsed.ID)

	// Should NOT be persisted as a regular message.
	assert.Equal(t, 0, sink.MessageCount())
}

func TestHandleCodexOutput_CommandExecutionApproval(t *testing.T) {
	sink := &recordingControlSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","id":7,"method":"item/commandExecution/requestApproval","params":{"command":"rm -rf /","reason":"cleanup"}}`

	handleCodexOutput(agent, parseLine([]byte(input)))

	require.Equal(t, 1, sink.PersistedControlCount())

	rec := sink.LastPersistedControl()
	assert.Equal(t, "7", rec.RequestID)
}

func TestHandleCodexOutput_FileChangeApproval(t *testing.T) {
	sink := &recordingControlSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","id":8,"method":"item/fileChange/requestApproval","params":{"reason":"editing file"}}`

	handleCodexOutput(agent, parseLine([]byte(input)))

	require.Equal(t, 1, sink.PersistedControlCount())

	rec := sink.LastPersistedControl()
	assert.Equal(t, "8", rec.RequestID)
}

func TestHandleCodexOutput_PermissionsApproval(t *testing.T) {
	sink := &recordingControlSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","id":9,"method":"item/permissions/requestApproval","params":{"reason":"needs access"}}`

	handleCodexOutput(agent, parseLine([]byte(input)))

	require.Equal(t, 1, sink.PersistedControlCount())

	rec := sink.LastPersistedControl()
	assert.Equal(t, "9", rec.RequestID)
}

func TestHandleCodexOutput_PlanDelta(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"item/plan/delta","params":{"delta":"# Plan\n"}}`
	handleCodexOutput(agent, parseLine([]byte(input)))

	require.Equal(t, 1, sink.StreamChunkCount())
	got := sink.LastStreamChunk()
	require.Equal(t, "item/plan/delta", got.Method)
	require.Equal(t, "", got.SpanID)
	require.Equal(t, "# Plan\n", string(got.Content))

	// Verify the streaming_type session info was broadcast with "plan". (The
	// plan delta also broadcasts a thinking_tokens estimate, so assert on the
	// streaming_type key rather than the total broadcast count.)
	streamingType, ok := lastSessionInfoValue(sink, "streaming_type")
	require.True(t, ok)
	assert.Equal(t, "plan", streamingType)
	require.Equal(t, 1, countSessionInfoWithKey(sink, "streaming_type"))

	// Second delta should NOT broadcast streaming_type again.
	input2 := `{"method":"item/plan/delta","params":{"delta":"Step 1\n"}}`
	handleCodexOutput(agent, parseLine([]byte(input2)))

	require.Equal(t, 2, sink.StreamChunkCount())
	assert.Equal(t, 1, countSessionInfoWithKey(sink, "streaming_type"))

	// Should NOT be persisted as a regular message.
	assert.Equal(t, 0, sink.MessageCount())
}

func TestHandleCodexOutput_ContextCompactionStartPersistsRawAsAgent(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"item/started","params":{"item":{"type":"contextCompaction","id":"compact-1"},"threadId":"t1","turnId":"turn1"}}`
	handleCodexOutput(agent, parseLine([]byte(input)))

	require.Equal(t, 1, sink.NotificationCount())
	require.Equal(t, 0, sink.MessageCount())
	last := sink.LastNotification()
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, last.Source,
		"contextCompaction must persist as AGENT (Codex-emitted, not LeapMux-synthesized)")
	assert.JSONEq(t, input, string(last.Content),
		"raw JSON-RPC envelope must be preserved verbatim — synthesized {type:\"compacting\"} discarded item.id and threadId")
}

func TestHandleCodexOutput_McpStartupStatusPersistsAsAgent(t *testing.T) {
	sink := &startupStatusGuardSink{t: t}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"mcpServer/startupStatus/updated","params":{"name":"codex_apps","status":"ready"}}`
	handleCodexOutput(agent, parseLine([]byte(input)))

	require.Equal(t, 1, sink.NotificationCount())
	require.Equal(t, 0, sink.MessageCount())
	last := sink.LastNotification()
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, last.Source,
		"Codex-emitted MCP startup-status updates must persist as AGENT (not LEAPMUX)")
	assert.JSONEq(t, input, string(last.Content), "raw JSON-RPC envelope must be preserved verbatim")
}

func TestHandleCodexOutput_ThreadNameUpdatedPersistsRawAsAgent(t *testing.T) {
	sink := &recordingControlSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"thread/name/updated","params":{"threadId":"thread-1","name":"Refactoring auth"}}`
	handleCodexOutput(agent, parseLine([]byte(input)))

	require.Equal(t, 1, sink.NotificationCount(),
		"thread/name/updated must persist for reconnect rehydration")
	require.Equal(t, 0, sink.MessageCount(),
		"thread/name/updated must NOT fall through to the default AGENT branch")
	last := sink.LastNotification()
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, last.Source,
		"Codex-emitted lifecycle metadata must persist as AGENT")
	assert.JSONEq(t, input, string(last.Content),
		"raw envelope must be preserved so future renderers can read every field")
}

func TestHandleCodexOutput_MetadataNotificationsPersistRawAsAgent(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
	}{
		{
			name:  "skills changed",
			input: `{"method":"skills/changed","params":{}}`,
		},
		{
			name:  "remote control status changed",
			input: `{"method":"remoteControl/status/changed","params":{"status":"disabled","environmentId":null}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &recordingControlSink{}
			agent := newCodexAgentWithSink(sink)

			handleCodexOutput(agent, parseLine([]byte(tc.input)))

			require.Equal(t, 1, sink.NotificationCount())
			require.Equal(t, 0, sink.MessageCount(),
				"Codex metadata notifications must not fall through to the default AGENT branch")
			last := sink.LastNotification()
			assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, last.Source,
				"Codex-emitted metadata must persist as AGENT")
			assert.JSONEq(t, tc.input, string(last.Content),
				"raw JSON-RPC envelope must be preserved verbatim")
		})
	}
}

func TestHandleCodexOutput_RateLimitExceededSchedulesResume(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"account/rateLimits/updated","params":{"rateLimits":{"primary":{"usedPercent":100,"windowDurationMins":300,"resetsAt":1893456000},"secondary":{"usedPercent":20,"windowDurationMins":10080,"resetsAt":1894000000}}}}`
	handleCodexOutput(agent, parseLine([]byte(input)))

	require.Equal(t, 1, sink.AutoScheduleCount())
	schedule := sink.LastAutoSchedule()
	require.Equal(t, AutoContinueReasonRateLimit, schedule.Reason)
	require.True(t, schedule.DueAt.Equal(time.Unix(1893456000, 0).UTC()))

	// Raw notification persisted as AGENT (agent-emitted metadata).
	require.Equal(t, 1, sink.NotificationCount())
	last := sink.LastNotification()
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, last.Source)
	assert.JSONEq(t, input, string(last.Content))
}

// TestHandleCodexOutput_RateLimitBroadcastsSnakeCaseWire locks in the
// snake_case wire shape for Codex's session-info `rate_limits` payload.
// Both Codex and Claude broadcast the same tier shape so the frontend
// can consume one format regardless of provider.
func TestHandleCodexOutput_RateLimitBroadcastsSnakeCaseWire(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"account/rateLimits/updated","params":{"rateLimits":{"primary":{"usedPercent":85,"windowDurationMins":300,"resetsAt":1893456000},"secondary":{"usedPercent":10,"windowDurationMins":10080,"resetsAt":1894000000}}}}`
	handleCodexOutput(agent, parseLine([]byte(input)))

	require.Equal(t, 1, sink.SessionInfoCount())
	info := sink.LastSessionInfo()
	rateLimits, ok := info["rate_limits"].(map[string]interface{})
	require.True(t, ok, "broadcast must carry rate_limits in snake_case, got %#v", info)

	primary, ok := rateLimits["five_hour"].(map[string]interface{})
	require.True(t, ok, "primary tier should be keyed by rate_limit_type=five_hour")
	assert.Equal(t, "five_hour", primary["rate_limit_type"])
	assert.Equal(t, "allowed_warning", primary["status"])
	assert.Equal(t, 0.85, primary["utilization"])
	assert.Equal(t, int64(1893456000), primary["resets_at"])

	secondary, ok := rateLimits["seven_day"].(map[string]interface{})
	require.True(t, ok, "secondary tier should be keyed by rate_limit_type=seven_day")
	assert.Equal(t, "seven_day", secondary["rate_limit_type"])
}

func TestHandleCodexOutput_RateLimitClearCancelsResume(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"account/rateLimits/updated","params":{"rateLimits":{"primary":{"usedPercent":75,"windowDurationMins":300,"resetsAt":1893456000},"secondary":{"usedPercent":10,"windowDurationMins":10080,"resetsAt":1894000000}}}}`
	handleCodexOutput(agent, parseLine([]byte(input)))

	require.Equal(t, 1, sink.AutoCancelCount())
	require.Equal(t, AutoContinueReasonRateLimit, sink.LastAutoCancel())
}

func TestHandleCodexOutput_TurnFailedServerOverloadedSchedulesResume(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"019d8b39-6599-7081-8901-53f80c6c56b7","items":[],"status":"failed","error":{"message":"Selected model is at capacity. Please try a different model.","codexErrorInfo":"serverOverloaded","additionalDetails":null}}}}`
	handleCodexOutput(agent, parseLine([]byte(input)))

	require.Equal(t, 1, sink.AutoScheduleCount())
	schedule := sink.LastAutoSchedule()
	require.Equal(t, AutoContinueReasonAPIError, schedule.Reason)
	require.False(t, schedule.DueAt.IsZero())
	require.NotEmpty(t, schedule.SourcePayload)
}

func TestHandleCodexOutput_TurnFailedNonOverloadedCancelsAPIErrorResume(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","items":[],"status":"failed","error":{"message":"Something else failed","codexErrorInfo":"invalidRequest","additionalDetails":null}}}}`
	handleCodexOutput(agent, parseLine([]byte(input)))

	require.Equal(t, 1, sink.AutoCancelCount())
	require.Equal(t, AutoContinueReasonAPIError, sink.LastAutoCancel())
}

func TestHandleCodexOutput_TurnCompletedFailedRetryableSchedulesAPIError(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)
	agent.threadID = "main-thread"

	input := `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"failed","items":[],"error":{"message":"stream disconnected before completion: An error occurred while processing your request.","codexErrorInfo":"other","additionalDetails":null}}}}`
	handleCodexOutput(agent, parseLine([]byte(input)))

	require.Equal(t, 1, sink.MessageCount())
	require.Equal(t, 1, sink.AutoScheduleCount())
	schedule := sink.LastAutoSchedule()
	require.Equal(t, AutoContinueReasonAPIError, schedule.Reason)
	require.Equal(t, string(sink.Messages()[0].Content), string(schedule.SourcePayload))
	require.Equal(t, 0, sink.AutoCancelCount())
}

func TestIsRetryableCodexTurnFailure(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    bool
	}{
		{"exact phrase", "stream disconnected before completion", true},
		{"colon suffix", "stream disconnected before completion: An error occurred while processing your request.", true},
		{"dash suffix", "stream disconnected before completion - upstream connection closed", true},
		{"double punctuation", "stream disconnected before completion:: retry later", true},
		{"alphanumeric suffix not matched", "stream disconnected before completionX", false},
		{"different message", "Request was aborted by the user.", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRetryableCodexTurnFailure(tt.message)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestHandleCodexOutput_TurnCompletedFailedNonRetryableCancelsAPIError(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)
	agent.threadID = "main-thread"

	input := `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"failed","items":[],"error":{"message":"Request was aborted by the user.","codexErrorInfo":"other","additionalDetails":null}}}}`
	handleCodexOutput(agent, parseLine([]byte(input)))

	require.Equal(t, 0, sink.AutoScheduleCount())
	require.Equal(t, 1, sink.AutoCancelCount())
	require.Equal(t, AutoContinueReasonAPIError, sink.LastAutoCancel())
}

func TestHandleCodexOutput_TurnCompletedSuccessCancelsAPIError(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)
	agent.threadID = "main-thread"

	input := `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed","items":[],"error":null}}}`
	handleCodexOutput(agent, parseLine([]byte(input)))

	require.Equal(t, 0, sink.AutoScheduleCount())
	require.Equal(t, 1, sink.AutoCancelCount())
	require.Equal(t, AutoContinueReasonAPIError, sink.LastAutoCancel())
}

func TestHandleCodexOutput_SpawnAgentStartedOpensSubagentSpan(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{}}}}`
	handleCodexOutput(agent, parseLine([]byte(input)))

	got := sink.OpenSpans()
	require.Len(t, got, 1)
	require.Equal(t, "call-1", got[0].SpanID)
	require.Equal(t, "", got[0].ParentSpanID)
	require.Equal(t, 0, sink.ClosedSpanCount())
}

func TestHandleCodexOutput_WaitMessagesStayInsideSpawnAgentSpan(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	spawnStarted := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{}}}}`
	waitStarted := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-2","tool":"wait","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":null,"model":null,"reasoningEffort":null,"agentsStates":{}}}}`
	waitCompleted := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-2","tool":"wait","status":"completed","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":null,"model":null,"reasoningEffort":null,"agentsStates":{"child-1":{"status":"completed","message":"done"}}}}}`

	handleCodexOutput(agent, parseLine([]byte(spawnStarted)))
	handleCodexOutput(agent, parseLine([]byte(waitStarted)))
	handleCodexOutput(agent, parseLine([]byte(waitCompleted)))

	messages := sink.Messages()
	require.Len(t, messages, 3)
	require.Equal(t, "call-1", messages[1].ParentSpanID)
	require.Equal(t, "call-1", messages[2].ParentSpanID)
	require.True(t, messages[2].Closing)
	require.Equal(t, "call-1", messages[2].ConnectorSpanID)
	openSpans := sink.OpenSpans()
	require.Len(t, openSpans, 1)
	require.Equal(t, "call-1", openSpans[0].SpanID)
	closedSpans := sink.ClosedSpans()
	require.Len(t, closedSpans, 1)
	require.Equal(t, "call-1", closedSpans[0])
}

func TestHandleCodexOutput_SubagentCommandPersistsVisibleParentSpan(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	spawnStarted := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{}}}}`
	cmdStarted := `{"method":"item/started","params":{"threadId":"child-1","turnId":"turn2","item":{"type":"commandExecution","id":"cmd-1","status":"inProgress","command":"ls","cwd":"/tmp","processId":"123","commandActions":[]}}}`
	cmdCompleted := `{"method":"item/completed","params":{"threadId":"child-1","turnId":"turn2","item":{"type":"commandExecution","id":"cmd-1","status":"completed","command":"ls","cwd":"/tmp","processId":"123","commandActions":[],"aggregatedOutput":"ok","exitCode":0,"durationMs":1}}}`

	handleCodexOutput(agent, parseLine([]byte(spawnStarted)))
	handleCodexOutput(agent, parseLine([]byte(cmdStarted)))
	handleCodexOutput(agent, parseLine([]byte(cmdCompleted)))

	messages := sink.Messages()
	require.Len(t, messages, 3)
	require.Equal(t, "call-1", messages[1].ParentSpanID)
	require.Equal(t, "call-1", messages[2].ParentSpanID)
}

func TestHandleCodexOutput_SpawnAgentCompletedDoesNotCloseSubagentSpan(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"completed","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{"child-1":{"status":"running","message":null}}}}}`
	handleCodexOutput(agent, parseLine([]byte(input)))

	require.Equal(t, 0, sink.ClosedSpanCount())
}

func TestHandleCodexOutput_SpawnAgentCompletedRegistersLateReceiverThreads(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	started := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":[],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{}}}}`
	completed := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"completed","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{"child-1":{"status":"running","message":null}}}}}`
	cmdStarted := `{"method":"item/started","params":{"threadId":"child-1","turnId":"turn2","item":{"type":"commandExecution","id":"cmd-1","status":"inProgress","command":"ls","cwd":"/tmp","processId":"123","commandActions":[]}}}`

	handleCodexOutput(agent, parseLine([]byte(started)))
	handleCodexOutput(agent, parseLine([]byte(completed)))
	handleCodexOutput(agent, parseLine([]byte(cmdStarted)))

	openSpans := sink.OpenSpans()
	require.Len(t, openSpans, 2)
	require.Equal(t, "call-1", openSpans[0].SpanID)
	require.Equal(t, "cmd-1", openSpans[1].SpanID)
	require.Equal(t, "call-1", openSpans[1].ParentSpanID)
	messages := sink.Messages()
	require.Len(t, messages, 3)
	require.Equal(t, "call-1", messages[2].ParentSpanID)
}

func TestHandleCodexOutput_WaitCompletedClosesTerminalSubagentSpan(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-2","tool":"wait","status":"completed","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":null,"model":null,"reasoningEffort":null,"agentsStates":{"child-1":{"status":"completed","message":"done"}}}}}`
	handleCodexOutput(agent, parseLine([]byte(input)))

	require.Empty(t, sink.ClosedSpans())
}

func TestHandleCodexOutput_WaitCompletedDoesNotCloseNonTerminalOrMissingStatuses(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-2","tool":"wait","status":"completed","senderThreadId":"main-thread","receiverThreadIds":["child-1","child-2"],"prompt":null,"model":null,"reasoningEffort":null,"agentsStates":{"child-1":{"status":"running","message":null}}}}}`
	handleCodexOutput(agent, parseLine([]byte(input)))

	require.Empty(t, sink.ClosedSpans())
}

func TestHandleCodexOutput_CloseAgentCompletedClosesSubagentSpan(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-3","tool":"closeAgent","status":"completed","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":null,"model":null,"reasoningEffort":null,"agentsStates":{"child-1":{"status":"shutdown","message":null}}}}}`
	handleCodexOutput(agent, parseLine([]byte(input)))

	require.Empty(t, sink.ClosedSpans())
}

func TestHandleCodexOutput_WaitCompletedClosesOnlyTerminalReceivers(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-4","tool":"wait","status":"completed","senderThreadId":"main-thread","receiverThreadIds":["child-1","child-2","child-3"],"prompt":null,"model":null,"reasoningEffort":null,"agentsStates":{"child-1":{"status":"completed","message":"done"},"child-2":{"status":"running","message":null},"child-3":{"status":"notFound","message":null}}}}}`
	handleCodexOutput(agent, parseLine([]byte(input)))

	require.Empty(t, sink.ClosedSpans())
}

func TestHandleCodexOutput_WaitCompletedClosesParentSpawnOnlyAfterLastReceiverFinishes(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	spawnStarted := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1","child-2"],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{}}}}`
	waitCompletedFirst := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-2","tool":"wait","status":"completed","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":null,"model":null,"reasoningEffort":null,"agentsStates":{"child-1":{"status":"completed","message":"done"}}}}}`
	waitCompletedSecond := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-3","tool":"wait","status":"completed","senderThreadId":"main-thread","receiverThreadIds":["child-2"],"prompt":null,"model":null,"reasoningEffort":null,"agentsStates":{"child-2":{"status":"completed","message":"done"}}}}}`

	handleCodexOutput(agent, parseLine([]byte(spawnStarted)))
	handleCodexOutput(agent, parseLine([]byte(waitCompletedFirst)))

	require.Empty(t, sink.ClosedSpans())

	handleCodexOutput(agent, parseLine([]byte(waitCompletedSecond)))

	closedSpans := sink.ClosedSpans()
	require.Len(t, closedSpans, 1)
	require.Equal(t, "call-1", closedSpans[0])
}

func TestHandleCodexOutput_ThreadCompactedPersistsRawAsAgent(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"thread/compacted","params":{"threadId":"t1","turnId":"turn1"}}`
	handleCodexOutput(agent, parseLine([]byte(input)))

	require.Equal(t, 1, sink.NotificationCount())
	require.Equal(t, 0, sink.MessageCount())
	last := sink.LastNotification()
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, last.Source,
		"thread/compacted must persist as AGENT (Codex-emitted)")
	assert.JSONEq(t, input, string(last.Content),
		"raw JSON-RPC envelope must be preserved verbatim — frontend keys off `method:\"thread/compacted\"`")
}

func TestHandleCodexOutput_CommandExecutionOutputDelta(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"item/commandExecution/outputDelta","params":{"itemId":"cmd-1","delta":"hello\n","threadId":"t1","turnId":"turn1"}}`
	handleCodexOutput(agent, parseLine([]byte(input)))

	require.Equal(t, 1, sink.StreamChunkCount())
	got := sink.LastStreamChunk()
	require.Equal(t, "cmd-1", got.SpanID)
	require.Equal(t, "item/commandExecution/outputDelta", got.Method)
	require.Equal(t, "hello\n", string(got.Content))
	require.Equal(t, 0, sink.MessageCount())
}

func TestHandleCodexOutput_ReasoningSummaryTextDelta(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"item/reasoning/summaryTextDelta","params":{"itemId":"reason-1","delta":"thinking...","summaryIndex":0,"threadId":"t1","turnId":"turn1"}}`
	handleCodexOutput(agent, parseLine([]byte(input)))

	require.Equal(t, 1, sink.StreamChunkCount())
	got := sink.LastStreamChunk()
	require.Equal(t, "reason-1", got.SpanID)
	require.Equal(t, "item/reasoning/summaryTextDelta", got.Method)
	require.Equal(t, "thinking...", string(got.Content))
	require.Equal(t, 0, sink.MessageCount())
}

func TestHandleCodexOutput_ReasoningSummaryPartAdded(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"item/reasoning/summaryPartAdded","params":{"itemId":"reason-1","summaryIndex":1,"threadId":"t1","turnId":"turn1"}}`
	handleCodexOutput(agent, parseLine([]byte(input)))

	require.Equal(t, 1, sink.StreamChunkCount())
	got := sink.LastStreamChunk()
	require.Equal(t, "reason-1", got.SpanID)
	require.Equal(t, "item/reasoning/summaryPartAdded", got.Method)
	require.Empty(t, got.Content)
	require.Equal(t, 0, sink.MessageCount())
}

func TestHandleCodexOutput_ReasoningTextDelta(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"item/reasoning/textDelta","params":{"itemId":"reason-1","delta":"raw chain","contentIndex":0,"threadId":"t1","turnId":"turn1"}}`
	handleCodexOutput(agent, parseLine([]byte(input)))

	require.Equal(t, 1, sink.StreamChunkCount())
	got := sink.LastStreamChunk()
	require.Equal(t, "reason-1", got.SpanID)
	require.Equal(t, "item/reasoning/textDelta", got.Method)
	require.Equal(t, "raw chain", string(got.Content))
	require.Equal(t, 0, sink.MessageCount())
}

func TestHandleCodexOutput_CommandExecutionTerminalInteraction(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"item/commandExecution/terminalInteraction","params":{"itemId":"cmd-1","processId":"123","stdin":"y\n","threadId":"t1","turnId":"turn1"}}`
	handleCodexOutput(agent, parseLine([]byte(input)))

	require.Equal(t, 1, sink.StreamChunkCount())
	got := sink.LastStreamChunk()
	require.Equal(t, "cmd-1", got.SpanID)
	require.Equal(t, "item/commandExecution/terminalInteraction", got.Method)
	require.Equal(t, "y\n", string(got.Content))
	require.Equal(t, 0, sink.MessageCount())
}

func TestHandleCodexOutput_FileChangeOutputDelta(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"item/fileChange/outputDelta","params":{"itemId":"fc-1","delta":"diff --git a.txt b.txt\n","threadId":"t1","turnId":"turn1"}}`
	handleCodexOutput(agent, parseLine([]byte(input)))

	require.Equal(t, 1, sink.StreamChunkCount())
	got := sink.LastStreamChunk()
	require.Equal(t, "fc-1", got.SpanID)
	require.Equal(t, "item/fileChange/outputDelta", got.Method)
	require.Equal(t, "diff --git a.txt b.txt\n", string(got.Content))
	require.Equal(t, 0, sink.MessageCount())
}

func TestHandleCodexOutput_PlanDeltaThenCompleted(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	// Send a plan delta first. (It also broadcasts a thinking_tokens estimate on
	// the same channel, so assert on the streaming_type key specifically rather
	// than on raw broadcast counts / the last payload.)
	delta := `{"method":"item/plan/delta","params":{"delta":"# Plan\n"}}`
	handleCodexOutput(agent, parseLine([]byte(delta)))

	streamingType, ok := lastSessionInfoValue(sink, "streaming_type")
	require.True(t, ok)
	assert.Equal(t, "plan", streamingType)

	// Send item/completed with plan type.
	completed := `{"method":"item/completed","params":{"item":{"type":"plan","id":"plan1","text":"# Plan\nStep 1"}}}`
	handleCodexOutput(agent, parseLine([]byte(completed)))

	// Session info should now have streaming_type "" to clear the plan streaming.
	streamingType, ok = lastSessionInfoValue(sink, "streaming_type")
	require.True(t, ok)
	assert.Equal(t, "", streamingType)

	// Plan message should be persisted.
	require.Equal(t, 1, sink.MessageCount())

	// Verify streamingPlan flag was cleared (next delta should re-broadcast).
	delta2 := `{"method":"item/plan/delta","params":{"delta":"New plan\n"}}`
	handleCodexOutput(agent, parseLine([]byte(delta2)))

	streamingType, ok = lastSessionInfoValue(sink, "streaming_type")
	require.True(t, ok)
	assert.Equal(t, "plan", streamingType)

	// The exact streaming_type lifecycle: "plan" on the first delta, "" exactly
	// once when the plan item completes, then "plan" again because item/completed
	// cleared the streamingPlan flag. (Guards against a regression where
	// item/completed double-broadcasts "" or the second delta fails to re-arm.)
	assert.Equal(t, []interface{}{"plan", "", "plan"}, sessionInfoValues(sink, "streaming_type"))
}

func TestHandleCodexOutput_CommandExecutionCompletedBroadcastsStreamEnd(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	completed := `{"method":"item/completed","params":{"threadId":"t1","item":{"type":"commandExecution","id":"cmd-1","status":"completed","aggregatedOutput":"done"}}}`
	handleCodexOutput(agent, parseLine([]byte(completed)))

	require.Equal(t, 1, sink.MessageCount())
	require.Equal(t, 1, sink.StreamEndCount())
	require.Equal(t, "cmd-1", sink.LastStreamEnd())
}

func TestHandleCodexOutput_FileChangeCompletedBroadcastsStreamEnd(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	completed := `{"method":"item/completed","params":{"threadId":"t1","item":{"type":"fileChange","id":"fc-1","status":"completed","changes":[{"path":"a.txt","kind":"update","diff":"@@ -1 +1 @@\n-old\n+new"}]}}}`
	handleCodexOutput(agent, parseLine([]byte(completed)))

	require.Equal(t, 1, sink.MessageCount())
	require.Equal(t, 1, sink.StreamEndCount())
	require.Equal(t, "fc-1", sink.LastStreamEnd())
}

func TestHandleCodexOutput_ReasoningCompletedBroadcastsStreamEnd(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	completed := `{"method":"item/completed","params":{"threadId":"t1","item":{"type":"reasoning","id":"reason-1","summary":["done"]}}}`
	handleCodexOutput(agent, parseLine([]byte(completed)))

	require.Equal(t, 1, sink.MessageCount())
	require.Equal(t, 1, sink.StreamEndCount())
	require.Equal(t, "reason-1", sink.LastStreamEnd())
}

func TestHandleCodexOutput_ApprovalWithoutID(t *testing.T) {
	sink := &recordingControlSink{}
	agent := newCodexAgentWithSink(sink)

	// Missing "id" field — should be ignored (logged as warning).
	input := `{"method":"item/tool/requestUserInput","params":{"questions":[]}}`

	handleCodexOutput(agent, parseLine([]byte(input)))

	assert.Equal(t, 0, sink.PersistedControlCount())
	assert.Equal(t, 0, sink.BroadcastControlCount())
}

func TestHandleCodexOutput_TokenUsageUpdatedBroadcastsContextUsage(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"thread/tokenUsage/updated","params":{"threadId":"thread-1","turnId":"turn-1","tokenUsage":{"total":{"totalTokens":200,"inputTokens":100,"cachedInputTokens":25,"outputTokens":50,"reasoningOutputTokens":9},"last":{"totalTokens":23,"inputTokens":10,"cachedInputTokens":5,"outputTokens":7,"reasoningOutputTokens":1},"modelContextWindow":4096}}}`
	agent.threadID = "thread-1"
	handleCodexOutput(agent, parseLine([]byte(input)))

	require.Equal(t, 1, sink.NotificationCount())
	last := sink.LastNotification()
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, last.Source,
		"Codex-emitted thread/tokenUsage/updated must persist as AGENT")
	assert.JSONEq(t, input, string(last.Content),
		"raw envelope must be preserved so reconnect/catch-up sees full token usage detail")
	require.Equal(t, 1, sink.SessionInfoCount())

	info := sink.LastSessionInfo()
	usage, ok := info["context_usage"].(map[string]interface{})
	require.True(t, ok, "expected context_usage map, got %#v", info["context_usage"])
	require.Equal(t, int64(5), usage["input_tokens"])
	require.Equal(t, int64(0), usage["cache_creation_input_tokens"])
	require.Equal(t, int64(5), usage["cache_read_input_tokens"])
	require.Equal(t, int64(4096), usage["context_window"])
}

func TestHandleCodexOutput_TokenUsageUpdatedFallsBackToModelContextWindow(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)
	agent.model = "gpt-5.4"
	agent.availableModels = codexDefaultModels
	agent.threadID = "thread-1"

	input := `{"method":"thread/tokenUsage/updated","params":{"threadId":"thread-1","turnId":"turn-1","tokenUsage":{"total":{"totalTokens":200,"inputTokens":100,"cachedInputTokens":25,"outputTokens":50,"reasoningOutputTokens":9},"last":{"totalTokens":23,"inputTokens":10,"cachedInputTokens":5,"outputTokens":7,"reasoningOutputTokens":1},"modelContextWindow":null}}}`
	handleCodexOutput(agent, parseLine([]byte(input)))

	info := sink.LastSessionInfo()
	usage, ok := info["context_usage"].(map[string]interface{})
	require.True(t, ok, "expected context_usage map, got %#v", info["context_usage"])
	require.Equal(t, int64(1_050_000), usage["context_window"])
}

func TestHandleCodexOutput_TokenUsageUpdatedIgnoresSubagentThreads(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)
	agent.threadID = "main-thread"

	input := `{"method":"thread/tokenUsage/updated","params":{"threadId":"child-thread","turnId":"turn-1","tokenUsage":{"total":{"totalTokens":200,"inputTokens":100,"cachedInputTokens":25,"outputTokens":50,"reasoningOutputTokens":9},"last":{"totalTokens":23,"inputTokens":10,"cachedInputTokens":5,"outputTokens":7,"reasoningOutputTokens":1},"modelContextWindow":4096}}}`
	handleCodexOutput(agent, parseLine([]byte(input)))

	require.Equal(t, 0, sink.NotificationCount())
	require.Equal(t, 0, sink.SessionInfoCount())
}

func TestHandleCodexOutput_TurnCompletedIgnoresSubagentThreads(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)
	agent.threadID = "main-thread"

	input := `{"method":"turn/completed","params":{"threadId":"child-thread","turn":{"id":"turn-1","status":"completed","items":[],"error":null}}}`
	handleCodexOutput(agent, parseLine([]byte(input)))

	require.Equal(t, 0, sink.MessageCount())
	require.Equal(t, 0, sink.ResetSpanCount())
	require.Equal(t, 0, sink.SessionInfoCount())
}

func TestHandleCodexOutput_TurnCompletedPlanModePersistsRealPlanAndPrompts(t *testing.T) {
	sink := &recordingControlSink{}
	agent := newCodexAgentWithSink(sink)
	agent.threadID = "main-thread"
	agent.collaborationMode = CodexCollaborationPlan

	planStarted := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"plan","id":"plan-1"}}}`
	planCompleted := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"plan","id":"plan-1","text":"# Design Doc: Rendering fixes\n\n- first\n"}}}`
	turnCompleted := `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed","items":[],"error":null}}}`

	handleCodexOutput(agent, parseLine([]byte(planStarted)))
	handleCodexOutput(agent, parseLine([]byte(planCompleted)))
	handleCodexOutput(agent, parseLine([]byte(turnCompleted)))

	require.Equal(t, 1, sink.PlanUpdateCount())
	plan := sink.LastPlanUpdate()
	decoded, err := msgcodec.Decompress(plan.Content, plan.Compression)
	require.NoError(t, err)
	require.Equal(t, "# Design Doc: Rendering fixes\n\n- first\n", string(decoded))
	require.Equal(t, "Rendering fixes", plan.Title)
	require.Equal(t, 1, sink.PersistedControlCount())
	require.Equal(t, 1, sink.BroadcastControlCount())
}

func TestHandleCodexOutput_TurnCompletedPlanModeIgnoresAssistantTextWithoutPlanItem(t *testing.T) {
	sink := &recordingControlSink{}
	agent := newCodexAgentWithSink(sink)
	agent.threadID = "main-thread"
	agent.collaborationMode = CodexCollaborationPlan

	assistantCompleted := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"agentMessage","id":"msg-1","text":"Revised plan:\n- not a real plan item"}}}`
	turnCompleted := `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed","items":[],"error":null}}}`

	handleCodexOutput(agent, parseLine([]byte(assistantCompleted)))
	handleCodexOutput(agent, parseLine([]byte(turnCompleted)))

	require.Equal(t, 0, sink.PlanUpdateCount())
	require.Equal(t, 0, sink.PersistedControlCount())
	require.Equal(t, 0, sink.BroadcastControlCount())
}

func TestHandleCodexOutput_TurnCompletedPlanModeWithoutRealPlanDoesNotPrompt(t *testing.T) {
	sink := &recordingControlSink{}
	agent := newCodexAgentWithSink(sink)
	agent.threadID = "main-thread"
	agent.collaborationMode = CodexCollaborationPlan

	turnCompleted := `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed","items":[],"error":null}}}`
	handleCodexOutput(agent, parseLine([]byte(turnCompleted)))

	require.Equal(t, 0, sink.PlanUpdateCount())
	require.Equal(t, 0, sink.PersistedControlCount())
	require.Equal(t, 0, sink.BroadcastControlCount())
}

func TestHandleCodexOutput_TurnCompletedPlanModeWithEmptyPlanTextDoesNotPersist(t *testing.T) {
	sink := &recordingControlSink{}
	agent := newCodexAgentWithSink(sink)
	agent.threadID = "main-thread"
	agent.collaborationMode = CodexCollaborationPlan

	planCompleted := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"plan","id":"plan-1"}}}`
	turnCompleted := `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed","items":[],"error":null}}}`

	handleCodexOutput(agent, parseLine([]byte(planCompleted)))
	handleCodexOutput(agent, parseLine([]byte(turnCompleted)))

	require.Equal(t, 0, sink.PlanUpdateCount())
	require.Equal(t, 0, sink.PersistedControlCount())
	require.Equal(t, 0, sink.BroadcastControlCount())
}

// lastSessionInfoValue returns the value of the most recent BroadcastSessionInfo
// payload that carried key, and whether any did. Used to assert on one key while
// ignoring unrelated broadcasts (e.g. streaming_type vs thinking_tokens) that may
// interleave on the same channel.
func lastSessionInfoValue(sink *testSink, key string) (interface{}, bool) {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	for i := len(sink.sessionInfos) - 1; i >= 0; i-- {
		if v, ok := sink.sessionInfos[i][key]; ok {
			return v, true
		}
	}
	return nil, false
}

// countSessionInfoWithKey counts how many BroadcastSessionInfo payloads carried
// key. Used to assert a key was broadcast exactly N times while other keys
// (e.g. thinking_tokens) interleave on the same channel.
func countSessionInfoWithKey(sink *testSink, key string) int {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	n := 0
	for _, info := range sink.sessionInfos {
		if _, ok := info[key]; ok {
			n++
		}
	}
	return n
}

// sessionInfoValues returns, in broadcast order, every value carried under key.
// Lets a test assert the exact sequence (and therefore count) of a key's
// broadcasts while other keys interleave on the same channel.
func sessionInfoValues(sink *testSink, key string) []interface{} {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	var out []interface{}
	for _, info := range sink.sessionInfos {
		if v, ok := info[key]; ok {
			out = append(out, v)
		}
	}
	return out
}

// lastThinkingTokens returns the most recently broadcast thinking_tokens value,
// or -1 when none has been broadcast yet. -1 (not 0) is the sentinel so a test
// can tell "never broadcast" apart from a real 0.
func lastThinkingTokens(sink *testSink) int64 {
	if v, ok := lastSessionInfoValue(sink, SessionInfoKeyThinkingTokens); ok {
		return v.(int64)
	}
	return -1
}

func TestHandleCodexOutput_AgentMessageDeltaAccumulatesThinkingTokens(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	// 8-char delta -> 8/4 = 2 tokens; a second 8-char delta accumulates to
	// 16/4 = 4. The estimate climbs cumulatively across deltas of the same phase.
	handleCodexOutput(agent, parseLine([]byte(`{"method":"item/agentMessage/delta","params":{"delta":"abcdefgh"}}`)))
	assert.Equal(t, int64(2), lastThinkingTokens(sink))
	handleCodexOutput(agent, parseLine([]byte(`{"method":"item/agentMessage/delta","params":{"delta":"ijklmnop"}}`)))
	assert.Equal(t, int64(4), lastThinkingTokens(sink))

	// A live estimate is broadcast, never persisted to the timeline.
	assert.Equal(t, 0, sink.MessageCount(), "thinking_tokens deltas must not persist")
	assert.Equal(t, 0, sink.NotificationCount())
}

func TestHandleCodexOutput_ReasoningAndPlanDeltasAccumulateThinkingTokens(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
	}{
		{"reasoning text", `{"method":"item/reasoning/textDelta","params":{"itemId":"r1","delta":"%s"}}`},
		{"reasoning summary", `{"method":"item/reasoning/summaryTextDelta","params":{"itemId":"r1","delta":"%s"}}`},
		{"plan", `{"method":"item/plan/delta","params":{"delta":"%s"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &testSink{}
			agent := newCodexAgentWithSink(sink)
			agent.threadID = "main-thread"

			handleCodexOutput(agent, parseLine([]byte(fmt.Sprintf(tc.input, "abcdefgh"))))
			assert.Equal(t, int64(2), lastThinkingTokens(sink))
			handleCodexOutput(agent, parseLine([]byte(fmt.Sprintf(tc.input, "ijklmnop"))))
			assert.Equal(t, int64(4), lastThinkingTokens(sink))
		})
	}
}

func TestHandleCodexOutput_ReasoningSummaryAndRawCountOncePerItem(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)
	agent.threadID = "main-thread"

	// Codex can stream BOTH a summary and the raw reasoning for one reasoning item
	// (same itemId, same generation surfaced twice). The summary arrives first and
	// locks item r1 onto "summary" (8 chars -> 2 tokens).
	handleCodexOutput(agent, parseLine([]byte(`{"method":"item/reasoning/summaryTextDelta","params":{"itemId":"r1","delta":"abcdefgh","threadId":"main-thread"}}`)))
	require.Equal(t, int64(2), lastThinkingTokens(sink))

	// Raw reasoning for the SAME item must NOT be counted -- it would double the
	// estimate for the same underlying generation.
	handleCodexOutput(agent, parseLine([]byte(`{"method":"item/reasoning/textDelta","params":{"itemId":"r1","delta":"ZZZZZZZZZZZZZZZZ","threadId":"main-thread"}}`)))
	assert.Equal(t, int64(2), lastThinkingTokens(sink), "raw reasoning for the locked item is not double-counted")

	// More summary deltas for the locked item still climb (16 chars -> 4 tokens).
	handleCodexOutput(agent, parseLine([]byte(`{"method":"item/reasoning/summaryTextDelta","params":{"itemId":"r1","delta":"ijklmnop","threadId":"main-thread"}}`)))
	assert.Equal(t, int64(4), lastThinkingTokens(sink))

	// A different reasoning item locks independently: raw arrives first for r2.
	handleCodexOutput(agent, parseLine([]byte(`{"method":"item/reasoning/textDelta","params":{"itemId":"r2","delta":"qrstuvwx","threadId":"main-thread"}}`)))
	assert.Equal(t, int64(6), lastThinkingTokens(sink), "a separate item counts its own first-seen kind (24 chars -> 6)")
}

func TestHandleCodexOutput_ReasoningCountsWhicheverStreamArrivesFirst(t *testing.T) {
	// A model that streams only one reasoning kind must still move the counter, so
	// the estimator locks onto whichever arrives first rather than preferring one.
	for _, tc := range []struct {
		name   string
		method string
	}{
		{"raw only", "item/reasoning/textDelta"},
		{"summary only", "item/reasoning/summaryTextDelta"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &testSink{}
			agent := newCodexAgentWithSink(sink)
			agent.threadID = "main-thread"

			handleCodexOutput(agent, parseLine([]byte(fmt.Sprintf(
				`{"method":%q,"params":{"itemId":"r1","delta":"abcdefgh","threadId":"main-thread"}}`, tc.method))))
			assert.Equal(t, int64(2), lastThinkingTokens(sink), "a single reasoning stream still counts")
		})
	}
}

func TestHandleCodexOutput_ReasoningItemCompletedReleasesStreamLock(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)
	agent.threadID = "main-thread"

	// Item r1 locks onto "summary".
	handleCodexOutput(agent, parseLine([]byte(`{"method":"item/reasoning/summaryTextDelta","params":{"itemId":"r1","delta":"abcdefgh","threadId":"main-thread"}}`)))
	require.Equal(t, int64(2), lastThinkingTokens(sink))

	// Completing the reasoning item resets the estimate (a main-scope AGENT commit)
	// AND releases r1's stream-kind lock so the map stays bounded.
	handleCodexOutput(agent, parseLine([]byte(`{"method":"item/completed","params":{"threadId":"main-thread","item":{"type":"reasoning","id":"r1","summary":[{"text":"done"}]}}}`)))
	agent.mu.Lock()
	_, stillLocked := agent.reasoningStreamKind["r1"]
	agent.mu.Unlock()
	assert.False(t, stillLocked, "the completed item's stream-kind lock is released")
}

func TestHandleCodexOutput_ChildThreadDeltasDoNotCountThinkingTokens(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink) // threadID = "main-thread"

	// A main-thread delta counts (explicit main threadId).
	handleCodexOutput(agent, parseLine([]byte(`{"method":"item/agentMessage/delta","params":{"delta":"abcdefgh","threadId":"main-thread"}}`)))
	require.Equal(t, int64(2), lastThinkingTokens(sink))

	// A collab subagent (child thread) delta is still broadcast as a stream chunk
	// but must NOT inflate the primary agent's estimate.
	handleCodexOutput(agent, parseLine([]byte(`{"method":"item/reasoning/textDelta","params":{"itemId":"r1","delta":"ignoredchildtext","threadId":"child-1"}}`)))
	assert.Equal(t, int64(2), lastThinkingTokens(sink), "child-thread text must not count")

	// A subsequent main-thread delta (empty threadId == main) resumes from the
	// main total, unaffected by the child text in between.
	handleCodexOutput(agent, parseLine([]byte(`{"method":"item/agentMessage/delta","params":{"delta":"ijklmnop"}}`)))
	assert.Equal(t, int64(4), lastThinkingTokens(sink))
}

func TestHandleCodexOutput_CommandAndFileOutputDeltasDoNotCountThinkingTokens(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	// Tool output (shell stdout, file diffs) is not model generation, so it must
	// NOT inflate the thinking-token estimate.
	handleCodexOutput(agent, parseLine([]byte(`{"method":"item/commandExecution/outputDelta","params":{"itemId":"c1","delta":"lots of stdout here"}}`)))
	handleCodexOutput(agent, parseLine([]byte(`{"method":"item/fileChange/outputDelta","params":{"itemId":"f1","delta":"@@ -1 +1 @@ big diff"}}`)))

	assert.Equal(t, int64(-1), lastThinkingTokens(sink), "tool output must not broadcast thinking_tokens")
}

func TestHandleCodexOutput_ItemCompletedResetsThinkingTokens(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)
	agent.threadID = "main-thread"

	handleCodexOutput(agent, parseLine([]byte(`{"method":"item/agentMessage/delta","params":{"delta":"abcdefghijklmnop"}}`)))
	require.Equal(t, int64(4), lastThinkingTokens(sink))

	// Committing the message is a phase boundary the frontend clears on; the
	// backend estimate must reset so the next phase restarts from zero.
	handleCodexOutput(agent, parseLine([]byte(`{"method":"item/completed","params":{"threadId":"main-thread","item":{"type":"agentMessage","id":"m1","text":"hi"}}}`)))

	handleCodexOutput(agent, parseLine([]byte(`{"method":"item/agentMessage/delta","params":{"delta":"abcdefgh"}}`)))
	assert.Equal(t, int64(2), lastThinkingTokens(sink), "next phase restarts at 8/4, not the cumulative total")
}

func TestHandleCodexOutput_ItemStartedAndApprovalResetThinkingTokens(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reset string
	}{
		// A started tool item commits an AGENT message the frontend clears on.
		{"item started", `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"commandExecution","id":"cmd-1","status":"inProgress","command":"ls","cwd":"/tmp","processId":"123","commandActions":[]}}}`},
		// An approval request is a live control request the frontend clears on.
		{"approval request", `{"jsonrpc":"2.0","id":7,"method":"item/commandExecution/requestApproval","params":{"command":"ls","reason":"x"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &recordingControlSink{}
			agent := newCodexAgentWithSink(sink)
			agent.threadID = "main-thread"

			handleCodexOutput(agent, parseLine([]byte(`{"method":"item/agentMessage/delta","params":{"delta":"abcdefghijklmnop"}}`)))
			require.Equal(t, int64(4), lastThinkingTokens(&sink.testSink))

			handleCodexOutput(agent, parseLine([]byte(tc.reset)))

			handleCodexOutput(agent, parseLine([]byte(`{"method":"item/agentMessage/delta","params":{"delta":"abcdefgh"}}`)))
			assert.Equal(t, int64(2), lastThinkingTokens(&sink.testSink), "the boundary restarts the estimate")
		})
	}
}

func TestHandleCodexOutput_ChildThreadTurnStartedDoesNotResetThinkingTokens(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink) // threadID = "main-thread"

	// Accumulate a main-thread estimate (16 chars -> 4 tokens).
	handleCodexOutput(agent, parseLine([]byte(`{"method":"item/agentMessage/delta","params":{"delta":"abcdefghijklmnop","threadId":"main-thread"}}`)))
	require.Equal(t, int64(4), lastThinkingTokens(sink))

	// A collab subagent's turn/started carries its own child threadId. It must NOT
	// reset the primary agent's estimate -- the frontend has no counter clear for
	// turn/started, so an ungated reset would spin the odometer backward mid-phase.
	handleCodexOutput(agent, parseLine([]byte(`{"method":"turn/started","params":{"threadId":"child-1","turn":{"id":"child-turn"}}}`)))

	// The next main-thread delta keeps climbing from the cumulative total (24/4=6),
	// proving the child-thread turn/started did not reset the estimate.
	handleCodexOutput(agent, parseLine([]byte(`{"method":"item/agentMessage/delta","params":{"delta":"qrstuvwx","threadId":"main-thread"}}`)))
	assert.Equal(t, int64(6), lastThinkingTokens(sink), "a child-thread turn/started must not reset the primary estimate")
}

func TestHandleCodexOutput_TurnStartedClearsReasoningStreamLocksOnMainThreadOnly(t *testing.T) {
	for _, tc := range []struct {
		name        string
		turnStarted string
		wantCleared bool
	}{
		{"main thread clears", `{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-2"}}}`, true},
		{"child thread keeps", `{"method":"turn/started","params":{"threadId":"child-1","turn":{"id":"child-turn"}}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &testSink{}
			agent := newCodexAgentWithSink(sink)
			agent.threadID = "main-thread"

			// Lock reasoning item r1 onto its first-seen sub-stream kind.
			handleCodexOutput(agent, parseLine([]byte(`{"method":"item/reasoning/summaryTextDelta","params":{"itemId":"r1","delta":"abcdefgh","threadId":"main-thread"}}`)))
			agent.mu.Lock()
			_, locked := agent.reasoningStreamKind["r1"]
			agent.mu.Unlock()
			require.True(t, locked, "the reasoning item is locked before the turn boundary")

			// A new turn on the main thread drops stale per-item locks so itemIds can't
			// leak across turns (e.g. a reasoning item left open by an abort); a
			// child-thread turn/started belongs to a collab subagent and must leave the
			// primary agent's locks intact, mirroring the main-thread gate on the reset.
			handleCodexOutput(agent, parseLine([]byte(tc.turnStarted)))

			agent.mu.Lock()
			_, stillLocked := agent.reasoningStreamKind["r1"]
			agent.mu.Unlock()
			assert.Equal(t, !tc.wantCleared, stillLocked)
		})
	}
}

func TestHandleCodexOutput_ReasoningItemCompletedResetsThinkingTokens(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)
	agent.threadID = "main-thread"

	// Reasoning streams and climbs (16 chars -> 4 tokens).
	handleCodexOutput(agent, parseLine([]byte(`{"method":"item/reasoning/textDelta","params":{"itemId":"r1","delta":"abcdefghijklmnop","threadId":"main-thread"}}`)))
	require.Equal(t, int64(4), lastThinkingTokens(sink))

	// Codex has no explicit reasoning->answer hand-off (unlike ACP); it relies on
	// the reasoning item/completed persisting an AGENT message the frontend clears
	// on, which the decorator mirrors with a reset. Without it, the reasoning chars
	// would stack onto the following answer's count.
	handleCodexOutput(agent, parseLine([]byte(`{"method":"item/completed","params":{"threadId":"main-thread","item":{"type":"reasoning","id":"r1","summary":[{"text":"done"}]}}}`)))

	// The answer phase restarts from zero (8/4 = 2), not the reasoning total.
	handleCodexOutput(agent, parseLine([]byte(`{"method":"item/agentMessage/delta","params":{"delta":"abcdefgh","threadId":"main-thread"}}`)))
	assert.Equal(t, int64(2), lastThinkingTokens(sink), "reasoning item/completed resets so the answer restarts from zero")
}

func TestHandleCodexOutput_TurnBoundariesResetThinkingTokens(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reset string
	}{
		{"turn started", `{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-2"}}}`},
		{"turn completed", `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed","items":[],"error":null}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &testSink{}
			agent := newCodexAgentWithSink(sink)
			agent.threadID = "main-thread"

			handleCodexOutput(agent, parseLine([]byte(`{"method":"item/agentMessage/delta","params":{"delta":"abcdefghijklmnop"}}`)))
			require.Equal(t, int64(4), lastThinkingTokens(sink))

			handleCodexOutput(agent, parseLine([]byte(tc.reset)))

			handleCodexOutput(agent, parseLine([]byte(`{"method":"item/agentMessage/delta","params":{"delta":"abcdefgh"}}`)))
			assert.Equal(t, int64(2), lastThinkingTokens(sink), "the new turn restarts the estimate")
		})
	}
}
