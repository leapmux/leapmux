package inputqueue

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

type recordingDispatcher struct {
	// refusedKind lets a test refuse one kind the way a subagent refuses a
	// clear or a compact.
	refusedKind     leapmuxv1.AgentInputKind
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

// AcceptsKind admits every kind unless a test narrows refusedKind.
func (d *recordingDispatcher) AcceptsKind(_ string, kind leapmuxv1.AgentInputKind) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return kind != d.refusedKind
}

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
		steerFail: &DeliveryError{Err: assert.AnError, Outcome: DispatchUncertain},
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

func TestManagerPlannedRestartPreservesQueueState(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name             string
		manualPause      bool
		processReplaced  bool
		restartSucceeded bool
		wantActive       bool
		wantPaused       bool
		wantPauseReason  leapmuxv1.AgentInputQueuePauseReason
	}{
		{
			name:            "successful replacement",
			processReplaced: true, restartSucceeded: true,
		},
		{
			name:            "failure before replacement",
			processReplaced: false, restartSucceeded: false,
			wantActive: true,
		},
		{
			name:            "failed replacement",
			processReplaced: true, restartSucceeded: false,
			wantPaused:      true,
			wantPauseReason: leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED,
		},
		{
			name:        "existing manual pause",
			manualPause: true, processReplaced: true, restartSucceeded: true,
			wantPaused:      true,
			wantPauseReason: leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, store := newStoreFixture(t)
			manager := NewManager(store, &recordingDispatcher{}, &recordingObserver{})
			ctx := context.Background()
			_, err := manager.TurnStarted(ctx, "agent-1")
			require.NoError(t, err)
			if test.manualPause {
				_, err = manager.SetPaused(ctx, "agent-1", true)
				require.NoError(t, err)
			}

			restart, err := manager.BeginPlannedRestart(ctx, "agent-1")
			require.NoError(t, err)
			during, err := store.Snapshot(ctx, "agent-1")
			require.NoError(t, err)
			assert.True(t, during.Paused)
			assert.True(t, during.ActiveTurn)

			require.NoError(t, restart.Finish(ctx, test.processReplaced, test.restartSucceeded))
			after, err := manager.Snapshot(ctx, "agent-1")
			require.NoError(t, err)
			assert.Equal(t, test.wantActive, after.ActiveTurn)
			assert.Equal(t, test.wantPaused, after.Paused)
			assert.Equal(t, test.wantPauseReason, after.PauseReason)
		})
	}
}

// A planned restart takes NO lock that an explicit resume can wait on. It held
// one before, and that deadlocked the Worker whenever the old process crashed
// during the stop: the exit goroutine calls Pause, Pause took the restart's
// lock, and the stop waits for that same exit goroutine. The durable
// pause_owner column carries what the lock protected, so the restart never
// blocks a caller and never resumes a pause that a different cause created.
func TestManagerPlannedRestartNeverBlocksAnExplicitResume(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	manager := NewManager(store, &recordingDispatcher{}, &recordingObserver{})
	ctx := context.Background()
	restart, err := manager.BeginPlannedRestart(ctx, "agent-1")
	require.NoError(t, err)
	resumeDone := make(chan error, 1)
	go func() {
		_, resumeErr := manager.SetPaused(ctx, "agent-1", false)
		resumeDone <- resumeErr
	}()
	select {
	case resumeErr := <-resumeDone:
		require.NoError(t, resumeErr)
	case <-time.After(2 * time.Second):
		t.Fatal("an explicit resume waited on the planned restart")
	}

	// The user owns the queue state now, so finishing the restart must not
	// pause it again or resume something it no longer owns.
	require.NoError(t, restart.Finish(ctx, true, true))
	after, err := manager.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	assert.False(t, after.Paused)
}

// A pause that a crash records during a planned restart must survive the
// restart. The crash takes ownership, so Finish leaves it in place.
func TestManagerPlannedRestartKeepsACrashPause(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	manager := NewManager(store, &recordingDispatcher{}, &recordingObserver{})
	ctx := context.Background()
	restart, err := manager.BeginPlannedRestart(ctx, "agent-1")
	require.NoError(t, err)
	_, err = manager.Pause(ctx, "agent-1", leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED)
	require.NoError(t, err)

	require.NoError(t, restart.Finish(ctx, true, true))
	after, err := manager.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	assert.True(t, after.Paused, "the crash pause must outlive the restart that did not create it")
}

// Retry and Steer write into the provider, so they must refuse while the
// Worker replaces the process. The old code relied on a mutex for this; the
// durable owner keeps the refusal after a Worker restart too.
func TestManagerRetryRefusesDuringAPlannedRestart(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	manager := NewManager(store, &recordingDispatcher{}, &recordingObserver{})
	ctx := context.Background()
	// THROUGH THE STORE, like the two calls below it. `Manager.Enqueue` starts a
	// drain goroutine, which claims the item with its own `PrepareDispatch` --
	// so the one here answers nil whenever that goroutine reaches the store
	// first, which a loaded CI runner does. The subject is what `Retry` refuses,
	// and the item it refuses for is store state, so the setup belongs there.
	_, err := store.Enqueue(ctx, NewItem{
		ID: "one", AgentID: "agent-1", Text: "hello",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	})
	require.NoError(t, err)
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1")
	require.NoError(t, err)
	require.NotNil(t, prepared)
	_, err = store.FailDispatch(ctx, "agent-1", "one", assert.AnError, false)
	require.NoError(t, err)

	restart, err := manager.BeginPlannedRestart(ctx, "agent-1")
	require.NoError(t, err)
	_, err = manager.Retry(ctx, "agent-1", "one", false)
	assert.ErrorIs(t, err, ErrPlannedRestart)
	require.NoError(t, restart.Finish(ctx, true, true))
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

// onceDispatcher acts on the FIRST dispatch alone and takes every later one
// normally, so a test can watch the queue continue past the one event it
// stages. firstEffect runs from inside the provider call, which is where a
// provider's reader goroutine reports a turn that ends while the dispatch it
// belongs to is still in flight. firstErr, when set, is what that first
// dispatch returns instead of a result.
type onceDispatcher struct {
	recordingDispatcher
	firstEffect func()
	firstErr    error
	once        sync.Once
}

func (d *onceDispatcher) Dispatch(item Item) (DispatchResult, error) {
	result, err := d.recordingDispatcher.Dispatch(item)
	first := false
	d.once.Do(func() {
		first = true
		if d.firstEffect != nil {
			d.firstEffect()
		}
	})
	if first && d.firstErr != nil {
		return DispatchResult{}, d.firstErr
	}
	return result, err
}

func TestManagerKeepsDrainingWhenTheTurnEndsDuringItsOwnDispatch(t *testing.T) {
	t.Parallel()

	// The manager releases the coordinator lock across the provider call, so a
	// turn that ends inside it commits its clear first, and the drain it
	// scheduled finds this loop still marked draining and does nothing. The
	// acceptance must neither write that turn back nor stop the loop, or every
	// item behind the first waits for a turn that already ended.
	_, store := newStoreFixture(t)
	dispatcher := &onceDispatcher{}
	manager := NewManager(store, dispatcher, &recordingObserver{})
	dispatcher.firstEffect = func() { _, _ = manager.TurnEnded(context.Background(), "agent-1") }
	ctx := context.Background()
	for _, inputID := range []string{"one", "two"} {
		_, err := manager.Enqueue(ctx, NewItem{ID: inputID, AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: inputID})
		require.NoError(t, err)
	}

	// recordingDispatcher records the call before it returns, and the drain
	// commits the acceptance after it. Waiting on the count alone would let the
	// assertions below read the queue while the second item is still
	// DISPATCHING, which snapshotTx still lists.
	require.Eventually(t, func() bool {
		s, snapshotErr := manager.Snapshot(ctx, "agent-1")
		return snapshotErr == nil && len(dispatcher.dispatches()) == 2 && len(s.Items) == 0
	}, time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"one", "two"}, dispatcher.dispatches())
	snapshot, err := manager.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	assert.Empty(t, snapshot.Items)
	assert.True(t, snapshot.ActiveTurn, "the second dispatch's own turn is the one that stands")
}

func TestManagerTurnSignalIsIdempotentInBothDirections(t *testing.T) {
	t.Parallel()

	// Every publish of a provider's turn flag reaches the manager, and a
	// provider republishes the unchanged value freely. A repeat must move no
	// revision, or every watcher takes a snapshot that says what it already
	// holds.
	_, store := newStoreFixture(t)
	observer := &recordingObserver{}
	manager := NewManager(store, &recordingDispatcher{}, observer)
	ctx := context.Background()

	started, err := manager.TurnStarted(ctx, "agent-1")
	require.NoError(t, err)
	require.True(t, started.ActiveTurn)

	repeated, err := manager.TurnStarted(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, started.Revision, repeated.Revision)

	ended, err := manager.TurnEnded(ctx, "agent-1")
	require.NoError(t, err)
	require.False(t, ended.ActiveTurn)
	assert.Greater(t, ended.Revision, started.Revision)

	endedAgain, err := manager.TurnEnded(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, ended.Revision, endedAgain.Revision)

	observer.mu.Lock()
	broadcasts := len(observer.snapshots)
	observer.mu.Unlock()
	assert.Equal(t, 2, broadcasts, "only the two real transitions broadcast")
}

func TestManagerTurnStartedKeepsTheTurnADispatchAlreadyOwns(t *testing.T) {
	t.Parallel()

	// A provider publishes "a turn is in flight" for the very turn the queue
	// just dispatched. That report carries no input id, so adopting it would
	// drop the identity the dispatch recorded -- and the acceptance, which
	// writes the turn back only while the state still holds its own dispatch,
	// would then find no match and treat the live turn as finished.
	_, store := newStoreFixture(t)
	release := make(chan struct{})
	dispatcher := &recordingDispatcher{dispatchStarted: make(chan struct{}), dispatchRelease: release}
	manager := NewManager(store, dispatcher, &recordingObserver{})
	ctx := context.Background()
	_, err := manager.Enqueue(ctx, NewItem{ID: "one", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "one"})
	require.NoError(t, err)
	<-dispatcher.dispatchStarted

	_, err = manager.TurnStarted(ctx, "agent-1")
	require.NoError(t, err)
	close(release)

	require.Eventually(t, func() bool {
		snapshot, snapshotErr := manager.Snapshot(ctx, "agent-1")
		return snapshotErr == nil && len(snapshot.Items) == 0
	}, time.Second, 10*time.Millisecond)
	snapshot, err := manager.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	assert.True(t, snapshot.ActiveTurn, "the dispatched turn survives the provider's own report of it")
}

// busyRefusal is what a provider returns when a dispatch collided with a turn
// of its own.
func busyRefusal() error {
	return &DeliveryError{Err: errors.New("turn-1"), Outcome: DispatchBusy}
}

func TestManagerBusyRefusalHoldsTheItemWithoutPausingTheQueue(t *testing.T) {
	t.Parallel()

	// The provider refused because a turn of its own is in flight. Nothing
	// failed, so the item waits with no error and the queue stays open: the
	// turn that refused it ends and delivers it. A pause here stopped a healthy
	// queue until the user resumed it by hand.
	_, store := newStoreFixture(t)
	dispatcher := &onceDispatcher{firstErr: busyRefusal()}
	manager := NewManager(store, dispatcher, &recordingObserver{})
	ctx := context.Background()
	_, err := manager.Enqueue(ctx, NewItem{ID: "one", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "one"})
	require.NoError(t, err)

	require.Eventually(t, func() bool { return len(dispatcher.dispatches()) == 1 }, time.Second, 10*time.Millisecond)
	var snapshot Snapshot
	require.Eventually(t, func() bool {
		snapshot, err = manager.Snapshot(ctx, "agent-1")
		return err == nil && len(snapshot.Items) == 1 &&
			snapshot.Items[0].State == leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED
	}, time.Second, 10*time.Millisecond)
	assert.False(t, snapshot.Paused, "a busy agent is not a stopped one")
	assert.Empty(t, snapshot.Items[0].Error, "nothing failed, so the item carries no error")
	assert.True(t, snapshot.ActiveTurn, "the turn the item collided with is what holds it")

	_, err = manager.TurnEnded(ctx, "agent-1")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		snapshot, err = manager.Snapshot(ctx, "agent-1")
		return err == nil && len(snapshot.Items) == 0
	}, time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"one", "one"}, dispatcher.dispatches(),
		"the same item goes out again once the turn that refused it ends")
}

func TestManagerBusyRefusalRepeatsWhenTheTurnEndsInsideIt(t *testing.T) {
	t.Parallel()

	// The turn ends while the provider call that it refused is still running.
	// Its drain request finds this loop still marked draining, so nothing else
	// can act on it -- and the item would then wait for a turn that already
	// ended, with no later event to release it.
	_, store := newStoreFixture(t)
	dispatcher := &onceDispatcher{firstErr: busyRefusal()}
	manager := NewManager(store, dispatcher, &recordingObserver{})
	dispatcher.firstEffect = func() { _, _ = manager.TurnEnded(context.Background(), "agent-1") }
	ctx := context.Background()
	_, err := manager.Enqueue(ctx, NewItem{ID: "one", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "one"})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		snapshot, snapshotErr := manager.Snapshot(ctx, "agent-1")
		return snapshotErr == nil && len(snapshot.Items) == 0
	}, time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"one", "one"}, dispatcher.dispatches(),
		"the loop repeats for the turn end it could not see, with no outside trigger")
}

func TestManagerBusyRefusalReleasesTheTurnIdentityItClaimed(t *testing.T) {
	t.Parallel()

	// PrepareDispatch records the item's kind and id as the running turn's
	// BEFORE the provider call. A busy refusal means that dispatch never
	// happened, so the turn that stands is the provider's own -- and it carries
	// neither. A claim left behind makes CanSteer and PrepareSteer judge the
	// running turn by the refused item's kind: a bounced /compact hides Steer
	// for a plain reply, and a bounced plain message offers it for a compaction.
	_, store := newStoreFixture(t)
	dispatcher := &onceDispatcher{firstErr: busyRefusal()}
	manager := NewManager(store, dispatcher, &recordingObserver{})
	ctx := context.Background()
	_, err := manager.Enqueue(ctx, NewItem{
		ID: "compact", AgentID: "agent-1", Text: "/compact",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_COMPACT_CONTEXT,
	})
	require.NoError(t, err)

	var snapshot Snapshot
	require.Eventually(t, func() bool {
		snapshot, err = manager.Snapshot(ctx, "agent-1")
		return err == nil && len(dispatcher.dispatches()) == 1 && len(snapshot.Items) == 1 &&
			snapshot.Items[0].State == leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED
	}, time.Second, 10*time.Millisecond)
	assert.True(t, snapshot.ActiveTurn, "the refusal proves a turn is in flight")
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_UNSPECIFIED, snapshot.ActiveTurnKind,
		"the refused item's kind does not describe the provider's own turn")

	// The identity is gone, so the head item is judged against a turn the queue
	// admits it knows nothing about, and the steer is refused rather than
	// misrouted into whatever the provider actually runs.
	_, err = manager.Enqueue(ctx, NewItem{
		ID: "reply", AgentID: "agent-1", Text: "and also",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	})
	require.NoError(t, err)
	snapshot, err = manager.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	assert.False(t, snapshot.Items[0].CanSteer,
		"a turn the queue did not dispatch states no kind, so no steer is offered")
}

func TestManagerRetryBusyRefusalDrainsWhenTheTurnEndsInsideIt(t *testing.T) {
	t.Parallel()

	// Retry runs its own dispatch outside the drain loop, so the drainAgain
	// latch cannot cover it: the TurnEnded that lands during the provider call
	// schedules a drain that finds the item still DISPATCHING and gives up.
	// Retry must re-read the committed state and drain for itself, or the item
	// waits for a turn that already ended.
	_, store := newStoreFixture(t)
	dispatcher := &recordingDispatcher{fail: errors.New("provider exploded")}
	manager := NewManager(store, dispatcher, &recordingObserver{})
	ctx := context.Background()
	_, err := manager.Enqueue(ctx, NewItem{
		ID: "one", AgentID: "agent-1", Text: "one",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		snapshot, snapshotErr := manager.Snapshot(ctx, "agent-1")
		return snapshotErr == nil && len(snapshot.Items) == 1 &&
			snapshot.Items[0].State == leapmuxv1.AgentInputState_AGENT_INPUT_STATE_FAILED
	}, time.Second, 10*time.Millisecond)

	// The retry collides with a turn, and that turn ends inside the refusal.
	busy := &onceDispatcher{firstErr: busyRefusal()}
	busy.firstEffect = func() { _, _ = manager.TurnEnded(context.Background(), "agent-1") }
	manager.dispatcher = busy
	_, err = manager.Retry(ctx, "agent-1", "one", false)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		snapshot, snapshotErr := manager.Snapshot(ctx, "agent-1")
		return snapshotErr == nil && len(snapshot.Items) == 0
	}, time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"one", "one"}, busy.dispatches(),
		"the retried item goes out again with no further user action")
}

func TestManagerRepeatedTurnEndRestartsADrainAStoreErrorStopped(t *testing.T) {
	t.Parallel()

	// A store error stops the drain loop with the items still queued, the queue
	// unpaused and no turn in flight. Nothing schedules another drain, so the
	// only event left is the provider's turn flag -- which repeats a state the
	// queue already holds, and therefore moves no revision.
	_, store := newStoreFixture(t)
	dispatcher := &recordingDispatcher{}
	manager := NewManager(store, dispatcher, &recordingObserver{})
	ctx := context.Background()

	// Enqueue through the STORE, which writes the item and schedules nothing.
	// That is exactly what the drain loop leaves behind when PrepareDispatch
	// fails: an item queued, the queue open, and no turn in flight.
	_, err := store.Enqueue(ctx, NewItem{
		ID: "one", AgentID: "agent-1", Text: "one",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	})
	require.NoError(t, err)
	before, err := manager.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	require.Len(t, before.Items, 1)
	require.False(t, before.ActiveTurn)
	require.False(t, before.Paused)
	require.Empty(t, dispatcher.dispatches(), "nothing scheduled a drain")

	// The clear repeats a state the queue already holds, so it moves no
	// revision -- and a drain that fires only on a real change never runs.
	_, err = manager.TurnEnded(ctx, "agent-1")
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		snapshot, snapshotErr := manager.Snapshot(ctx, "agent-1")
		return snapshotErr == nil && len(snapshot.Items) == 0
	}, time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"one"}, dispatcher.dispatches(),
		"the repeated clear restarted the loop the store error stopped")
}
