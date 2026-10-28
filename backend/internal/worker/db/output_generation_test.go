package db_test

import (
	"database/sql"
	"math"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func outputGenerationQueries(t *testing.T) (*sql.DB, *db.Queries) {
	t.Helper()
	store := integerEnumStore(t)
	queries := db.New(store)
	require.NoError(t, queries.CreateAgent(t.Context(), db.CreateAgentParams{ID: "generation-root", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE}))
	return store, queries
}

func TestOutputGenerationAllocationUsesTheStoredRootSource(t *testing.T) {
	t.Parallel()
	store, queries := outputGenerationQueries(t)
	require.NoError(t, queries.UpdateAgentSessionID(t.Context(), db.UpdateAgentSessionIDParams{ID: "generation-root", AgentSessionID: "checked-native-session"}))
	first, err := queries.AllocateAgentOutputGeneration(t.Context(), "generation-root")
	require.NoError(t, err)
	assert.Equal(t, int64(1), first.OutputGeneration)
	assert.Equal(t, "checked-native-session", first.AgentSessionID)
	second, err := queries.AllocateAgentOutputGeneration(t.Context(), "generation-root")
	require.NoError(t, err)
	assert.Equal(t, int64(2), second.OutputGeneration)
	require.NoError(t, queries.CreateChildAgent(t.Context(), db.CreateChildAgentParams{
		ID: "generation-child", ParentAgentID: sql.NullString{String: "generation-root", Valid: true}, SpawnSpanID: "native-child",
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	_, err = queries.AllocateAgentOutputGeneration(t.Context(), "generation-child")
	assert.ErrorIs(t, err, sql.ErrNoRows)
	child, err := queries.GetAgentByID(t.Context(), "generation-child")
	require.NoError(t, err)
	assert.Zero(t, child.OutputGeneration)
	_, err = queries.AllocateAgentOutputGeneration(t.Context(), "absent-root")
	assert.ErrorIs(t, err, sql.ErrNoRows)
	for _, invalid := range []any{-1, 0.5, nil} {
		_, err = store.ExecContext(t.Context(), "UPDATE agents SET output_generation = ? WHERE id = ?", invalid, "generation-root")
		assert.Error(t, err, "the durable counter must remain a nonnegative integer")
	}
	_, err = store.ExecContext(t.Context(), "UPDATE agents SET output_generation = ? WHERE id = ?", int64(math.MaxInt64), "generation-root")
	require.NoError(t, err)
	_, err = queries.AllocateAgentOutputGeneration(t.Context(), "generation-root")
	assert.ErrorIs(t, err, sql.ErrNoRows, "exhaustion must not wrap or convert the counter to REAL")
	root, err := queries.GetAgentByID(t.Context(), "generation-root")
	require.NoError(t, err)
	assert.Equal(t, int64(math.MaxInt64), root.OutputGeneration)
}

func TestOutputGenerationConcurrentAllocationsAreDistinct(t *testing.T) {
	t.Parallel()
	_, queries := outputGenerationQueries(t)
	const count = 24
	results := make(chan int64, count)
	errors := make(chan error, count)
	var group sync.WaitGroup
	for range count {
		group.Go(func() {
			row, err := queries.AllocateAgentOutputGeneration(t.Context(), "generation-root")
			errors <- err
			results <- row.OutputGeneration
		})
	}
	group.Wait()
	close(errors)
	close(results)
	for err := range errors {
		require.NoError(t, err)
	}
	var generations []int64
	for generation := range results {
		generations = append(generations, generation)
	}
	sort.Slice(generations, func(a, b int) bool { return generations[a] < generations[b] })
	for index, generation := range generations {
		assert.Equal(t, int64(index+1), generation)
	}
}

func TestOutputGenerationCounterSurvivesDatabaseReopen(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "worker.sqlite")
	store, err := workerdb.Open(path, sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, workerdb.Migrate(t.Context(), store))
	queries := db.New(store)
	require.NoError(t, queries.CreateAgent(t.Context(), db.CreateAgentParams{ID: "generation-root", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE}))
	first, err := queries.AllocateAgentOutputGeneration(t.Context(), "generation-root")
	require.NoError(t, err)
	stamp := sqltime.NewSQLiteTime(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	task := db.UpsertAgentBackgroundTaskParams{OwnerAgentID: "generation-root", RowKey: "shared", OutputGeneration: first.OutputGeneration, Seq: 1,
		Kind: leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_WORKFLOW, Title: "Before Worker restart", Status: leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
		CreatedAt: stamp, UpdatedAt: stamp}
	_, err = queries.UpsertAgentBackgroundTask(t.Context(), task)
	require.NoError(t, err)
	require.NoError(t, store.Close())
	reopened, err := workerdb.Open(path, sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	require.NoError(t, workerdb.Migrate(t.Context(), reopened))
	current := db.New(reopened)
	second, err := current.AllocateAgentOutputGeneration(t.Context(), "generation-root")
	require.NoError(t, err)
	assert.Greater(t, second.OutputGeneration, first.OutputGeneration)
	replacement := task
	replacement.OutputGeneration, replacement.Title = second.OutputGeneration, "After Worker restart"
	_, err = current.UpsertAgentBackgroundTask(t.Context(), replacement)
	require.NoError(t, err)
	rows, err := current.UpsertAgentBackgroundTask(t.Context(), task)
	require.NoError(t, err)
	assert.Zero(t, rows, "the persisted counter prevents an old captured generation from matching the replacement")
	stored, err := current.GetAgentBackgroundTaskByRowKey(t.Context(), db.GetAgentBackgroundTaskByRowKeyParams{OwnerAgentID: "generation-root", RowKey: "shared"})
	require.NoError(t, err)
	assert.Equal(t, "After Worker restart", stored.Title)
	assert.Equal(t, second.OutputGeneration, stored.OutputGeneration)
}

func TestBackgroundTaskQueriesRejectAnOlderPhysicalWriter(t *testing.T) {
	for _, operation := range []string{"upsert", "status", "stamp", "close", "revive", "rename", "claim", "delete"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			_, queries := outputGenerationQueries(t)
			_, err := queries.AllocateAgentOutputGeneration(t.Context(), "generation-root")
			require.NoError(t, err)
			_, err = queries.AllocateAgentOutputGeneration(t.Context(), "generation-root")
			require.NoError(t, err)
			stamp := sqltime.NewSQLiteTime(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
			status := leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING
			if operation == "revive" || operation == "stamp" {
				status = leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_SUCCEEDED
			}
			task := db.UpsertAgentBackgroundTaskParams{
				OwnerAgentID: "generation-root", OutputGeneration: 2, RowKey: "shared", Seq: 1,
				Kind: leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT, Title: "Replacement assignment", Status: status,
				CreatedAt: stamp, UpdatedAt: stamp,
			}
			written, err := queries.UpsertAgentBackgroundTask(t.Context(), task)
			require.NoError(t, err)
			require.Equal(t, int64(1), written)
			before, err := queries.GetAgentBackgroundTaskByRowKey(t.Context(), db.GetAgentBackgroundTaskByRowKeyParams{OwnerAgentID: "generation-root", RowKey: "shared"})
			require.NoError(t, err)
			var affected int64
			switch operation {
			case "upsert":
				task.OutputGeneration, task.Title = 1, "Obsolete assignment"
				affected, err = queries.UpsertAgentBackgroundTask(t.Context(), task)
			case "status":
				affected, err = queries.UpdateAgentBackgroundTaskStatus(t.Context(), db.UpdateAgentBackgroundTaskStatusParams{OwnerAgentID: "generation-root", RowKey: "shared", OutputGeneration: 1, Status: leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_FAILED, UpdatedAt: stamp})
			case "stamp":
				affected, err = queries.StampAgentBackgroundTaskEndedAt(t.Context(), db.StampAgentBackgroundTaskEndedAtParams{OwnerAgentID: "generation-root", RowKey: "shared", OutputGeneration: 1, MinFinalStatus: leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_SUCCEEDED, EndedAt: sqltime.SQLiteNullTimeOf(stamp.Time)})
			case "close":
				affected, err = queries.CloseAgentBackgroundTask(t.Context(), db.CloseAgentBackgroundTaskParams{OwnerAgentID: "generation-root", RowKey: "shared", OutputGeneration: 1, MinFinalStatus: leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_SUCCEEDED, Status: leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_STOPPED, EndedAt: sqltime.SQLiteNullTimeOf(stamp.Time), UpdatedAt: stamp})
			case "revive":
				affected, err = queries.ReviveAgentBackgroundTask(t.Context(), db.ReviveAgentBackgroundTaskParams{OwnerAgentID: "generation-root", RowKey: "shared", OutputGeneration: 1, MinFinalStatus: leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_SUCCEEDED, Status: leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING, UpdatedAt: stamp})
			case "rename":
				affected, err = queries.RenameAgentBackgroundTask(t.Context(), db.RenameAgentBackgroundTaskParams{OwnerAgentID: "generation-root", OldRowKey: "shared", NewRowKey: "obsolete", OutputGeneration: 1})
			case "claim":
				affected, err = queries.ClaimAgentBackgroundTaskOutputGeneration(t.Context(), db.ClaimAgentBackgroundTaskOutputGenerationParams{OwnerAgentID: "generation-root", RowKey: "shared", OutputGeneration: 1})
			case "delete":
				affected, err = queries.DeleteAgentBackgroundTaskForGeneration(t.Context(), db.DeleteAgentBackgroundTaskForGenerationParams{OwnerAgentID: "generation-root", RowKey: "shared", OutputGeneration: int64(1)})
			}
			require.NoError(t, err)
			assert.Zero(t, affected)
			after, err := queries.GetAgentBackgroundTaskByRowKey(t.Context(), db.GetAgentBackgroundTaskByRowKeyParams{OwnerAgentID: "generation-root", RowKey: "shared"})
			require.NoError(t, err)
			assert.Equal(t, before, after)
		})
	}
}

func TestBackgroundTaskGenerationZeroIsOnlyUnclaimedHistory(t *testing.T) {
	t.Parallel()
	store, queries := outputGenerationQueries(t)
	_, allocationErr := queries.AllocateAgentOutputGeneration(t.Context(), "generation-root")
	require.NoError(t, allocationErr)
	stamp := sqltime.NewSQLiteTime(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	rows, err := queries.UpsertAgentBackgroundTask(t.Context(), db.UpsertAgentBackgroundTaskParams{
		OwnerAgentID: "generation-root", RowKey: "runtime", Seq: 1, OutputGeneration: 0,
		Kind:   leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT,
		Status: leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING, CreatedAt: stamp, UpdatedAt: stamp,
	})
	require.NoError(t, err)
	assert.Zero(t, rows, "a runtime statement cannot fabricate an unclaimed writer")
	_, err = store.ExecContext(t.Context(), `INSERT INTO agent_background_tasks (owner_agent_id, output_generation, row_key, seq, kind, status) VALUES ('generation-root', 0, 'cloned', 1, 1, 4)`)
	require.NoError(t, err)
	claimed, err := queries.ClaimAgentBackgroundTaskOutputGeneration(t.Context(), db.ClaimAgentBackgroundTaskOutputGenerationParams{OwnerAgentID: "generation-root", RowKey: "cloned", OutputGeneration: 1})
	require.NoError(t, err)
	assert.Equal(t, int64(1), claimed)
	for _, invalid := range []any{-1, 0.5, nil} {
		_, err = store.ExecContext(t.Context(), "UPDATE agent_background_tasks SET output_generation = ? WHERE owner_agent_id = ?", invalid, "generation-root")
		assert.Error(t, err)
	}
}

func TestBackgroundTaskCloneDoesNotCopyPhysicalWriterOwnership(t *testing.T) {
	t.Parallel()
	_, queries := outputGenerationQueries(t)
	_, err := queries.AllocateAgentOutputGeneration(t.Context(), "generation-root")
	require.NoError(t, err)
	source, err := queries.AllocateAgentOutputGeneration(t.Context(), "generation-root")
	require.NoError(t, err)
	stamp := sqltime.NewSQLiteTime(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	_, err = queries.UpsertAgentBackgroundTask(t.Context(), db.UpsertAgentBackgroundTaskParams{
		OwnerAgentID: "generation-root", OutputGeneration: source.OutputGeneration, RowKey: "native-workflow", Seq: 1,
		Kind: leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_WORKFLOW, Title: "Archived workflow", Status: leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING,
		CreatedAt: stamp, UpdatedAt: stamp,
	})
	require.NoError(t, err)
	require.NoError(t, queries.CreateAgent(t.Context(), db.CreateAgentParams{ID: "cloned-root", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE}))
	rows, err := queries.CloneAgentBackgroundTaskForResume(t.Context(), db.CloneAgentBackgroundTaskForResumeParams{
		OldRootID: "generation-root", NewRootID: "cloned-root", RowKey: "native-workflow",
		MinFinalStatus:    leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_SUCCEEDED,
		InterruptedStatus: leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_INTERRUPTED, CopyTime: stamp,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
	clone, err := queries.GetAgentBackgroundTaskByRowKey(t.Context(), db.GetAgentBackgroundTaskByRowKeyParams{OwnerAgentID: "cloned-root", RowKey: "native-workflow"})
	require.NoError(t, err)
	assert.Zero(t, clone.OutputGeneration)
	assert.Equal(t, leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_INTERRUPTED, clone.Status)
	root, err := queries.GetAgentByID(t.Context(), "cloned-root")
	require.NoError(t, err)
	assert.Zero(t, root.OutputGeneration)
	next, err := queries.AllocateAgentOutputGeneration(t.Context(), "cloned-root")
	require.NoError(t, err)
	assert.Equal(t, int64(1), next.OutputGeneration)
	rows, err = queries.ClaimAgentBackgroundTaskOutputGeneration(t.Context(), db.ClaimAgentBackgroundTaskOutputGenerationParams{
		OwnerAgentID: "cloned-root", RowKey: "native-workflow", OutputGeneration: next.OutputGeneration,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), rows)
	original, err := queries.GetAgentBackgroundTaskByRowKey(t.Context(), db.GetAgentBackgroundTaskByRowKeyParams{OwnerAgentID: "generation-root", RowKey: "native-workflow"})
	require.NoError(t, err)
	assert.Equal(t, source.OutputGeneration, original.OutputGeneration)
	assert.Equal(t, leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING, original.Status)
}
