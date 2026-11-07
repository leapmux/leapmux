package mimo

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
)

func TestMiMoUnknownEventsPersist(t *testing.T) {
	t.Parallel()
	agenttest.AssertUnknownFramePersists(t, []agenttest.UnknownFrameCase{
		{Name: "future receipt", Frame: []byte(`{"type":"session.receipt.updated.future","properties":{"sessionID":"ses_test","value":1}}`)},
		{Name: "future inbox", Frame: []byte(`{"type":"session.inbox.future","properties":{"sessionID":"ses_test","value":1}}`)},
		{Name: "current session", Frame: []byte(`{"type":"future.event","properties":{"sessionID":"ses_test","value":1}}`)},
		{Name: "another session", Frame: []byte(`{"type":"future.event","properties":{"sessionID":"another","value":1}}`), OtherSession: true},
		{Name: "absent session", Frame: []byte(`{"type":"future.event","properties":{"value":1}}`), OtherSession: true},
		{Name: "malformed session", Frame: []byte(`{"type":"future.event","properties":{"sessionID":7}}`), OtherSession: true},
	}, func(t *testing.T, tc agenttest.UnknownFrameCase) *agenttest.Sink {
		a, sink, _ := newSinkTestAgent(t)
		feed(a, tc.Frame)
		for _, row := range sink.Messages() {
			assert.Equal(t, testSessionID, row.AgentSessionID)
		}
		return sink
	})
}

func TestMiMoUnknownEventsRespectDiscard(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	a.DiscardOutput()
	feed(a, []byte(`{"type":"future.event","properties":{"sessionID":"ses_test","value":1}}`))
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.PersistedNotifications())
}

func TestMiMoCurrentInboxMetadataCreatesNoTranscriptRows(t *testing.T) {
	t.Parallel()
	events := map[string]any{
		"session.receipt.updated": map[string]any{
			"sessionID": testSessionID, "receiptId": "receipt-1", "agentID": "main", "state": "claimed",
			"inputMessageID": "msg_input", "deliveryMessageID": "msg_delivery", "epoch": 0,
		},
		"session.inbox.delta": map[string]any{
			"sessionID": testSessionID, "receipts": []any{}, "deletedReceiptIDs": []string{"receipt-1"},
			"lanes": []any{map[string]any{"session_id": testSessionID, "agent_id": "main", "consumed_frontier": nil, "input_revision": 1, "time_updated": 1}},
		},
		"session.inbox.snapshot": map[string]any{
			"version": 1, "sessionID": testSessionID, "receipts": []any{}, "lanes": []any{}, "epoch": nil,
		},
	}
	for name, properties := range events {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSinkTestAgent(t)
			feed(a, eventJSON(t, name, properties))
			assert.Empty(t, sink.Messages())
			assert.Empty(t, sink.PersistedNotifications())
		})
	}
}
