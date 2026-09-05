package inputqueue

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

type recordingDispatcher struct {
	mu              sync.Mutex
	dispatched      []string
	dispatchRelease <-chan struct{}
	dispatchStarted chan struct{}
	steered         []string
	fail            error
	steering        bool
	steerStarted    chan struct{}
	steerRelease    <-chan struct{}
	steerFail       error
}

func (d *recordingDispatcher) Dispatch(item Item) (DispatchResult, error) {
	d.mu.Lock()
	d.dispatched = append(d.dispatched, item.ID)
	started, release, dispatchErr := d.dispatchStarted, d.dispatchRelease, d.fail
	d.mu.Unlock()
	if started != nil {
		close(started)
	}
	if release != nil {
		<-release
	}
	return DispatchResult{StartsTurn: true}, dispatchErr
}

func (d *recordingDispatcher) Steer(item Item) (DispatchResult, error) {
	d.mu.Lock()
	d.steered = append(d.steered, item.ID)
	started, release, steerFail := d.steerStarted, d.steerRelease, d.steerFail
	d.mu.Unlock()
	if started != nil {
		close(started)
	}
	if release != nil {
		<-release
	}
	return DispatchResult{StartsTurn: true}, steerFail
}

func (d *recordingDispatcher) SupportsSteering(string) bool { return d.steering }

func (d *recordingDispatcher) dispatches() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.dispatched...)
}

type recordingObserver struct {
	mu        sync.Mutex
	accepted  []AcceptedTranscript
	snapshots []Snapshot
}

func (o *recordingObserver) QueueChanged(snapshot Snapshot) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.snapshots = append(o.snapshots, snapshot)
}

func (o *recordingObserver) InputAccepted(message AcceptedTranscript) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.accepted = append(o.accepted, message)
}

func TestManagerDispatchesOneTurnAtATime(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	dispatcher := &recordingDispatcher{}
	manager := NewManager(store, dispatcher, &recordingObserver{})
	ctx := context.Background()
	for _, inputID := range []string{"one", "two"} {
		_, err := manager.Enqueue(ctx, NewItem{ID: inputID, AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: inputID})
		require.NoError(t, err)
	}
	require.Eventually(t, func() bool { return len(dispatcher.dispatches()) == 1 }, time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"one"}, dispatcher.dispatches())
	_, err := manager.TurnEnded(ctx, "agent-1")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(dispatcher.dispatches()) == 2 }, time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"one", "two"}, dispatcher.dispatches())
}

func TestManagerWaitsForExternallyStartedChildTurn(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	dispatcher := &recordingDispatcher{}
	manager := NewManager(store, dispatcher, &recordingObserver{})
	ctx := context.Background()
	_, err := manager.TurnStarted(ctx, "agent-1")
	require.NoError(t, err)
	_, err = manager.Enqueue(ctx, NewItem{ID: "next", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "next"})
	require.NoError(t, err)
	assert.Never(t, func() bool { return len(dispatcher.dispatches()) > 0 }, 50*time.Millisecond, 5*time.Millisecond)
	_, err = manager.TurnEnded(ctx, "agent-1")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(dispatcher.dispatches()) == 1 }, time.Second, 10*time.Millisecond)
}

func TestManagerWaitsForActiveTurnBeforeCompactOperation(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	dispatcher := &recordingDispatcher{}
	manager := NewManager(store, dispatcher, &recordingObserver{})
	ctx := context.Background()
	_, err := manager.Enqueue(ctx, NewItem{ID: "message", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "message"})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(dispatcher.dispatches()) == 1 }, time.Second, 10*time.Millisecond)
	_, err = manager.Enqueue(ctx, NewItem{ID: "compact", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_COMPACT_CONTEXT, Text: "/compact"})
	require.NoError(t, err)
	assert.Never(t, func() bool { return len(dispatcher.dispatches()) > 1 }, 50*time.Millisecond, 5*time.Millisecond)
	_, err = manager.TurnEnded(ctx, "agent-1")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(dispatcher.dispatches()) == 2 }, time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"message", "compact"}, dispatcher.dispatches())
}

func TestManagerEditBarrierAndSteer(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	dispatcher := &recordingDispatcher{steering: true}
	manager := NewManager(store, dispatcher, &recordingObserver{})
	ctx := context.Background()
	_, err := manager.SetPaused(ctx, "agent-1", true)
	require.NoError(t, err)
	_, err = manager.Enqueue(ctx, NewItem{ID: "one", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "one"})
	require.NoError(t, err)
	_, _, _, err = manager.BeginEdit(ctx, "agent-1", "one", "client", false)
	require.NoError(t, err)
	_, err = manager.SetPaused(ctx, "agent-1", false)
	require.NoError(t, err)
	assert.Never(t, func() bool { return len(dispatcher.dispatches()) > 0 }, 50*time.Millisecond, 5*time.Millisecond)
	snapshot, err := manager.CancelEdit(ctx, "agent-1", "one", "client")
	require.NoError(t, err)
	assert.Empty(t, snapshot.Items[0].EditOwner)
	require.Eventually(t, func() bool { return len(dispatcher.dispatches()) == 1 }, time.Second, 10*time.Millisecond)

	_, err = manager.Enqueue(ctx, NewItem{ID: "two", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "two"})
	require.NoError(t, err)
	_, err = manager.Steer(ctx, "agent-1", "two")
	require.NoError(t, err)
	dispatcher.mu.Lock()
	assert.Equal(t, []string{"two"}, dispatcher.steered)
	dispatcher.mu.Unlock()
}

func TestManagerFailurePausesAndRetryDispatchesOnlyHead(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	dispatcher := &recordingDispatcher{fail: &DeliveryError{Err: assert.AnError}}
	manager := NewManager(store, dispatcher, &recordingObserver{})
	ctx := context.Background()
	_, err := manager.Enqueue(ctx, NewItem{ID: "one", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "one"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		snapshot, snapshotErr := manager.Snapshot(ctx, "agent-1")
		return snapshotErr == nil && snapshot.Paused && len(snapshot.Items) == 1 && snapshot.Items[0].State == leapmuxv1.AgentInputState_AGENT_INPUT_STATE_FAILED
	}, time.Second, 10*time.Millisecond)
	dispatcher.mu.Lock()
	dispatcher.fail = nil
	dispatcher.mu.Unlock()
	snapshot, err := manager.Retry(ctx, "agent-1", "one", false)
	require.NoError(t, err)
	assert.Empty(t, snapshot.Items)
}

func TestManagerSteerRequeuesWhenTurnEndsDuringRequest(t *testing.T) {
	t.Parallel()
	testManagerSteerTurnEndRace(t, ErrTurnEnded)
}

func TestManagerSteerRequeuesTurnEndWhenProviderReturnsSuccess(t *testing.T) {
	t.Parallel()
	testManagerSteerTurnEndRace(t, nil)
}

func TestManagerSteerRequeuesWhenCapabilityDisappears(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	dispatcher := &recordingDispatcher{steering: true, steerFail: ErrSteeringUnsupported}
	manager := NewManager(store, dispatcher, &recordingObserver{})
	ctx := context.Background()
	_, err := manager.Enqueue(ctx, NewItem{
		ID: "active", AgentID: "agent-1", Text: "active",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(dispatcher.dispatches()) == 1 }, time.Second, 10*time.Millisecond)
	_, err = manager.Enqueue(ctx, NewItem{
		ID: "steer", AgentID: "agent-1", Text: "guide",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	})
	require.NoError(t, err)

	snapshot, err := manager.Steer(ctx, "agent-1", "steer")
	assert.ErrorIs(t, err, ErrSteeringUnsupported)
	require.Len(t, snapshot.Items, 1)
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, snapshot.Items[0].State)
	assert.False(t, snapshot.Paused)
}

func TestManagerSteerMarksUncertainDelivery(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	dispatcher := &recordingDispatcher{
		steering:  true,
		steerFail: &DeliveryError{Err: assert.AnError, Uncertain: true},
	}
	manager := NewManager(store, dispatcher, &recordingObserver{})
	ctx := context.Background()
	_, err := manager.Enqueue(ctx, NewItem{
		ID: "active", AgentID: "agent-1", Text: "active",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(dispatcher.dispatches()) == 1 }, time.Second, 10*time.Millisecond)
	_, err = manager.Enqueue(ctx, NewItem{
		ID: "steer", AgentID: "agent-1", Text: "guide",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	})
	require.NoError(t, err)

	snapshot, err := manager.Steer(ctx, "agent-1", "steer")
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DELIVERY_UNCERTAIN, snapshot.Items[0].State)
	assert.True(t, snapshot.Paused)
	assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_DELIVERY_UNCERTAIN, snapshot.PauseReason)
}

func TestManagerDrainsNormallyAfterSuccessfulSteerTurnEnds(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	dispatcher := &recordingDispatcher{steering: true}
	manager := NewManager(store, dispatcher, &recordingObserver{})
	ctx := context.Background()
	_, err := manager.Enqueue(ctx, NewItem{
		ID: "active", AgentID: "agent-1", Text: "active",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(dispatcher.dispatches()) == 1 }, time.Second, 10*time.Millisecond)
	_, err = manager.Enqueue(ctx, NewItem{
		ID: "steer", AgentID: "agent-1", Text: "guide",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	})
	require.NoError(t, err)
	_, err = manager.Steer(ctx, "agent-1", "steer")
	require.NoError(t, err)
	_, err = manager.Enqueue(ctx, NewItem{
		ID: "later", AgentID: "agent-1", Text: "later",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"active"}, dispatcher.dispatches())

	_, err = manager.TurnEnded(ctx, "agent-1")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(dispatcher.dispatches()) == 2 }, time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"active", "later"}, dispatcher.dispatches())
}

func testManagerSteerTurnEndRace(t *testing.T, providerErr error) {
	t.Helper()

	_, store := newStoreFixture(t)
	dispatcher := &recordingDispatcher{steering: true}
	manager := NewManager(store, dispatcher, &recordingObserver{})
	ctx := context.Background()
	_, err := manager.Enqueue(ctx, NewItem{ID: "active", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "active"})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(dispatcher.dispatches()) == 1 }, time.Second, 10*time.Millisecond)
	_, err = manager.Enqueue(ctx, NewItem{ID: "next", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "next"})
	require.NoError(t, err)
	started := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	dispatcher.mu.Lock()
	dispatcher.steerStarted = started
	dispatcher.steerRelease = release
	dispatcher.steerFail = providerErr
	dispatcher.mu.Unlock()
	steerDone := make(chan struct{})
	var snapshot Snapshot
	var steerErr error
	go func() {
		snapshot, steerErr = manager.Steer(ctx, "agent-1", "next")
		close(steerDone)
	}()
	<-started
	turnEndDone := make(chan error, 1)
	go func() {
		_, turnEndErr := manager.TurnEnded(ctx, "agent-1")
		turnEndDone <- turnEndErr
	}()
	require.Eventually(t, func() bool { return len(turnEndDone) == 1 }, time.Second, 10*time.Millisecond)
	require.NoError(t, <-turnEndDone)
	close(release)
	<-steerDone
	err = steerErr
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, snapshot.Items[0].State)
	assert.False(t, snapshot.ActiveTurn)
}

func TestManagerRecoverDrainsUnpausedQueuedInput(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	ctx := context.Background()
	_, err := store.Enqueue(ctx, NewItem{ID: "queued", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "queued"})
	require.NoError(t, err)
	dispatcher := &recordingDispatcher{}
	manager := NewManager(store, dispatcher, &recordingObserver{})

	require.NoError(t, manager.Recover(ctx))
	require.Eventually(t, func() bool { return len(dispatcher.dispatches()) == 1 }, time.Second, 10*time.Millisecond)
}

func TestManagerRecoverStateWaitsForRecoveredDrain(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	ctx := context.Background()
	_, err := store.Enqueue(ctx, NewItem{ID: "queued", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "queued"})
	require.NoError(t, err)
	dispatcher := &recordingDispatcher{}
	manager := NewManager(store, dispatcher, &recordingObserver{})

	require.NoError(t, manager.RecoverState(ctx))
	assert.Never(t, func() bool { return len(dispatcher.dispatches()) > 0 }, 50*time.Millisecond, 5*time.Millisecond)
	require.NoError(t, manager.DrainRecovered(ctx))
	require.Eventually(t, func() bool { return len(dispatcher.dispatches()) == 1 }, time.Second, 10*time.Millisecond)
}

func TestManagerRecoveredDrainSkipsAnAgentRemovedDuringReconciliation(t *testing.T) {
	t.Parallel()

	database, store := newStoreFixture(t)
	ctx := context.Background()
	_, err := store.Enqueue(ctx, NewItem{ID: "queued", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "queued"})
	require.NoError(t, err)
	dispatcher := &recordingDispatcher{}
	manager := NewManager(store, dispatcher, &recordingObserver{})
	require.NoError(t, manager.RecoverState(ctx))
	_, err = database.ExecContext(ctx, `DELETE FROM agents WHERE id = 'agent-1'`)
	require.NoError(t, err)

	require.NoError(t, manager.DrainRecovered(ctx))
	assert.Never(t, func() bool { return len(dispatcher.dispatches()) > 0 }, 50*time.Millisecond, 5*time.Millisecond)
}

func TestManagerPausesAsUncertainWhenAcceptedTranscriptCannotPersist(t *testing.T) {
	t.Parallel()

	database, store := newStoreFixture(t)
	_, err := database.Exec(`CREATE TRIGGER reject_queue_transcript BEFORE INSERT ON messages BEGIN SELECT RAISE(ABORT, 'transcript unavailable'); END`)
	require.NoError(t, err)
	manager := NewManager(store, &recordingDispatcher{}, &recordingObserver{})
	ctx := context.Background()
	_, err = manager.Enqueue(ctx, NewItem{ID: "one", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "one"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		snapshot, snapshotErr := manager.Snapshot(ctx, "agent-1")
		return snapshotErr == nil && snapshot.Paused && len(snapshot.Items) == 1 &&
			snapshot.Items[0].State == leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DELIVERY_UNCERTAIN
	}, time.Second, 10*time.Millisecond)
}

func TestManagerSerializesConcurrentClientMutations(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	manager := NewManager(store, &recordingDispatcher{}, &recordingObserver{})
	ctx := context.Background()
	_, err := manager.SetPaused(ctx, "agent-1", true)
	require.NoError(t, err)

	var wait sync.WaitGroup
	for i := 0; i < 20; i++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, enqueueErr := manager.Enqueue(ctx, NewItem{
				ID: fmt.Sprintf("input-%02d", index), AgentID: "agent-1",
				Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "queued",
			})
			assert.NoError(t, enqueueErr)
		}(i)
	}
	wait.Wait()
	snapshot, err := manager.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	assert.Len(t, snapshot.Items, 20)
	assert.Equal(t, uint64(21), snapshot.Revision)
}

func TestManagerStopRefusesNewWorkAndWaitJoinsDispatch(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	dispatchStarted := make(chan struct{})
	dispatchRelease := make(chan struct{})
	dispatcher := &recordingDispatcher{dispatchStarted: dispatchStarted, dispatchRelease: dispatchRelease}
	manager := NewManager(store, dispatcher, &recordingObserver{})
	ctx := context.Background()
	_, err := manager.Enqueue(ctx, NewItem{
		ID: "active", AgentID: "agent-1", Text: "active",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	})
	require.NoError(t, err)
	<-dispatchStarted

	waitForStop := manager.Stop()
	_, err = manager.Enqueue(ctx, NewItem{
		ID: "late", AgentID: "agent-1", Text: "late",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	})
	assert.ErrorIs(t, err, ErrManagerStopped)

	stopped := make(chan struct{})
	go func() {
		waitForStop()
		close(stopped)
	}()
	assert.Never(t, func() bool {
		select {
		case <-stopped:
			return true
		default:
			return false
		}
	}, 50*time.Millisecond, 5*time.Millisecond, "Wait returned while dispatch was active")
	close(dispatchRelease)
	<-stopped
}
