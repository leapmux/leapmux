package mimo

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
)

func TestMiMoUnknownEventsPersist(t *testing.T) {
	t.Parallel()
	agenttest.AssertUnknownFramePersists(t, []agenttest.UnknownFrameCase{
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
