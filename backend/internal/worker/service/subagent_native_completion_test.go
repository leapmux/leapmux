package service

import (
	"fmt"
	"sync"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubagentNativeCompletionSurvivesEveryRegistryOrder(t *testing.T) {
	t.Parallel()
	for _, order := range []string{"registry first", "native first", "concurrent"} {
		t.Run(order, func(t *testing.T) {
			t.Parallel()
			svc, root := setupRootSink(t, "root-1")
			childID, err := root.EnsureChildAgent(agent.ChildAgentSpec{ProviderChildKey: "native-child", Title: "Read the source"})
			require.NoError(t, err)
			child := root.ChildSink(childID)
			child.UpdateSessionID("native-session")
			content := agent.MessageContent{
				Original:       []byte(`{"type":"result","subtype":"error_during_execution","duration_ms":5100,"is_error":true,"errors":["The child could not read its assigned file."]}`),
				AgentSessionID: "native-session", IdempotencyKey: "native-turn:1",
			}
			writer := &testResponseWriter{channelID: "native-child-completion"}
			registerAgentWatch(svc, writer.channelID, childID, leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
			closeRegistry := func() { assert.NoError(t, root.CloseBackgroundTask("native-child", bgtask.StatusFailed)) }
			finishNative := func() { assert.NoError(t, child.PersistTurnEnd(content, agent.SpanInfo{})) }
			switch order {
			case "registry first":
				closeRegistry()
				finishNative()
			case "native first":
				finishNative()
				closeRegistry()
			case "concurrent":
				var group sync.WaitGroup
				group.Go(closeRegistry)
				group.Go(finishNative)
				group.Wait()
			}
			// A replay must preserve the message without repeating completion effects.
			finishNative()
			rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: childID})
			require.NoError(t, err)
			require.Len(t, rows, 1)
			raw, err := msgcodec.Decompress(rows[0].Content, rows[0].ContentCompression)
			require.NoError(t, err)
			assert.Equal(t, content.Original, raw)
			assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, rows[0].Source)
			assert.Equal(t, content.IdempotencyKey, rows[0].IdempotencyKey)
			assert.Equal(t, content.AgentSessionID, rows[0].AgentSessionID)
			var messageCount, turnCount int
			for _, stream := range writer.streamsSnapshot() {
				event := decodeWatchAgentEvent(t, stream)
				if event.GetAgentMessage() != nil {
					messageCount++
				}
				if event.GetTurnEnd() != nil {
					assert.Equal(t, 1, messageCount, "publish the native message before its completion")
					turnCount++
				}
			}
			assert.Equal(t, 1, messageCount)
			assert.Equal(t, 1, turnCount)
			row := registrySnapshotRow(t, svc, "root-1", "native-child")
			assert.Equal(t, bgtask.StatusFailed, row.Status)
			assert.False(t, row.EndedAt.IsZero())
		})
	}
}

func TestSubagentRegistryCompletionDoesNotInventTranscriptMessages(t *testing.T) {
	t.Parallel()
	for _, status := range []bgtask.Status{bgtask.StatusSucceeded, bgtask.StatusFailed, bgtask.StatusStopped, bgtask.StatusInterrupted, bgtask.StatusEndedWithUnknownOutcome} {
		t.Run(bgtask.StatusWire(status), func(t *testing.T) {
			t.Parallel()
			svc, root := setupRootSink(t, "root-1")
			childID, err := root.EnsureChildAgent(agent.ChildAgentSpec{ProviderChildKey: "native-child", Title: "Read the source"})
			require.NoError(t, err)
			require.NoError(t, root.CloseBackgroundTask("native-child", status))
			assert.Empty(t, transcriptMessages(t, svc, childID))
			row := registrySnapshotRow(t, svc, "root-1", "native-child")
			assert.Equal(t, status, row.Status)
			assert.False(t, row.EndedAt.IsZero())
		})
	}
}

func TestFinishedChildClearsReportedActivityWithoutInventingTranscript(t *testing.T) {
	t.Parallel()
	svc, root := setupRootSink(t, "root-1")
	childID, err := root.EnsureChildAgent(agent.ChildAgentSpec{ProviderChildKey: "native-child", Title: "Read the source"})
	require.NoError(t, err)
	require.NoError(t, root.UpdateBackgroundTaskStatus("native-child", bgtask.StatusRunning, "Read the source"))
	assert.Equal(t, int32(1), countActiveBackgroundTasks(svc.Output.backgroundTaskRows("root-1"), childID))
	require.NoError(t, root.CloseBackgroundTask("native-child", bgtask.StatusEndedWithUnknownOutcome))
	assert.Zero(t, countActiveBackgroundTasks(svc.Output.backgroundTaskRows("root-1"), childID))
	assert.Zero(t, svc.Output.AgentActivitySnapshot(childID, "root-1").ActiveTasks)
	assert.Empty(t, transcriptMessages(t, svc, childID))
	assertChildTaskOutcome(t, svc, "root-1", "native-child", childID, bgtask.StatusEndedWithUnknownOutcome)
}

// assertChildTaskOutcome checks both the displayed status and the stored status.
func assertChildTaskOutcome(t *testing.T, svc *Service, rootID, rowKey, childID string, status bgtask.Status) {
	t.Helper()
	row := registrySnapshotRow(t, svc, rootID, rowKey)
	assert.Equal(t, status, row.Status)
	assert.False(t, row.EndedAt.IsZero())
	stored, err := svc.Queries.GetAgentBackgroundTaskByChildAgentID(t.Context(), childID)
	require.NoError(t, err)
	assert.Equal(t, rootID, stored.OwnerAgentID)
	assert.Equal(t, rowKey, stored.RowKey)
	assert.Equal(t, leapmuxv1.BackgroundTaskStatus(status), stored.Status)
	assert.True(t, stored.EndedAt.Valid)
}

func TestFinalBackgroundTaskUpsertKeepsTheSameEndTimeInColdAndWarmReads(t *testing.T) {
	t.Parallel()
	for _, status := range []bgtask.Status{bgtask.StatusSucceeded, bgtask.StatusFailed, bgtask.StatusStopped, bgtask.StatusInterrupted, bgtask.StatusEndedWithUnknownOutcome} {
		for _, startsActive := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/starts_active_%t", bgtask.StatusWire(status), startsActive), func(t *testing.T) {
				t.Parallel()
				svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
				task := bgtask.Upsert{RowKey: "native-task", Kind: bgtask.KindShell, Title: "Read the source", Status: status}
				if startsActive {
					active := task
					active.Status = bgtask.StatusRunning
					require.NoError(t, sink.UpsertBackgroundTask(active))
				}
				require.NoError(t, sink.UpsertBackgroundTask(task))
				warm := registrySnapshotRow(t, svc, ownerID, task.RowKey)
				require.False(t, warm.EndedAt.IsZero())
				stored := storedRow(t, svc, ownerID, task.RowKey)
				require.True(t, stored.EndedAt.Valid, "the final upsert must store its cache end time")
				assert.Equal(t, warm.EndedAt, stored.EndedAt.Time)
				task.Description = "The native report arrived."
				require.NoError(t, sink.UpsertBackgroundTask(task))
				assert.Equal(t, stored.EndedAt, storedRow(t, svc, ownerID, task.RowKey).EndedAt, "a final report update retains the first end time")
				require.NoError(t, sink.ReviveBackgroundTask(task.RowKey))
				running := storedRow(t, svc, ownerID, task.RowKey)
				assert.False(t, running.EndedAt.Valid)
				assert.True(t, registrySnapshotRow(t, svc, ownerID, task.RowKey).EndedAt.IsZero())
				require.NoError(t, sink.UpsertBackgroundTask(task))
				finished := storedRow(t, svc, ownerID, task.RowKey)
				require.True(t, finished.EndedAt.Valid)
				assert.Equal(t, registrySnapshotRow(t, svc, ownerID, task.RowKey).EndedAt, finished.EndedAt.Time)
			})
		}
	}
}
