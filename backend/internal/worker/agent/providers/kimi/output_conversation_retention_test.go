package kimi

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiPublicTurnEndReleasesPartialBodiesAndKeepsToolIdentity(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.startPublicTurn(t, kimiMainAgentID, "t0", 0)
	const tool = `"type":"tool_call","item_id":"call","step_index":1,"name":"Bash","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","input":{"command":"printf ok"}`
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":{`+tool+`,"status":"running","stage":"executing"}}`)
	rig.conversationFrame(t, `{"type":"item.delta","turn_id":"t0","item_id":"call","field":"output","delta":"partial output"}`)
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":{"type":"assistant_message","item_id":"answer","status":"running","created_at":"2026-11-18T00:00:00Z","text":"Half an answer"}}`)
	rig.endPublicTurn(t, kimiMainAgentID, "t0", 0, "interrupted")
	awaitKimiTestOutput(t, rig.agent.sink)

	rows := rig.sink.Messages()
	require.Len(t, rows, 4)
	require.True(t, rows[1].Closing)
	assert.Equal(t, rows[0].SpanID, rows[1].SpanID)
	assert.Equal(t, agent.MessageCompletionInterrupted, rows[1].Completion)
	require.True(t, rows[3].TurnEnd)
	view, err := decodeKimiConversationRecord(rows[1].Content)
	require.NoError(t, err)
	require.NotNil(t, view.item)
	assert.JSONEq(t, `"partial output"`, string(view.item.Output))

	doc := rig.agent.run(kimiMainAgentID).conversation.document
	turn := doc.turns["t0"]
	for _, itemID := range []string{"call", "answer"} {
		state := turn.items[itemID]
		require.NotNil(t, state)
		assert.Equal(t, itemID, state.identity.itemID)
		assert.Nil(t, state.data, itemID)
		assert.Nil(t, state.text, itemID)
		assert.Nil(t, state.inputText, itemID)
		assert.Nil(t, state.output, itemID)
	}

	original := rows[1].Content
	rig.conversationFrame(t, `{"type":"item.ended","turn_id":"t0","item":{`+tool+`,"status":"ended","outcome":"completed","output":"authoritative output"}}`)
	awaitKimiTestOutput(t, rig.agent.sink)
	rows = rig.sink.Messages()
	require.Len(t, rows, 4)
	assert.Equal(t, original, rows[1].Content)
	assert.Equal(t, rows[0].SpanID, rows[1].SpanID)
	var supplement map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rows[1].SupplementalContent, &supplement))
	latest, err := decodeKimiConversationRecord(supplement[contracts.KimiConversationSupplementRoot])
	require.NoError(t, err)
	require.NotNil(t, latest.item)
	assert.JSONEq(t, `"authoritative output"`, string(latest.item.Output))
	assert.Nil(t, doc.turns["t0"].items["call"].output, "an authoritative result can release its body too")
}

func TestKimiPublicRetryWaitsForLiveUnnamedToolInputAfterReplay(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	const page = `{"type":"turns","has_more":false,"turns":[{"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[{"index":1,"outcome":"failed","started_at":"2026-11-18T00:00:00Z"}],"retry":{"attempt":1,"next_attempt":2,"max_attempts":10,"delay_ms":0,"error":{"name":"status","message":"Failed"}},"items":[{"type":"tool_call","item_id":"partial","step_index":1,"name":"","status":"running","stage":"input","created_at":"2026-11-18T00:00:01Z","started_at":"2026-11-18T00:00:01Z","input_text":"{"}]}]}`
	for range 2 {
		rig.conversationFrame(t, page)
	}
	rig.conversationFrame(t, `{"type":"turn.updated","turn_id":"t0"}`)
	awaitKimiTestOutput(t, rig.agent.sink)
	require.Equal(t, 1, rig.sink.NotificationCount())
	assert.Empty(t, rig.sink.Notifications(), "a replayed prefix and the cleared retry wait are not fresh output")
	rig.conversationFrame(t, `{"type":"item.delta","turn_id":"t0","item_id":"partial","field":"input_text","delta":"\"path\":"}`)
	awaitKimiTestOutput(t, rig.agent.sink)
	assert.Empty(t, rig.sink.Messages(), "partial input does not open a tool declaration")
	require.Len(t, rig.sink.Notifications(), 1)
	assert.Equal(t, contracts.NotificationTypeApiRetryResolved, rig.sink.Notifications()[0]["type"])
}

func TestKimiPublicTaskNotificationRetainsOneDurableItem(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.startPublicTurn(t, kimiMainAgentID, "t0", 0)
	const event = `{"type":"item.ended","turn_id":"t0","item":{"type":"notification","item_id":"task-notice","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","notification_id":"notice-1","notification_type":"task.completed","source":{"kind":"background_task","id":"task-1"},"task_id":"task-1","title":"Task finished","body":"All tests pass.","severity":"info"}}`
	for range 2 {
		rig.conversationFrame(t, event)
	}
	awaitKimiTestOutput(t, rig.agent.sink)
	rows := rig.sink.Messages()
	require.Len(t, rows, 1)
	view, err := decodeKimiConversationRecord(rows[0].Content)
	require.NoError(t, err)
	require.NotNil(t, view.item)
	require.NotNil(t, view.item.NotificationID)
	assert.Equal(t, "notice-1", *view.item.NotificationID)
	assert.Equal(t, "task-notice", view.item.ItemID)
	assert.False(t, rows[0].TurnEnd)
	assert.True(t, rig.agent.PublishTurnActive().Active)
}
