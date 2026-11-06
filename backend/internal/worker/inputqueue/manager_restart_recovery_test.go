package inputqueue

import (
	"context"
	"database/sql"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type queueRestartTestMode struct {
	name   string
	begin  func(context.Context, *Manager) (*PlannedRestart, error)
	finish func(context.Context, *PlannedRestart) error
}

func queueRestartTestModes() []queueRestartTestMode {
	return []queueRestartTestMode{
		{
			name: "planned restart",
			begin: func(ctx context.Context, manager *Manager) (*PlannedRestart, error) {
				return manager.BeginPlannedRestart(ctx, "agent-1")
			},
			finish: func(ctx context.Context, restart *PlannedRestart) error {
				return restart.Finish(ctx, true, true)
			},
		},
		{
			name: "input recovery",
			begin: func(ctx context.Context, manager *Manager) (*PlannedRestart, error) {
				return manager.BeginInputRecovery(ctx, "agent-1")
			},
			finish: func(ctx context.Context, restart *PlannedRestart) error {
				return restart.FinishInputRecovery(ctx, true, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_UNSPECIFIED)
			},
		},
	}
}

func readQueueRestarting(t *testing.T, database *sql.DB) bool {
	t.Helper()
	var restarting bool
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT restarting FROM agent_input_queue_state WHERE agent_id = 'agent-1'").Scan(&restarting))
	return restarting
}

func seedFailedRestartInput(t *testing.T, store *Store) {
	t.Helper()
	_, err := store.Enqueue(t.Context(), NewItem{ID: "failed", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "retry this only when requested"})
	require.NoError(t, err)
	prepared, _, err := store.PrepareDispatch(t.Context(), "agent-1", false)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	_, err = store.FailDispatch(t.Context(), "agent-1", "failed", assert.AnError, false)
	require.NoError(t, err)
}

func rejectRestartFinalization(t *testing.T, database *sql.DB) func() {
	t.Helper()
	_, err := database.ExecContext(t.Context(), `CREATE TRIGGER fail_restart_finish BEFORE UPDATE OF restarting ON agent_input_queue_state
WHEN OLD.restarting = 1 AND NEW.restarting = 0 BEGIN SELECT RAISE(ABORT, 'restart finalization failed'); END`)
	require.NoError(t, err)
	restore := func() {
		_, err := database.ExecContext(context.Background(), "DROP TRIGGER IF EXISTS fail_restart_finish")
		assert.NoError(t, err)
	}
	t.Cleanup(restore)
	return restore
}

func TestManagerRejectsOverlappingRestartGuards(t *testing.T) {
	for _, firstMode := range queueRestartTestModes() {
		for _, secondMode := range queueRestartTestModes() {
			t.Run(firstMode.name+" then "+secondMode.name, func(t *testing.T) {
				t.Parallel()
				database, store := newStoreFixture(t)
				manager := NewManager(store, &recordingDispatcher{}, &recordingObserver{})
				t.Cleanup(manager.StopAndWait)
				first, err := firstMode.begin(t.Context(), manager)
				require.NoError(t, err)
				t.Cleanup(func() { _ = firstMode.finish(context.Background(), first) })
				before, err := manager.Snapshot(t.Context(), "agent-1")
				require.NoError(t, err)
				require.True(t, readQueueRestarting(t, database))
				second, err := secondMode.begin(t.Context(), manager)
				if second != nil {
					t.Cleanup(func() { _ = secondMode.finish(context.Background(), second) })
				}
				require.ErrorIs(t, err, ErrPlannedRestart)
				assert.Nil(t, second)
				during, err := manager.Snapshot(t.Context(), "agent-1")
				require.NoError(t, err)
				assert.Equal(t, before, during, "a refused restart cannot change the first guard's pause or revision")
				assert.True(t, readQueueRestarting(t, database))
				require.NoError(t, firstMode.finish(t.Context(), first))
				after, err := manager.Snapshot(t.Context(), "agent-1")
				require.NoError(t, err)
				assert.False(t, after.Paused)
				assert.False(t, readQueueRestarting(t, database))
			})
		}
	}
}

func TestManagerResumeRepairsACompletedRestartMarkerAfterFinishFailure(t *testing.T) {
	for _, mode := range queueRestartTestModes() {
		t.Run(mode.name, func(t *testing.T) {
			t.Parallel()
			database, store := newStoreFixture(t)
			dispatcher := &recordingDispatcher{}
			manager := NewManager(store, dispatcher, &recordingObserver{})
			t.Cleanup(manager.StopAndWait)
			seedFailedRestartInput(t, store)
			restart, err := mode.begin(t.Context(), manager)
			require.NoError(t, err)
			t.Cleanup(func() { _ = mode.finish(context.Background(), restart) })
			restore := rejectRestartFinalization(t, database)
			err = mode.finish(t.Context(), restart)
			require.ErrorContains(t, err, "restart finalization failed")
			require.True(t, readQueueRestarting(t, database), "the failed transaction keeps the durable restart marker")
			coordinator := manager.coordinator("agent-1")
			coordinator.mu.Lock()
			activeRestarts := coordinator.plannedRestarts
			coordinator.mu.Unlock()
			assert.Zero(t, activeRestarts, "the completed guard no longer owns a native restart")
			restore()
			_, err = store.SetPaused(t.Context(), "agent-1", false, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_UNSPECIFIED)
			require.NoError(t, err)
			assert.True(t, readQueueRestarting(t, database), "the raw store cannot judge whether a restart still runs")
			resumed, err := manager.SetPaused(t.Context(), "agent-1", false)
			require.NoError(t, err)
			assert.False(t, readQueueRestarting(t, database), "explicit resume clears a marker that no active guard owns")
			require.Len(t, resumed.Items, 1)
			assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_FAILED, resumed.Items[0].State, "resume never replays failed input")
			_, err = manager.Retry(t.Context(), "agent-1", "failed", false)
			require.NoError(t, err, "the stale marker cannot refuse the user's later retry")
			dispatcher.mu.Lock()
			assert.Equal(t, []string{"failed"}, dispatcher.dispatched)
			dispatcher.mu.Unlock()
		})
	}
}

func TestManagerResumePreservesAnActiveRestartMarker(t *testing.T) {
	for _, mode := range queueRestartTestModes() {
		t.Run(mode.name, func(t *testing.T) {
			t.Parallel()
			database, store := newStoreFixture(t)
			manager := NewManager(store, &recordingDispatcher{}, &recordingObserver{})
			t.Cleanup(manager.StopAndWait)
			seedFailedRestartInput(t, store)
			restart, err := mode.begin(t.Context(), manager)
			require.NoError(t, err)
			t.Cleanup(func() { _ = mode.finish(context.Background(), restart) })
			_, err = manager.SetPaused(t.Context(), "agent-1", false)
			require.NoError(t, err)
			assert.True(t, readQueueRestarting(t, database), "resume cannot remove an active guard's delivery barrier")
			_, err = manager.Retry(t.Context(), "agent-1", "failed", false)
			require.ErrorIs(t, err, ErrPlannedRestart)
			require.NoError(t, mode.finish(t.Context(), restart))
			assert.False(t, readQueueRestarting(t, database))
		})
	}
}

func TestManagerInputRecoveryAdoptsAnOrphanedRestartMarker(t *testing.T) {
	for _, firstMode := range queueRestartTestModes() {
		for _, completed := range []bool{false, true} {
			outcome := map[bool]string{false: "refused recovery", true: "completed recovery"}[completed]
			t.Run(firstMode.name+" then "+outcome, func(t *testing.T) {
				t.Parallel()
				database, store := newStoreFixture(t)
				manager := NewManager(store, &recordingDispatcher{}, &recordingObserver{})
				t.Cleanup(manager.StopAndWait)
				first, err := firstMode.begin(t.Context(), manager)
				require.NoError(t, err)
				t.Cleanup(func() { _ = firstMode.finish(context.Background(), first) })
				restore := rejectRestartFinalization(t, database)
				require.ErrorContains(t, firstMode.finish(t.Context(), first), "restart finalization failed")
				orphan := readQueuePauseState(t, database)
				require.Equal(t, pauseOwnerPlannedRestart, orphan.owner)
				require.True(t, readQueueRestarting(t, database))
				restore()
				second, err := manager.BeginInputRecovery(t.Context(), "agent-1")
				require.NoError(t, err, "a later recovery approval must adopt a marker that has no active guard")
				t.Cleanup(func() {
					_ = second.FinishInputRecovery(context.Background(), false, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED)
				})
				failureReason := leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED
				if completed {
					failureReason = leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_UNSPECIFIED
				}
				require.NoError(t, second.FinishInputRecovery(t.Context(), completed, failureReason))
				after := readQueuePauseState(t, database)
				if completed {
					assert.Equal(t, pauseOwnerNone, after.owner)
					assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_UNSPECIFIED, after.reason)
				} else {
					assert.Equal(t, pauseOwnerAgentStopped, after.owner, "a refused recovery cannot restore authority to a guard that already ended")
					assert.Equal(t, orphan.reason, after.reason)
				}
				assert.False(t, readQueueRestarting(t, database))
			})
		}
	}
}
