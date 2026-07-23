package crossworker

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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
	store.MintRetryBackoff = 5 * time.Millisecond
	store.MintMaxAttempts = 6
	store.Acquire(userid.MustNew("user-1"), "ws-1", "tab-1", tabTypeAgent)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)

	bearer, err := store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("user-1"), WorkspaceID: "ws-1", AgentID: "agent-1"})
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
	store.MintRetryBackoff = 1 * time.Millisecond
	store.MintMaxAttempts = 4
	store.Acquire(userid.MustNew("user-1"), "ws-1", "tab-1", tabTypeAgent)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)

	_, err := store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("user-1"), WorkspaceID: "ws-1", AgentID: "agent-1"})
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
	store.MintRetryBackoff = 1 * time.Millisecond
	store.MintMaxAttempts = 6
	store.Acquire(userid.MustNew("user-1"), "ws-1", "tab-1", tabTypeAgent)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)

	_, err := store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("user-1"), WorkspaceID: "ws-1", AgentID: "agent-1"})
	require.Error(t, err)
	assert.Equal(t, int32(1), calls.Load(), "non-propagation errors must not retry")
}

// TestDelegationStore_GetBearerCachesPerWorkspace verifies the cache
// is keyed by (user, workspace): the second call for the same key
// hits cache, and a different workspace forces a fresh mint.
func TestDelegationStore_GetBearerCachesPerWorkspace(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"lmx_tok_secret","token_id":"tok","expires_in":3600}`))
	}))
	t.Cleanup(srv.Close)

	store := NewDelegationStore(srv.URL, "worker-auth", "worker-1")
	store.Acquire(userid.MustNew("u-1"), "ws-1", "tab-1", tabTypeAgent)
	store.Acquire(userid.MustNew("u-1"), "ws-2", "tab-2", tabTypeAgent)
	ctx := context.Background()

	_, err := store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("u-1"), WorkspaceID: "ws-1", AgentID: "agent-1"})
	require.NoError(t, err)
	_, err = store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("u-1"), WorkspaceID: "ws-1", AgentID: "agent-1"})
	require.NoError(t, err)
	assert.Equal(t, int32(1), calls.Load(), "second call for same (u,w) must hit cache")

	_, err = store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("u-1"), WorkspaceID: "ws-2", AgentID: "agent-1"})
	require.NoError(t, err)
	assert.Equal(t, int32(2), calls.Load(), "different workspace must mint a fresh delegation")
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
	store.MintGracePeriod = 5 * time.Second
	store.Acquire(userid.MustNew("u-1"), "ws-1", "tab-1", tabTypeAgent)
	ctx := context.Background()

	_, err := store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("u-1"), WorkspaceID: "ws-1", AgentID: "agent-1"})
	require.NoError(t, err)
	_, err = store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("u-1"), WorkspaceID: "ws-1", AgentID: "agent-1"})
	require.NoError(t, err)
	assert.Equal(t, int32(2), calls.Load(), "expiry within grace window must trigger remint")
}

// TestDelegationStore_RevokeIsIdempotent exercises the worker-side
// revoke path: after Revoke, the cache entry is gone (so subsequent
// GetBearer calls re-mint), and a redundant Revoke against an
// already-revoked token is harmless.
func TestDelegationStore_RevokeIsIdempotent(t *testing.T) {
	var mintCalls, revokeCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/worker/delegation-tokens/mint":
			mintCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"lmx_tok_secret","token_id":"tok","expires_in":3600}`))
		case "/worker/delegation-tokens/revoke":
			revokeCalls.Add(1)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(srv.Close)

	store := NewDelegationStore(srv.URL, "worker-auth", "worker-1")
	store.Acquire(userid.MustNew("u-1"), "ws-1", "tab-1", tabTypeAgent)
	ctx := context.Background()

	_, err := store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("u-1"), WorkspaceID: "ws-1", AgentID: "agent-1"})
	require.NoError(t, err)

	require.NoError(t, store.Revoke(ctx, userid.MustNew("u-1"), "ws-1"))
	require.NoError(t, store.Revoke(ctx, userid.MustNew("u-1"), "ws-1")) // second call: cache empty, no-op
	assert.Equal(t, int32(1), revokeCalls.Load(), "second Revoke for an empty cache entry must be a no-op")

	// After revoke the cache is empty, so GetBearer mints again.
	_, err = store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("u-1"), WorkspaceID: "ws-1", AgentID: "agent-1"})
	require.NoError(t, err)
	assert.Equal(t, int32(2), mintCalls.Load())
}

// TestDelegationStore_GetBearerRequiresIDs guards the contract that
// downstream callers depend on: empty user_id or workspace_id is
// rejected at the store layer, not silently passed to the hub.
func TestDelegationStore_GetBearerRequiresIDs(t *testing.T) {
	store := NewDelegationStore("http://nowhere", "tok", "worker-1")
	_, err := store.GetBearer(context.Background(), DelegationScope{UserID: userid.UserID{}, WorkspaceID: "ws-1"})
	require.Error(t, err)
	_, err = store.GetBearer(context.Background(), DelegationScope{UserID: userid.MustNew("u-1"), WorkspaceID: ""})
	require.Error(t, err)
}

// stubMintRevokeServer returns an httptest.Server that mints / revokes
// using shared counters. The mint response carries a stable token id
// so tests can assert which token was revoked when multiple
// (user, workspace) pairs are in play.
func stubMintRevokeServer(t *testing.T, mintCalls, revokeCalls *atomic.Int32, lastRevokedTokenID *atomic.Pointer[string]) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/worker/delegation-tokens/mint":
			n := mintCalls.Add(1)
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
	ctx := context.Background()

	store.Acquire(userid.MustNew("u-1"), "ws-1", "tab-1", tabTypeAgent)
	_, err := store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("u-1"), WorkspaceID: "ws-1", AgentID: "agent-1"})
	require.NoError(t, err)

	require.NoError(t, store.Release(ctx, userid.MustNew("u-1"), "ws-1"))
	assert.Equal(t, int32(1), revokes.Load(), "single Acquire + single Release must revoke exactly once")

	// Subsequent Release for an unknown key is a no-op.
	require.NoError(t, store.Release(ctx, userid.MustNew("u-1"), "ws-1"))
	assert.Equal(t, int32(1), revokes.Load(), "Release of an empty key must NOT call the hub again")
}

// TestDelegationStore_RefcountKeepsBearerAliveAcrossSpawns verifies
// the multi-agent case: two spawns share the same (user, workspace)
// delegation slot, and the bearer survives until the LAST one
// releases. Without refcounting, the first close would tear down a
// bearer the second agent still needs.
func TestDelegationStore_RefcountKeepsBearerAliveAcrossSpawns(t *testing.T) {
	var mints, revokes atomic.Int32
	srv := stubMintRevokeServer(t, &mints, &revokes, nil)
	store := NewDelegationStore(srv.URL, "worker-auth", "worker-1")
	ctx := context.Background()

	// Two spawns for the same user+workspace.
	store.Acquire(userid.MustNew("u-1"), "ws-1", "tab-1", tabTypeAgent)
	store.Acquire(userid.MustNew("u-1"), "ws-1", "tab-2", tabTypeAgent)
	_, err := store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("u-1"), WorkspaceID: "ws-1", AgentID: "agent-1"})
	require.NoError(t, err)

	// First release: no revoke yet.
	require.NoError(t, store.Release(ctx, userid.MustNew("u-1"), "ws-1"))
	assert.Equal(t, int32(0), revokes.Load(), "Release with surviving refs must NOT revoke")

	// Bearer still cached and reusable.
	_, err = store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("u-1"), WorkspaceID: "ws-1", AgentID: "agent-1"})
	require.NoError(t, err)
	assert.Equal(t, int32(1), mints.Load(), "second GetBearer must hit cache, not re-mint")

	// Last release fires the revoke.
	require.NoError(t, store.Release(ctx, userid.MustNew("u-1"), "ws-1"))
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
	ctx := context.Background()

	store.Acquire(userid.MustNew("u-1"), "ws-1", "tab-1", tabTypeAgent)
	require.NoError(t, store.Release(ctx, userid.MustNew("u-1"), "ws-1"))
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
	ctx := context.Background()

	store.Acquire(userid.MustNew("u-1"), "ws-1", "tab-1", tabTypeAgent)
	_, err := store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("u-1"), WorkspaceID: "ws-1", AgentID: "agent-1"})
	require.NoError(t, err)
	require.NoError(t, store.Release(ctx, userid.MustNew("u-1"), "ws-1"))

	store.Acquire(userid.MustNew("u-1"), "ws-1", "tab-2", tabTypeAgent)
	_, err = store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("u-1"), WorkspaceID: "ws-1", AgentID: "agent-2"})
	require.NoError(t, err)
	require.NoError(t, store.Release(ctx, userid.MustNew("u-1"), "ws-1"))

	assert.Equal(t, int32(2), mints.Load(), "second spawn must mint a fresh bearer")
	assert.Equal(t, int32(2), revokes.Load(), "each lifecycle must end in a revoke")
}

// TestDelegationStore_InvalidateDropsCacheWithoutHubCall locks down the
// 401-handling contract: workers that observe a hub-side revocation
// (e.g. the delegation row was wiped by `RevokeByUser`) need a way to
// drop the cached bearer without round-tripping. Invalidate is that
// hook; subsequent GetBearer mints fresh.
func TestDelegationStore_InvalidateDropsCacheWithoutHubCall(t *testing.T) {
	var mints, revokes atomic.Int32
	srv := stubMintRevokeServer(t, &mints, &revokes, nil)
	store := NewDelegationStore(srv.URL, "worker-auth", "worker-1")
	store.Acquire(userid.MustNew("u-1"), "ws-1", "tab-1", tabTypeAgent)
	ctx := context.Background()

	_, err := store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("u-1"), WorkspaceID: "ws-1", AgentID: "agent-1"})
	require.NoError(t, err)

	store.Invalidate(userid.MustNew("u-1"), "ws-1")
	assert.Equal(t, int32(0), revokes.Load(), "Invalidate must NOT post to /revoke")

	_, err = store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("u-1"), WorkspaceID: "ws-1", AgentID: "agent-1"})
	require.NoError(t, err)
	assert.Equal(t, int32(2), mints.Load(), "post-Invalidate GetBearer must re-mint")
}

// TestDelegationStore_RefcountIsPerWorkspace ensures Release under
// (user, ws-1) doesn't tear down a bearer cached for (user, ws-2).
// The cache key already gives this property, but the refcount table
// has to honour it too.
func TestDelegationStore_RefcountIsPerWorkspace(t *testing.T) {
	var mints, revokes atomic.Int32
	srv := stubMintRevokeServer(t, &mints, &revokes, nil)
	store := NewDelegationStore(srv.URL, "worker-auth", "worker-1")
	ctx := context.Background()

	store.Acquire(userid.MustNew("u-1"), "ws-1", "tab-1", tabTypeAgent)
	store.Acquire(userid.MustNew("u-1"), "ws-2", "tab-2", tabTypeAgent)
	_, err := store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("u-1"), WorkspaceID: "ws-1", AgentID: "agent-A"})
	require.NoError(t, err)
	_, err = store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("u-1"), WorkspaceID: "ws-2", AgentID: "agent-B"})
	require.NoError(t, err)

	require.NoError(t, store.Release(ctx, userid.MustNew("u-1"), "ws-1"))
	assert.Equal(t, int32(1), revokes.Load())

	// ws-2 bearer survives — still cached, no extra mint on next call.
	_, err = store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("u-1"), WorkspaceID: "ws-2", AgentID: "agent-B"})
	require.NoError(t, err)
	assert.Equal(t, int32(2), mints.Load(), "ws-2 bearer must remain cached after ws-1 Release")

	require.NoError(t, store.Release(ctx, userid.MustNew("u-1"), "ws-2"))
	assert.Equal(t, int32(2), revokes.Load())
}

// TestDelegationStore_ReleaseRejectsEmptyArgs prevents accidental
// blanket-revokes (a slot keyed on an unminted id would otherwise iterate the
// whole table once we add user-scoped revocation).
//
// The typed parameter makes a blank id unreachable from any caller holding a
// minted identity, so what stays testable is the residual zero value -- the one
// shape Go cannot forbid. Every entrypoint that takes a (user, workspace) pair
// is listed, so a new one added without the refusal shows up as a missing line
// here.
func TestDelegationStore_ReleaseRejectsEmptyArgs(t *testing.T) {
	store := NewDelegationStore("http://nowhere", "tok", "worker-1")
	require.NoError(t, store.Release(context.Background(), userid.UserID{}, "ws-1"))
	require.NoError(t, store.Release(context.Background(), userid.MustNew("u-1"), ""))
	require.NoError(t, store.Revoke(context.Background(), userid.UserID{}, "ws-1"))
	require.NoError(t, store.Revoke(context.Background(), userid.MustNew("u-1"), ""))
	store.Invalidate(userid.UserID{}, "ws-1")
	store.Invalidate(userid.MustNew("u-1"), "")
	store.Acquire(userid.UserID{}, "ws-1", "tab-1", tabTypeAgent) // must be silent no-op, not a panic
	store.Acquire(userid.MustNew("u-1"), "", "tab-1", tabTypeAgent)

	// Nothing was recorded under a zero-id key: a later minted Acquire for the
	// same workspace must start from a clean slot, not inherit refcount or tab
	// provenance from the refused calls above.
	store.mu.Lock()
	defer store.mu.Unlock()
	assert.Empty(t, store.refcount, "a refused Acquire must not create a refcount slot")
	assert.Empty(t, store.tabs, "a refused Acquire must not record tab provenance")
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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)

	store.Acquire(userid.MustNew("user-1"), "ws-1", "tab-7", tabTypeAgent)
	bearer, err := store.GetBearer(ctx, DelegationScope{UserID: userid.MustNew("user-1"), WorkspaceID: "ws-1"})
	require.NoError(t, err)
	assert.Equal(t, "lmx_tok_secret", bearer)
	assert.Equal(t, int32(1), mintCalls.Load())

	require.NoError(t, store.Release(ctx, userid.MustNew("user-1"), "ws-1"))
	assert.Equal(t, int32(1), revokeCalls.Load(),
		"revoke must reach the hub through the same socket-aware transport")
}

// TestDelegationStore_MintCarriesAcquireTabIdentity locks in the
// Acquire→mint provenance contract: the (tabID, tabType) registered
// at spawn time must reach the hub as `issued_for_tab_id` /
// `issued_for_tab_type`. The hub rejects mint with HTTP 400 when
// `issued_for_tab_id` is empty and then validates the calling worker
// owns the (workspace_id, tab_id) row in workspace_tabs, so this
// field MUST round-trip from Acquire to the POST body. Skipping the
// plumbing (the previous behaviour: shared store with empty
// IssuedForTabID) surfaced as "mint failed (400): user_id,
// workspace_id, issued_for_tab_id are required" in solo mode the
// moment a remote-enabled terminal ran a hub-bound CLI command.
func TestDelegationStore_MintCarriesAcquireTabIdentity(t *testing.T) {
	type mintBody struct {
		UserID           string `json:"user_id"`
		WorkspaceID      string `json:"workspace_id"`
		IssuedForTabID   string `json:"issued_for_tab_id"`
		IssuedForTabType int32  `json:"issued_for_tab_type"`
	}
	var seen mintBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&seen))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"lmx_tok_secret","token_id":"tok","expires_in":600}`))
	}))
	t.Cleanup(srv.Close)

	store := NewDelegationStore(srv.URL, "worker-auth", "worker-1")
	const tabTypeTerminal = int32(leapmuxv1.TabType_TAB_TYPE_TERMINAL)
	store.Acquire(userid.MustNew("user-1"), "ws-1", "term-42", tabTypeTerminal)

	_, err := store.GetBearer(context.Background(), DelegationScope{UserID: userid.MustNew("user-1"), WorkspaceID: "ws-1", TerminalID: "term-42"})
	require.NoError(t, err)

	assert.Equal(t, "user-1", seen.UserID)
	assert.Equal(t, "ws-1", seen.WorkspaceID)
	assert.Equal(t, "term-42", seen.IssuedForTabID,
		"mint payload must carry the tab_id registered by Acquire")
	assert.Equal(t, tabTypeTerminal, seen.IssuedForTabType,
		"mint payload must carry the tab_type registered by Acquire")
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
	store.Acquire(userid.MustNew("user-1"), "ws-1", "tab-1", tabTypeAgent)
	store.Acquire(userid.MustNew("user-2"), "ws-2", "tab-2", tabTypeAgent)

	// Mint both bearers so they land in the cache.
	_, err := store.GetBearer(context.Background(), DelegationScope{UserID: userid.MustNew("user-1"), WorkspaceID: "ws-1", AgentID: "agent-1"})
	require.NoError(t, err)
	_, err = store.GetBearer(context.Background(), DelegationScope{UserID: userid.MustNew("user-2"), WorkspaceID: "ws-2", AgentID: "agent-2"})
	require.NoError(t, err)

	// Drop user-2's refcount so its row is now orphaned (refcount 0),
	// while user-1's refcount stays at 1 — it's still actively used.
	require.NoError(t, store.Release(context.Background(), userid.MustNew("user-2"), "ws-2"))

	// At this point user-2's slot is already deleted by Release. To
	// exercise SweepExpired's "orphaned + expired" combination, simulate
	// an orphan that Release didn't reach: re-Acquire then mutate
	// refcount manually under the store mutex.
	store.Acquire(userid.MustNew("user-2"), "ws-2", "tab-2", tabTypeAgent)
	_, err = store.GetBearer(context.Background(), DelegationScope{UserID: userid.MustNew("user-2"), WorkspaceID: "ws-2", AgentID: "agent-2"})
	require.NoError(t, err)
	store.mu.Lock()
	store.refcount["user-2|ws-2"] = 0
	store.mu.Unlock()

	// Wait long enough that the 1-second expires_in has passed.
	time.Sleep(1100 * time.Millisecond)

	dropped := store.SweepExpired(time.Now())
	assert.Equal(t, 1, dropped, "exactly the orphaned + expired row should be reaped")

	store.mu.Lock()
	_, hasOrphaned := store.cached["user-2|ws-2"]
	_, hasRefcounted := store.cached["user-1|ws-1"]
	store.mu.Unlock()
	assert.False(t, hasOrphaned, "orphaned + expired row must be evicted")
	assert.True(t, hasRefcounted, "refcounted row must be preserved even when expired")
}

// TestDelegationStore_MintWithoutAcquireFailsLoudly guards against
// silent regressions to the old behaviour where mint POSTed an empty
// `issued_for_tab_id`. Without prior Acquire, mint must refuse to
// hit the hub at all — surfacing a clear local error instead of an
// opaque hub-side 400.
func TestDelegationStore_MintWithoutAcquireFailsLoudly(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	t.Cleanup(srv.Close)

	store := NewDelegationStore(srv.URL, "worker-auth", "worker-1")
	_, err := store.GetBearer(context.Background(), DelegationScope{UserID: userid.MustNew("user-1"), WorkspaceID: "ws-1", AgentID: "agent-1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no tab registered")
	assert.Equal(t, int32(0), calls.Load(),
		"mint must short-circuit locally when no tab identity is registered")
}
