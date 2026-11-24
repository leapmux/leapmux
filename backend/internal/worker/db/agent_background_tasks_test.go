package db_test

import (
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	gendb "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func openMigratedWorkerDB(t *testing.T, agentIDs ...string) (*sql.DB, *gendb.Queries) {
	t.Helper()
	sqlDB, err := workerdb.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, workerdb.Migrate(t.Context(), sqlDB))
	for _, agentID := range agentIDs {
		_, err := sqlDB.ExecContext(t.Context(), `INSERT INTO agents (id) VALUES (?)`, agentID)
		require.NoError(t, err)
	}
	return sqlDB, gendb.New(sqlDB)
}

func TestAgentBackgroundTasksReviveCountConstraint(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	sqlDB, queries := openMigratedWorkerDB(t, "root-1")
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

	_, err := queries.UpsertAgentBackgroundTask(ctx, gendb.UpsertAgentBackgroundTaskParams{
		OwnerAgentID: "root-1",
		RowKey:       "task-1",
		Seq:          1,
		Kind:         leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL,
		Role:         leapmuxv1.BackgroundTaskRole_BACKGROUND_TASK_ROLE_TASK_OUTCOME,
		Status:       leapmuxv1.BackgroundTaskStatus(bgtask.StatusRunning),
		CreatedAt:    sqltime.NewSQLiteTime(now),
		UpdatedAt:    sqltime.NewSQLiteTime(now),
	})
	require.NoError(t, err)

	row, err := queries.GetAgentBackgroundTaskByRowKey(ctx, gendb.GetAgentBackgroundTaskByRowKeyParams{
		OwnerAgentID: "root-1",
		RowKey:       "task-1",
	})
	require.NoError(t, err)
	assert.Zero(t, row.ReviveCount, "revive_count must default to 0")

	_, err = sqlDB.ExecContext(ctx,
		`UPDATE agent_background_tasks SET revive_count = -1 WHERE owner_agent_id = ? AND row_key = ?`,
		"root-1", "task-1")
	require.Error(t, err)
	assert.ErrorContains(t, err, "CHECK constraint failed")

	_, err = sqlDB.ExecContext(ctx,
		`INSERT INTO agent_background_tasks (owner_agent_id, row_key, seq, kind, role, status, revive_count) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"root-1", "task-neg", 2,
		leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL,
		leapmuxv1.BackgroundTaskRole_BACKGROUND_TASK_ROLE_TASK_OUTCOME,
		bgtask.StatusRunning,
		-1)
	require.Error(t, err)
	assert.ErrorContains(t, err, "CHECK constraint failed")

	for _, want := range []int64{0, 1, 7} {
		_, err = sqlDB.ExecContext(ctx,
			`UPDATE agent_background_tasks SET revive_count = ? WHERE owner_agent_id = ? AND row_key = ?`,
			want, "root-1", "task-1")
		require.NoError(t, err)

		row, err = queries.GetAgentBackgroundTaskByRowKey(ctx, gendb.GetAgentBackgroundTaskByRowKeyParams{
			OwnerAgentID: "root-1",
			RowKey:       "task-1",
		})
		require.NoError(t, err)
		assert.Equal(t, want, row.ReviveCount)
	}
}

func TestReviveAgentBackgroundTaskIncrementsReviveCountOnlyFromFinalStatus(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	_, queries := openMigratedWorkerDB(t, "root-1")

	t0 := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)
	t2 := t1.Add(time.Minute)
	t3 := t2.Add(time.Minute)
	t4 := t3.Add(time.Minute)

	_, err := queries.UpsertAgentBackgroundTask(ctx, gendb.UpsertAgentBackgroundTaskParams{
		OwnerAgentID: "root-1",
		RowKey:       "sub-1",
		Seq:          1,
		Kind:         leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT,
		Role:         leapmuxv1.BackgroundTaskRole_BACKGROUND_TASK_ROLE_CHILD_TASK,
		ChildAgentID: "child-1",
		Description:  "/tmp/previous-output.txt",
		Status:       leapmuxv1.BackgroundTaskStatus(bgtask.StatusSucceeded),
		CreatedAt:    sqltime.NewSQLiteTime(t0),
		UpdatedAt:    sqltime.NewSQLiteTime(t0),
		EndedAt:      sqltime.SQLiteNullTimeOf(t0),
	})
	require.NoError(t, err)

	// Reviving a finished row increments revive_count and clears description and ended_at.
	affected, err := queries.ReviveAgentBackgroundTask(ctx, gendb.ReviveAgentBackgroundTaskParams{
		Status:         leapmuxv1.BackgroundTaskStatus(bgtask.StatusRunning),
		UpdatedAt:      sqltime.NewSQLiteTime(t1),
		OwnerAgentID:   "root-1",
		RowKey:         "sub-1",
		MinFinalStatus: leapmuxv1.BackgroundTaskStatus(bgtask.MinFinalStatus),
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), affected)

	row, err := queries.GetAgentBackgroundTaskByRowKey(ctx, gendb.GetAgentBackgroundTaskByRowKeyParams{
		OwnerAgentID: "root-1",
		RowKey:       "sub-1",
	})
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.BackgroundTaskStatus(bgtask.StatusRunning), row.Status)
	assert.Equal(t, int64(1), row.ReviveCount)
	assert.Empty(t, row.Description)
	assert.False(t, row.EndedAt.Valid)

	// Calling revive while running is a no-op and leaves revive_count unchanged.
	affected, err = queries.ReviveAgentBackgroundTask(ctx, gendb.ReviveAgentBackgroundTaskParams{
		Status:         leapmuxv1.BackgroundTaskStatus(bgtask.StatusRunning),
		UpdatedAt:      sqltime.NewSQLiteTime(t2),
		OwnerAgentID:   "root-1",
		RowKey:         "sub-1",
		MinFinalStatus: leapmuxv1.BackgroundTaskStatus(bgtask.MinFinalStatus),
	})
	require.NoError(t, err)
	assert.Zero(t, affected)

	row, err = queries.GetAgentBackgroundTaskByRowKey(ctx, gendb.GetAgentBackgroundTaskByRowKeyParams{
		OwnerAgentID: "root-1",
		RowKey:       "sub-1",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), row.ReviveCount)

	// Calling revive while paused is also a no-op and leaves revive_count unchanged.
	affected, err = queries.UpdateAgentBackgroundTaskStatus(ctx, gendb.UpdateAgentBackgroundTaskStatusParams{
		Status:       leapmuxv1.BackgroundTaskStatus(bgtask.StatusPaused),
		UpdatedAt:    sqltime.NewSQLiteTime(t2),
		OwnerAgentID: "root-1",
		RowKey:       "sub-1",
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), affected)

	affected, err = queries.ReviveAgentBackgroundTask(ctx, gendb.ReviveAgentBackgroundTaskParams{
		Status:         leapmuxv1.BackgroundTaskStatus(bgtask.StatusRunning),
		UpdatedAt:      sqltime.NewSQLiteTime(t2),
		OwnerAgentID:   "root-1",
		RowKey:         "sub-1",
		MinFinalStatus: leapmuxv1.BackgroundTaskStatus(bgtask.MinFinalStatus),
	})
	require.NoError(t, err)
	assert.Zero(t, affected)

	row, err = queries.GetAgentBackgroundTaskByRowKey(ctx, gendb.GetAgentBackgroundTaskByRowKeyParams{
		OwnerAgentID: "root-1",
		RowKey:       "sub-1",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), row.ReviveCount)

	// After closing to a final status, reviving again increments revive_count to 2.
	affected, err = queries.CloseAgentBackgroundTask(ctx, gendb.CloseAgentBackgroundTaskParams{
		Status:         leapmuxv1.BackgroundTaskStatus(bgtask.StatusFailed),
		EndedAt:        sqltime.SQLiteNullTimeOf(t3),
		UpdatedAt:      sqltime.NewSQLiteTime(t3),
		OwnerAgentID:   "root-1",
		RowKey:         "sub-1",
		MinFinalStatus: leapmuxv1.BackgroundTaskStatus(bgtask.MinFinalStatus),
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), affected)

	affected, err = queries.ReviveAgentBackgroundTask(ctx, gendb.ReviveAgentBackgroundTaskParams{
		Status:         leapmuxv1.BackgroundTaskStatus(bgtask.StatusRunning),
		UpdatedAt:      sqltime.NewSQLiteTime(t4),
		OwnerAgentID:   "root-1",
		RowKey:         "sub-1",
		MinFinalStatus: leapmuxv1.BackgroundTaskStatus(bgtask.MinFinalStatus),
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), affected)

	row, err = queries.GetAgentBackgroundTaskByRowKey(ctx, gendb.GetAgentBackgroundTaskByRowKeyParams{
		OwnerAgentID: "root-1",
		RowKey:       "sub-1",
	})
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.BackgroundTaskStatus(bgtask.StatusRunning), row.Status)
	assert.Equal(t, int64(2), row.ReviveCount)
	assert.Empty(t, row.Description)
	assert.False(t, row.EndedAt.Valid)
}

func TestCloneAgentBackgroundTaskForResumePreservesReviveCount(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	sqlDB, queries := openMigratedWorkerDB(t, "old-root", "new-root")

	t0 := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	tCopy := t0.Add(5 * time.Minute)

	_, err := queries.UpsertAgentBackgroundTask(ctx, gendb.UpsertAgentBackgroundTaskParams{
		OwnerAgentID: "old-root",
		RowKey:       "finished-task",
		Seq:          1,
		Kind:         leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT,
		Role:         leapmuxv1.BackgroundTaskRole_BACKGROUND_TASK_ROLE_CHILD_TASK,
		ChildAgentID: "old-child-1",
		Status:       leapmuxv1.BackgroundTaskStatus(bgtask.StatusSucceeded),
		CreatedAt:    sqltime.NewSQLiteTime(t0),
		UpdatedAt:    sqltime.NewSQLiteTime(t0),
		EndedAt:      sqltime.SQLiteNullTimeOf(t0),
	})
	require.NoError(t, err)
	_, err = sqlDB.ExecContext(ctx,
		`UPDATE agent_background_tasks SET revive_count = 2 WHERE owner_agent_id = ? AND row_key = ?`,
		"old-root", "finished-task")
	require.NoError(t, err)

	_, err = queries.UpsertAgentBackgroundTask(ctx, gendb.UpsertAgentBackgroundTaskParams{
		OwnerAgentID: "old-root",
		RowKey:       "active-task",
		Seq:          2,
		Kind:         leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT,
		Role:         leapmuxv1.BackgroundTaskRole_BACKGROUND_TASK_ROLE_CHILD_TASK,
		ChildAgentID: "old-child-2",
		Status:       leapmuxv1.BackgroundTaskStatus(bgtask.StatusRunning),
		CreatedAt:    sqltime.NewSQLiteTime(t0),
		UpdatedAt:    sqltime.NewSQLiteTime(t0),
	})
	require.NoError(t, err)
	_, err = sqlDB.ExecContext(ctx,
		`UPDATE agent_background_tasks SET revive_count = 3 WHERE owner_agent_id = ? AND row_key = ?`,
		"old-root", "active-task")
	require.NoError(t, err)

	affected, err := queries.CloneAgentBackgroundTaskForResume(ctx, gendb.CloneAgentBackgroundTaskForResumeParams{
		NewRootID:         "new-root",
		NewChildID:        "new-child-1",
		NewParentID:       "",
		MinFinalStatus:    leapmuxv1.BackgroundTaskStatus(bgtask.MinFinalStatus),
		InterruptedStatus: leapmuxv1.BackgroundTaskStatus(bgtask.StatusInterrupted),
		CopyTime:          sqltime.NewSQLiteTime(tCopy),
		OldRootID:         "old-root",
		RowKey:            "finished-task",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), affected)

	clonedFinished, err := queries.GetAgentBackgroundTaskByRowKey(ctx, gendb.GetAgentBackgroundTaskByRowKeyParams{
		OwnerAgentID: "new-root",
		RowKey:       "finished-task",
	})
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.BackgroundTaskStatus(bgtask.StatusSucceeded), clonedFinished.Status)
	assert.Equal(t, int64(2), clonedFinished.ReviveCount)
	assert.True(t, clonedFinished.EndedAt.Valid)
	assert.Equal(t, t0, clonedFinished.EndedAt.Time)

	affected, err = queries.CloneAgentBackgroundTaskForResume(ctx, gendb.CloneAgentBackgroundTaskForResumeParams{
		NewRootID:         "new-root",
		NewChildID:        "new-child-2",
		NewParentID:       "",
		MinFinalStatus:    leapmuxv1.BackgroundTaskStatus(bgtask.MinFinalStatus),
		InterruptedStatus: leapmuxv1.BackgroundTaskStatus(bgtask.StatusInterrupted),
		CopyTime:          sqltime.NewSQLiteTime(tCopy),
		OldRootID:         "old-root",
		RowKey:            "active-task",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), affected)

	clonedActive, err := queries.GetAgentBackgroundTaskByRowKey(ctx, gendb.GetAgentBackgroundTaskByRowKeyParams{
		OwnerAgentID: "new-root",
		RowKey:       "active-task",
	})
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.BackgroundTaskStatus(bgtask.StatusInterrupted), clonedActive.Status)
	assert.Equal(t, int64(3), clonedActive.ReviveCount)
	assert.True(t, clonedActive.EndedAt.Valid)
	assert.Equal(t, tCopy, clonedActive.EndedAt.Time)
	assert.Equal(t, tCopy, clonedActive.UpdatedAt.Time)
}
