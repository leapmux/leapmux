package revocationwatcher_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/revocationwatcher"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
)

// fakeCloser records every CloseChannelsBy* call so tests can
// assert the watcher dispatched the right teardown without
// standing up the full ChannelService.
type fakeCloser struct {
	mu           sync.Mutex
	bearerClosed []string
	userClosed   []string
}

func (c *fakeCloser) CloseChannelsByBearer(tokenID string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bearerClosed = append(c.bearerClosed, tokenID)
	return 1
}

func (c *fakeCloser) CloseChannelsByUser(userID string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.userClosed = append(c.userClosed, userID)
	return 1
}

func (c *fakeCloser) bearerSnapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.bearerClosed...)
}

func (c *fakeCloser) userSnapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.userClosed...)
}

// envT bundles the seed objects every test uses: a hub-side store,
// a session cache, a fake closer, and the user / worker / workspace
// rows that delegation tokens reference for FK integrity.
type envT struct {
	st       store.Store
	cache    *auth.SessionCache
	closer   *fakeCloser
	watcher  *revocationwatcher.Watcher
	userID   string
	workerID string
	wsID     string
	tabID    string
}

func setup(t *testing.T) *envT {
	t.Helper()
	st := hubtestutil.OpenTestStore(t)
	hubtestutil.CreateTestAdmin(t, st)
	_, sc := auth.NewInterceptor(st, nil, false, false)
	t.Cleanup(sc.Stop)

	closer := &fakeCloser{}
	w := revocationwatcher.New(st, sc, closer)
	// Trim sweep interval so periodic-loop tests run quickly. Tests
	// that drive RunOnce directly don't depend on this.
	w.Interval = 50 * time.Millisecond

	u, err := st.Users().GetByUsername(context.Background(), "admin")
	require.NoError(t, err)

	workerID := id.Generate()
	require.NoError(t, st.Workers().Create(context.Background(), store.CreateWorkerParams{
		ID:              workerID,
		AuthToken:       id.Generate(),
		RegisteredBy:    u.ID,
		PublicKey:       []byte("test-x25519-key-32-bytes-padding"),
		MlkemPublicKey:  []byte("mlkem"),
		SlhdsaPublicKey: []byte("slhdsa"),
	}))

	wsID := id.Generate()
	require.NoError(t, st.Workspaces().Create(context.Background(), store.CreateWorkspaceParams{
		ID: wsID, OrgID: u.OrgID, OwnerUserID: u.ID, Title: "ws",
	}))
	tabID := id.Generate()
	require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(context.Background(), store.UpsertOwnedTabParams{
		OrgID: u.OrgID, WorkspaceID: wsID, WorkerID: workerID,
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabID:   tabID, Position: "a", TileID: "tile-1",
	}))

	return &envT{
		st: st, cache: sc, closer: closer, watcher: w,
		userID: u.ID, workerID: workerID, wsID: wsID, tabID: tabID,
	}
}

// seedAPIToken inserts an api_tokens row so tests have a real PK
// to revoke.
func (e *envT) seedAPIToken(t *testing.T) string {
	t.Helper()
	tokenID := id.Generate()
	require.NoError(t, e.st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:         tokenID,
		UserID:     e.userID,
		ClientType: "cli",
		ClientName: "test",
		SecretHash: []byte("hash"),
		Scope:      "remote:*",
	}))
	return tokenID
}

func (e *envT) seedDelegationToken(t *testing.T) string {
	t.Helper()
	tokenID := id.Generate()
	require.NoError(t, e.st.DelegationTokens().Create(context.Background(), store.CreateDelegationTokenParams{
		ID:               tokenID,
		UserID:           e.userID,
		WorkerID:         e.workerID,
		WorkspaceID:      e.wsID,
		IssuedForTabID:   e.tabID,
		IssuedForTabType: int32(leapmuxv1.TabType_TAB_TYPE_AGENT),
		SecretHash:       []byte("hash"),
		ExpiresAt:        time.Now().Add(time.Hour),
	}))
	return tokenID
}

// TestWatcher_FiresCloseByBearerForRevokedAPIToken pins the
// admin-flow contract: a row revoked from outside the hub process
// (e.g. `leapmux admin api-token revoke`) lands in the watcher's
// next sweep, evicts the bearer cache, and closes every open
// channel that token authorized.
func TestWatcher_FiresCloseByBearerForRevokedAPIToken(t *testing.T) {
	env := setup(t)
	tokenID := env.seedAPIToken(t)

	// Revoke the row simulating the admin CLI mutation.
	_, err := env.st.APITokens().Revoke(context.Background(), tokenID)
	require.NoError(t, err)

	env.watcher.RunOnce(context.Background())

	assert.Equal(t, []string{tokenID}, env.closer.bearerSnapshot(), "watcher must close channels for the revoked api_token")
	assert.Empty(t, env.closer.userSnapshot(), "user-wide close should not fire for a single-token revoke")
}

// TestWatcher_FiresCloseByBearerForRevokedDelegationToken mirrors
// the api-tokens flow for delegation_tokens. Both tables share the
// same fan-out pattern; the test pins that the watcher actually
// queries both.
func TestWatcher_FiresCloseByBearerForRevokedDelegationToken(t *testing.T) {
	env := setup(t)
	tokenID := env.seedDelegationToken(t)

	_, err := env.st.DelegationTokens().Revoke(context.Background(), tokenID)
	require.NoError(t, err)

	env.watcher.RunOnce(context.Background())
	assert.Equal(t, []string{tokenID}, env.closer.bearerSnapshot())
	assert.Empty(t, env.closer.userSnapshot())
}

// TestWatcher_FiresCloseByUserOnTokensRevokedAtBump pins the
// user-wide path. Admin commands bump `users.tokens_revoked_at`
// alongside row-level revokes; the watcher picks up the bump and
// fires CloseChannelsByUser so cookie channels (which carry no
// bearer token id) also die in lock-step.
func TestWatcher_FiresCloseByUserOnTokensRevokedAtBump(t *testing.T) {
	env := setup(t)

	_, err := env.st.Users().BumpTokensRevokedAt(context.Background(), env.userID)
	require.NoError(t, err)

	env.watcher.RunOnce(context.Background())
	assert.Equal(t, []string{env.userID}, env.closer.userSnapshot())
}

// TestWatcher_HighWaterMarkPreventsRereadingRevokedRows verifies
// the watcher only acts on rows it hasn't seen yet. Without an
// HWM, every sweep would re-close the same channels and re-evict
// the same cache entries, producing log spam and racing with
// concurrent re-opens.
func TestWatcher_HighWaterMarkPreventsRereadingRevokedRows(t *testing.T) {
	env := setup(t)
	tokenID := env.seedAPIToken(t)

	_, err := env.st.APITokens().Revoke(context.Background(), tokenID)
	require.NoError(t, err)

	env.watcher.RunOnce(context.Background())
	require.Equal(t, []string{tokenID}, env.closer.bearerSnapshot())

	// Second sweep — no new revocations → no new closes.
	env.watcher.RunOnce(context.Background())
	assert.Equal(t, []string{tokenID}, env.closer.bearerSnapshot(),
		"a row revoked before the HWM must not be re-emitted on subsequent sweeps")
}

// TestWatcher_HighWaterMarkAdvancesAcrossMultipleRevocations
// verifies that a fresh revocation that lands AFTER the watcher
// has already processed earlier revocations is still picked up.
// The HWM must move forward, not stick at the first sweep's max.
func TestWatcher_HighWaterMarkAdvancesAcrossMultipleRevocations(t *testing.T) {
	env := setup(t)
	first := env.seedAPIToken(t)

	_, err := env.st.APITokens().Revoke(context.Background(), first)
	require.NoError(t, err)
	env.watcher.RunOnce(context.Background())
	require.Equal(t, []string{first}, env.closer.bearerSnapshot())

	// Sleep past the storage layer's timestamp resolution so the
	// second revoke's revoked_at is strictly greater than the
	// first's. SQLite's strftime gives ms precision; 5ms is
	// enough headroom.
	time.Sleep(5 * time.Millisecond)

	second := env.seedAPIToken(t)
	_, err = env.st.APITokens().Revoke(context.Background(), second)
	require.NoError(t, err)
	env.watcher.RunOnce(context.Background())

	got := env.closer.bearerSnapshot()
	assert.Equal(t, []string{first, second}, got, "second sweep must observe the second revoke even after the first")
}

// TestWatcher_StartLoopFiresOnSchedule exercises the goroutine
// path: a fresh revocation that lands while the watcher is
// running should be picked up within a poll interval, without the
// test driving RunOnce manually.
func TestWatcher_StartLoopFiresOnSchedule(t *testing.T) {
	env := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	env.watcher.StartLoop(ctx)

	tokenID := env.seedAPIToken(t)
	_, err := env.st.APITokens().Revoke(context.Background(), tokenID)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		s := env.closer.bearerSnapshot()
		return len(s) == 1 && s[0] == tokenID
	}, 2*time.Second, 25*time.Millisecond, "watcher loop should pick up the revoke within a few sweeps")
}

// TestWatcher_NoOpsOnEmptyTables exercises the steady state: a
// hub with no recent revocations must not emit any closes or
// errors. The first call after start() runs against the now()
// HWM so it sees nothing.
func TestWatcher_NoOpsOnEmptyTables(t *testing.T) {
	env := setup(t)
	env.watcher.RunOnce(context.Background())
	assert.Empty(t, env.closer.bearerSnapshot())
	assert.Empty(t, env.closer.userSnapshot())
}

// TestWatcher_TolerantOfNilCacheAndCloser captures the
// dependency-optional contract: tests / minimal configurations
// that wire one half can still construct a useful watcher.
func TestWatcher_TolerantOfNilCacheAndCloser(t *testing.T) {
	env := setup(t)
	w := revocationwatcher.New(env.st, nil, nil)

	tokenID := env.seedAPIToken(t)
	_, err := env.st.APITokens().Revoke(context.Background(), tokenID)
	require.NoError(t, err)

	// Must not panic when both Cache and Closer are nil.
	require.NotPanics(t, func() {
		w.RunOnce(context.Background())
	})
}

// TestWatcher_SeedWatermarksOnEmptyTablesLeavesZero pins the
// pre-bootstrap state: with no historical revocations, SeedWatermarks
// must succeed and leave both watermarks at the zero time so the
// first sweep still picks up anything that lands afterwards.
func TestWatcher_SeedWatermarksOnEmptyTablesLeavesZero(t *testing.T) {
	env := setup(t)

	require.NoError(t, env.watcher.SeedWatermarks(context.Background()))

	// A revocation that lands now must still be caught by RunOnce,
	// proving the zero seed didn't prematurely skip future rows.
	tokenID := env.seedAPIToken(t)
	_, err := env.st.APITokens().Revoke(context.Background(), tokenID)
	require.NoError(t, err)
	env.watcher.RunOnce(context.Background())
	assert.Equal(t, []string{tokenID}, env.closer.bearerSnapshot())
}

// TestWatcher_SeedWatermarksSkipsHistoricalRevocations pins the
// bootstrap optimization: revocations already in the DB at startup
// were torn down by the original in-process revoke handler, so the
// watcher's first sweep must NOT re-emit teardowns for them.
//
// Setup races the seed against existing revoked rows; after seed
// the first sweep sees rows <= HWM and skips them.
func TestWatcher_SeedWatermarksSkipsHistoricalRevocations(t *testing.T) {
	env := setup(t)

	// Two historical revocations BEFORE the watcher seeds. In a real
	// hub these would have already fired their in-process teardown
	// when the original revoke handler ran.
	historical1 := env.seedAPIToken(t)
	_, err := env.st.APITokens().Revoke(context.Background(), historical1)
	require.NoError(t, err)
	time.Sleep(2 * time.Millisecond)
	historical2 := env.seedAPIToken(t)
	_, err = env.st.APITokens().Revoke(context.Background(), historical2)
	require.NoError(t, err)

	// Seed past them.
	require.NoError(t, env.watcher.SeedWatermarks(context.Background()))

	// First sweep MUST be a no-op for the historical rows.
	env.watcher.RunOnce(context.Background())
	assert.Empty(t, env.closer.bearerSnapshot(),
		"historical revocations seeded past must not trigger teardowns")

	// A fresh revocation that lands AFTER the seed must still fire.
	time.Sleep(2 * time.Millisecond)
	fresh := env.seedAPIToken(t)
	_, err = env.st.APITokens().Revoke(context.Background(), fresh)
	require.NoError(t, err)
	env.watcher.RunOnce(context.Background())
	assert.Equal(t, []string{fresh}, env.closer.bearerSnapshot(),
		"post-seed revocations must still flow through the sweep")
}

// TestWatcher_SeedWatermarksFoldsAPIAndDelegationTokens checks that
// the seed picks the MAX across the two token tables, not just one.
// Without this, revoking only one table at startup would leave the
// other table's HWM stuck at the older value and the watcher would
// re-emit teardowns for the older table's historical rows.
func TestWatcher_SeedWatermarksFoldsAPIAndDelegationTokens(t *testing.T) {
	env := setup(t)

	// API token revoked first, delegation revoked second (later).
	apiID := env.seedAPIToken(t)
	_, err := env.st.APITokens().Revoke(context.Background(), apiID)
	require.NoError(t, err)
	time.Sleep(2 * time.Millisecond)
	delID := env.seedDelegationToken(t)
	_, err = env.st.DelegationTokens().Revoke(context.Background(), delID)
	require.NoError(t, err)

	require.NoError(t, env.watcher.SeedWatermarks(context.Background()))

	// Both rows pre-date the seed; first sweep should be a no-op.
	env.watcher.RunOnce(context.Background())
	assert.Empty(t, env.closer.bearerSnapshot(),
		"seed must fold both token tables; otherwise the older one re-fires")
}

// TestWatcher_SeedWatermarksSeedsUserBumpsToo pins the per-table
// independence of the user-side watermark: pre-startup user revokes
// (BumpTokensRevokedAt) must seed userWatermark just like token
// revokes seed tokenWatermark.
func TestWatcher_SeedWatermarksSeedsUserBumpsToo(t *testing.T) {
	env := setup(t)

	_, err := env.st.Users().BumpTokensRevokedAt(context.Background(), env.userID)
	require.NoError(t, err)

	require.NoError(t, env.watcher.SeedWatermarks(context.Background()))

	env.watcher.RunOnce(context.Background())
	assert.Empty(t, env.closer.userSnapshot(),
		"pre-seed user bump must not trigger CloseChannelsByUser on first sweep")

	// A fresh bump strictly after the seed is still observed.
	time.Sleep(2 * time.Millisecond)
	_, err = env.st.Users().BumpTokensRevokedAt(context.Background(), env.userID)
	require.NoError(t, err)
	env.watcher.RunOnce(context.Background())
	assert.Equal(t, []string{env.userID}, env.closer.userSnapshot())
}

// TestWatcher_PerUserAndPerBearerHWMsAreIndependent ensures the
// two watermarks don't confuse each other: a token revoke must
// not advance the user HWM, and vice versa, otherwise a new bump
// after a token revoke (or a new token revoke after a bump) would
// be missed.
func TestWatcher_PerUserAndPerBearerHWMsAreIndependent(t *testing.T) {
	env := setup(t)

	tokenID := env.seedAPIToken(t)
	_, err := env.st.APITokens().Revoke(context.Background(), tokenID)
	require.NoError(t, err)
	env.watcher.RunOnce(context.Background())
	require.Equal(t, []string{tokenID}, env.closer.bearerSnapshot())
	require.Empty(t, env.closer.userSnapshot())

	// Now bump the user. The user HWM was set to now() at New(); a
	// bump strictly after that must be visible.
	time.Sleep(5 * time.Millisecond)
	_, err = env.st.Users().BumpTokensRevokedAt(context.Background(), env.userID)
	require.NoError(t, err)
	env.watcher.RunOnce(context.Background())
	assert.Equal(t, []string{env.userID}, env.closer.userSnapshot(),
		"user HWM must be independent of token HWM")
}
