package kimi

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiNativeControlFlushRetainsOneAuthoritativeTextRow(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name, prefix, snapshotText, delta, want string
		replay                                  bool
	}{
		{"unchanged prefix", "prefix", "prefix", "", "prefix", true},
		{"later append", "same", "same", " tail", "same tail", true},
		{"recovered unicode suffix", "A🙂", "A🙂 recovered", "!", "A🙂 recovered!", true},
		{"corrected snapshot", "old", "corrected", "!", "corrected!", true},
		{"append without replay", "same", "", " tail", "same tail", false},
		{"empty prefix", "", "", "after", "after", true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiConversationRig(t)
			const turn = `"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]`
			const item = `"type":"assistant_message","item_id":"f1","created_at":"2026-11-18T00:00:00Z"`
			rig.conversationFrame(t, `{"type":"turn.started","turn":{`+turn+`}}`)
			rig.conversationFrame(t, fmt.Sprintf(`{"type":"item.started","turn_id":"t0","item":{%s,"status":"running","text":%q}}`, item, scenario.prefix))
			publishNativeKimiApproval(t, rig, "main", "approval_1")
			awaitKimiTestOutput(t, rig.agent.sink)
			require.Len(t, rig.sink.PublishedControls(), 1)
			rows := rig.sink.Messages()
			if scenario.prefix == "" {
				require.Empty(t, rows)
			} else {
				require.Len(t, rows, 1)
				view, err := decodeKimiConversationRecord(rows[0].Content)
				require.NoError(t, err)
				require.NotNil(t, view.item)
				require.NotNil(t, view.item.Text)
				assert.Equal(t, scenario.prefix, *view.item.Text)
			}
			if scenario.replay {
				page := fmt.Sprintf(`{"type":"turns","turns":[{%s,"items":[{%s,"status":"running","text":%q}]}],"has_more":false}`, turn, item, scenario.snapshotText)
				rig.conversationFrame(t, page)
				rig.conversationFrame(t, page)
			}
			if scenario.delta != "" {
				rig.conversationFrame(t, fmt.Sprintf(`{"type":"item.delta","turn_id":"t0","item_id":"f1","field":"text","delta":%q}`, scenario.delta))
			}
			ended := fmt.Sprintf(`{"type":"item.ended","turn_id":"t0","item":{%s,"status":"ended","outcome":"completed","text":%q}}`, item, scenario.want)
			rig.conversationFrame(t, ended)
			rig.conversationFrame(t, ended)
			awaitKimiTestOutput(t, rig.agent.sink)
			updated := rig.sink.Messages()
			require.Len(t, updated, 1)
			latest := updated[0].Content
			if len(rows) != 0 {
				assert.Equal(t, rows[0].Content, latest, "the original row must stay immutable")
				supplement, err := kimiConversationProviderFields(updated[0].SupplementalContent)
				require.NoError(t, err)
				latest = supplement[contracts.KimiConversationSupplementRoot]
			}
			view, err := decodeKimiConversationRecord(latest)
			require.NoError(t, err)
			require.NotNil(t, view.item)
			require.NotNil(t, view.item.Text)
			assert.Equal(t, scenario.want, *view.item.Text)
			assert.Equal(t, contracts.KimiConversationStatusEnded, view.item.Status)
			assert.Equal(t, agent.MessageCompletionComplete, updated[0].Completion)
		})
	}
}

type kimiControlFlushOrderSink struct {
	agenttest.ControlSink
	textAtPublication chan []agenttest.Message
}

func (sink *kimiControlFlushOrderSink) PublishControlRequest(request agent.ControlRequest) error {
	sink.textAtPublication <- sink.Messages()
	return sink.ControlSink.PublishControlRequest(request)
}

func TestKimiNativeControlFlushOrdersTheControlAfterRetainedText(t *testing.T) {
	t.Parallel()
	sink := &kimiControlFlushOrderSink{textAtPublication: make(chan []agenttest.Message, 1)}
	runtime := newOfflineKimiAgent(t, sink)
	sink.UpdateSessionID("session_1")
	rig := &kimiOutputRig{agent: runtime, sink: &sink.ControlSink}
	rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"turn","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"turn","item":{"type":"assistant_message","item_id":"text","status":"running","created_at":"2026-11-18T00:00:00Z","text":"I need permission."}}`)

	var release atomic.Bool
	queue := agent.OutputQueueFor(runtime.sink)
	require.NotNil(t, queue)
	queue.Append(agent.OutputWrite{Owner: agent.NewOutputOwner(), Label: "hold an earlier Kimi output write", Run: func() error {
		if !release.Load() {
			return agent.ErrOutputInFlight
		}
		return nil
	}})
	publishNativeKimiApproval(t, rig, "main", "approval_1")
	assert.Empty(t, sink.PublishedControls())
	release.Store(true)
	awaitKimiTestOutput(t, runtime.sink)
	require.Len(t, sink.PublishedControls(), 1)
	messages := <-sink.textAtPublication
	require.Len(t, messages, 1)
	view, err := decodeKimiConversationRecord(messages[0].Content)
	require.NoError(t, err)
	require.NotNil(t, view.item)
	require.NotNil(t, view.item.Text)
	assert.Equal(t, "I need permission.", *view.item.Text)
}
