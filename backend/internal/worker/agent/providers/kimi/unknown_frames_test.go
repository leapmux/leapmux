package kimi

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestKimiUnknownEventsPersist(t *testing.T) {
	t.Parallel()
	agenttest.AssertUnknownFramePersists(t, []agenttest.UnknownFrameCase{
		{Name: "main", Frame: []byte(`{"session_id":"session_1","payload":{"type":"future.event","agentId":"main","value":1}}`), Stored: []byte(`{"type":"future.event","agentId":"main","value":1}`)},
		{Name: "child", Frame: []byte(`{"session_id":"session_1","payload":{"type":"future.event","agentId":"agent-1","value":1}}`), Stored: []byte(`{"type":"future.event","agentId":"agent-1","value":1}`)},
		{Name: "other session", Frame: []byte(`{"session_id":"session_other","payload":{"type":"future.event","agentId":"main"}}`), OtherSession: true},
	}, func(t *testing.T, tc agenttest.UnknownFrameCase) *agenttest.Sink {
		sink := &agenttest.Sink{}
		a := newOfflineKimiAgent(t, sink)
		if tc.Name == "child" {
			a.children.link("agent-1", &kimiChild{childAgentID: "child-1", rowKey: "session_1/agent-1"})
		}
		a.HandleOutput(tc.Frame)
		if tc.Name == "child" {
			if len(sink.Messages()) != 0 || len(sink.PersistedNotifications()) != 0 {
				t.Error("the child event reached the parent transcript")
			}
			return sink.Child("child-1")
		}
		return sink
	})
}
