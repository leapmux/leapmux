package kimi

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type kimiRefusedConversationAdmissionSink struct {
	agent.ProviderServices
	refuse atomic.Bool
}

func (sink *kimiRefusedConversationAdmissionSink) ReserveTranscriptOutput(queue *agent.OutputQueue, writes ...agent.OutputWrite) ([]*agent.OutputTicket, error) {
	if sink.refuse.Load() {
		return nil, errors.New("the captured Kimi transcript reservation was refused")
	}
	return sink.ProviderServices.ReserveTranscriptOutput(queue, writes...)
}

func TestKimiRepeatedNativeFrameRetriesAnUnadmittedProjection(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name  string
		frame string
		rows  int
	}{
		{"complete item", `{"type":"item.ended","turn_id":"turn","item":{"type":"assistant_message","item_id":"answer","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"The native answer."}}`, 1},
		{"complete turn", `{"type":"turn.ended","turn":{"turn_id":"turn","ordinal":0,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","steps":[]}}`, 2},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiConversationRig(t)
			queue := agent.OutputQueueFor(rig.agent.sink)
			direct, valid := agent.DirectTranscript(rig.agent.sink).(agent.ProviderServices)
			require.True(t, valid)
			admission := &kimiRefusedConversationAdmissionSink{ProviderServices: direct}
			rig.agent.sink = agent.NewOrderedProviderServices(queue, admission)
			rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"turn","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
			if scenario.rows == 2 {
				rig.conversationFrame(t, `{"type":"item.started","turn_id":"turn","item":{"type":"assistant_message","item_id":"answer","status":"running","created_at":"2026-11-18T00:00:00Z","text":"The native answer."}}`)
			}
			admission.refuse.Store(true)
			rig.conversationFrame(t, scenario.frame)
			assert.Empty(t, rig.sink.Messages())
			assert.False(t, queue.HasPending(), "the queue never owned the refused batch")
			admission.refuse.Store(false)
			rig.conversationFrame(t, scenario.frame)
			awaitKimiTestOutput(t, rig.agent.sink)
			rows := rig.sink.Messages()
			require.Equal(t, scenario.rows, len(rows), "an identical delivery must retry the unadmitted projection")
			view, err := decodeKimiConversationRecord(rows[0].Content)
			require.NoError(t, err)
			require.NotNil(t, view.item)
			require.NotNil(t, view.item.Text)
			assert.Equal(t, "The native answer.", *view.item.Text)
			rig.conversationFrame(t, scenario.frame)
			awaitKimiTestOutput(t, rig.agent.sink)
			assert.Equal(t, rows, rig.sink.Messages(), "a retained replay must keep the same rows and receipts")
			if scenario.rows == 2 {
				assert.False(t, rig.agent.PublishTurnActive().Active)
			}
		})
	}
}
