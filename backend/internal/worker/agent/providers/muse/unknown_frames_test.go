package muse

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestMuseUnknownMethodsPersist(t *testing.T) {
	agenttest.AssertUnknownFramePersists(t, []agenttest.UnknownFrameCase{
		{Name: "owned", Frame: []byte(`{"method":"future/event","params":{"sessionId":"session","value":1}}`)},
		{Name: "opaque fields", Frame: []byte(`{"method":"future/event","params":{"sessionId":"session","modelId":42,"value":9007199254740993}}`)},
		{Name: "foreign", Frame: []byte(`{"method":"future/event","params":{"sessionId":"other","value":1}}`), OtherSession: true},
		{Name: "no session", Frame: []byte(`{"method":"future/event","params":{"value":1}}`), OtherSession: true},
	}, func(t *testing.T, tc agenttest.UnknownFrameCase) *agenttest.Sink {
		a, sink := testAgent(t)
		a.HandleOutput(tc.Frame)
		return sink
	})
}
