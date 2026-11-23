package service

import (
	"context"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/claude/claudetest"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
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
// RestoreState identifies queued agents held by active background work, runs
// MarkAllActiveAgentBackgroundTasksInterrupted, and then recovers the input
// queues so recovered snapshots reflect the post-sweep state. The sweep is pure
// DB (caches do not exist yet at boot), so the rows read back after the call
// reflect the persisted state the next boot -- and the live worker -- will see.
func TestRestoreStateMarksActiveBackgroundTasksInterrupted(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "root-1",
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	// Seed background-task rows directly in the DB at ACTIVE statuses, the way
	// a previous worker process would have left them at a crash. Two active
	// rows (pending + running) plus one already-finished row to confirm the
	// sweep is scoped to active rows only.
	writeBackgroundTaskFixture(t, svc.Queries, db.UpsertAgentBackgroundTaskParams{
		Role: leapmuxv1.BackgroundTaskRole_BACKGROUND_TASK_ROLE_TASK_OUTCOME,

		OwnerAgentID: "root-1", RowKey: "task-pending", Seq: 1,
		Kind: leapmuxv1.BackgroundTaskKind(bgtask.KindSubagent), Title: "pending row", Status: leapmuxv1.BackgroundTaskStatus(bgtask.StatusPending),
	})
	writeBackgroundTaskFixture(t, svc.Queries, db.UpsertAgentBackgroundTaskParams{
		Role: leapmuxv1.BackgroundTaskRole_BACKGROUND_TASK_ROLE_TASK_OUTCOME,

		OwnerAgentID: "root-1", RowKey: "task-running", Seq: 2,
		Kind: leapmuxv1.BackgroundTaskKind(bgtask.KindShell), Title: "running row", Status: leapmuxv1.BackgroundTaskStatus(bgtask.StatusRunning),
	})
	writeBackgroundTaskFixture(t, svc.Queries, db.UpsertAgentBackgroundTaskParams{
		Role: leapmuxv1.BackgroundTaskRole_BACKGROUND_TASK_ROLE_TASK_OUTCOME,

		OwnerAgentID: "root-1", RowKey: "task-done", Seq: 3,
		Kind: leapmuxv1.BackgroundTaskKind(bgtask.KindSubagent), Title: "already done", Status: leapmuxv1.BackgroundTaskStatus(bgtask.StatusSucceeded),
	})

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
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	sink := newTestRootOutputSink(t, svc.Output, "root-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)

	// Only the two active children receive an interrupted registry status.
	liveID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole: bgtask.RoleChildTask,
		SpawnSpanID:  "span-1", ProviderChildKey: "task-1", Title: "SCAN"})
	require.NoError(t, err)
	otherID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole: bgtask.RoleChildTask,
		SpawnSpanID:  "span-2", ProviderChildKey: "task-2", Title: "BUILD"})
	require.NoError(t, err)
	for _, childID := range []string{liveID, otherID} {
		require.NoError(t, sink.PersistChildMessage(childID,
			leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, []byte(`{"type":"text","text":"Native work before the Worker stopped."}`), agent.SpanInfo{}))
	}
	doneID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole: bgtask.RoleChildTask,
		SpawnSpanID:  "span-3", ProviderChildKey: "task-3", Title: "DONE"})
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

func TestRestoreStatePausesQueueHeldByCrashedBackgroundTaskAndSkipsAutoDrain(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	svc, _, _ := setupTestService(t)
	workingDir := t.TempDir()
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "root-1",
		WorkingDir:    workingDir,
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	// Simulate a crash while the root agent's turn was idle, a background task
	// was running, and a user message was queued behind that background task.
	writeBackgroundTaskFixture(t, svc.Queries, db.UpsertAgentBackgroundTaskParams{
		Role:         leapmuxv1.BackgroundTaskRole_BACKGROUND_TASK_ROLE_TASK_OUTCOME,
		OwnerAgentID: "root-1",
		RowKey:       "bg-running",
		Seq:          1,
		Kind:         leapmuxv1.BackgroundTaskKind(bgtask.KindShell),
		Title:        "long shell task",
		Status:       leapmuxv1.BackgroundTaskStatus(bgtask.StatusRunning),
	})
	enqueued, err := svc.InputQueue.Enqueue(ctx, inputqueue.NewItem{
		ID:      "queued-after-crash",
		AgentID: "root-1",
		Kind:    leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		Text:    "do not auto-dispatch after crash",
	})
	require.NoError(t, err)
	require.False(t, enqueued.Paused)
	require.True(t, enqueued.ActiveBackgroundWork)

	// Capture the snapshot broadcast during RestoreState to confirm it reflects
	// the post-sweep background-task state rather than stale pre-sweep flags.
	writer := newTestWriter()
	registerAgentWatch(svc, "browser-1", "root-1", leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)

	svc.RestoreState()

	// Verify the background task was marked interrupted.
	rows, err := svc.Queries.ListAgentBackgroundTasksNewestFirst(ctx, db.ListAgentBackgroundTasksNewestFirstParams{
		OwnerAgentID: "root-1", Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, leapmuxv1.BackgroundTaskStatus(bgtask.StatusInterrupted), rows[0].Status)

	// Verify the broadcast snapshot from RestoreState itself shows post-sweep state.
	var broadcastSnapshot *leapmuxv1.AgentInputQueueSnapshot
	for _, stream := range writer.streamsSnapshot() {
		event := decodeWatchAgentEvent(t, stream)
		if snap := event.GetInputQueueChanged().GetSnapshot(); snap != nil {
			broadcastSnapshot = snap
		}
	}
	require.NotNil(t, broadcastSnapshot, "RestoreState must publish the recovered queue snapshot")
	assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_INTERRUPTED, broadcastSnapshot.GetPauseReason())
	require.Len(t, broadcastSnapshot.GetItems(), 1)
	assert.False(t, broadcastSnapshot.GetItems()[0].GetCanSteer(), "recovered paused snapshot must not advertise stale background-work CanSteer")
	assert.False(t, broadcastSnapshot.GetItems()[0].GetCanPreempt(), "recovered paused snapshot must not advertise stale background-work CanPreempt")

	// Verify the persisted snapshot matches.
	recovered, err := svc.InputQueue.Snapshot(ctx, "root-1")
	require.NoError(t, err)
	assert.True(t, recovered.Paused)
	assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_INTERRUPTED, recovered.PauseReason)
	assert.False(t, recovered.ActiveBackgroundWork)
	require.Len(t, recovered.Items, 1)
	assert.False(t, recovered.Items[0].CanSteer)
	assert.False(t, recovered.Items[0].CanPreempt)

	// Start a live agent and run DrainRecovered: the interrupted queue must NOT auto-drain.
	rootSink := newTestRootOutputSink(t, svc.Output, "root-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	_, err = svc.Agents.StartAgentWith(ctx, agent.Options{
		AgentID: "root-1", WorkingDir: workingDir,
	}, rootSink, claudetest.StartEcho)
	require.NoError(t, err)
	t.Cleanup(func() { svc.Agents.StopAndWaitAgent("root-1") })

	require.NoError(t, svc.InputQueue.DrainRecovered(ctx))
	svc.InputQueue.StopAndWait()

	var msgCount int
	require.NoError(t, svc.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE agent_id = 'root-1' AND id = 'queued-after-crash'`).Scan(&msgCount))
	assert.Zero(t, msgCount, "DrainRecovered must not auto-dispatch a message held by a crashed background task")
}
