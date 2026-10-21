package qoder

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestQoderUnknownFramesPersist(t *testing.T) {
	t.Parallel()
	agenttest.AssertUnknownFramePersists(t, []agenttest.UnknownFrameCase{
		{Name: "type", Frame: []byte(`{"type":"future_frame","session_id":"session-1","value":1}`)},
		{Name: "subtype", Frame: []byte(`{"type":"system","subtype":"future_subtype","session_id":"session-1","value":1}`)},
		{Name: "foreign type", Frame: []byte(`{"type":"future_frame","session_id":"other","value":1}`), OtherSession: true},
		{Name: "foreign subtype", Frame: []byte(`{"type":"system","subtype":"future_subtype","session_id":"other","value":1}`), OtherSession: true},
		{Name: "no session", Frame: []byte(`{"type":"future_frame","value":1}`), OtherSession: true},
	}, func(t *testing.T, tc agenttest.UnknownFrameCase) *agenttest.Sink {
		a, sink, _ := newGoalAgent(t)
		a.HandleOutput(tc.Frame)
		return sink
	})
}
