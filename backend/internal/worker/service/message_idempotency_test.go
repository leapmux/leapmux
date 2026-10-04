package service

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

func TestKeyedTodoReplaySurvivesRemovalOfItsCanonicalTask(t *testing.T) {
	t.Parallel()
	svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
	sink.UpdateSessionID("native-session")
	createUse := []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"TaskCreate","input":{"subject":"Read the source"}}]}}`)
	createResult := []byte(`{"type":"user","message":{"content":[]},"tool_use_result":{"task":{"id":"1","subject":"Read the source"}}}`)
	updateUse := []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"TaskUpdate","input":{"taskId":"1","status":"in_progress"}}]}}`)
	updateResult := []byte(`{"type":"user","message":{"content":[]},"tool_use_result":{"success":true,"taskId":"1","updatedFields":["status"],"statusChange":{"from":"pending","to":"in_progress"}}}`)
	for _, row := range []struct {
		source          leapmuxv1.MessageSource
		content         []byte
		span, tool, key string
	}{
		{leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, createUse, "create", "TaskCreate", ""},
		{leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, createResult, "create", "TaskCreate", ""},
		{leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, updateUse, "update", "TaskUpdate", ""},
		{leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, updateResult, "update", "TaskUpdate", "native-update"},
	} {
		require.NoError(t, sink.PersistMessage(row.source, agent.MessageContent{Original: row.content, IdempotencyKey: row.key}, agent.SpanInfo{SpanID: row.span, SpanType: row.tool}))
	}
	_, changed, err := svc.Output.applyTodoEvent(ownerID, todoevents.Event{Kind: todoevents.KindSnapshot})
	require.NoError(t, err)
	require.True(t, changed)
	span := agent.SpanInfo{SpanID: "update", SpanType: "TaskUpdate"}
	replay := agent.MessageContent{Original: updateResult, AgentSessionID: "native-session", IdempotencyKey: "native-update"}
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, replay, span), "an immutable replay cannot require a removed task")
	rows, err := svc.Queries.ListAllMessagesByAgentID(context.Background(), db.ListAllMessagesByAgentIDParams{AgentID: ownerID})
	require.NoError(t, err)
	assert.Len(t, rows, 4)
	for _, content := range []agent.MessageContent{
		{Original: updateResult, AgentSessionID: "native-session"},
		{Original: updateResult, AgentSessionID: "native-session", IdempotencyKey: "another-record"},
		{Original: updateResult, AgentSessionID: "another-session", IdempotencyKey: "native-update"},
	} {
		require.ErrorContains(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, content, span), "unknown task")
	}
}

func TestProviderMessageKeyPreservesOneImmutableRecordAcrossReplay(t *testing.T) {
	t.Parallel()
	svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
	writer := &agentMessageCapturingWriter{channelID: "native-replay-watch"}
	registerAgentWatch(svc, writer.channelID, ownerID, leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
	sink.UpdateSessionID("native-session")
	content := agent.MessageContent{Original: []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"Complete native bytes"}]}}`), IdempotencyKey: "native-message:content"}
	for range 2 {
		require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{}))
	}
	rows, err := svc.Queries.ListMessagesByAgentID(context.Background(), db.ListMessagesByAgentIDParams{AgentID: ownerID, Limit: 10})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "native-message:content", rows[0].IdempotencyKey)
	assert.Equal(t, "native-session", rows[0].AgentSessionID)
	assert.Equal(t, int64(1), rows[0].Seq)
	require.Len(t, writer.snapshot(), 1)
	assert.Equal(t, rows[0].ID, writer.snapshot()[0].GetId())
}

func TestKeyedNativeTurnEndReplayBroadcastsOnce(t *testing.T) {
	t.Parallel()
	svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
	writer := &testResponseWriter{channelID: "native-turn-replay"}
	registerAgentWatch(svc, writer.channelID, ownerID, leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
	sink.UpdateSessionID("native-session")
	content := agent.MessageContent{Original: []byte(`{"type":"result","subtype":"success","num_turns":1}`), IdempotencyKey: "native-turn:17"}
	for range 2 {
		require.NoError(t, sink.PersistTurnEnd(content, agent.SpanInfo{}))
	}
	var turns int
	for _, stream := range writer.streamsSnapshot() {
		if decodeWatchAgentEvent(t, stream).GetTurnEnd() != nil {
			turns++
		}
	}
	assert.Equal(t, 1, turns)
	rows, err := svc.Queries.ListAllMessagesByAgentID(context.Background(), db.ListAllMessagesByAgentIDParams{AgentID: ownerID})
	require.NoError(t, err)
	assert.Len(t, rows, 1)
}

func TestConcurrentKeyedNativeTurnEndReplayPublishesAfterOneMessage(t *testing.T) {
	t.Parallel()
	svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
	writer := &testResponseWriter{channelID: "native-concurrent-turn-replay"}
	registerAgentWatch(svc, writer.channelID, ownerID, leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
	sink.UpdateSessionID("native-session")
	content := agent.MessageContent{Original: []byte(`{"type":"result","subtype":"success","num_turns":1}`), IdempotencyKey: "native-turn:0"}
	var group sync.WaitGroup
	for range 16 {
		group.Go(func() { assert.NoError(t, sink.PersistTurnEnd(content, agent.SpanInfo{})) })
	}
	group.Wait()
	var messageCount, turnCount int
	for _, stream := range writer.streamsSnapshot() {
		event := decodeWatchAgentEvent(t, stream)
		if event.GetAgentMessage() != nil {
			messageCount++
		}
		if event.GetTurnEnd() != nil {
			assert.Equal(t, 1, messageCount, "the completion event must follow the persisted message")
			turnCount++
		}
	}
	assert.Equal(t, 1, messageCount)
	assert.Equal(t, 1, turnCount)
}

func TestKeyedChildTurnEndReplayKeepsTheNativeResultAndOneCompletion(t *testing.T) {
	t.Parallel()
	svc, root := setupRootSink(t, "root-1")
	childID, err := root.EnsureChildAgent(agent.ChildAgentSpec{ProviderChildKey: "native-child", SpawnSpanID: "spawn", Title: "Read the source"})
	require.NoError(t, err)
	child := root.ChildSink(childID)
	child.UpdateSessionID("native-child-session")
	writer := &testResponseWriter{channelID: "native-child-turn-replay"}
	registerAgentWatch(svc, writer.channelID, childID, leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
	require.NoError(t, root.CloseBackgroundTask("native-child", bgtask.StatusCompleted))
	content := agent.MessageContent{Original: []byte(`{"type":"result","duration_ms":12}`), IdempotencyKey: "native-turn:1"}
	for range 2 {
		require.NoError(t, child.PersistTurnEnd(content, agent.SpanInfo{}))
	}
	messages := transcriptMessages(t, svc, childID)
	require.Len(t, messages, 1)
	assert.Equal(t, "result", messages[0]["type"])
	assert.Equal(t, float64(12), messages[0]["duration_ms"])
	var turns int
	for _, stream := range writer.streamsSnapshot() {
		if decodeWatchAgentEvent(t, stream).GetTurnEnd() != nil {
			turns++
		}
	}
	assert.Equal(t, 1, turns)
	stored, err := svc.Queries.HasAgentTurnEnd(t.Context(), db.HasAgentTurnEndParams{AgentID: childID, AgentSessionID: "native-child-session", IdempotencyKey: content.IdempotencyKey})
	require.NoError(t, err)
	assert.True(t, stored, "the native completion retains its independent replay receipt")
}

func TestKeyedNativeTurnEndReplaySurvivesSinkReplacementAndKeepsSessionsSeparate(t *testing.T) {
	t.Parallel()
	svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
	writer := &testResponseWriter{channelID: "native-durable-turn-replay"}
	registerAgentWatch(svc, writer.channelID, ownerID, leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
	content := agent.MessageContent{Original: []byte(`{"type":"result"}`), AgentSessionID: "native-session-a", IdempotencyKey: "native-turn:0"}
	require.NoError(t, sink.PersistTurnEnd(content, agent.SpanInfo{}))
	replacement := svc.Output.NewSink(ownerID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	require.NoError(t, replacement.PersistTurnEnd(content, agent.SpanInfo{}))
	content.AgentSessionID = "native-session-b"
	require.NoError(t, replacement.PersistTurnEnd(content, agent.SpanInfo{}))
	var turns int
	for _, stream := range writer.streamsSnapshot() {
		if decodeWatchAgentEvent(t, stream).GetTurnEnd() != nil {
			turns++
		}
	}
	assert.Equal(t, 2, turns)
	rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: ownerID})
	require.NoError(t, err)
	assert.Len(t, rows, 2)
}

func TestFailedNativeTurnEndClaimPublishesNoCompletionAndAcceptsTheNextValidAttempt(t *testing.T) {
	t.Parallel()
	svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
	writer := &testResponseWriter{channelID: "native-failed-turn-claim"}
	registerAgentWatch(svc, writer.channelID, ownerID, leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
	_, err := svc.Output.db.ExecContext(t.Context(), `CREATE TRIGGER refuse_native_turn_end BEFORE INSERT ON agent_turn_ends BEGIN SELECT RAISE(FAIL, 'the store refused the native completion'); END`)
	require.NoError(t, err)
	content := agent.MessageContent{Original: []byte(`{"type":"result"}`), AgentSessionID: "native", IdempotencyKey: "native-turn:0"}
	require.ErrorContains(t, sink.PersistTurnEnd(content, agent.SpanInfo{}), "claim native turn completion")
	for _, stream := range writer.streamsSnapshot() {
		assert.Nil(t, decodeWatchAgentEvent(t, stream).GetTurnEnd())
	}
	_, err = svc.Output.db.ExecContext(t.Context(), "DROP TRIGGER refuse_native_turn_end")
	require.NoError(t, err)
	require.NoError(t, sink.PersistTurnEnd(content, agent.SpanInfo{}))
	var turns int
	for _, stream := range writer.streamsSnapshot() {
		if decodeWatchAgentEvent(t, stream).GetTurnEnd() != nil {
			turns++
		}
	}
	assert.Equal(t, 1, turns)
	rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: ownerID})
	require.NoError(t, err)
	assert.Len(t, rows, 1)
}

func TestProviderMessageReplayDoesNotSplitAnAdjacentNotificationThread(t *testing.T) {
	t.Parallel()
	svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
	sink.UpdateSessionID("native-session")
	content := agent.MessageContent{Original: []byte(`{"type":"assistant"}`), IdempotencyKey: "native-record"}
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{}))
	_, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, []byte(`{"type":"context_cleared"}`))
	require.NoError(t, err)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{}))
	_, err = sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, []byte(`{"type":"interrupted"}`))
	require.NoError(t, err)
	rows, err := svc.Queries.ListAllMessagesByAgentID(context.Background(), db.ListAllMessagesByAgentIDParams{AgentID: ownerID})
	require.NoError(t, err)
	assert.Len(t, rows, 2, "a replay cannot end the current notification thread")
}

func TestProviderMessageKeyKeepsNativePartsAndSessionsSeparate(t *testing.T) {
	t.Parallel()
	svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
	for _, session := range []string{"native-session-a", "native-session-b"} {
		sink.UpdateSessionID(session)
		for _, key := range []string{"native-message:content", "native-message:thought:0"} {
			require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"type":"assistant"}`), IdempotencyKey: key}, agent.SpanInfo{}))
		}
	}
	rows, err := svc.Queries.ListMessagesByAgentID(context.Background(), db.ListMessagesByAgentIDParams{AgentID: ownerID, Limit: 10})
	require.NoError(t, err)
	assert.Len(t, rows, 4)
}

func TestEmptyProviderMessageKeyPreservesRepeatedConversationRows(t *testing.T) {
	t.Parallel()
	svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
	for range 2 {
		require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"type":"assistant"}`)}, agent.SpanInfo{}))
	}
	rows, err := svc.Queries.ListMessagesByAgentID(context.Background(), db.ListMessagesByAgentIDParams{AgentID: ownerID, Limit: 10})
	require.NoError(t, err)
	assert.Len(t, rows, 2)
}

func TestConcurrentProviderMessageReplayPersistsOneRecord(t *testing.T) {
	t.Parallel()
	svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
	sink.UpdateSessionID("native-session")
	var group sync.WaitGroup
	failures := make(chan error, 8)
	for range 8 {
		group.Go(func() {
			failures <- sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"type":"assistant"}`), IdempotencyKey: "native-record"}, agent.SpanInfo{})
		})
	}
	group.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	rows, err := svc.Queries.ListMessagesByAgentID(context.Background(), db.ListMessagesByAgentIDParams{AgentID: ownerID, Limit: 10})
	require.NoError(t, err)
	assert.Len(t, rows, 1)
}
