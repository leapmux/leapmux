package kimi

import (
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiConversationSnapshotReplacesTheLiveBaseWithoutRepeatingOutput(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.startPublicTurn(t, kimiMainAgentID, "current", 1)
	const tool = `"type":"tool_call","item_id":"held","step_index":1,"created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"Bash","input":{"command":"printf test"}`
	const answer = `"type":"assistant_message","item_id":"answer","status":"running","created_at":"2026-11-18T00:00:00Z"`
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"current","item":{`+tool+`,"status":"running","stage":"executing"}}`)
	rig.conversationFrame(t, `{"type":"item.delta","turn_id":"current","item_id":"held","field":"output","delta":"one"}`)
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"current","item":{`+answer+`,"text":"old"}}`)
	awaitKimiTestOutput(t, rig.agent.sink)
	opening := rig.sink.Messages()
	require.Len(t, opening, 1)
	span := opening[0].SpanID
	require.NotEmpty(t, span)
	lifecycle := len(rig.sink.TurnLifecycle())
	const previous = `{"turn_id":"older","ordinal":0,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-17T00:00:00Z","steps":[],"items":[]}`
	page := fmt.Sprintf(`{"type":"turns","has_more":false,"turns":[%s,{"turn_id":"current","ordinal":1,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[{%s,"status":"running","stage":"executing","output":"one"},{%s,"text":"new"}]}]}`, previous, tool, answer)
	for range 2 {
		rig.conversationFrame(t, page)
	}
	awaitKimiTestOutput(t, rig.agent.sink)
	rows := rig.sink.Messages()
	require.Len(t, rows, 2, "replay retains one historical divider and no new tool request")
	require.True(t, rows[1].TurnEnd)
	count, present := metadataField(t, rows[1], contracts.MessageMetadataFieldToolUses)
	require.True(t, present)
	assert.JSONEq(t, "0", string(count), "the historical turn owns no tools from the live turn")
	assert.NotContains(t, rig.sink.TurnLifecycle()[lifecycle:], "turn_active:false")
	assert.True(t, rig.agent.PublishTurnActive().Active)
	publication := rig.agent.run(kimiMainAgentID).conversation.turns["current"]
	assert.Equal(t, "Bash", publication.scope.GetSpanType(span))
	assert.Empty(t, publication.results, "the historical divider cannot close the live tool")
	items := rig.agent.run(kimiMainAgentID).conversation.document.turns["current"].items
	assert.EqualValues(t, 3, items["held"].output.total)
	assert.Equal(t, "one", items["held"].output.text)

	rig.conversationFrame(t, `{"type":"item.delta","turn_id":"current","item_id":"answer","field":"text","delta":"!"}`)
	rig.conversationFrame(t, `{"type":"item.ended","turn_id":"current","item":{`+tool+`,"status":"ended","outcome":"completed","output":"one"}}`)
	rig.endPublicTurn(t, kimiMainAgentID, "current", 1, "completed")
	awaitKimiTestOutput(t, rig.agent.sink)
	rows = rig.sink.Messages()
	require.Len(t, rows, 5)
	assert.True(t, rows[2].Closing)
	assert.Equal(t, span, rows[2].SpanID)
	text, err := decodeKimiConversationRecord(rows[3].Content)
	require.NoError(t, err)
	require.NotNil(t, text.item)
	require.NotNil(t, text.item.Text)
	assert.Equal(t, "new!", *text.item.Text, "the later delta appends to the snapshot base")
	require.True(t, rows[4].TurnEnd)
	count, present = metadataField(t, rows[4], contracts.MessageMetadataFieldToolUses)
	require.True(t, present)
	assert.JSONEq(t, "1", string(count))
	events := rig.sink.TurnLifecycle()
	assert.Equal(t, []string{"turn_end", "turn_active:false"}, events[len(events)-2:], "the divider supplies the tool count before the idle update")
}

func TestKimiConversationSnapshotKeepsTheCurrentChildBusy(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	const nativeChildID = "agent-0"
	childID := adoptNativeKimiChild(t, rig.agent, nativeChildID)
	run := rig.agent.run(nativeChildID)
	child := rig.sink.Child(childID)
	rig.startPublicTurn(t, nativeChildID, "current", 1)
	const opener = `{"type":"user_message","item_id":"opener","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","prompt_ids":["assignment"],"placement":"opener","origin":{"kind":"system"},"text":"Review the change.","attachments":[],"skills":[]}`
	rig.conversationAgentFrame(t, nativeChildID, `{"type":"item.ended","turn_id":"current","item":`+opener+`}`)
	require.NoError(t, agent.FlushPendingOutput(run.childLifetime.writer))
	require.Len(t, child.Messages(), 1)
	lifetime := run.childLifetime
	page := `{"type":"turns","has_more":false,"turns":[{"turn_id":"older","ordinal":0,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-17T00:00:00Z","steps":[],"items":[]},{"turn_id":"current","ordinal":1,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[` + opener + `]}]}`
	for range 2 {
		rig.conversationAgentFrame(t, nativeChildID, page)
	}
	require.NoError(t, agent.FlushPendingOutput(lifetime.writer))
	assert.Same(t, lifetime, run.childLifetime)
	assert.False(t, lifetime.retiring)
	current, linked := rig.agent.children.get(nativeChildID)
	require.True(t, linked)
	assert.False(t, current.ended)
	assert.True(t, rig.agent.ActiveChildTurnState(current.rowKey).Active)
	assert.ErrorIs(t, agenttest.SendChildInput(rig.agent, current.rowKey, agent.ChildInput{Content: "another prompt"}), agent.ErrAgentBusy)
	rows := child.Messages()
	require.Len(t, rows, 2, "the older divider and original child assignment are each saved once")
	assert.False(t, rows[0].TurnEnd)
	assert.True(t, rows[1].TurnEnd)
}

func TestKimiConversationSnapshotClearsAMissingActiveTurn(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.startPublicTurn(t, kimiMainAgentID, "removed", 0)
	rig.publicOpener(t, kimiMainAgentID, "removed", `{"kind":"user"}`)
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"removed","item":{"type":"assistant_message","item_id":"answer","status":"running","created_at":"2026-11-18T00:00:00Z","text":"Withdrawn answer"}}`)
	require.Equal(t, agent.TurnState{Active: true, Steerable: true}, rig.agent.PublishTurnActive())
	rig.conversationFrame(t, `{"type":"turns","turns":[],"has_more":false}`)
	awaitKimiTestOutput(t, rig.agent.sink)
	assert.Equal(t, agent.TurnState{}, rig.agent.PublishTurnActive())
	assert.Empty(t, rig.sink.Messages(), "replacing withdrawn text does not write an answer or divider")
}

func TestKimiConversationSnapshotReconcilesAnInterruptedTurnAndItsSuccessor(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.startPublicTurn(t, kimiMainAgentID, "old", 0)
	rig.publicOpener(t, kimiMainAgentID, "old", `{"kind":"user"}`)
	const tool = `{"type":"tool_call","item_id":"held","step_index":1,"status":"running","stage":"executing","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"Bash","input":{"command":"printf test"}}`
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"old","item":`+tool+`}`)
	awaitKimiTestOutput(t, rig.agent.sink)
	rows := rig.sink.Messages()
	require.Len(t, rows, 1)
	span := rows[0].SpanID
	require.NotEmpty(t, span)
	rig.conversationFrame(t, `{"type":"turns","has_more":false,"turns":[{"turn_id":"old","ordinal":0,"kind":"agent","status":"ended","outcome":"interrupted","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[`+tool+`]},{"turn_id":"new","ordinal":1,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[]}]}`)
	awaitKimiTestOutput(t, rig.agent.sink)
	rows = rig.sink.Messages()
	require.Len(t, rows, 3)
	assert.Equal(t, span, rows[1].SpanID)
	assert.True(t, rows[1].Closing)
	assert.Equal(t, agent.MessageCompletionInterrupted, rows[1].Completion)
	assert.True(t, rows[2].TurnEnd)
	view, err := decodeKimiConversationRecord(rows[2].Content)
	require.NoError(t, err)
	require.NotNil(t, view.turn)
	assert.Equal(t, "old", view.turn.TurnID)
	assert.Equal(t, agent.TurnState{Active: true}, rig.agent.PublishTurnActive())
	assert.Equal(t, "new", rig.agent.run(kimiMainAgentID).conversation.activeTurnID)
	rig.endPublicTurn(t, kimiMainAgentID, "new", 1, "completed")
	assert.Equal(t, agent.TurnState{}, rig.agent.PublishTurnActive())
}

func TestKimiConversationSnapshotHistoricalInterruptedTurnPreservesLiveOpenTool(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.startPublicTurn(t, kimiMainAgentID, "current", 1)
	const tool = `"type":"tool_call","item_id":"held","step_index":1,"created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"Bash","input":{"command":"sleep 30"}`
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"current","item":{`+tool+`,"status":"running","stage":"executing"}}`)
	awaitKimiTestOutput(t, rig.agent.sink)

	opening := rig.sink.Messages()
	require.Len(t, opening, 1)
	span := opening[0].SpanID
	require.NotEmpty(t, span)
	require.Empty(t, rig.sink.ClosedSpans())

	lifecycleBefore := len(rig.sink.TurnLifecycle())
	progressBefore := len(rig.sink.ProgressUpdates())

	const historicalInterrupted = `{"turn_id":"older","ordinal":0,"kind":"agent","status":"ended","outcome":"interrupted","started_at":"2026-11-17T00:00:00Z","steps":[],"items":[]}`
	page := fmt.Sprintf(`{"type":"turns","has_more":false,"turns":[%s,{"turn_id":"current","ordinal":1,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[{%s,"status":"running","stage":"executing"}]}]}`, historicalInterrupted, tool)
	rig.conversationFrame(t, page)
	awaitKimiTestOutput(t, rig.agent.sink)

	assert.Empty(t, rig.sink.ClosedSpans(), "replaying a historical interrupted turn must not close a live open tool")
	assert.NotContains(t, rig.sink.TurnLifecycle()[lifecycleBefore:], "reset_spans", "replaying a historical interrupted turn must not reset root spans")
	assert.NotContains(t, rig.sink.ProgressUpdates()[progressBefore:], agent.ResetProgress(), "replaying a historical interrupted turn must not reset root progress")
	conv := rig.agent.run(kimiMainAgentID).conversation
	assert.Empty(t, conv.turns["current"].results, "live tool must remain open after historical interrupted replay")
	assert.Equal(t, "Bash", rig.sink.GetSpanType(span))
	assert.NotEmpty(t, conv.openLiveSpans)

	// The live tool can still close normally when its native item.ended arrives.
	rig.conversationFrame(t, `{"type":"item.ended","turn_id":"current","item":{`+tool+`,"status":"ended","outcome":"completed","output":"done"}}`)
	rig.endPublicTurn(t, kimiMainAgentID, "current", 1, "completed")
	awaitKimiTestOutput(t, rig.agent.sink)
	assert.Equal(t, []string{span}, rig.sink.ClosedSpans())
}
