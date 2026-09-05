package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/util/userid"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/bootstrap"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/servicetest"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
)

// setupInterceptorTestServer creates an httptest server with the AuthService
// registered behind the auth interceptor. It returns a ConnectRPC client and
// the admin credentials (username "admin", password "admin123").
func setupInterceptorTestServer(t *testing.T) leapmuxv1connect.AuthServiceClient {
	t.Helper()

	st := hubtestutil.OpenTestStore(t)

	hubtestutil.CreateTestAdmin(t, st)

	mux := http.NewServeMux()
	interceptor, _ := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st})
	interceptors := connect.WithInterceptors(interceptor)
	authSvc := service.NewAuthService(servicetest.AuthServiceDeps(st, &config.Config{}, servicetest.NewSettingsManager(t, st, nil), auth.NewCredentialLifecycleEffects(nil, nil, nil)))
	path, handler := leapmuxv1connect.NewAuthServiceHandler(authSvc, interceptors)
	mux.Handle(path, handler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL)
}

// loginAdmin logs in with the bootstrapped admin credentials and returns the
// session token extracted from the Set-Cookie response header.
func loginAdmin(t *testing.T, client leapmuxv1connect.AuthServiceClient) string {
	t.Helper()

	resp, err := client.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
		Username: "admin",
		Password: "admin123",
	}))
	require.NoError(t, err)
	return hubtestutil.SessionFromCookie(t, resp.Header().Get("Set-Cookie"))
}

func TestInterceptor_PublicProcedure_NoTokenRequired(t *testing.T) {
	client := setupInterceptorTestServer(t)

	// GetSystemInfo is a public procedure -- it should succeed without a cookie.
	resp, err := client.GetSystemInfo(context.Background(), connect.NewRequest(&leapmuxv1.GetSystemInfoRequest{}))
	require.NoError(t, err)
	assert.NotNil(t, resp.Msg)
}

func TestInterceptor_PrivateProcedure_NoCookie(t *testing.T) {
	client := setupInterceptorTestServer(t)

	// GetCurrentUser is a private procedure. Calling it without a session cookie
	// should produce an Unauthenticated error.
	_, err := client.GetCurrentUser(context.Background(), connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestInterceptor_PrivateProcedure_ValidCookie(t *testing.T) {
	client := setupInterceptorTestServer(t)

	token := loginAdmin(t, client)

	// Use the valid session ID in a cookie to call a private endpoint.
	req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req.Header().Set("Cookie", auth.CookieName+"="+token)

	resp, err := client.GetCurrentUser(context.Background(), req)
	require.NoError(t, err)

	// The interceptor should have attached UserInfo to the context, allowing
	// GetCurrentUser to return the admin user.
	assert.Equal(t, "admin", resp.Msg.GetUser().GetUsername())
	assert.True(t, resp.Msg.GetUser().GetIsAdmin())
}

func TestInterceptor_SoloModeRejectsPasswordlessTCP(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)

	// Bootstrap in solo mode creates a user named "solo".
	err := bootstrap.Run(context.Background(), st, true)
	require.NoError(t, err)

	soloUser, err := auth.LoadSoloUser(context.Background(), st)
	require.NoError(t, err)

	mux := http.NewServeMux()
	interceptor, _ := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st, SoloUser: soloUser})
	interceptors := connect.WithInterceptors(interceptor)
	authSvc := service.NewAuthService(servicetest.AuthServiceDeps(st, &config.Config{SoloMode: true}, servicetest.NewSettingsManager(t, st, nil), auth.NewCredentialLifecycleEffects(nil, nil, nil)))
	path, handler := leapmuxv1connect.NewAuthServiceHandler(authSvc, interceptors)
	mux.Handle(path, handler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL)

	_, err = client.GetCurrentUser(context.Background(), connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// TestInterceptor_SoloMode_YieldsToAPresentedBearer pins the solo decision on
// the Connect surface: reaching the port authenticates the synthetic user, but
// a caller that PRESENTS an lmx_ bearer is asking to be its app, so the solo
// rung steps aside and the credential's grant binds.
//
// Without the yield, the narrow subtest below would pass -- the solo user is
// unscoped, so answering "you are the solo user" would hand a file:read
// credential every permission the account has, silently discarding the
// narrowing its owner accepted on a consent screen.
func TestInterceptor_SoloMode_YieldsToAPresentedBearer(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)

	err := bootstrap.Run(context.Background(), st, true)
	require.NoError(t, err)

	soloUser, err := auth.LoadSoloUser(context.Background(), st)
	require.NoError(t, err)

	pepper := []byte("0123456789abcdef0123456789abcdef")
	tv, err := auth.NewTokenValidator(st, pepper)
	require.NoError(t, err)

	mux := http.NewServeMux()
	interceptor, _ := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st, SoloUser: soloUser, TokenValidator: tv})
	interceptors := connect.WithInterceptors(interceptor)
	authSvc := service.NewAuthService(servicetest.AuthServiceDeps(st, &config.Config{SoloMode: true}, servicetest.NewSettingsManager(t, st, nil), auth.NewCredentialLifecycleEffects(nil, nil, nil)))
	path, handler := leapmuxv1connect.NewAuthServiceHandler(authSvc, interceptors)
	mux.Handle(path, handler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL)

	soloRow, err := st.Users().GetByUsername(context.Background(), "solo")
	require.NoError(t, err)

	// A credential whose grant does not reach GetCurrentUser (account:read).
	// file:read implies worker:read and nothing else, so the scope rung must
	// refuse it there.
	tokenID := newTestTokenID()
	secret := auth.MintAccessSecret()
	require.NoError(t, st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:               tokenID,
		UserID:           userid.MustNew(soloRow.ID),
		ClientID:         oauthapp.ControlCLIClientID,
		InstallationName: "test",
		GrantedScopes:    "file:read",
		SecretHash:       tv.HashSecret(secret),
	}))

	narrow := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	narrow.Header().Set("Authorization", "Bearer "+auth.FormatBearer(auth.BearerKindAPI, tokenID, secret))
	_, err = client.GetCurrentUser(context.Background(), narrow)
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err),
		"the solo rung must yield to a presented bearer, so its grant binds instead of the account's unscoped authority")

	// The yield is on the PRESENCE of a bearer, not its validity: a malformed
	// one must be refused by the bearer rung, not fall back to solo -- falling
	// back would make a broken credential stronger than a working one.
	broken := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	broken.Header().Set("Authorization", "Bearer lmx_only-one-piece")
	_, err = client.GetCurrentUser(context.Background(), broken)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err),
		"a malformed bearer must be refused, not answered as the solo user")
}

func TestInterceptor_PrivateProcedure_InvalidCookie(t *testing.T) {
	client := setupInterceptorTestServer(t)

	// Use a garbage session ID in a cookie on a private endpoint.
	req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req.Header().Set("Cookie", auth.CookieName+"=totally-invalid-token")

	_, err := client.GetCurrentUser(context.Background(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestInterceptor_BearerTokenNotAccepted(t *testing.T) {
	client := setupInterceptorTestServer(t)

	token := loginAdmin(t, client)

	// Try using Bearer token in Authorization header — should NOT be accepted.
	req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req.Header().Set("Authorization", "Bearer "+token)

	_, err := client.GetCurrentUser(context.Background(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// setupInterceptorTestServerWithBearerSupport wires the TokenValidator
// into the interceptor so `Authorization: Bearer lmx_*` requests are
// validated. Returns the client plus the store so the caller can mint
// API tokens for the test.
func setupInterceptorTestServerWithBearerSupport(t *testing.T) (leapmuxv1connect.AuthServiceClient, store.Store, *auth.TokenValidator) {
	t.Helper()

	st := hubtestutil.OpenTestStore(t)
	hubtestutil.CreateTestAdmin(t, st)

	pepper := []byte("0123456789abcdef0123456789abcdef")
	tv, err := auth.NewTokenValidator(st, pepper)
	require.NoError(t, err)

	mux := http.NewServeMux()
	interceptor, _ := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st, TokenValidator: tv})
	interceptors := connect.WithInterceptors(interceptor)
	authSvc := service.NewAuthService(servicetest.AuthServiceDeps(st, &config.Config{}, servicetest.NewSettingsManager(t, st, nil), auth.NewCredentialLifecycleEffects(nil, nil, nil)))
	path, handler := leapmuxv1connect.NewAuthServiceHandler(authSvc, interceptors)
	mux.Handle(path, handler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL), st, tv
}

// adminUserID looks up the bootstrap admin user's id so tests can mint
// tokens scoped to it.
func adminUserID(t *testing.T, st store.Store) string {
	t.Helper()
	u, err := st.Users().GetByUsername(context.Background(), "admin")
	require.NoError(t, err)
	return u.ID
}

func TestInterceptor_LeapMuxBearer_AcceptsValidToken(t *testing.T) {
	client, st, tv := setupInterceptorTestServerWithBearerSupport(t)
	userID := adminUserID(t, st)

	tokenID := newTestTokenID()
	secret := auth.MintAccessSecret()
	require.NoError(t, st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:               tokenID,
		UserID:           userid.MustNew(userID),
		ClientID:         oauthapp.ControlCLIClientID,
		InstallationName: "test",
		GrantedScopes:    authscope.NonAdminGrant().String(),
		SecretHash:       tv.HashSecret(secret),
	}))

	req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req.Header().Set("Authorization", "Bearer "+auth.FormatBearer(auth.BearerKindAPI, tokenID, secret))

	resp, err := client.GetCurrentUser(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "admin", resp.Msg.GetUser().GetUsername())
}

func TestInterceptor_LeapMuxBearer_RejectsWrongSecretAfterCacheWarm(t *testing.T) {
	client, st, tv := setupInterceptorTestServerWithBearerSupport(t)
	userID := adminUserID(t, st)

	tokenID := newTestTokenID()
	secret := auth.MintAccessSecret()
	require.NoError(t, st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:               tokenID,
		UserID:           userid.MustNew(userID),
		ClientID:         oauthapp.ControlCLIClientID,
		InstallationName: "test",
		GrantedScopes:    authscope.NonAdminGrant().String(),
		SecretHash:       tv.HashSecret(secret),
	}))

	validReq := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	validReq.Header().Set("Authorization", "Bearer "+auth.FormatBearer(auth.BearerKindAPI, tokenID, secret))
	_, err := client.GetCurrentUser(context.Background(), validReq)
	require.NoError(t, err)

	wrongSecretReq := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	wrongSecretReq.Header().Set("Authorization", "Bearer "+auth.FormatBearer(auth.BearerKindAPI, tokenID, "wrong-secret"))
	_, err = client.GetCurrentUser(context.Background(), wrongSecretReq)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestInterceptor_LeapMuxBearer_RejectsRevoked(t *testing.T) {
	client, st, tv := setupInterceptorTestServerWithBearerSupport(t)
	userID := adminUserID(t, st)

	tokenID := newTestTokenID()
	secret := auth.MintAccessSecret()
	require.NoError(t, st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:               tokenID,
		UserID:           userid.MustNew(userID),
		ClientID:         oauthapp.ControlCLIClientID,
		InstallationName: "test",
		GrantedScopes:    authscope.NonAdminGrant().String(),
		SecretHash:       tv.HashSecret(secret),
	}))
	_, err := st.APITokens().Revoke(context.Background(), tokenID)
	require.NoError(t, err)

	req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req.Header().Set("Authorization", "Bearer "+auth.FormatBearer(auth.BearerKindAPI, tokenID, secret))

	_, err = client.GetCurrentUser(context.Background(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestInterceptor_LeapMuxBearer_RejectsMalformed(t *testing.T) {
	client, _, _ := setupInterceptorTestServerWithBearerSupport(t)

	req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req.Header().Set("Authorization", "Bearer lmx_only-one-piece")

	_, err := client.GetCurrentUser(context.Background(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestInterceptor_LeapMuxBearer_RejectsUnknownTokenID(t *testing.T) {
	client, _, _ := setupInterceptorTestServerWithBearerSupport(t)

	req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req.Header().Set("Authorization", "Bearer "+auth.FormatBearer(auth.BearerKindAPI, newTestTokenID(), "any"))

	_, err := client.GetCurrentUser(context.Background(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// TestInterceptor_LeapMuxBearer_CacheEvictedOnRevoke pins down the
// "revocation is immediate" contract from the plan. The cache TTL is
// 30s; without explicit eviction, a revoked token would keep working
// for up to 30s after the admin clicks Revoke. AuthContextRegistry.EvictBearer
// must purge the in-memory cache so the next request hits the DB and
// observes the revoked_at column.
func TestInterceptor_LeapMuxBearer_CacheEvictedOnRevoke(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	hubtestutil.CreateTestAdmin(t, st)

	pepper := []byte("0123456789abcdef0123456789abcdef")
	tv, err := auth.NewTokenValidator(st, pepper)
	require.NoError(t, err)

	mux := http.NewServeMux()
	interceptor, sc := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st, TokenValidator: tv})
	t.Cleanup(sc.Stop)
	interceptors := connect.WithInterceptors(interceptor)
	authSvc := service.NewAuthService(servicetest.AuthServiceDeps(st, &config.Config{}, servicetest.NewSettingsManager(t, st, nil), auth.NewCredentialLifecycleEffects(sc, nil, nil)))
	path, handler := leapmuxv1connect.NewAuthServiceHandler(authSvc, interceptors)
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL)

	userID := adminUserID(t, st)
	tokenID := newTestTokenID()
	secret := auth.MintAccessSecret()
	require.NoError(t, st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID: tokenID, UserID: userid.MustNew(userID), ClientID: oauthapp.ControlCLIClientID, InstallationName: "test", GrantedScopes: authscope.NonAdminGrant().String(),
		SecretHash: tv.HashSecret(secret),
	}))

	bearer := "Bearer " + auth.FormatBearer(auth.BearerKindAPI, tokenID, secret)

	// Warm the bearer cache.
	req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req.Header().Set("Authorization", bearer)
	_, err = client.GetCurrentUser(context.Background(), req)
	require.NoError(t, err)

	// Revoke + evict.
	_, err = st.APITokens().Revoke(context.Background(), tokenID)
	require.NoError(t, err)
	sc.EvictBearer(auth.NewBearerRef(auth.BearerKindAPI, tokenID))

	// Next call must fail immediately, not 30s later.
	req2 := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req2.Header().Set("Authorization", bearer)
	_, err = client.GetCurrentUser(context.Background(), req2)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// TestInterceptor_LeapMuxBearer_RejectsExpired pins down that the
// interceptor rejects API bearers whose access token has expired even
// when their underlying row is otherwise valid (not revoked, not
// malformed). Without this guard, expired tokens would keep working
// indefinitely as long as the row exists — defeating the purpose of
// `expires_at`.
func TestInterceptor_LeapMuxBearer_RejectsExpired(t *testing.T) {
	client, st, tv := setupInterceptorTestServerWithBearerSupport(t)
	userID := adminUserID(t, st)

	tokenID := newTestTokenID()
	secret := auth.MintAccessSecret()
	pastExpiry := time.Now().Add(-1 * time.Minute)
	require.NoError(t, st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:               tokenID,
		UserID:           userid.MustNew(userID),
		ClientID:         oauthapp.ControlCLIClientID,
		InstallationName: "test",
		GrantedScopes:    authscope.NonAdminGrant().String(),
		SecretHash:       tv.HashSecret(secret),
		ExpiresAt:        &pastExpiry,
	}))

	req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req.Header().Set("Authorization", "Bearer "+auth.FormatBearer(auth.BearerKindAPI, tokenID, secret))

	_, err := client.GetCurrentUser(context.Background(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err),
		"expired bearer must surface as Unauthenticated, got %v", err)
}

func TestInterceptor_LeapMuxBearer_CachedEntryExpiresWithCredential(t *testing.T) {
	client, st, tv := setupInterceptorTestServerWithBearerSupport(t)
	userID := adminUserID(t, st)

	tokenID := newTestTokenID()
	secret := auth.MintAccessSecret()
	expiresAt := time.Now().Add(50 * time.Millisecond)
	require.NoError(t, st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:               tokenID,
		UserID:           userid.MustNew(userID),
		ClientID:         oauthapp.ControlCLIClientID,
		InstallationName: "test",
		GrantedScopes:    authscope.NonAdminGrant().String(),
		SecretHash:       tv.HashSecret(secret),
		ExpiresAt:        &expiresAt,
	}))

	bearer := "Bearer " + auth.FormatBearer(auth.BearerKindAPI, tokenID, secret)
	req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req.Header().Set("Authorization", bearer)
	_, err := client.GetCurrentUser(context.Background(), req)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		retry := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
		retry.Header().Set("Authorization", bearer)
		_, err := client.GetCurrentUser(context.Background(), retry)
		return connect.CodeOf(err) == connect.CodeUnauthenticated
	}, time.Second, 10*time.Millisecond, "cached bearer must stop authenticating at its persisted expiry")
}

func TestInterceptor_DelegationBearer_RejectsAccountProcedure(t *testing.T) {
	client, st, tv := setupInterceptorTestServerWithBearerSupport(t)
	userID := adminUserID(t, st)

	// Seed a worker so the delegation row's worker_id FK is satisfied.
	workerID := id.Generate()
	require.NoError(t, st.Workers().Create(context.Background(), store.CreateWorkerParams{
		ID:              workerID,
		AuthToken:       id.Generate(),
		RegisteredBy:    userid.MustNew(userID),
		PublicKey:       []byte("delegation-x25519-key-32-bytes-pad"),
		MlkemPublicKey:  []byte("dele-mlkem"),
		SlhdsaPublicKey: []byte("dele-slhdsa"),
	}))
	// Seed a workspace owned by the user so the delegation's
	// workspace_id is meaningful for downstream scope checks.
	workspaceID := id.Generate()
	require.NoError(t, st.Workspaces().Create(context.Background(), store.CreateWorkspaceParams{
		ID:          workspaceID,
		OwnerUserID: userid.MustNew(userID),
		Title:       "ws",
	}))

	tokenID := newTestTokenID()
	secret := auth.MintAccessSecret()
	require.NoError(t, st.DelegationTokens().Create(context.Background(), store.CreateDelegationTokenParams{
		GrantedScopes: "workspace:read workspace:write worker:read",
		ID:            tokenID,
		UserID:        userid.MustNew(userID),
		WorkerID:      workerID,
		SecretHash:    tv.HashSecret(secret),
		ExpiresAt:     time.Now().Add(time.Hour),
	}))

	req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req.Header().Set("Authorization", "Bearer "+auth.FormatBearer(auth.BearerKindDelegation, tokenID, secret))

	_, err := client.GetCurrentUser(context.Background(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err),
		"delegation bearers must not authenticate account-level procedures")
}

// TestInterceptor_DelegationBearer_RejectsRevoked confirms the
// dispatch path handles revocation symmetrically across token kinds.
// A revoked delegation row should never authenticate, even though
// every other field still matches.
func TestInterceptor_DelegationBearer_RejectsRevoked(t *testing.T) {
	client, st, tv := setupInterceptorTestServerWithBearerSupport(t)
	userID := adminUserID(t, st)

	workerID := id.Generate()
	require.NoError(t, st.Workers().Create(context.Background(), store.CreateWorkerParams{
		ID:              workerID,
		AuthToken:       id.Generate(),
		RegisteredBy:    userid.MustNew(userID),
		PublicKey:       []byte("revoked-x25519-key-32-bytes-padxx"),
		MlkemPublicKey:  []byte("rev-mlkem"),
		SlhdsaPublicKey: []byte("rev-slhdsa"),
	}))
	workspaceID := id.Generate()
	require.NoError(t, st.Workspaces().Create(context.Background(), store.CreateWorkspaceParams{
		ID:          workspaceID,
		OwnerUserID: userid.MustNew(userID),
		Title:       "ws",
	}))

	tokenID := newTestTokenID()
	secret := auth.MintAccessSecret()
	require.NoError(t, st.DelegationTokens().Create(context.Background(), store.CreateDelegationTokenParams{
		GrantedScopes: "workspace:read workspace:write worker:read",
		ID:            tokenID,
		UserID:        userid.MustNew(userID),
		WorkerID:      workerID,
		SecretHash:    tv.HashSecret(secret),
		ExpiresAt:     time.Now().Add(time.Hour),
	}))
	_, err := st.DelegationTokens().Revoke(context.Background(), tokenID)
	require.NoError(t, err)

	req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req.Header().Set("Authorization", "Bearer "+auth.FormatBearer(auth.BearerKindDelegation, tokenID, secret))

	_, err = client.GetCurrentUser(context.Background(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// TestInterceptor_DelegationBearer_RejectsExpired pins the same
// expiry contract for delegation tokens. Their TTL is short by design
// (DelegationTokenTTL = 1h); the interceptor must enforce it.
func TestInterceptor_DelegationBearer_RejectsExpired(t *testing.T) {
	client, st, tv := setupInterceptorTestServerWithBearerSupport(t)
	userID := adminUserID(t, st)

	workerID := id.Generate()
	require.NoError(t, st.Workers().Create(context.Background(), store.CreateWorkerParams{
		ID:              workerID,
		AuthToken:       id.Generate(),
		RegisteredBy:    userid.MustNew(userID),
		PublicKey:       []byte("expired-x25519-key-32-bytes-padxx"),
		MlkemPublicKey:  []byte("exp-mlkem"),
		SlhdsaPublicKey: []byte("exp-slhdsa"),
	}))
	workspaceID := id.Generate()
	require.NoError(t, st.Workspaces().Create(context.Background(), store.CreateWorkspaceParams{
		ID:          workspaceID,
		OwnerUserID: userid.MustNew(userID),
		Title:       "ws",
	}))

	tokenID := newTestTokenID()
	secret := auth.MintAccessSecret()
	require.NoError(t, st.DelegationTokens().Create(context.Background(), store.CreateDelegationTokenParams{
		GrantedScopes: "workspace:read workspace:write worker:read",
		ID:            tokenID,
		UserID:        userid.MustNew(userID),
		WorkerID:      workerID,
		SecretHash:    tv.HashSecret(secret),
		ExpiresAt:     time.Now().Add(-time.Minute), // already expired
	}))

	req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req.Header().Set("Authorization", "Bearer "+auth.FormatBearer(auth.BearerKindDelegation, tokenID, secret))

	_, err := client.GetCurrentUser(context.Background(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// TestInterceptor_LeapMuxBearer_RejectsUnknownKindTag pins the
// dispatch table's "unknown kind = reject without a DB round-trip"
// guarantee. Format is `lmx_<kind><id>_<secret>`; a kind char that
// isn't 'a' (api) or 'd' (delegation) must short-circuit at parse
// time. Without this, every spam request would burn a primary-key
// lookup on each table — measurable under load.
func TestInterceptor_LeapMuxBearer_RejectsUnknownKindTag(t *testing.T) {
	client, _, _ := setupInterceptorTestServerWithBearerSupport(t)

	// Manually craft a bearer with kind 'z' (unrecognised). Format:
	// lmx_<kind><id>_<secret>; FormatBearer hides the kind char so we
	// stitch one together by hand.
	tokenID := newTestTokenID()
	bogus := "lmx_z" + tokenID + "_anysecret"

	req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req.Header().Set("Authorization", "Bearer "+bogus)

	_, err := client.GetCurrentUser(context.Background(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func newTestTokenID() string {
	// Reuse the project's id generator for token primary keys.
	return id.Generate()
}

// setupInterceptorTestServerWithCache is like setupInterceptorTestServer but
// wires the AuthContextRegistry into the AuthService (so Logout evicts entries) and
// returns the store for DB inspection.
func setupInterceptorTestServerWithCache(t *testing.T) (leapmuxv1connect.AuthServiceClient, store.Store) {
	t.Helper()
	return setupSessionDurationTestServer(t, 0)
}

// setupSessionDurationTestServer builds that server with a configured session
// duration; zero leaves the setting's default. One settings manager feeds both
// the service that stamps the first expiry at login and the interceptor that
// slides it, so a test that pins a non-default duration exercises the whole
// wiring rather than one half of it.
func setupSessionDurationTestServer(t *testing.T, sessionDuration time.Duration) (leapmuxv1connect.AuthServiceClient, store.Store) {
	t.Helper()

	st := hubtestutil.OpenTestStore(t)

	hubtestutil.CreateTestAdmin(t, st)

	set := servicetest.NewSettingsManager(t, st, nil)
	if sessionDuration > 0 {
		require.NoError(t, set.SetValue(context.Background(),
			settings.KeySessionDurationSeconds, int64(sessionDuration/time.Second)))
	}
	mux := http.NewServeMux()
	interceptor, sc := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{
		Store:  st,
		Policy: servicetest.AuthPolicy(set),
	})
	t.Cleanup(sc.Stop)
	interceptors := connect.WithInterceptors(interceptor)
	authSvc := service.NewAuthService(servicetest.AuthServiceDeps(st, &config.Config{}, set, auth.NewCredentialLifecycleEffects(sc, nil, nil)))
	path, handler := leapmuxv1connect.NewAuthServiceHandler(authSvc, interceptors)
	mux.Handle(path, handler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL), st
}

func TestTouchSession_ThrottledWithinThreshold(t *testing.T) {
	client, st := setupInterceptorTestServerWithCache(t)

	token := loginAdmin(t, client)

	// First authenticated request — triggers touchSession and updates last_active_at.
	req1 := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req1.Header().Set("Cookie", auth.CookieName+"="+token)
	_, err := client.GetCurrentUser(context.Background(), req1)
	require.NoError(t, err)

	sess1, err := st.Sessions().GetByID(context.Background(), token, time.Now().UTC())
	require.NoError(t, err)
	t1 := sess1.LastActiveAt

	// Second authenticated request immediately — should be throttled (no DB write).
	req2 := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req2.Header().Set("Cookie", auth.CookieName+"="+token)
	_, err = client.GetCurrentUser(context.Background(), req2)
	require.NoError(t, err)

	sess2, err := st.Sessions().GetByID(context.Background(), token, time.Now().UTC())
	require.NoError(t, err)
	t2 := sess2.LastActiveAt

	assert.Equal(t, t1, t2, "last_active_at should not change on rapid successive requests (throttled)")
}

// backdateLastActive puts a session's last_active_at an hour in the past, past
// any touch throttle, so the next authenticated request slides the expiry. The
// throttle is what makes a slide unobservable in a test that only logs in: the
// row is created with last_active_at = now, so the conditional UPDATE matches
// no row until the threshold passes.
func backdateLastActive(t *testing.T, st store.Store, sessionID string) {
	t.Helper()
	testable, ok := st.(store.TestableStore)
	require.True(t, ok, "the test store must expose the test helper")
	require.NoError(t, testable.TestHelper().SetLastActiveAt(context.Background(), sessionID, time.Now().Add(-time.Hour)))
}

// A slide must re-issue the cookie. The cookie carries its own Expires, so a
// browser drops it at the deadline the login set however far the row slid --
// which would sign an active user out one session duration after the login.
//
// The configured duration, not the default, is what both ends must carry.
func TestTouchSession_SlideRefreshesCookie(t *testing.T) {
	const configured = 36 * time.Hour
	client, st := setupSessionDurationTestServer(t, configured)

	token := loginAdmin(t, client)
	backdateLastActive(t, st, token)

	req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req.Header().Set("Cookie", auth.CookieName+"="+token)
	before := time.Now()
	resp, err := client.GetCurrentUser(context.Background(), req)
	require.NoError(t, err)

	setCookie := resp.Header().Get("Set-Cookie")
	require.NotEmpty(t, setCookie, "a slid session must re-issue its cookie")
	parsed, err := http.ParseSetCookie(setCookie)
	require.NoError(t, err)
	assert.Equal(t, auth.CookieName, parsed.Name)
	assert.Equal(t, token, parsed.Value, "the refresh must re-issue the same session, not mint one")
	assert.True(t, parsed.HttpOnly)
	assert.True(t, parsed.Expires.After(before.Add(configured)),
		"the browser's copy must outlive the configured duration measured from this request")
	assert.WithinDuration(t, before.Add(configured), parsed.Expires, 15*time.Minute,
		"the slide must follow the configured duration, not the default")

	sess, err := st.Sessions().GetByID(context.Background(), token, time.Now().UTC())
	require.NoError(t, err)
	assert.WithinDuration(t, sess.ExpiresAt, parsed.Expires, time.Second,
		"the cookie and the row must expire together, or one outlives the other silently")
}

// Login stamps the first expiry from the same configured duration. Without
// this, a configured value would take effect only at the first slide -- five
// minutes into the session, and never for a user who signs in and leaves.
func TestLogin_UsesConfiguredSessionDuration(t *testing.T) {
	const configured = 36 * time.Hour
	client, st := setupSessionDurationTestServer(t, configured)

	before := time.Now()
	token := loginAdmin(t, client)

	sess, err := st.Sessions().GetByID(context.Background(), token, time.Now().UTC())
	require.NoError(t, err)
	hubtestutil.AssertSessionLifetime(t, before, configured, sess.ExpiresAt)
}

// An unconfigured Hub issues the default, so an operator who sets nothing gets
// the documented lifetime rather than whatever the zero value would mean.
func TestLogin_UnconfiguredUsesDefaultSessionDuration(t *testing.T) {
	client, st := setupInterceptorTestServerWithCache(t)

	before := time.Now()
	token := loginAdmin(t, client)

	sess, err := st.Sessions().GetByID(context.Background(), token, time.Now().UTC())
	require.NoError(t, err)
	hubtestutil.AssertSessionLifetime(t, before, auth.DefaultSessionDuration, sess.ExpiresAt)
}

// The throttle governs the cookie too: a request that writes no row must not
// hand the browser an expiry the row does not carry.
func TestTouchSession_ThrottledRequestSendsNoCookie(t *testing.T) {
	client, _ := setupInterceptorTestServerWithCache(t)

	token := loginAdmin(t, client)

	// No backdating: login stamped last_active_at, so this request is inside the
	// throttle window and the conditional UPDATE matches no row.
	req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req.Header().Set("Cookie", auth.CookieName+"="+token)
	resp, err := client.GetCurrentUser(context.Background(), req)
	require.NoError(t, err)

	assert.Empty(t, resp.Header().Values("Set-Cookie"))
}

// Logout writes the clearing cookie itself. A refresh must not follow it: a
// browser keeps the last Set-Cookie for a name, so the user would stay signed
// in on a session the Hub deleted.
func TestLogout_SlideDoesNotResurrectCookie(t *testing.T) {
	client, st := setupInterceptorTestServerWithCache(t)

	token := loginAdmin(t, client)
	backdateLastActive(t, st, token)

	logoutReq := connect.NewRequest(&leapmuxv1.LogoutRequest{})
	logoutReq.Header().Set("Cookie", auth.CookieName+"="+token)
	resp, err := client.Logout(context.Background(), logoutReq)
	require.NoError(t, err)

	values := resp.Header().Values("Set-Cookie")
	require.Len(t, values, 1, "logout must answer with exactly one session cookie")
	parsed, err := http.ParseSetCookie(values[0])
	require.NoError(t, err)
	assert.Empty(t, parsed.Value)
	assert.Negative(t, parsed.MaxAge, "the clearing cookie must survive the slide's refresh")
}

// A bearer holds no session cookie, so a bearer-authenticated call must never
// answer with one -- that would hand a CLI or an agent a browser credential.
func TestBearerRequest_SendsNoSessionCookie(t *testing.T) {
	client, st, tv := setupInterceptorTestServerWithBearerSupport(t)
	userID := adminUserID(t, st)

	tokenID := newTestTokenID()
	secret := auth.MintAccessSecret()
	require.NoError(t, st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:               tokenID,
		UserID:           userid.MustNew(userID),
		ClientID:         oauthapp.ControlCLIClientID,
		InstallationName: "test",
		GrantedScopes:    authscope.NonAdminGrant().String(),
		SecretHash:       tv.HashSecret(secret),
	}))

	req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req.Header().Set("Authorization", "Bearer "+auth.FormatBearer(auth.BearerKindAPI, tokenID, secret))
	resp, err := client.GetCurrentUser(context.Background(), req)
	require.NoError(t, err)

	assert.Empty(t, resp.Header().Values("Set-Cookie"))
}

func TestLogout_EvictsSessionFromCache(t *testing.T) {
	client, _ := setupInterceptorTestServerWithCache(t)

	token := loginAdmin(t, client)

	// Verify the session is valid.
	req1 := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req1.Header().Set("Cookie", auth.CookieName+"="+token)
	resp, err := client.GetCurrentUser(context.Background(), req1)
	require.NoError(t, err)
	assert.Equal(t, "admin", resp.Msg.GetUser().GetUsername())

	// Logout — deletes session from DB and evicts from cache.
	logoutReq := connect.NewRequest(&leapmuxv1.LogoutRequest{})
	logoutReq.Header().Set("Cookie", auth.CookieName+"="+token)
	_, err = client.Logout(context.Background(), logoutReq)
	require.NoError(t, err)

	// Using the same token should now fail with Unauthenticated.
	req2 := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req2.Header().Set("Cookie", auth.CookieName+"="+token)
	_, err = client.GetCurrentUser(context.Background(), req2)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestAuthContextRegistry_RapidRequestsSucceed(t *testing.T) {
	client, _ := setupInterceptorTestServerWithCache(t)

	token := loginAdmin(t, client)

	// Issue multiple rapid requests — the session cache should serve
	// the cached UserInfo without repeated DB queries.
	for i := 0; i < 5; i++ {
		req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
		req.Header().Set("Cookie", auth.CookieName+"="+token)
		resp, err := client.GetCurrentUser(context.Background(), req)
		require.NoError(t, err)
		assert.Equal(t, "admin", resp.Msg.GetUser().GetUsername())
	}
}

func TestAuthContextRegistry_EvictInvalidatesCache(t *testing.T) {
	// This test verifies that logging out (which evicts from cache) immediately
	// invalidates the session, even if it was recently cached.
	client, _ := setupInterceptorTestServerWithCache(t)

	token := loginAdmin(t, client)

	// Warm the session cache.
	req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req.Header().Set("Cookie", auth.CookieName+"="+token)
	_, err := client.GetCurrentUser(context.Background(), req)
	require.NoError(t, err)

	// Logout evicts from cache.
	logoutReq := connect.NewRequest(&leapmuxv1.LogoutRequest{})
	logoutReq.Header().Set("Cookie", auth.CookieName+"="+token)
	_, err = client.Logout(context.Background(), logoutReq)
	require.NoError(t, err)

	// The cached session must be gone — request should fail immediately.
	req2 := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req2.Header().Set("Cookie", auth.CookieName+"="+token)
	_, err = client.GetCurrentUser(context.Background(), req2)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}
