package service

import (
	"context"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/bgtask"

	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// TestRestoreStateMarksActiveBackgroundTasksInterrupted pins the boot-time
// invariant RestoreState enforces: every background-task row left in an ACTIVE
// status (pending/running) by a previous worker process is relabeled
// 'interrupted', because the process that was making progress on it is gone
// (crash/restart). A row that was already final (completed/failed/...) must
// be left untouched.
//
// RestoreState runs MarkAllActiveAgentBackgroundTasksInterrupted BEFORE
// restoring auto-continue schedules, and the sweep is pure DB (caches do not
// exist yet at boot), so the rows read back after the call reflect the
// persisted state the next boot -- and the live worker -- will see.
func TestRestoreStateMarksActiveBackgroundTasksInterrupted(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "root-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	// Seed background-task rows directly in the DB at ACTIVE statuses, the way
	// a previous worker process would have left them at a crash. Two active
	// rows (pending + running) plus one already-finished row to confirm the
	// sweep is scoped to active rows only.
	require.NoError(t, svc.Queries.UpsertAgentBackgroundTask(ctx, db.UpsertAgentBackgroundTaskParams{
		OwnerAgentID: "root-1", RowKey: "task-pending", Seq: 1,
		Kind: leapmuxv1.BackgroundTaskKind(bgtask.KindSubagent), Title: "pending row", Status: leapmuxv1.BackgroundTaskStatus(bgtask.StatusPending),
	}))
	require.NoError(t, svc.Queries.UpsertAgentBackgroundTask(ctx, db.UpsertAgentBackgroundTaskParams{
		OwnerAgentID: "root-1", RowKey: "task-running", Seq: 2,
		Kind: leapmuxv1.BackgroundTaskKind(bgtask.KindShell), Title: "running row", Status: leapmuxv1.BackgroundTaskStatus(bgtask.StatusRunning),
	}))
	require.NoError(t, svc.Queries.UpsertAgentBackgroundTask(ctx, db.UpsertAgentBackgroundTaskParams{
		OwnerAgentID: "root-1", RowKey: "task-done", Seq: 3,
		Kind: leapmuxv1.BackgroundTaskKind(bgtask.KindSubagent), Title: "already done", Status: leapmuxv1.BackgroundTaskStatus(bgtask.StatusSucceeded),
	}))

	// The boot-time sweep. RestoreState logs but does not return the
	// affected-row count, so the assertion is against the persisted rows.
	svc.RestoreState()

	rows, err := svc.Queries.ListAgentBackgroundTasksNewestFirst(ctx, db.ListAgentBackgroundTasksNewestFirstParams{
		OwnerAgentID: "root-1", Limit: 100,
	})
	require.NoError(t, err)
	byKey := make(map[string]db.AgentBackgroundTask, len(rows))
	for _, r := range rows {
		byKey[r.RowKey] = r
	}

	require.Contains(t, byKey, "task-pending")
	assert.Equal(t, leapmuxv1.BackgroundTaskStatus(bgtask.StatusInterrupted), byKey["task-pending"].Status,
		"a pending row left by a crashed worker must be relabeled 'interrupted' at boot")
	require.Contains(t, byKey, "task-running")
	assert.Equal(t, leapmuxv1.BackgroundTaskStatus(bgtask.StatusInterrupted), byKey["task-running"].Status,
		"a running row left by a crashed worker must be relabeled 'interrupted' at boot")

	// The already-finished row is untouched -- the sweep scopes to active rows.
	require.Contains(t, byKey, "task-done")
	assert.Equal(t, leapmuxv1.BackgroundTaskStatus(bgtask.StatusSucceeded), byKey["task-done"].Status,
		"an already-final row must not be relabeled by the boot sweep")
}

// Boot recovery records interruption without changing any native transcript.
func TestRestoreStatePreservesNativeMessagesForEachInterruptedChild(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "root-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	sink := svc.Output.NewSink("root-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)

	// Only the two active children receive an interrupted registry status.
	liveID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "span-1", ProviderChildKey: "task-1", Title: "SCAN"})
	require.NoError(t, err)
	otherID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "span-2", ProviderChildKey: "task-2", Title: "BUILD"})
	require.NoError(t, err)
	for _, childID := range []string{liveID, otherID} {
		require.NoError(t, sink.PersistChildMessage(childID,
			leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, []byte(`{"type":"text","text":"Native work before the Worker stopped."}`), agent.SpanInfo{}))
	}
	doneID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "span-3", ProviderChildKey: "task-3", Title: "DONE"})
	require.NoError(t, err)
	require.NoError(t, sink.CloseBackgroundTask("task-3", bgtask.StatusSucceeded))
	doneBefore := len(transcriptMessages(t, svc, doneID))

	svc.RestoreState()

	for _, childID := range []string{liveID, otherID} {
		msgs := transcriptMessages(t, svc, childID)
		require.Len(t, msgs, 1)
		assert.Equal(t, "Native work before the Worker stopped.", msgs[0]["text"])
		stored, err := svc.Queries.GetAgentBackgroundTaskByChildAgentID(ctx, childID)
		require.NoError(t, err)
		assert.Equal(t, leapmuxv1.BackgroundTaskStatus(bgtask.StatusInterrupted), stored.Status)
		assert.True(t, stored.EndedAt.Valid)
	}
	assert.Len(t, transcriptMessages(t, svc, doneID), doneBefore,
		"a child that already ended is not closed a second time")
}
