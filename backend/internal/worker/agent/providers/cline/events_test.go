package cline

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCloudReplicationStaysOutsideLocalTranscript(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		payload any
	}{
		{name: "empty snapshot", payload: map[string]any{
			"serviceId": "cloud-service", "revision": 0, "scopeGeneration": 0, "snapshots": []any{},
		}},
		{name: "incremental update", payload: map[string]any{
			"serviceId": "cloud-service", "revision": 1, "scopeGeneration": 0,
			"event": map[string]any{"type": "removed", "sessionId": "cloud-session"},
		}},
		{name: "absent payload", payload: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.feedTurn(t)
			r.feed(t, eventRunStarted, map[string]any{})
			activity := r.sink.TurnActives()
			infoCount := r.sink.SessionInfoCount()
			progress := r.sink.ProgressSnapshot()

			r.feed(t, "cloud.event", test.payload)
			assert.Empty(t, r.sink.Messages(), "cloud viewer state is not local conversation output")
			assert.Empty(t, r.sink.PersistedNotifications())
			assert.Equal(t, activity, r.sink.TurnActives())
			assert.Equal(t, infoCount, r.sink.SessionInfoCount())
			assert.Equal(t, progress, r.sink.ProgressSnapshot())

			r.feed(t, contracts.ClineEventAssistantFinished, map[string]any{"text": "Local reply."})
			r.feed(t, contracts.ClineEventRunCompleted, map[string]any{"reason": runReasonCompleted})
			require.Len(t, r.sink.Messages(), 2)
			assert.Equal(t, []string{contracts.ClineEventAssistantFinished, contracts.ClineEventRunCompleted}, rowEvents(t, &r.sink.Sink))
			assert.Contains(t, string(r.sink.Messages()[0].Content), "Local reply.")
			r.feed(t, "cloud.future_event", map[string]any{"value": 1})
			rows := r.sink.Messages()
			require.Len(t, rows, 3, "an unknown event must still reach the diagnostic transcript")
			assert.Contains(t, string(rows[2].Content), "cloud.future_event")
		})
	}
}
