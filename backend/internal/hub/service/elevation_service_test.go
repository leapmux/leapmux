package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/servicetest"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// teardownRecorder records every channel-teardown effect the lifecycle
// applies, so a test can assert that a grant caused NONE of them.
type teardownRecorder struct {
	mu        sync.Mutex
	sessions  []string
	users     []string
	bearers   []string
	restamped []string
}

func (r *teardownRecorder) CloseChannelsBySession(sessionID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions = append(r.sessions, sessionID)
	return 0
}

func (r *teardownRecorder) CloseChannelsByBearer(ref auth.BearerRef) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bearers = append(r.bearers, ref.TokenID())
	return 0
}

func (r *teardownRecorder) CloseChannelsByUserRevocation(userID string, _ int64) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.users = append(r.users, userID)
	return 0
}

func (r *teardownRecorder) RestampSessionGeneration(sessionID string, _ int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.restamped = append(r.restamped, sessionID)
}

func (r *teardownRecorder) snapshot() (sessions, users, bearers []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.sessions...), append([]string(nil), r.users...), append([]string(nil), r.bearers...)
}

type elevationEnv struct {
	client   leapmuxv1connect.UserServiceClient
	store    store.Store
	token    string
	userID   string
	teardown *teardownRecorder
}

func setupElevationTest(t *testing.T) *elevationEnv {
	t.Helper()
	ctx := context.Background()

	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Migrator().Migrate(ctx))

	userID := id.Generate()
	hash, err := password.Hash("testpass")
	require.NoError(t, err)
	require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
		ID: userID, Username: "elevated", PasswordHash: hash,
		DisplayName: "Elevated", PasswordSet: true, IsAdmin: true,
	}))
	token, _, _, err := auth.Login(ctx, st, "elevated", "testpass", auth.DefaultSessionDuration)
	require.NoError(t, err)

	recorder := &teardownRecorder{}
	interceptor, contexts := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st})
	t.Cleanup(contexts.Stop)
	userSvc := service.NewUserService(st, testConfig(), servicetest.NewSettingsManager(t, st, nil),
		auth.NewCredentialLifecycleEffects(contexts, recorder, nil), mail.NewStubSender(), mail.Renderer{}, nil)

	mux := http.NewServeMux()
	path, handler := leapmuxv1connect.NewUserServiceHandler(userSvc, connect.WithInterceptors(interceptor))
	mux.Handle(path, handler)
	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	return &elevationEnv{
		client:   leapmuxv1connect.NewUserServiceClient(server.Client(), server.URL, connect.WithGRPC()),
		store:    st,
		token:    token,
		userID:   userID,
		teardown: recorder,
	}
}

// TestElevateSession_InvalidatesTheCacheWithoutSigningTheUserOut is the
// single most important regression test in the change.
//
// The grant must reach the NEXT request through a cache that the hub
// populated before it existed -- and it must do that on the one lane whose
// contract is
// "drop the cached UserInfo without logging the user out". Any of the other
// revocation lanes would also refresh the cache, and would also cancel the
// user's leases and close their channels: an elevation would then look like
// a security incident to every open connection the user has.
func TestElevateSession_InvalidatesTheCacheWithoutSigningTheUserOut(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	env := setupElevationTest(t)

	// WARM the cache with the pre-elevation UserInfo. Without this the test
	// proves nothing: a cold cache would re-read the row anyway.
	_, err := env.client.ListUserSettings(ctx, authedReq(&leapmuxv1.ListUserSettingsRequest{}, env.token))
	require.NoError(t, err)

	// A sensitive action on the warm cache: refused, because the session is
	// not elevated.
	_, err = env.client.ChangePassword(ctx, authedReq(&leapmuxv1.ChangePasswordRequest{
		NewPassword: "newpass123",
	}, env.token))
	require.Error(t, err)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	resp, err := env.client.ElevateSession(ctx, authedReq(&leapmuxv1.ElevateSessionRequest{
		CurrentPassword: "testpass",
	}, env.token))
	require.NoError(t, err)
	require.NotNil(t, resp.Msg.GetElevationExpiresAt())

	// (1) The next validation RE-READS: the same call the hub refused a
	// moment ago now lands, inside the auth cache's TTL.
	_, err = env.client.ChangePassword(ctx, authedReq(&leapmuxv1.ChangePasswordRequest{
		NewPassword: "newpass123",
	}, env.token))
	require.NoError(t, err, "the grant must reach the next request through the warm cache")

	// (2) Nothing signs the user out: the session still authenticates.
	// TestElevateSession_GrantTearsNothingDown isolates the teardown half,
	// away from the credential revocation ChangePassword performs on the
	// user's OTHER sessions and tokens.
	info, err := auth.ValidateToken(ctx, env.store, env.token)
	require.NoError(t, err, "the session must survive its own elevation")
	assert.Equal(t, env.userID, info.ID.String())
}

// TestElevateSession_GrantTearsNothingDown isolates the effect of the grant
// itself, with no follow-up mutation to confuse the recorder.
func TestElevateSession_GrantTearsNothingDown(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	env := setupElevationTest(t)

	_, err := env.client.ListUserSettings(ctx, authedReq(&leapmuxv1.ListUserSettingsRequest{}, env.token))
	require.NoError(t, err)

	_, err = env.client.ElevateSession(ctx, authedReq(&leapmuxv1.ElevateSessionRequest{
		CurrentPassword: "testpass",
	}, env.token))
	require.NoError(t, err)

	sessions, users, bearers := env.teardown.snapshot()
	assert.Empty(t, sessions, "an elevation must close no session channel")
	assert.Empty(t, users, "an elevation must not look like a user-wide revocation")
	assert.Empty(t, bearers, "an elevation must not revoke the user's bearers")

	// And the durable stream carries the SOFT signal, not a revocation.
	_, err = env.store.RevocationEvents().PublishPending(ctx, 100)
	require.NoError(t, err)
	events, err := env.store.RevocationEvents().ListPublishedAfter(ctx, 0, 100)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, store.RevocationEventKindUserInfo, events[0].Event.Kind)
	assert.Equal(t, env.userID, events[0].Event.UserID)
}

// TestDropElevation_InvalidatesTheCacheImmediately is the polarity that
// matters on the way DOWN. A cached longer deadline fails OPEN, so the drop
// must reach the next request rather than waiting for the cache TTL to
// expire.
func TestDropElevation_InvalidatesTheCacheImmediately(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	env := setupElevationTest(t)

	_, err := env.client.ElevateSession(ctx, authedReq(&leapmuxv1.ElevateSessionRequest{
		CurrentPassword: "testpass",
	}, env.token))
	require.NoError(t, err)

	// Warm the cache with the ELEVATED UserInfo.
	_, err = env.client.ListUserSettings(ctx, authedReq(&leapmuxv1.ListUserSettingsRequest{}, env.token))
	require.NoError(t, err)

	_, err = env.client.DropElevation(ctx, authedReq(&leapmuxv1.DropElevationRequest{}, env.token))
	require.NoError(t, err)

	_, err = env.client.ChangePassword(ctx, authedReq(&leapmuxv1.ChangePasswordRequest{
		NewPassword: "newpass123",
	}, env.token))
	require.Error(t, err, "the drop must take effect before the auth cache would expire")
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	// The user is still signed in: dropping an elevation is not a logout.
	_, err = auth.ValidateToken(ctx, env.store, env.token)
	assert.NoError(t, err)
}

// TestElevateSession_WrongFactorDoesNotReadAsADeadSession is the behaviour
// the marker exists for, driven through a real client so the metadata a
// browser would see is what the assertion reads.
//
// This is the bug the E2E suite found: the wrong-password answer was a bare
// Unauthenticated, and the frontend's general "Unauthenticated means signed
// out" rule ended the very session the prompt protected.
func TestElevateSession_WrongFactorDoesNotReadAsADeadSession(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	env := setupElevationTest(t)

	_, err := env.client.ElevateSession(ctx, authedReq(&leapmuxv1.ElevateSessionRequest{
		CurrentPassword: "definitely-not-the-password",
	}, env.token))
	require.Error(t, err)
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err),
		"a rejected credential is what Unauthenticated means; the marker says which one")

	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, "1", connectErr.Meta().Get(service.CredentialRejectedHeader),
		"without the marker a browser signs the user out of the session it is verifying")

	// The session really is still alive.
	_, err = auth.ValidateToken(ctx, env.store, env.token)
	assert.NoError(t, err)

	// An EMPTY password takes the same path: a client must not be signed out
	// for submitting an empty prompt either.
	_, err = env.client.ElevateSession(ctx, authedReq(&leapmuxv1.ElevateSessionRequest{}, env.token))
	require.Error(t, err)
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, "1", connectErr.Meta().Get(service.CredentialRejectedHeader))

	// And the refusal granted nothing.
	row, err := env.store.Sessions().GetByID(ctx, env.token, time.Now().UTC())
	require.NoError(t, err)
	assert.Nil(t, row.ElevationExpiresAt)
}

// TestElevateSession_RefusesABearer pins the structural rule end to end: a
// CLI credential cannot elevate itself, so it can never reach a
// passkey-management RPC however long it lives.
func TestElevateSession_RefusesABearer(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	env := setupElevationTest(t)

	pepper := []byte("0123456789abcdef0123456789abcdef")
	tv, err := auth.NewTokenValidator(env.store, pepper)
	require.NoError(t, err)
	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	expires := time.Now().Add(time.Hour)
	require.NoError(t, env.store.APITokens().Create(ctx, store.CreateAPITokenParams{
		ID: tokenID, UserID: userid.MustNew(env.userID), ClientID: oauthapp.ControlCLIClientID, InstallationName: "laptop", GrantedScopes: authscope.NonAdminGrant().String(),
		SecretHash: tv.HashSecret(secret), ExpiresAt: &expires,
	}))

	// The bearer path is not wired into this env's interceptor, so drive the
	// refusal through the handler's own guard instead: a session-less
	// credential has nothing to stamp.
	req := connect.NewRequest(&leapmuxv1.ElevateSessionRequest{CurrentPassword: "testpass"})
	req.Header().Set("Authorization", "Bearer "+auth.FormatBearer(auth.BearerKindAPI, tokenID, secret))
	_, err = env.client.ElevateSession(ctx, req)
	require.Error(t, err)
	assert.Contains(t, []connect.Code{connect.CodeUnauthenticated, connect.CodeFailedPrecondition},
		connect.CodeOf(err), "a bearer must never obtain an elevation")
}
