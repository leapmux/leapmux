package dirac

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newDiracNoticeAgent wires an agent whose metadata hook reads notice chunks.
func newDiracNoticeAgent(t *testing.T) (*Agent, *agenttest.Sink) {
	t.Helper()
	sink := &agenttest.Sink{}
	a := &Agent{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetSessionIDForTest("dirac-session")
	a.SetPromptActiveForTest(true)
	a.HooksForTest().SessionMetadataHandler = a.observeDiracNotice
	return a, sink
}

// diracFrame builds one session/update line of the main session.
func diracFrame(t *testing.T, update map[string]any) []byte {
	t.Helper()
	line := map[string]any{
		"jsonrpc": "2.0", "method": "session/update",
		"params": map[string]any{"sessionId": "dirac-session", "update": update},
	}
	raw, err := json.Marshal(line)
	require.NoError(t, err)
	return raw
}

func diracText(t *testing.T, text string) map[string]any {
	t.Helper()
	return map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}}
}

func diracTexts(sink *agenttest.Sink) [][2]interface{} {
	var rows [][2]interface{}
	for _, message := range sink.Messages() {
		if kind, text, completion, ok := agenttest.DecodeAssembledMessage(message.Content); ok && kind == agent.AssembledMessageKindText {
			rows = append(rows, [2]interface{}{text, completion})
		}
	}
	return rows
}

// dirac/cut frames 12-21: the partial text keeps its interrupted row, the
// retry tool card flushes it first, the notice chunk becomes a status row, and
// the second attempt reads clean.
func TestDiracNoticeKeepsTheAttemptSplit(t *testing.T) {
	t.Parallel()
	a, sink := newDiracNoticeAgent(t)

	a.HandleOutput(diracFrame(t, diracText(t, "PARTIAL ")))
	a.HandleOutput(diracFrame(t, diracText(t, "attempt ")))
	a.HandleOutput(diracFrame(t, diracText(t, "one text")))
	a.HandleOutput(diracFrame(t, map[string]any{
		"sessionUpdate": "tool_call", "toolCallId": "retry-1", "title": "API Error (Retrying)",
		"kind": "other", "status": "pending",
	}))
	a.HandleOutput(diracFrame(t, diracText(t, "Retrying API request...")))
	a.HandleOutput(diracFrame(t, diracText(t, "FULL an")))
	a.HandleOutput(diracFrame(t, diracText(t, "swer #2")))
	a.FinishPromptRequestForTest("dirac-session", json.RawMessage(`{"stopReason":"end_turn"}`), nil)

	rows := diracTexts(sink)
	require.Len(t, rows, 2, "the interrupted attempt and the clean answer form two rows")
	assert.Equal(t, "PARTIAL attempt one text", rows[0][0])
	assert.Equal(t, agent.MessageCompletionInterrupted, rows[0][1], "the failed attempt keeps its text, marked interrupted")
	assert.Equal(t, "FULL answer #2", rows[1][0])

	notifications := sink.PersistedNotifications()
	require.Len(t, notifications, 1, "the notice chunk becomes one native row")
	assert.Contains(t, string(notifications[0].Content), "Retrying API request...")
	assert.Equal(t, agent.StatusNotification(), diracProvider{}.Classify(notifications[0].Content))
}

// A notice chunk that an exact match catches has other text around it in the
// model's own output, so a notice with more text is model output.
func TestDiracNoticeRequiresAnExactMatch(t *testing.T) {
	t.Parallel()
	a, sink := newDiracNoticeAgent(t)
	a.HandleOutput(diracFrame(t, diracText(t, "Retrying API request... and more")))
	a.FinishPromptRequestForTest("dirac-session", json.RawMessage(`{"stopReason":"end_turn"}`), nil)
	assert.Empty(t, sink.PersistedNotifications())
	rows := diracTexts(sink)
	require.Len(t, rows, 1)
	assert.Equal(t, "Retrying API request... and more", rows[0][0])
}

// Dirac stamps every main-session update with `dev.dirac/seq`, so a notice
// chunk that carries that metadata is still a notice.
func TestDiracNoticeWithSequenceMetadataIsANotice(t *testing.T) {
	t.Parallel()
	a, sink := newDiracNoticeAgent(t)
	update := diracText(t, "Retrying API request...")
	update["_meta"] = map[string]any{"dev.dirac/seq": 9}
	a.HandleOutput(diracFrame(t, update))
	a.FinishPromptRequestForTest("dirac-session", json.RawMessage(`{"stopReason":"end_turn"}`), nil)
	require.Len(t, sink.PersistedNotifications(), 1, "the stamped notice becomes one native row")
	assert.Empty(t, diracTexts(sink), "the notice joins no text row")
}

// Each of the three other notices becomes its own native row.
func TestDiracOtherNoticesPersist(t *testing.T) {
	t.Parallel()
	for _, notice := range []string{
		"\nHook execution cancelled by user",
		"Shell integration is not available.",
		"Shell integration is not available. Consider using background execution mode for better performance.",
	} {
		t.Run(notice, func(t *testing.T) {
			t.Parallel()
			a, sink := newDiracNoticeAgent(t)
			a.HandleOutput(diracFrame(t, diracText(t, notice)))
			rows := sink.PersistedNotifications()
			require.Len(t, rows, 1)
			assert.Contains(t, string(rows[0].Content), jsonEscape(t, notice))
		})
	}
	_ = contracts.DiracNoticeHookCancelled
}

// The retries-exhausted card is consumed with its updates, because the ask
// that follows it states the same service error once. A later tool_call_update
// of the consumed call stays consumed, and a repeated tool_call id renders
// again after the final update ended the claim.
func TestDiracNoticeConsumesTheExhaustedCard(t *testing.T) {
	t.Parallel()
	a, sink := newDiracNoticeAgent(t)

	exhausted := map[string]any{
		"sessionUpdate": "tool_call", "toolCallId": "exhausted-1", "title": "API Error (Retries Exhausted)",
		"kind": "other", "status": "pending",
		"rawInput":  map[string]any{"body": "The API request failed after 3 retries. 400 NATIVEERROR"},
		"rawOutput": map[string]any{"body": "The API request failed after 3 retries. 400 NATIVEERROR"},
	}
	a.HandleOutput(diracFrame(t, exhausted))
	for _, status := range []string{"in_progress", "in_progress", "failed"} {
		a.HandleOutput(diracFrame(t, map[string]any{
			"sessionUpdate": "tool_call_update", "toolCallId": "exhausted-1", "status": status,
		}))
	}
	a.HandleOutput(diracFrame(t, map[string]any{
		"sessionUpdate": "tool_call", "toolCallId": "ask-1", "title": "API Request Failed",
		"kind": "other", "status": "pending", "rawInput": map[string]any{"body": "400 NATIVEERROR"},
	}))
	a.FinishPromptRequestForTest("dirac-session", json.RawMessage(`{"stopReason":"end_turn"}`), nil)

	for _, message := range sink.Messages() {
		assert.NotContains(t, string(message.Content), "Retries Exhausted", "the exhausted card stores no row")
	}
	rows := sink.PersistedNotifications()
	require.Len(t, rows, 0, "no exhausted frame persists as a notification")

	// The final update ended the claim, so a repeated id renders again.
	a.SetPromptActiveForTest(true)
	a.HandleOutput(diracFrame(t, map[string]any{
		"sessionUpdate": "tool_call", "toolCallId": "exhausted-1", "title": "API Error (Retries Exhausted)",
		"kind": "other", "status": "pending",
	}))
	consumed := a.consumeClaimedUpdate(diracFrame(t, map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": "exhausted-1", "status": "pending",
	}))
	assert.False(t, consumed, "a final update ended the claim of the exhausted call")
}

func jsonEscape(t *testing.T, text string) string {
	t.Helper()
	encoded, err := json.Marshal(text)
	require.NoError(t, err)
	return string(encoded)[1 : len(encoded)-1]
}
