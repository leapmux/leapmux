package kimi

import (
	"testing"

	"github.com/stretchr/testify/assert"

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
		childID := ""
		if tc.Name == "child" {
			childID = adoptNativeKimiChild(t, a, "agent-1")
		}
		a.HandleOutput(tc.Frame)
		if tc.Name == "child" {
			if len(sink.Messages()) != 0 || len(sink.PersistedNotifications()) != 0 {
				t.Error("the child event reached the parent transcript")
			}
			return sink.Child(childID)
		}
		return sink
	})
}

func TestKimiRetiredTranscriptEnvelopesDoNotPersist(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	for _, frame := range []string{
		`{"type":"transcript.ops","session_id":"session_1","payload":{"type":"transcript.ops","agent_id":"main","seq":1,"ops":[{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"}}}]}}`,
		`{"type":"transcript.reset","session_id":"session_1","payload":{"type":"future.event","agentId":"main"}}`,
		`{"session_id":"session_1","payload":{"type":"transcript.reset","agentId":"main"}}`,
	} {
		rig.agent.HandleOutput([]byte(frame))
	}
	assert.Empty(t, rig.sink.Messages())
	assert.Empty(t, rig.sink.PersistedNotifications())
	assert.Empty(t, rig.sink.TurnActives())
}
