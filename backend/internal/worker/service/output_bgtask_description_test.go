package service

import (
	"fmt"
	"strings"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBackgroundTaskDescriptionPreservesLifecycleAndDisplayAdmission(t *testing.T) {
	for _, status := range []bgtask.Status{bgtask.StatusPending, bgtask.StatusRunning, bgtask.StatusPaused, bgtask.StatusSucceeded, bgtask.StatusFailed, bgtask.StatusStopped, bgtask.StatusInterrupted, bgtask.StatusEndedWithUnknownOutcome} {
		for _, cold := range []bool{false, true} {
			t.Run(fmt.Sprintf("status=%d/cold=%v", status, cold), func(t *testing.T) {
				t.Parallel()
				root := fmt.Sprintf("description-%d-%v", status, cold)
				svc, sink := setupRootSink(t, root)
				require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: "task", Kind: bgtask.KindShell, Title: "Build", Description: "Original", ActiveForm: "Building", Status: status}))
				before := storedRow(t, svc, root, "task")
				cache := svc.Output.bgTaskCache(root)
				if cold {
					cache.Mu.Lock()
					require.True(t, cache.dropRowLocked("task"))
					cache.Mu.Unlock()
				}
				require.NoError(t, sink.UpdateBackgroundTaskDescription("task", "Actual profile"))
				after := storedRow(t, svc, root, "task")
				assert.Equal(t, "Actual profile", after.Description)
				after.Description = before.Description
				after.UpdatedAt = before.UpdatedAt
				assert.Equal(t, before, after, "descriptive metadata cannot alter any lifecycle or linkage field")
				cache.Mu.Lock()
				index := cache.indexOf("task")
				if cold {
					assert.Equal(t, -1, index, "metadata cannot re-admit a stored row into the display")
				} else if assert.GreaterOrEqual(t, index, 0) {
					assert.Equal(t, "Actual profile", cache.Rows[index].Description)
				}
				cache.Mu.Unlock()
			})
		}
	}
}

func TestBackgroundTaskDescriptionHandlesMissingEmptyAndLargeValues(t *testing.T) {
	t.Parallel()
	svc, sink := setupRootSink(t, "description-boundaries")
	require.NoError(t, sink.UpdateBackgroundTaskDescription("missing", "No row"))
	_, _, exists, err := sink.LookupBackgroundTask("missing")
	require.NoError(t, err)
	assert.False(t, exists)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: "task", Kind: bgtask.KindShell, Title: "Build", Description: "Original", Status: bgtask.StatusRunning}))
	for _, description := range []string{"", "0", strings.Repeat("界", bgtask.LabelByteLimit), "read\x00profile"} {
		require.NoError(t, sink.UpdateBackgroundTaskDescription("task", description))
		row := storedRow(t, svc, "description-boundaries", "task")
		assert.Equal(t, (bgtask.Upsert{Description: description}).Clean().Description, row.Description)
		assert.EqualValues(t, bgtask.StatusRunning, row.Status)
	}
}

func TestBackgroundTaskDescriptionKeepsCacheAndStoreAfterRefusal(t *testing.T) {
	t.Parallel()
	svc, sink := setupRootSink(t, "description-refusal")
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: "task", Kind: bgtask.KindShell, Title: "Build", Description: "Original", Status: bgtask.StatusRunning}))
	before := storedRow(t, svc, "description-refusal", "task")
	_, err := svc.DB.ExecContext(t.Context(), `CREATE TRIGGER refuse_description BEFORE UPDATE OF description ON agent_background_tasks BEGIN SELECT RAISE(ABORT, 'description held'); END`)
	require.NoError(t, err)
	require.ErrorContains(t, sink.UpdateBackgroundTaskDescription("task", "Replacement"), "description held")
	assert.Equal(t, before, storedRow(t, svc, "description-refusal", "task"))
	cache := svc.Output.bgTaskCache("description-refusal")
	cache.Mu.Lock()
	require.Len(t, cache.Rows, 1)
	assert.Equal(t, "Original", cache.Rows[0].Description)
	cache.Mu.Unlock()
	_, err = svc.DB.ExecContext(t.Context(), "DROP TRIGGER refuse_description")
	require.NoError(t, err)
	replacement := newTestRootOutputSink(t, svc.Output, "description-refusal", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	require.NoError(t, replacement.UpdateBackgroundTaskStatus("task", bgtask.StatusRunning, ""))
	newer := storedRow(t, svc, "description-refusal", "task")
	require.NoError(t, sink.UpdateBackgroundTaskDescription("task", "Stale metadata"))
	assert.Equal(t, newer, storedRow(t, svc, "description-refusal", "task"))
}
