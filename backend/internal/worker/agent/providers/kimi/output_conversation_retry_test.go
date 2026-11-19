package kimi

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiPublicRetryAttemptConformance(t *testing.T) {
	t.Parallel()

	for _, tc := range []agenttest.RetryAttemptCase{
		{
			Name: "retains failed text before a successful retry", FramesPath: "testdata/retry_step.jsonl",
			Want: []agenttest.RetryAttemptText{
				{Text: "FAILED-ONE FAILED-TWO ", Completion: agent.MessageCompletionError},
				{Text: "RETRIED-ANSWER", Completion: agent.MessageCompletionComplete},
			},
		},
		{
			Name: "omits withdrawn reasoning", FramesPath: "testdata/retry_thinking_only.jsonl",
			Want:      []agenttest.RetryAttemptText{{Text: "RETRIED-ANSWER", Completion: agent.MessageCompletionComplete}},
			Discarded: []string{"DISCARDED-THOUGHT-ONE", "DISCARDED-THOUGHT-TWO"},
		},
	} {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			tc.NewAgent = func(t *testing.T, sink *agenttest.Sink) func([]byte) {
				sink.UpdateSessionID("session_1")
				a := newOfflineKimiAgent(t, sink)
				return func(frame []byte) {
					a.HandleOutput(frame)
					awaitKimiTestOutput(t, a.sink)
				}
			}
			tc.TextOf = func(row agenttest.Message) (agenttest.RetryAttemptText, bool) {
				view, err := decodeKimiConversationRecord(row.Content)
				if err != nil || view.item == nil || view.item.Type != contracts.KimiConversationItemKindAssistantMessage {
					return agenttest.RetryAttemptText{}, false
				}
				return agenttest.RetryAttemptText{Text: *view.item.Text, Completion: row.Completion}, true
			}
			agenttest.RunRetryAttemptCase(t, tc)
		})
	}
}

func TestKimiConversationRetryRetainsNativeAttemptAndResolvesItsOwnStep(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"retry-turn","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	rig.conversationFrame(t, `{"type":"turn.updated","turn_id":"retry-turn","step":{"index":1,"outcome":"failed","started_at":"2026-11-18T00:00:00Z","ended_at":"2026-11-18T00:00:01Z","finish_reason":"error"}}`)
	retry := `{"type":"turn.updated","turn_id":"retry-turn","retry":{"attempt":1,"next_attempt":2,"max_attempts":10,"delay_ms":0,"error":{"name":"status","message":"The stream failed."}}}`
	rig.conversationFrame(t, retry)
	awaitKimiTestOutput(t, rig.agent.sink)
	require.Equal(t, 1, rig.sink.NotificationCount(), "the native retry state needs its own retained notification")
	rig.conversationFrame(t, retry)
	awaitKimiTestOutput(t, rig.agent.sink)
	assert.Equal(t, 1, rig.sink.NotificationCount(), "the same native step/next-attempt view cannot publish twice")
	rig.conversationFrame(t, `{"type":"turn.updated","turn_id":"retry-turn"}`)
	assert.Empty(t, rig.sink.Notifications(), "clearing retry wait alone does not prove API success")
	rig.conversationFrame(t, `{"type":"turn.updated","turn_id":"retry-turn","step":{"index":1,"outcome":"completed","started_at":"2026-11-18T00:00:02Z","ended_at":"2026-11-18T00:00:03Z","finish_reason":"end_turn"}}`)
	awaitKimiTestOutput(t, rig.agent.sink)
	require.Len(t, rig.sink.Notifications(), 1)
	assert.Equal(t, "api_retry_resolved", rig.sink.Notifications()[0]["type"])
}

func TestKimiConversationRetryResolutionRetainsItsTurnAndStep(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"retry-turn","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	rig.conversationFrame(t, `{"type":"turn.updated","turn_id":"retry-turn","step":{"index":1,"outcome":"failed","started_at":"2026-11-18T00:00:00Z","finish_reason":"error"},"retry":{"attempt":1,"next_attempt":2,"max_attempts":10,"delay_ms":0,"error":{"name":"status","message":"Failed"}}}`)
	awaitKimiTestOutput(t, rig.agent.sink)
	require.Equal(t, 1, rig.sink.NotificationCount())
	rig.conversationFrame(t, `{"type":"turn.updated","turn_id":"retry-turn","step":{"index":2,"outcome":"completed","started_at":"2026-11-18T00:00:02Z"}}`)
	rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"other-turn","ordinal":1,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:03Z","steps":[]}}`)
	rig.conversationFrame(t, `{"type":"turn.updated","turn_id":"other-turn","step":{"index":1,"outcome":"completed","started_at":"2026-11-18T00:00:03Z"}}`)
	awaitKimiTestOutput(t, rig.agent.sink)
	assert.Empty(t, rig.sink.Notifications(), "another turn or step cannot resolve the failed request")
	rig.conversationFrame(t, `{"type":"turn.updated","turn_id":"retry-turn","step":{"index":1,"outcome":"interrupted","started_at":"2026-11-18T00:00:02Z"}}`)
	awaitKimiTestOutput(t, rig.agent.sink)
	assert.Empty(t, rig.sink.Notifications(), "interruption cannot declare API recovery")
}

func TestKimiConversationRetryResolvesBeforeItsSuccessfulAnswer(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"retry-turn","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	rig.conversationFrame(t, `{"type":"turn.updated","turn_id":"retry-turn","step":{"index":1,"outcome":"failed","started_at":"2026-11-18T00:00:00Z"},"retry":{"attempt":1,"next_attempt":2,"max_attempts":10,"delay_ms":0,"error":{"name":"status","message":"Failed"}}}`)
	rig.conversationFrame(t, `{"type":"turn.updated","turn_id":"retry-turn"}`)
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"retry-turn","item":{"type":"assistant_message","item_id":"answer:1","step_index":1,"status":"running","created_at":"2026-11-18T00:00:02Z","text":""}}`)
	awaitKimiTestOutput(t, rig.agent.sink)
	assert.Empty(t, rig.sink.Notifications(), "an empty placeholder does not prove model output")
	rig.conversationFrame(t, `{"type":"item.delta","turn_id":"retry-turn","item_id":"answer:1","field":"text","delta":"Recovered answer"}`)
	awaitKimiTestOutput(t, rig.agent.sink)
	require.Len(t, rig.sink.Notifications(), 1, "the retry resolves before the answer breaks notification adjacency")
	assert.Equal(t, "api_retry_resolved", rig.sink.Notifications()[0]["type"])
}

func TestKimiConversationRetryCompletionDoesNotRelabelRemovedAttempt(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"retry-turn","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	rig.conversationFrame(t, `{"type":"item.ended","turn_id":"retry-turn","item":{"type":"assistant_message","item_id":"failed:1","step_index":1,"status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"partial "}}`)
	rig.conversationFrame(t, `{"type":"turn.updated","turn_id":"retry-turn","step":{"index":1,"outcome":"failed","started_at":"2026-11-18T00:00:00Z","finish_reason":"error"},"retry":{"attempt":1,"next_attempt":2,"max_attempts":10,"delay_ms":0,"error":{"name":"status","message":"Failed"}}}`)
	awaitKimiTestOutput(t, rig.agent.sink)
	require.Len(t, rig.sink.Messages(), 1)
	require.Equal(t, agent.MessageCompletionError, rig.sink.Messages()[0].Completion)
	rig.conversationFrame(t, `{"type":"item.removed","turn_id":"retry-turn","item_id":"failed:1"}`)
	rig.conversationFrame(t, `{"type":"turn.updated","turn_id":"retry-turn"}`)
	rig.conversationFrame(t, `{"type":"item.ended","turn_id":"retry-turn","item":{"type":"assistant_message","item_id":"success:1","step_index":1,"status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:01Z","text":"Recovered answer"}}`)
	rig.conversationFrame(t, `{"type":"turn.updated","turn_id":"retry-turn","step":{"index":1,"outcome":"completed","started_at":"2026-11-18T00:00:01Z","finish_reason":"end_turn"}}`)
	awaitKimiTestOutput(t, rig.agent.sink)
	rows := rig.sink.Messages()
	require.Len(t, rows, 2)
	assert.Equal(t, agent.MessageCompletionError, rows[0].Completion, "the removed attempt keeps its own failed outcome")
	assert.Equal(t, agent.MessageCompletionComplete, rows[1].Completion)
}

func TestKimiConversationRetryKeepsEachNewAttemptOnce(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"retry-turn","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	for _, attempt := range []int{1, 2, 1, 2} {
		rig.conversationFrame(t, fmt.Sprintf(`{"type":"turn.updated","turn_id":"retry-turn","step":{"index":1,"outcome":"failed","started_at":"2026-11-18T00:00:00Z"},"retry":{"attempt":%d,"next_attempt":%d,"max_attempts":10,"delay_ms":0,"error":{"name":"status","message":"Failed"}}}`, attempt, attempt+1))
	}
	awaitKimiTestOutput(t, rig.agent.sink)
	require.Equal(t, 2, rig.sink.NotificationCount())
	rows := rig.sink.PersistedNotifications()
	first, err := decodeKimiConversationRecord(rows[0].Content)
	require.NoError(t, err)
	second, err := decodeKimiConversationRecord(rows[1].Content)
	require.NoError(t, err)
	assert.NotEqual(t, first.key(), second.key())
	for index, row := range rows {
		view, err := decodeKimiConversationRecord(row.Content)
		require.NoError(t, err)
		require.NotNil(t, view.retry)
		assert.Equal(t, int64(index+2), view.retry.NextAttempt)
		assert.Equal(t, rig.sessionID(), view.record.SessionID)
		assert.Equal(t, "main", view.record.AgentID)
		assert.Equal(t, "retry-turn", view.record.TurnID)
		assert.Equal(t, int64(1), view.record.StepIndex)
		assert.Equal(t, agent.APIRetryNotification(), (kimiProvider{}).Classify(row.Content))
	}
}

func TestKimiConversationRetryRecordRejectsIncompleteOwnershipAndState(t *testing.T) {
	t.Parallel()
	base := contracts.KimiConversationRecord{Type: contracts.KimiConversationRecordKindRetry,
		SessionID: "native", AgentID: "main", TurnID: "turn-1", StepIndex: 1,
		Retry: json.RawMessage(`{"attempt":1,"next_attempt":2,"max_attempts":10,"delay_ms":0,"error":{"name":"status","message":"Failed"}}`)}
	for _, field := range []string{"session", "agent", "turn", "step-zero", "step-negative", "retry", "role", "item", "context", "attempt", "delay", "error"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			record := base
			switch field {
			case "session":
				record.SessionID = ""
			case "agent":
				record.AgentID = ""
			case "turn":
				record.TurnID = ""
			case "step-zero":
				record.StepIndex = 0
			case "step-negative":
				record.StepIndex = -1
			case "retry":
				record.Retry = json.RawMessage(`null`)
			case "role":
				record.Role = contracts.KimiConversationRecordRoleRequest
			case "item":
				record.Item = json.RawMessage(`{}`)
			case "context":
				record.TurnContext = json.RawMessage(`{}`)
			case "attempt":
				record.Retry = json.RawMessage(`{"attempt":0,"next_attempt":2,"max_attempts":10,"delay_ms":0,"error":{"name":"status","message":"Failed"}}`)
			case "delay":
				record.Retry = json.RawMessage(`{"attempt":1,"next_attempt":2,"max_attempts":10,"error":{"name":"status","message":"Failed"}}`)
			case "error":
				record.Retry = json.RawMessage(`{"attempt":1,"next_attempt":2,"max_attempts":10,"delay_ms":0,"error":{"name":"status"}}`)
			}
			raw, err := json.Marshal(record)
			require.NoError(t, err)
			_, err = decodeKimiConversationRecord(raw)
			require.Error(t, err)
			assert.Empty(t, (kimiProvider{}).Classify(raw))
		})
	}
}

func TestKimiConversationRetryRestoresPendingRetryFromSnapshot(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	page := `{"type":"turns","has_more":false,"turns":[{"turn_id":"retry-turn","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[{"index":1,"outcome":"failed","started_at":"2026-11-18T00:00:00Z","ended_at":"2026-11-18T00:00:01Z","finish_reason":"error"}],"retry":{"attempt":1,"next_attempt":2,"max_attempts":10,"delay_ms":0,"error":{"name":"status","message":"The stream failed."}},"items":[]}]}`
	rig.conversationFrame(t, page)
	awaitKimiTestOutput(t, rig.agent.sink)
	require.Equal(t, 1, rig.sink.NotificationCount(), "a restored native retry must remain visible")
	rig.conversationFrame(t, page)
	awaitKimiTestOutput(t, rig.agent.sink)
	assert.Equal(t, 1, rig.sink.NotificationCount(), "repeated pages retain one exact retry")
	rig.conversationFrame(t, `{"type":"turn.updated","turn_id":"retry-turn","step":{"index":1,"outcome":"completed","started_at":"2026-11-18T00:00:02Z","ended_at":"2026-11-18T00:00:03Z","finish_reason":"end_turn"}}`)
	awaitKimiTestOutput(t, rig.agent.sink)
	require.Len(t, rig.sink.Notifications(), 1)
	assert.Equal(t, "api_retry_resolved", rig.sink.Notifications()[0]["type"])
}

func TestKimiPublicChildRetryIgnoresWithdrawnReasoningAndKeepsItsOwnOutput(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	childID := adoptNativeKimiChild(t, rig.agent, "agent-1")
	rig.startPublicTurn(t, kimiMainAgentID, "parent", 0)
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"parent","item":{"type":"assistant_message","item_id":"root-answer","status":"running","created_at":"2026-11-18T00:00:00Z","text":"Root answer."}}`)
	rig.startPublicTurn(t, "agent-1", "child", 0)
	feed := func(raw string) { rig.conversationAgentFrame(t, "agent-1", raw) }
	feed(`{"type":"item.started","turn_id":"child","item":{"type":"reasoning","item_id":"thought","step_index":1,"status":"running","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","text":"Unsigned thought."}}`)
	feed(`{"type":"item.removed","turn_id":"child","item_id":"thought"}`)
	feed(`{"type":"turn.updated","turn_id":"child","step":{"index":1,"outcome":"failed","started_at":"2026-11-18T00:00:00Z"},"retry":{"attempt":1,"next_attempt":2,"max_attempts":10,"delay_ms":0,"error":{"name":"status","message":"Failed"}}}`)
	feed(`{"type":"turn.updated","turn_id":"child"}`)
	feed(`{"type":"item.ended","turn_id":"child","item":{"type":"assistant_message","item_id":"answer","step_index":1,"status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:01Z","text":"Child answer."}}`)
	feed(`{"type":"turn.updated","turn_id":"child","step":{"index":1,"outcome":"completed","started_at":"2026-11-18T00:00:01Z"}}`)
	rig.endPublicTurn(t, "agent-1", "child", 0, "completed")
	awaitKimiTestOutput(t, rig.agent.sink)

	child := rig.sink.Child(childID)
	rows := child.Messages()
	require.Len(t, rows, 2)
	view, err := decodeKimiConversationRecord(rows[0].Content)
	require.NoError(t, err)
	require.NotNil(t, view.item)
	require.NotNil(t, view.item.Text)
	assert.Equal(t, "Child answer.", *view.item.Text)
	assert.Equal(t, agent.MessageCompletionComplete, rows[0].Completion)
	assert.True(t, rows[1].TurnEnd)
	assert.Len(t, child.PersistedNotifications(), 1)
	resolved := child.LeapMuxNotifications()
	require.Len(t, resolved, 1)
	assert.Equal(t, contracts.NotificationTypeApiRetryResolved, resolved[0][contracts.NotificationFieldType])
	assert.Empty(t, rig.sink.Messages())
	assert.Empty(t, rig.sink.PersistedNotifications())
	assert.True(t, rig.agent.PublishTurnActive().Active)
	rig.endPublicTurn(t, kimiMainAgentID, "parent", 0, "completed")
	awaitKimiTestOutput(t, rig.agent.sink)
	parent := rig.sink.Messages()
	require.Len(t, parent, 2)
	view, err = decodeKimiConversationRecord(parent[0].Content)
	require.NoError(t, err)
	require.NotNil(t, view.item)
	require.NotNil(t, view.item.Text)
	assert.Equal(t, "Root answer.", *view.item.Text)
}
