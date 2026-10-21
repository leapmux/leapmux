package cline

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestClineUnknownEventsPersist(t *testing.T) {
	t.Parallel()
	agenttest.AssertUnknownFramePersists(t, []agenttest.UnknownFrameCase{
		{Name: "current", Frame: []byte(`{"version":"v1","event":"future.event","sessionId":"current-session","payload":{"value":1}}`)},
		{Name: "other", Frame: []byte(`{"version":"v1","event":"future.event","sessionId":"other-session","payload":{"value":1}}`), OtherSession: true},
		{Name: "absent", Frame: []byte(`{"version":"v1","event":"future.event","payload":{"value":1}}`), OtherSession: true},
	}, func(t *testing.T, tc agenttest.UnknownFrameCase) *agenttest.Sink {
		r := newRig(t, func(c *rigConfig) { c.noSession = true })
		r.agent.Mu.Lock()
		r.agent.sessionID = "current-session"
		r.agent.Mu.Unlock()
		r.agent.HandleOutput(tc.Frame)
		return &r.sink.Sink
	})
}
