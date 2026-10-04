package droid

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestThinkingDeltaCreatesAReasoningRow(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	for _, frame := range []string{
		`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"thinking_text_delta","messageId":"message-1","blockIndex":1,"textDelta":"Compare "}}}`,
		`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"thinking_text_delta","messageId":"message-1","blockIndex":1,"textDelta":"the values."}}}`,
		`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"thinking_text_complete","messageId":"message-1","blockIndex":1,"durationMs":8}}}`,
	} {
		a.HandleOutput([]byte(frame))
	}

	rows := sink.Messages()
	require.Len(t, rows, 1, "the complete native thinking block creates one row")
	var payload map[string]any
	require.NoError(t, json.Unmarshal(rows[0].Content, &payload))
	assert.Equal(t, contracts.AssembledMessageKindReasoning, payload[contracts.AssembledMessageFieldKind])
	assert.Equal(t, "Compare the values.", payload[contracts.AssembledMessageFieldText])
	assert.Empty(t, sink.PersistedNotifications(), "thinking frames do not become raw notifications")
}

func TestEmptyThinkingBlockCreatesNoRow(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	a.HandleOutput([]byte(`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"thinking_text_complete","messageId":"message-1","blockIndex":1}}}`))
	assert.Empty(t, sink.Messages(), "an empty thinking block has no content")
	assert.Empty(t, sink.PersistedNotifications(), "the completion is protocol state")
}

func TestTurnEndReportsZeroToolUses(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	a.HandleOutput([]byte(`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"assistant_text_delta","messageId":"text-1","blockIndex":0,"textDelta":"A text-only answer."}}}`))
	a.HandleOutput([]byte(`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"assistant_text_complete","messageId":"text-1","blockIndex":0}}}`))
	a.HandleOutput([]byte(`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"agent_turn_completed","turnId":"text-turn","reason":"end_turn"}}}`))

	assert.Equal(t, []int{0}, agenttest.TurnToolUseCounts(t, sink.Messages()))
}

func TestTurnEndCountsEachNativeToolResultOnce(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	result := []byte(`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"tool_result","toolUseId":"read-1","content":"The read result.","isError":false}}}`)
	a.HandleOutput([]byte(`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"tool_call","toolUse":{"id":"read-1","name":"Read","input":{"file_path":"note.txt"}}}}}`))
	a.HandleOutput(result)
	a.HandleOutput(result)
	a.HandleOutput([]byte(`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"agent_turn_completed","turnId":"tool-turn","reason":"end_turn"}}}`))

	assert.Equal(t, []int{1}, agenttest.TurnToolUseCounts(t, sink.Messages()), "a duplicate native result must not increase the count")
}

func TestTurnEndToolCountResetsAndExcludesChildCalls(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	a.HandleOutput([]byte(droidTaskCall))
	a.HandleOutput([]byte(droidChildAvailable))
	for _, frame := range []string{
		`{"type":"notification","params":{"sessionId":"child-1","notification":{"type":"tool_call","toolUse":{"id":"reused-read","name":"Read","input":{"file_path":"child.txt"}}}}}`,
		`{"type":"notification","params":{"sessionId":"child-1","notification":{"type":"tool_result","toolUseId":"reused-read","content":"child result","isError":false}}}`,
		`{"type":"notification","params":{"sessionId":"child-1","notification":{"type":"agent_turn_completed","turnId":"child-turn","reason":"end_turn"}}}`,
		`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"tool_call","toolUse":{"id":"reused-read","name":"Read","input":{"file_path":"root.txt"}}}}}`,
		`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"tool_result","toolUseId":"reused-read","content":"root result","isError":false}}}`,
		`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"agent_turn_completed","turnId":"first-root-turn","reason":"end_turn"}}}`,
		`{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"agent_turn_completed","turnId":"second-root-turn","reason":"end_turn"}}}`,
	} {
		a.HandleOutput([]byte(frame))
	}

	assert.Equal(t, []int{1, 0}, agenttest.TurnToolUseCounts(t, sink.Messages()), "child activity cannot change the reset root count")
}

// These identities preserve the captured native call and session.
const droidAuditSessionID = "d5a2d492-459b-47f2-9b37-d0fe6f5abb74"
const droidAuditCallID = "call_native-full-artifact"

func droidAuditNotification(t *testing.T, sessionID string, payload []byte) []byte {
	t.Helper()
	env := newDroidEnvelope(droidTypeNotification)
	env.Method = droidMethodSessionNotif
	var err error
	env.Params, err = json.Marshal(droidNotification{SessionID: sessionID, Notification: payload})
	require.NoError(t, err)
	line, err := env.Marshal()
	require.NoError(t, err)
	return line
}

func TestDroidFullOutputPreservesActualProgressAndResultShapes(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newSteerAgent(t)
	handshakeDone := make(chan error, 1)
	go func() {
		handshakeDone <- a.handshake(agent.Options{ResumeSessionID: droidAuditSessionID})
	}()
	select {
	case <-stdin.written:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the controlled native load request did not reach stdin")
	}
	frames := stdin.frames()
	require.Len(t, frames, 1)
	var nativeRequest droidEnvelope
	require.NoError(t, json.Unmarshal([]byte(frames[0]), &nativeRequest))
	require.Equal(t, droidMethodLoadSession, nativeRequest.Method)
	request := lastRequest(t, stdin)
	require.Equal(t, droidAuditSessionID, request["sessionId"])
	response := newDroidEnvelope(droidTypeResponse)
	response.ID = droidInitRequestID
	var err error
	response.Result, err = json.Marshal(map[string]any{
		"sessionId": droidAuditSessionID, "settings": map[string]any{}, "availableModels": []any{},
	})
	require.NoError(t, err)
	line, err := response.Marshal()
	require.NoError(t, err)
	a.HandleOutput(line)
	select {
	case err := <-handshakeDone:
		require.NoError(t, err)
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the controlled native load response did not settle startup")
	}
	require.Equal(t, []string{droidAuditSessionID}, sink.SessionIDs())
	call := []byte(`{"type":"tool_call","toolUse":{"type":"tool_use","id":"call_native-full-artifact","name":"Execute","input":{"command":"printf ok","riskLevel":"low"}}}`)
	progress := []byte(`{"type":"tool_progress_update","toolUseId":"call_native-full-artifact","toolName":"Execute","update":{"type":"status","text":"","timestamp":1791035602380,"terminalId":"58abd506-6e96-404c-88b7-25152c70b972"}}`)
	result := []byte(`{"type":"tool_result","toolUseId":"call_native-full-artifact","messageId":"ab8f6d47-df92-4d88-b1fd-ac7cc6e6aec7","content":"native masked summary\n\nFull command output saved to: /unowned/droid-terminal-bxa7bv/58abd506-6e96-404c-88b7-25152c70b972.log (686KB)\n\n[Process exited with code 0]","isError":false}`)

	a.HandleOutput(droidAuditNotification(t, droidAuditSessionID, call))
	a.HandleOutput(droidAuditNotification(t, droidAuditSessionID, progress))
	a.HandleOutput(droidAuditNotification(t, droidAuditSessionID, result))

	notifications := sink.PersistedNotifications()
	require.Len(t, notifications, 1)
	assert.Equal(t, progress, notifications[0].Content, "the original native progress payload stays unchanged")
	rows := sink.Messages()
	require.Len(t, rows, 2)
	for _, row := range rows {
		assert.Equal(t, droidAuditSessionID, row.AgentSessionID, "the native handshake binds every stored tool row to its session")
	}
	assert.Equal(t, result, rows[1].Content, "the native result keeps its masked summary, footer, and status")
	assert.Equal(t, "droid-tool-"+droidAuditCallID, rows[1].SpanID)
	assert.True(t, rows[1].Closing)
	assert.Empty(t, rows[1].SupplementalContent, "an unowned printed path cannot create a receipt")
}

func TestDroidFullOutputForeignProgressCannotWriteTheRoot(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	progress := []byte(`{"type":"tool_progress_update","toolUseId":"call_native-full-artifact","toolName":"Execute","update":{"type":"status","text":"","timestamp":1791035602380,"terminalId":"58abd506-6e96-404c-88b7-25152c70b972"}}`)
	a.HandleOutput(droidAuditNotification(t, "c9ef87b5-4ce6-45a3-9753-7f1e44aad709", progress))
	assert.Empty(t, sink.PersistedNotifications())
	assert.Empty(t, sink.Messages())
	assert.Empty(t, a.outputStates)
}
