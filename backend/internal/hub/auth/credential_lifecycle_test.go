package auth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingCredentialCloser struct {
	sessions            []string
	bearers             []BearerRef
	users               []userRevocationCall
	rescheduledBearers  []bearerRescheduleCall
	rescheduledSessions []sessionRescheduleCall
	restampedSessions   []sessionRestampCall
}

type userRevocationCall struct {
	userID     string
	generation int64
}

type bearerRescheduleCall struct {
	key       BearerRef
	newExpiry CredentialDeadline
}

type sessionRescheduleCall struct {
	sessionID string
	newExpiry CredentialDeadline
}

type sessionRestampCall struct {
	sessionID  string
	generation int64
}

func (c *recordingCredentialCloser) CloseChannelsBySession(sessionID string) int {
	c.sessions = append(c.sessions, sessionID)
	return 1
}

func (c *recordingCredentialCloser) CloseChannelsByBearer(ref BearerRef) int {
	c.bearers = append(c.bearers, ref)
	return 1
}

func (c *recordingCredentialCloser) CloseChannelsByUserRevocation(userID string, generation int64) int {
	c.users = append(c.users, userRevocationCall{userID: userID, generation: generation})
	return 1
}

func (c *recordingCredentialCloser) RestampSessionGeneration(sessionID string, generation int64) {
	c.restampedSessions = append(c.restampedSessions, sessionRestampCall{sessionID: sessionID, generation: generation})
}

// recordingCredentialCloser also stands in as the ChannelExpiryRescheduler so
// one fake records both the teardown and the expiry-extension effects.
func (c *recordingCredentialCloser) RescheduleExpiryByBearer(ref BearerRef, newExpiry CredentialDeadline) {
	c.rescheduledBearers = append(c.rescheduledBearers, bearerRescheduleCall{key: ref, newExpiry: newExpiry})
}

func (c *recordingCredentialCloser) RescheduleExpiryBySession(sessionID string, newExpiry CredentialDeadline) {
	c.rescheduledSessions = append(c.rescheduledSessions, sessionRescheduleCall{sessionID: sessionID, newExpiry: newExpiry})
}

func TestCredentialLifecycleEffectsEventMatrix(t *testing.T) {
	registry := &AuthContextRegistry{state: &authState{}}
	closer := &recordingCredentialCloser{}
	effects := NewCredentialLifecycleEffects(registry, closer, closer)

	sessionCtx, sessionCancel := context.WithCancel(context.Background())
	_, ok := registry.RegisterAuthenticatedLease(context.Background(), &UserInfo{
		ID: "session-user", Credential: SessionCredential("session-1"),
	}, sessionCancel)
	require.True(t, ok)
	effects.SessionRevoked("session-1")
	assert.ErrorIs(t, sessionCtx.Err(), context.Canceled)
	assert.Equal(t, []string{"session-1"}, closer.sessions)

	bearerCtx, bearerCancel := context.WithCancel(context.Background())
	_, ok = registry.RegisterAuthenticatedLease(context.Background(), &UserInfo{
		ID: "bearer-user", Credential: APICredential("api-1"),
	}, bearerCancel)
	require.True(t, ok)
	effects.BearerRevoked(BearerKindAPI, "api-1")
	assert.ErrorIs(t, bearerCtx.Err(), context.Canceled)
	assert.Equal(t, []BearerRef{{kind: BearerKindAPI, tokenID: "api-1"}}, closer.bearers)

	rotationCtx, rotationCancel := context.WithCancel(context.Background())
	rotationExpiry := time.Now().Add(30 * time.Minute)
	rotationRelease, ok := registry.RegisterAuthenticatedLease(context.Background(), &UserInfo{
		ID: "rotation-user", Credential: APICredential("api-rotation"),
	}, rotationCancel)
	require.True(t, ok)
	t.Cleanup(rotationRelease)
	effects.BearerRotatedExtending(BearerKindAPI, "api-rotation", rotationExpiry)
	assert.NoError(t, rotationCtx.Err(), "rotation must preserve authenticated leases")
	assert.Len(t, closer.bearers, 1, "rotation must not close bearer channels")
	assert.Equal(t, []bearerRescheduleCall{{
		key: BearerRef{kind: BearerKindAPI, tokenID: "api-rotation"}, newExpiry: DeadlineAt(rotationExpiry),
	}}, closer.rescheduledBearers, "rotation must extend the bearer's channel expiry")

	oldCtx, oldCancel := context.WithCancel(context.Background())
	_, ok = registry.RegisterAuthenticatedLease(context.Background(), &UserInfo{
		ID: "user", Credential: SessionCredential("old"), UserAuthGeneration: 4,
	}, oldCancel)
	require.True(t, ok)
	currentCtx, currentCancel := context.WithCancel(context.Background())
	currentRelease, ok := registry.RegisterAuthenticatedLease(context.Background(), &UserInfo{
		ID: "user", Credential: SessionCredential("current"), UserAuthGeneration: 5,
	}, currentCancel)
	require.True(t, ok)
	t.Cleanup(currentRelease)
	effects.UserRevoked("user", 5)
	assert.ErrorIs(t, oldCtx.Err(), context.Canceled)
	assert.NoError(t, currentCtx.Err(), "current-generation work must survive")
	assert.Equal(t, []userRevocationCall{{userID: "user", generation: 5}}, closer.users)
}

func TestCredentialLifecycleEffectsRejectsIncompleteEventKeys(t *testing.T) {
	closer := &recordingCredentialCloser{}
	effects := NewCredentialLifecycleEffects(nil, closer, closer)

	effects.SessionRevoked("")
	effects.BearerRevoked(0, "token")
	effects.BearerRevoked(BearerKindAPI, "")
	effects.BearerRotatedExtending(0, "token", time.Now().Add(time.Minute)) // invalid kind: no effect
	effects.BearerRotatedCacheOnly(BearerKindAPI, "token")                  // cache-only: never reschedules
	effects.BearerRotatedCacheOnly(0, "token")                              // invalid kind: no effect
	effects.BearerRotatedCacheOnly(BearerKindAPI, "")                       // empty token id: no effect
	effects.UserRevoked("", 1)
	// UserRevoked("user", 0) is intentionally NOT here: a non-positive generation
	// with a real userID is no longer an "incomplete key" -- it fails SAFE and
	// drops every current credential (see TestUserRevokedNonPositiveGenerationFailsSafe).
	effects.preserveSession("", 5)
	effects.preserveSession("session", 0)
	effects.UserInfoInvalidated("")

	assert.Empty(t, closer.sessions)
	assert.Empty(t, closer.bearers)
	assert.Empty(t, closer.users)
	assert.Empty(t, closer.rescheduledBearers, "zero/invalid rotations must not reschedule channels")
	assert.Empty(t, closer.restampedSessions, "invalid restamps must be ignored")
}

func TestPreserveSessionRestampsChannelGeneration(t *testing.T) {
	closer := &recordingCredentialCloser{}
	effects := NewCredentialLifecycleEffects(&AuthContextRegistry{state: &authState{}}, closer, closer)

	effects.preserveSession("session-1", 7)

	assert.Equal(t, []sessionRestampCall{{sessionID: "session-1", generation: 7}}, closer.restampedSessions)
}

// A password change keeps the acting session alive but bumps the user
// generation. RevokeUserPreservingSession must restamp the acting session's
// authenticated LEASES and its channels to the new generation before the
// user-wide revocation -- which cancels leases and closes channels below the new
// generation -- so it does not tear down the acting user's own live WebSocket
// connections. A lease belonging to a different session of the same user is
// still torn down.
func TestRevokeUserPreservingSessionSparesActingSession(t *testing.T) {
	registry := &AuthContextRegistry{state: &authState{}}
	closer := &recordingCredentialCloser{}
	effects := NewCredentialLifecycleEffects(registry, closer, closer)

	actingCtx, actingCancel := context.WithCancel(context.Background())
	actingRelease, ok := registry.RegisterAuthenticatedLease(context.Background(), &UserInfo{
		ID: "user", Credential: SessionCredential("acting"), UserAuthGeneration: 4,
	}, actingCancel)
	require.True(t, ok)
	t.Cleanup(actingRelease)

	otherCtx, otherCancel := context.WithCancel(context.Background())
	_, ok = registry.RegisterAuthenticatedLease(context.Background(), &UserInfo{
		ID: "user", Credential: SessionCredential("other"), UserAuthGeneration: 4,
	}, otherCancel)
	require.True(t, ok)

	// One atomic call restamps the acting session, then revokes user-wide.
	effects.RevokeUserPreservingSession("user", "acting", 5)

	assert.NoError(t, actingCtx.Err(), "the acting session's own lease must survive its own password change")
	assert.ErrorIs(t, otherCtx.Err(), context.Canceled, "a different session's lease must still be revoked")
	assert.Equal(t, []sessionRestampCall{{sessionID: "acting", generation: 5}}, closer.restampedSessions,
		"the acting session's channels must be restamped before the user-wide revocation")
	assert.Equal(t, []userRevocationCall{{userID: "user", generation: 5}}, closer.users,
		"the user-wide channel revocation must still run")
}

// A bearer refresh reschedules the lease's expiry and bumps its epoch, so a
// stale timer that already fired must not tear down the now-extended lease.
func TestRenewBearerLeasesSupersedesStaleExpiryTimer(t *testing.T) {
	registry := &AuthContextRegistry{state: &authState{}}
	ctx, cancel := context.WithCancel(context.Background())
	release, ok := registry.RegisterAuthenticatedLease(context.Background(), &UserInfo{
		ID: "u", Credential: APICredential("api-1"), CredentialExpiresAt: DeadlineAt(time.Now().Add(time.Hour)),
	}, cancel)
	require.True(t, ok)
	t.Cleanup(release)

	ref := NewBearerRef(BearerKindAPI, "api-1")
	registry.state.revocationMu.Lock()
	var id uint64
	for lid := range registry.state.leasesByBearer[ref] {
		id = lid
	}
	staleEpoch := registry.state.leases[id].expiryEpoch
	registry.state.revocationMu.Unlock()

	newExpiry := time.Now().Add(2 * time.Hour)
	registry.RenewBearerLeases(ref, DeadlineAt(newExpiry))

	registry.state.revocationMu.Lock()
	assert.Equal(t, DeadlineAt(newExpiry), registry.state.leases[id].user.CredentialExpiresAt, "reschedule must record the extended expiry")
	currentEpoch := registry.state.leases[id].expiryEpoch
	registry.state.revocationMu.Unlock()
	assert.Greater(t, currentEpoch, staleEpoch, "reschedule must bump the expiry epoch")

	// A stale-epoch timer firing must NOT cancel the extended lease.
	registry.state.expireLease(id, staleEpoch)
	assert.NoError(t, ctx.Err(), "stale-epoch expiry must not cancel a rescheduled lease")

	// The current-epoch timer still tears it down.
	registry.state.expireLease(id, currentEpoch)
	assert.ErrorIs(t, ctx.Err(), context.Canceled)
}

// A bearer rotation that extends to a zero (never-expires) deadline must clear
// the lease's teardown timer, matching rescheduleLeaseLocked and its channel-side
// twin RescheduleExpiryByBearer. The old newExpiry.IsZero() early-return left the
// finite connect-time timer armed, so the lease was torn down at the stale
// deadline while the channel side treated zero as never-expires -- the two sides
// disagreed on what a zero rotation deadline meant.
func TestRenewBearerLeasesZeroExpiryClearsDeadline(t *testing.T) {
	registry := &AuthContextRegistry{state: &authState{}}
	ctx, cancel := context.WithCancel(context.Background())
	// A finite deadline arms a teardown timer at registration.
	release, ok := registry.RegisterAuthenticatedLease(context.Background(), &UserInfo{
		ID: "u", Credential: APICredential("api-1"), CredentialExpiresAt: DeadlineAt(time.Now().Add(time.Hour)),
	}, cancel)
	require.True(t, ok)
	t.Cleanup(release)

	ref := NewBearerRef(BearerKindAPI, "api-1")
	registry.state.revocationMu.Lock()
	var id uint64
	for lid := range registry.state.leasesByBearer[ref] {
		id = lid
	}
	require.NotNil(t, registry.state.leases[id].timer, "a finite deadline must arm a teardown timer")
	registry.state.revocationMu.Unlock()

	// Rotate to a never-expires (zero) deadline.
	registry.RenewBearerLeases(ref, NeverExpires())

	registry.state.revocationMu.Lock()
	lease := registry.state.leases[id]
	zeroed := lease.user.CredentialExpiresAt.IsNever()
	timerNil := lease.timer == nil
	registry.state.revocationMu.Unlock()
	assert.True(t, zeroed, "a zero rotation deadline must clear the recorded lease expiry")
	assert.True(t, timerNil, "a zero rotation deadline must disarm the teardown timer (never-expires)")
	assert.NoError(t, ctx.Err(), "a never-expires lease must not be canceled")
}

func TestCredentialLifecycleEffectsUserInfoInvalidationDoesNotCloseChannels(t *testing.T) {
	registry := &AuthContextRegistry{state: &authState{}}
	closer := &recordingCredentialCloser{}
	effects := NewCredentialLifecycleEffects(registry, closer, closer)
	key := bearerCacheKey(BearerKindAPI, "token", []byte("secret"))
	cached := cachedSession{user: &UserInfo{ID: "user", Credential: APICredential("token")}}
	registry.state.bearers.Store(key, cached)
	registry.state.indexBearerCacheEntry(key, cached)

	effects.UserInfoInvalidated("user")

	_, exists := registry.state.bearers.Load(key)
	assert.False(t, exists)
	assert.Empty(t, closer.sessions)
	assert.Empty(t, closer.bearers)
	assert.Empty(t, closer.users)
}

// A session slide that lands after the request was validated but before the
// lease is indexed advances the cached session deadline in place (under
// revocationMu). A lease registering in that window must be armed at -- and its
// liveness checked against -- the slid deadline, not the stale connect-time one,
// mirroring the channel path's CurrentCredentialExpiry guard. Without it a
// still-valid session's WebSocket is rejected (near-expiry connect-time) or torn
// down early at the stale deadline the renew sweep could not reach.
func TestRegisterAuthenticatedLeaseAdoptsRacedSessionSlide(t *testing.T) {
	registry := &AuthContextRegistry{state: &authState{}}
	now := time.Now()
	// The session cache reflects a slide to now+1h...
	slid := DeadlineAt(now.Add(time.Hour))
	registry.state.sessions.Store("sess-1", cachedSession{
		user:     &UserInfo{ID: "u", Credential: SessionCredential("sess-1"), CredentialExpiresAt: slid},
		cachedAt: now,
	})

	// ...but the request captured a now-stale connect-time deadline in the past.
	ctx, cancel := context.WithCancel(context.Background())
	release, ok := registry.RegisterAuthenticatedLease(context.Background(), &UserInfo{
		ID: "u", Credential: SessionCredential("sess-1"), CredentialExpiresAt: DeadlineAt(now.Add(-time.Second)),
	}, cancel)
	require.True(t, ok, "a lease whose session was slid forward must not be rejected at the stale connect-time deadline")
	t.Cleanup(release)
	assert.NoError(t, ctx.Err(), "the lease must not be canceled")

	registry.state.revocationMu.Lock()
	var id uint64
	for lid := range registry.state.leasesBySession["sess-1"] {
		id = lid
	}
	got := registry.state.leases[id].user.CredentialExpiresAt
	registry.state.revocationMu.Unlock()
	assert.Equal(t, slid, got, "the lease must carry the slid deadline, not the connect-time one")
}

// The cache-MISS twin of the raced-slide test: when the session's cache row was
// evicted after a slide (e.g. by a concurrent user_info change), the lease guard
// must fall back to the AUTHORITATIVE DB expiry via CurrentCredentialExpiry rather
// than degrade to the stale connect-time deadline -- the WebSocket-side twin of
// the channel guard's DB fallback. Without it the still-valid socket is rejected
// (or torn down) at the connect-time deadline.
func TestRegisterAuthenticatedLeaseFallsBackToDBExpiryOnCacheMiss(t *testing.T) {
	registry := &AuthContextRegistry{state: &authState{}}
	now := time.Now()
	slid := now.Add(time.Hour)
	// No session cache row (evicted), but the DB reports the slid expiry.
	registry.sessionExpiry = func(_ context.Context, sessionID string) (time.Time, bool, error) {
		if sessionID == "sess-1" {
			return slid, true, nil
		}
		return time.Time{}, false, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	release, ok := registry.RegisterAuthenticatedLease(context.Background(), &UserInfo{
		ID: "u", Credential: SessionCredential("sess-1"), CredentialExpiresAt: DeadlineAt(now.Add(-time.Second)),
	}, cancel)
	require.True(t, ok, "a lease whose cache row was evicted after a slide must adopt the DB expiry, not be rejected at the stale connect-time deadline")
	t.Cleanup(release)
	assert.NoError(t, ctx.Err(), "the lease must not be canceled")

	registry.state.revocationMu.Lock()
	var id uint64
	for lid := range registry.state.leasesBySession["sess-1"] {
		id = lid
	}
	got := registry.state.leases[id].user.CredentialExpiresAt
	registry.state.revocationMu.Unlock()
	assert.Equal(t, DeadlineAt(slid), got, "the lease must carry the DB-fallback deadline, not the connect-time one")
}

// The bearer analogue: a rotation records an extended per-bearer deadline
// (RecordBearerExpiry) before a lease racing the rotation is indexed. That lease
// must be armed at the extended deadline via the recorded extension, not the
// stale connect-time value.
func TestRegisterAuthenticatedLeaseAdoptsRacedBearerRotation(t *testing.T) {
	registry := &AuthContextRegistry{state: &authState{}}
	now := time.Now()
	ref := NewBearerRef(BearerKindAPI, "api-1")
	extended := DeadlineAt(now.Add(time.Hour))
	registry.RecordBearerExpiry(ref, extended)

	ctx, cancel := context.WithCancel(context.Background())
	release, ok := registry.RegisterAuthenticatedLease(context.Background(), &UserInfo{
		ID: "u", Credential: APICredential("api-1"), CredentialExpiresAt: DeadlineAt(now.Add(-time.Second)),
	}, cancel)
	require.True(t, ok, "a lease whose bearer was rotated-and-extended must not be rejected at the stale deadline")
	t.Cleanup(release)
	assert.NoError(t, ctx.Err(), "the lease must not be canceled")

	registry.state.revocationMu.Lock()
	var id uint64
	for lid := range registry.state.leasesByBearer[ref] {
		id = lid
	}
	got := registry.state.leases[id].user.CredentialExpiresAt
	registry.state.revocationMu.Unlock()
	assert.Equal(t, extended, got, "the lease must carry the extended rotation deadline")
}

// A user_tokens revocation whose committed generation is unknown (non-positive)
// must fail SAFE -- drop every current credential -- rather than fail open and
// silently lose the revocation. UserRevoked previously returned early on
// generation <= 0, leaving the user's leases and channels alive.
func TestUserRevokedNonPositiveGenerationFailsSafe(t *testing.T) {
	registry := &AuthContextRegistry{state: &authState{}}
	closer := &recordingCredentialCloser{}
	effects := NewCredentialLifecycleEffects(registry, closer, closer)

	// A live lease for the user, minted at a positive connect-time generation.
	ctx, cancel := context.WithCancel(context.Background())
	release, ok := registry.RegisterAuthenticatedLease(context.Background(), &UserInfo{
		ID: "user", Credential: SessionCredential("s1"), UserAuthGeneration: 3,
	}, cancel)
	require.True(t, ok)
	t.Cleanup(release)

	// Generation 0: the committed generation is unknown.
	effects.UserRevoked("user", 0)

	assert.ErrorIs(t, ctx.Err(), context.Canceled,
		"a non-positive-generation user revocation must still cancel the user's leases (fail safe)")
	assert.Equal(t, []userRevocationCall{{userID: "user", generation: 0}}, closer.users,
		"the channel teardown must be invoked, not skipped, for a non-positive generation")
}
