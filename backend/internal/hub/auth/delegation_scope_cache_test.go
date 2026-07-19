package auth_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
)

// blockingWorkerStore wraps a real WorkerStore and, on the FIRST GetByID, reads
// the current row and then parks (holding that result) until release is closed,
// signalling entered once parked. It lets a test drive the
// eviction-during-resolve race deterministically: the leader reads the minter
// while it is still ACTIVE, parks holding that ACTIVE result while the test
// deregisters and evicts, then resumes into its write-back with the eviction
// generation already bumped.
type blockingWorkerStore struct {
	store.WorkerStore
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (b *blockingWorkerStore) GetByID(ctx context.Context, workerID string) (*store.Worker, error) {
	w, err := b.WorkerStore.GetByID(ctx, workerID)
	b.once.Do(func() {
		close(b.entered)
		<-b.release
	})
	return w, err
}

// blockingStore is a Store whose Workers() returns the blocking wrapper; every
// other method delegates to the embedded real store.
type blockingStore struct {
	store.Store
	workers store.WorkerStore
}

func (b *blockingStore) Workers() store.WorkerStore { return b.workers }

// A worker deregistration that races an in-flight Resolve must NOT leave the
// pre-deregistration scope cached: the leader read the minter as ACTIVE just
// before the deregister committed, so caching its ownsMinter scope would keep
// the compromised worker's cross-worker reach alive for a full TTL after the
// operator's containment action. The generation snapshot makes the write-back
// refuse a scope resolved across an eviction, so the very next Resolve re-reads
// the now-deregistered row.
func TestDelegationScopeCache_EvictionDuringResolveIsNotCached(t *testing.T) {
	ctx := context.Background()
	base := testutil.OpenTestStore(t)
	f := seedScopeUser(t, base)
	bws := &blockingWorkerStore{
		WorkerStore: base.Workers(),
		entered:     make(chan struct{}),
		release:     make(chan struct{}),
	}
	cache := auth.NewDelegationScopeCache(&blockingStore{Store: base, workers: bws})
	user := delegationUser(f.userID, f.workerID)

	type result struct {
		scope auth.DelegationWorkerScope
		err   error
	}
	done := make(chan result, 1)
	go func() {
		scope, err := cache.Resolve(ctx, user)
		done <- result{scope, err}
	}()

	// The leader has snapshotted the eviction generation and is parked in GetByID.
	<-bws.entered

	// Deregister the minter behind the cache, then evict -- the operator's
	// containment action racing the in-flight resolve.
	rows, err := base.Workers().Deregister(ctx, store.DeregisterWorkerParams{
		ID: f.workerID, RegisteredBy: f.userID,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
	cache.EvictWorker(f.workerID)

	// Let the parked resolve finish. It returns the stale ACTIVE-based scope to
	// THIS caller -- the request was already in flight, the accepted TOCTOU window
	// -- but must not cache it.
	close(bws.release)
	got := <-done
	require.NoError(t, got.err)
	assert.True(t, got.scope.Allows("another-worker-of-mine"),
		"the in-flight resolve returns the scope it computed -- the accepted TOCTOU window")

	// The very next Resolve must be a MISS that re-reads the deregistered row, not
	// a hit on a poisoned entry: without the generation guard the stale scope would
	// still grant cross-worker reach here for the rest of the TTL.
	scope, err := cache.Resolve(ctx, user)
	require.NoError(t, err)
	assert.True(t, scope.Allows(f.workerID), "the minter itself stays reachable")
	assert.False(t, scope.Allows("another-worker-of-mine"),
		"an eviction that raced the resolve must strip cross-worker reach on the very next Resolve, not a TTL later")
}

// The cache serves the memoized scope until evicted: a status change the
// eviction path missed is deliberately invisible inside the TTL (that is what
// makes SubmitOps stop paying a store round trip per batch), and EvictWorker
// is what makes deregistration -- the operator's containment action --
// immediate rather than TTL-lagged.
func TestDelegationScopeCache_ServesMemoUntilEvicted(t *testing.T) {
	ctx := context.Background()
	f := seedScopeUser(t, testutil.OpenTestStore(t))
	cache := auth.NewDelegationScopeCache(f.st)
	user := delegationUser(f.userID, f.workerID)

	scope, err := cache.Resolve(ctx, user)
	require.NoError(t, err)
	assert.True(t, scope.Allows("another-worker-of-mine"),
		"an owned ACTIVE minter lends the token cross-worker reach")

	// Deregister the minter behind the cache's back (no eviction).
	rows, err := f.st.Workers().Deregister(ctx, store.DeregisterWorkerParams{
		ID: f.workerID, RegisteredBy: f.userID,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)

	scope, err = cache.Resolve(ctx, user)
	require.NoError(t, err)
	assert.True(t, scope.Allows("another-worker-of-mine"),
		"inside the TTL and without eviction, the memoized scope is served -- this is the cache working")

	// Eviction makes the next resolve re-read the row: the deregistered minter
	// no longer lends cross-worker reach.
	cache.EvictWorker(f.workerID)
	scope, err = cache.Resolve(ctx, user)
	require.NoError(t, err)
	assert.True(t, scope.Allows(f.workerID), "the minter itself stays reachable")
	assert.False(t, scope.Allows("another-worker-of-mine"),
		"after eviction the deregistered minter's scope collapses to minter-only")
}

// Non-delegation credentials must keep ResolveDelegationWorkerScope's
// store-free fast path through the cache: a nil store makes that mechanical.
func TestDelegationScopeCache_NonDelegationIsStoreFree(t *testing.T) {
	cache := auth.NewDelegationScopeCache(nil)
	scope, err := cache.Resolve(context.Background(), &auth.UserInfo{
		ID: "u1", Credential: auth.SessionCredential("s1"),
	})
	require.NoError(t, err)
	assert.False(t, scope.IsBounded())
}

// Two bearers whose delegation credentials name the SAME minter but who are
// DIFFERENT users must receive DISTINCT resolved scopes -- the scope depends on
// the bearer's user.ID (ownsMinter + the user's own workers), so keying the
// cache and the singleflight by minter alone would serve one user's scope to
// the other if a delegation_tokens row ever bound a worker to a non-registrant
// (a mint-path bug, a hand-edited row, or any future code that bypasses
// handleMint's RegisteredBy check). Keying by (minter, user) makes that
// poisoning mechanically impossible: userB's resolve is a cache miss and
// resolves its own scope, never userA's.
func TestDelegationScopeCache_KeysByMinterAndUser(t *testing.T) {
	ctx := context.Background()
	f := seedScopeUser(t, testutil.OpenTestStore(t))
	cache := auth.NewDelegationScopeCache(f.st)

	// userA owns the minter; userB does not -- but both hold a delegation
	// credential naming the same minter (the mis-issued-token shape).
	userA := delegationUser(f.userID, f.workerID)
	userB := delegationUser(id.Generate(), f.workerID)

	scopeA, err := cache.Resolve(ctx, userA)
	require.NoError(t, err)
	assert.True(t, scopeA.Allows("another-worker-of-mine"),
		"userA owns the minter, so its token reaches userA's other workers")

	// userB resolves AFTER userA populated the cache. Under the old minter-only
	// key, userB would hit userA's cache entry (or collapse onto userA's
	// singleflight leader) and inherit scopeA. Under the (minter, user) key it
	// is a miss and resolves its own scope.
	scopeB, err := cache.Resolve(ctx, userB)
	require.NoError(t, err)
	assert.False(t, scopeB.Allows("another-worker-of-mine"),
		"userB does not own the minter; it must never inherit userA's cross-worker reach")
	assert.NotEqual(t, scopeA, scopeB,
		"the two users' scopes must be distinct entries, not one shared cache slot")
}
