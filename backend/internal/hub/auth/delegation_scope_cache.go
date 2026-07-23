package auth

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/leapmux/leapmux/internal/hub/store"
)

// delegationScopeCacheTTL bounds how long a resolved worker scope may be
// served without re-reading the minter row. It matches the interceptor's
// sessionCacheTTL: both memoize a per-credential authorization fact whose
// authoritative row rarely changes, and both treat the TTL as the backstop
// behind an explicit eviction path.
const delegationScopeCacheTTL = 30 * time.Second

// delegationScopeCacheSweepThreshold bounds how large the cache map may grow
// before a miss triggers a sweep of TTL-expired entries. The TTL governs
// freshness on read (an entry past its TTL is a miss) but, without a sweep,
// never REMOVES an entry -- so workers retired by a path that does not call
// EvictWorker (the user-delete cascade's MarkAllWorkersDeletedByUser, a future
// bulk retire) would leave one entry per retired worker in the map for the
// process's whole life. EvictWorker (synchronous, on DeregisterWorker) stays
// the immediate path; this sweep is the bounded-memory backstop for every
// other retirement path, run lazily on a miss so the hot read path (RLock) is
// untouched. A cold working set small relative to the threshold is left alone:
// a few stale entries below the threshold are negligible, and sweeping only
// once the map is large bounds the worst case without per-Resolve overhead.
// A var (not a const) so tests can shrink it and exercise the sweep directly.
var delegationScopeCacheSweepThreshold = 512

type delegationScopeEntry struct {
	scope    DelegationWorkerScope
	cachedAt time.Time
}

// delegationCacheKey is the (minter, bearer-user) pair a resolved scope is
// cached under. The scope ResolveDelegationWorkerScope returns is a function of
// BOTH the minter row (its registrant + status) AND the calling user.ID
// (ownsMinter = user.ID.Matches(minter.RegisteredBy), and the allowed-worker set is
// the minter plus the user's own workers) -- so keying by minter alone would
// serve one user's resolved scope to another user collapsed onto the same
// singleflight leader if a delegation_tokens row ever bound a worker to a user
// who is not its registrant (a mint-path bug, a hand-edited row, or any future
// code that bypasses handleMint's RegisteredBy check). Keying by (minter, user)
// makes that poisoning mechanically impossible rather than relying on the mint
// path's check, at no cost in the common case where a minter serves exactly one
// user and the collapse behaves identically to a minter-only key.
type delegationCacheKey struct {
	minter string
	user   string
}

// DelegationScopeCache memoizes ResolveDelegationWorkerScope by minting
// worker.
//
// SubmitOps is the hottest delegation-bearer RPC -- an agent session submits
// presence/cursor/tile batches continuously -- and every call re-resolved the
// scope with a Workers().GetByID round trip; concurrent submits from one agent
// stampeded identical lookups. The scope is cached by (minter, bearer-user):
// minter, because it is the row the scope is resolved against; user, because the
// resolved ownsMinter flag and the allowed-worker set depend on the bearer's
// user.ID and serving one user's scope to another (e.g. via a singleflight
// leader whose `user` closure a follower inherited) would grant cross-worker
// reach the bearer's token must not have. Hits take a read lock (an RWMutex, so
// concurrent delegation bearers do not serialize on hot cache hits), concurrent
// misses collapse through a singleflight keyed on the same (minter, user) pair,
// and the leader resolves under a context decoupled from any single caller
// (mirroring the interceptor's bearer flight) so one cancelled caller cannot
// fail the followers waiting on the same key.
//
// Staleness is bounded on two axes rather than left to the TTL alone:
//
//   - The worker-deregistration path calls EvictWorker synchronously, so the
//     operator's one containment action against a compromised worker --
//     deregistering it -- still strips its outstanding tokens' cross-worker
//     reach on the very next SubmitOps, not a TTL later.
//   - The TTL is the backstop for mutations that bypass that path (the
//     user-delete cascade that soft-deletes workers -- moot in practice, since
//     a deleted user's bearer already fails validation at the interceptor --
//     or a direct store mutation).
//
// Resolution ERRORS are never cached: a transient store fault must stay
// retryable, and the fail-closed deny-all scope it returns must not outlive
// the fault.
type DelegationScopeCache struct {
	st     store.Store
	now    func() time.Time
	flight singleflight.Group

	// evictGen is bumped by EvictWorker (under mu). A Resolve leader snapshots it
	// BEFORE its store read and, at write-back, refuses to cache a scope resolved
	// across an eviction. Without it, a resolve that read the minter as ACTIVE just
	// before a concurrent DeregisterWorker committed would take the write lock
	// AFTER EvictWorker swept an as-yet-absent key and store its now-stale
	// ownsMinter scope, leaving the compromised worker cross-worker reach cached
	// for a full TTL -- exactly the containment latency EvictWorker exists to
	// avoid. This mirrors the interceptor's revocationGen snapshot
	// (interceptor.go's validateTokenCached).
	evictGen atomic.Uint64

	mu sync.RWMutex
	// lastSweptAt is when sweepExpiredLocked last ran, so a working set sustained
	// above the sweep threshold sweeps at most once per TTL rather than on ~every
	// miss (each a full O(map) scan under the write lock that serializes hot-path
	// RLock readers).
	lastSweptAt time.Time
	entries     map[delegationCacheKey]delegationScopeEntry
}

// NewDelegationScopeCache returns an empty cache resolving against st.
func NewDelegationScopeCache(st store.Store) *DelegationScopeCache {
	return &DelegationScopeCache{
		st:      st,
		now:     time.Now,
		entries: make(map[delegationCacheKey]delegationScopeEntry),
	}
}

// Resolve is ResolveDelegationWorkerScope with the per-(minter, user) cache
// applied. The non-delegation and unresolvable-minter fast paths never touch
// the cache, so they keep ResolveDelegationWorkerScope's exact contract
// (including the fail-closed deny-all value on error).
func (c *DelegationScopeCache) Resolve(ctx context.Context, user *UserInfo) (DelegationWorkerScope, error) {
	minterID, bounded, err := delegationMinter(user)
	if err != nil {
		return DenyAllScope(), err
	}
	if !bounded {
		// A non-delegation credential is UNBOUNDED; constructed explicitly since the
		// zero value now fails closed (see UnboundedScope).
		return UnboundedScope(), nil
	}
	key := delegationCacheKey{minter: minterID, user: user.ID.String()}

	c.mu.RLock()
	if e, ok := c.entries[key]; ok && c.now().Sub(e.cachedAt) < delegationScopeCacheTTL {
		c.mu.RUnlock()
		return e.scope, nil
	}
	c.mu.RUnlock()

	v, err, _ := c.flight.Do(singleflightKey(key.minter, key.user), func() (any, error) {
		// Snapshot the eviction generation BEFORE the store read so an EvictWorker
		// that races this resolve (a concurrent DeregisterWorker) is detected at
		// write-back below. Mirrors the interceptor's validationGen snapshot.
		gen := c.evictGen.Load()
		// Decoupled from the caller so a cancelled leader cannot fail the
		// followers collapsed onto this key; bounded so a wedged store cannot
		// pin the flight forever (the same shape as the interceptor's bearer
		// validation flight).
		workCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), delegationScopeCacheTTL)
		defer cancel()
		scope, rerr := ResolveDelegationWorkerScope(workCtx, c.st, user)
		if rerr != nil {
			return nil, rerr
		}
		c.mu.Lock()
		// Refuse to cache a scope resolved ACROSS an eviction of this minter: the
		// row it was computed from may predate the deregistration that evicted the
		// key, so storing it would re-poison the slot EvictWorker just swept and
		// keep a stale ownsMinter scope alive for a full TTL. The scope is still
		// RETURNED to this caller -- its request was already in flight when the
		// eviction landed, the inherent TOCTOU window the interceptor also accepts
		// -- but every LATER Resolve is now a miss that re-reads the current row, so
		// deregistration strips cross-worker reach on the very next SubmitOps rather
		// than a TTL later.
		if c.evictGen.Load() == gen {
			c.entries[key] = delegationScopeEntry{scope: scope, cachedAt: c.now()}
			c.maybeSweepLocked()
		}
		c.mu.Unlock()
		return scope, nil
	})
	if err != nil {
		return DenyAllScope(), err
	}
	return v.(DelegationWorkerScope), nil
}

// EvictWorker drops every cached scope minted by workerID so the next Resolve
// re-reads the row. Called synchronously by the worker-deregistration path; a
// workerID with no entries is a no-op. Scopes are keyed by (minter, user), so
// deregistration scans once to drop every user that cached against this minter
// (in practice exactly one, since a minter serves its registrant, but the sweep
// is over a small map and is correct regardless of how many users cached).
func (c *DelegationScopeCache) EvictWorker(workerID string) {
	c.mu.Lock()
	for k := range c.entries {
		if k.minter == workerID {
			delete(c.entries, k)
		}
	}
	// Bump under the lock so a Resolve write-back that acquires mu after this
	// eviction observes the new generation and skips caching a scope it resolved
	// against the pre-deregistration row. A resolve whose key was not yet present
	// (its store read still in flight) is caught here rather than by the delete
	// above -- the delete is a no-op for it, the generation check is not.
	c.evictGen.Add(1)
	c.mu.Unlock()
}

// singleflightKey renders a (minter, user) pair into the string key the
// singleflight.Group.Do call requires, in a way that cannot collapse two
// DIFFERENT pairs into one string. A bare separator like `minter + "\x00" +
// user` collides when an ID itself contains the separator byte: the pairs
// ("a\x00b","c") and ("a","b\x00c") both yield "a\x00b\x00c", so the second
// pair's follower would inherit the first pair's leader and be served a scope
// resolved for the wrong user -- the exact cross-worker-reach poisoning the
// (minter, user) key exists to prevent. Worker/user IDs are system-generated
// (UUIDs / numeric) so the collision is unreachable today, but length-prefixing
// removes the assumption rather than resting on an ID-format invariant a future
// slug or hand-edited row could violate.
func singleflightKey(minter, user string) string {
	// fmt is avoided on this hot path: it reflects and allocates more than a pair
	// of strconv.AppendInt calls into a pre-sized buffer. The decimal lengths are
	// bounded (the IDs themselves dominate), and the format is stable for any ID
	// content including empty strings and bytes that happen to be digits.
	lm := len(minter)
	lu := len(user)
	// Worst case: two digits for each length prefix (IDs are not gigabytes), a
	// ':' and '|' separator, plus the two IDs.
	size := 2 + lm + 1 + lu
	if size < 16 {
		size = 16
	}
	b := make([]byte, 0, size)
	b = strconv.AppendInt(b, int64(lm), 10)
	b = append(b, ':')
	b = append(b, minter...)
	b = append(b, '|')
	b = strconv.AppendInt(b, int64(lu), 10)
	b = append(b, ':')
	b = append(b, user...)
	return string(b)
}

// maybeSweepLocked runs a sweep only when the map has grown past the threshold
// AND none has run within a TTL. The threshold keeps a small map off the sweep
// path entirely; the interval keeps a working set sustained above the threshold
// from sweeping on ~every miss -- each sweep is a full O(map) scan under the
// write lock, and once per TTL is enough since an entry is not evictable until
// it is itself past the TTL. Caller holds c.mu for writing.
func (c *DelegationScopeCache) maybeSweepLocked() {
	if len(c.entries) < delegationScopeCacheSweepThreshold {
		return
	}
	now := c.now()
	if now.Sub(c.lastSweptAt) < delegationScopeCacheTTL {
		return
	}
	c.lastSweptAt = now
	c.sweepExpiredLocked()
}

// sweepExpiredLocked drops entries whose cachedAt is past the TTL. It is the
// bounded-memory backstop behind EvictWorker: the TTL governs freshness on
// read (a stale entry is a miss and re-resolves) but, without removal, never
// frees the slot, so workers retired by paths that do not call EvictWorker
// would accumulate entries for the process's life. Caller holds c.mu for
// writing; run lazily on a miss once the map crosses the sweep threshold so
// the hot read path stays RLock-only.
func (c *DelegationScopeCache) sweepExpiredLocked() {
	now := c.now()
	for k, e := range c.entries {
		if now.Sub(e.cachedAt) >= delegationScopeCacheTTL {
			delete(c.entries, k)
		}
	}
}
