package kimi

import (
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const kimiCompletionFixtureStart = `{"type":"turn.started","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`

func TestKimiNativeCompletionCancelsRetryWithoutRearmingFromALateNotice(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"completed", "interrupted"} {
		t.Run(outcome, func(t *testing.T) {
			t.Parallel()
			rig := newKimiConversationRig(t)
			rig.conversationFrame(t, kimiCompletionFixtureStart)
			rig.conversationFrame(t, `{"type":"turn.ended","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"ended","outcome":"failed","started_at":"2026-11-18T00:00:00Z","steps":[],"error":{"code":"overloaded","message":"Model busy","retryable":true}}}`)
			awaitKimiTestOutput(t, rig.agent.sink)
			require.Equal(t, 1, rig.sink.AutoScheduleCount())
			rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"t1","ordinal":1,"kind":"agent","status":"running","started_at":"2026-11-18T00:01:00Z","steps":[]}}`)
			rig.conversationFrame(t, fmt.Sprintf(`{"type":"turn.ended","turn":{"turn_id":"t1","ordinal":1,"kind":"agent","status":"ended","outcome":%q,"started_at":"2026-11-18T00:01:00Z","steps":[]}}`, outcome))
			const notice = `{"type":"notice","level":"error","title":"Model busy","code":"overloaded"}`
			rig.conversationFrame(t, notice)
			rig.conversationFrame(t, notice)
			awaitKimiTestOutput(t, rig.agent.sink)
			assert.Equal(t, 1, rig.sink.AutoScheduleCount())
			assert.Equal(t, 1, rig.sink.AutoCancelCount())
			assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, rig.sink.LastAutoCancel())
			assert.Len(t, rig.sink.PersistedNotifications(), 2, "the public stream has no shared delivery identity for identical notices")
		})
	}
}

func TestKimiNativeChildFailureDoesNotScheduleTheMainRetry(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	childID := adoptNativeKimiChild(t, rig.agent, "agent-0")
	rig.conversationFrame(t, kimiCompletionFixtureStart)
	rig.conversationAgentFrame(t, "agent-0", kimiCompletionFixtureStart)
	rig.conversationAgentFrame(t, "agent-0", `{"type":"turn.ended","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"ended","outcome":"failed","started_at":"2026-11-18T00:00:00Z","steps":[],"error":{"code":"overloaded","message":"Model busy","retryable":true}}}`)
	awaitKimiTestOutput(t, rig.agent.sink)
	child := rig.sink.Child(childID)
	rows := child.Messages()
	require.Len(t, rows, 1)
	require.True(t, rows[0].TurnEnd)
	view, err := decodeKimiConversationRecord(rows[0].Content)
	require.NoError(t, err)
	assert.Equal(t, "agent-0", view.record.AgentID)
	require.NotNil(t, view.turn.Error)
	require.NotNil(t, view.turn.Error.Retryable)
	assert.True(t, *view.turn.Error.Retryable)
	assert.Zero(t, child.AutoScheduleCount())
	assert.Zero(t, rig.sink.AutoScheduleCount())
	assert.Zero(t, rig.sink.AutoCancelCount())
	assert.True(t, rig.agent.PublishTurnActive().Active)
}

func TestKimiNativeTurnErrorKeepsEmptyMessageSeparateFromMissingDetails(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		errorField string
		schedules  int
	}{
		{"empty message", `,"error":{"code":"provider.overloaded","message":"","retryable":true}`, 1},
		{"absent error", "", 0},
		{"null error", `,"error":null`, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiConversationRig(t)
			rig.conversationFrame(t, kimiCompletionFixtureStart)
			rig.conversationFrame(t, fmt.Sprintf(`{"type":"turn.ended","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"ended","outcome":"failed","started_at":"2026-11-18T00:00:00Z","steps":[]%s}}`, test.errorField))
			awaitKimiTestOutput(t, rig.agent.sink)
			assert.Equal(t, test.schedules, rig.sink.AutoScheduleCount())
			assert.Equal(t, 1-test.schedules, rig.sink.AutoCancelCount())
			rows := rig.sink.Messages()
			require.Len(t, rows, 1)
			view, err := decodeKimiConversationRecord(rows[0].Content)
			require.NoError(t, err)
			if test.schedules == 0 {
				assert.Nil(t, view.turn.Error)
				return
			}
			require.NotNil(t, view.turn.Error)
			assert.Equal(t, "provider.overloaded", view.turn.Error.Code)
			assert.Empty(t, view.turn.Error.Message)
			assert.Equal(t, rows[0].Content, rig.sink.LastAutoSchedule().SourcePayload)
		})
	}
}

func TestKimiNativeTurnEndPersistsContextUsageIncludingZero(t *testing.T) {
	t.Parallel()
	for _, tokens := range []int{0, 1200} {
		t.Run(fmt.Sprint(tokens), func(t *testing.T) {
			t.Parallel()
			rig := newKimiConversationRig(t)
			rig.conversationFrame(t, kimiCompletionFixtureStart)
			rig.agent.HandleOutput([]byte(fmt.Sprintf(`{"type":"usage","session_id":"session_1","agent_id":"main","context_tokens":%d,"max_context_tokens":262144}`, tokens)))
			rig.conversationFrame(t, `{"type":"turn.ended","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
			awaitKimiTestOutput(t, rig.agent.sink)
			messages := rig.sink.Messages()
			require.Len(t, messages, 1)
			usage, present := metadataField(t, messages[0], contracts.SessionInfoKeyContextUsage)
			require.True(t, present)
			assert.JSONEq(t, fmt.Sprintf(`{"context_tokens":%d,"context_window":262144,"input_tokens":0,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}`, tokens), string(usage))
			count, present := metadataField(t, messages[0], contracts.MessageMetadataFieldToolUses)
			require.True(t, present)
			assert.JSONEq(t, `0`, string(count))
		})
	}
}

func TestKimiNativeFailureSurvivesSnapshotWithoutRepeatingRetry(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	const failure = `"turn_id":"t0","ordinal":0,"kind":"agent","status":"ended","outcome":"failed","started_at":"2026-11-18T00:00:00Z","steps":[],"error":{"code":"overloaded","message":"Model busy","retryable":true}`
	rig.conversationFrame(t, kimiCompletionFixtureStart)
	rig.conversationFrame(t, `{"type":"turn.ended","turn":{`+failure+`}}`)
	awaitKimiTestOutput(t, rig.agent.sink)
	require.Equal(t, 1, rig.sink.AutoScheduleCount())
	for range 2 {
		rig.conversationFrame(t, `{"type":"turns","turns":[{`+failure+`,"items":[]}],"has_more":false}`)
	}
	awaitKimiTestOutput(t, rig.agent.sink)
	assert.Equal(t, 1, rig.sink.AutoScheduleCount())
	assert.Zero(t, rig.sink.AutoCancelCount())
	assert.Len(t, rig.sink.Messages(), 1)
}
