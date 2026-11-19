package kimi

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiConversationBlockedPromptKeepsExactTurnContext(t *testing.T) {
	t.Parallel()
	for _, endedFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("ended_first=%t", endedFirst), func(t *testing.T) {
			t.Parallel()
			rig := newKimiConversationRig(t)
			header := `{"turn_id":"blocked-turn","ordinal":0,"kind":"agent","status":"ended","outcome":"failed","end_reason":"blocked","started_at":"2026-11-18T00:00:00Z","steps":[]}`
			if endedFirst {
				rig.conversationFrame(t, `{"type":"turn.started","turn":`+header+`}`)
			} else {
				rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"blocked-turn","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
			}
			rig.conversationFrame(t, `{"type":"item.ended","turn_id":"blocked-turn","item":{"type":"assistant_message","item_id":"hook-result","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"<hook_result hook_event=\"UserPromptSubmit\">\nNative rejection reason\n</hook_result>"}}`)
			before := rig.sink.Messages()
			if !endedFirst {
				rig.conversationFrame(t, `{"type":"turn.ended","turn":`+header+`}`)
			}
			awaitKimiTestOutput(t, rig.agent.sink)
			var found bool
			for _, message := range rig.sink.Messages() {
				var record map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(message.Content, &record))
				if len(record["item"]) == 0 {
					continue
				}
				found = true
				if len(message.SupplementalContent) != 0 {
					var supplement map[string]json.RawMessage
					require.NoError(t, json.Unmarshal(message.SupplementalContent, &supplement))
					if latest := supplement["kimi_conversation"]; len(latest) != 0 {
						require.NoError(t, json.Unmarshal(latest, &record))
					}
				}
				var context map[string]any
				require.NoError(t, json.Unmarshal(record["turn_context"], &context), "the native item must retain its explicit turn context")
				assert.Equal(t, "blocked-turn", context["turn_id"])
				assert.Equal(t, "blocked", context["end_reason"])
				assert.Equal(t, "failed", context["outcome"])
				assert.Equal(t, "ended", context["status"])
			}
			require.True(t, found)
			if !endedFirst {
				assert.Equal(t, before[0].Content, rig.sink.Messages()[0].Content, "turn completion enriches the item without rewriting its original")
			}
		})
	}
}
