package crossworker

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// tabTypeAgent is the int32 value the hub-side mint endpoint expects
// for an agent-spawn provenance. Pinned as a constant so the tests
// don't drag a proto cast onto every Acquire call.
var tabTypeAgent = int32(leapmuxv1.TabType_TAB_TYPE_AGENT)

// tabTypeTerminal is the same for a terminal spawn. The restart interleaving
// that shares one tab id across two live spawns is terminal-specific, so the
// tests covering it use this rather than the agent value.
var tabTypeTerminal = int32(leapmuxv1.TabType_TAB_TYPE_TERMINAL)

// TestDelegationStore_MintRetriesOnTabPropagation simulates the
// AddTab → mint race: the hub returns 403 "tab not owned by calling
// worker" until the workspace_tabs row becomes visible, then 200.
// The store must transparently retry until success.
func TestDelegationStore_MintRetriesOnTabPropagation(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n < 3 {
			http.Error(w, "tab not owned by calling worker", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"lmx_tok_secret","token_id":"tok","expires_in":600}`))
	}))
	t.Cleanup(srv.Close)

	store := NewDelegationStore(srv.URL, "worker-auth", "worker-1")
	store.LiveTab = staticLiveTab("tab-1", tabTypeAgent)
	store.MintRetryBackoff = 5 * time.Millisecond
	store.MintMaxAttempts = 6
	store.Acquire(userid.MustNew("user-1"))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)

	bearer, err := store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("user-1"), AgentID: "agent-1"})
	require.NoError(t, err)
	assert.Equal(t, "lmx_tok_secret", bearer)
	assert.Equal(t, int32(3), calls.Load(), "expected 2 propagation 403s + 1 success")
}

// TestDelegationStore_MintGivesUpAfterMaxAttempts ensures the retry is
// bounded — if the tab never propagates, GetBearer must surface the
// propagation error rather than spinning forever.
func TestDelegationStore_MintGivesUpAfterMaxAttempts(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "tab not owned by calling worker", http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	store := NewDelegationStore(srv.URL, "worker-auth", "worker-1")
	store.LiveTab = staticLiveTab("tab-1", tabTypeAgent)
	store.MintRetryBackoff = 1 * time.Millisecond
	store.MintMaxAttempts = 4
	store.Acquire(userid.MustNew("user-1"))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)

	_, err := store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("user-1"), AgentID: "agent-1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tab not yet visible to hub")
	assert.Equal(t, int32(4), calls.Load(), "should have used all attempts")
}

// TestDelegationStore_MintNonPropagationErrorDoesNotRetry confirms that
// non-propagation 4xx responses (auth failure, bad workspace) abort
// immediately rather than burning the retry budget.
func TestDelegationStore_MintNonPropagationErrorDoesNotRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "user lacks workspace access", http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	store := NewDelegationStore(srv.URL, "worker-auth", "worker-1")
	store.LiveTab = staticLiveTab("tab-1", tabTypeAgent)
	store.MintRetryBackoff = 1 * time.Millisecond
	store.MintMaxAttempts = 6
	store.Acquire(userid.MustNew("user-1"))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)

	_, err := store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("user-1"), AgentID: "agent-1"})
	require.Error(t, err)
	assert.Equal(t, int32(1), calls.Load(), "non-propagation errors must not retry")
}

// TestDelegationStore_GetBearerCachesPerUser verifies the cache is keyed by
// user: repeated calls for the same user hit the cache, whatever the spawn
// provenance carried alongside it.
func TestDelegationStore_GetBearerCachesPerUser(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"lmx_tok_secret","token_id":"tok","expires_in":3600}`))
	}))
	t.Cleanup(srv.Close)

	store := NewDelegationStore(srv.URL, "worker-auth", "worker-1")
	store.LiveTab = staticLiveTab("tab-1", tabTypeAgent)
	store.Acquire(userid.MustNew("u-1"))
	store.Acquire(userid.MustNew("u-1"))
	ctx := context.Background()

	_, err := store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("u-1"), AgentID: "agent-1"})
	require.NoError(t, err)
	_, err = store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("u-1"), AgentID: "agent-1"})
	require.NoError(t, err)
	assert.Equal(t, int32(1), calls.Load(), "second call for the same user must hit cache")

	// A second spawn of the same user, in whatever workspace: still one bearer.
	// The workspace axis is gone, so this must NOT mint again.
	_, err = store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("u-1"), AgentID: "agent-2"})
	require.NoError(t, err)
	assert.Equal(t, int32(1), calls.Load(), "a second spawn of the same user shares one bearer")

	// A DIFFERENT user is a different slot and does mint.
	store.Acquire(userid.MustNew("u-2"))
	_, err = store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("u-2"), AgentID: "agent-3"})
	require.NoError(t, err)
	assert.Equal(t, int32(2), calls.Load(), "a different user must mint a fresh delegation")
}

// TestDelegationStore_GetBearerRemintsNearExpiry exercises the grace
// window: when the cached bearer is within MintGracePeriod of expiry,
// the next call must mint a fresh pair rather than return the
// soon-to-be-expired one.
func TestDelegationStore_GetBearerRemintsNearExpiry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		// Issue a token that expires almost immediately so the next
		// GetBearer call sees it as past the grace cliff.
		_, _ = w.Write([]byte(`{"access_token":"lmx_tok_secret","token_id":"tok","expires_in":1}`))
	}))
	t.Cleanup(srv.Close)

	store := NewDelegationStore(srv.URL, "worker-auth", "worker-1")
	store.LiveTab = staticLiveTab("tab-1", tabTypeAgent)
	store.MintGracePeriod = 5 * time.Second
	store.Acquire(userid.MustNew("u-1"))
	ctx := context.Background()

	_, err := store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("u-1"), AgentID: "agent-1"})
	require.NoError(t, err)
	_, err = store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("u-1"), AgentID: "agent-1"})
	require.NoError(t, err)
	assert.Equal(t, int32(2), calls.Load(), "expiry within grace window must trigger remint")
}

// TestDelegationStore_GetBearerRequiresIDs guards the contract that
// downstream callers depend on: an unminted user_id is rejected at the store
// layer, not silently passed to the hub as a bearer for nobody.
func TestDelegationStore_GetBearerRequiresIDs(t *testing.T) {
	store := NewDelegationStore("http://nowhere", "tok", "worker-1")
	store.LiveTab = staticLiveTab("tab-1", tabTypeAgent)
	_, err := store.GetBearer(context.Background(), DelegationScope{UserID: userid.UserID{}})
	require.Error(t, err)
	_, err = store.GetBearer(context.Background(), DelegationScope{UserID: userid.MustNew("u-1")})
	require.Error(t, err)
}

// stubMintRevokeServer returns an httptest.Server that mints / revokes
// using shared counters. The mint response carries a stable token id
// so tests can assert which token was revoked when multiple users are
// in play.
func stubMintRevokeServer(t *testing.T, mintCalls, revokeCalls *atomic.Int32, lastRevokedTokenID *atomic.Pointer[string]) *httptest.Server {
	t.Helper()
	return stubMintRevokeServerFull(t, mintCalls, revokeCalls, lastRevokedTokenID, nil)
}

func stubMintRevokeServerFull(t *testing.T, mintCalls, revokeCalls *atomic.Int32, lastRevokedTokenID *atomic.Pointer[string], onTabID func(string)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/worker/delegation-tokens/mint":
			n := mintCalls.Add(1)
			if onTabID != nil {
				var body struct {
					IssuedForTabID string `json:"issued_for_tab_id"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
					onTabID(body.IssuedForTabID)
				}
			}
			tokenID := "tok-" + string(rune('A'+n-1))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"lmx_` + tokenID + `_secret","token_id":"` + tokenID + `","expires_in":3600}`))
		case "/worker/delegation-tokens/revoke":
			revokeCalls.Add(1)
			if lastRevokedTokenID != nil {
				var body struct {
					TokenID string `json:"token_id"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err == nil && body.TokenID != "" {
					id := body.TokenID
					lastRevokedTokenID.Store(&id)
				}
			}
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestDelegationStore_AcquireReleaseRevokesOnLastRelease pins the
// refcount semantics: a single Acquire+GetBearer paired with a single
// Release must produce exactly one hub revoke. Without the refcount,
// Release wouldn't know whether other spawns still need the bearer.
func TestDelegationStore_AcquireReleaseRevokesOnLastRelease(t *testing.T) {
	var mints, revokes atomic.Int32
	srv := stubMintRevokeServer(t, &mints, &revokes, nil)
	store := NewDelegationStore(srv.URL, "worker-auth", "worker-1")
	store.LiveTab = staticLiveTab("tab-1", tabTypeAgent)
	ctx := context.Background()

	store.Acquire(userid.MustNew("u-1"))
	_, err := store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("u-1"), AgentID: "agent-1"})
	require.NoError(t, err)

	require.NoError(t, store.Release(ctx, userid.MustNew("u-1")))
	assert.Equal(t, int32(1), revokes.Load(), "single Acquire + single Release must revoke exactly once")

	// Subsequent Release for an unknown key is a no-op.
	require.NoError(t, store.Release(ctx, userid.MustNew("u-1")))
	assert.Equal(t, int32(1), revokes.Load(), "Release of an empty key must NOT call the hub again")
}

// TestDelegationStore_RefcountKeepsBearerAliveAcrossSpawns verifies
// the multi-agent case: two spawns share the same per-user
// delegation slot, and the bearer survives until the LAST one
// releases. Without refcounting, the first close would tear down a
// bearer the second agent still needs.
func TestDelegationStore_RefcountKeepsBearerAliveAcrossSpawns(t *testing.T) {
	var mints, revokes atomic.Int32
	srv := stubMintRevokeServer(t, &mints, &revokes, nil)
	store := NewDelegationStore(srv.URL, "worker-auth", "worker-1")
	store.LiveTab = staticLiveTab("tab-1", tabTypeAgent)
	ctx := context.Background()

	// Two spawns for the same user, each with its OWN tab -- which is what a
	// second agent actually looks like.
	store.Acquire(userid.MustNew("u-1"))
	store.Acquire(userid.MustNew("u-1"))
	_, err := store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("u-1"), AgentID: "agent-1"})
	require.NoError(t, err)

	// First release: no revoke yet.
	require.NoError(t, store.Release(ctx, userid.MustNew("u-1")))
	assert.Equal(t, int32(0), revokes.Load(), "Release with surviving refs must NOT revoke")

	// Bearer still cached and reusable.
	_, err = store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("u-1"), AgentID: "agent-1"})
	require.NoError(t, err)
	assert.Equal(t, int32(1), mints.Load(), "second GetBearer must hit cache, not re-mint")

	// Last release fires the revoke.
	require.NoError(t, store.Release(ctx, userid.MustNew("u-1")))
	assert.Equal(t, int32(1), revokes.Load(), "last Release must revoke")
}

// TestDelegationStore_ReleaseWithoutMintIsHubFree captures the lazy-
// mint contract: agents that never make hub-bound calls leave no
// cached bearer behind. Releasing the slot for such an agent must
// NOT post anything to the hub — there's nothing to revoke.
func TestDelegationStore_ReleaseWithoutMintIsHubFree(t *testing.T) {
	var mints, revokes atomic.Int32
	srv := stubMintRevokeServer(t, &mints, &revokes, nil)
	store := NewDelegationStore(srv.URL, "worker-auth", "worker-1")
	store.LiveTab = staticLiveTab("tab-1", tabTypeAgent)
	ctx := context.Background()

	store.Acquire(userid.MustNew("u-1"))
	require.NoError(t, store.Release(ctx, userid.MustNew("u-1")))
	assert.Equal(t, int32(0), mints.Load())
	assert.Equal(t, int32(0), revokes.Load())
}

// TestDelegationStore_ReacquireAfterReleaseMintsFresh confirms the
// post-release state: once the last spawn releases, subsequent
// Acquire+GetBearer for the same key starts from scratch with a new
// mint instead of reviving the just-revoked bearer.
func TestDelegationStore_ReacquireAfterReleaseMintsFresh(t *testing.T) {
	var mints, revokes atomic.Int32
	srv := stubMintRevokeServer(t, &mints, &revokes, nil)
	store := NewDelegationStore(srv.URL, "worker-auth", "worker-1")
	store.LiveTab = staticLiveTab("tab-1", tabTypeAgent)
	ctx := context.Background()

	store.Acquire(userid.MustNew("u-1"))
	_, err := store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("u-1"), AgentID: "agent-1"})
	require.NoError(t, err)
	require.NoError(t, store.Release(ctx, userid.MustNew("u-1")))

	store.Acquire(userid.MustNew("u-1"))
	_, err = store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("u-1"), AgentID: "agent-2"})
	require.NoError(t, err)
	require.NoError(t, store.Release(ctx, userid.MustNew("u-1")))

	assert.Equal(t, int32(2), mints.Load(), "second spawn must mint a fresh bearer")
	assert.Equal(t, int32(2), revokes.Load(), "each lifecycle must end in a revoke")
}

// TestDelegationStore_RefcountIsPerUser ensures Release under user-1 doesn't
// tear down a bearer cached for user-2. The cache key already gives this
// property, but the refcount table has to honour it too.
func TestDelegationStore_RefcountIsPerUser(t *testing.T) {
	var mints, revokes atomic.Int32
	srv := stubMintRevokeServer(t, &mints, &revokes, nil)
	store := NewDelegationStore(srv.URL, "worker-auth", "worker-1")
	store.LiveTab = staticLiveTab("tab-1", tabTypeAgent)
	ctx := context.Background()

	store.Acquire(userid.MustNew("u-1"))
	store.Acquire(userid.MustNew("u-2"))
	_, err := store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("u-1"), AgentID: "agent-A"})
	require.NoError(t, err)
	_, err = store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("u-2"), AgentID: "agent-B"})
	require.NoError(t, err)
	assert.Equal(t, int32(2), mints.Load(), "two users, two bearers")

	require.NoError(t, store.Release(ctx, userid.MustNew("u-1")))
	assert.Equal(t, int32(1), revokes.Load())

	// u-2's bearer survives — still cached, no extra mint on next call.
	_, err = store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("u-2"), AgentID: "agent-B"})
	require.NoError(t, err)
	assert.Equal(t, int32(2), mints.Load(), "u-2's bearer must remain cached after u-1's Release")

	require.NoError(t, store.Release(ctx, userid.MustNew("u-2")))
	assert.Equal(t, int32(2), revokes.Load())
}

// TestDelegationStore_ReleaseRejectsEmptyArgs prevents accidental
// blanket-revokes (a slot keyed on an unminted id would otherwise iterate the
// whole table once we add user-scoped revocation).
//
// The typed parameter makes a blank id unreachable from any caller holding a
// minted identity, so what stays testable is the residual zero value -- the one
// shape Go cannot forbid. Every entrypoint that takes a user is listed, so a
// new one added without the refusal shows up as a missing line here.
// (Revoke and Invalidate used to be listed too; both were deleted as having
// no production caller.)
func TestDelegationStore_ReleaseRejectsEmptyArgs(t *testing.T) {
	store := NewDelegationStore("http://nowhere", "tok", "worker-1")
	store.LiveTab = staticLiveTab("tab-1", tabTypeAgent)
	require.NoError(t, store.Release(context.Background(), userid.UserID{}))
	store.Acquire(userid.UserID{}) // must be silent no-op, not a panic

	// Nothing was recorded under a zero-id key: a later minted Acquire for the
	// same user must start from a clean slot, not inherit a refcount from the
	// refused calls above. (There is no tab bookkeeping left to check -- the mint's
	// provenance is read from the live inventory, so a refused Acquire has nothing
	// it could record.)
	store.mu.Lock()
	defer store.mu.Unlock()
	assert.Empty(t, store.refcount, "a refused Acquire must not create a refcount slot")
	assert.Empty(t, store.cached, "no refused call may leave a cached bearer")
}

// shortDelegationSocket builds a unix-socket path under os.TempDir()
// short enough to fit the platform's sun_path limit (~104 chars on
// macOS). t.TempDir() routinely produces directories that exceed it.
func shortDelegationSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(os.TempDir(), "lmx-deleg-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "hub.sock")
}

// TestDelegationStore_MintAndRevokeOverUnixSocket exercises the solo /
// hub-over-unix-socket path: when the worker's HubURL is `unix:<path>`
// the mint and revoke POSTs must reach the hub through a socket-aware
// transport instead of being handed to net/http with a literal `unix:`
// scheme (which fails with "unsupported protocol scheme \"unix\"").
//
// Regression coverage for `leapmux remote workspace list` from inside
// a remote-enabled terminal in solo mode.
func TestDelegationStore_MintAndRevokeOverUnixSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses unix sockets; npipe variant exercised via locallisten tests")
	}

	sockPath := shortDelegationSocket(t)
	ln, err := net.Listen("unix", sockPath)
	require.NoError(t, err)

	var mintCalls, revokeCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/worker/delegation-tokens/mint", func(w http.ResponseWriter, r *http.Request) {
		mintCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"lmx_tok_secret","token_id":"tok-7","expires_in":600}`))
	})
	mux.HandleFunc("/worker/delegation-tokens/revoke", func(w http.ResponseWriter, r *http.Request) {
		revokeCalls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Shutdown(context.Background())
		_ = ln.Close()
	})

	store := NewDelegationStore("unix:"+sockPath, "worker-auth", "worker-1")
	store.LiveTab = staticLiveTab("tab-1", tabTypeAgent)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)

	store.Acquire(userid.MustNew("user-1"))
	bearer, err := store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("user-1")})
	require.NoError(t, err)
	assert.Equal(t, "lmx_tok_secret", bearer)
	assert.Equal(t, int32(1), mintCalls.Load())

	require.NoError(t, store.Release(ctx, userid.MustNew("user-1")))
	assert.Equal(t, int32(1), revokeCalls.Load(),
		"revoke must reach the hub through the same socket-aware transport")
}

// TestDelegationStore_SweepExpired_DropsExpiredAndOrphaned pins down
// the defense-in-depth eviction pass: cached rows whose access token
// expired before the cutoff are dropped, but ONLY if no live spawn
// still references them (refcount == 0). Refcounted rows survive
// because the next GetBearer call will mint a fresh token through
// the existing slot — eviction would force a redundant Acquire round-
// trip on the very next call.
func TestDelegationStore_SweepExpired_DropsExpiredAndOrphaned(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"lmx_tok_secret","token_id":"tok","expires_in":1}`))
	}))
	t.Cleanup(srv.Close)

	store := NewDelegationStore(srv.URL, "worker-auth", "worker-1")
	store.LiveTab = staticLiveTab("tab-1", tabTypeAgent)
	store.Acquire(userid.MustNew("user-1"))
	store.Acquire(userid.MustNew("user-2"))

	// Mint both bearers so they land in the cache.
	_, err := store.GetBearer(context.Background(), DelegationScope{UserID: userid.MustNew("user-1"), AgentID: "agent-1"})
	require.NoError(t, err)
	_, err = store.GetBearer(context.Background(), DelegationScope{UserID: userid.MustNew("user-2"), AgentID: "agent-2"})
	require.NoError(t, err)

	// Drop user-2's refcount so its row is now orphaned (refcount 0),
	// while user-1's refcount stays at 1 — it's still actively used.
	require.NoError(t, store.Release(context.Background(), userid.MustNew("user-2")))

	// At this point user-2's slot is already deleted by Release. To
	// exercise SweepExpired's "orphaned + expired" combination, simulate
	// an orphan that Release didn't reach: re-Acquire then mutate
	// refcount manually under the store mutex.
	store.Acquire(userid.MustNew("user-2"))
	_, err = store.GetBearer(context.Background(), DelegationScope{UserID: userid.MustNew("user-2"), AgentID: "agent-2"})
	require.NoError(t, err)
	store.mu.Lock()
	store.refcount["user-2"] = 0
	store.mu.Unlock()

	// Wait long enough that the 1-second expires_in has passed.
	time.Sleep(1100 * time.Millisecond)

	dropped := store.SweepExpired(time.Now())
	assert.Equal(t, 1, dropped, "exactly the orphaned + expired row should be reaped")

	store.mu.Lock()
	_, hasOrphaned := store.cached["user-2"]
	_, hasRefcounted := store.cached["user-1"]
	store.mu.Unlock()
	assert.False(t, hasOrphaned, "orphaned + expired row must be evicted")
	assert.True(t, hasRefcounted, "refcounted row must be preserved even when expired")
}

// TestDelegationStore_ConcurrentFirstMintCollapsesToOne pins the singleflight on
// GetBearer.
//
// Two callers that miss the cache together used to each POST the mint endpoint
// and each receive a DISTINCT token_id. Only one survives in `cached`, so the
// other is never passed to revokeTokenID by Release -- a live delegation
// credential with nothing able to revoke it until its TTL expires.
//
// The race became reachable when the cache key collapsed from (user, workspace)
// to user alone: two spawns in different workspaces on one worker used to hold
// separate slots and mint separately by design, and now share one.
func TestDelegationStore_ConcurrentFirstMintCollapsesToOne(t *testing.T) {
	var mints atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := mints.Add(1)
		// Hold the first mint open so a second caller is guaranteed to arrive
		// while it is still in flight -- the exact window the singleflight closes.
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w,
			`{"access_token":"lmx_tok_%d","token_id":"tok-%d","expires_in":3600}`, n, n)
	}))
	t.Cleanup(srv.Close)

	store := NewDelegationStore(srv.URL, "worker-auth", "worker-1")
	store.LiveTab = staticLiveTab("tab-1", tabTypeAgent)
	user := userid.MustNew("u-1")
	store.Acquire(user)
	store.Acquire(user)

	const callers = 2
	var wg sync.WaitGroup
	bearers := make([]string, callers)
	errs := make([]error, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, err := store.GetBearer(context.Background(),
				DelegationScope{UserID: user, AgentID: fmt.Sprintf("agent-%d", i)})
			bearers[i], errs[i] = b, err
		}()
	}
	// Both are past the cache miss and one holds the flight by the time the
	// handler is unblocked.
	require.Eventually(t, func() bool { return mints.Load() >= 1 }, 2*time.Second, 5*time.Millisecond)
	close(release)
	wg.Wait()

	for i := range callers {
		require.NoError(t, errs[i])
	}
	assert.Equal(t, int32(1), mints.Load(),
		"concurrent first-time callers for one slot must mint ONE token; a second mint leaves an unrevocable credential live until TTL")
	assert.Equal(t, bearers[0], bearers[1], "both callers must receive the same bearer")
}

// staticLiveTab is a LiveTabProvider that always names one tab, standing in for
// the worker's open-tab tables.
func staticLiveTab(tabID string, tabType int32) LiveTabProvider {
	return func() (string, int32, bool) { return tabID, tabType, true }
}

// TestDelegationStore_MintReadsProvenanceFromTheLiveInventory pins that the mint's
// issued_for_tab_id comes from the worker's own open-tab tables, not from
// Acquire/Release bookkeeping.
//
// This is what makes a missed Release harmless. The store used to keep a shadow set
// of "tabs this worker hosts", maintained by Acquire/Release; a Release that never
// ran -- a panic in a cleanup, or a close path added later that bypasses the
// registry -- left a dead tab id in it, the hub answered 403 "tab not owned by
// calling worker", and nothing pruned it because a 403 is also what a
// not-yet-propagated tab looks like. Every later mint for that user failed.
func TestDelegationStore_MintReadsProvenanceFromTheLiveInventory(t *testing.T) {
	var gotTabID atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotTabID.Store(body["issued_for_tab_id"])
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"lmx_tok","token_id":"tok","expires_in":3600}`))
	}))
	t.Cleanup(srv.Close)

	store := NewDelegationStore(srv.URL, "worker-auth", "worker-1")
	store.LiveTab = staticLiveTab("tab-1", tabTypeAgent)
	user := userid.MustNew("u-1")
	// The inventory says "term-live". No Acquire has named any tab.
	store.LiveTab = staticLiveTab("term-live", tabTypeTerminal)
	store.Acquire(user)

	_, err := store.GetBearer(context.Background(), DelegationScope{UserID: user, TerminalID: "term-live"})
	require.NoError(t, err)
	assert.Equal(t, "term-live", gotTabID.Load(),
		"the mint must take its provenance tab from the live inventory")
}

// TestDelegationStore_MissedReleaseCannotPoisonLaterMints is the property the
// derivation buys: a spawn that never releases leaves nothing behind for the next
// mint to trip over, because there is no shadow set to go stale.
func TestDelegationStore_MissedReleaseCannotPoisonLaterMints(t *testing.T) {
	var mints atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mints.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"lmx_tok","token_id":"tok","expires_in":3600}`))
	}))
	t.Cleanup(srv.Close)

	store := NewDelegationStore(srv.URL, "worker-auth", "worker-1")
	store.LiveTab = staticLiveTab("tab-1", tabTypeAgent)
	user := userid.MustNew("u-1")
	live := "agent-survivor"
	store.LiveTab = func() (string, int32, bool) { return live, tabTypeAgent, true }

	// Two spawns; the first is closed WITHOUT its Release ever running.
	store.Acquire(user)
	store.Acquire(user)
	// (no Release for the first spawn -- that is the whole point)

	// Force a fresh mint, as a near-expiry refresh would.
	store.MintGracePeriod = 2 * time.Hour
	_, err := store.GetBearer(context.Background(), DelegationScope{UserID: user, AgentID: "agent-survivor"})
	require.NoError(t, err, "a missed Release must not break the surviving spawn's mint")
	assert.Equal(t, int32(1), mints.Load())
}

// TestDelegationStore_MintWithoutAnyLiveTabFailsLoudly keeps the fail-closed half:
// with nothing hosted there is no provenance to claim, and the mint must say so
// rather than send a blank tab id the hub would refuse opaquely.
func TestDelegationStore_MintWithoutAnyLiveTabFailsLoudly(t *testing.T) {
	store := NewDelegationStore("http://unused", "worker-auth", "worker-1")
	store.LiveTab = staticLiveTab("tab-1", tabTypeAgent)
	user := userid.MustNew("u-1")
	store.LiveTab = func() (string, int32, bool) { return "", 0, false }
	store.Acquire(user)

	_, err := store.GetBearer(context.Background(), DelegationScope{UserID: user, AgentID: "a-1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hosts no open tab")
}
