package acp

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestSessionStateUpdatesStoreNoRow(t *testing.T) {
	t.Parallel()
	for _, kind := range contracts.ACPSessionStateUpdateMembers {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			b, sink := newACPTurnBase(t, agenttest.NopStdin(&output))
			c := b.main()
			c.out.appendAssistant("before", "answer")
			raw, err := json.Marshal(map[string]string{"sessionUpdate": kind})
			require.NoError(t, err)
			assert.False(t, c.handleUpdate(acpUpdateHeader{SessionUpdate: kind}, raw))
			assert.Empty(t, sink.Messages())
			assert.Empty(t, sink.PersistedNotifications())
			assert.Empty(t, sink.ProgressUpdates())
			assert.Equal(t, "before", b.TurnAssistantTextForTest().String())
		})
	}
}

func TestSessionStateModeRecordStillPersistsThroughTheBase(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	b, sink := newACPTurnBase(t, agenttest.NopStdin(&output))
	update := json.RawMessage(`{"sessionUpdate":"current_mode_update","currentModeId":"plan"}`)
	b.handleACPUpdate(update)
	require.Len(t, sink.Messages(), 1)
	assert.JSONEq(t, string(update), string(sink.Messages()[0].Content))
}

func TestSessionNotificationMetadataUsesTheOuterCurrentSessionMeta(t *testing.T) {
	t.Parallel()
	b := &Base{}
	b.SetSinkForTest(agent.NewProviderServices(&agenttest.Sink{}))
	b.SetSessionIDForTest("session-1")
	var outer, inner map[string]json.RawMessage
	var outerKind string
	var calls int
	b.HooksForTest().SessionNotificationMetadata = func(updateType string, metadata map[string]json.RawMessage) {
		outerKind = updateType
		outer = metadata
		calls++
	}
	b.HooksForTest().SessionMetadataHandler = func(_ string, metadata map[string]json.RawMessage, _ json.RawMessage) bool {
		inner = metadata
		return false
	}
	frame := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":""},"_meta":{"parentToolCallId":"child-1"}},"_meta":{"field_meta":{"openhands.dev/metrics":{"status_line":"120 in, 30 out"}}}}}`
	b.HandleOutput([]byte(frame))

	require.Equal(t, 1, calls)
	assert.Equal(t, "agent_message_chunk", outerKind, "the hook reads the kind of update that carried the metadata")
	assert.JSONEq(t, `{"openhands.dev/metrics":{"status_line":"120 in, 30 out"}}`, string(outer["field_meta"]))
	assert.JSONEq(t, `"child-1"`, string(inner["parentToolCallId"]))
	assert.NotContains(t, outer, "parentToolCallId")
	assert.NotContains(t, inner, "field_meta")

	b.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-other","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":""}},"_meta":{"field_meta":{"openhands.dev/metrics":{"status_line":"999 in, 999 out"}}}}}`))
	b.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":""}}}}`))
	assert.Equal(t, 1, calls, "another session and absent metadata must not invoke the hook")
}
