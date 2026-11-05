package crdt_test

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// TestRegistry_SweepIdle_StopsAndReboots verifies the eviction path:
// after Bootstrap stamps activity, SweepIdle leaves the manager in
// place while it's "fresh"; once the idleTTL window passes (and there
// are no subscribers), SweepIdle evicts the manager and the next Get
// re-bootstraps via the factory.
func TestRegistry_SweepIdle_StopsAndReboots(t *testing.T) {
	var factoryCalls atomic.Int32

	journal := newFakeJournal()

	factory := func(_ context.Context, userID userid.UserID) (*crdt.Manager, error) {
		factoryCalls.Add(1)
		mgr := crdt.NewManager(userID, journal, allowAll{}, nil, time.Now)
		require.NoError(t, mgr.Bootstrap(context.Background()))
		return mgr, nil
	}

	registry := crdt.NewRegistry(factory, nil, crdt.WithManagerIdleTTL(10*time.Millisecond))
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

	mgr1, err := registry.Get(context.Background(), "user-1")
	require.NoError(t, err)
	require.NotNil(t, mgr1)
	require.Equal(t, int32(1), factoryCalls.Load())

	// While the manager is "fresh" (activity just stamped during
	// Bootstrap), SweepIdle is a no-op.
	registry.SweepIdle()
	mgr1Again, err := registry.Get(context.Background(), "user-1")
	require.NoError(t, err)
	assert.Same(t, mgr1, mgr1Again, "fresh manager survives a sweep")
	assert.Equal(t, int32(1), factoryCalls.Load())

	// Past the idleTTL window with no subscribers and no submits, the
	// sweep evicts the manager. The next Get re-invokes the factory
	// and returns a fresh instance.
	time.Sleep(15 * time.Millisecond)
	registry.SweepIdle()

	mgr2, err := registry.Get(context.Background(), "user-1")
	require.NoError(t, err)
	assert.NotSame(t, mgr1, mgr2, "idle manager was evicted; Get re-bootstrapped")
	assert.Equal(t, int32(2), factoryCalls.Load())
}

// TestRegistry_Get_RefreshesActivityAgainstSweep closes the Get-then-evict
// race: a Get that hands out an existing manager stamps activity, so a manager
// that was otherwise idle-evictable is kept alive by the recent reference and a
// caller can never be handed a manager a concurrent sweep is about to Stop.
//
// The setup makes the manager stale (idle past the TTL) and then Gets it: with
// the activity stamp on Get, the immediately-following sweep must treat it as
// fresh and leave it in place, so the next Get returns the SAME instance. Without
// the stamp, the stale manager would be swept between the two Gets and the second
// Get would re-bootstrap a different one.
func TestRegistry_Get_RefreshesActivityAgainstSweep(t *testing.T) {
	var factoryCalls atomic.Int32
	journal := newFakeJournal()

	factory := func(_ context.Context, userID userid.UserID) (*crdt.Manager, error) {
		factoryCalls.Add(1)
		mgr := crdt.NewManager(userID, journal, allowAll{}, nil, time.Now)
		require.NoError(t, mgr.Bootstrap(context.Background()))
		return mgr, nil
	}

	registry := crdt.NewRegistry(factory, nil, crdt.WithManagerIdleTTL(10*time.Millisecond))
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

	mgr1, err := registry.Get(context.Background(), "user-1")
	require.NoError(t, err)
	require.Equal(t, int32(1), factoryCalls.Load())

	// Let the manager go stale (its only activity was the Bootstrap stamp).
	time.Sleep(15 * time.Millisecond)

	// A Get of the stale manager must refresh its activity...
	mgr1Again, err := registry.Get(context.Background(), "user-1")
	require.NoError(t, err)
	require.Same(t, mgr1, mgr1Again, "the stale manager is still the one returned")
	require.Equal(t, int32(1), factoryCalls.Load(), "Get must not re-bootstrap a still-present manager")

	// ...so an immediately-following sweep leaves it in place rather than
	// evicting the manager the caller just received.
	registry.SweepIdle()

	mgr1Third, err := registry.Get(context.Background(), "user-1")
	require.NoError(t, err)
	assert.Same(t, mgr1, mgr1Third, "a manager a recent Get returned must survive the next sweep")
	assert.Equal(t, int32(1), factoryCalls.Load(), "no eviction, so no re-bootstrap")
}

// TestRegistry_SweepIdle_KeepsManagersWithSubscribers documents the
// safety net: a manager with a live subscriber is never evicted, even
// if its last submit predates the idleTTL window.
func TestRegistry_SweepIdle_KeepsManagersWithSubscribers(t *testing.T) {
	journal := newFakeJournal()

	factory := func(_ context.Context, userID userid.UserID) (*crdt.Manager, error) {
		mgr := crdt.NewManager(userID, journal, allowAll{}, nil, time.Now)
		require.NoError(t, mgr.Bootstrap(context.Background()))
		return mgr, nil
	}

	registry := crdt.NewRegistry(factory, nil, crdt.WithManagerIdleTTL(10*time.Millisecond))
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

	mgr, err := registry.Get(context.Background(), "user-attached")
	require.NoError(t, err)

	// Attach a subscriber so hasLiveAttachments returns true regardless
	// of when the last submit landed.
	listener := &captureSubscriber{}
	_, unsub := mgr.Subscribe(&crdt.Subscriber{
		UserID: "user-1",
		Filter: crdt.SubscriberFilter{},
		Send:   listener.send,
	})
	defer unsub()

	time.Sleep(15 * time.Millisecond)
	registry.SweepIdle()

	mgrAgain, err := registry.Get(context.Background(), "user-attached")
	require.NoError(t, err)
	assert.Same(t, mgr, mgrAgain, "attached manager is not evicted")
}

// TestRegistry_DisabledJanitor_KeepsManagersForever pins the opt-out:
// passing WithManagerIdleTTL(0) keeps the legacy "managers live
// forever" behavior and never spawns a janitor goroutine.
func TestRegistry_DisabledJanitor_KeepsManagersForever(t *testing.T) {
	journal := newFakeJournal()

	factory := func(_ context.Context, userID userid.UserID) (*crdt.Manager, error) {
		mgr := crdt.NewManager(userID, journal, allowAll{}, nil, time.Now)
		require.NoError(t, mgr.Bootstrap(context.Background()))
		return mgr, nil
	}

	registry := crdt.NewRegistry(factory, nil, crdt.WithManagerIdleTTL(0))
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

	mgr1, err := registry.Get(context.Background(), "user-x")
	require.NoError(t, err)
	mgr2, err := registry.Get(context.Background(), "user-x")
	require.NoError(t, err)
	assert.Same(t, mgr1, mgr2, "managers are stable across Get when janitor is disabled")
}

// workersChangedCount counts the workers_changed events among events.
func workersChangedCount(events []*leapmuxv1.WatchUserEvent) int {
	n := 0
	for _, evt := range events {
		if evt.GetWorkersChanged() != nil {
			n++
		}
	}
	return n
}

// TestRegistry_BroadcastWorkersChanged_NeverBootstrapsAManager pins the
// loaded-only lookup: a user with no loaded manager has no live subscriber,
// so the broadcast must not run the factory, which reads the journal.
func TestRegistry_BroadcastWorkersChanged_NeverBootstrapsAManager(t *testing.T) {
	var factoryCalls atomic.Int32
	journal := newFakeJournal()
	factory := func(_ context.Context, userID userid.UserID) (*crdt.Manager, error) {
		factoryCalls.Add(1)
		mgr := crdt.NewManager(userID, journal, allowAll{}, nil, time.Now)
		require.NoError(t, mgr.Bootstrap(context.Background()))
		return mgr, nil
	}
	registry := crdt.NewRegistry(factory, nil, crdt.WithManagerIdleTTL(0))
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

	registry.BroadcastWorkersChanged("user-unloaded")
	assert.Zero(t, factoryCalls.Load(), "a broadcast must not bootstrap a manager for a user nobody watches")
	registry.BroadcastWorkersChanged("")
	assert.Zero(t, factoryCalls.Load(), "a blank user has no manager to tell")

	var absent *crdt.Registry
	assert.NotPanics(t, func() { absent.BroadcastWorkersChanged("user-unloaded") },
		"a nil registry, as a service built without one holds, broadcasts nothing")
}

// TestRegistry_BroadcastWorkersChanged_StampsNoActivity pins that a broadcast
// is not the user's activity: a manager that only received broadcasts is still
// idle, and the sweep evicts it. A real Get, by contrast, keeps its manager.
//
// Each manager reads a clock that reports a time two hours old until the test
// switches it to the present. So the bootstrap stamps a stale time, and only a
// call that stamps activity after the switch can make a manager look fresh.
func TestRegistry_BroadcastWorkersChanged_StampsNoActivity(t *testing.T) {
	var factoryCalls atomic.Int32
	var present atomic.Bool
	stale := time.Now().Add(-2 * time.Hour)
	clock := func() time.Time {
		if present.Load() {
			return time.Now()
		}
		return stale
	}
	journal := newFakeJournal()
	factory := func(_ context.Context, userID userid.UserID) (*crdt.Manager, error) {
		factoryCalls.Add(1)
		mgr := crdt.NewManager(userID, journal, allowAll{}, nil, clock)
		require.NoError(t, mgr.Bootstrap(context.Background()))
		return mgr, nil
	}
	// The janitor ticks every 20 minutes at this TTL, so only the explicit
	// SweepIdle below evicts.
	registry := crdt.NewRegistry(factory, nil, crdt.WithManagerIdleTTL(time.Hour))
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

	broadcastOnly, err := registry.Get(context.Background(), "user-broadcast")
	require.NoError(t, err)
	used, err := registry.Get(context.Background(), "user-used")
	require.NoError(t, err)
	require.EqualValues(t, 2, factoryCalls.Load())

	present.Store(true)
	registry.BroadcastWorkersChanged("user-broadcast")
	_, err = registry.Get(context.Background(), "user-used")
	require.NoError(t, err)

	registry.SweepIdle()

	usedAgain, err := registry.Get(context.Background(), "user-used")
	require.NoError(t, err)
	assert.Same(t, used, usedAgain, "control: a Get stamps activity, so its manager stays")
	broadcastAgain, err := registry.Get(context.Background(), "user-broadcast")
	require.NoError(t, err)
	assert.NotSame(t, broadcastOnly, broadcastAgain, "a manager that only received a broadcast is evicted as idle")
	assert.EqualValues(t, 3, factoryCalls.Load(), "only the evicted manager is built again")
}

// TestRegistry_BroadcastWorkersChanged_ReachesEveryWorkerReaderOfTheUser pins
// the unfiltered fan-out: the worker list belongs to the user, so a worker
// reader whose filter admits no workspace hears the change as one that admits a
// workspace does. A subscriber that cannot read workers and another user's
// subscriber hear nothing.
func TestRegistry_BroadcastWorkersChanged_ReachesEveryWorkerReaderOfTheUser(t *testing.T) {
	journal := newFakeJournal()
	factory := func(_ context.Context, userID userid.UserID) (*crdt.Manager, error) {
		mgr := crdt.NewManager(userID, journal, allowAll{}, nil, time.Now)
		require.NoError(t, mgr.Bootstrap(context.Background()))
		return mgr, nil
	}
	registry := crdt.NewRegistry(factory, nil, crdt.WithManagerIdleTTL(0))
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

	subscribe := func(userID string, filter crdt.SubscriberFilter, mayReadWorkers bool) *captureSubscriber {
		mgr, err := registry.Get(context.Background(), userID)
		require.NoError(t, err)
		listener := &captureSubscriber{}
		_, unsub := mgr.Subscribe(&crdt.Subscriber{
			UserID:         userID,
			Filter:         filter,
			MayReadWorkers: mayReadWorkers,
			Send:           listener.send,
		})
		t.Cleanup(unsub)
		return listener
	}
	noWorkspace := subscribe("user-1", crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{}}, true)
	oneWorkspace := subscribe("user-1", crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"ws-1": true}}, true)
	nonReader := subscribe("user-1", crdt.SubscriberFilter{}, false)
	otherUser := subscribe("user-2", crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"ws-2": true}}, true)

	// The fan-out calls each subscriber's Send before it returns.
	registry.BroadcastWorkersChanged("user-1")

	assert.Equal(t, 1, workersChangedCount(noWorkspace.snapshot()), "a worker reader that admits no workspace still hears it")
	assert.Equal(t, 1, workersChangedCount(oneWorkspace.snapshot()))
	assert.Zero(t, workersChangedCount(nonReader.snapshot()), "a subscriber that cannot read workers hears nothing")
	assert.Zero(t, workersChangedCount(otherUser.snapshot()), "another user's subscriber hears nothing")
}

// --- Shutdown: concurrent, limited by a deadline, and refused after the latch ---

// registryUserID gives the fixture's user IDs.
func registryUserID(n int) userid.UserID { return userid.MustNew(fmt.Sprintf("user-%d", n)) }

// stopped reports whether a manager's goroutine has ended -- the answer of a
// manager whose Stop ran, which is what "stopped before the deadline" means
// here. A live manager answers a batch before its context expires; a stopped
// one leaves the context to run out.
func stopped(t *testing.T, m *crdt.Manager) bool {
	t.Helper()
	submitCtx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := m.Submit(submitCtx, crdt.SubmitInput{})
	return err == context.DeadlineExceeded
}

// One stalled manager must not leave the others running past the deadline:
// the stops run one goroutine per manager, so every healthy manager is
// stopped when Shutdown returns even though the stalled one held the budget.
//
// "Stopped" is read through Submit: a live manager answers a batch before its
// context expires; a stopped one leaves the context to run out.
// stallManager parks m's goroutine inside a test seed until the test ends, so
// Stop on m waits no matter when the sweep of Shutdown reaches it. It returns
// only once the goroutine is parked: a Shutdown that overtook the seed would
// stop a healthy manager, and the test would prove nothing.
func stallManager(t *testing.T, m *crdt.Manager) {
	t.Helper()
	entered := make(chan struct{})
	gate := make(chan struct{})
	go m.SeedStateForTest(func(*leapmuxv1.UserCrdtState) {
		close(entered)
		<-gate
	})
	t.Cleanup(func() { close(gate) })
	select {
	case <-entered:
	case <-time.After(30 * time.Second):
		t.Fatal("the manager goroutine never ran the seed")
	}
}

func TestRegistryShutdownStopsManagersConcurrently(t *testing.T) {
	journal := newFakeJournal()
	var managers sync.Map
	registry := crdt.NewRegistry(func(_ context.Context, userID userid.UserID) (*crdt.Manager, error) {
		mgr := crdt.NewManager(userID, journal, allowAll{}, nil, time.Now)
		managers.Store(userID.String(), mgr)
		return mgr, nil
	}, nil)
	t.Cleanup(func() { registry.Shutdown(5 * time.Second) })

	const healthy = 16
	handles := make([]*crdt.Manager, 0, healthy+1)
	for i := range healthy {
		m, err := registry.Get(context.Background(), registryUserID(i).String())
		require.NoError(t, err)
		handles = append(handles, m)
	}

	stalled, err := registry.Get(context.Background(), registryUserID(healthy).String())
	require.NoError(t, err)
	handles = append(handles, stalled)
	stallManager(t, stalled)

	registry.Shutdown(2 * time.Second)

	for i, m := range handles[:healthy] {
		require.True(t, stopped(t, m),
			"healthy manager %d must be stopped when Shutdown returned", i)
	}
}

// A manager the janitor is stopping -- parked inside its own goroutine, and
// evicted by a sweep for being idle -- must not spend Shutdown's whole budget
// in the janitor wait AND the stops: the janitor wait has a limit, and
// Shutdown returns at the deadline.
func TestRegistryShutdownHonoursItsDeadlineForTheJanitor(t *testing.T) {
	registry := crdt.NewRegistryWithWedgedJanitorForTest(func(_ context.Context, userID userid.UserID) (*crdt.Manager, error) {
		return crdt.NewManager(userID, newFakeJournal(), allowAll{}, nil, time.Now), nil
	}, nil)

	done := make(chan struct{})
	go func() {
		registry.Shutdown(200 * time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Shutdown did not return at its deadline")
	}
}

// The janitor's wait shares the budget with the manager stops, so the stops
// must not queue behind it: a wedged janitor would spend the whole budget, and
// every manager would still be running when the caller closes the store.
func TestRegistryShutdownStopsManagersWhileTheJanitorIsWedged(t *testing.T) {
	registry := crdt.NewRegistryWithWedgedJanitorForTest(func(_ context.Context, userID userid.UserID) (*crdt.Manager, error) {
		return crdt.NewManager(userID, newFakeJournal(), allowAll{}, nil, time.Now), nil
	}, nil)
	m, err := registry.Get(context.Background(), registryUserID(1).String())
	require.NoError(t, err)

	registry.Shutdown(500 * time.Millisecond)

	require.True(t, stopped(t, m), "a healthy manager must stop while the janitor is wedged")
}

// A manager that outlives the deadline runs past the caller's store close, so
// the shutdown must report how many did.
func TestRegistryShutdownWarnsOfManagersStillRunning(t *testing.T) {
	logs := &testutil.LogBuffer{}
	registry := crdt.NewRegistry(func(_ context.Context, userID userid.UserID) (*crdt.Manager, error) {
		return crdt.NewManager(userID, newFakeJournal(), allowAll{}, nil, time.Now), nil
	}, slog.New(slog.NewTextHandler(logs, nil)))

	stalled, err := registry.Get(context.Background(), registryUserID(1).String())
	require.NoError(t, err)
	stallManager(t, stalled)

	registry.Shutdown(100 * time.Millisecond)

	assert.Contains(t, logs.String(), "running=1", "the stalled manager must be counted")
}

func TestRegistryGetRefusedAfterShutdown(t *testing.T) {
	registry := crdt.NewRegistry(func(_ context.Context, userID userid.UserID) (*crdt.Manager, error) {
		return crdt.NewManager(userID, newFakeJournal(), allowAll{}, nil, time.Now), nil
	}, nil)
	registry.Shutdown(time.Second)

	_, err := registry.Get(context.Background(), registryUserID(1).String())

	assert.ErrorIs(t, err, crdt.ErrRegistryClosed,
		"a Get after Shutdown must be refused, not lazily create a manager nothing stops")
}

// A Get whose factory raced Shutdown must discard the manager it built, not
// store it: the sweep already snapshotted, so a stored manager would run past
// the store close with nothing stopping it.
func TestRegistryGetDuringShutdownDiscardsTheManager(t *testing.T) {
	release := make(chan struct{})
	var factories atomic.Int32
	registry := crdt.NewRegistry(func(_ context.Context, userID userid.UserID) (*crdt.Manager, error) {
		if factories.Add(1) == 1 {
			// Park the FIRST factory across Shutdown's latch.
			<-release
		}
		return crdt.NewManager(userID, newFakeJournal(), allowAll{}, nil, time.Now), nil
	}, nil)
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		registry.Shutdown(time.Second)
	})

	var getErr error
	getDone := make(chan struct{})
	go func() {
		defer close(getDone)
		_, getErr = registry.Get(context.Background(), registryUserID(99).String())
	}()
	registry.Shutdown(time.Second)
	releaseOnce.Do(func() { close(release) })
	<-getDone

	assert.ErrorIs(t, getErr, crdt.ErrRegistryClosed,
		"a Get that lost the race to Shutdown's latch must be refused")
}

// Two concurrent Gets for one user race for the map slot. The loser's manager
// is discarded with Discard, which closes the stop channel without waiting on
// m.done: a duplicate whose Start never ran has a done that nothing closes,
// so a Stop there would park the losing goroutine for ever. The goroutine
// count is what observes the park: after the race settles, every racer has
// returned, so only the winner's manager goroutine may remain.
func TestRegistryGetRaceDiscardsTheDuplicate(t *testing.T) {
	registry := crdt.NewRegistry(func(_ context.Context, userID userid.UserID) (*crdt.Manager, error) {
		return crdt.NewManager(userID, newFakeJournal(), allowAll{}, nil, time.Now), nil
	}, nil)
	t.Cleanup(func() { registry.Shutdown(5 * time.Second) })

	// Settle the runloop first, so the baseline holds no parked racers.
	time.Sleep(50 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	const n = 8
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = registry.Get(context.Background(), registryUserID(7).String())
		}()
	}
	wg.Wait()
	time.Sleep(50 * time.Millisecond)

	after := runtime.NumGoroutine()
	assert.LessOrEqual(t, after, baseline+2,
		"the race must leave no goroutine parked behind a discarded manager: %d before, %d after", baseline, after)
}
