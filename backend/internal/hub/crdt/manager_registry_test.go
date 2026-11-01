package crdt_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

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

// TestRegistry_BroadcastWorkersChanged_ReachesEverySubscriberOfTheUser pins
// the unfiltered fan-out: the worker list belongs to the user, so a
// subscriber whose filter admits no workspace hears the change as one that
// admits a workspace does. Another user's subscriber hears nothing.
func TestRegistry_BroadcastWorkersChanged_ReachesEverySubscriberOfTheUser(t *testing.T) {
	journal := newFakeJournal()
	factory := func(_ context.Context, userID userid.UserID) (*crdt.Manager, error) {
		mgr := crdt.NewManager(userID, journal, allowAll{}, nil, time.Now)
		require.NoError(t, mgr.Bootstrap(context.Background()))
		return mgr, nil
	}
	registry := crdt.NewRegistry(factory, nil, crdt.WithManagerIdleTTL(0))
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

	subscribe := func(userID string, filter crdt.SubscriberFilter) *captureSubscriber {
		mgr, err := registry.Get(context.Background(), userID)
		require.NoError(t, err)
		listener := &captureSubscriber{}
		_, unsub := mgr.Subscribe(&crdt.Subscriber{UserID: userID, Filter: filter, Send: listener.send})
		t.Cleanup(unsub)
		return listener
	}
	noWorkspace := subscribe("user-1", crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{}})
	oneWorkspace := subscribe("user-1", crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"ws-1": true}})
	otherUser := subscribe("user-2", crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"ws-2": true}})

	// The fan-out calls each subscriber's Send before it returns.
	registry.BroadcastWorkersChanged("user-1")

	assert.Equal(t, 1, workersChangedCount(noWorkspace.snapshot()), "a subscriber that admits no workspace still hears it")
	assert.Equal(t, 1, workersChangedCount(oneWorkspace.snapshot()))
	assert.Zero(t, workersChangedCount(otherUser.snapshot()), "another user's subscriber hears nothing")
}
