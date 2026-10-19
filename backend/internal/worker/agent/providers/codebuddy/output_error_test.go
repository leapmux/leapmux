package codebuddy

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func diagnosticAssistantFrame(t *testing.T, requestID, sessionID, text string, modelMessage bool) []byte {
	t.Helper()
	// e2e-PJqZmM recorded this runtime-generated assistant diagnostic before its failed result.
	frame := map[string]any{
		"type": "assistant", "_requestId": requestID, "session_id": sessionID,
		"message": map[string]any{
			"role": "assistant", "content": []map[string]string{{"type": "text", "text": text}},
			"usage": map[string]int{"input_tokens": 0, "output_tokens": 0},
		},
	}
	if modelMessage {
		frame["_messageId"] = "model-message"
	}
	raw, err := json.Marshal(frame)
	require.NoError(t, err)
	return raw
}

func TestCodebuddyResultKeepsEachErrorOnce(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, requestID, sessionID, reason string
		failed, modelMessage               bool
		repeated                           bool
	}{
		{"repeated diagnostic", "request-1", "session-1", "The model refused the request.", true, false, true},
		{"distinct error", "request-1", "session-1", "Another failure.", true, false, false},
		{"successful answer", "request-1", "session-1", "The model refused the request.", false, false, false},
		{"another request", "request-2", "session-1", "The model refused the request.", true, false, false},
		{"another session", "request-1", "session-2", "The model refused the request.", true, false, false},
		{"actual model message", "request-1", "session-1", "The model refused the request.", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			a.HandleOutput(diagnosticAssistantFrame(t, "request-1", "session-1", "The model refused the request.", tc.modelMessage))
			result, err := json.Marshal(map[string]any{
				"type": "result", "_requestId": tc.requestID, "session_id": tc.sessionID,
				"is_error": tc.failed, "errors": []string{tc.reason},
			})
			require.NoError(t, err)
			a.HandleOutput(result)
			rows := sink.Messages()
			require.Len(t, rows, 2, "both native rows remain available unchanged")
			assert.True(t, rows[len(rows)-1].TurnEnd)
			assert.JSONEq(t, string(result), string(rows[len(rows)-1].Content))
			assert.Contains(t, string(rows[0].Content), "The model refused the request.")
			if tc.repeated {
				var extra contracts.CodebuddyResultSupplement
				require.NoError(t, json.Unmarshal(rows[1].SupplementalContent, &extra))
				assert.Equal(t, "request-1", extra.RequestID)
				assert.Equal(t, "session-1", extra.SessionID)
				assert.Equal(t, tc.reason, extra.RepeatedError)
			} else {
				assert.Empty(t, rows[1].SupplementalContent)
			}
		})
	}
}

func TestCodebuddyDiagnosticRemainsImmediateWithoutAResult(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	raw := diagnosticAssistantFrame(t, "request-1", "session-1", "The model failed.", false)
	a.HandleOutput(raw)
	require.Len(t, sink.Messages(), 1)
	assert.JSONEq(t, string(raw), string(sink.Messages()[0].Content))
}

func TestCodebuddyDiagnosticRequiresNativeIdentityAndZeroOutput(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, requestID, sessionID string
		output                     any
	}{
		{"absent request", "", "session-1", 0},
		{"blank request", " ", "session-1", 0},
		{"absent session", "request-1", "", 0},
		{"blank session", "request-1", " ", 0},
		{"absent output", "request-1", "session-1", nil},
		{"negative output", "request-1", "session-1", -1},
		{"positive output", "request-1", "session-1", 1},
		{"large output", "request-1", "session-1", int64(1 << 62)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(map[string]any{
				"_requestId": tc.requestID, "session_id": tc.sessionID,
				"message": map[string]any{
					"content": []map[string]string{{"type": "text", "text": "A diagnostic."}},
					"usage":   map[string]any{"output_tokens": tc.output},
				},
			})
			require.NoError(t, err)
			var message assistantMessage
			require.NoError(t, json.Unmarshal(raw, &message))
			assert.Nil(t, message.diagnostic())
		})
	}
}

func TestPendingControlResponseRejectsMalformedInnerData(t *testing.T) {
	t.Parallel()
	for _, inner := range []string{`[]`, `"invalid"`, `7`, `{"mode":7}`} {
		t.Run(inner, func(t *testing.T) {
			a := newOfflineAgent(t, &agenttest.Sink{})
			response := make(chan codebuddyControlResult, 1)
			a.pendingControl["request-1"] = response
			a.HandleOutput([]byte(`{"type":"control_response","response":{"request_id":"request-1","subtype":"success","response":` + inner + `}}`))
			got := <-response
			assert.True(t, got.Answered)
			assert.False(t, got.Success)
			assert.Contains(t, got.Error, "invalid control response")
			assert.Empty(t, got.Mode)
		})
	}
}

func TestDiagnosticMetadataDoesNotChangeAValidAssistantMessage(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		requestID any
		usage     any
	}{
		{"fractional output", "request-1", map[string]any{"output_tokens": 0.5}},
		{"invalid output", "request-1", map[string]any{"output_tokens": "unknown"}},
		{"invalid usage", "request-1", "unknown"},
		{"invalid request identity", 7, map[string]any{"output_tokens": 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			raw, err := json.Marshal(map[string]any{
				"type": "assistant", "_requestId": tc.requestID, "_messageId": "model-message",
				"session_id": "session-1", "message": map[string]any{
					"content": []map[string]string{{"type": "text", "text": "Actual answer."}}, "usage": tc.usage,
				},
			})
			require.NoError(t, err)
			a.HandleOutput(raw)
			a.HandleOutput([]byte(`{"type":"result","is_error":false,"result":"Actual answer."}`))
			require.Len(t, sink.Messages(), 2, "optional diagnostic metadata must not cause a second answer row")
			assert.JSONEq(t, string(raw), string(sink.Messages()[0].Content))
		})
	}
}

func TestDiagnosticIdentityDoesNotPreventResultTextRecovery(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.HandleOutput([]byte(`{"type":"result","_requestId":7,"is_error":false,"result":"Recovered answer."}`))
	require.Len(t, sink.Messages(), 2)
	assert.Contains(t, string(sink.Messages()[0].Content), "Recovered answer.")
}

func TestDiagnosticRequiresOneTextBlock(t *testing.T) {
	t.Parallel()
	var message assistantMessage
	require.NoError(t, json.Unmarshal([]byte(`{"_requestId":"request-1","session_id":"session-1","message":{"usage":{"output_tokens":0},"content":[{"type":"text","text":"First"},{"type":"text","text":"Second"}]}}`), &message))
	assert.Nil(t, message.diagnostic(), "the browser can insert separators between distinct text blocks")
}

func TestDiagnosticWriteFailureKeepsTheResultBehindTheDiagnostic(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{PersistErr: errors.New("the diagnostic write failed")}
	a := newOfflineAgent(t, sink)
	diagnostic := diagnosticAssistantFrame(t, "request-1", "session-1", "The model failed.", false)
	a.HandleOutput(diagnostic)
	a.HandleOutput([]byte(`{"type":"result","_requestId":"request-1","session_id":"session-1","is_error":true,"errors":["The model failed."]}`))
	for _, attempt := range sink.Messages() {
		assert.False(t, attempt.TurnEnd, "the result must wait for the diagnostic")
	}
	sink.PersistErr = nil
	a.HandleOutput([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"Next answer."}]}}`))
	rows := sink.Messages()
	require.GreaterOrEqual(t, len(rows), 3)
	last := rows[len(rows)-3:]
	assert.JSONEq(t, string(diagnostic), string(last[0].Content))
	assert.True(t, last[1].TurnEnd)
	var supplement contracts.CodebuddyResultSupplement
	require.NoError(t, json.Unmarshal(last[1].SupplementalContent, &supplement))
	assert.Equal(t, "The model failed.", supplement.RepeatedError)
	assert.Contains(t, string(last[2].Content), "Next answer.")
}

func TestMalformedControlResponsePreservesTheNativeFailure(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	response := make(chan codebuddyControlResult, 1)
	a.pendingControl["request-1"] = response
	a.HandleOutput([]byte(`{"type":"control_response","response":{"request_id":"request-1","subtype":"error","error":"Native refusal.","response":{"mode":7}}}`))
	got := <-response
	assert.False(t, got.Success)
	assert.Equal(t, "Native refusal.", got.Error)
	assert.Empty(t, got.Mode)
}
