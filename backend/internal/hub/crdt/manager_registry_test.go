package crdt_test

import (
	"context"
	"fmt"
	"log/slog"
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
	releaseFactory := sync.OnceFunc(func() { close(release) })
	entered := make(chan *crdt.Manager, 1)
	registry := crdt.NewRegistry(func(_ context.Context, userID userid.UserID) (*crdt.Manager, error) {
		m := crdt.NewManager(userID, newFakeJournal(), allowAll{}, nil, time.Now, crdt.WithPresenceClearGrace(time.Hour))
		m.PreparePendingPresenceClearForTest()
		entered <- m
		<-release
		return m, nil
	}, nil, crdt.WithManagerIdleTTL(0))
	var getErr error
	getDone := make(chan struct{})
	go func() {
		defer close(getDone)
		_, getErr = registry.Get(t.Context(), registryUserID(99).String())
	}()
	var created *crdt.Manager
	t.Cleanup(func() {
		releaseFactory()
		<-getDone
		registry.Shutdown(time.Second)
		if created != nil {
			created.Discard()
		}
	})
	select {
	case created = <-entered:
	case <-time.After(30 * time.Second):
		t.Fatal("Get never entered its factory")
	}
	require.Equal(t, 1, created.PendingPresenceClearsForTest())
	registry.Shutdown(time.Second)
	releaseFactory()
	<-getDone
	assert.ErrorIs(t, getErr, crdt.ErrRegistryClosed, "Get must not install a manager after shutdown")
	require.Eventually(t, func() bool { return created.PendingPresenceClearsForTest() == 0 }, 30*time.Second, time.Millisecond,
		"the rejected manager must complete its own presence teardown")
}

// Concurrent factories build distinct managers for one user. Every Get returns
// the winner, and each unstarted loser must discard its pending presence timer.
func TestRegistryGetRaceDiscardsTheDuplicate(t *testing.T) {
	const n = 8
	release := make(chan struct{})
	releaseFactories := sync.OnceFunc(func() { close(release) })
	entered := make(chan *crdt.Manager, n)
	registry := crdt.NewRegistry(func(_ context.Context, userID userid.UserID) (*crdt.Manager, error) {
		m := crdt.NewManager(userID, newFakeJournal(), allowAll{}, nil, time.Now, crdt.WithPresenceClearGrace(time.Hour))
		m.PreparePendingPresenceClearForTest()
		entered <- m
		<-release
		return m, nil
	}, nil, crdt.WithManagerIdleTTL(0))
	type result struct {
		manager *crdt.Manager
		err     error
	}
	results := make(chan result, n)
	var racers sync.WaitGroup
	for range n {
		racers.Go(func() {
			m, err := registry.Get(t.Context(), registryUserID(7).String())
			results <- result{m, err}
		})
	}
	created := make([]*crdt.Manager, 0, n)
	t.Cleanup(func() {
		releaseFactories()
		racers.Wait()
		registry.Shutdown(5 * time.Second)
		for _, m := range created {
			m.Discard()
		}
	})
	for range n {
		select {
		case m := <-entered:
			require.Equal(t, 1, m.PendingPresenceClearsForTest())
			created = append(created, m)
		case <-time.After(30 * time.Second):
			t.Fatal("the concurrent Gets did not all enter distinct factories")
		}
	}
	releaseFactories()
	var winner *crdt.Manager
	for range n {
		select {
		case got := <-results:
			require.NoError(t, got.err)
			require.NotNil(t, got.manager)
			if winner == nil {
				winner = got.manager
			}
			assert.Same(t, winner, got.manager)
		case <-time.After(30 * time.Second):
			t.Fatal("Get did not return after its factory was released")
		}
	}
	racers.Wait()
	losers := 0
	for _, m := range created {
		if m == winner {
			continue
		}
		losers++
		require.Eventually(t, func() bool { return m.PendingPresenceClearsForTest() == 0 }, 30*time.Second, time.Millisecond,
			"each unstarted loser must complete its own presence teardown")
	}
	assert.Equal(t, n-1, losers)
}

// LayoutEventWake lives on the Registry rather than the Manager, so a waiter
// captures a valid wake channel before any Manager loads and continues to wake
// even after SweepIdle evicts the Manager.
func TestRegistryLayoutEventWakeSurvivesIdleEviction(t *testing.T) {
	userID := registryUserID(8)
	registry := crdt.NewRegistry(func(ctx context.Context, owner userid.UserID) (*crdt.Manager, error) {
		m := crdt.NewManager(owner, newFakeJournal(), allowAll{}, nil, time.Now)
		if err := m.Bootstrap(ctx); err != nil {
			return nil, err
		}
		return m, nil
	}, nil, crdt.WithManagerIdleTTL(time.Millisecond))
	t.Cleanup(func() { registry.Shutdown(5 * time.Second) })

	// 1. Wake before any manager is loaded.
	chBefore := registry.LayoutEventWake(userID.String())
	registry.SignalLayoutEvents(userID.String())
	select {
	case <-chBefore:
	default:
		t.Fatal("LayoutEventWake did not close on SignalLayoutEvents before manager load")
	}

	// 2. Load a manager, capture a wake channel, evict the manager, then signal.
	_, err := registry.Get(t.Context(), userID.String())
	require.NoError(t, err)
	chAcrossEvict := registry.LayoutEventWake(userID.String())
	require.Eventually(t, func() bool {
		registry.SweepIdle()
		_, loaded := registry.Loaded(userID.String())
		return !loaded
	}, 2*time.Second, 2*time.Millisecond, "manager must be evicted by SweepIdle")

	registry.SignalLayoutEvents(userID.String())
	select {
	case <-chAcrossEvict:
	default:
		t.Fatal("LayoutEventWake did not close across SweepIdle eviction")
	}
}

// WorkerInventoryWake lives on the Registry rather than the Manager, so a
// waiter wakes on BroadcastWorkersChanged before any Manager loads, across
// SweepIdle eviction, and on Shutdown.
func TestRegistryWorkerInventoryWakeSurvivesUnloadedAndEviction(t *testing.T) {
	var factoryCalls atomic.Int32
	userID := registryUserID(10)
	otherUserID := registryUserID(11)
	registry := crdt.NewRegistry(func(ctx context.Context, owner userid.UserID) (*crdt.Manager, error) {
		factoryCalls.Add(1)
		m := crdt.NewManager(owner, newFakeJournal(), allowAll{}, nil, time.Now)
		if err := m.Bootstrap(ctx); err != nil {
			return nil, err
		}
		return m, nil
	}, nil, crdt.WithManagerIdleTTL(time.Millisecond))
	t.Cleanup(func() { registry.Shutdown(5 * time.Second) })

	// 1. Nil registry and blank userID return nil.
	var nilReg *crdt.Registry
	assert.Nil(t, nilReg.WorkerInventoryWake(userID.String()))
	assert.Nil(t, registry.WorkerInventoryWake(""))

	// 2. Wake before any manager is loaded, without invoking the factory.
	chBeforeA := registry.WorkerInventoryWake(userID.String())
	chBeforeB := registry.WorkerInventoryWake(userID.String())
	otherCh := registry.WorkerInventoryWake(otherUserID.String())
	require.NotNil(t, chBeforeA)
	assert.Equal(t, chBeforeA, chBeforeB, "concurrent callers share one wake channel")

	registry.BroadcastWorkersChanged(userID.String())
	assert.Zero(t, factoryCalls.Load(), "BroadcastWorkersChanged must not bootstrap a manager")
	select {
	case <-chBeforeA:
	default:
		t.Fatal("WorkerInventoryWake did not close on BroadcastWorkersChanged before manager load")
	}
	select {
	case <-otherCh:
		t.Fatal("BroadcastWorkersChanged woke a different user's channel")
	default:
	}

	// Subsequent call returns a fresh open channel.
	chNext := registry.WorkerInventoryWake(userID.String())
	select {
	case <-chNext:
		t.Fatal("WorkerInventoryWake returned an already-closed channel after prior wake")
	default:
	}

	// 3. Load a manager, capture a wake channel, evict the manager, then broadcast.
	_, err := registry.Get(t.Context(), userID.String())
	require.NoError(t, err)
	require.Equal(t, int32(1), factoryCalls.Load())
	chAcrossEvict := registry.WorkerInventoryWake(userID.String())
	require.Eventually(t, func() bool {
		registry.SweepIdle()
		_, loaded := registry.Loaded(userID.String())
		return !loaded
	}, 2*time.Second, 2*time.Millisecond, "manager must be evicted by SweepIdle")

	registry.BroadcastWorkersChanged(userID.String())
	assert.Equal(t, int32(1), factoryCalls.Load(), "BroadcastWorkersChanged must not re-bootstrap an evicted manager")
	select {
	case <-chAcrossEvict:
	default:
		t.Fatal("WorkerInventoryWake did not close across SweepIdle eviction")
	}

	// 4. Shutdown closes any pending wake channel.
	chShutdown := registry.WorkerInventoryWake(userID.String())
	registry.Shutdown(5 * time.Second)
	select {
	case <-chShutdown:
	default:
		t.Fatal("WorkerInventoryWake did not close on Shutdown")
	}
	select {
	case <-otherCh:
	default:
		t.Fatal("other user's WorkerInventoryWake did not close on Shutdown")
	}
}

type nonAdvancingOutbox struct {
	lists atomic.Int32
}

func (o *nonAdvancingOutbox) ListPendingLifecycleOutbox(_ context.Context, userID string) ([]crdt.LifecycleOutboxRow, error) {
	o.lists.Add(1)
	payload, err := crdt.EncodeLifecyclePayload(crdt.LifecyclePayload{
		OpType: crdt.LifecycleOpRename, WorkspaceID: "ws-stuck", NewTitle: "Same",
	}, nil)
	if err != nil {
		return nil, err
	}
	return []crdt.LifecycleOutboxRow{{ID: 42, UserID: userID, OpType: crdt.LifecycleOpRename, Payload: payload}}, nil
}

func (o *nonAdvancingOutbox) MarkLifecycleOutboxConsumed(context.Context, int64, time.Time) error {
	return nil
}

func (o *nonAdvancingOutbox) ConsumeLifecycleOutboxWithLayoutFact(context.Context, crdt.LifecycleOutboxRow, time.Time, *leapmuxv1.LayoutEventFact) error {
	return nil
}

func TestRegistryLifecycleDrainEdgeCases(t *testing.T) {
	userID := registryUserID(9)

	// 1. Nil outbox or blank userID returns an already-closed drain with nil error.
	noOutbox := crdt.NewRegistry(func(ctx context.Context, owner userid.UserID) (*crdt.Manager, error) {
		m := crdt.NewManager(owner, newFakeJournal(), allowAll{}, nil, time.Now)
		return m, m.Bootstrap(ctx)
	}, nil, crdt.WithManagerIdleTTL(0))
	t.Cleanup(func() { noOutbox.Shutdown(5 * time.Second) })
	d := noOutbox.StartLifecycleDrain(userID.String())
	select {
	case <-d.Done():
		assert.NoError(t, d.Err())
	default:
		t.Fatal("StartLifecycleDrain without outbox must complete immediately")
	}

	// 2. Caller context cancellation unblocks DrainLifecycle while the shared
	// flight finishes for another waiter.
	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce sync.Once
	releaseOnce := sync.OnceFunc(func() { close(release) })
	outbox := &gatedOutbox{entered: entered, release: release}
	reg := crdt.NewRegistry(func(ctx context.Context, owner userid.UserID) (*crdt.Manager, error) {
		m := crdt.NewManager(owner, newFakeJournal(), allowAll{}, nil, time.Now)
		return m, m.Bootstrap(ctx)
	}, nil, crdt.WithManagerIdleTTL(0), crdt.WithLifecycleOutbox(outbox))
	t.Cleanup(func() {
		releaseOnce()
		reg.Shutdown(5 * time.Second)
	})

	dBlank := reg.StartLifecycleDrain("")
	<-dBlank.Done()
	assert.NoError(t, dBlank.Err())

	cancelCtx, cancel := context.WithCancel(t.Context())
	cancelDone := make(chan error, 1)
	go func() {
		cancelDone <- reg.DrainLifecycle(cancelCtx, userID.String())
	}()
	<-entered
	enteredOnce.Do(func() {})

	secondDone := make(chan error, 1)
	go func() {
		secondDone <- reg.DrainLifecycle(t.Context(), userID.String())
	}()

	cancel()
	require.ErrorIs(t, <-cancelDone, context.Canceled)

	releaseOnce()
	require.NoError(t, <-secondDone)

	// 3. Shutdown makes subsequent StartLifecycleDrain return ErrRegistryClosed.
	reg.Shutdown(5 * time.Second)
	closedDrain := reg.StartLifecycleDrain(userID.String())
	<-closedDrain.Done()
	assert.ErrorIs(t, closedDrain.Err(), crdt.ErrRegistryClosed)

	// 4. Non-advancing leading row ID stops after two passes instead of looping forever.
	stuckOutbox := &nonAdvancingOutbox{}
	stuckReg := crdt.NewRegistry(func(ctx context.Context, owner userid.UserID) (*crdt.Manager, error) {
		m := crdt.NewManager(owner, newFakeJournal(), allowAll{}, nil, time.Now)
		return m, m.Bootstrap(ctx)
	}, nil, crdt.WithManagerIdleTTL(0), crdt.WithLifecycleOutbox(stuckOutbox))
	t.Cleanup(func() { stuckReg.Shutdown(5 * time.Second) })
	require.NoError(t, stuckReg.DrainLifecycle(t.Context(), userID.String()))
	assert.Equal(t, int32(2), stuckOutbox.lists.Load(), "non-advancing outbox must stop on the second pass")
}
