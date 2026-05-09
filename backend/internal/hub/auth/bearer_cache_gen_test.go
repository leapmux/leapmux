package auth

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestBearerCacheFresh_TTL pins down the TTL component of cache
// freshness. An entry beyond sessionCacheTTL is stale regardless of
// generation; an entry within TTL with matching generation is fresh.
func TestBearerCacheFresh_TTL(t *testing.T) {
	a := &authInterceptor{revocationGen: &atomic.Uint64{}}
	now := time.Now()

	fresh := cachedSession{user: &UserInfo{ID: "u"}, cachedAt: now, gen: 0}
	assert.True(t, a.bearerCacheFresh(fresh, 0), "newly cached entry must be fresh")

	stale := cachedSession{user: &UserInfo{ID: "u"}, cachedAt: now.Add(-2 * sessionCacheTTL), gen: 0}
	assert.False(t, a.bearerCacheFresh(stale, 0), "entry beyond TTL must be stale")
}

// TestBearerCacheFresh_GenerationBump asserts that bumping the
// revocation generation makes a previously-fresh entry stale on the
// next read. This is what gives the cache its cross-process-aware
// invalidation: a revoke handler (in this process or via the
// revocationwatcher syncing another process's revoke) bumps gen,
// and every cache entry written under the prior generation is
// treated as a miss on the next hit — even if the entry itself
// wasn't the one targeted by the evict.
func TestBearerCacheFresh_GenerationBump(t *testing.T) {
	a := &authInterceptor{revocationGen: &atomic.Uint64{}}
	cs := cachedSession{user: &UserInfo{ID: "u"}, cachedAt: time.Now(), gen: a.revocationGen.Load()}
	assert.True(t, a.bearerCacheFresh(cs, a.revocationGen.Load()))

	// Simulate an Evict* call that bumps the generation.
	a.revocationGen.Add(1)
	assert.False(t, a.bearerCacheFresh(cs, a.revocationGen.Load()),
		"entry written before the gen bump must now be stale")
}

// TestSessionCache_EvictsBumpGen runs each Evict* surface through one
// call and asserts the generation moves forward. This is the
// invariant the bearerCacheFresh check relies on: any eviction
// (specific bearer, all bearers for a user, sessions for a user, or
// a single session) must invalidate concurrent reads against the
// pre-eviction cache.
func TestSessionCache_EvictsBumpGen(t *testing.T) {
	cases := []struct {
		name string
		fn   func(*SessionCache)
	}{
		{"EvictBearer", func(c *SessionCache) { c.EvictBearer("some-token") }},
		{"EvictBearersByUserID", func(c *SessionCache) { c.EvictBearersByUserID("u-1") }},
		{"EvictByUserID", func(c *SessionCache) { c.EvictByUserID("u-1") }},
		{"Evict", func(c *SessionCache) { c.Evict("sess-1") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, sc := NewInterceptorWithTokens(nil, nil, nil, false, false)
			t.Cleanup(sc.Stop)
			before := sc.revocationGen.Load()
			tc.fn(sc)
			assert.Greater(t, sc.revocationGen.Load(), before, "%s must bump the revocation generation", tc.name)
		})
	}
}
