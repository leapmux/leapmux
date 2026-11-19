package kimi

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const kimiConversationTestTurn = `{"turn_id":"turn-1","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[]}`

func TestKimiFrameRetainsConversationPackets(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`{"type":"turns","turns":[],"has_more":false}`,
		`{"type":"notice","level":"warning","title":"Compaction failed","code":"context.compaction_failed"}`,
		`{"type":"item.delta","turn_id":"turn-1","item_id":"answer-1","field":"text","delta":"A🙂한"}`,
	} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			encoded := kimiConversationTestFrame(t, "session_1", "main", raw)
			var frame kimiFrame
			require.NoError(t, json.Unmarshal(encoded, &frame))
			assert.JSONEq(t, string(encoded), string(frame.Raw))
		})
	}
}

func TestKimiConversationWireAcceptsNativeFrames(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`{"type":"turns","turns":[],"has_more":false}`,
		`{"type":"turns","turns":[` + kimiConversationTestTurn + `],"has_more":false}`,
		`{"type":"turn.started","turn":{"turn_id":"turn-1","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`,
		`{"type":"turn.updated","turn_id":"turn-1"}`,
		`{"type":"turn.updated","turn_id":"turn-1","retry":{"attempt":1,"next_attempt":2,"max_attempts":10,"delay_ms":0,"error":{"name":"","message":""}}}`,
		`{"type":"item.updated","turn_id":"turn-1","item_id":"call-1","set":{}}`,
		`{"type":"item.delta","turn_id":"turn-1","item_id":"answer-1","field":"text","delta":""}`,
		`{"type":"item.removed","turn_id":"turn-1","item_id":"answer-1"}`,
		`{"type":"turns.removed","turn_ids":[]}`,
		`{"type":"turns.failed","code":0,"msg":""}`,
		`{"type":"notice","level":"warning","title":"","code":"context.compaction_failed"}`,
	} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			frame, err := decodeKimiConversationFrame(kimiConversationTestFrame(t, "session_1", "main", raw))
			require.NoError(t, err)
			assert.Equal(t, "session_1", frame.SessionID)
			assert.Equal(t, "main", frame.AgentID)
		})
	}
}

func TestKimiConversationWirePreservesFractionalAndZeroUsage(t *testing.T) {
	t.Parallel()
	raw := `{"type":"turn.started","turn":{"turn_id":"turn-1","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[],"usage":{"input_other":0.5,"output":0,"input_cache_read":0,"input_cache_creation":0}}}`
	frame, err := decodeKimiConversationFrame(kimiConversationTestFrame(t, "session_1", "main", raw))
	require.NoError(t, err)
	require.NotNil(t, frame.Turn.Usage)
	assert.Equal(t, 0.5, *frame.Turn.Usage.InputOther)
	assert.Zero(t, *frame.Turn.Usage.Output)
}

func TestKimiConversationWireChecksTheNativeEndReason(t *testing.T) {
	t.Parallel()
	for _, reason := range []string{"user_cancelled", "aborted", "max_steps", "error", "filtered", "blocked", "unknown-native-reason"} {
		t.Run(reason, func(t *testing.T) {
			t.Parallel()
			raw, err := json.Marshal(map[string]any{
				"type": "turn.ended",
				"turn": map[string]any{"turn_id": "turn-1", "ordinal": 0, "kind": "agent", "status": "ended", "outcome": "completed", "started_at": "2026-11-18T00:00:00Z", "steps": []any{}, "end_reason": reason},
			})
			require.NoError(t, err)
			_, err = decodeKimiConversationFrame(kimiConversationTestFrame(t, "session_1", "main", string(raw)))
			if reason == "unknown-native-reason" {
				require.ErrorContains(t, err, "end reason")
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestKimiConversationWireAcceptsNativeItemsAndZeroValues(t *testing.T) {
	t.Parallel()
	for _, item := range []string{
		`{"type":"user_message","item_id":"prompt-1","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","prompt_ids":[],"placement":"opener","origin":{"kind":"user"},"text":"","attachments":[],"skills":[]}`,
		`{"type":"assistant_message","item_id":"answer-1","status":"ended","created_at":"2026-11-18T00:00:00Z","text":""}`,
		`{"type":"reasoning","item_id":"thought-1","status":"ended","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","duration_ms":0,"text":""}`,
		`{"type":"tool_call","item_id":"call-1","step_index":1,"status":"running","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"","stage":"input","input":{"zero":0,"disabled":false,"empty":""},"input_text":"","output":""}`,
		`{"type":"notification","item_id":"notice-1","status":"ended","created_at":"2026-11-18T00:00:00Z","notification_id":"","title":"","body":"","severity":"info","notification_type":"","source":{"kind":"","id":""},"output":{"bytes":0,"truncated":false},"answer":{"answers":{"one":"","two":true}}}`,
		`{"type":"system","item_id":"compact-1","status":"ended","created_at":"2026-11-18T00:00:00Z","kind":"compaction","trigger":"manual","tokens_before":0,"tokens_after":0,"compacted_count":0}`,
		`{"type":"system","item_id":"fork-1","status":"ended","created_at":"2026-11-18T00:00:00Z","kind":"fork_boundary","source":"fork","forked_from":{"agent_id":"agent-0"}}`,
	} {
		t.Run(item, func(t *testing.T) {
			t.Parallel()
			raw := `{"type":"item.started","turn_id":"turn-1","item":` + item + `}`
			frame, err := decodeKimiConversationFrame(kimiConversationTestFrame(t, "session_1", "main", raw))
			require.NoError(t, err)
			require.NotNil(t, frame.Item)
			assert.NotEmpty(t, frame.Item.ItemID)
			if frame.Item.Type == "tool_call" {
				assert.JSONEq(t, `{"zero":0,"disabled":false,"empty":""}`, string(frame.Item.Input))
				assert.JSONEq(t, `""`, string(frame.Item.Output))
			}
		})
	}
}

func TestKimiConversationWireRejectsIncompleteOrConflictingFrames(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`{"type":"turns","turns":[]}`,
		`{"type":"turns","has_more":false}`,
		`{"type":"turns","turns":[],"has_more":true}`,
		`{"type":"turns","turns":[` + kimiConversationTestTurn + `,` + kimiConversationTestTurn + `],"has_more":false}`,
		`{"type":"turns","turns":[{"turn_id":"turn-1","ordinal":0,"kind":"agent","status":"ended","started_at":"2026-11-18T00:00:00Z","steps":[]}],"has_more":false}`,
		`{"type":"turn.started","turn":{"turn_id":"turn-1","ordinal":-1,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`,
		`{"type":"turn.started","turn":{"turn_id":"turn-1","ordinal":1e100,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`,
		`{"type":"turn.ended","turn":{"turn_id":"turn-1","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`,
		`{"type":"turn.updated","turn_id":"turn-1","retry":{"attempt":1,"next_attempt":2,"max_attempts":10,"error":{"name":"APIError","message":"Retry."}}}`,
		`{"type":"item.delta","turn_id":"turn-1","item_id":"answer-1","field":"text"}`,
		`{"type":"item.delta","turn_id":"turn-1","item_id":"answer-1","field":"status","delta":"ended"}`,
		`{"type":"item.updated","turn_id":"turn-1","item_id":"call-1","set":{"item_id":"another"}}`,
		`{"type":"item.updated","turn_id":"turn-1","item_id":"call-1","set":{"type":"assistant_message"}}`,
		`{"type":"turns.failed","code":null,"msg":"Missing."}`,
		`{"type":"notice","level":"warning","title":"Notice","code":1}`,
		`{"type":"turn.started","event_created_at":"invalid","turn":` + kimiConversationTestTurn + `}`,
	} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			_, err := decodeKimiConversationFrame(kimiConversationTestFrame(t, "session_1", "main", raw))
			require.Error(t, err)
		})
	}
}

func TestKimiConversationWireRejectsInvalidNestedItems(t *testing.T) {
	t.Parallel()
	for _, extra := range []string{
		`"type":"user_message","text":"","prompt_ids":[],"placement":"opener","origin":null,"attachments":[],"skills":[]`,
		`"type":"user_message","text":"","prompt_ids":[],"placement":"opener","origin":{"kind":"unknown"},"attachments":[],"skills":[]`,
		`"type":"user_message","text":"","prompt_ids":[],"placement":"opener","origin":{"kind":"user"},"attachments":[{"kind":"image","ordinal":0}],"skills":[]`,
		`"type":"tool_call","name":"Read","started_at":"2026-11-18T00:00:00Z","output":[]`,
		`"type":"notification","notification_id":"notice","title":"","body":"","severity":"info","notification_type":"task","source":null`,
		`"type":"notification","notification_id":"notice","title":"","body":"","severity":"info","notification_type":"task","source":{},"output":{"bytes":-1}`,
		`"type":"notification","notification_id":"notice","title":"","body":"","severity":"info","notification_type":"task","source":{"kind":"task","id":"one"},"answer":{"answers":{"one":false}}`,
	} {
		t.Run(extra, func(t *testing.T) {
			t.Parallel()
			item := `{"item_id":"item-1","status":"ended","created_at":"2026-11-18T00:00:00Z",` + extra + `}`
			var parsed kimiConversationItem
			require.NoError(t, json.Unmarshal([]byte(item), &parsed))
			require.Error(t, parsed.validate())
		})
	}
}
