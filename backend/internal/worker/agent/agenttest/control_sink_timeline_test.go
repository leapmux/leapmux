package agenttest

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestControlSinkRecordsALeapMuxNotificationInTheTimeline(t *testing.T) {
	t.Parallel()
	sink := &ControlSink{}
	sink.PersistLeapMuxNotification(map[string]interface{}{"type": "agent_error"})
	sink.CancelServerErrorRetry("the turn ended without a retryable failure")

	ops := make([]string, 0, 2)
	for _, entry := range sink.Timeline() {
		ops = append(ops, entry.Op)
	}
	assert.Equal(t, []string{"leapmux_notification", "cancel_server_error"}, ops)
	assert.Len(t, sink.Notifications(), 1)
}
