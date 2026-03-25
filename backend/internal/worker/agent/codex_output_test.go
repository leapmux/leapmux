package agent

import (
	"encoding/json"
	"sync"
	"testing"
)

// controlRequestRecord captures a single PersistControlRequest / BroadcastControlRequest call.
type controlRequestRecord struct {
	RequestID string
	Payload   []byte
}

// controlTestSink extends testSink to also record control request calls.
type controlTestSink struct {
	testSink

	crMu              sync.Mutex
	persistedControls []controlRequestRecord
	broadcastControls []controlRequestRecord
}

func (s *controlTestSink) PersistControlRequest(requestID string, payload []byte) {
	s.crMu.Lock()
	defer s.crMu.Unlock()
	s.persistedControls = append(s.persistedControls, controlRequestRecord{
		RequestID: requestID,
		Payload:   append([]byte(nil), payload...),
	})
}

func (s *controlTestSink) BroadcastControlRequest(requestID string, payload []byte) {
	s.crMu.Lock()
	defer s.crMu.Unlock()
	s.broadcastControls = append(s.broadcastControls, controlRequestRecord{
		RequestID: requestID,
		Payload:   append([]byte(nil), payload...),
	})
}

func (s *controlTestSink) PersistedControlCount() int {
	s.crMu.Lock()
	defer s.crMu.Unlock()
	return len(s.persistedControls)
}

func (s *controlTestSink) BroadcastControlCount() int {
	s.crMu.Lock()
	defer s.crMu.Unlock()
	return len(s.broadcastControls)
}

func (s *controlTestSink) LastPersistedControl() controlRequestRecord {
	s.crMu.Lock()
	defer s.crMu.Unlock()
	return s.persistedControls[len(s.persistedControls)-1]
}

func (s *controlTestSink) LastBroadcastControl() controlRequestRecord {
	s.crMu.Lock()
	defer s.crMu.Unlock()
	return s.broadcastControls[len(s.broadcastControls)-1]
}

func newCodexAgentWithSink(sink OutputSink) *CodexAgent {
	return &CodexAgent{
		processBase: processBase{
			agentID: "test-agent",
		},
		sink:     sink,
		threadID: "main-thread",
	}
}

func TestHandleCodexOutput_RequestUserInput(t *testing.T) {
	sink := &controlTestSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","id":42,"method":"item/tool/requestUserInput","params":{"threadId":"t1","turnId":"turn1","itemId":"item1","questions":[{"id":"q1","header":"Header","question":"Which option?","options":[{"label":"A"}]}]}}`

	handleCodexOutput(agent, []byte(input))

	if sink.PersistedControlCount() != 1 {
		t.Fatalf("expected 1 persisted control request, got %d", sink.PersistedControlCount())
	}
	if sink.BroadcastControlCount() != 1 {
		t.Fatalf("expected 1 broadcast control request, got %d", sink.BroadcastControlCount())
	}

	rec := sink.LastPersistedControl()
	if rec.RequestID != "42" {
		t.Errorf("expected requestID '42', got %q", rec.RequestID)
	}

	// Verify payload is the original content.
	var parsed struct {
		Method string `json:"method"`
		ID     int    `json:"id"`
	}
	if err := json.Unmarshal(rec.Payload, &parsed); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if parsed.Method != "item/tool/requestUserInput" {
		t.Errorf("expected method 'item/tool/requestUserInput', got %q", parsed.Method)
	}
	if parsed.ID != 42 {
		t.Errorf("expected id 42, got %d", parsed.ID)
	}

	// Should NOT be persisted as a regular message.
	if sink.MessageCount() != 0 {
		t.Errorf("expected 0 messages, got %d", sink.MessageCount())
	}
}

func TestHandleCodexOutput_CommandExecutionApproval(t *testing.T) {
	sink := &controlTestSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","id":7,"method":"item/commandExecution/requestApproval","params":{"command":"rm -rf /","reason":"cleanup"}}`

	handleCodexOutput(agent, []byte(input))

	if sink.PersistedControlCount() != 1 {
		t.Fatalf("expected 1 persisted control request, got %d", sink.PersistedControlCount())
	}

	rec := sink.LastPersistedControl()
	if rec.RequestID != "7" {
		t.Errorf("expected requestID '7', got %q", rec.RequestID)
	}
}

func TestHandleCodexOutput_FileChangeApproval(t *testing.T) {
	sink := &controlTestSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","id":8,"method":"item/fileChange/requestApproval","params":{"reason":"editing file"}}`

	handleCodexOutput(agent, []byte(input))

	if sink.PersistedControlCount() != 1 {
		t.Fatalf("expected 1 persisted control request, got %d", sink.PersistedControlCount())
	}

	rec := sink.LastPersistedControl()
	if rec.RequestID != "8" {
		t.Errorf("expected requestID '8', got %q", rec.RequestID)
	}
}

func TestHandleCodexOutput_PermissionsApproval(t *testing.T) {
	sink := &controlTestSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"jsonrpc":"2.0","id":9,"method":"item/permissions/requestApproval","params":{"reason":"needs access"}}`

	handleCodexOutput(agent, []byte(input))

	if sink.PersistedControlCount() != 1 {
		t.Fatalf("expected 1 persisted control request, got %d", sink.PersistedControlCount())
	}

	rec := sink.LastPersistedControl()
	if rec.RequestID != "9" {
		t.Errorf("expected requestID '9', got %q", rec.RequestID)
	}
}

func TestHandleCodexOutput_PlanDelta(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"item/plan/delta","params":{"delta":"# Plan\n"}}`
	handleCodexOutput(agent, []byte(input))

	if sink.StreamChunkCount() != 1 {
		t.Fatalf("expected 1 stream chunk, got %d", sink.StreamChunkCount())
	}
	if got := sink.LastStreamChunk(); got.Method != "item/plan/delta" || got.SpanID != "" || string(got.Content) != "# Plan\n" {
		t.Fatalf("unexpected stream chunk: %+v", got)
	}

	// Verify the session info was broadcast with streamingType "plan".
	if sink.SessionInfoCount() != 1 {
		t.Fatalf("expected 1 session info broadcast, got %d", sink.SessionInfoCount())
	}
	info := sink.LastSessionInfo()
	if info["streamingType"] != "plan" {
		t.Errorf("expected streamingType 'plan', got %v", info["streamingType"])
	}

	// Second delta should NOT broadcast session info again.
	input2 := `{"method":"item/plan/delta","params":{"delta":"Step 1\n"}}`
	handleCodexOutput(agent, []byte(input2))

	if sink.StreamChunkCount() != 2 {
		t.Fatalf("expected 2 stream chunks, got %d", sink.StreamChunkCount())
	}
	if sink.SessionInfoCount() != 1 {
		t.Errorf("expected still 1 session info broadcast, got %d", sink.SessionInfoCount())
	}

	// Should NOT be persisted as a regular message.
	if sink.MessageCount() != 0 {
		t.Errorf("expected 0 messages, got %d", sink.MessageCount())
	}
}

func TestHandleCodexOutput_ContextCompactionStartPersistsCompactingNotification(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"item/started","params":{"item":{"type":"contextCompaction","id":"compact-1"},"threadId":"t1","turnId":"turn1"}}`
	handleCodexOutput(agent, []byte(input))

	if sink.NotificationCount() != 1 {
		t.Fatalf("expected 1 notification, got %d", sink.NotificationCount())
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(sink.LastNotification().Content, &parsed); err != nil {
		t.Fatalf("failed to unmarshal notification: %v", err)
	}
	if parsed["type"] != "compacting" {
		t.Fatalf("expected compacting notification, got %+v", parsed)
	}
	if sink.MessageCount() != 0 {
		t.Fatalf("expected 0 assistant messages, got %d", sink.MessageCount())
	}
}

func TestHandleCodexOutput_SpawnAgentStartedOpensSubagentSpan(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{}}}}`
	handleCodexOutput(agent, []byte(input))

	if got := sink.OpenSpans(); len(got) != 2 || got[0].SpanID != "call-1" || got[0].ParentSpanID != "" || got[1].SpanID != "child-1" || got[1].ParentSpanID != "call-1" {
		t.Fatalf("expected child span to open, got %v", got)
	}
	if sink.ClosedSpanCount() != 0 {
		t.Fatalf("expected no spans to close, got %v", sink.ClosedSpans())
	}
}

func TestHandleCodexOutput_WaitMessagesStayInsideSpawnAgentSpan(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	spawnStarted := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{}}}}`
	waitStarted := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-2","tool":"wait","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":null,"model":null,"reasoningEffort":null,"agentsStates":{}}}}`
	waitCompleted := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-2","tool":"wait","status":"completed","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":null,"model":null,"reasoningEffort":null,"agentsStates":{"child-1":{"status":"completed","message":"done"}}}}}`

	handleCodexOutput(agent, []byte(spawnStarted))
	handleCodexOutput(agent, []byte(waitStarted))
	handleCodexOutput(agent, []byte(waitCompleted))

	messages := sink.Messages()
	if len(messages) != 3 {
		t.Fatalf("expected 3 persisted messages, got %d", len(messages))
	}
	if messages[1].ParentSpanID != "call-1" {
		t.Fatalf("expected wait started to be nested under spawnAgent span, got parent %q", messages[1].ParentSpanID)
	}
	if messages[2].ParentSpanID != "call-1" {
		t.Fatalf("expected wait completed to be nested under spawnAgent span, got parent %q", messages[2].ParentSpanID)
	}
	if got := sink.ClosedSpans(); len(got) != 2 || got[0] != "child-1" || got[1] != "call-1" {
		t.Fatalf("expected wait completion to close child and spawnAgent spans, got %v", got)
	}
}

func TestHandleCodexOutput_SpawnAgentCompletedDoesNotCloseSubagentSpan(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"completed","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{"child-1":{"status":"running","message":null}}}}}`
	handleCodexOutput(agent, []byte(input))

	if sink.ClosedSpanCount() != 0 {
		t.Fatalf("expected spawn completion to keep span open, got %v", sink.ClosedSpans())
	}
}

func TestHandleCodexOutput_WaitCompletedClosesTerminalSubagentSpan(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-2","tool":"wait","status":"completed","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":null,"model":null,"reasoningEffort":null,"agentsStates":{"child-1":{"status":"completed","message":"done"}}}}}`
	handleCodexOutput(agent, []byte(input))

	if got := sink.ClosedSpans(); len(got) != 1 || got[0] != "child-1" {
		t.Fatalf("expected wait completion to close child span, got %v", got)
	}
}

func TestHandleCodexOutput_WaitCompletedDoesNotCloseNonTerminalOrMissingStatuses(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-2","tool":"wait","status":"completed","senderThreadId":"main-thread","receiverThreadIds":["child-1","child-2"],"prompt":null,"model":null,"reasoningEffort":null,"agentsStates":{"child-1":{"status":"running","message":null}}}}}`
	handleCodexOutput(agent, []byte(input))

	if sink.ClosedSpanCount() != 0 {
		t.Fatalf("expected non-terminal or missing wait statuses to keep spans open, got %v", sink.ClosedSpans())
	}
}

func TestHandleCodexOutput_CloseAgentCompletedClosesSubagentSpan(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-3","tool":"closeAgent","status":"completed","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":null,"model":null,"reasoningEffort":null,"agentsStates":{"child-1":{"status":"shutdown","message":null}}}}}`
	handleCodexOutput(agent, []byte(input))

	if got := sink.ClosedSpans(); len(got) != 1 || got[0] != "child-1" {
		t.Fatalf("expected closeAgent completion to close child span, got %v", got)
	}
}

func TestHandleCodexOutput_WaitCompletedClosesOnlyTerminalReceivers(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-4","tool":"wait","status":"completed","senderThreadId":"main-thread","receiverThreadIds":["child-1","child-2","child-3"],"prompt":null,"model":null,"reasoningEffort":null,"agentsStates":{"child-1":{"status":"completed","message":"done"},"child-2":{"status":"running","message":null},"child-3":{"status":"notFound","message":null}}}}}`
	handleCodexOutput(agent, []byte(input))

	if got := sink.ClosedSpans(); len(got) != 2 || got[0] != "child-1" || got[1] != "child-3" {
		t.Fatalf("expected only terminal receivers to close, got %v", got)
	}
}

func TestHandleCodexOutput_ThreadCompactedPersistsBoundaryNotification(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"thread/compacted","params":{"threadId":"t1","turnId":"turn1"}}`
	handleCodexOutput(agent, []byte(input))

	if sink.NotificationCount() != 1 {
		t.Fatalf("expected 1 notification, got %d", sink.NotificationCount())
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(sink.LastNotification().Content, &parsed); err != nil {
		t.Fatalf("failed to unmarshal notification: %v", err)
	}
	if parsed["type"] != "system" || parsed["subtype"] != "compact_boundary" {
		t.Fatalf("expected compact boundary notification, got %+v", parsed)
	}
	if parsed["threadId"] != "t1" || parsed["turnId"] != "turn1" {
		t.Fatalf("expected thread/turn ids to be preserved, got %+v", parsed)
	}
	if sink.MessageCount() != 0 {
		t.Fatalf("expected 0 assistant messages, got %d", sink.MessageCount())
	}
}

func TestHandleCodexOutput_CommandExecutionOutputDelta(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"item/commandExecution/outputDelta","params":{"itemId":"cmd-1","delta":"hello\n","threadId":"t1","turnId":"turn1"}}`
	handleCodexOutput(agent, []byte(input))

	if sink.StreamChunkCount() != 1 {
		t.Fatalf("expected 1 stream chunk, got %d", sink.StreamChunkCount())
	}
	got := sink.LastStreamChunk()
	if got.SpanID != "cmd-1" {
		t.Fatalf("expected spanID cmd-1, got %q", got.SpanID)
	}
	if got.Method != "item/commandExecution/outputDelta" {
		t.Fatalf("expected command output method, got %q", got.Method)
	}
	if string(got.Content) != "hello\n" {
		t.Fatalf("unexpected content %q", string(got.Content))
	}
	if sink.MessageCount() != 0 {
		t.Fatalf("expected no persisted messages, got %d", sink.MessageCount())
	}
}

func TestHandleCodexOutput_ReasoningSummaryTextDelta(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"item/reasoning/summaryTextDelta","params":{"itemId":"reason-1","delta":"thinking...","summaryIndex":0,"threadId":"t1","turnId":"turn1"}}`
	handleCodexOutput(agent, []byte(input))

	if sink.StreamChunkCount() != 1 {
		t.Fatalf("expected 1 stream chunk, got %d", sink.StreamChunkCount())
	}
	got := sink.LastStreamChunk()
	if got.SpanID != "reason-1" {
		t.Fatalf("expected spanID reason-1, got %q", got.SpanID)
	}
	if got.Method != "item/reasoning/summaryTextDelta" {
		t.Fatalf("expected reasoning summary method, got %q", got.Method)
	}
	if string(got.Content) != "thinking..." {
		t.Fatalf("unexpected content %q", string(got.Content))
	}
	if sink.MessageCount() != 0 {
		t.Fatalf("expected no persisted messages, got %d", sink.MessageCount())
	}
}

func TestHandleCodexOutput_ReasoningSummaryPartAdded(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"item/reasoning/summaryPartAdded","params":{"itemId":"reason-1","summaryIndex":1,"threadId":"t1","turnId":"turn1"}}`
	handleCodexOutput(agent, []byte(input))

	if sink.StreamChunkCount() != 1 {
		t.Fatalf("expected 1 stream chunk, got %d", sink.StreamChunkCount())
	}
	got := sink.LastStreamChunk()
	if got.SpanID != "reason-1" {
		t.Fatalf("expected spanID reason-1, got %q", got.SpanID)
	}
	if got.Method != "item/reasoning/summaryPartAdded" {
		t.Fatalf("expected reasoning summary part method, got %q", got.Method)
	}
	if len(got.Content) != 0 {
		t.Fatalf("expected empty content, got %q", string(got.Content))
	}
	if sink.MessageCount() != 0 {
		t.Fatalf("expected no persisted messages, got %d", sink.MessageCount())
	}
}

func TestHandleCodexOutput_ReasoningTextDelta(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"item/reasoning/textDelta","params":{"itemId":"reason-1","delta":"raw chain","contentIndex":0,"threadId":"t1","turnId":"turn1"}}`
	handleCodexOutput(agent, []byte(input))

	if sink.StreamChunkCount() != 1 {
		t.Fatalf("expected 1 stream chunk, got %d", sink.StreamChunkCount())
	}
	got := sink.LastStreamChunk()
	if got.SpanID != "reason-1" {
		t.Fatalf("expected spanID reason-1, got %q", got.SpanID)
	}
	if got.Method != "item/reasoning/textDelta" {
		t.Fatalf("expected reasoning text method, got %q", got.Method)
	}
	if string(got.Content) != "raw chain" {
		t.Fatalf("unexpected content %q", string(got.Content))
	}
	if sink.MessageCount() != 0 {
		t.Fatalf("expected no persisted messages, got %d", sink.MessageCount())
	}
}

func TestHandleCodexOutput_CommandExecutionTerminalInteraction(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"item/commandExecution/terminalInteraction","params":{"itemId":"cmd-1","processId":"123","stdin":"y\n","threadId":"t1","turnId":"turn1"}}`
	handleCodexOutput(agent, []byte(input))

	if sink.StreamChunkCount() != 1 {
		t.Fatalf("expected 1 stream chunk, got %d", sink.StreamChunkCount())
	}
	got := sink.LastStreamChunk()
	if got.SpanID != "cmd-1" {
		t.Fatalf("expected spanID cmd-1, got %q", got.SpanID)
	}
	if got.Method != "item/commandExecution/terminalInteraction" {
		t.Fatalf("expected command interaction method, got %q", got.Method)
	}
	if string(got.Content) != "y\n" {
		t.Fatalf("unexpected content %q", string(got.Content))
	}
	if sink.MessageCount() != 0 {
		t.Fatalf("expected no persisted messages, got %d", sink.MessageCount())
	}
}

func TestHandleCodexOutput_FileChangeOutputDelta(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"item/fileChange/outputDelta","params":{"itemId":"fc-1","delta":"diff --git a.txt b.txt\n","threadId":"t1","turnId":"turn1"}}`
	handleCodexOutput(agent, []byte(input))

	if sink.StreamChunkCount() != 1 {
		t.Fatalf("expected 1 stream chunk, got %d", sink.StreamChunkCount())
	}
	got := sink.LastStreamChunk()
	if got.SpanID != "fc-1" {
		t.Fatalf("expected spanID fc-1, got %q", got.SpanID)
	}
	if got.Method != "item/fileChange/outputDelta" {
		t.Fatalf("expected file change output method, got %q", got.Method)
	}
	if string(got.Content) != "diff --git a.txt b.txt\n" {
		t.Fatalf("unexpected content %q", string(got.Content))
	}
	if sink.MessageCount() != 0 {
		t.Fatalf("expected no persisted messages, got %d", sink.MessageCount())
	}
}

func TestHandleCodexOutput_PlanDeltaThenCompleted(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	// Send a plan delta first.
	delta := `{"method":"item/plan/delta","params":{"delta":"# Plan\n"}}`
	handleCodexOutput(agent, []byte(delta))

	if sink.SessionInfoCount() != 1 {
		t.Fatalf("expected 1 session info after delta, got %d", sink.SessionInfoCount())
	}

	// Send item/completed with plan type.
	completed := `{"method":"item/completed","params":{"item":{"type":"plan","id":"plan1","text":"# Plan\nStep 1"}}}`
	handleCodexOutput(agent, []byte(completed))

	// Session info should now have streamingType "" to clear the plan streaming.
	if sink.SessionInfoCount() != 2 {
		t.Fatalf("expected 2 session info broadcasts, got %d", sink.SessionInfoCount())
	}
	info := sink.LastSessionInfo()
	if info["streamingType"] != "" {
		t.Errorf("expected streamingType '', got %v", info["streamingType"])
	}

	// Plan message should be persisted.
	if sink.MessageCount() != 1 {
		t.Fatalf("expected 1 persisted message, got %d", sink.MessageCount())
	}

	// Verify streamingPlan flag was cleared (next delta should re-broadcast).
	delta2 := `{"method":"item/plan/delta","params":{"delta":"New plan\n"}}`
	handleCodexOutput(agent, []byte(delta2))

	if sink.SessionInfoCount() != 3 {
		t.Fatalf("expected 3 session info broadcasts, got %d", sink.SessionInfoCount())
	}
	info2 := sink.LastSessionInfo()
	if info2["streamingType"] != "plan" {
		t.Errorf("expected streamingType 'plan', got %v", info2["streamingType"])
	}
}

func TestHandleCodexOutput_CommandExecutionCompletedBroadcastsStreamEnd(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	completed := `{"method":"item/completed","params":{"threadId":"t1","item":{"type":"commandExecution","id":"cmd-1","status":"completed","aggregatedOutput":"done"}}}`
	handleCodexOutput(agent, []byte(completed))

	if sink.MessageCount() != 1 {
		t.Fatalf("expected 1 persisted message, got %d", sink.MessageCount())
	}
	if sink.StreamEndCount() != 1 {
		t.Fatalf("expected 1 stream end, got %d", sink.StreamEndCount())
	}
	if got := sink.LastStreamEnd(); got != "cmd-1" {
		t.Fatalf("expected stream end for cmd-1, got %q", got)
	}
}

func TestHandleCodexOutput_FileChangeCompletedBroadcastsStreamEnd(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	completed := `{"method":"item/completed","params":{"threadId":"t1","item":{"type":"fileChange","id":"fc-1","status":"completed","changes":[{"path":"a.txt","kind":"update","diff":"@@ -1 +1 @@\n-old\n+new"}]}}}`
	handleCodexOutput(agent, []byte(completed))

	if sink.MessageCount() != 1 {
		t.Fatalf("expected 1 persisted message, got %d", sink.MessageCount())
	}
	if sink.StreamEndCount() != 1 {
		t.Fatalf("expected 1 stream end, got %d", sink.StreamEndCount())
	}
	if got := sink.LastStreamEnd(); got != "fc-1" {
		t.Fatalf("expected stream end for fc-1, got %q", got)
	}
}

func TestHandleCodexOutput_ReasoningCompletedBroadcastsStreamEnd(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	completed := `{"method":"item/completed","params":{"threadId":"t1","item":{"type":"reasoning","id":"reason-1","summary":["done"]}}}`
	handleCodexOutput(agent, []byte(completed))

	if sink.MessageCount() != 1 {
		t.Fatalf("expected 1 persisted message, got %d", sink.MessageCount())
	}
	if sink.StreamEndCount() != 1 {
		t.Fatalf("expected 1 stream end, got %d", sink.StreamEndCount())
	}
	if got := sink.LastStreamEnd(); got != "reason-1" {
		t.Fatalf("expected stream end for reason-1, got %q", got)
	}
}

func TestHandleCodexOutput_ApprovalWithoutID(t *testing.T) {
	sink := &controlTestSink{}
	agent := newCodexAgentWithSink(sink)

	// Missing "id" field — should be ignored (logged as warning).
	input := `{"method":"item/tool/requestUserInput","params":{"questions":[]}}`

	handleCodexOutput(agent, []byte(input))

	if sink.PersistedControlCount() != 0 {
		t.Errorf("expected 0 persisted control requests (no id), got %d", sink.PersistedControlCount())
	}
	if sink.BroadcastControlCount() != 0 {
		t.Errorf("expected 0 broadcast control requests (no id), got %d", sink.BroadcastControlCount())
	}
}

func TestHandleCodexOutput_TokenUsageUpdatedBroadcastsContextUsage(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)

	input := `{"method":"thread/tokenUsage/updated","params":{"threadId":"thread-1","turnId":"turn-1","tokenUsage":{"total":{"totalTokens":200,"inputTokens":100,"cachedInputTokens":25,"outputTokens":50,"reasoningOutputTokens":9},"last":{"totalTokens":23,"inputTokens":10,"cachedInputTokens":5,"outputTokens":7,"reasoningOutputTokens":1},"modelContextWindow":4096}}}`
	handleCodexOutput(agent, []byte(input))

	if sink.NotificationCount() != 1 {
		t.Fatalf("expected 1 persisted notification, got %d", sink.NotificationCount())
	}
	if sink.SessionInfoCount() != 1 {
		t.Fatalf("expected 1 session info broadcast, got %d", sink.SessionInfoCount())
	}

	info := sink.LastSessionInfo()
	usage, ok := info["contextUsage"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected contextUsage map, got %#v", info["contextUsage"])
	}
	if usage["inputTokens"] != int64(100) {
		t.Fatalf("expected inputTokens 100, got %#v", usage["inputTokens"])
	}
	if usage["cacheCreationInputTokens"] != int64(0) {
		t.Fatalf("expected cacheCreationInputTokens 0, got %#v", usage["cacheCreationInputTokens"])
	}
	if usage["cacheReadInputTokens"] != int64(25) {
		t.Fatalf("expected cacheReadInputTokens 25, got %#v", usage["cacheReadInputTokens"])
	}
	if usage["contextWindow"] != int64(4096) {
		t.Fatalf("expected contextWindow 4096, got %#v", usage["contextWindow"])
	}
}

func TestHandleCodexOutput_TokenUsageUpdatedFallsBackToModelContextWindow(t *testing.T) {
	sink := &testSink{}
	agent := newCodexAgentWithSink(sink)
	agent.model = "gpt-5.4"
	agent.availableModels = codexDefaultModels

	input := `{"method":"thread/tokenUsage/updated","params":{"threadId":"thread-1","turnId":"turn-1","tokenUsage":{"total":{"totalTokens":200,"inputTokens":100,"cachedInputTokens":25,"outputTokens":50,"reasoningOutputTokens":9},"last":{"totalTokens":23,"inputTokens":10,"cachedInputTokens":5,"outputTokens":7,"reasoningOutputTokens":1},"modelContextWindow":null}}}`
	handleCodexOutput(agent, []byte(input))

	info := sink.LastSessionInfo()
	usage, ok := info["contextUsage"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected contextUsage map, got %#v", info["contextUsage"])
	}
	if usage["contextWindow"] != int64(1_050_000) {
		t.Fatalf("expected fallback contextWindow 1050000, got %#v", usage["contextWindow"])
	}
}
