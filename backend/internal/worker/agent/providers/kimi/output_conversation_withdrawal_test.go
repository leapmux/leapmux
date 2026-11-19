package kimi

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiConversationWithdrawalClosesOnlyTheRemovedOpenSpan(t *testing.T) {
	t.Parallel()
	for _, event := range []string{"complete snapshot", "item removed", "turn removed"} {
		t.Run(event, func(t *testing.T) {
			t.Parallel()
			rig := newKimiConversationRig(t)
			header := func(id string, ordinal int) string {
				return fmt.Sprintf(`{"turn_id":%q,"ordinal":%d,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}`, id, ordinal)
			}
			item := func(id string) string {
				return fmt.Sprintf(`{"type":"tool_call","item_id":%q,"step_index":1,"status":"running","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"Bash","stage":"executing","input":{"command":"run the held command"}}`, id)
			}
			start := func(turn string, ordinal int) {
				rig.conversationFrame(t, `{"type":"turn.started","turn":`+header(turn, ordinal)+`}`)
			}
			open := func(turn, id string) {
				rig.conversationFrame(t, fmt.Sprintf(`{"type":"item.started","turn_id":%q,"item":%s}`, turn, item(id)))
			}
			start("first", 0)
			open("first", "removed")
			rig.conversationFrame(t, `{"type":"item.delta","turn_id":"first","item_id":"removed","field":"output","delta":"partial output"}`)
			keptTurn := "first"
			if event == "turn removed" {
				keptTurn = "second"
				start(keptTurn, 1)
			}
			open(keptTurn, "kept")
			awaitKimiTestOutput(t, rig.agent.sink)
			before := rig.sink.Messages()
			require.Len(t, before, 2)
			require.NotEqual(t, before[0].SpanID, before[1].SpanID)
			switch event {
			case "complete snapshot":
				var turn map[string]any
				require.NoError(t, json.Unmarshal([]byte(header("first", 0)), &turn))
				turn["items"] = []json.RawMessage{json.RawMessage(item("kept"))}
				raw, err := json.Marshal(turn)
				require.NoError(t, err)
				rig.agent.HandleOutput(kimiConversationTestPage(t, rig.sessionID(), "main", raw))
			case "item removed":
				rig.conversationFrame(t, `{"type":"item.removed","turn_id":"first","item_id":"removed"}`)
			case "turn removed":
				rig.conversationFrame(t, `{"type":"turns.removed","turn_ids":["first"]}`)
			}
			awaitKimiTestOutput(t, rig.agent.sink)
			rows := rig.sink.Messages()
			require.Equal(t, 3, len(rows), "the removed span has one closing record")
			closing := rows[2]
			assert.True(t, closing.Closing)
			assert.Equal(t, before[0].SpanID, closing.SpanID)
			assert.Equal(t, agent.MessageCompletionInterrupted, closing.Completion)
			view, err := decodeKimiConversationRecord(closing.Content)
			require.NoError(t, err)
			require.NotNil(t, view.item)
			assert.Equal(t, contracts.KimiConversationRecordRoleResult, view.record.Role)
			assert.Equal(t, "first", view.record.TurnID)
			assert.Equal(t, "removed", view.item.ItemID)
			assert.Equal(t, contracts.KimiConversationStatusRunning, view.item.Status)
			assert.JSONEq(t, `"partial output"`, string(view.item.Output))
			assert.NotContains(t, rig.sink.ClosedSpans(), before[1].SpanID)
			assert.True(t, rig.agent.PublishTurnActive().Active)

			rig.conversationFrame(t, fmt.Sprintf(`{"type":"item.ended","turn_id":%q,"item":{"type":"tool_call","item_id":"kept","step_index":1,"status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"Bash","input":{"command":"run the held command"},"output":"kept tool finished"}}`, keptTurn))
			awaitKimiTestOutput(t, rig.agent.sink)
			rows = rig.sink.Messages()
			require.Len(t, rows, 4)
			assert.Equal(t, before[1].SpanID, rows[3].SpanID)
			assert.True(t, rows[3].Closing)
			assert.Equal(t, agent.MessageCompletionComplete, rows[3].Completion)
		})
	}
}
