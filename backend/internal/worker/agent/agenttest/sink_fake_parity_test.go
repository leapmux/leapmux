package agenttest

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// The fake uses the same row keys as production, including derived keys for unusable provider keys.
//
// The upsert comments in this file state the complete rule.
// Each step must match production, or a provider test can assert a contract the registry does not keep.
// NormalizeRowKey supplies the first step.
// agentOutputSink.applyAndBroadcast applies it before the closure runs.
// This test verifies that step instead of depending on a reader to remember it.
func TestTestSinkKeysRowsTheWayProductionDoes(t *testing.T) {
	t.Parallel()

	sink := &Sink{}
	raw := strings.Repeat("a", bgtask.RowKeyByteLimit+1)
	derived := bgtask.NormalizeRowKey(raw)
	require.NotEqual(t, raw, derived, "the case must be one the rule refuses")

	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: raw, Kind: bgtask.KindShell, Title: "derived", Status: bgtask.StatusRunning,
	}))

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, derived, rows[0].RowKey, "the fake must store the key production stores")

	// Each lifecycle call addresses the row through the original provider key.
	// Each call must derive the same stored key.
	// A fake that normalizes only on upsert would hide this failure.
	require.NoError(t, sink.UpdateBackgroundTaskStatus(raw, bgtask.StatusRunning, "working"))
	require.NoError(t, sink.CloseBackgroundTask(raw, bgtask.StatusSucceeded))

	rows = sink.BackgroundTasks()
	require.Len(t, rows, 1, "the close must find the row the upsert opened")
	assert.Equal(t, bgtask.StatusSucceeded, rows[0].Status)
	assert.Equal(t, "working", rows[0].ActiveForm)
}

// The status log records each distinct write, so a fast close cannot hide the earlier Running write.
// No-op and absorbed writes must add no log entry.
func TestTestSinkRecordsDistinctBackgroundTaskStatuses(t *testing.T) {
	t.Parallel()

	sink := &Sink{}
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "term_1", Kind: bgtask.KindShell, Title: "printf hi", Status: bgtask.StatusRunning,
	}))
	// Same status again: active-form-only update must not pad the trail.
	require.NoError(t, sink.UpdateBackgroundTaskStatus("term_1", bgtask.StatusRunning, "still going"))
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "term_1", Kind: bgtask.KindShell, Title: "printf hi", Status: bgtask.StatusRunning,
	}))
	require.NoError(t, sink.CloseBackgroundTask("term_1", bgtask.StatusSucceeded))
	// First close wins: a second close must not append.
	require.NoError(t, sink.CloseBackgroundTask("term_1", bgtask.StatusFailed))
	// Absorbed non-final upsert must not append either.
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "term_1", Kind: bgtask.KindShell, Title: "printf hi", Status: bgtask.StatusRunning,
	}))

	sink.bgTasksMu.Lock()
	log := append([]bgtask.Status(nil), sink.bgTaskStatuses["term_1"]...)
	row := sink.bgTasks["term_1"]
	sink.bgTasksMu.Unlock()

	assert.Equal(t, []bgtask.Status{bgtask.StatusRunning, bgtask.StatusSucceeded}, log)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)

	require.NoError(t, sink.ReviveBackgroundTask("term_1"))
	sink.bgTasksMu.Lock()
	log = append([]bgtask.Status(nil), sink.bgTaskStatuses["term_1"]...)
	sink.bgTasksMu.Unlock()
	assert.Equal(t, []bgtask.Status{
		bgtask.StatusRunning, bgtask.StatusSucceeded, bgtask.StatusRunning,
	}, log)
}

// Only the root can write a goal.
// The production sink refuses a child's write instead of redirecting it.
// A child's objective must never replace the session's objective.
// The fake must refuse that write also.
//
// A fake that accepts the write would hide the defect.
// A provider could select a child sink and write a goal through it, and Goals() would still return that goal.
// Every provider goal test in this package would then pass.
// Only one service test exercises the real sink, and no parser test reaches it.
func TestSinkFakeParity_AChildSinkRefusesEveryGoalWrite(t *testing.T) {
	t.Parallel()
	sink := &Sink{}
	child := sink.Child("child-1")

	child.UpsertGoal(agent.GoalUpdate{Objective: "subagent objective", Status: agent.GoalStatusActive})
	child.ClearGoal(false)
	child.PublishGoalCapabilities()

	assert.Empty(t, child.Goals(), "a child sink records no goal")
	assert.Zero(t, child.GoalClears())
	assert.Zero(t, child.GoalCapabilityPublishes())
	assert.Empty(t, sink.Goals(), "and it must not redirect the write to its root either")

	// The root itself still accepts, so the guard refuses a child and nothing
	// else.
	sink.UpsertGoal(agent.GoalUpdate{Objective: "session objective", Status: agent.GoalStatusActive})
	assert.Len(t, sink.Goals(), 1)
}
