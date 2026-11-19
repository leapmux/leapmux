package kimi

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func kimiContextTestRecord() contracts.KimiConversationRecord {
	return contracts.KimiConversationRecord{Type: contracts.KimiConversationRecordKindItem, SessionID: "native", AgentID: "main", TurnID: "turn-1", Role: contracts.KimiConversationRecordRoleItem,
		Item: json.RawMessage(`{"type":"assistant_message","item_id":"answer:1","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"Native text"}`)}
}

func TestKimiConversationRecordRejectsInvalidTurnContext(t *testing.T) {
	t.Parallel()
	for name, raw := range map[string]string{
		"null": "null", "number": "0", "array": "[]", "empty": "{}",
		"foreign turn":     `{"turn_id":"foreign","kind":"agent","status":"ended"}`,
		"unknown kind":     `{"turn_id":"turn-1","kind":"foreign","status":"ended"}`,
		"unknown status":   `{"turn_id":"turn-1","kind":"agent","status":"foreign"}`,
		"unknown outcome":  `{"turn_id":"turn-1","kind":"agent","status":"ended","outcome":"foreign"}`,
		"nonnative reason": `{"turn_id":"turn-1","kind":"agent","status":"ended","end_reason":0}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			record := kimiContextTestRecord()
			record.TurnContext = json.RawMessage(raw)
			encoded, err := json.Marshal(record)
			require.NoError(t, err)
			_, err = decodeKimiConversationRecord(encoded)
			require.Error(t, err)
		})
	}
}

func TestKimiConversationRecordKeepsOutcomeContextOutOfRequests(t *testing.T) {
	t.Parallel()
	record := kimiContextTestRecord()
	record.Role = contracts.KimiConversationRecordRoleRequest
	record.Item = json.RawMessage(kimiConversationRecordTestTool)
	record.TurnContext = json.RawMessage(`{"turn_id":"turn-1","kind":"agent","status":"ended","outcome":"failed","end_reason":"blocked"}`)
	_, err := encodeKimiConversationRecord(record)
	require.ErrorContains(t, err, "another record role")
}

func TestKimiConversationRecordCorrectionRetainsAcceptedContextIdentity(t *testing.T) {
	t.Parallel()
	record := kimiContextTestRecord()
	record.TurnContext = json.RawMessage(`{"turn_id":"turn-1","kind":"agent","status":"running"}`)
	raw, err := encodeKimiConversationRecord(record)
	require.NoError(t, err)
	original, err := decodeKimiConversationRecord(raw)
	require.NoError(t, err)
	for _, context := range []json.RawMessage{nil, json.RawMessage(`{"turn_id":"turn-1","kind":"system","status":"running"}`)} {
		record.TurnContext = context
		raw, err = encodeKimiConversationRecord(record)
		require.NoError(t, err)
		latest, err := decodeKimiConversationRecord(raw)
		require.NoError(t, err)
		assert.False(t, original.acceptsView(latest), "a correction cannot remove or replace accepted native context identity")
	}
}
