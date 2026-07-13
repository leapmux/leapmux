package crdt_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

	factory := func(_ context.Context, orgID string) (*crdt.Manager, error) {
		factoryCalls.Add(1)
		mgr := crdt.NewManager(orgID, journal, allowAll{}, nil, time.Now)
		require.NoError(t, mgr.Bootstrap(context.Background()))
		return mgr, nil
	}

	registry := crdt.NewRegistry(factory, nil, crdt.WithManagerIdleTTL(10*time.Millisecond))
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

	mgr1, err := registry.Get(context.Background(), "org-1")
	require.NoError(t, err)
	require.NotNil(t, mgr1)
	require.Equal(t, int32(1), factoryCalls.Load())

	// While the manager is "fresh" (activity just stamped during
	// Bootstrap), SweepIdle is a no-op.
	registry.SweepIdle()
	mgr1Again, err := registry.Get(context.Background(), "org-1")
	require.NoError(t, err)
	assert.Same(t, mgr1, mgr1Again, "fresh manager survives a sweep")
	assert.Equal(t, int32(1), factoryCalls.Load())

	// Past the idleTTL window with no subscribers and no submits, the
	// sweep evicts the manager. The next Get re-invokes the factory
	// and returns a fresh instance.
	time.Sleep(15 * time.Millisecond)
	registry.SweepIdle()

	mgr2, err := registry.Get(context.Background(), "org-1")
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

	factory := func(_ context.Context, orgID string) (*crdt.Manager, error) {
		factoryCalls.Add(1)
		mgr := crdt.NewManager(orgID, journal, allowAll{}, nil, time.Now)
		require.NoError(t, mgr.Bootstrap(context.Background()))
		return mgr, nil
	}

	registry := crdt.NewRegistry(factory, nil, crdt.WithManagerIdleTTL(10*time.Millisecond))
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

	mgr1, err := registry.Get(context.Background(), "org-1")
	require.NoError(t, err)
	require.Equal(t, int32(1), factoryCalls.Load())

	// Let the manager go stale (its only activity was the Bootstrap stamp).
	time.Sleep(15 * time.Millisecond)

	// A Get of the stale manager must refresh its activity...
	mgr1Again, err := registry.Get(context.Background(), "org-1")
	require.NoError(t, err)
	require.Same(t, mgr1, mgr1Again, "the stale manager is still the one returned")
	require.Equal(t, int32(1), factoryCalls.Load(), "Get must not re-bootstrap a still-present manager")

	// ...so an immediately-following sweep leaves it in place rather than
	// evicting the manager the caller just received.
	registry.SweepIdle()

	mgr1Third, err := registry.Get(context.Background(), "org-1")
	require.NoError(t, err)
	assert.Same(t, mgr1, mgr1Third, "a manager a recent Get returned must survive the next sweep")
	assert.Equal(t, int32(1), factoryCalls.Load(), "no eviction, so no re-bootstrap")
}

// TestRegistry_SweepIdle_KeepsManagersWithSubscribers documents the
// safety net: a manager with a live subscriber is never evicted, even
// if its last submit predates the idleTTL window.
func TestRegistry_SweepIdle_KeepsManagersWithSubscribers(t *testing.T) {
	journal := newFakeJournal()

	factory := func(_ context.Context, orgID string) (*crdt.Manager, error) {
		mgr := crdt.NewManager(orgID, journal, allowAll{}, nil, time.Now)
		require.NoError(t, mgr.Bootstrap(context.Background()))
		return mgr, nil
	}

	registry := crdt.NewRegistry(factory, nil, crdt.WithManagerIdleTTL(10*time.Millisecond))
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

	mgr, err := registry.Get(context.Background(), "org-attached")
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

	mgrAgain, err := registry.Get(context.Background(), "org-attached")
	require.NoError(t, err)
	assert.Same(t, mgr, mgrAgain, "attached manager is not evicted")
}

// TestRegistry_DisabledJanitor_KeepsManagersForever pins the opt-out:
// passing WithManagerIdleTTL(0) keeps the legacy "managers live
// forever" behavior and never spawns a janitor goroutine.
func TestRegistry_DisabledJanitor_KeepsManagersForever(t *testing.T) {
	journal := newFakeJournal()

	factory := func(_ context.Context, orgID string) (*crdt.Manager, error) {
		mgr := crdt.NewManager(orgID, journal, allowAll{}, nil, time.Now)
		require.NoError(t, mgr.Bootstrap(context.Background()))
		return mgr, nil
	}

	registry := crdt.NewRegistry(factory, nil, crdt.WithManagerIdleTTL(0))
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

	mgr1, err := registry.Get(context.Background(), "org-x")
	require.NoError(t, err)
	mgr2, err := registry.Get(context.Background(), "org-x")
	require.NoError(t, err)
	assert.Same(t, mgr1, mgr2, "managers are stable across Get when janitor is disabled")
}
