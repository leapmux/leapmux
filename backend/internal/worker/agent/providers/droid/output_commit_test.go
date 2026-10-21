package droid

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAssistantCommitUsesFinalContentInNativeOrder(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	for _, raw := range []string{
		`{"type":"assistant_text_delta","messageId":"m","blockIndex":2,"textDelta":"streamed draft"}`,
		`{"type":"assistant_text_complete","messageId":"m","blockIndex":2}`,
		`{"type":"tool_call","toolUse":{"id":"call","name":"Read","input":{"file_path":"note"}}}`,
	} {
		a.HandleOutput(droidAuditNotification(t, "", []byte(raw)))
	}
	assert.Empty(t, sink.Messages())
	commit := []byte(`{"type":"create_message","message":{"id":"m","role":"assistant","content":[{"type":"thinking","thinking":"reason"},{"type":"tool_use","id":"call"},{"type":"text","text":"first"},{"type":"text","text":"second"}]}}`)
	a.HandleOutput(droidAuditNotification(t, "", commit))
	rows := sink.Messages()
	require.Len(t, rows, 5)
	kind, text, completion, ok := agenttest.DecodeAssembledMessage(rows[0].Content)
	require.True(t, ok)
	assert.Equal(t, agent.AssembledMessageKindReasoning, kind)
	assert.Equal(t, "reason", text)
	assert.Equal(t, agent.MessageCompletionComplete, completion)
	assert.Equal(t, "droid-tool-call", rows[1].SpanID)
	for i, want := range []string{"first", "second"} {
		kind, text, completion, ok := agenttest.DecodeAssembledMessage(rows[i+2].Content)
		require.True(t, ok)
		assert.Equal(t, agent.AssembledMessageKindText, kind)
		assert.Equal(t, want, text)
		assert.Equal(t, agent.MessageCompletionComplete, completion)
	}
	assert.JSONEq(t, string(commit), string(rows[4].Content))
	a.HandleOutput(droidAuditNotification(t, "", commit))
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"assistant_text_delta","messageId":"m","blockIndex":2,"textDelta":"late"}`)))
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"agent_turn_completed","reason":"completed"}`)))
	assert.Len(t, sink.Messages(), 6, "a duplicate commit and a late delta add no text")
}

func TestAssistantCommitKeepsAnImmutablePendingWrite(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	commit := []byte(`{"type":"create_message","message":{"id":"m","role":"assistant","content":[{"type":"text","text":"original"}]}}`)
	sink.PersistErr = assert.AnError
	a.HandleOutput(droidAuditNotification(t, "", commit))
	a.HandleOutput(droidAuditNotification(t, "", commit))
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"assistant_message_retracted","messageId":"m"}`)))
	attempts := len(sink.Messages())
	sink.PersistErr = nil
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"agent_turn_completed","reason":"cancelled"}`)))
	rows := sink.Messages()[attempts:]
	require.Len(t, rows, 3)
	_, text, completion, ok := agenttest.DecodeAssembledMessage(rows[0].Content)
	require.True(t, ok)
	assert.Equal(t, "original", text)
	assert.Equal(t, agent.MessageCompletionComplete, completion)
	assert.JSONEq(t, string(commit), string(rows[1].Content))
	assert.True(t, rows[2].TurnEnd)
}

func TestAssistantCommitWithEmptyContentDropsItsDraft(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"assistant_text_delta","messageId":"m","textDelta":"discarded draft"}`)))
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"create_message","message":{"id":"m","role":"assistant","content":[]}}`)))
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"agent_turn_completed","reason":"completed"}`)))
	require.Len(t, sink.Messages(), 2)
	for _, row := range sink.Messages() {
		assert.NotContains(t, string(row.Content), "discarded draft")
	}
}

func TestModelBlocksKeepTheirIndexesUntilAnInterruptedTurnEnds(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	for _, index := range []int{0, 1, -1, 2147483647} {
		raw, err := json.Marshal(map[string]any{"type": "assistant_text_delta", "messageId": "m", "blockIndex": index, "textDelta": "block"})
		require.NoError(t, err)
		a.HandleOutput(droidAuditNotification(t, "", raw))
	}
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"agent_turn_completed","reason":"cancelled"}`)))
	rows := sink.Messages()
	require.Len(t, rows, 4)
	for _, row := range rows[:3] {
		_, text, completion, ok := agenttest.DecodeAssembledMessage(row.Content)
		require.True(t, ok)
		assert.Equal(t, "block", text)
		assert.Equal(t, agent.MessageCompletionInterrupted, completion)
	}
}

func TestRetractionDoesNotDiscardANeighborMessage(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	for _, id := range []string{"m", "m:text:0"} {
		raw, err := json.Marshal(map[string]any{"type": "thinking_text_delta", "messageId": id, "blockIndex": 0, "textDelta": id})
		require.NoError(t, err)
		a.HandleOutput(droidAuditNotification(t, "", raw))
	}
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"assistant_message_retracted","messageId":"m"}`)))
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"agent_turn_completed","reason":"cancelled"}`)))
	rows := sink.Messages()
	require.Len(t, rows, 2)
	_, text, _, ok := agenttest.DecodeAssembledMessage(rows[0].Content)
	require.True(t, ok)
	assert.Equal(t, "m:text:0", text)
}

func TestAssistantCommitRejectsMalformedContentWithoutLosingTheDraft(t *testing.T) {
	t.Parallel()
	for _, content := range []string{`null`, `"not an array"`, `[{"type":"text","text":7}]`, `[{"type":"thinking"}]`, `[{"type":"tool_use"}]`} {
		t.Run(content, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSteerAgent(t)
			a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"assistant_text_delta","messageId":"m","textDelta":"retained"}`)))
			a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"create_message","message":{"id":"m","role":"assistant","content":`+content+`}}`)))
			assert.Empty(t, sink.Messages())
			a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"agent_turn_completed","reason":"cancelled"}`)))
			rows := sink.Messages()
			require.Len(t, rows, 2)
			_, text, completion, ok := agenttest.DecodeAssembledMessage(rows[0].Content)
			require.True(t, ok)
			assert.Equal(t, "retained", text)
			assert.Equal(t, agent.MessageCompletionInterrupted, completion)
		})
	}
}

func TestAssistantCommitIdentitySurvivesOutputStateReplacement(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	commit := []byte(`{"type":"create_message","message":{"id":"m","role":"assistant","content":[{"type":"text","text":"once"}]}}`)
	a.HandleOutput(droidAuditNotification(t, "", commit))
	require.Len(t, sink.Messages(), 2)
	a.outputStates = nil
	a.HandleOutput(droidAuditNotification(t, "", commit))
	assert.Len(t, sink.Messages(), 2)
}
