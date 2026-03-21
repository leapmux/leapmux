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
		sink: sink,
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
