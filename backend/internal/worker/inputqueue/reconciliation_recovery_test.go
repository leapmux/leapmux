package inputqueue

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManagerReconciliationRecoveryReleasesOnlyActualNativeSuccess(t *testing.T) {
	t.Parallel()
	interrupted := leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_INTERRUPTED
	stopped := leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED
	manual := leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL
	for _, scenario := range []struct {
		name      string
		required  bool
		actual    bool
		failure   error
		key       string
		active    bool
		manual    bool
		remaining int
		reason    leapmuxv1.AgentInputQueuePauseReason
	}{
		{"unflagged", false, false, nil, "native:A", false, false, 1, interrupted},
		{"no actual synchronization", true, false, nil, "native:A", false, false, 1, interrupted},
		{"success", true, true, nil, "native:A", false, false, 0, 0},
		{"manual pause during synchronization", true, true, nil, "native:A", false, true, 0, manual},
		{"failure", true, false, assert.AnError, "native:A", false, false, 1, stopped},
		{"failure after active report", true, false, assert.AnError, "native:A", true, false, 1, stopped},
		{"failure after manual pause", true, false, assert.AnError, "native:A", false, true, 1, manual},
		{"newer owner", true, true, nil, "native:B", true, false, 1, 0},
		{"unkeyed intent", true, true, nil, "", true, false, 1, 0},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			clock, _, store := newPriorityFixture(t)
			scheduleKeyedUsage(t, store, "native:A", clock.Now().Add(time.Hour))
			if scenario.key != "native:A" {
				scheduleKeyedUsage(t, store, scenario.key, clock.Now().Add(2*time.Hour))
			}
			_, _, err := store.TurnStarted(t.Context(), "agent-1", false)
			require.NoError(t, err)
			var manager *Manager
			var calls atomic.Int32
			dispatcher := &reconcilingDispatcher{recordingDispatcher: &recordingDispatcher{},
				requires: func(context.Context, string) (bool, error) { return scenario.required, nil },
				reconcile: func(ctx context.Context, _ string) (bool, error) {
					calls.Add(1)
					if scenario.active {
						_, err := manager.TurnStarted(ctx, "agent-1", false)
						if err != nil {
							return false, err
						}
					}
					if scenario.manual {
						if _, err := manager.SetPaused(ctx, "agent-1", true); err != nil {
							return false, err
						}
					}
					if scenario.failure != nil || !scenario.actual {
						return scenario.actual, scenario.failure
					}
					return true, manager.CancelUsageLimitRetryWithKey(ctx, "agent-1", "native:A", "native A completed", nil)
				},
			}
			manager = NewManager(store, dispatcher, nil)
			t.Cleanup(manager.StopAndWait)
			require.NoError(t, manager.RecoverState(t.Context()))
			err = manager.DrainRecovered(t.Context())
			if scenario.failure == nil {
				require.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, scenario.failure)
			}
			waitForDrainToSettle(t, manager, "agent-1")
			snapshot, err := manager.Snapshot(t.Context(), "agent-1")
			require.NoError(t, err)
			assert.Equal(t, scenario.reason != 0, snapshot.Paused)
			assert.Equal(t, scenario.reason, snapshot.PauseReason)
			assert.Equal(t, scenario.active, snapshot.ActiveTurn)
			require.Len(t, snapshot.Items, scenario.remaining)
			if scenario.remaining != 0 {
				item := storedItem(t, store, snapshot.Items[0].ID)
				assert.Equal(t, scenario.key, item.AutoRetryKey)
				assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, item.State)
				assert.Zero(t, item.ReservedSeq)
			}
			expectedCalls := int32(0)
			if scenario.required {
				expectedCalls = 1
			}
			assert.Equal(t, expectedCalls, calls.Load())
			assert.Empty(t, dispatchedIDs(dispatcher.recordingDispatcher))
			manager.lifecycleMu.Lock()
			assert.Empty(t, manager.recovered, "completed recovery must release its in-memory holds on every outcome")
			manager.lifecycleMu.Unlock()
		})
	}
}

func TestManagerReconciliationRecoveryLeavesUncertainDeliveryForTheUser(t *testing.T) {
	t.Parallel()
	_, store := newStoreFixture(t)
	enqueueAt(t, store, "head", nil, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	prepared, _, err := store.PrepareDispatch(t.Context(), "agent-1", false)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	var calls atomic.Int32
	dispatcher := &reconcilingDispatcher{recordingDispatcher: &recordingDispatcher{}, reconcile: func(context.Context, string) (bool, error) {
		calls.Add(1)
		return true, nil
	}}
	manager := NewManager(store, dispatcher, nil)
	t.Cleanup(manager.StopAndWait)
	require.NoError(t, manager.RecoverState(t.Context()))
	require.NoError(t, manager.DrainRecovered(t.Context()))
	snapshot, err := manager.Snapshot(t.Context(), "agent-1")
	require.NoError(t, err)
	assert.True(t, snapshot.Paused)
	assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_DELIVERY_UNCERTAIN, snapshot.PauseReason)
	require.Len(t, snapshot.Items, 1)
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DELIVERY_UNCERTAIN, snapshot.Items[0].State)
	assert.Zero(t, calls.Load())
	assert.Empty(t, dispatchedIDs(dispatcher.recordingDispatcher))
}

func TestManagerReconciliationRecoverySerializesConcurrentPasses(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	clock, _, store := newPriorityFixture(t)
	scheduleKeyedUsage(t, store, "native:A", clock.Now().Add(time.Hour))
	_, _, err := store.TurnStarted(ctx, "agent-1", false)
	require.NoError(t, err)
	entered, release := make(chan struct{}, 2), make(chan struct{})
	var releaseOnce sync.Once
	releaseNative := func() { releaseOnce.Do(func() { close(release) }) }
	var manager *Manager
	var calls atomic.Int32
	dispatcher := &reconcilingDispatcher{recordingDispatcher: &recordingDispatcher{}, reconcile: func(ctx context.Context, _ string) (bool, error) {
		calls.Add(1)
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return false, ctx.Err()
		}
		return true, manager.CancelUsageLimitRetryWithKey(ctx, "agent-1", "native:A", "native A completed", nil)
	}}
	manager = NewManager(store, dispatcher, nil)
	t.Cleanup(manager.StopAndWait)
	t.Cleanup(releaseNative)
	require.NoError(t, manager.RecoverState(ctx))
	finished := make(chan error, 2)
	go func() { finished <- manager.DrainRecovered(ctx) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the first pass did not reach native synchronization")
	}
	locked := manager.recoveryMu.TryLock()
	if locked {
		manager.recoveryMu.Unlock()
	}
	assert.False(t, locked, "the recovery pass must retain serialization through native synchronization")
	secondStarted := make(chan struct{})
	go func() { close(secondStarted); finished <- manager.DrainRecovered(ctx) }()
	select {
	case <-secondStarted:
	case <-ctx.Done():
		t.Fatal("the second recovery caller did not start")
	}
	manager.lifecycleMu.Lock()
	_, held := manager.recovered["agent-1"]
	manager.lifecycleMu.Unlock()
	assert.True(t, held)
	releaseNative()
	for range 2 {
		select {
		case err := <-finished:
			require.NoError(t, err)
		case <-ctx.Done():
			t.Fatal("concurrent recovery passes did not finish")
		}
	}
	assert.Equal(t, int32(1), calls.Load())
	snapshot, err := manager.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	assert.Empty(t, snapshot.Items)
	assert.False(t, snapshot.Paused)
}
