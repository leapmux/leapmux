package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// TestRestoreStateMarksActiveBackgroundTasksInterrupted pins the boot-time
// invariant RestoreState enforces: every background-task row left in an ACTIVE
// status (pending/running) by a previous worker process is relabeled
// 'interrupted', because the process that was making progress on it is gone
// (crash/restart). A row that was already terminal (completed/failed/...) must
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
		Kind: "subagent", Title: "pending row", Status: "pending",
	}))
	require.NoError(t, svc.Queries.UpsertAgentBackgroundTask(ctx, db.UpsertAgentBackgroundTaskParams{
		OwnerAgentID: "root-1", RowKey: "task-running", Seq: 2,
		Kind: "shell", Title: "running row", Status: "running",
	}))
	require.NoError(t, svc.Queries.UpsertAgentBackgroundTask(ctx, db.UpsertAgentBackgroundTaskParams{
		OwnerAgentID: "root-1", RowKey: "task-done", Seq: 3,
		Kind: "subagent", Title: "already done", Status: "completed",
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
	assert.Equal(t, "interrupted", byKey["task-pending"].Status,
		"a pending row left by a crashed worker must be relabeled 'interrupted' at boot")
	require.Contains(t, byKey, "task-running")
	assert.Equal(t, "interrupted", byKey["task-running"].Status,
		"a running row left by a crashed worker must be relabeled 'interrupted' at boot")

	// The already-finished row is untouched -- the sweep scopes to active rows.
	require.Contains(t, byKey, "task-done")
	assert.Equal(t, "completed", byKey["task-done"].Status,
		"an already-terminal row must not be relabeled by the boot sweep")
}
