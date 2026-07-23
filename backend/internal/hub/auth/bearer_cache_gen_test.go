package auth

import (
	"context"
	"errors"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type validationErrorStore struct {
	store.Store
	err error
}

func (s validationErrorStore) Sessions() store.SessionStore {
	return validationErrorSessionStore{err: s.err}
}

type validationErrorSessionStore struct {
	store.SessionStore
	err error
}

type sessionValidationStore struct {
	store.SessionStore
	validate func(context.Context, string) (*store.SessionWithUser, error)
}

func (s sessionValidationStore) ValidateWithUser(ctx context.Context, sessionID string) (*store.SessionWithUser, error) {
	return s.validate(ctx, sessionID)
}

type sessionValidationOverrideStore struct {
	store.Store
	sessions store.SessionStore
}

func (s sessionValidationOverrideStore) Sessions() store.SessionStore { return s.sessions }

type touchRecordingSessionStore struct {
	store.SessionStore
	row     *store.SessionWithUser
	touched store.TouchSessionParams
	// touchMissed simulates the conditional UPDATE matching zero rows (the
	// session was touched within the threshold). Default false = one row
	// matched.
	touchMissed bool
}

func (s *touchRecordingSessionStore) ValidateWithUser(context.Context, string) (*store.SessionWithUser, error) {
	return s.row, nil
}

func (s *touchRecordingSessionStore) Touch(_ context.Context, p store.TouchSessionParams) (int64, error) {
	s.touched = p
	if s.touchMissed {
		return 0, nil
	}
	return 1, nil
}

func validSessionRow(userID, username string) *store.SessionWithUser {
	return &store.SessionWithUser{
		UserID: userID, OrgID: "org", Username: username,
		CreatedAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(time.Hour),
	}
}

type bearerValidationOverrideStore struct {
	store.Store
	api   store.APITokenStore
	users store.UserStore
}

func (s bearerValidationOverrideStore) APITokens() store.APITokenStore { return s.api }
func (s bearerValidationOverrideStore) Users() store.UserStore         { return s.users }

type blockingAPITokenStore struct {
	store.APITokenStore
	row     *store.APIToken
	started chan struct{}
	release chan struct{}
}

type contextBlockingAPITokenStore struct {
	store.APITokenStore
	row     *store.APIToken
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *contextBlockingAPITokenStore) GetByID(ctx context.Context, _ string) (*store.APIToken, error) {
	s.once.Do(func() { close(s.started) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.release:
		return s.row, nil
	}
}

func (s *contextBlockingAPITokenStore) Touch(context.Context, string) error { return nil }

func (s blockingAPITokenStore) GetByID(context.Context, string) (*store.APIToken, error) {
	close(s.started)
	<-s.release
	return s.row, nil
}

func (s blockingAPITokenStore) Touch(context.Context, string) error { return nil }

type countingAPITokenStore struct {
	store.APITokenStore
	rows  map[string]*store.APIToken
	calls sync.Map
}

func (s *countingAPITokenStore) GetByID(_ context.Context, tokenID string) (*store.APIToken, error) {
	counter, _ := s.calls.LoadOrStore(tokenID, &atomic.Int32{})
	counter.(*atomic.Int32).Add(1)
	return s.rows[tokenID], nil
}

func (s *countingAPITokenStore) Touch(context.Context, string) error { return nil }

func (s *countingAPITokenStore) callCount(tokenID string) int32 {
	counter, ok := s.calls.Load(tokenID)
	if !ok {
		return 0
	}
	return counter.(*atomic.Int32).Load()
}

type staticUserStore struct {
	store.UserStore
	row *store.User
}

func (s staticUserStore) GetByID(context.Context, string) (*store.User, error) { return s.row, nil }

func (s validationErrorSessionStore) ValidateWithUser(context.Context, string) (*store.SessionWithUser, error) {
	return nil, s.err
}

// TestBearerCacheFresh_TTL pins down the TTL component of cache
// freshness. An entry beyond sessionCacheTTL is stale regardless of
// generation; an entry within TTL with matching generation is fresh.
func TestBearerCacheFresh_TTL(t *testing.T) {
	a := &authInterceptor{state: &authState{}}
	now := time.Now()
	key := bearerCacheKey(BearerKindAPI, "token", []byte("secret"))

	fresh := cachedSession{user: &UserInfo{ID: userid.MustNew("u")}, cachedAt: now, gen: 0}
	assert.True(t, a.bearerCacheFresh(key, fresh), "newly cached entry must be fresh")

	stale := cachedSession{user: &UserInfo{ID: userid.MustNew("u")}, cachedAt: now.Add(-2 * sessionCacheTTL), gen: 0}
	assert.False(t, a.bearerCacheFresh(key, stale), "entry beyond TTL must be stale")
}

func TestValidateTokenCachedUnindexesStaleSessionWhenRevalidationFails(t *testing.T) {
	validationErr := errors.New("database unavailable")
	state := &authState{}
	state.revocationGen.Store(2)
	state.userInvalidations.Store("user", revocationMark{generation: 2, recordedAt: time.Now()})
	state.sessions.Store("session", cachedSession{
		user:     &UserInfo{ID: userid.MustNew("user")},
		cachedAt: time.Now(),
		gen:      1,
	})
	inner := &sync.Map{}
	inner.Store("session", struct{}{})
	state.userSessions.Store("user", inner)

	a := &authInterceptor{
		store: validationErrorStore{err: validationErr},
		state: state,
	}
	_, err := a.validateTokenCached(context.Background(), "session")
	require.ErrorIs(t, err, validationErr)
	_, sessionCached := state.sessions.Load("session")
	_, userIndexed := state.userSessions.Load("user")
	assert.False(t, sessionCached)
	assert.False(t, userIndexed, "stale session must not remain in the reverse index")
}

// TestSweepCachesOnce_EvictsStaleRetainsFresh exercises the lock-free-scan +
// locked-delete sweep end to end: entries past sessionCacheTTL are evicted from
// both the primary cache and every reverse index, while fresh entries are kept.
func TestSweepCachesOnce_EvictsStaleRetainsFresh(t *testing.T) {
	now := time.Now()
	stale := now.Add(-2 * sessionCacheTTL)
	state := &authState{}

	// Sessions: one stale, one fresh, each with a reverse index entry.
	state.sessions.Store("s-stale", cachedSession{user: &UserInfo{ID: userid.MustNew("u-stale")}, cachedAt: stale})
	indexSyncMap(&state.userSessions, "u-stale", "s-stale")
	state.sessions.Store("s-fresh", cachedSession{user: &UserInfo{ID: userid.MustNew("u-fresh")}, cachedAt: now})
	indexSyncMap(&state.userSessions, "u-fresh", "s-fresh")

	// Bearers: one stale, one fresh, each with both reverse index entries.
	staleBearer := bearerCacheKey(BearerKindAPI, "tok-stale", []byte("sec"))
	state.bearers.Store(staleBearer, cachedSession{user: &UserInfo{ID: userid.MustNew("ub-stale")}, cachedAt: stale})
	indexSyncMap(&state.bearerKeysByToken, staleBearer.bearerRef(), staleBearer)
	indexSyncMap(&state.userBearerKeys, "ub-stale", staleBearer)
	freshBearer := bearerCacheKey(BearerKindAPI, "tok-fresh", []byte("sec"))
	state.bearers.Store(freshBearer, cachedSession{user: &UserInfo{ID: userid.MustNew("ub-fresh")}, cachedAt: now})
	indexSyncMap(&state.bearerKeysByToken, freshBearer.bearerRef(), freshBearer)
	indexSyncMap(&state.userBearerKeys, "ub-fresh", freshBearer)

	a := &authInterceptor{state: state}
	a.sweepCachesOnce()

	assertAbsent := func(m *sync.Map, key any, msg string) {
		_, ok := m.Load(key)
		assert.False(t, ok, msg)
	}
	assertPresent := func(m *sync.Map, key any, msg string) {
		_, ok := m.Load(key)
		assert.True(t, ok, msg)
	}
	// Stale evicted from the primary cache and every reverse index.
	assertAbsent(&state.sessions, "s-stale", "stale session evicted")
	assertAbsent(&state.userSessions, "u-stale", "stale session reverse index cleaned")
	assertAbsent(&state.bearers, staleBearer, "stale bearer evicted")
	assertAbsent(&state.bearerKeysByToken, staleBearer.bearerRef(), "stale bearer token index cleaned")
	assertAbsent(&state.userBearerKeys, "ub-stale", "stale bearer user index cleaned")
	// Fresh retained.
	assertPresent(&state.sessions, "s-fresh", "fresh session retained")
	assertPresent(&state.bearers, freshBearer, "fresh bearer retained")
}

// TestSweepCachesOnce_RefreshBetweenScanAndDeleteSurvives pins the safety of
// moving the sweep SCAN outside revocationMu: the scan collects a stale
// (key, snapshot) pair, then the locked phase deletes it via a CAS on that exact
// snapshot. If the entry is refreshed in the scan->delete gap, the CAS must
// no-op so the fresh entry (and its reverse index) survives. This models that
// interleaving directly through the CAS-guarded delete the sweep runs.
func TestSweepCachesOnce_RefreshBetweenScanAndDeleteSurvives(t *testing.T) {
	now := time.Now()
	state := &authState{}
	staleSnap := cachedSession{user: &UserInfo{ID: userid.MustNew("u")}, cachedAt: now.Add(-2 * sessionCacheTTL)}
	state.sessions.Store("s", staleSnap)
	indexSyncMap(&state.userSessions, "u", "s")

	// A concurrent slide refreshes the entry between the lock-free scan (which
	// observed staleSnap) and the locked delete.
	freshSnap := cachedSession{user: &UserInfo{ID: userid.MustNew("u")}, cachedAt: now}
	state.sessions.Store("s", freshSnap)

	// The locked phase deletes the snapshot the scan observed -- its CAS must fail.
	state.deleteStaleSession("s", staleSnap)

	got, ok := state.sessions.Load("s")
	require.True(t, ok, "a session refreshed between scan and delete must survive (CAS no-op)")
	assert.Equal(t, now, got.(cachedSession).cachedAt, "the refreshed snapshot is the one retained")
	_, indexed := state.userSessions.Load("u")
	assert.True(t, indexed, "the refreshed session's reverse index must remain intact")
}

func TestAuthenticateUsesTouchedSessionExpiry(t *testing.T) {
	oldExpiry := time.Now().Add(time.Minute).UTC()
	sessions := &touchRecordingSessionStore{row: &store.SessionWithUser{
		UserID: "user", OrgID: "org", Username: "user",
		CreatedAt: time.Now().Add(-time.Hour), ExpiresAt: oldExpiry,
	}}
	a := &authInterceptor{
		store: sessionValidationOverrideStore{sessions: sessions},
		state: &authState{},
	}

	ctx, err := a.authenticate(
		context.Background(), "/private",
		CookieName+"=session", "",
	)
	require.NoError(t, err)
	user := GetUser(ctx)
	require.NotNil(t, user)
	assert.Equal(t, DeadlineAt(sessions.touched.ExpiresAt), user.CredentialExpiresAt)
	touchedAt, atOK := user.CredentialExpiresAt.At()
	require.True(t, atOK)
	assert.True(t, touchedAt.After(oldExpiry))
}

// TestZeroRowTouchDoesNotExtendSessionExpiry pins the C1 guard: when the
// conditional session Touch matches no row (the session was touched within the
// threshold, e.g. on a freshly-restarted Hub whose lastTouch map is empty), the
// in-memory credential expiry must NOT slide past the un-advanced DB value.
func TestZeroRowTouchDoesNotExtendSessionExpiry(t *testing.T) {
	oldExpiry := time.Now().Add(time.Minute).UTC()
	sessions := &touchRecordingSessionStore{
		row: &store.SessionWithUser{
			UserID: "user", OrgID: "org", Username: "user",
			CreatedAt: time.Now().Add(-time.Hour), ExpiresAt: oldExpiry,
		},
		touchMissed: true,
	}
	a := &authInterceptor{
		store: sessionValidationOverrideStore{sessions: sessions},
		state: &authState{},
	}

	ctx, err := a.authenticate(
		context.Background(), "/private",
		CookieName+"=session", "",
	)
	require.NoError(t, err)
	user := GetUser(ctx)
	require.NotNil(t, user)
	assert.Equal(t, DeadlineAt(oldExpiry), user.CredentialExpiresAt,
		"a zero-row touch must not extend the in-memory session expiry")
}

func TestUnrelatedSessionEvictionDoesNotReloadWarmCacheEntry(t *testing.T) {
	var calls sync.Map
	sessions := sessionValidationStore{validate: func(_ context.Context, sessionID string) (*store.SessionWithUser, error) {
		counter, _ := calls.LoadOrStore(sessionID, &atomic.Int32{})
		counter.(*atomic.Int32).Add(1)
		return validSessionRow("user-"+sessionID, sessionID), nil
	}}
	state := &authState{}
	a := &authInterceptor{
		store: sessionValidationOverrideStore{sessions: sessions},
		state: state,
	}
	sc := &AuthContextRegistry{state: state}

	_, err := a.validateTokenCached(context.Background(), "session-a")
	require.NoError(t, err)
	_, err = a.validateTokenCached(context.Background(), "session-b")
	require.NoError(t, err)
	sc.Evict("session-a")
	_, err = a.validateTokenCached(context.Background(), "session-b")
	require.NoError(t, err)

	bCalls, ok := calls.Load("session-b")
	require.True(t, ok)
	assert.Equal(t, int32(1), bCalls.(*atomic.Int32).Load(), "evicting session A must not invalidate session B")
}

func TestUnrelatedBearerEvictionDoesNotReloadWarmCacheEntry(t *testing.T) {
	pepper := []byte("0123456789abcdef0123456789abcdef")
	api := &countingAPITokenStore{rows: make(map[string]*store.APIToken)}
	base := bearerValidationOverrideStore{
		api:   api,
		users: staticUserStore{row: &store.User{ID: "user", OrgID: "org", Username: "user"}},
	}
	validator, err := NewTokenValidator(base, pepper)
	require.NoError(t, err)
	secretA := MintAccessSecret()
	secretB := MintAccessSecret()
	api.rows["token-a"] = &store.APIToken{
		ID: "token-a", UserID: "user", SecretHash: validator.HashSecret(secretA), CreatedAt: time.Now(),
	}
	api.rows["token-b"] = &store.APIToken{
		ID: "token-b", UserID: "user", SecretHash: validator.HashSecret(secretB), CreatedAt: time.Now(),
	}
	state := &authState{}
	a := &authInterceptor{store: base, tokenValidator: validator, state: state}
	sc := &AuthContextRegistry{state: state}
	bearerA := "Bearer " + FormatBearer(BearerKindAPI, "token-a", secretA)
	bearerB := "Bearer " + FormatBearer(BearerKindAPI, "token-b", secretB)

	_, _, err = a.tryAuthenticateBearer(context.Background(), bearerA)
	require.NoError(t, err)
	_, _, err = a.tryAuthenticateBearer(context.Background(), bearerB)
	require.NoError(t, err)
	sc.EvictBearer(NewBearerRef(BearerKindAPI, "token-a"))
	_, _, err = a.tryAuthenticateBearer(context.Background(), bearerB)
	require.NoError(t, err)

	assert.Equal(t, int32(1), api.callCount("token-b"), "evicting bearer A must not invalidate bearer B")
}

func TestSessionRevocationDuringValidationRejectsResult(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	sessions := sessionValidationStore{validate: func(context.Context, string) (*store.SessionWithUser, error) {
		close(started)
		<-release
		return validSessionRow("user", "stale"), nil
	}}
	state := &authState{}
	a := &authInterceptor{store: sessionValidationOverrideStore{sessions: sessions}, state: state}
	sc := &AuthContextRegistry{state: state}

	result := make(chan error, 1)
	go func() {
		_, err := a.validateTokenCached(context.Background(), "session")
		result <- err
	}()
	<-started
	sc.Evict("session")
	close(release)

	err := <-result
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	_, cached := state.sessions.Load("session")
	assert.False(t, cached, "a session revoked during validation must not be cached")
}

func TestBearerRevocationDuringValidationRejectsResult(t *testing.T) {
	pepper := []byte("0123456789abcdef0123456789abcdef")
	secret := MintAccessSecret()
	tokenID := "token"
	started := make(chan struct{})
	release := make(chan struct{})
	api := &blockingAPITokenStore{
		row:     &store.APIToken{ID: tokenID, UserID: "user", CreatedAt: time.Now()},
		started: started,
		release: release,
	}
	base := bearerValidationOverrideStore{
		api:   api,
		users: staticUserStore{row: &store.User{ID: "user", OrgID: "org", Username: "user"}},
	}
	validator, err := NewTokenValidator(base, pepper)
	require.NoError(t, err)
	api.row.SecretHash = validator.HashSecret(secret)
	state := &authState{}
	a := &authInterceptor{store: base, tokenValidator: validator, state: state}
	sc := &AuthContextRegistry{state: state}
	bearer := FormatBearer(BearerKindAPI, tokenID, secret)

	result := make(chan error, 1)
	go func() {
		_, _, err := a.tryAuthenticateBearer(context.Background(), "Bearer "+bearer)
		result <- err
	}()
	<-started
	sc.EvictBearer(NewBearerRef(BearerKindAPI, tokenID))
	close(release)

	err = <-result
	require.Error(t, err)
	_, cached := state.bearers.Load(bearerCacheKey(BearerKindAPI, tokenID, validator.HashSecret(secret)))
	assert.False(t, cached, "a bearer revoked during validation must not be cached")
}

func TestBearerSingleflightFollowerDoesNotInheritLeaderCancellation(t *testing.T) {
	pepper := []byte("0123456789abcdef0123456789abcdef")
	secret := MintAccessSecret()
	tokenID := "token"
	api := &contextBlockingAPITokenStore{
		row:     &store.APIToken{ID: tokenID, UserID: "user", CreatedAt: time.Now()},
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	base := bearerValidationOverrideStore{
		api:   api,
		users: staticUserStore{row: &store.User{ID: "user", OrgID: "org", Username: "user"}},
	}
	validator, err := NewTokenValidator(base, pepper)
	require.NoError(t, err)
	api.row.SecretHash = validator.HashSecret(secret)
	a := &authInterceptor{store: base, tokenValidator: validator, state: &authState{}}
	bearer := "Bearer " + FormatBearer(BearerKindAPI, tokenID, secret)

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderResult := make(chan error, 1)
	go func() {
		_, _, err := a.tryAuthenticateBearer(leaderCtx, bearer)
		leaderResult <- err
	}()
	<-api.started

	followerResult := make(chan error, 1)
	followerStarted := make(chan struct{})
	go func() {
		close(followerStarted)
		_, _, err := a.tryAuthenticateBearer(context.Background(), bearer)
		followerResult <- err
	}()
	<-followerStarted
	time.Sleep(20 * time.Millisecond)
	cancelLeader()
	require.ErrorIs(t, <-leaderResult, context.Canceled)
	close(api.release)
	require.NoError(t, <-followerResult)
}

// TestBearerSingleflightFollowersGetDistinctUserInfo pins the invariant that
// every caller collapsed into one ValidateBearer flight receives its OWN
// *UserInfo clone, matching the per-caller isolation the cache-hit and
// DB-validate paths already give. Sharing one pointer across concurrent
// requests would be a latent cross-request data race the moment any handler
// mutates the context user.
func TestBearerSingleflightFollowersGetDistinctUserInfo(t *testing.T) {
	pepper := []byte("0123456789abcdef0123456789abcdef")
	secret := MintAccessSecret()
	tokenID := "token"
	api := &contextBlockingAPITokenStore{
		row:     &store.APIToken{ID: tokenID, UserID: "user", CreatedAt: time.Now()},
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	base := bearerValidationOverrideStore{
		api:   api,
		users: staticUserStore{row: &store.User{ID: "user", OrgID: "org", Username: "user"}},
	}
	validator, err := NewTokenValidator(base, pepper)
	require.NoError(t, err)
	api.row.SecretHash = validator.HashSecret(secret)
	a := &authInterceptor{store: base, tokenValidator: validator, state: &authState{}}
	bearer := "Bearer " + FormatBearer(BearerKindAPI, tokenID, secret)

	type result struct {
		user *UserInfo
		err  error
	}
	leaderResult := make(chan result, 1)
	go func() {
		u, _, err := a.tryAuthenticateBearer(context.Background(), bearer)
		leaderResult <- result{u, err}
	}()
	<-api.started

	followerResult := make(chan result, 1)
	followerStarted := make(chan struct{})
	go func() {
		close(followerStarted)
		u, _, err := a.tryAuthenticateBearer(context.Background(), bearer)
		followerResult <- result{u, err}
	}()
	<-followerStarted
	time.Sleep(20 * time.Millisecond)
	close(api.release)

	leader := <-leaderResult
	follower := <-followerResult
	require.NoError(t, leader.err)
	require.NoError(t, follower.err)
	require.NotNil(t, leader.user)
	require.NotNil(t, follower.user)
	assert.NotSame(t, leader.user, follower.user,
		"followers collapsed into one singleflight must each receive their own *UserInfo clone")
}

func TestSoloAuthenticationAdvancesPastUserRevocation(t *testing.T) {
	solo := &UserInfo{ID: userid.MustNew("solo"), UserAuthGeneration: 1}
	state := &authState{}
	a := &authInterceptor{soloUser: solo, state: state}
	cache := &AuthContextRegistry{state: state}

	cache.RevokeUserAuthContextAtGeneration("solo", 2)
	ctx, err := a.authenticate(context.Background(), "/private", "", "")
	require.NoError(t, err)
	current := GetUser(ctx)
	require.NotNil(t, current)
	assert.Equal(t, int64(2), current.UserAuthGeneration)
	assert.True(t, cache.IsAuthContextCurrent(current))
}

func TestUserInvalidationDuringValidationRetriesStableSnapshot(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	sessions := sessionValidationStore{validate: func(context.Context, string) (*store.SessionWithUser, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			return validSessionRow("user", "stale"), nil
		}
		return validSessionRow("user", "fresh"), nil
	}}
	state := &authState{}
	a := &authInterceptor{store: sessionValidationOverrideStore{sessions: sessions}, state: state}
	sc := &AuthContextRegistry{state: state}

	result := make(chan *UserInfo, 1)
	go func() {
		user, _ := a.validateTokenCached(context.Background(), "session")
		result <- user
	}()
	<-started
	sc.EvictByUserID("user")
	close(release)

	user := <-result
	require.NotNil(t, user)
	assert.Equal(t, "fresh", user.Username)
	assert.Equal(t, int32(2), calls.Load(), "matching user invalidation must retry an in-flight validation")
}

func TestEvictByUserIDInvalidatesBearerUserInfo(t *testing.T) {
	state := &authState{}
	registry := &AuthContextRegistry{state: state}
	key := bearerCacheKey(BearerKindAPI, "token", []byte("secret"))
	cached := cachedSession{user: &UserInfo{ID: userid.MustNew("user"), Credential: APICredential("token")}}
	state.bearers.Store(key, cached)
	registry.state.indexBearerCacheEntry(key, cached)

	registry.EvictByUserID("user")

	_, exists := state.bearers.Load(key)
	assert.False(t, exists, "profile invalidation must not retain stale bearer user data")
}

func TestAuthenticateHTTPPreservesInternalSessionValidationError(t *testing.T) {
	validationErr := errors.New("database unavailable")
	req := httptest.NewRequest("GET", "/ws/channel", nil)
	req.AddCookie(BuildSessionCookie("session", time.Now().Add(time.Hour), false))

	_, err := AuthenticateHTTP(context.Background(), req, HTTPAuthOpts{
		Store:   validationErrorStore{err: validationErr},
		Cookies: []bool{false},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, validationErr)
}

func TestAuthenticateHTTPRefreshesSyntheticUserGeneration(t *testing.T) {
	contexts := &AuthContextRegistry{state: &authState{}}
	contexts.RevokeUserAuthContextAtGeneration("solo", 2)
	req := httptest.NewRequest("GET", "/ws/channel", nil)

	user, err := AuthenticateHTTP(context.Background(), req, HTTPAuthOpts{
		SoloUser: &UserInfo{ID: userid.MustNew("solo"), UserAuthGeneration: 1},
		Contexts: contexts,
	})

	require.NoError(t, err)
	assert.Equal(t, int64(2), user.UserAuthGeneration)
	assert.True(t, contexts.IsAuthContextCurrent(user))
}

func TestAuthenticatedLeaseExactSessionRevocation(t *testing.T) {
	sc := &AuthContextRegistry{state: &authState{}}
	revokedCtx, revokedCancel := context.WithCancel(context.Background())
	revokedRelease, ok := sc.RegisterAuthenticatedLease(context.Background(), &UserInfo{
		ID: userid.MustNew("user"), Credential: SessionCredential("session-1"),
	}, revokedCancel)
	require.True(t, ok)
	defer revokedRelease()

	otherCtx, otherCancel := context.WithCancel(context.Background())
	otherRelease, ok := sc.RegisterAuthenticatedLease(context.Background(), &UserInfo{
		ID: userid.MustNew("user"), Credential: SessionCredential("session-2"),
	}, otherCancel)
	require.True(t, ok)
	defer otherRelease()

	sc.Evict("session-1")
	select {
	case <-revokedCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("revoked session lease was not canceled")
	}
	select {
	case <-otherCtx.Done():
		t.Fatal("unrelated session lease was canceled")
	default:
	}
}

func TestAuthenticatedLeaseExactBearerRevocation(t *testing.T) {
	sc := &AuthContextRegistry{state: &authState{}}
	revokedCtx, revokedCancel := context.WithCancel(context.Background())
	revokedRelease, ok := sc.RegisterAuthenticatedLease(context.Background(), &UserInfo{
		ID: userid.MustNew("user"), Credential: APICredential("token-1"),
	}, revokedCancel)
	require.True(t, ok)
	defer revokedRelease()

	otherCtx, otherCancel := context.WithCancel(context.Background())
	otherRelease, ok := sc.RegisterAuthenticatedLease(context.Background(), &UserInfo{
		ID: userid.MustNew("user"), Credential: APICredential("token-2"),
	}, otherCancel)
	require.True(t, ok)
	defer otherRelease()

	sc.EvictBearer(NewBearerRef(BearerKindAPI, "token-1"))
	select {
	case <-revokedCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("revoked bearer lease was not canceled")
	}
	select {
	case <-otherCtx.Done():
		t.Fatal("unrelated bearer lease was canceled")
	default:
	}
}

func TestAuthenticatedLeaseUserRevocationIsGenerationSelective(t *testing.T) {
	sc := &AuthContextRegistry{state: &authState{}}
	oldCtx, oldCancel := context.WithCancel(context.Background())
	oldRelease, ok := sc.RegisterAuthenticatedLease(context.Background(), &UserInfo{
		ID: userid.MustNew("user"), Credential: SessionCredential("old"), UserAuthGeneration: 4,
	}, oldCancel)
	require.True(t, ok)
	defer oldRelease()

	currentCtx, currentCancel := context.WithCancel(context.Background())
	currentRelease, ok := sc.RegisterAuthenticatedLease(context.Background(), &UserInfo{
		ID: userid.MustNew("user"), Credential: SessionCredential("current"), UserAuthGeneration: 5,
	}, currentCancel)
	require.True(t, ok)
	defer currentRelease()

	sc.RevokeUserAuthContextAtGeneration("user", 5)
	select {
	case <-oldCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("older-generation lease was not canceled")
	}
	select {
	case <-currentCtx.Done():
		t.Fatal("current-generation lease was canceled")
	default:
	}
}

func TestUserRevocationGenerationDoesNotRegress(t *testing.T) {
	sc := &AuthContextRegistry{state: &authState{}}
	sc.RevokeUserAuthContextAtGeneration("user", 5)
	sc.RevokeUserAuthContextAtGeneration("user", 4)

	assert.False(t, sc.IsAuthContextCurrent(&UserInfo{
		ID: userid.MustNew("user"), UserAuthGeneration: 4,
	}))
	assert.True(t, sc.IsAuthContextCurrent(&UserInfo{
		ID: userid.MustNew("user"), UserAuthGeneration: 5,
	}))
}

func TestUserRevocationCacheEvictionIsGenerationSelective(t *testing.T) {
	state := &authState{}
	sc := &AuthContextRegistry{state: state}
	oldUser := &UserInfo{ID: userid.MustNew("user"), Credential: SessionCredential("old"), UserAuthGeneration: 4}
	currentUser := &UserInfo{ID: userid.MustNew("user"), Credential: SessionCredential("current"), UserAuthGeneration: 5}
	state.sessions.Store("old", cachedSession{user: oldUser})
	state.sessions.Store("current", cachedSession{user: currentUser})
	inner := &sync.Map{}
	inner.Store("old", struct{}{})
	inner.Store("current", struct{}{})
	state.userSessions.Store("user", inner)
	oldBearer := bearerCacheKey(BearerKindAPI, "old", []byte("secret"))
	currentBearer := bearerCacheKey(BearerKindAPI, "current", []byte("secret"))
	oldBearerSession := cachedSession{user: &UserInfo{ID: userid.MustNew("user"), Credential: APICredential("old"), UserAuthGeneration: 4}}
	currentBearerSession := cachedSession{user: &UserInfo{ID: userid.MustNew("user"), Credential: APICredential("current"), UserAuthGeneration: 5}}
	state.bearers.Store(oldBearer, oldBearerSession)
	state.bearers.Store(currentBearer, currentBearerSession)
	sc.state.indexBearerCacheEntry(oldBearer, oldBearerSession)
	sc.state.indexBearerCacheEntry(currentBearer, currentBearerSession)

	sc.RevokeUserAuthContextAtGeneration("user", 5)

	_, oldSessionExists := state.sessions.Load("old")
	_, currentSessionExists := state.sessions.Load("current")
	_, oldBearerExists := state.bearers.Load(oldBearer)
	_, currentBearerExists := state.bearers.Load(currentBearer)
	assert.False(t, oldSessionExists)
	assert.True(t, currentSessionExists)
	assert.False(t, oldBearerExists)
	assert.True(t, currentBearerExists)
}

// TestBlankUserIDRevocationEvictsNothingAndBumpsNoGeneration pins the blank-id
// prologue on the revocation entrypoint, which is the ONE thing keeping the
// three Matches calls beneath it correct.
//
// Matches is tuned for GRANT semantics: false means "not authorized", which is
// safe on an authorization path. On this EVICTION path false means "do not
// revoke", so a blank id reaching the comparisons would skip every cached
// session, bearer, and lease -- an operator's containment action reporting
// success having evicted nothing. Nothing about userid.UserID prevents that;
// only the `userID == ""` guard does, and until this test existed deleting it
// as "redundant with userid.UserID" left the entire suite green.
//
// The generation is the observable: without the guard the call still bumps it
// and records a junk revocation mark under the empty key, which invalidates
// every warm cache entry in the process while revoking nothing.
func TestBlankUserIDRevocationEvictsNothingAndBumpsNoGeneration(t *testing.T) {
	newFixture := func() (*authState, *AuthContextRegistry, bearerCacheKeyParts) {
		state := &authState{}
		sc := &AuthContextRegistry{state: state}
		user := &UserInfo{ID: userid.MustNew("user"), Credential: SessionCredential("sess"), UserAuthGeneration: 4}
		state.sessions.Store("sess", cachedSession{user: user})
		inner := &sync.Map{}
		inner.Store("sess", struct{}{})
		state.userSessions.Store("user", inner)
		bearer := bearerCacheKey(BearerKindAPI, "tok", []byte("secret"))
		bearerSession := cachedSession{user: &UserInfo{ID: userid.MustNew("user"), Credential: APICredential("tok"), UserAuthGeneration: 4}}
		state.bearers.Store(bearer, bearerSession)
		state.indexBearerCacheEntry(bearer, bearerSession)
		return state, sc, bearer
	}

	t.Run("blank id changes nothing", func(t *testing.T) {
		state, sc, bearer := newFixture()
		before := state.revocationGen.Load()

		sc.RevokeUserAuthContextAtGeneration("", 5)

		assert.Equal(t, before, state.revocationGen.Load(),
			"a blank id must not bump the revocation generation: it revokes nobody while invalidating every warm cache entry")
		_, marked := state.userRevocations.Load("")
		assert.False(t, marked, "a blank id must not record a revocation mark")
		_, sessionExists := state.sessions.Load("sess")
		_, bearerExists := state.bearers.Load(bearer)
		assert.True(t, sessionExists, "a blank id must not disturb a real user's cached session")
		assert.True(t, bearerExists, "a blank id must not disturb a real user's cached bearer")
	})

	// Control: the same fixture, revoked by the id that actually owns it. This
	// is what makes the case above mean "refused", not "the fixture was never
	// reachable in the first place".
	t.Run("control: the owning id evicts", func(t *testing.T) {
		state, sc, bearer := newFixture()
		before := state.revocationGen.Load()

		sc.RevokeUserAuthContextAtGeneration("user", 5)

		assert.Greater(t, state.revocationGen.Load(), before)
		_, sessionExists := state.sessions.Load("sess")
		_, bearerExists := state.bearers.Load(bearer)
		assert.False(t, sessionExists, "control: the owner's cached session is evicted")
		assert.False(t, bearerExists, "control: the owner's cached bearer is evicted")
	})
}

func TestAuthenticatedLeaseRegistrationRejectsRevokedIdentity(t *testing.T) {
	sc := &AuthContextRegistry{state: &authState{}}
	sc.Evict("session")

	ctx, cancel := context.WithCancel(context.Background())
	release, ok := sc.RegisterAuthenticatedLease(context.Background(), &UserInfo{
		ID: userid.MustNew("user"), Credential: SessionCredential("session"), AuthGeneration: 0,
	}, cancel)
	defer release()
	assert.False(t, ok)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("rejected lease was not canceled")
	}
}

func TestAuthenticatedLeaseExpiresWithCredential(t *testing.T) {
	sc := &AuthContextRegistry{state: &authState{}}
	ctx, cancel := context.WithCancel(context.Background())
	release, ok := sc.RegisterAuthenticatedLease(context.Background(), &UserInfo{
		ID: userid.MustNew("user"), Credential: SessionCredential("session"), CredentialExpiresAt: DeadlineAt(time.Now().Add(20 * time.Millisecond)),
	}, cancel)
	require.True(t, ok)
	defer release()

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("lease remained active after credential expiry")
	}
}

// The global sequence orders validation against scoped marks; advancing it
// alone must not invalidate an unrelated cache entry.
func TestBearerCacheFresh_UnmarkedGenerationBumpDoesNotInvalidate(t *testing.T) {
	a := &authInterceptor{state: &authState{}}
	key := bearerCacheKey(BearerKindAPI, "token", []byte("secret"))
	cs := cachedSession{user: &UserInfo{ID: userid.MustNew("u")}, cachedAt: time.Now(), gen: a.state.revocationGen.Load()}
	assert.True(t, a.bearerCacheFresh(key, cs))

	a.state.revocationGen.Add(1)
	assert.True(t, a.bearerCacheFresh(key, cs), "an unmarked sequence advance is unrelated to this bearer")
}

// TestAuthContextRegistry_EvictsBumpGen runs each Evict* surface through one
// call and asserts the ordering sequence moves forward so its scoped mark can
// be compared with validation snapshots.
func TestAuthContextRegistry_EvictsBumpGen(t *testing.T) {
	cases := []struct {
		name string
		fn   func(*AuthContextRegistry)
	}{
		{"EvictBearer", func(c *AuthContextRegistry) { c.EvictBearer(NewBearerRef(BearerKindAPI, "some-token")) }},
		{"EvictByUserID", func(c *AuthContextRegistry) { c.EvictByUserID("u-1") }},
		{"RevokeUserAuthContextAtGeneration", func(c *AuthContextRegistry) { c.RevokeUserAuthContextAtGeneration("u-1", 1) }},
		{"Evict", func(c *AuthContextRegistry) { c.Evict("sess-1") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, sc := NewInterceptorWithTokens(nil, nil, nil, false, false)
			t.Cleanup(sc.Stop)
			before := sc.state.revocationGen.Load()
			tc.fn(sc)
			assert.Greater(t, sc.state.revocationGen.Load(), before, "%s must bump the revocation generation", tc.name)
		})
	}
}

func TestDelegationAllowedProcedures_FailClosed(t *testing.T) {
	allowed := []string{
		leapmuxv1connect.ChannelServiceGetWorkerHandshakeParamsProcedure,
		leapmuxv1connect.ChannelServiceOpenChannelProcedure,
		leapmuxv1connect.ChannelServiceCloseChannelProcedure,
		leapmuxv1connect.ChannelServicePrepareWorkspaceAccessProcedure,
		leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure,
		leapmuxv1connect.WorkspaceServiceGetWorkspaceProcedure,
		leapmuxv1connect.WorkspaceServiceListTabsProcedure,
		leapmuxv1connect.WorkspaceServiceGetTabProcedure,
		leapmuxv1connect.WorkspaceServiceLocateTabProcedure,
		leapmuxv1connect.WorkspaceServiceLocateTileProcedure,
		leapmuxv1connect.OrgCRDTSubmitOpsProcedure,
		leapmuxv1connect.OrgCRDTGetMaterializedProcedure,
		leapmuxv1connect.OrgCRDTUpdatePresenceProcedure,
	}
	for _, procedure := range allowed {
		assert.True(t, delegationAllowedProcedures[procedure], "%s must be callable by scoped delegation bearers", procedure)
	}

	denied := []string{
		leapmuxv1connect.AuthServiceGetCurrentUserProcedure,
		leapmuxv1connect.UserServiceGetUserProcedure,
		leapmuxv1connect.WorkerManagementServiceListWorkersProcedure,
		leapmuxv1connect.WorkerManagementServiceGetWorkerProcedure,
		leapmuxv1connect.WorkspaceServiceCreateWorkspaceProcedure,
		leapmuxv1connect.WorkspaceServiceRenameWorkspaceProcedure,
		leapmuxv1connect.WorkspaceServiceDeleteWorkspaceProcedure,
	}
	for _, procedure := range denied {
		assert.False(t, delegationAllowedProcedures[procedure], "%s must stay denied unless it gets an explicit scope guard", procedure)
	}
}

func TestAuthContextRegistry_IsAuthContextCurrentIsScoped(t *testing.T) {
	_, sc := NewInterceptorWithTokens(nil, nil, nil, false, false)
	t.Cleanup(sc.Stop)

	gen := sc.state.revocationGen.Load()
	user := &UserInfo{ID: userid.MustNew("u-1"), Credential: SessionCredential("sess-1"), AuthGeneration: gen}
	assert.True(t, sc.IsAuthContextCurrent(user))

	sc.EvictByUserID("u-2")
	sc.Evict("sess-2")
	sc.EvictBearer(NewBearerRef(BearerKindAPI, "tok-2"))
	assert.True(t, sc.IsAuthContextCurrent(user), "unrelated revocations must not reject this auth context")

	sc.EvictByUserID("u-1")
	assert.True(t, sc.IsAuthContextCurrent(user), "ordinary user cache eviction must not reject this auth context")

	sc.RevokeUserAuthContextAtGeneration("u-1", 1)
	assert.False(t, sc.IsAuthContextCurrent(user), "user-wide revocation must reject older user auth contexts")

	gen = sc.state.revocationGen.Load()
	sessionUser := &UserInfo{ID: userid.MustNew("u-3"), Credential: SessionCredential("sess-3"), AuthGeneration: gen}
	sc.Evict("sess-3")
	assert.False(t, sc.IsAuthContextCurrent(sessionUser), "session revocation must reject that session")

	gen = sc.state.revocationGen.Load()
	bearerUser := &UserInfo{ID: userid.MustNew("u-4"), Credential: APICredential("tok-4"), AuthGeneration: gen}
	sc.EvictBearer(NewBearerRef(BearerKindAPI, "tok-4"))
	assert.False(t, sc.IsAuthContextCurrent(bearerUser), "bearer revocation must reject that bearer")
}

func TestAuthContextRegistry_CurrentCredentialExpiryReadsSlidSessionDeadline(t *testing.T) {
	reg := &AuthContextRegistry{state: &authState{}}
	e0 := time.Now().Add(10 * time.Minute).UTC()
	e1 := time.Now().Add(40 * time.Minute).UTC()

	// A cookie-session request that captured e0 before a concurrent slide.
	sessionUser := &UserInfo{ID: userid.MustNew("u-1"), Credential: SessionCredential("sess-1"), CredentialExpiresAt: DeadlineAt(e0)}

	// No cache entry yet: fall back to the caller's captured deadline.
	assert.Equal(t, DeadlineAt(e0), reg.CurrentCredentialExpiry(context.Background(), sessionUser))

	// A concurrent slide records the newer deadline in the shared session cache
	// (as touchSession does). CurrentCredentialExpiry must return e1, not the
	// caller's stale e0 -- this is what keeps a channel armed at the current
	// deadline when a slide raced its registration.
	reg.state.sessions.Store("sess-1", cachedSession{
		user: &UserInfo{ID: userid.MustNew("u-1"), Credential: SessionCredential("sess-1"), CredentialExpiresAt: DeadlineAt(e1)},
	})
	assert.Equal(t, DeadlineAt(e1), reg.CurrentCredentialExpiry(context.Background(), sessionUser),
		"must return the slid deadline recorded in the session cache, not the caller's captured value")

	// A bearer credential has no session id and no recorded rotation extension
	// yet, so it falls back to the caller's captured value.
	bearerUser := &UserInfo{ID: userid.MustNew("u-1"), Credential: APICredential("tok-1"), CredentialExpiresAt: DeadlineAt(e0)}
	assert.Equal(t, DeadlineAt(e0), reg.CurrentCredentialExpiry(context.Background(), bearerUser))

	// Nil-safe on both the receiver and the user.
	assert.Equal(t, DeadlineAt(e0), (*AuthContextRegistry)(nil).CurrentCredentialExpiry(context.Background(), sessionUser))
	assert.True(t, reg.CurrentCredentialExpiry(context.Background(), nil).IsUnset())
}

// A session cache HIT must still take the more permissive of the cached-row
// deadline and the caller's captured deadline: if the cached row is ever
// repopulated below the connect-time value (a stale/raced cache write), the
// channel must be armed at the caller's later deadline, not torn down early at
// the smaller cached one. This mirrors the Later() the bearer arm, the DB
// fallback, and the lock-held twin currentCredentialExpiryLocked all use.
func TestAuthContextRegistry_CurrentCredentialExpiryCacheHitNeverShrinksBelowConnectTime(t *testing.T) {
	reg := &AuthContextRegistry{state: &authState{}}
	connectTime := time.Now().Add(40 * time.Minute).UTC() // caller captured a later deadline
	staleCached := time.Now().Add(10 * time.Minute).UTC() // cache row holds an earlier one

	sessionUser := &UserInfo{ID: userid.MustNew("u-1"), Credential: SessionCredential("sess-1"), CredentialExpiresAt: DeadlineAt(connectTime)}
	reg.state.sessions.Store("sess-1", cachedSession{
		user: &UserInfo{ID: userid.MustNew("u-1"), Credential: SessionCredential("sess-1"), CredentialExpiresAt: DeadlineAt(staleCached)},
	})

	assert.Equal(t, DeadlineAt(connectTime), reg.CurrentCredentialExpiry(context.Background(), sessionUser),
		"cache hit must return the more permissive connect-time deadline, not the smaller cached one")
}

// On a session-cache MISS (the row was evicted after a slide extended the
// deadline), CurrentCredentialExpiry must read the authoritative DB expiry rather
// than return the stale connect-time value, so a channel armed after the eviction
// is not torn down at the earlier deadline. A gone/expired session or a transient
// lookup failure degrades to the connect-time value.
func TestAuthContextRegistry_CurrentCredentialExpiryFallsBackToDBOnCacheMiss(t *testing.T) {
	connectTime := time.Now().Add(10 * time.Minute).UTC() // value captured at request validation
	slid := time.Now().Add(40 * time.Minute).UTC()        // authoritative DB value after a slide
	sessionUser := &UserInfo{ID: userid.MustNew("u"), Credential: SessionCredential("sess-1"), CredentialExpiresAt: DeadlineAt(connectTime)}

	// No cache row for the session (evicted); the DB fallback returns the slid deadline.
	var gotSessionID string
	reg := &AuthContextRegistry{state: &authState{}}
	reg.sessionExpiry = func(_ context.Context, sessionID string) (time.Time, bool, error) {
		gotSessionID = sessionID
		return slid, true, nil
	}
	assert.Equal(t, DeadlineAt(slid), reg.CurrentCredentialExpiry(context.Background(), sessionUser),
		"cache miss must fall back to the authoritative DB expiry, not the stale connect-time value")
	assert.Equal(t, "sess-1", gotSessionID, "the fallback must look up the evicted session by id")

	// A gone/expired session (ok=false) degrades to the connect-time value.
	reg.sessionExpiry = func(_ context.Context, _ string) (time.Time, bool, error) {
		return time.Time{}, false, nil
	}
	assert.Equal(t, DeadlineAt(connectTime), reg.CurrentCredentialExpiry(context.Background(), sessionUser),
		"a gone/expired session degrades to the connect-time value")

	// A transient lookup failure also degrades to the connect-time value (no early-teardown regression).
	reg.sessionExpiry = func(_ context.Context, _ string) (time.Time, bool, error) {
		return time.Time{}, false, errors.New("db unavailable")
	}
	assert.Equal(t, DeadlineAt(connectTime), reg.CurrentCredentialExpiry(context.Background(), sessionUser),
		"a transient lookup failure degrades to the connect-time value")

	// A cache HIT short-circuits the DB fallback entirely.
	reg.sessionExpiry = func(_ context.Context, _ string) (time.Time, bool, error) {
		t.Fatal("cache hit must not consult the DB fallback")
		return time.Time{}, false, nil
	}
	reg.state.sessions.Store("sess-1", cachedSession{
		user: &UserInfo{ID: userid.MustNew("u"), Credential: SessionCredential("sess-1"), CredentialExpiresAt: DeadlineAt(slid)},
	})
	assert.Equal(t, DeadlineAt(slid), reg.CurrentCredentialExpiry(context.Background(), sessionUser))
}

// A bearer rotation extends the credential's deadline but evicts (rather than
// updates) the bearer cache, so CurrentCredentialExpiry reads the extension from
// the recorded per-bearer expiry -- otherwise a channel opening in the window
// between validation and channel indexing would arm at the stale connect-time
// deadline and tear a still-valid channel down early.
func TestAuthContextRegistry_CurrentCredentialExpiryReadsRotatedBearerDeadline(t *testing.T) {
	reg := &AuthContextRegistry{state: &authState{}}
	e0 := time.Now().Add(10 * time.Minute).UTC()
	e1 := time.Now().Add(40 * time.Minute).UTC()
	ref := NewBearerRef(BearerKindAPI, "tok-1")

	// Validated at e0; a rotation raced the channel open and extended to e1.
	bearerUser := &UserInfo{ID: userid.MustNew("u-1"), Credential: APICredential("tok-1"), CredentialExpiresAt: DeadlineAt(e0)}
	assert.Equal(t, DeadlineAt(e0), reg.CurrentCredentialExpiry(context.Background(), bearerUser), "no recorded extension: caller's value")

	reg.RecordBearerExpiry(ref, DeadlineAt(e1))
	assert.Equal(t, DeadlineAt(e1), reg.CurrentCredentialExpiry(context.Background(), bearerUser),
		"must return the rotation-extended deadline, not the stale connect-time value")

	// Monotonic: a later, earlier-deadline record must not shrink the recorded
	// extension (a channel must never be armed earlier than a known deadline).
	reg.RecordBearerExpiry(ref, DeadlineAt(time.Now().Add(20*time.Minute).UTC()))
	assert.Equal(t, DeadlineAt(e1), reg.CurrentCredentialExpiry(context.Background(), bearerUser), "record is monotonic toward the more permissive deadline")

	// A zero (never-expires) rotation supersedes any finite recorded deadline.
	reg.RecordBearerExpiry(ref, NeverExpires())
	assert.True(t, reg.CurrentCredentialExpiry(context.Background(), bearerUser).IsNever(), "never-expires rotation must win")

	// Evicting the bearer (revocation) drops the recorded extension.
	reg.EvictBearer(ref)
	assert.Equal(t, DeadlineAt(e0), reg.CurrentCredentialExpiry(context.Background(), bearerUser), "eviction clears the recorded extension")
}

// RecordBearerExpiry's monotonic guarantee must hold under concurrent writers,
// not only when callers happen to be serialized by the DB rotation CAS. The
// load-merge-store runs as a compare-and-swap loop, so a racing writer's
// less-permissive deadline can never clobber a more-permissive one recorded in
// the gap. Hammer many goroutines recording a spread of deadlines for one ref
// and assert the most-permissive (latest) wins. Run under -race.
func TestAuthContextRegistry_RecordBearerExpiryMonotonicUnderConcurrency(t *testing.T) {
	reg := &AuthContextRegistry{state: &authState{}}
	ref := NewBearerRef(BearerKindAPI, "tok-concurrent")
	base := time.Now().Truncate(time.Second)
	const writers = 64
	latest := base.Add(time.Duration(writers) * time.Minute)

	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		// Each writer records a distinct deadline; base+writers*min is the max.
		go func(i int) {
			defer wg.Done()
			reg.RecordBearerExpiry(ref, DeadlineAt(base.Add(time.Duration(i+1)*time.Minute)))
		}(i)
	}
	wg.Wait()

	got, ok := reg.state.bearerExpiries.Load(ref)
	require.True(t, ok)
	assert.Equal(t, DeadlineAt(latest), got.(CredentialDeadline),
		"the most-permissive deadline must survive concurrent records")
}

// The periodic sweep drops recorded bearer extensions whose deadline has passed
// but retains a never-expires (zero) entry, which cannot be aged out by time.
func TestAuthInterceptor_SweepsExpiredBearerExpiries(t *testing.T) {
	a := &authInterceptor{state: &authState{}}
	reg := &AuthContextRegistry{state: a.state}
	pastRef := NewBearerRef(BearerKindAPI, "tok-past")
	liveRef := NewBearerRef(BearerKindAPI, "tok-live")
	neverRef := NewBearerRef(BearerKindAPI, "tok-never")

	a.state.bearerExpiries.Store(pastRef, DeadlineAt(time.Now().Add(-time.Minute)))
	reg.RecordBearerExpiry(liveRef, DeadlineAt(time.Now().Add(time.Hour)))
	reg.RecordBearerExpiry(neverRef, NeverExpires())

	a.sweepCachesOnce()

	_, ok := a.state.bearerExpiries.Load(pastRef)
	assert.False(t, ok, "a past-deadline bearer extension must be swept")
	_, ok = a.state.bearerExpiries.Load(liveRef)
	assert.True(t, ok, "a future-deadline bearer extension must be retained")
	_, ok = a.state.bearerExpiries.Load(neverRef)
	assert.True(t, ok, "a never-expires bearer extension must be retained")
}

func TestAuthInterceptor_SweepsBearerAndRevocationCaches(t *testing.T) {
	a := &authInterceptor{state: &authState{}}
	old := time.Now().Add(-SessionDuration - time.Minute)

	a.state.bearers.Store(bearerCacheKey(BearerKindAPI, "tok-1", []byte("secret-hash")), cachedSession{
		user:     &UserInfo{ID: userid.MustNew("u-1")},
		cachedAt: time.Now().Add(-2 * sessionCacheTTL),
	})
	a.state.sessionRevocations.Store("sess-1", revocationMark{generation: 1, recordedAt: old})
	a.state.userRevocations.Store("u-1", userRevocationMark{revocationMark: revocationMark{generation: 1, recordedAt: old}})
	a.state.userInvalidations.Store("u-1", revocationMark{generation: 1, recordedAt: old})
	a.state.bearerRevocations.Store(BearerRef{kind: BearerKindAPI, tokenID: "tok-1"}, revocationMark{generation: 1, recordedAt: old})
	a.state.bearerInvalidations.Store(BearerRef{kind: BearerKindAPI, tokenID: "tok-2"}, revocationMark{generation: 1, recordedAt: old})

	a.sweepCachesOnce()

	_, ok := a.state.bearers.Load(bearerCacheKey(BearerKindAPI, "tok-1", []byte("secret-hash")))
	assert.False(t, ok, "stale bearer cache entries must be swept")
	_, ok = a.state.sessionRevocations.Load("sess-1")
	assert.False(t, ok, "old session revocation marks must be swept")
	_, ok = a.state.userRevocations.Load("u-1")
	assert.False(t, ok, "old user revocation marks must be swept")
	_, ok = a.state.userInvalidations.Load("u-1")
	assert.False(t, ok, "old user invalidation marks must be swept")
	_, ok = a.state.bearerRevocations.Load(BearerRef{kind: BearerKindAPI, tokenID: "tok-1"})
	assert.False(t, ok, "old bearer revocation marks must be swept")
	_, ok = a.state.bearerInvalidations.Load(BearerRef{kind: BearerKindAPI, tokenID: "tok-2"})
	assert.False(t, ok, "old bearer invalidation marks must be swept")
}

func TestStaleCacheSweepDoesNotDeleteFreshReplacements(t *testing.T) {
	caches := &credentialCaches{}
	oldTime := time.Now().Add(-SessionDuration)
	freshTime := time.Now()
	caches.lastTouch.Store("session", freshTime)
	caches.deleteStaleLastTouch("session", oldTime)
	gotTime, ok := caches.lastTouch.Load("session")
	require.True(t, ok)
	assert.Equal(t, freshTime, gotTime)

	oldSession := cachedSession{user: &UserInfo{ID: userid.MustNew("user")}, cachedAt: oldTime}
	freshSession := cachedSession{user: &UserInfo{ID: userid.MustNew("user")}, cachedAt: freshTime}
	caches.sessions.Store("session", freshSession)
	indexSyncMap(&caches.userSessions, "user", "session")
	caches.deleteStaleSession("session", oldSession)
	gotSession, ok := caches.sessions.Load("session")
	require.True(t, ok)
	assert.Equal(t, freshSession, gotSession)

	key := bearerCacheKey(BearerKindAPI, "token", []byte("secret"))
	caches.bearers.Store(key, freshSession)
	indexSyncMap(&caches.bearerKeysByToken, BearerRef{kind: BearerKindAPI, tokenID: "token"}, key)
	indexSyncMap(&caches.userBearerKeys, "user", key)
	caches.deleteStaleBearer(key, oldSession)
	gotBearer, ok := caches.bearers.Load(key)
	require.True(t, ok)
	assert.Equal(t, freshSession, gotBearer)
}

func TestStaleCredentialCacheSweepSerializesWithValidation(t *testing.T) {
	state := &authState{}
	a := &authInterceptor{state: state}
	stale := cachedSession{
		user:     &UserInfo{ID: userid.MustNew("user")},
		cachedAt: time.Now().Add(-2 * sessionCacheTTL),
	}
	state.lastTouch.Store("touch", time.Now().Add(-2*SessionDuration))
	state.sessions.Store("session", stale)
	indexSyncMap(&state.userSessions, "user", "session")

	state.revocationMu.Lock()
	locked := true
	defer func() {
		if locked {
			state.revocationMu.Unlock()
		}
	}()
	done := make(chan struct{})
	go func() {
		a.sweepCachesOnce()
		close(done)
	}()
	require.Eventually(t, func() bool {
		_, exists := state.lastTouch.Load("touch")
		return !exists
	}, time.Second, time.Millisecond)
	require.Never(t, func() bool {
		_, exists := state.sessions.Load("session")
		return !exists
	}, 50*time.Millisecond, time.Millisecond, "cache and index cleanup must wait for validation publication")

	state.revocationMu.Unlock()
	locked = false
	<-done
	_, exists := state.sessions.Load("session")
	require.False(t, exists)
}
