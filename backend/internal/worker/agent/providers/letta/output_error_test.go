package letta

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lettaErrorFrame(t *testing.T, childID, message string, final bool) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"type": "stream_delta", "subagent_id": childID, "delta": map[string]any{"message_type": "loop_error", "message": message, "is_terminal": final}})
	require.NoError(t, err)
	return raw
}

func TestLettaFinalErrorWithOtherTextKeepsItsRow(t *testing.T) {
	t.Parallel()
	a, sink := newLettaChildTestAgent(t)
	a.HandleOutput(lettaErrorFrame(t, "", "Earlier failure.", false))
	a.HandleOutput(lettaErrorFrame(t, "", "Earlier failure.", true))
	assert.Empty(t, sink.PersistedNotifications())
	a.HandleOutput([]byte(`{"type":"turn_finished","stop_reason":"error","error":"Another failure."}`))
	require.Len(t, sink.PersistedNotifications(), 1)
	assert.Contains(t, string(sink.PersistedNotifications()[0].Content), "Earlier failure.")
	assert.Len(t, sink.Messages(), 1)
}

func TestLettaDistinctHeldErrorsRetainTheEarlierReason(t *testing.T) {
	t.Parallel()
	a, sink := newLettaChildTestAgent(t)
	a.HandleOutput(lettaErrorFrame(t, "", "Earlier reason.", false))
	a.HandleOutput(lettaErrorFrame(t, "", "Final reason.", false))
	a.HandleOutput(lettaErrorFrame(t, "", "Final reason.", true))
	a.HandleOutput([]byte(`{"type":"turn_finished","stop_reason":"error","error":"Final reason."}`))
	require.Len(t, sink.PersistedNotifications(), 1)
	assert.Contains(t, string(sink.PersistedNotifications()[0].Content), "Earlier reason.")
}

func TestLettaHeldErrorAndTurnEndWaitBehindAFailedTextWrite(t *testing.T) {
	t.Parallel()
	a, sink := newLettaChildTestAgent(t)
	a.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(`{"type":"stream_delta","delta":{"message_type":"assistant_message","content":"Partial answer."}}`))
	a.HandleOutput(lettaErrorFrame(t, "", "Earlier reason.", true))
	sink.PersistErr = assert.AnError
	a.HandleOutput([]byte(`{"type":"turn_finished","stop_reason":"error","error":"Final reason."}`))
	assert.Empty(t, sink.PersistedNotifications())
	for _, row := range sink.Messages() {
		assert.False(t, row.TurnEnd)
	}
	sink.PersistErr = nil
	require.NoError(t, agent.FlushPendingOutput(a.sink))
	require.Len(t, sink.PersistedNotifications(), 1)
	rows := sink.Messages()
	require.GreaterOrEqual(t, len(rows), 2)
	assert.Contains(t, string(rows[len(rows)-2].Content), "Partial answer.")
	assert.True(t, rows[len(rows)-1].TurnEnd)
}

func TestLettaRetryRemovesItsHeldLoopError(t *testing.T) {
	t.Parallel()
	a, sink := newLettaChildTestAgent(t)
	a.HandleOutput(lettaErrorFrame(t, "", "Retry this failure.", false))
	a.HandleOutput([]byte(`{"type":"stream_delta","delta":{"message_type":"retry","attempt":1}}`))
	a.HandleOutput([]byte(`{"type":"turn_finished","stop_reason":"end_turn"}`))
	for _, row := range sink.PersistedNotifications() {
		assert.NotContains(t, string(row.Content), "Retry this failure.")
	}
}

func TestLettaRetryResolvesBeforeAToolOnlyAnswer(t *testing.T) {
	t.Parallel()
	a, sink := newLettaChildTestAgent(t)
	a.HandleOutput([]byte(`{"type":"stream_delta","delta":{"message_type":"retry","attempt":1}}`))
	a.HandleOutput([]byte(`{"type":"stream_delta","delta":{"message_type":"client_tool_start","tool_call_id":"tool-1","tool_name":"Bash","tool_input":{"command":"pwd"}}}`))
	assert.Equal(t, []map[string]any{{"type": "api_retry_resolved"}}, sink.LeapMuxNotifications())
	require.Len(t, sink.Messages(), 1)
	assert.Equal(t, "letta-tool-tool-1", sink.Messages()[0].SpanID)
}

func TestLettaChildFinalErrorPublishesOnceAtTheNativeError(t *testing.T) {
	t.Parallel()
	a, sink := newLettaChildTestAgent(t)
	a.HandleOutput(lettaChildStateFrame(t, "running"))
	row, ok := sink.BackgroundTask(lettaTestChildID)
	require.True(t, ok)
	child := sink.Child(row.ChildAgentID)
	a.HandleOutput(lettaErrorFrame(t, lettaTestChildID, "Child failure.", false))
	assert.Empty(t, child.PersistedNotifications())
	a.HandleOutput(lettaErrorFrame(t, lettaTestChildID, "Child failure.", true))
	require.Len(t, child.PersistedNotifications(), 1)
	assert.Contains(t, string(child.PersistedNotifications()[0].Content), "Child failure.")
}

func TestLettaFailedOrCancelledTextKeepsItsCompletion(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		reason     string
		completion agent.MessageCompletion
	}{
		{"error", agent.MessageCompletionError},
		{"cancelled", agent.MessageCompletionInterrupted},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			a, sink := newLettaChildTestAgent(t)
			a.HandleOutput([]byte(`{"type":"stream_delta","delta":{"message_type":"assistant_message","content":"Partial answer."}}`))
			a.HandleOutput([]byte(`{"type":"turn_finished","stop_reason":"` + tc.reason + `"}`))
			require.Len(t, sink.Messages(), 2)
			_, text, completion, ok := agenttest.DecodeAssembledMessage(sink.Messages()[0].Content)
			require.True(t, ok)
			assert.Equal(t, "Partial answer.", text)
			assert.Equal(t, tc.completion, completion)
		})
	}
}

func TestLettaExitKeepsAHeldErrorAndRejectsLateOutput(t *testing.T) {
	t.Parallel()
	a, sink := newLettaChildTestAgent(t)
	done := make(chan struct{})
	close(done)
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{Ctx: t.Context(), ProcessDone: done, StderrDone: done})
	a.HandleOutput(lettaErrorFrame(t, "", "The process failed.", true))
	assert.Empty(t, sink.PersistedNotifications())
	require.NoError(t, a.Wait())
	require.Len(t, sink.PersistedNotifications(), 1)
	a.HandleOutput([]byte(`{"type":"stream_delta","delta":{"message_type":"assistant_message","content":"Late output."}}`))
	a.HandleOutput([]byte(`{"type":"turn_finished","stop_reason":"end_turn"}`))
	assert.Empty(t, sink.Messages())
}
