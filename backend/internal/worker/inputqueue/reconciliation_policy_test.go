package inputqueue

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManagerReconciliationPolicyKeepsUnflaggedSteeringRefusals(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name      string
		supported bool
		active    bool
		future    bool
		inputID   string
		expected  error
	}{
		{"unsupported idle", false, false, false, "head", ErrSteeringUnsupported},
		{"unsupported active", false, true, false, "head", ErrSteeringUnsupported},
		{"unsupported future", false, false, true, "head", ErrSteeringUnsupported},
		{"ended turn", true, false, false, "head", ErrTurnEnded},
		{"wrong head", true, true, false, "other", ErrNotHead},
		{"scheduled", true, true, true, "head", ErrScheduled},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			clock, _, store := newPriorityFixture(t)
			var scheduledAt *time.Time
			if scenario.future {
				at := clock.Now().Add(time.Hour)
				scheduledAt = &at
			}
			enqueueAt(t, store, "head", scheduledAt, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
			if scenario.active {
				_, _, err := store.TurnStarted(t.Context(), "agent-1", true)
				require.NoError(t, err)
			}
			before, err := store.Snapshot(t.Context(), "agent-1")
			require.NoError(t, err)
			var nativeCalls atomic.Int32
			dispatcher := &reconcilingDispatcher{recordingDispatcher: &recordingDispatcher{steering: scenario.supported},
				requires:  func(context.Context, string) (bool, error) { return false, nil },
				reconcile: func(context.Context, string) (bool, error) { nativeCalls.Add(1); return true, nil },
			}
			manager := NewManager(store, dispatcher, nil)
			t.Cleanup(manager.StopAndWait)
			_, err = manager.Steer(t.Context(), "agent-1", scenario.inputID)
			assert.ErrorIs(t, err, scenario.expected)
			after, err := manager.Snapshot(t.Context(), "agent-1")
			require.NoError(t, err)
			assert.Equal(t, before, after)
			assert.Zero(t, nativeCalls.Load())
			assert.Empty(t, dispatcher.steered)
		})
	}
}

func TestManagerReconciliationPolicyKeepsUnflaggedDispatchAndRetry(t *testing.T) {
	t.Parallel()
	for _, retry := range []bool{false, true} {
		label := "automatic"
		if retry {
			label = "retry"
		}
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			clock, _, store := newPriorityFixture(t)
			inputID := "head"
			if retry {
				inputID = failDueRetry(t, clock, store, `{"failure":true}`)
			} else {
				enqueueAt(t, store, inputID, nil, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
			}
			var nativeCalls atomic.Int32
			dispatcher := &reconcilingDispatcher{recordingDispatcher: &recordingDispatcher{},
				requires:  func(context.Context, string) (bool, error) { return false, nil },
				reconcile: func(context.Context, string) (bool, error) { nativeCalls.Add(1); return true, nil },
			}
			manager := NewManager(store, dispatcher, nil)
			t.Cleanup(manager.StopAndWait)
			if retry {
				_, err := manager.Retry(t.Context(), "agent-1", inputID, false)
				require.NoError(t, err)
			} else {
				manager.NotifyDependencyReady("agent-1")
			}
			eventuallyDispatches(t, dispatcher.recordingDispatcher, []string{inputID})
			waitForDrainToSettle(t, manager, "agent-1")
			assert.Zero(t, nativeCalls.Load())
			snapshot, err := manager.Snapshot(t.Context(), "agent-1")
			require.NoError(t, err)
			assert.Empty(t, snapshot.Items)
		})
	}
}

func TestManagerReconciliationRestoresCapabilityBeforePreparingASteer(t *testing.T) {
	t.Parallel()
	_, store := newStoreFixture(t)
	enqueueAt(t, store, "head", nil, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	_, _, err := store.TurnStarted(t.Context(), "agent-1", true)
	require.NoError(t, err)
	var nativeCalls atomic.Int32
	base := &recordingDispatcher{}
	base.onPrepare = func(string) { assert.Equal(t, int32(1), nativeCalls.Load()) }
	dispatcher := &reconcilingDispatcher{recordingDispatcher: base, reconcile: func(context.Context, string) (bool, error) {
		nativeCalls.Add(1)
		base.steering = true
		return true, nil
	}}
	manager := NewManager(store, dispatcher, nil)
	t.Cleanup(manager.StopAndWait)
	_, err = manager.Steer(t.Context(), "agent-1", "head")
	require.NoError(t, err)
	assert.Equal(t, []string{"agent-1"}, base.prepared)
	assert.Equal(t, []string{"head"}, base.steered)
	assert.Equal(t, int32(1), nativeCalls.Load())
}

func TestManagerReconciliationDoesNotStartForIneligibleAutomaticInput(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"empty", "future", "paused", "edited"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			clock, database, store := newPriorityFixture(t)
			if scenario != "empty" {
				var at *time.Time
				if scenario == "future" {
					value := clock.Now().Add(time.Hour)
					at = &value
				}
				enqueueAt(t, store, "head", at, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
			}
			if scenario == "paused" {
				_, err := store.SetPaused(t.Context(), "agent-1", true, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL)
				require.NoError(t, err)
			}
			if scenario == "edited" {
				_, _, _, err := store.BeginEdit(t.Context(), "agent-1", "head", "client", false)
				require.NoError(t, err)
			}
			before, err := store.Snapshot(t.Context(), "agent-1")
			require.NoError(t, err)
			var nativeCalls atomic.Int32
			dispatcher := &reconcilingDispatcher{recordingDispatcher: &recordingDispatcher{}, reconcile: func(context.Context, string) (bool, error) {
				nativeCalls.Add(1)
				return true, nil
			}}
			manager := NewManager(store, dispatcher, nil)
			t.Cleanup(manager.StopAndWait)
			manager.NotifyDependencyReady("agent-1")
			waitForDrainToSettle(t, manager, "agent-1")
			after, err := manager.Snapshot(t.Context(), "agent-1")
			require.NoError(t, err)
			assert.Equal(t, before, after)
			assert.Zero(t, nativeCalls.Load())
			assert.Empty(t, dispatchedIDs(dispatcher.recordingDispatcher))
			if scenario == "empty" {
				var rows int
				require.NoError(t, database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM agent_input_queue_state`).Scan(&rows))
				assert.Zero(t, rows, "readiness must not create queue state")
			}
		})
	}
}

func TestManagerReconciliationFailurePreservesNativeStateAndIndependentPauses(t *testing.T) {
	t.Parallel()
	for _, transition := range []string{"plain", "active", "manual", "archive"} {
		t.Run(transition, func(t *testing.T) {
			t.Parallel()
			database, store := newStoreFixture(t)
			var manager *Manager
			dispatcher := &reconcilingDispatcher{recordingDispatcher: &recordingDispatcher{}, reconcile: func(ctx context.Context, _ string) (bool, error) {
				var err error
				switch transition {
				case "active":
					_, err = manager.TurnStarted(ctx, "agent-1", false)
				case "manual":
					_, err = manager.SetPaused(ctx, "agent-1", true)
				case "archive":
					_, err = manager.PauseForArchive(ctx, "agent-1")
				}
				assert.NoError(t, err)
				return false, assert.AnError
			}}
			manager = NewManager(store, dispatcher, nil)
			t.Cleanup(manager.StopAndWait)
			_, err := manager.Enqueue(t.Context(), NewItem{ID: "head", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "Continue"})
			require.NoError(t, err)
			waitForDrainToSettle(t, manager, "agent-1")
			snapshot, err := manager.Snapshot(t.Context(), "agent-1")
			require.NoError(t, err)
			assert.True(t, snapshot.Paused)
			assert.Equal(t, transition == "active", snapshot.ActiveTurn)
			expectedOwner := pauseOwnerAgentStopped
			expectedReason := leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED
			switch transition {
			case "manual":
				expectedOwner = pauseOwnerManual
				expectedReason = leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL
			case "archive":
				expectedOwner = pauseOwnerArchive
			}
			assert.Equal(t, expectedReason, snapshot.PauseReason)
			var owner pauseOwner
			require.NoError(t, database.QueryRowContext(t.Context(), `SELECT pause_owner FROM agent_input_queue_state WHERE agent_id = 'agent-1'`).Scan(&owner))
			assert.Equal(t, expectedOwner, owner)
			require.Len(t, snapshot.Items, 1)
			assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, snapshot.Items[0].State)
			assert.Zero(t, snapshot.Items[0].ReservedSeq)
			assert.Empty(t, dispatchedIDs(dispatcher.recordingDispatcher))
		})
	}
}

func TestManagerReconciliationFailureCommitsAfterCallerCancellation(t *testing.T) {
	t.Parallel()
	_, store := newStoreFixture(t)
	enqueueAt(t, store, "head", nil, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	manager := NewManager(store, &recordingDispatcher{}, nil)
	t.Cleanup(manager.StopAndWait)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	coordinator := manager.coordinator("agent-1")
	coordinator.mu.Lock()
	err := manager.recordReconciliationFailureLocked(ctx, coordinator, "agent-1", assert.AnError)
	manager.unlockAndPublish(coordinator)
	assert.ErrorIs(t, err, assert.AnError)
	snapshot, err := manager.Snapshot(t.Context(), "agent-1")
	require.NoError(t, err)
	assert.True(t, snapshot.Paused)
	assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED, snapshot.PauseReason)
	require.Len(t, snapshot.Items, 1)
	assert.Zero(t, snapshot.Items[0].ReservedSeq)
}
