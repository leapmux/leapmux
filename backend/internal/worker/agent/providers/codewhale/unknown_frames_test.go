package codewhale

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestCodewhaleUnknownEventsPersist(t *testing.T) {
	t.Parallel()
	agenttest.AssertUnknownFramePersists(t, []agenttest.UnknownFrameCase{
		{Name: "current event", Frame: runtimeEvent(1, "future.event", testTurnID, "", map[string]any{"value": 1})},
		{Name: "current item start", Frame: itemEvent(1, "item.started", "future-item", "future_kind", "", nil)},
		{Name: "current item delta", Frame: runtimeEvent(1, eventItemDelta, testTurnID, "future-item", map[string]any{"kind": "future_kind", "delta": "Future content."})},
		{Name: "current item end", Frame: itemEvent(1, "item.completed", "future-item", "future_kind", "Future detail.", nil)},
		{Name: "other thread", Frame: []byte(`{"seq":1,"event":"future.event","thread_id":"other","payload":{"value":1}}`), OtherSession: true},
		{Name: "no thread", Frame: []byte(`{"seq":1,"event":"future.event","payload":{"value":1}}`), OtherSession: true},
	}, func(t *testing.T, tc agenttest.UnknownFrameCase) *agenttest.Sink {
		a, sink := newTestAgent(t, nil)
		a.HandleOutput(tc.Frame)
		return &sink.Sink
	})
}

func TestCodewhaleStreamEventVerdictsStoreNothing(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	for i, event := range []string{"turn.artifacts", "turn.workspace_snapshot", "approval.grant_added", "approval.grant_revoked", "agent.future", "tool_call.future", "agent_mail.future"} {
		a.HandleOutput(runtimeEvent(uint64(i+1), event, testTurnID, "", map[string]any{}))
	}
	if len(sink.Messages()) != 0 || len(sink.PersistedNotifications()) != 0 {
		t.Fatal("a consumed native event wrote a row")
	}
}
