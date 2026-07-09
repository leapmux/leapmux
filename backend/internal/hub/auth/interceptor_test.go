package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/bootstrap"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/service"
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
	interceptor, _ := auth.NewInterceptor(st, nil, false, false)
	interceptors := connect.WithInterceptors(interceptor)
	authSvc := service.NewAuthService(st, &config.Config{}, auth.NewCredentialLifecycleEffects(nil, nil, nil), nil, mail.NewStubSender(), mail.Renderer{})
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

func TestInterceptor_SoloMode_AutoAuthenticated(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)

	// Bootstrap in solo mode creates a user named "solo".
	err := bootstrap.Run(context.Background(), st, true)
	require.NoError(t, err)

	soloUser, err := auth.LoadSoloUser(context.Background(), st)
	require.NoError(t, err)

	mux := http.NewServeMux()
	interceptor, _ := auth.NewInterceptor(st, soloUser, false, false)
	interceptors := connect.WithInterceptors(interceptor)
	authSvc := service.NewAuthService(st, &config.Config{SoloMode: true}, auth.NewCredentialLifecycleEffects(nil, nil, nil), nil, mail.NewStubSender(), mail.Renderer{})
	path, handler := leapmuxv1connect.NewAuthServiceHandler(authSvc, interceptors)
	mux.Handle(path, handler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL)

	// In solo mode, private endpoints should work without any cookie.
	resp, err := client.GetCurrentUser(context.Background(), connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{}))
	require.NoError(t, err)
	assert.Equal(t, "solo", resp.Msg.GetUser().GetUsername())
	assert.True(t, resp.Msg.GetUser().GetIsAdmin())
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
	interceptor, _ := auth.NewInterceptorWithTokens(st, nil, tv, false, false)
	interceptors := connect.WithInterceptors(interceptor)
	authSvc := service.NewAuthService(st, &config.Config{}, auth.NewCredentialLifecycleEffects(nil, nil, nil), nil, mail.NewStubSender(), mail.Renderer{})
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
		ID:         tokenID,
		UserID:     userID,
		ClientType: "cli",
		ClientName: "test",
		SecretHash: tv.HashSecret(secret),
		Scope:      "remote:*",
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
		ID:         tokenID,
		UserID:     userID,
		ClientType: "cli",
		ClientName: "test",
		SecretHash: tv.HashSecret(secret),
		Scope:      "remote:*",
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
		ID:         tokenID,
		UserID:     userID,
		ClientType: "cli",
		ClientName: "test",
		SecretHash: tv.HashSecret(secret),
		Scope:      "remote:*",
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
	interceptor, sc := auth.NewInterceptorWithTokens(st, nil, tv, false, false)
	t.Cleanup(sc.Stop)
	interceptors := connect.WithInterceptors(interceptor)
	authSvc := service.NewAuthService(st, &config.Config{}, auth.NewCredentialLifecycleEffects(sc, nil, nil), nil, mail.NewStubSender(), mail.Renderer{})
	path, handler := leapmuxv1connect.NewAuthServiceHandler(authSvc, interceptors)
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL)

	userID := adminUserID(t, st)
	tokenID := newTestTokenID()
	secret := auth.MintAccessSecret()
	require.NoError(t, st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID: tokenID, UserID: userID, ClientType: "cli", ClientName: "test",
		SecretHash: tv.HashSecret(secret), Scope: "remote:*",
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
		ID:         tokenID,
		UserID:     userID,
		ClientType: "cli",
		ClientName: "test",
		SecretHash: tv.HashSecret(secret),
		Scope:      "remote:*",
		ExpiresAt:  &pastExpiry,
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
		ID:         tokenID,
		UserID:     userID,
		ClientType: "cli",
		ClientName: "test",
		SecretHash: tv.HashSecret(secret),
		Scope:      "remote:*",
		ExpiresAt:  &expiresAt,
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
		RegisteredBy:    userID,
		PublicKey:       []byte("delegation-x25519-key-32-bytes-pad"),
		MlkemPublicKey:  []byte("dele-mlkem"),
		SlhdsaPublicKey: []byte("dele-slhdsa"),
	}))
	// Seed a workspace owned by the user so the delegation's
	// workspace_id is meaningful for downstream scope checks.
	workspaceID := id.Generate()
	user, err := st.Users().GetByUsername(context.Background(), "admin")
	require.NoError(t, err)
	require.NoError(t, st.Workspaces().Create(context.Background(), store.CreateWorkspaceParams{
		ID:          workspaceID,
		OrgID:       user.OrgID,
		OwnerUserID: userID,
		Title:       "ws",
	}))

	tokenID := newTestTokenID()
	secret := auth.MintAccessSecret()
	require.NoError(t, st.DelegationTokens().Create(context.Background(), store.CreateDelegationTokenParams{
		ID:          tokenID,
		UserID:      userID,
		WorkerID:    workerID,
		WorkspaceID: workspaceID,
		SecretHash:  tv.HashSecret(secret),
		ExpiresAt:   time.Now().Add(time.Hour),
	}))

	req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req.Header().Set("Authorization", "Bearer "+auth.FormatBearer(auth.BearerKindDelegation, tokenID, secret))

	_, err = client.GetCurrentUser(context.Background(), req)
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
		RegisteredBy:    userID,
		PublicKey:       []byte("revoked-x25519-key-32-bytes-padxx"),
		MlkemPublicKey:  []byte("rev-mlkem"),
		SlhdsaPublicKey: []byte("rev-slhdsa"),
	}))
	workspaceID := id.Generate()
	user, err := st.Users().GetByUsername(context.Background(), "admin")
	require.NoError(t, err)
	require.NoError(t, st.Workspaces().Create(context.Background(), store.CreateWorkspaceParams{
		ID:          workspaceID,
		OrgID:       user.OrgID,
		OwnerUserID: userID,
		Title:       "ws",
	}))

	tokenID := newTestTokenID()
	secret := auth.MintAccessSecret()
	require.NoError(t, st.DelegationTokens().Create(context.Background(), store.CreateDelegationTokenParams{
		ID:          tokenID,
		UserID:      userID,
		WorkerID:    workerID,
		WorkspaceID: workspaceID,
		SecretHash:  tv.HashSecret(secret),
		ExpiresAt:   time.Now().Add(time.Hour),
	}))
	_, err = st.DelegationTokens().Revoke(context.Background(), tokenID)
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
		RegisteredBy:    userID,
		PublicKey:       []byte("expired-x25519-key-32-bytes-padxx"),
		MlkemPublicKey:  []byte("exp-mlkem"),
		SlhdsaPublicKey: []byte("exp-slhdsa"),
	}))
	workspaceID := id.Generate()
	user, err := st.Users().GetByUsername(context.Background(), "admin")
	require.NoError(t, err)
	require.NoError(t, st.Workspaces().Create(context.Background(), store.CreateWorkspaceParams{
		ID:          workspaceID,
		OrgID:       user.OrgID,
		OwnerUserID: userID,
		Title:       "ws",
	}))

	tokenID := newTestTokenID()
	secret := auth.MintAccessSecret()
	require.NoError(t, st.DelegationTokens().Create(context.Background(), store.CreateDelegationTokenParams{
		ID:          tokenID,
		UserID:      userID,
		WorkerID:    workerID,
		WorkspaceID: workspaceID,
		SecretHash:  tv.HashSecret(secret),
		ExpiresAt:   time.Now().Add(-time.Minute), // already expired
	}))

	req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req.Header().Set("Authorization", "Bearer "+auth.FormatBearer(auth.BearerKindDelegation, tokenID, secret))

	_, err = client.GetCurrentUser(context.Background(), req)
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

	st := hubtestutil.OpenTestStore(t)

	hubtestutil.CreateTestAdmin(t, st)

	mux := http.NewServeMux()
	interceptor, sc := auth.NewInterceptor(st, nil, false, false)
	t.Cleanup(sc.Stop)
	interceptors := connect.WithInterceptors(interceptor)
	authSvc := service.NewAuthService(st, &config.Config{}, auth.NewCredentialLifecycleEffects(sc, nil, nil), nil, mail.NewStubSender(), mail.Renderer{})
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

	sess1, err := st.Sessions().GetByID(context.Background(), token)
	require.NoError(t, err)
	t1 := sess1.LastActiveAt

	// Second authenticated request immediately — should be throttled (no DB write).
	req2 := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req2.Header().Set("Cookie", auth.CookieName+"="+token)
	_, err = client.GetCurrentUser(context.Background(), req2)
	require.NoError(t, err)

	sess2, err := st.Sessions().GetByID(context.Background(), token)
	require.NoError(t, err)
	t2 := sess2.LastActiveAt

	assert.Equal(t, t1, t2, "last_active_at should not change on rapid successive requests (throttled)")
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
