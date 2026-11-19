package kimi

import (
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiPublicCompleteResultReconstructsItsOwnOpening(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.startPublicTurn(t, kimiMainAgentID, "t4", 0)
	rig.conversationFrame(t, `{"type":"item.ended","turn_id":"t4","item":{"type":"tool_call","item_id":"call_old","step_index":1,"name":"Read","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","input":{"path":"a.go"},"output":"done"}}`)
	rig.endPublicTurn(t, kimiMainAgentID, "t4", 0, "completed")
	awaitKimiTestOutput(t, rig.agent.sink)

	rows := rig.sink.Messages()
	require.Len(t, rows, 3)
	req, err := decodeKimiConversationRecord(rows[0].Content)
	require.NoError(t, err)
	require.NotNil(t, req.item)
	assert.Equal(t, contracts.KimiConversationRecordRoleRequest, req.record.Role)
	assert.JSONEq(t, `{"path":"a.go"}`, string(req.item.Input))
	assert.Empty(t, req.item.Output, "the reconstructed request cannot borrow result output")
	result, err := decodeKimiConversationRecord(rows[1].Content)
	require.NoError(t, err)
	require.NotNil(t, result.item)
	assert.Equal(t, contracts.KimiConversationRecordRoleResult, result.record.Role)
	assert.JSONEq(t, `"done"`, string(result.item.Output))
	assert.Equal(t, req.item.ItemID, result.item.ItemID)
	assert.NotEmpty(t, rows[0].SpanID)
	assert.Equal(t, rows[0].SpanID, rows[1].SpanID)
	assert.True(t, rows[1].Closing)
	require.True(t, rows[2].TurnEnd)
	count, present := metadataField(t, rows[2], contracts.MessageMetadataFieldToolUses)
	require.True(t, present)
	assert.JSONEq(t, "1", string(count))
}

func TestKimiPublicRepeatedCallIDKeepsDistinctTurnSpans(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	for ordinal, turnID := range []string{"t0", "t1"} {
		rig.startPublicTurn(t, kimiMainAgentID, turnID, ordinal)
		item := `{"type":"item.ended","turn_id":"TURN","item":{"type":"tool_call","item_id":"call_1","step_index":1,"name":"Read","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","input":{},"output":"ok"}}`
		rig.conversationFrame(t, strings.Replace(item, "TURN", turnID, 1))
		rig.endPublicTurn(t, kimiMainAgentID, turnID, ordinal, "completed")
	}
	awaitKimiTestOutput(t, rig.agent.sink)
	rows := rig.sink.Messages()
	require.Len(t, rows, 6)
	var spans []string
	for i, turnID := range []string{"t0", "t1"} {
		start, end := rows[3*i], rows[3*i+1]
		for j, role := range []string{contracts.KimiConversationRecordRoleRequest, contracts.KimiConversationRecordRoleResult} {
			record, err := decodeKimiConversationRecord(rows[3*i+j].Content)
			require.NoError(t, err)
			assert.Equal(t, turnID, record.record.TurnID)
			assert.Equal(t, role, record.record.Role)
			require.NotNil(t, record.item)
			assert.Equal(t, "call_1", record.item.ItemID)
		}
		assert.True(t, end.Closing)
		assert.Equal(t, start.SpanID, end.SpanID)
		assert.NotEmpty(t, start.SpanID)
		spans = append(spans, start.SpanID)
	}
	assert.NotEqual(t, spans[0], spans[1], "a repeated call ID cannot reuse an older span")
}

func TestKimiPublicInterruptedTurnClosesOpenSpansInStartOrder(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.startPublicTurn(t, kimiMainAgentID, "t0", 0)
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":{"type":"tool_call","item_id":"first","step_index":1,"name":"Bash","status":"running","stage":"executing","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","input":{"command":"sleep 1"}}}`)
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":{"type":"tool_call","item_id":"second","step_index":1,"name":"Grep","status":"running","stage":"executing","created_at":"2026-11-18T00:00:01Z","started_at":"2026-11-18T00:00:01Z","input":{"pattern":"x"}}}`)
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":{"type":"assistant_message","item_id":"answer","status":"running","created_at":"2026-11-18T00:00:02Z","text":"partial"}}`)
	rig.endPublicTurn(t, kimiMainAgentID, "t0", 0, "interrupted")
	awaitKimiTestOutput(t, rig.agent.sink)
	rows := rig.sink.Messages()
	require.Len(t, rows, 6)
	var closed []string
	var keptAnswer bool
	for _, row := range rows[:len(rows)-1] {
		view, err := decodeKimiConversationRecord(row.Content)
		require.NoError(t, err)
		require.NotNil(t, view.item)
		if row.Closing {
			closed = append(closed, row.SpanID)
			assert.Equal(t, agent.MessageCompletionInterrupted, row.Completion)
			assert.Equal(t, contracts.KimiConversationRecordRoleResult, view.record.Role)
			assert.Equal(t, "running", view.item.Status, "local completion must not fabricate a native result")
		}
		if view.item.ItemID == "answer" {
			keptAnswer = true
			require.NotNil(t, view.item.Text)
			assert.Equal(t, "partial", *view.item.Text)
			assert.Equal(t, agent.MessageCompletionInterrupted, row.Completion)
		}
	}
	require.True(t, keptAnswer)
	assert.Equal(t, []string{rows[0].SpanID, rows[1].SpanID}, closed)
	assert.True(t, rows[5].TurnEnd)
}

func TestKimiPublicToolWithoutItemIDDoesNotOpenASpan(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.startPublicTurn(t, kimiMainAgentID, "t0", 0)
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":{"type":"tool_call","item_id":"","name":"Bash","status":"running","stage":"executing","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","input":{}}}`)
	awaitKimiTestOutput(t, rig.agent.sink)
	assert.Empty(t, rig.sink.Messages())
	assert.Empty(t, rig.sink.ClosedSpans())
	assert.True(t, rig.agent.PublishTurnActive().Active)
}
