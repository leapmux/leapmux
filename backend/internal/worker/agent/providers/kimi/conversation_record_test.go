package kimi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const kimiConversationRecordTestTool = `{"type":"tool_call","item_id":"call-1","step_index":1,"status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"Bash","input":{"command":"printf once"},"output":"first"}`

func conversationRecordTestView(t *testing.T, record contracts.KimiConversationRecord) kimiConversationRecordView {
	t.Helper()
	raw, err := encodeKimiConversationRecord(record)
	require.NoError(t, err)
	view, err := decodeKimiConversationRecord(raw)
	require.NoError(t, err)
	return view
}

func TestKimiConversationRecordKeysSeparateEveryNativeIdentityField(t *testing.T) {
	t.Parallel()
	base := contracts.KimiConversationRecord{
		Type: contracts.KimiConversationRecordKindItem, SessionID: "native", AgentID: "main", TurnID: "turn-1",
		Role: contracts.KimiConversationRecordRoleResult, Item: json.RawMessage(kimiConversationRecordTestTool),
	}
	view := conversationRecordTestView(t, base)
	updated := base
	updated.Item = json.RawMessage(strings.Replace(kimiConversationRecordTestTool, `"first"`, `"latest"`, 1))
	updated.EventCreatedAt = "2026-11-18T00:01:00Z"
	latest := conversationRecordTestView(t, updated)
	assert.Equal(t, view.key(), latest.key())
	assert.True(t, view.acceptsView(latest))
	keys := map[string]struct{}{view.key(): {}}
	for _, field := range []string{"session", "agent", "turn", "item", "role"} {
		other := base
		switch field {
		case "session":
			other.SessionID = "other"
		case "agent":
			other.AgentID = "agent-0"
		case "turn":
			other.TurnID = "turn-2"
		case "item":
			other.Item = json.RawMessage(strings.Replace(kimiConversationRecordTestTool, `"call-1"`, `"call-2"`, 1))
		case "role":
			other.Role = contracts.KimiConversationRecordRoleRequest
		}
		candidate := conversationRecordTestView(t, other)
		assert.NotContains(t, keys, candidate.key(), field)
		assert.False(t, view.acceptsView(candidate), field)
		keys[candidate.key()] = struct{}{}
	}
	other := base
	other.Item = json.RawMessage(strings.Replace(kimiConversationRecordTestTool, `"step_index":1`, `"step_index":2`, 1))
	assert.False(t, view.acceptsView(conversationRecordTestView(t, other)), "a known step cannot relabel the stored item")
}

func TestKimiConversationRecordTurnAndNoticeVariantsRetainTheirMeaning(t *testing.T) {
	t.Parallel()
	base := contracts.KimiConversationRecord{
		Type: contracts.KimiConversationRecordKindTurn, SessionID: "native", AgentID: "main", TurnID: "turn-1",
		Turn: json.RawMessage(`{"turn_id":"turn-1","ordinal":0,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","steps":[]}`),
	}
	view := conversationRecordTestView(t, base)
	other := base
	other.Turn = json.RawMessage(strings.Replace(string(base.Turn), `"ordinal":0`, `"ordinal":1`, 1))
	candidate := conversationRecordTestView(t, other)
	assert.Equal(t, view.key(), candidate.key())
	assert.False(t, view.acceptsView(candidate), "a stable turn key does not authorize another ordinal")
	notice := contracts.KimiConversationRecord{
		Type: contracts.KimiConversationRecordKindNotice, SessionID: "native", AgentID: "main", EventCreatedAt: "2026-11-18T00:00:00Z",
		Notice: json.RawMessage(`{"level":"warning","title":"Native warning","code":"context.compaction_failed"}`),
	}
	warning := conversationRecordTestView(t, notice)
	require.NotNil(t, warning.notice)
	assert.Equal(t, "context.compaction_failed", *warning.notice.Code)
	assert.Empty(t, warning.record.TurnID, "a notice with no native turn identity does not acquire one")
}

func TestKimiConversationRecordRejectsIncompleteOrMixedEnvelopes(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`{"type":"leapmux.kimi.item","agent_id":"main","turn_id":"turn-1","role":"result","item":` + kimiConversationRecordTestTool + `}`,
		`{"type":"leapmux.kimi.item","session_id":"native","agent_id":"main","role":"result","item":` + kimiConversationRecordTestTool + `}`,
		`{"type":"leapmux.kimi.item","session_id":"native","agent_id":"main","turn_id":"turn-1","role":"item","item":` + kimiConversationRecordTestTool + `}`,
		`{"type":"leapmux.kimi.item","session_id":"native","agent_id":"main","turn_id":"turn-1","role":"result","item":null}`,
		`{"type":"leapmux.kimi.item","session_id":"native","agent_id":"main","turn_id":"turn-1","role":"result","item":` + kimiConversationRecordTestTool + `,"turn":{}}`,
		`{"type":"leapmux.kimi.turn","session_id":"native","agent_id":"main","turn_id":"another","turn":{"turn_id":"turn-1","ordinal":0,"kind":"agent","status":"ended","started_at":"2026-11-18T00:00:00Z","steps":[]}}`,
		`{"type":"leapmux.kimi.notice","session_id":"native","agent_id":"main","turn_id":"guessed","event_created_at":"2026-11-18T00:00:00Z","notice":{"level":"warning","title":"Notice"}}`,
		`{"type":"leapmux.kimi.notice","session_id":"native","agent_id":"main","event_created_at":"2026-11-18T00:00:00Z","notice":{"level":"warning","title":"Notice","code":1}}`,
		`{"type":"unknown","session_id":"native","agent_id":"main"}`,
	} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			_, err := decodeKimiConversationRecord([]byte(raw))
			require.Error(t, err)
		})
	}
}
