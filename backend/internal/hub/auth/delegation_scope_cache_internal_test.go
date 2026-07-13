package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestCache returns a DelegationScopeCache whose clock is `now`, so a test
// can advance time past the TTL without sleeping and assert which entries the
// sweep keeps. The store is unused on the sweep path (resolution errors are
// never cached, and these tests never resolve).
func newTestCache(now time.Time) *DelegationScopeCache {
	t := now
	return &DelegationScopeCache{
		st:      nil,
		now:     func() time.Time { return t },
		entries: make(map[delegationCacheKey]delegationScopeEntry),
	}
}

// advance bumps the test cache's clock by d.
func (c *DelegationScopeCache) advance(d time.Duration) {
	prev := c.now()
	c.now = func() time.Time { return prev.Add(d) }
}

// sweepExpiredLocked drops only entries past the TTL, leaving fresh entries
// (and the deny-all/empty scopes a real resolve would cache) intact. The TTL
// is the freshness budget the read path already treats as a miss; this test
// pins that the sweep actually FREES the slots those misses leave behind,
// which is the whole point of the bounded-memory backstop.
func TestDelegationScopeCache_SweepExpiredLocked(t *testing.T) {
	start := time.UnixMilli(1_000_000)
	c := newTestCache(start)

	fresh := DenyAllScope()
	stale := DenyAllScope()
	c.entries[delegationCacheKey{minter: "w1", user: "u1"}] = delegationScopeEntry{scope: fresh, cachedAt: start}
	c.entries[delegationCacheKey{minter: "w2", user: "u2"}] = delegationScopeEntry{scope: stale, cachedAt: start}

	// Advance past the TTL, then sweep.
	c.advance(delegationScopeCacheTTL + time.Second)
	// Re-add a fresh entry under the advanced clock so one entry is within TTL.
	c.entries[delegationCacheKey{minter: "w3", user: "u3"}] = delegationScopeEntry{scope: fresh, cachedAt: c.now()}

	c.sweepExpiredLocked()

	_, hasW1 := c.entries[delegationCacheKey{minter: "w1", user: "u1"}]
	_, hasW2 := c.entries[delegationCacheKey{minter: "w2", user: "u2"}]
	_, hasW3 := c.entries[delegationCacheKey{minter: "w3", user: "u3"}]
	assert.False(t, hasW1, "an entry past its TTL must be swept")
	assert.False(t, hasW2, "an entry past its TTL must be swept")
	assert.True(t, hasW3, "an entry within its TTL must survive the sweep")
}

// The sweep fires lazily on a cache MISS once the map crosses the sweep
// threshold, bounding memory for retirement paths that never call EvictWorker
// (the user-delete cascade). The hot read path is untouched: only the
// singleflight leader (a miss) ever sweeps, under the write lock it already
// holds. This pins the gating: with the threshold lowered, a miss past the
// threshold triggers the sweep and reclaims the stale entries.
func TestDelegationScopeCache_SweepsOnMissPastThreshold(t *testing.T) {
	old := delegationScopeCacheSweepThreshold
	delegationScopeCacheSweepThreshold = 2
	defer func() { delegationScopeCacheSweepThreshold = old }()

	start := time.UnixMilli(2_000_000)
	c := newTestCache(start)
	// Seed two stale entries (past the TTL after we advance) so the map is at the
	// threshold before the miss.
	c.entries[delegationCacheKey{minter: "stale1", user: "u1"}] = delegationScopeEntry{scope: DenyAllScope(), cachedAt: start}
	c.entries[delegationCacheKey{minter: "stale2", user: "u2"}] = delegationScopeEntry{scope: DenyAllScope(), cachedAt: start}
	c.advance(delegationScopeCacheTTL + time.Second)

	// Trigger the sweep gating directly: simulate what the singleflight leader
	// does after a successful resolve -- insert, then maybeSweepLocked.
	c.mu.Lock()
	c.entries[delegationCacheKey{minter: "fresh", user: "u3"}] = delegationScopeEntry{scope: DenyAllScope(), cachedAt: c.now()}
	c.maybeSweepLocked()
	c.mu.Unlock()

	_, hasStale1 := c.entries[delegationCacheKey{minter: "stale1", user: "u1"}]
	_, hasStale2 := c.entries[delegationCacheKey{minter: "stale2", user: "u2"}]
	_, hasFresh := c.entries[delegationCacheKey{minter: "fresh", user: "u3"}]
	assert.False(t, hasStale1 && hasStale2, "stale entries past the threshold must be reclaimed on a miss")
	require.True(t, hasFresh, "the just-inserted fresh entry must survive")
	assert.LessOrEqual(t, len(c.entries), delegationScopeCacheSweepThreshold,
		"the map must be at or below the threshold after the sweep")
}

// A map below the threshold is not swept on a miss -- a few stale entries
// below the threshold are negligible, and sweeping only once the map is large
// keeps the common case off the sweep path.
func TestDelegationScopeCache_DoesNotSweepBelowThreshold(t *testing.T) {
	old := delegationScopeCacheSweepThreshold
	delegationScopeCacheSweepThreshold = 10
	defer func() { delegationScopeCacheSweepThreshold = old }()

	start := time.UnixMilli(3_000_000)
	c := newTestCache(start)
	c.entries[delegationCacheKey{minter: "stale1", user: "u1"}] = delegationScopeEntry{scope: DenyAllScope(), cachedAt: start}
	c.advance(delegationScopeCacheTTL + time.Second)

	// Below threshold: the leader's gating skips the sweep.
	c.mu.Lock()
	c.maybeSweepLocked()
	c.mu.Unlock()

	_, hasStale := c.entries[delegationCacheKey{minter: "stale1", user: "u1"}]
	assert.True(t, hasStale, "a sub-threshold map must not be swept on a miss")
}

// Above the threshold, maybeSweepLocked still runs at most once per TTL: a
// second miss within the interval must NOT re-scan the map, so a stale entry
// added after the first sweep survives until the interval elapses. This pins
// the interval gate that keeps a working set sustained above the threshold from
// sweeping (a full O(map) scan under the write lock) on ~every miss.
func TestDelegationScopeCache_SweepIntervalGated(t *testing.T) {
	old := delegationScopeCacheSweepThreshold
	delegationScopeCacheSweepThreshold = 1
	defer func() { delegationScopeCacheSweepThreshold = old }()

	start := time.UnixMilli(4_000_000)
	c := newTestCache(start)

	// First sweep past the interval (lastSweptAt is the zero time) reclaims a
	// stale entry.
	c.entries[delegationCacheKey{minter: "w1", user: "u1"}] = delegationScopeEntry{scope: DenyAllScope(), cachedAt: start}
	c.advance(delegationScopeCacheTTL + time.Second)
	c.mu.Lock()
	c.maybeSweepLocked()
	c.mu.Unlock()
	_, hasW1 := c.entries[delegationCacheKey{minter: "w1", user: "u1"}]
	require.False(t, hasW1, "the first sweep past the interval reclaims the stale entry")

	// A second stale entry, then a miss only TTL/2 later: within the interval of
	// the just-run sweep, so maybeSweepLocked must skip and the entry survives.
	c.entries[delegationCacheKey{minter: "w2", user: "u2"}] = delegationScopeEntry{scope: DenyAllScope(), cachedAt: start}
	c.advance(delegationScopeCacheTTL / 2)
	c.mu.Lock()
	c.maybeSweepLocked()
	c.mu.Unlock()
	_, hasW2 := c.entries[delegationCacheKey{minter: "w2", user: "u2"}]
	assert.True(t, hasW2, "a second sweep within the interval is skipped, so the stale entry survives until the interval elapses")
}

// singleflightKey must not collapse two DIFFERENT (minter, user) pairs into one
// string. A bare separator (minter + "\x00" + user) collides when an ID itself
// contains the separator byte, so the second pair's singleflight follower would
// inherit the first pair's leader and be served a scope resolved for the wrong
// user -- the cross-worker-reach poisoning the (minter, user) key exists to
// prevent. Length-prefixing makes the encoding uniquely decodable for any ID
// content.
func TestSingleflightKey_IsInjective(t *testing.T) {
	// Two pairs that a naive "\x00"-joined key would render identically.
	a := singleflightKey("a\x00b", "c")
	b := singleflightKey("a", "b\x00c")
	assert.NotEqual(t, a, b, "pairs differing only by where the separator byte sits must map to different keys")

	// The empty-minter and empty-user edge cases must not collapse into each
	// other or into a non-empty pair.
	assert.NotEqual(t, singleflightKey("", "x"), singleflightKey("x", ""))
	assert.NotEqual(t, singleflightKey("", ""), singleflightKey("", "0"))

	// The same pair must render identically (stable, so the leader and a later
	// follower built from the same inputs collapse onto the same flight).
	assert.Equal(t, singleflightKey("m1", "u1"), singleflightKey("m1", "u1"))

	// IDs that happen to contain the length-prefix's delimiters (':', '|') or
	// digits must still disambiguate.
	assert.NotEqual(t,
		singleflightKey("1:2|3", "u"),
		singleflightKey("1", ":2|3u"),
	)
}
