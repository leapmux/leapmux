package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/sqltime"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOutcomeAliasPreservesFinishedEvidenceAndChildActivity(t *testing.T) {
	t.Parallel()
	svc, sink := setupRootSink(t, "outcome-alias-root")
	svc.Output.processRunning = func(string) bool { return true }
	childID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{RegistryRole: bgtask.RoleChildActivity, ProviderChildKey: "child-activity", SpawnSpanID: "outcome-alias-span", Title: "Child"})
	require.NoError(t, err)
	for _, key := range []string{"invocation", "native-task"} {
		require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{Role: bgtask.RoleTaskOutcome, RowKey: key, Kind: bgtask.KindSubagent, ChildAgentID: childID, Status: bgtask.StatusRunning}))
	}
	require.NoError(t, sink.CloseBackgroundTask("invocation", bgtask.StatusSucceeded))
	before := storedRow(t, svc, "outcome-alias-root", "invocation")
	require.NoError(t, sink.RenameBackgroundTask("invocation", "native-task"))
	merged := storedRow(t, svc, "outcome-alias-root", "native-task")
	assert.Equal(t, leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_SUCCEEDED, merged.Status)
	assert.Equal(t, before.EndedAt, merged.EndedAt, "joining aliases keeps the original finished observation")
	canonical, err := svc.Queries.GetAgentBackgroundTaskByChildAgentID(t.Context(), childID)
	require.NoError(t, err)
	assert.Equal(t, "child-activity", canonical.RowKey)
	assert.Equal(t, leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING, canonical.Status)
	assert.True(t, svc.Output.AgentActivitySnapshot(childID, "outcome-alias-root").Working())
	assert.Zero(t, svc.Output.AgentActivitySnapshot("outcome-alias-root", "outcome-alias-root").ActiveTasks)
}

func TestOutcomeAliasRejectsConflictingKnownFinality(t *testing.T) {
	t.Parallel()
	svc, sink := setupRootSink(t, "conflicting-outcome-alias")
	for _, key := range []string{"source", "target"} {
		require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{Role: bgtask.RoleTaskOutcome, RowKey: key, Kind: bgtask.KindSubagent, Status: bgtask.StatusRunning}))
	}
	require.NoError(t, sink.CloseBackgroundTask("source", bgtask.StatusSucceeded))
	require.NoError(t, sink.CloseBackgroundTask("target", bgtask.StatusFailed))
	before, err := svc.Queries.ListAllAgentBackgroundTasksForResume(t.Context(), "conflicting-outcome-alias")
	require.NoError(t, err)
	assert.Error(t, sink.RenameBackgroundTask("source", "target"))
	after, err := svc.Queries.ListAllAgentBackgroundTasksForResume(t.Context(), "conflicting-outcome-alias")
	require.NoError(t, err)
	assert.Equal(t, before, after, "contradictory finished evidence cannot be deleted by alias resolution")
}

func backgroundTaskRegistryMirrorForTest(h *OutputHandler, root string) registryMirror[bgtask.Item, string] {
	cache := h.bgTaskCache(root)
	cache.Mu.Lock()
	defer cache.Mu.Unlock()
	return cache.mirrorLocked()
}

func TestOutcomeAliasRefusesIndependentChildActivity(t *testing.T) {
	t.Parallel()
	for _, linked := range []bool{false, true} {
		for _, status := range []bgtask.Status{bgtask.StatusRunning, bgtask.StatusFailed} {
			name := "unlinked/" + status.String()
			if linked {
				name = "linked/" + status.String()
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				const root = "independent-alias-roles"
				svc, sink := setupRootSink(t, root)
				svc.Output.processRunning = func(string) bool { return true }
				childID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{RegistryRole: bgtask.RoleChildActivity, ProviderChildKey: "activity", Title: "Child"})
				require.NoError(t, err)
				sourceChild := ""
				if linked {
					sourceChild = childID
				}
				require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{Role: bgtask.RoleTaskOutcome, RowKey: "task", Kind: bgtask.KindSubagent, ChildAgentID: sourceChild, Status: bgtask.StatusRunning}))
				if status == bgtask.StatusFailed {
					require.NoError(t, sink.CloseBackgroundTask("task", status))
				}
				before, err := svc.Queries.ListAllAgentBackgroundTasksForResume(t.Context(), root)
				require.NoError(t, err)
				replacement := newTestRootOutputSink(t, svc.Output, root, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
				mirror := backgroundTaskRegistryMirrorForTest(svc.Output, root)
				require.Error(t, replacement.RenameBackgroundTask("task", "activity"))
				after, err := svc.Queries.ListAllAgentBackgroundTasksForResume(t.Context(), root)
				require.NoError(t, err)
				assert.Equal(t, before, after, "role refusal cannot claim generations or discard an outcome")
				assert.Equal(t, mirror, backgroundTaskRegistryMirrorForTest(svc.Output, root))
				canonical, err := svc.Queries.GetAgentBackgroundTaskByChildAgentID(t.Context(), childID)
				require.NoError(t, err)
				assert.Equal(t, "activity", canonical.RowKey)
				expected := 0
				if status == bgtask.StatusRunning {
					expected = 1
				}
				assert.EqualValues(t, expected, svc.Output.AgentActivitySnapshot(root, root).ActiveTasks)
				assert.True(t, svc.Output.AgentActivitySnapshot(childID, root).Working())
			})
		}
	}
}

func TestBackgroundTaskAliasRestoresSourceAfterTargetWriteFailure(t *testing.T) {
	t.Parallel()
	for _, promotion := range []bool{false, true} {
		name := "outcome evidence"
		if promotion {
			name = "canonical promotion"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			const root = "alias-write-rollback"
			svc, sink := setupRootSink(t, root)
			svc.Output.processRunning = func(string) bool { return true }
			canonicalKey := "activity"
			role := bgtask.RoleChildActivity
			if promotion {
				canonicalKey = "source"
				role = bgtask.RoleChildTask
			}
			childID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{RegistryRole: role, ProviderChildKey: canonicalKey, Title: "Child"})
			require.NoError(t, err)
			if !promotion {
				require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{Role: bgtask.RoleTaskOutcome, RowKey: "source", Kind: bgtask.KindSubagent, ChildAgentID: childID, Status: bgtask.StatusRunning}))
				require.NoError(t, sink.CloseBackgroundTask("source", bgtask.StatusSucceeded))
			}
			targetStatus := bgtask.StatusRunning
			if promotion {
				targetStatus = bgtask.StatusSucceeded
			}
			require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{Role: bgtask.RoleTaskOutcome, RowKey: "target", Kind: bgtask.KindSubagent, ChildAgentID: childID, Status: targetStatus}))
			sourceBefore := storedRow(t, svc, root, "source")
			targetBefore := storedRow(t, svc, root, "target")
			replacement := newTestRootOutputSink(t, svc.Output, root, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
			before, err := svc.Queries.ListAllAgentBackgroundTasksForResume(t.Context(), root)
			require.NoError(t, err)
			mirror := backgroundTaskRegistryMirrorForTest(svc.Output, root)
			column := "status"
			value := int32(leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_SUCCEEDED)
			if promotion {
				column = "role"
				value = int32(leapmuxv1.BackgroundTaskRole_BACKGROUND_TASK_ROLE_CHILD_TASK)
			}
			// SQLite trigger conditions cannot bind parameters. The ordinal comes from the proto enum.
			trigger := fmt.Sprintf("CREATE TRIGGER refuse_alias_target BEFORE UPDATE OF %s ON agent_background_tasks WHEN NEW.row_key = 'target' AND NEW.%s = %d BEGIN SELECT RAISE(ABORT, 'alias target held'); END", column, column, value)
			_, err = svc.DB.ExecContext(t.Context(), trigger)
			require.NoError(t, err)
			require.ErrorContains(t, replacement.RenameBackgroundTask("source", "target"), "alias target held")
			after, err := svc.Queries.ListAllAgentBackgroundTasksForResume(t.Context(), root)
			require.NoError(t, err)
			assert.Equal(t, before, after, "a refused target write restores the deleted source and both generation claims")
			assert.Equal(t, mirror, backgroundTaskRegistryMirrorForTest(svc.Output, root))
			_, err = svc.DB.ExecContext(t.Context(), "DROP TRIGGER refuse_alias_target")
			require.NoError(t, err)
			require.NoError(t, replacement.RenameBackgroundTask("source", "target"))
			rows, err := svc.Queries.ListAllAgentBackgroundTasksForResume(t.Context(), root)
			require.NoError(t, err)
			for _, row := range rows {
				assert.NotEqual(t, "source", row.RowKey)
			}
			target := storedRow(t, svc, root, "target")
			assert.Greater(t, target.OutputGeneration, targetBefore.OutputGeneration)
			assert.Equal(t, leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_SUCCEEDED, target.Status)
			if promotion {
				assert.Equal(t, leapmuxv1.BackgroundTaskRole_BACKGROUND_TASK_ROLE_CHILD_TASK, target.Role)
				assert.Equal(t, targetBefore.EndedAt, target.EndedAt)
				canonical, err := svc.Queries.GetAgentBackgroundTaskByChildAgentID(t.Context(), childID)
				require.NoError(t, err)
				assert.Equal(t, "target", canonical.RowKey)
			} else {
				assert.Equal(t, leapmuxv1.BackgroundTaskRole_BACKGROUND_TASK_ROLE_TASK_OUTCOME, target.Role)
				assert.Equal(t, sourceBefore.EndedAt, target.EndedAt)
				canonical, err := svc.Queries.GetAgentBackgroundTaskByChildAgentID(t.Context(), childID)
				require.NoError(t, err)
				assert.Equal(t, "activity", canonical.RowKey)
				assert.True(t, svc.Output.AgentActivitySnapshot(childID, root).Working())
			}
		})
	}
}

func TestOutcomeAliasReadsAnOccupiedTargetOutsideTheColdDisplayCache(t *testing.T) {
	t.Parallel()
	const root = "cold-occupied-outcome-alias"
	svc, sink := setupRootSink(t, root)
	svc.Output.processRunning = func(string) bool { return true }
	childID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{RegistryRole: bgtask.RoleChildActivity, ProviderChildKey: "activity", Title: "Child"})
	require.NoError(t, err)
	for _, key := range []string{"target", "source"} {
		require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{Role: bgtask.RoleTaskOutcome, RowKey: key, Kind: bgtask.KindWorkflow, ChildAgentID: childID, Status: bgtask.StatusRunning}))
	}
	require.NoError(t, sink.CloseBackgroundTask("source", bgtask.StatusSucceeded))
	sourceBefore := storedRow(t, svc, root, "source")
	for i := range bgtask.MaxTasks {
		require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{Role: bgtask.RoleTaskOutcome, RowKey: fmt.Sprintf("newer-%03d", i), Kind: bgtask.KindWorkflow, Status: bgtask.StatusRunning}))
	}
	// Retained native work can predate the current process. Pin its ordering without a sleep.
	old := time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)
	_, err = svc.DB.ExecContext(t.Context(), "UPDATE agent_background_tasks SET created_at = ? WHERE owner_agent_id = ? AND row_key = ?", sqltime.NewSQLiteTime(old), root, "target")
	require.NoError(t, err)
	require.Equal(t, leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING, storedRow(t, svc, root, "target").Status)
	svc.Output.bgtasks.Delete(root)
	cache := svc.Output.bgTaskCache(root)
	cache.Mu.Lock()
	assert.False(t, cache.seeded)
	assert.Empty(t, cache.Rows)
	cache.Mu.Unlock()
	require.NoError(t, sink.RenameBackgroundTask("source", "target"))
	target := storedRow(t, svc, root, "target")
	assert.Equal(t, leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_SUCCEEDED, target.Status)
	assert.Equal(t, sourceBefore.EndedAt, target.EndedAt)
	assert.Equal(t, childID, target.ChildAgentID)
	rows, err := svc.Queries.ListAllAgentBackgroundTasksForResume(t.Context(), root)
	require.NoError(t, err)
	for _, row := range rows {
		assert.NotEqual(t, "source", row.RowKey)
	}
	canonical, err := svc.Queries.GetAgentBackgroundTaskByChildAgentID(t.Context(), childID)
	require.NoError(t, err)
	assert.Equal(t, "activity", canonical.RowKey)
	assert.EqualValues(t, bgtask.MaxTasks, svc.Output.AgentActivitySnapshot(root, root).ActiveTasks)
	assert.True(t, svc.Output.AgentActivitySnapshot(childID, root).Working())
}
