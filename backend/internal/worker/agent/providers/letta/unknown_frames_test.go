package letta

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLettaUnknownDeltasPersist(t *testing.T) {
	t.Parallel()
	agenttest.AssertUnknownFramePersists(t, []agenttest.UnknownFrameCase{
		{Name: "delta", Frame: []byte(`{"type":"stream_delta","runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"},"delta":{"message_type":"future_delta","value":1}}`), Stored: []byte(`{"message_type":"future_delta","value":1}`)},
		{Name: "event", Frame: []byte(`{"type":"stream_delta","runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"},"delta":{"message_type":"event_message","event_type":"future_event","value":1}}`), Stored: []byte(`{"message_type":"event_message","event_type":"future_event","value":1}`)},
		{Name: "top frame", Frame: []byte(`{"type":"future_frame","runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"},"value":1}`)},
		{Name: "opaque top fields", Frame: []byte(`{"type":"future_frame","runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"},"request_id":42,"error":{"detail":"Future data."}}`)},
		{Name: "opaque delta fields", Frame: []byte(`{"type":"stream_delta","runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"},"delta":{"message_type":"future_delta","message":{"detail":"Future data."}}}`), Stored: []byte(`{"message_type":"future_delta","message":{"detail":"Future data."}}`)},
		{Name: "old top error", Frame: []byte(`{"type":"loop_error","runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"},"message":"Unknown top frame."}`)},
		{Name: "other conversation", Frame: []byte(`{"type":"stream_delta","runtime":{"agent_id":"agent-local-1","conversation_id":"other"},"delta":{"message_type":"future_delta","value":1}}`), OtherSession: true},
		{Name: "other agent", Frame: []byte(`{"type":"future_frame","runtime":{"agent_id":"other","conversation_id":"local-conv-1"},"value":1}`), OtherSession: true},
		{Name: "no runtime", Frame: []byte(`{"type":"future_frame","value":1}`), OtherSession: true},
	}, func(t *testing.T, tc agenttest.UnknownFrameCase) *agenttest.Sink {
		sink := &agenttest.Sink{}
		a := &Agent{sink: agent.NewModelProgressResetSink(agent.NewProviderServices(sink)), agentID: "agent-local-1", conversationID: "local-conv-1"}
		a.HandleOutput(tc.Frame)
		assert.Empty(t, sink.PersistedNotifications(), "unknown frames do not enter notification threads")
		return sink
	})
}

func TestLettaUnknownChildDeltaKeepsItsOwner(t *testing.T) {
	t.Parallel()
	a, sink := newLettaChildTestAgent(t)
	a.HandleOutput(lettaChildStateFrame(t, "running"))
	row, ok := sink.BackgroundTask(lettaTestChildID)
	require.True(t, ok)
	child := sink.Child(row.ChildAgentID)
	a.HandleOutput(lettaChildDeltaFrame(t, map[string]any{"message_type": "future_delta", "value": json.Number("9007199254740993")}))
	require.Len(t, child.Messages(), 2)
	assert.Contains(t, string(child.Messages()[1].Content), "9007199254740993")
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.PersistedNotifications())
	a.HandleOutput([]byte(`{"type":"stream_delta","subagent_id":"subagent-native-1","runtime":{"agent_id":"foreign","conversation_id":"local-conv-1"},"delta":{"message_type":"future_delta","value":2}}`))
	assert.Len(t, child.Messages(), 2)
}

func TestLettaKnownDeltaDropsStoreNothing(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"approval_request_message", "approval_classification_end", "error_message"} {
		a, sink := newLettaChildTestAgent(t)
		a.HandleOutput([]byte(`{"type":"stream_delta","runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"},"delta":{"message_type":"` + kind + `","message":"Internal protocol state."}}`))
		assert.Empty(t, sink.Messages())
		assert.Empty(t, sink.PersistedNotifications())
	}
}

func TestLettaFileOperationsStoreNoTranscriptRow(t *testing.T) {
	t.Parallel()
	a, sink := newLettaChildTestAgent(t)
	// Letta 0.34.15 sends this editor update after a write in native run e2e-sGMOYP.
	a.HandleOutput([]byte(`{"type":"file_ops","path":"notes.txt","cg_entries":[],"ops":[],"source":"agent","document_content":"saved text","runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"}}`))
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.PersistedNotifications())
	assert.Empty(t, sink.TurnLifecycle())
}
