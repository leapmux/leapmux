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

func TestManagerReconciliationRechecksChangedCandidateIdentityAndKind(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"identity", "kind"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			_, store := newStoreFixture(t)
			_, err := store.Enqueue(t.Context(), NewItem{
				ID: "clear", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CLEAR_CONTEXT,
				Text: "/clear", ReclassifyOnEdit: true,
			})
			require.NoError(t, err)
			var manager *Manager
			var mu sync.Mutex
			var requests []QueueReconciliationRequest
			dispatcher := &reconcilingDispatcher{recordingDispatcher: &recordingDispatcher{}}
			dispatcher.candidate = func(ctx context.Context, request QueueReconciliationRequest) (bool, error) {
				mu.Lock()
				requests = append(requests, request)
				mu.Unlock()
				if request.Kind != leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CLEAR_CONTEXT {
					return true, nil
				}
				if change == "identity" {
					if _, err := manager.Delete(ctx, "agent-1", "clear"); err != nil {
						return false, err
					}
					_, err := manager.Enqueue(ctx, NewItem{ID: "user", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "Continue"})
					return false, err
				}
				edit, _, _, err := store.BeginEdit(ctx, "agent-1", "clear", "client", false)
				if err != nil {
					return false, err
				}
				_, err = store.Update(ctx, UpdateRequest{
					AgentID: "agent-1", InputID: "clear", ClientID: "client", ExpectedVersion: edit.Items[0].Version,
					Text: "Continue", Schedule: KeepSchedule(),
				})
				return false, err
			}
			dispatcher.dispatch = func(item DispatchItem) {
				assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, item.Kind)
				mu.Lock()
				assert.Len(t, requests, 2, "the clear exemption must not authorize a different candidate")
				mu.Unlock()
			}
			manager = NewManager(store, dispatcher, nil)
			t.Cleanup(manager.StopAndWait)
			manager.NotifyDependencyReady("agent-1")
			expectedID := "clear"
			if change == "identity" {
				expectedID = "user"
			}
			eventuallyDispatches(t, dispatcher.recordingDispatcher, []string{expectedID})
			waitForDrainToSettle(t, manager, "agent-1")
			mu.Lock()
			actual := append([]QueueReconciliationRequest(nil), requests...)
			mu.Unlock()
			assert.Equal(t, []QueueReconciliationRequest{
				{AgentID: "agent-1", InputID: "clear", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CLEAR_CONTEXT},
				{AgentID: "agent-1", InputID: expectedID, Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE},
			}, actual)
		})
	}
}

func TestManagerReconciliationRetryPublishesUserOwnershipBeforeNativeIO(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	clock, _, store := newPriorityFixture(t)
	item := scheduleKeyedUsage(t, store, "native:A", clock.Now().Add(time.Hour)).Items[0]
	clock.Set(*item.ScheduledAt).MustWait(ctx)
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	_, err = store.FailDispatch(ctx, "agent-1", item.ID, assert.AnError, false)
	require.NoError(t, err)
	_, err = store.SetPaused(ctx, "agent-1", true, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL)
	require.NoError(t, err)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseNative := func() { releaseOnce.Do(func() { close(release) }) }
	dispatcher := &reconcilingDispatcher{recordingDispatcher: &recordingDispatcher{}, candidate: func(ctx context.Context, request QueueReconciliationRequest) (bool, error) {
		assert.Equal(t, item.ID, request.InputID)
		assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT, request.Kind)
		close(entered)
		select {
		case <-release:
			return true, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}}
	observer := &recordingObserver{}
	manager := NewManager(store, dispatcher, observer)
	t.Cleanup(manager.StopAndWait)
	t.Cleanup(releaseNative)
	finished := make(chan error, 1)
	go func() { _, err := manager.Retry(ctx, "agent-1", item.ID, false); finished <- err }()
	select {
	case <-entered:
	case err := <-finished:
		require.NoError(t, err)
		t.Fatal("retry finished before native synchronization")
	case <-ctx.Done():
		t.Fatal("retry did not reach native synchronization")
	}
	queued := storedItem(t, store, item.ID)
	assert.Empty(t, queued.AutoRetryKey, "the explicit retry must become user intent before synchronization")
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, queued.State)
	assert.Zero(t, queued.ReservedSeq)
	observer.mu.Lock()
	publications := append([]Snapshot(nil), observer.snapshots...)
	observer.mu.Unlock()
	require.NotEmpty(t, publications)
	require.Len(t, publications[0].Items, 1)
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, publications[0].Items[0].State)
	assert.Zero(t, publications[0].Items[0].ReservedSeq)
	assert.Empty(t, dispatchedIDs(dispatcher.recordingDispatcher))
	releaseNative()
	select {
	case err := <-finished:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("retry did not finish after native synchronization")
	}
	assert.Equal(t, []string{item.ID}, dispatchedIDs(dispatcher.recordingDispatcher))
	snapshot, err := manager.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	assert.Empty(t, snapshot.Items)
	assert.True(t, snapshot.Paused, "sending one explicit retry must preserve the independent manual pause")
}

func TestManagerReconciliationFinishesWhenNativeSyncDeletesTheOnlyCandidate(t *testing.T) {
	t.Parallel()
	database, store := newStoreFixture(t)
	enqueueAt(t, store, "head", nil, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE)
	var manager *Manager
	var calls atomic.Int32
	dispatcher := &reconcilingDispatcher{recordingDispatcher: &recordingDispatcher{}, reconcile: func(ctx context.Context, _ string) (bool, error) {
		calls.Add(1)
		_, err := manager.Delete(ctx, "agent-1", "head")
		return true, err
	}}
	manager = NewManager(store, dispatcher, nil)
	t.Cleanup(manager.StopAndWait)
	manager.NotifyDependencyReady("agent-1")
	waitForDrainToSettle(t, manager, "agent-1")
	snapshot, err := manager.Snapshot(t.Context(), "agent-1")
	require.NoError(t, err)
	assert.Empty(t, snapshot.Items)
	assert.Empty(t, dispatchedIDs(dispatcher.recordingDispatcher))
	assert.Equal(t, int32(1), calls.Load())
	var transcriptRows int
	require.NoError(t, database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM messages WHERE agent_id = 'agent-1'`).Scan(&transcriptRows))
	assert.Zero(t, transcriptRows)
}

func TestManagerReconciliationRetryReturnsAChangedCandidateWithoutDispatch(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"identity", "kind"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			_, store := newStoreFixture(t)
			_, err := store.Enqueue(t.Context(), NewItem{
				ID: "clear", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CLEAR_CONTEXT,
				Text: "/clear", ReclassifyOnEdit: true,
			})
			require.NoError(t, err)
			prepared, _, err := store.PrepareDispatch(t.Context(), "agent-1", false)
			require.NoError(t, err)
			require.NotNil(t, prepared)
			_, err = store.FailDispatch(t.Context(), "agent-1", "clear", assert.AnError, false)
			require.NoError(t, err)
			_, err = store.SetPaused(t.Context(), "agent-1", true, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL)
			require.NoError(t, err)
			var manager *Manager
			var calls atomic.Int32
			dispatcher := &reconcilingDispatcher{recordingDispatcher: &recordingDispatcher{}, candidate: func(ctx context.Context, request QueueReconciliationRequest) (bool, error) {
				calls.Add(1)
				if request.Kind != leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CLEAR_CONTEXT {
					return true, nil
				}
				if change == "identity" {
					if _, err := manager.Delete(ctx, "agent-1", "clear"); err != nil {
						return false, err
					}
					_, err := manager.Enqueue(ctx, NewItem{ID: "replacement", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "Continue"})
					return false, err
				}
				edit, _, _, err := store.BeginEdit(ctx, "agent-1", "clear", "client", false)
				if err != nil {
					return false, err
				}
				_, err = store.Update(ctx, UpdateRequest{
					AgentID: "agent-1", InputID: "clear", ClientID: "client", ExpectedVersion: edit.Items[0].Version,
					Text: "Continue", Schedule: KeepSchedule(),
				})
				return false, err
			}}
			dispatcher.dispatch = func(item DispatchItem) {
				assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, item.Kind)
				assert.Equal(t, int32(2), calls.Load(), "the later ordinary dispatch needs its own reconciliation")
			}
			manager = NewManager(store, dispatcher, nil)
			t.Cleanup(manager.StopAndWait)
			snapshot, err := manager.Retry(t.Context(), "agent-1", "clear", false)
			require.NoError(t, err)
			require.Len(t, snapshot.Items, 1)
			assert.True(t, snapshot.Paused)
			assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL, snapshot.PauseReason)
			expectedID := "clear"
			if change == "identity" {
				expectedID = "replacement"
			}
			assert.Equal(t, expectedID, snapshot.Items[0].ID)
			assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, snapshot.Items[0].Kind)
			assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, snapshot.Items[0].State)
			assert.Zero(t, snapshot.Items[0].ReservedSeq)
			assert.Empty(t, dispatchedIDs(dispatcher.recordingDispatcher))
			assert.Equal(t, int32(1), calls.Load())
			_, err = manager.SetPaused(t.Context(), "agent-1", false)
			require.NoError(t, err)
			eventuallyDispatches(t, dispatcher.recordingDispatcher, []string{expectedID})
			waitForDrainToSettle(t, manager, "agent-1")
			assert.Equal(t, int32(2), calls.Load())
		})
	}
}
