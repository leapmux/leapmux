package acp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestSessionNotificationMetadataUsesTheOuterCurrentSessionMeta(t *testing.T) {
	t.Parallel()
	b := &Base{}
	b.SetSinkForTest(agent.NewProviderServices(&agenttest.Sink{}))
	b.SetSessionIDForTest("session-1")
	var outer, inner map[string]json.RawMessage
	var calls int
	b.HooksForTest().SessionNotificationMetadata = func(metadata map[string]json.RawMessage) {
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
	assert.JSONEq(t, `{"openhands.dev/metrics":{"status_line":"120 in, 30 out"}}`, string(outer["field_meta"]))
	assert.JSONEq(t, `"child-1"`, string(inner["parentToolCallId"]))
	assert.NotContains(t, outer, "parentToolCallId")
	assert.NotContains(t, inner, "field_meta")

	b.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-other","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":""}},"_meta":{"field_meta":{"openhands.dev/metrics":{"status_line":"999 in, 999 out"}}}}}`))
	b.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":""}}}}`))
	assert.Equal(t, 1, calls, "another session and absent metadata must not invoke the hook")
}
