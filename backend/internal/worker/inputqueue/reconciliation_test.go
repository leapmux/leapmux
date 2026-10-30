package inputqueue

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type reconcilingDispatcher struct {
	*recordingDispatcher
	reconcile func(context.Context, string) (bool, error)
	requires  func(context.Context, string) (bool, error)
	candidate func(context.Context, QueueReconciliationRequest) (bool, error)
	dispatch  func(DispatchItem)
	onPolicy  func()
	supports  func(string) bool
}

func (dispatcher *reconcilingDispatcher) SteerPolicy(agentID string) agent.SteerPolicy {
	if dispatcher.onPolicy != nil {
		dispatcher.onPolicy()
	}
	return dispatcher.recordingDispatcher.SteerPolicy(agentID)
}

func (dispatcher *reconcilingDispatcher) SupportsSteering(agentID string) bool {
	if dispatcher.supports != nil {
		return dispatcher.supports(agentID)
	}
	return dispatcher.recordingDispatcher.SupportsSteering(agentID)
}

func TestManagerReconciliationCannotBeSkippedWhenDeadlineMatures(t *testing.T) {
	t.Parallel()
	clock, _, store := newPriorityFixture(t)
	due := clock.Now().Add(time.Hour)
	enqueueAt(t, store, "future", &due, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	var nativeCalls atomic.Int32
	dispatcher := &reconcilingDispatcher{
		recordingDispatcher: &recordingDispatcher{},
		reconcile: func(context.Context, string) (bool, error) {
			nativeCalls.Add(1)
			return true, nil
		},
		onPolicy: func() {
			// This is the current gap between readiness and claim. The native
			// clock change must not turn a skipped check into an admitted input.
			if clock.Now().Before(due) {
				clock.Set(due)
			}
			assert.Positive(t, nativeCalls.Load(), "preparation must follow native synchronization")
		},
	}
	manager := NewManager(store, dispatcher, nil)
	t.Cleanup(manager.StopAndWait)
	manager.NotifyDependencyReady("agent-1")
	waitForDrainToSettle(t, manager, "agent-1")
	if clock.Now().Before(due) {
		advanceClock(t, clock, due.Sub(clock.Now()))
	}
	eventuallyDispatches(t, dispatcher.recordingDispatcher, []string{"future"})
	assert.Equal(t, int32(1), nativeCalls.Load())
}

func TestManagerReconciliationSteerRefusalCannotBecomeAnUncheckedClaim(t *testing.T) {
	t.Parallel()
	_, store := newStoreFixture(t)
	enqueueAt(t, store, "input", nil, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	var nativeCalls atomic.Int32
	dispatcher := &reconcilingDispatcher{
		recordingDispatcher: &recordingDispatcher{steering: true},
		reconcile: func(context.Context, string) (bool, error) {
			nativeCalls.Add(1)
			return true, nil
		},
		supports: func(string) bool {
			_, _, err := store.TurnStarted(t.Context(), "agent-1", true)
			assert.NoError(t, err)
			return true
		},
	}
	manager := NewManager(store, dispatcher, nil)
	t.Cleanup(manager.StopAndWait)
	_, err := manager.Steer(t.Context(), "agent-1", "input")
	require.ErrorIs(t, err, ErrTurnEnded)
	assert.Zero(t, nativeCalls.Load())
	assert.Empty(t, dispatcher.steered)
	_, _, err = store.TurnStarted(t.Context(), "agent-1", true)
	require.NoError(t, err)
	_, err = manager.Steer(t.Context(), "agent-1", "input")
	require.NoError(t, err)
	assert.Equal(t, int32(1), nativeCalls.Load())
	assert.Equal(t, []string{"input"}, dispatcher.steered)
}

func TestManagerReconciliationSteerWithoutQueueStateIsRejected(t *testing.T) {
	t.Parallel()
	_, store := newStoreFixture(t)
	var calls atomic.Int32
	dispatcher := &reconcilingDispatcher{recordingDispatcher: &recordingDispatcher{steering: true}, reconcile: func(context.Context, string) (bool, error) {
		calls.Add(1)
		return true, nil
	}}
	manager := NewManager(store, dispatcher, nil)
	t.Cleanup(manager.StopAndWait)
	_, err := manager.Steer(t.Context(), "agent-1", "missing")
	assert.ErrorIs(t, err, ErrNotHead)
	assert.Zero(t, calls.Load())
	assert.Empty(t, dispatcher.steered)
}

func (dispatcher *reconcilingDispatcher) RequiresQueueReconciliation(ctx context.Context, agentID string) (bool, error) {
	if dispatcher.requires != nil {
		return dispatcher.requires(ctx, agentID)
	}
	return true, nil
}

func TestManagerReconciliationNeverSteersAnInvalidInputIdentity(t *testing.T) {
	t.Parallel()
	for _, policy := range []string{"absent", "false", "true"} {
		for _, input := range []struct{ name, id string }{{"empty", ""}, {"NUL", "bad\x00id"}, {"too long", strings.Repeat("x", maxQueueIdentityByteCount+1)}} {
			inputID := input.id
			t.Run(policy+"/"+input.name, func(t *testing.T) {
				t.Parallel()
				database, store := newStoreFixture(t)
				enqueueAt(t, store, "head", nil, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
				_, _, err := store.TurnStarted(t.Context(), "agent-1", true)
				require.NoError(t, err)
				before, err := store.Snapshot(t.Context(), "agent-1")
				require.NoError(t, err)
				base := &recordingDispatcher{steering: true}
				var nativeCalls atomic.Int32
				var dispatcher Dispatcher = base
				if policy != "absent" {
					dispatcher = &reconcilingDispatcher{recordingDispatcher: base,
						requires: func(context.Context, string) (bool, error) { return policy == "true", nil },
						reconcile: func(context.Context, string) (bool, error) {
							nativeCalls.Add(1)
							return true, nil
						},
					}
				}
				manager := NewManager(store, dispatcher, nil)
				t.Cleanup(manager.StopAndWait)
				_, err = manager.Steer(t.Context(), "agent-1", inputID)
				assert.ErrorIs(t, err, ErrNotHead)
				after, err := manager.Snapshot(t.Context(), "agent-1")
				require.NoError(t, err)
				assert.Equal(t, before, after)
				assert.Empty(t, base.steered)
				assert.Zero(t, nativeCalls.Load())
				var transcriptRows int
				require.NoError(t, database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM messages WHERE id = 'head'`).Scan(&transcriptRows))
				assert.Zero(t, transcriptRows)
			})
		}
	}
}

func TestManagerReconciliationKeepsAStoreFaultPauseReason(t *testing.T) {
	t.Parallel()
	_, store := newStoreFixture(t)
	var nativeCalls atomic.Int32
	dispatcher := &reconcilingDispatcher{recordingDispatcher: &recordingDispatcher{},
		requires: func(context.Context, string) (bool, error) {
			return false, &DeliveryError{Err: assert.AnError, Outcome: DispatchNotReady, PauseReason: leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_STORE_FAULT}
		},
		reconcile: func(context.Context, string) (bool, error) { nativeCalls.Add(1); return true, nil },
	}
	manager := NewManager(store, dispatcher, nil)
	t.Cleanup(manager.StopAndWait)
	_, err := manager.Enqueue(t.Context(), NewItem{ID: "head", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "Continue"})
	require.NoError(t, err)
	waitForDrainToSettle(t, manager, "agent-1")
	snapshot, err := manager.Snapshot(t.Context(), "agent-1")
	require.NoError(t, err)
	assert.True(t, snapshot.Paused)
	assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_STORE_FAULT, snapshot.PauseReason)
	require.Len(t, snapshot.Items, 1)
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, snapshot.Items[0].State)
	assert.Zero(t, snapshot.Items[0].ReservedSeq)
	assert.Zero(t, nativeCalls.Load())
	assert.Empty(t, dispatchedIDs(dispatcher.recordingDispatcher))
}

func TestManagerReconciliationFailureClassifiesMalformedAndWrappedHints(t *testing.T) {
	t.Parallel()
	stopped := leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED
	storeFault := leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_STORE_FAULT
	var typedNil *DeliveryError
	for _, scenario := range []struct {
		name    string
		failure error
		reason  leapmuxv1.AgentInputQueuePauseReason
	}{
		{"plain", assert.AnError, stopped},
		{"typed nil", typedNil, stopped},
		{"negative reason", &DeliveryError{Err: assert.AnError, Outcome: DispatchNotReady, PauseReason: -1}, stopped},
		{"unknown positive reason", &DeliveryError{Err: assert.AnError, Outcome: DispatchNotReady, PauseReason: 1 << 30}, stopped},
		{"zero reason", &DeliveryError{Err: assert.AnError, Outcome: DispatchNotReady}, stopped},
		{"wrapped store fault", fmt.Errorf("policy lookup failed: %w", &DeliveryError{Err: assert.AnError, Outcome: DispatchNotReady, PauseReason: storeFault}), storeFault},
		{"wrong outcome", &DeliveryError{Err: assert.AnError, Outcome: DispatchFailed, PauseReason: storeFault}, stopped},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			_, store := newStoreFixture(t)
			enqueueAt(t, store, "head", nil, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
			dispatcher := &recordingDispatcher{}
			manager := NewManager(store, dispatcher, nil)
			t.Cleanup(manager.StopAndWait)
			coordinator := manager.coordinator("agent-1")
			var failure error
			assert.NotPanics(t, func() {
				coordinator.mu.Lock()
				defer coordinator.mu.Unlock()
				failure = manager.recordReconciliationFailureLocked(t.Context(), coordinator, "agent-1", scenario.failure)
			})
			manager.drainPublications(coordinator)
			assert.ErrorIs(t, failure, scenario.failure)
			snapshot, err := manager.Snapshot(t.Context(), "agent-1")
			require.NoError(t, err)
			assert.True(t, snapshot.Paused)
			assert.Equal(t, scenario.reason, snapshot.PauseReason)
			require.Len(t, snapshot.Items, 1)
			assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, snapshot.Items[0].State)
			assert.Zero(t, snapshot.Items[0].ReservedSeq)
			assert.Empty(t, dispatchedIDs(dispatcher))
		})
	}
}

func TestStoreReconciliationPauseRejectsInvalidDirectReasons(t *testing.T) {
	t.Parallel()
	for _, reason := range []leapmuxv1.AgentInputQueuePauseReason{0, -1, 1 << 30} {
		t.Run(fmt.Sprint(reason), func(t *testing.T) {
			t.Parallel()
			_, store := newStoreFixture(t)
			enqueueAt(t, store, "head", nil, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
			before, err := store.Snapshot(t.Context(), "agent-1")
			require.NoError(t, err)
			_, changed, err := store.PauseForReconciliationFailure(t.Context(), "agent-1", reason)
			assert.ErrorIs(t, err, ErrInvalidInput)
			assert.False(t, changed)
			after, err := store.Snapshot(t.Context(), "agent-1")
			require.NoError(t, err)
			assert.Equal(t, before, after)
		})
	}
}

func (dispatcher *reconcilingDispatcher) ReconcileBeforeDispatch(ctx context.Context, request QueueReconciliationRequest) (bool, error) {
	if dispatcher.candidate != nil {
		return dispatcher.candidate(ctx, request)
	}
	return dispatcher.reconcile(ctx, request.AgentID)
}

func (dispatcher *reconcilingDispatcher) Dispatch(item DispatchItem) (DispatchResult, error) {
	if dispatcher.dispatch != nil {
		dispatcher.dispatch(item)
	}
	return dispatcher.recordingDispatcher.Dispatch(item)
}

func TestManagerReconciliationPrecedesEveryQueueClaim(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	_, store := newStoreFixture(t)
	entered, release, dispatched := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseNative := func() { releaseOnce.Do(func() { close(release) }) }
	dispatcher := &reconcilingDispatcher{
		recordingDispatcher: &recordingDispatcher{dispatchStarted: dispatched},
		reconcile: func(ctx context.Context, _ string) (bool, error) {
			close(entered)
			select {
			case <-release:
				return true, nil
			case <-ctx.Done():
				return false, ctx.Err()
			}
		},
	}
	manager := NewManager(store, dispatcher, nil)
	t.Cleanup(manager.StopAndWait)
	t.Cleanup(releaseNative)
	_, err := manager.Enqueue(ctx, NewItem{ID: "input", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "Continue"})
	require.NoError(t, err)
	select {
	case <-entered:
	case <-dispatched:
		t.Fatal("the queue dispatched before native reconciliation")
	case <-ctx.Done():
		t.Fatal("native reconciliation did not start")
	}
	item := storedItem(t, store, "input")
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, item.State)
	assert.Zero(t, item.ReservedSeq)
	// Each operation must finish while native synchronization is held. Running
	// them separately also identifies which operation regressed its lock scope.
	for _, operation := range []struct {
		name string
		run  func() error
	}{
		{"snapshot", func() error { _, err := manager.Snapshot(ctx, "agent-1"); return err }},
		{"pause", func() error { _, err := manager.SetPaused(ctx, "agent-1", true); return err }},
		{"delete", func() error { _, err := manager.Delete(ctx, "agent-1", "input"); return err }},
		{"native active publication", func() error { _, err := manager.TurnStarted(ctx, "agent-1", true); return err }},
	} {
		finished := make(chan error, 1)
		go func() { finished <- operation.run() }()
		select {
		case err := <-finished:
			require.NoError(t, err, operation.name)
		case <-ctx.Done():
			t.Fatalf("%s blocked behind native reconciliation", operation.name)
		}
	}
	releaseNative()
	waitForDrainToSettle(t, manager, "agent-1")
	assert.Empty(t, dispatchedIDs(dispatcher.recordingDispatcher))
	snapshot, err := manager.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	assert.True(t, snapshot.Paused)
	assert.True(t, snapshot.ActiveTurn)
	assert.Empty(t, snapshot.Items)
}

func TestManagerRecoveryPreservesAPauseFromBeforeTheRestart(t *testing.T) {
	t.Parallel()
	for _, archive := range []bool{false, true} {
		label := "manual"
		if archive {
			label = "archive"
		}
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			clock, database, store := newPriorityFixture(t)
			scheduleKeyedUsage(t, store, "native:A", clock.Now().Add(time.Hour))
			_, _, err := store.TurnStarted(t.Context(), "agent-1", true)
			require.NoError(t, err)
			if archive {
				_, _, err = store.PauseForArchive(t.Context(), "agent-1")
				// Archive cleanup normally ends the native turn. Preserve the
				// old active fact to model a restart during that cleanup.
				require.NoError(t, err)
				_, err = database.ExecContext(t.Context(), `UPDATE agent_input_queue_state SET active_turn = 1 WHERE agent_id = 'agent-1'`)
			} else {
				_, err = store.SetPaused(t.Context(), "agent-1", true, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL)
			}
			require.NoError(t, err)
			before, err := store.Snapshot(t.Context(), "agent-1")
			require.NoError(t, err)
			var beforeOwner pauseOwner
			require.NoError(t, database.QueryRowContext(t.Context(), `SELECT pause_owner FROM agent_input_queue_state WHERE agent_id = 'agent-1'`).Scan(&beforeOwner))
			var calls atomic.Int32
			dispatcher := &reconcilingDispatcher{recordingDispatcher: &recordingDispatcher{}, reconcile: func(context.Context, string) (bool, error) {
				calls.Add(1)
				return true, nil
			}}
			manager := NewManager(store, dispatcher, nil)
			t.Cleanup(manager.StopAndWait)
			require.NoError(t, manager.RecoverState(t.Context()))
			require.NoError(t, manager.DrainRecovered(t.Context()))
			after, err := manager.Snapshot(t.Context(), "agent-1")
			require.NoError(t, err)
			assert.True(t, after.Paused)
			assert.Equal(t, before.PauseReason, after.PauseReason)
			assert.False(t, after.ActiveTurn)
			var afterOwner pauseOwner
			require.NoError(t, database.QueryRowContext(t.Context(), `SELECT pause_owner FROM agent_input_queue_state WHERE agent_id = 'agent-1'`).Scan(&afterOwner))
			assert.Equal(t, beforeOwner, afterOwner)
			assert.Zero(t, calls.Load(), "this pass must not start a manually paused or archived runtime")
			assert.Empty(t, dispatchedIDs(dispatcher.recordingDispatcher))
		})
	}
}

func TestManagerRecoverySynchronizesBeforeReleasingItsHold(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	clock, _, store := newPriorityFixture(t)
	scheduleKeyedUsage(t, store, "native:A", clock.Now().Add(time.Hour))
	_, _, err := store.TurnStarted(ctx, "agent-1", true)
	require.NoError(t, err)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseNative := func() { releaseOnce.Do(func() { close(release) }) }
	var manager *Manager
	dispatcher := &reconcilingDispatcher{recordingDispatcher: &recordingDispatcher{}, reconcile: func(ctx context.Context, _ string) (bool, error) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return false, ctx.Err()
		}
		if err := manager.CancelUsageLimitRetryWithKey(ctx, "agent-1", "native:A", "native A completed", nil); err != nil {
			return false, err
		}
		return true, nil
	}}
	manager = NewManager(store, dispatcher, nil)
	t.Cleanup(manager.StopAndWait)
	t.Cleanup(releaseNative)
	require.NoError(t, manager.RecoverState(ctx))
	finished := make(chan error, 1)
	go func() { finished <- manager.DrainRecovered(ctx) }()
	select {
	case <-entered:
	case err := <-finished:
		require.NoError(t, err)
		t.Fatal("recovery completed without native synchronization")
	case <-ctx.Done():
		t.Fatal("recovery did not reach native synchronization")
	}
	manager.lifecycleMu.Lock()
	_, held := manager.recovered["agent-1"]
	manager.lifecycleMu.Unlock()
	assert.True(t, held)
	item := storedItem(t, store, pendingStoreRetry(t, store).ID)
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, item.State)
	assert.Zero(t, item.ReservedSeq)
	releaseNative()
	select {
	case err := <-finished:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("recovery did not complete after native synchronization")
	}
	snapshot, err := manager.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	assert.False(t, snapshot.Paused)
	assert.Empty(t, snapshot.Items)
	assert.Empty(t, dispatchedIDs(dispatcher.recordingDispatcher))
}

func pendingStoreRetry(t *testing.T, store *Store) SnapshotItem {
	t.Helper()
	snapshot, err := store.Snapshot(t.Context(), "agent-1")
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	return snapshot.Items[0]
}
