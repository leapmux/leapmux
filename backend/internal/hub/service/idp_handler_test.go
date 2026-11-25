package service_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/userid"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"

	"github.com/leapmux/leapmux/internal/hub/keystore"
	huboauth "github.com/leapmux/leapmux/internal/hub/oauth"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/servicetest"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	utiltest "github.com/leapmux/leapmux/internal/util/testutil"
)

type oauthTestEnv struct {
	server         *httptest.Server
	client         leapmuxv1connect.AuthServiceClient
	store          store.Store
	ks             *keystore.Keystore
	set            *settings.Manager
	states         *service.MemoryOAuthStateStore
	pendingSignups *service.PendingOAuthSignupStore
}

func setupOAuthTestEnv(t *testing.T) *oauthTestEnv {
	t.Helper()
	return setupOAuthTestEnvOver(t, ":4327", nil, nil)
}

func setupOAuthTestServer(t *testing.T) (*httptest.Server, store.Store, *keystore.Keystore) {
	t.Helper()
	env := setupOAuthTestEnv(t)
	return env.server, env.store, env.ks
}

// setupOAuthTestServerWithSettings also returns the settings manager, so
// a test can put the hub in its production shape (secure cookies) before it
// drives a flow.
func setupOAuthTestServerWithSettings(t *testing.T) (*httptest.Server, store.Store, *keystore.Keystore, *settings.Manager) {
	return setupOAuthTestServerWithListen(t, ":4327")
}

// bindSetForTest turns the helper's listen argument into a bind-set list: the
// TCP address as given, or a local-only list when the caller wants a hub with
// no browser-reachable origin (which is what an empty argument has always
// meant here).
func bindSetForTest(t *testing.T, listen string) []string {
	t.Helper()
	if listen != "" {
		return []string{listen}
	}
	return []string{"unix:" + filepath.Join(t.TempDir(), "hub.sock")}
}

// setupOAuthTestServerWithListen varies the bind set, which is what decides
// whether this hub can run a passkey ceremony at all: a LOCAL-ONLY bind set
// has no browser-reachable origin, so RPConfigFromSettings refuses and
// passkeys are cleanly unavailable. The account-shape rule still refuses a
// provider step-up for an account that holds such a passkey.
func setupOAuthTestServerWithListen(t *testing.T, listen string) (*httptest.Server, store.Store, *keystore.Keystore, *settings.Manager) {
	return setupOAuthTestServerOver(t, listen, nil)
}

// setupOAuthTestServerOver builds the same hub with a store the caller may
// decorate. The handler reads through the decorator, so a test can count the
// reads one request makes, hold two requests at the same row, or give the
// handler a row shape the schema refuses to store. The returned store is the
// UNDECORATED one, which is what a test seeds and asserts with.
func setupOAuthTestServerOver(
	t *testing.T,
	listen string,
	wrap func(store.Store) store.Store,
) (*httptest.Server, store.Store, *keystore.Keystore, *settings.Manager) {
	t.Helper()
	env := setupOAuthTestEnvOver(t, listen, wrap, nil)
	return env.server, env.store, env.ks, env.set
}

func setupOAuthTestEnvOver(
	t *testing.T,
	listen string,
	wrap func(store.Store) store.Store,
	wrapStates func(service.OAuthStateStore) service.OAuthStateStore,
) *oauthTestEnv {
	t.Helper()

	key, err := keystore.GenerateKey()
	require.NoError(t, err)
	ks, err := keystore.New(map[uint32][32]byte{1: key})
	require.NoError(t, err)

	st, err := sqlite.Open(":memory:", sqlitedb.Config{}, ks)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	err = st.Migrator().Migrate(context.Background())
	require.NoError(t, err)

	hubtestutil.CreateTestAdmin(t, st)

	cfg := &config.Config{
		Listen: bindSetForTest(t, listen),
	}
	set := servicetest.NewSettingsManager(t, st)
	enableSignup(t, set)

	handlerStore := st
	if wrap != nil {
		handlerStore = wrap(st)
	}
	states := service.NewMemoryOAuthStateStore()
	var handlerStates service.OAuthStateStore = states
	if wrapStates != nil {
		handlerStates = wrapStates(states)
	}
	pendingSignups := service.NewPendingOAuthSignupStore()
	idpHandler := service.NewIdPHandler(service.IdPHandlerDeps{
		SessionHasher:       hubtestutil.TestSecretHasher(),
		Store:               handlerStore,
		Config:              cfg,
		Settings:            set,
		Lifecycle:           auth.NewCredentialLifecycleEffects(nil, nil, nil),
		OAuthStates:         handlerStates,
		PendingOAuthSignups: pendingSignups,
	})

	mux := http.NewServeMux()
	idpHandler.RegisterRoutes(mux)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return &oauthTestEnv{
		server:         server,
		store:          st,
		ks:             ks,
		set:            set,
		states:         states,
		pendingSignups: pendingSignups,
	}
}

func createTestProvider(t *testing.T, st store.Store, ks *keystore.Keystore) string {
	return createTestProviderWithTrustEmail(t, st, ks, true)
}

func createTestProviderWithTrustEmail(t *testing.T, st store.Store, _ *keystore.Keystore, trustEmail bool) string {
	t.Helper()
	providerID := id.Generate()

	err := st.OAuthProviders().Create(context.Background(), store.CreateOAuthProviderParams{
		ID:           providerID,
		ProviderType: leapmuxv1.IdentityProviderType_IDENTITY_PROVIDER_TYPE_GITHUB,
		Name:         "Test GitHub",
		ClientID:     "test-client-id",
		ClientSecret: []byte("test-client-secret"),
		Scopes:       "read:user user:email",
		TrustEmail:   trustEmail,
		Enabled:      true,
	})
	require.NoError(t, err)
	return providerID
}

func TestOAuthLogin_RedirectsToProvider(t *testing.T) {
	t.Parallel()

	server, st, ks := setupOAuthTestServer(t)
	providerID := createTestProvider(t, st, ks)

	// Do not follow redirects.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	resp, err := client.Get(server.URL + "/auth/idp/" + providerID + "/login")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusFound, resp.StatusCode)

	location := resp.Header.Get("Location")
	assert.Contains(t, location, "github.com")
	assert.Contains(t, location, "state=")

	// Verify that the handler stored the state in the DB.
	// (Extracting it from the redirect URL needs parsing, but a working
	// redirect proves CreateOAuthState succeeded.)
}

func TestOAuthLogin_UnknownProvider_Returns404(t *testing.T) {
	t.Parallel()

	server, _, _ := setupOAuthTestServer(t)

	resp, err := http.Get(server.URL + "/auth/idp/nonexistent/login")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestOAuthLogin_DisabledProvider_Returns403(t *testing.T) {
	t.Parallel()

	server, st, ks := setupOAuthTestServer(t)
	providerID := createTestProvider(t, st, ks)

	// Disable the provider.
	err := st.OAuthProviders().UpdateEnabled(context.Background(), store.UpdateOAuthProviderEnabledParams{
		Enabled: false,
		ID:      providerID,
	})
	require.NoError(t, err)

	resp, err := http.Get(server.URL + "/auth/idp/" + providerID + "/login")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestOAuthLogin_StoresRedirectURI(t *testing.T) {
	t.Parallel()

	server, st, ks := setupOAuthTestServer(t)
	providerID := createTestProvider(t, st, ks)

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	resp, err := client.Get(server.URL + "/auth/idp/" + providerID + "/login?redirect=/workspace/123")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusFound, resp.StatusCode)
}

func TestOAuthCallback_InvalidState_Returns400(t *testing.T) {
	t.Parallel()

	server, _, _ := setupOAuthTestServer(t)

	resp, err := http.Get(server.URL + "/auth/idp/some-provider/callback?code=test&state=invalid-state")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestOAuthCallback_MissingCodeOrState_Returns400(t *testing.T) {
	t.Parallel()

	server, _, _ := setupOAuthTestServer(t)

	// Missing both.
	resp, err := http.Get(server.URL + "/auth/idp/some-provider/callback")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// Missing code.
	resp2, err := http.Get(server.URL + "/auth/idp/some-provider/callback?state=abc")
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()
	assert.Equal(t, http.StatusBadRequest, resp2.StatusCode)

	// Missing state.
	resp3, err := http.Get(server.URL + "/auth/idp/some-provider/callback?code=abc")
	require.NoError(t, err)
	defer func() { _ = resp3.Body.Close() }()
	assert.Equal(t, http.StatusBadRequest, resp3.StatusCode)
}

// TestOAuthCallback_ExpiredState_Returns400 drives the callback with a
// browser that DOES hold the flow's nonce, so the request reaches the
// expiry check. The binding check runs first and answers 400 as well, so a
// status assertion alone would pass without ever reaching this guard; the
// body is what distinguishes the two refusals.
func TestOAuthCallback_ExpiredState_Returns400(t *testing.T) {
	t.Parallel()

	env := setupOAuthTestEnv(t)
	providerID := createTestProvider(t, env.store, env.ks)

	const nonce = "expired-flow-nonce"
	// Create an already-expired state.
	err := env.states.Create(service.CreateOAuthStateParams{
		State:        "expired-state",
		ProviderID:   providerID,
		PkceVerifier: "test-verifier",
		NonceHash:    hashNonceForTest(nonce),
		Purpose:      service.OAuthStatePurposeLogin,
		ExpiresAt:    time.Now().Add(-1 * time.Minute).UTC(),
	})
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodGet,
		env.server.URL+"/auth/idp/"+providerID+"/callback?code=test&state=expired-state", nil)
	require.NoError(t, err)
	req.AddCookie(auth.BuildOAuthNonceCookie("expired-state", nonce, time.Minute, false))

	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Contains(t, readBody(t, resp), "state expired",
		"the refusal must come from the expiry check, not from the binding check before it")
}

// TestOAuthCallback_StateProviderMismatch_Returns400 pins the guard that
// stops provider B's callback from redeeming a state minted for provider A.
// It sits behind the binding check, so the browser holds the
// flow's nonce here too.
func TestOAuthCallback_StateProviderMismatch_Returns400(t *testing.T) {
	t.Parallel()

	env := setupOAuthTestEnv(t)
	providerID := createTestProvider(t, env.store, env.ks)

	const nonce = "mismatch-flow-nonce"
	state := seedOAuthState(t, env.states, providerID, hashNonceForTest(nonce))

	// A SECOND provider's callback route. The mismatch check runs before
	// the handler resolves the provider, so the row's own provider is what
	// the guard compares against.
	other := createTestProvider(t, env.store, env.ks)
	require.NotEqual(t, providerID, other)
	req, err := http.NewRequest(http.MethodGet,
		env.server.URL+"/auth/idp/"+other+"/callback?code=test&state="+state, nil)
	require.NoError(t, err)
	req.AddCookie(auth.BuildOAuthNonceCookie(state, nonce, time.Minute, false))

	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Contains(t, readBody(t, resp), "state/provider mismatch")
}

func TestGetOAuthProviders_ReturnsEnabledOnly(t *testing.T) {
	t.Parallel()

	_, st, ks := setupOAuthTestServer(t)

	// Create two providers, one enabled and one disabled.
	enabledID := createTestProvider(t, st, ks)
	disabledID := id.Generate()
	_ = st.OAuthProviders().Create(context.Background(), store.CreateOAuthProviderParams{
		ID:           disabledID,
		ProviderType: leapmuxv1.IdentityProviderType_IDENTITY_PROVIDER_TYPE_OIDC,
		Name:         "Disabled OIDC",
		ClientID:     "disabled-client",
		ClientSecret: []byte("secret"),
		Scopes:       "openid",
		Enabled:      false,
	})

	providers, err := st.OAuthProviders().ListEnabled(context.Background())
	require.NoError(t, err)

	// The listing should show only the enabled provider.
	assert.Len(t, providers, 1)
	assert.Equal(t, enabledID, providers[0].ID)
}

// setupOAuthTestEnvWithAuthService sets up both IdPHandler (HTTP routes) and
// AuthService (ConnectRPC) on a single test server sharing in-memory OAuth
// state and pending-signup stores.
func setupOAuthTestEnvWithAuthService(t *testing.T) *oauthTestEnv {
	t.Helper()

	ks := hubtestutil.TestKeystore(t)
	st, err := sqlite.OpenTestableWithKeystore(":memory:", ks)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	hubtestutil.CreateTestAdmin(t, st)

	env := setupOAuthSignupEnv(t, st)
	env.ks = ks
	return env
}

func setupOAuthSignupEnv(t *testing.T, st store.Store) *oauthTestEnv {
	t.Helper()
	ks := hubtestutil.TestKeystore(t)

	cfg := &config.Config{
		Listen: []string{":4327"},
	}
	set := servicetest.NewSettingsManager(t, st)
	enableSignup(t, set)

	mux := http.NewServeMux()
	states := service.NewMemoryOAuthStateStore()
	pendingSignups := service.NewPendingOAuthSignupStore()

	// Register OAuth HTTP routes.
	idpHandler := service.NewIdPHandler(service.IdPHandlerDeps{
		SessionHasher:       hubtestutil.TestSecretHasher(),
		Store:               st,
		Config:              cfg,
		Settings:            set,
		Lifecycle:           auth.NewCredentialLifecycleEffects(nil, nil, nil),
		OAuthStates:         states,
		PendingOAuthSignups: pendingSignups,
	})
	idpHandler.RegisterRoutes(mux)

	// Register AuthService ConnectRPC routes.
	interceptor, registry := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{SessionHasher: hubtestutil.TestSecretHasher(), Store: st})
	t.Cleanup(registry.Stop)
	authDeps := servicetest.AuthServiceDeps(st, cfg, set, auth.NewCredentialLifecycleEffects(nil, nil, nil))
	authDeps.PendingOAuthSignups = pendingSignups
	authSvc := newInitializedAuthService(t, authDeps)
	connectServer := connect.NewServer(interceptor)
	mountAuthAndSystem(t, connectServer, authSvc, set)
	connecthttp.Mount(mux, connectServer, connecthttp.WithReadMaxBytes(0))

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewAuthServiceClient(connect.NewClient(connecthttp.NewTransport(server.Client(), server.URL, connecthttp.WithReadMaxBytes(0))))
	return &oauthTestEnv{
		server:         server,
		client:         client,
		store:          st,
		ks:             ks,
		set:            set,
		states:         states,
		pendingSignups: pendingSignups,
	}
}

// testSignupNonce is the pending-signup nonce every seeded row is bound to.
// The hub refuses a pending signup to a browser that did not start the OAuth
// flow, so a test that drives GetPendingOAuthSignup or CompleteOAuthSignup
// must present it.
const testSignupNonce = "test-signup-nonce"

// signupCookieHeader returns the Cookie header the browser that started the
// flow would send for one signup token.
func signupCookieHeader(token string) string {
	c := auth.BuildOAuthSignupNonceCookie(token, testSignupNonce, time.Minute, false)
	return c.Name + "=" + c.Value
}

// signupTokenCarrier is the field the two pending-signup requests share.
type signupTokenCarrier interface{ GetSignupToken() string }

// signupReq builds a request that carries the pending-signup cookie, taking
// the token off the message so no call site can attach the wrong one.
func signupReq[T any, PT interface {
	*T
	signupTokenCarrier
}](ctx context.Context, msg PT) (context.Context, *T) {
	ctx, info := clientCallInfo(ctx)
	info.RequestHeader().Set("Cookie", signupCookieHeader(msg.GetSignupToken()))
	return ctx, (*T)(msg)
}

// insertPendingSignup creates an in-memory pending signup with no redirect.
func insertPendingSignup(t *testing.T, pendingSignups *service.PendingOAuthSignupStore, providerID, token, email, displayName, subject string, expiresAt time.Time) {
	t.Helper()
	seedPendingSignup(t, pendingSignups, service.CreatePendingOAuthSignupParams{
		Token:           token,
		ProviderID:      providerID,
		ProviderSubject: subject,
		Email:           email,
		DisplayName:     displayName,
		ExpiresAt:       expiresAt,
	})
}

// seedPendingSignup creates the pending signup that p describes, filling the
// shared testSignupNonce hash as the OAuth callback would.
func seedPendingSignup(t *testing.T, pendingSignups *service.PendingOAuthSignupStore, p service.CreatePendingOAuthSignupParams) {
	t.Helper()
	p.NonceHash = hashNonceForTest(testSignupNonce)
	require.NoError(t, pendingSignups.Create(p))
}

// --- GetPendingOAuthSignup RPC tests ---

func TestGetPendingOAuthSignup_Success(t *testing.T) {
	t.Parallel()

	env := setupOAuthTestEnvWithAuthService(t)
	providerID := createTestProvider(t, env.store, env.ks)
	signupToken := id.Generate()

	insertPendingSignup(t, env.pendingSignups, providerID, signupToken, "alice@example.com", "Alice", "sub-123", time.Now().Add(5*time.Minute).UTC())

	resp, err := env.client.GetPendingOAuthSignup(signupReq(context.Background(), &leapmuxv1.GetPendingOAuthSignupRequest{
		SignupToken: signupToken,
	}))
	require.NoError(t, err)
	assert.Equal(t, "alice@example.com", resp.GetEmail())
	assert.Equal(t, "Alice", resp.GetDisplayName())
	assert.Equal(t, "Test GitHub", resp.GetProviderName())
	assert.Empty(t, resp.GetRedirect(), "a sign-in that asked to continue nowhere carries no redirect")
}

// A first-time OAuth sign-up can start from a page that asked to continue
// somewhere, such as the CLI consent page. The callback stores that address on
// the pending sign-up, and the complete-signup page reads it back here, so the
// browser can continue there once the account exists.
func TestGetPendingOAuthSignup_ReturnsRedirect(t *testing.T) {
	t.Parallel()

	env := setupOAuthTestEnvWithAuthService(t)
	providerID := createTestProvider(t, env.store, env.ks)
	signupToken := id.Generate()
	seedPendingSignup(t, env.pendingSignups, service.CreatePendingOAuthSignupParams{
		Token:           signupToken,
		ProviderID:      providerID,
		ProviderSubject: "sub-redirect",
		Email:           "redirect@example.com",
		DisplayName:     "Redirect",
		RedirectURI:     "/oauth/authorize?client_id=cli",
		ExpiresAt:       time.Now().Add(5 * time.Minute).UTC(),
	})

	resp, err := env.client.GetPendingOAuthSignup(signupReq(context.Background(), &leapmuxv1.GetPendingOAuthSignupRequest{
		SignupToken: signupToken,
	}))
	require.NoError(t, err)
	assert.Equal(t, "/oauth/authorize?client_id=cli", resp.GetRedirect())
}

func TestGetPendingOAuthSignup_InvalidToken(t *testing.T) {
	t.Parallel()

	env := setupOAuthTestEnvWithAuthService(t)

	_, err := env.client.GetPendingOAuthSignup(signupReq(context.Background(), &leapmuxv1.GetPendingOAuthSignupRequest{
		SignupToken: "nonexistent-token",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestGetPendingOAuthSignup_ExpiredToken(t *testing.T) {
	t.Parallel()

	env := setupOAuthTestEnvWithAuthService(t)
	providerID := createTestProvider(t, env.store, env.ks)
	signupToken := id.Generate()

	// Insert an already-expired pending signup.
	insertPendingSignup(t, env.pendingSignups, providerID, signupToken, "expired@example.com", "Expired", "sub-expired", time.Now().Add(-1*time.Minute).UTC())

	_, err := env.client.GetPendingOAuthSignup(signupReq(context.Background(), &leapmuxv1.GetPendingOAuthSignupRequest{
		SignupToken: signupToken,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// --- CompleteOAuthSignup RPC tests ---

func TestCompleteOAuthSignup_Success(t *testing.T) {
	t.Parallel()

	env := setupOAuthTestEnvWithAuthService(t)
	providerID := createTestProvider(t, env.store, env.ks)
	signupToken := id.Generate()

	insertPendingSignup(t, env.pendingSignups, providerID, signupToken, "bob@example.com", "Bob", "sub-bob", time.Now().Add(5*time.Minute).UTC())

	callCtx, info := connect.NewClientContext(context.Background())
	resp, err := env.client.CompleteOAuthSignup(signupReq(callCtx, &leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "bobuser",
		DisplayName: "Bob User",
	}))
	require.NoError(t, err)
	assert.Equal(t, "bobuser", resp.GetSessionReport().GetUser().GetUsername())
	assert.Equal(t, "Bob User", resp.GetSessionReport().GetUser().GetDisplayName())

	// Verify that the response sets the session cookie.
	setCookie := info.ResponseHeader().Get("Set-Cookie")
	assert.Contains(t, setCookie, auth.CookieName+"=")

	// Verify that the flow created the OAuth user link.
	link, err := env.store.OAuthUserLinks().Get(context.Background(), store.GetOAuthUserLinkParams{
		ProviderID:      providerID,
		ProviderSubject: "sub-bob",
	})
	require.NoError(t, err)
	assert.Equal(t, resp.GetSessionReport().GetUser().GetId(), link.UserID)

	// The report carries the COMPLETE projection. The account holds no password
	// and no passkey, so its provider is its only sign-in credential: the reply
	// must list that link and allow a step-up through it, or the client offers
	// this account no way to elevate until it reads the account again.
	user := resp.GetSessionReport().GetUser()
	require.Len(t, user.GetOauthProviders(), 1)
	assert.Equal(t, providerID, user.GetOauthProviders()[0].GetId())
	assert.Equal(t, "Test GitHub", user.GetOauthProviders()[0].GetName())
	assert.True(t, user.GetOauthProviders()[0].GetEnabled())
	assert.False(t, user.GetPasswordSet())
	assert.Zero(t, user.GetPasskeyCount())
	assert.True(t, user.GetMayElevateThroughAProvider())

	// Verify that the flow consumed the pending signup.
	_, ok := env.pendingSignups.Get(signupToken)
	assert.False(t, ok)
}

func TestCompleteOAuthSignup_RechecksAdmission(t *testing.T) {
	for _, policy := range []string{"signup", "provider"} {
		t.Run(policy, func(t *testing.T) {
			env := setupOAuthTestEnvWithAuthService(t)
			providerID, token := createTestProvider(t, env.store, env.ks), id.Generate()
			insertPendingSignup(t, env.pendingSignups, providerID, token, "closed@example.com", "Closed", "closed-subject", time.Now().Add(5*time.Minute).UTC())
			if policy == "signup" {
				require.NoError(t, settings.KeySignupEnabled.Set(t.Context(), env.set, false))
			} else {
				require.NoError(t, env.store.OAuthProviders().UpdateEnabled(t.Context(), store.UpdateOAuthProviderEnabledParams{ID: providerID, Enabled: false}))
			}
			ctx, info := connect.NewClientContext(t.Context())
			_, err := env.client.CompleteOAuthSignup(signupReq(ctx, &leapmuxv1.CompleteOAuthSignupRequest{SignupToken: token, Username: "closed-user"}))
			require.Error(t, err)
			assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
			_, err = env.store.Users().GetByUsername(t.Context(), "closed-user")
			assert.ErrorIs(t, err, store.ErrNotFound)
			_, err = env.store.OAuthUserLinks().Get(t.Context(), store.GetOAuthUserLinkParams{ProviderID: providerID, ProviderSubject: "closed-subject"})
			assert.ErrorIs(t, err, store.ErrNotFound)
			assert.NotContains(t, info.ResponseHeader().Get("Set-Cookie"), auth.CookieName+"=")
		})
	}
}

// The OAuth sign-up mints its own session, so it must stamp the configured
// duration like every other mint path. Its own CreateSession call is the one
// that would otherwise leave OAuth users on the built-in default while password
// users follow the operator's setting.
func TestCompleteOAuthSignup_UsesConfiguredSessionDuration(t *testing.T) {
	t.Parallel()

	const configured = 90 * time.Minute
	env := setupOAuthTestEnvWithAuthService(t)
	setSessionDuration(t, env.set, configured)
	providerID := createTestProvider(t, env.store, env.ks)
	signupToken := id.Generate()

	insertPendingSignup(t, env.pendingSignups, providerID, signupToken, "dana@example.com", "Dana", "sub-dana", time.Now().Add(5*time.Minute).UTC())

	before := time.Now()
	callCtx, info := connect.NewClientContext(context.Background())
	_, err := env.client.CompleteOAuthSignup(signupReq(callCtx, &leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "danauser",
		DisplayName: "Dana User",
	}))
	require.NoError(t, err)

	sessionToken := sessionFromCookie(t, info.ResponseHeader().Get("Set-Cookie"))
	sess, err := env.store.Sessions().GetByID(context.Background(), hubtestutil.SessionIDFromCookie(t, sessionToken), time.Now().UTC())
	require.NoError(t, err)
	hubtestutil.AssertSessionLifetime(t, before, configured, sess.ExpiresAt)
}

func TestCompleteOAuthSignup_UsesProviderEmail_IgnoresRequestEmail(t *testing.T) {
	t.Parallel()

	env := setupOAuthTestEnvWithAuthService(t)
	providerID := createTestProvider(t, env.store, env.ks) // trust_email=1 by default
	signupToken := id.Generate()

	// Pending signup has provider email "provider@example.com".
	insertPendingSignup(t, env.pendingSignups, providerID, signupToken, "provider@example.com", "Provider", "sub-provider", time.Now().Add(5*time.Minute).UTC())

	// The request tries to override with a different email; the handler must
	// ignore it.
	resp, err := env.client.CompleteOAuthSignup(signupReq(context.Background(), &leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "provideruser",
		Email:       "attacker@evil.com",
	}))
	require.NoError(t, err)

	// The user's email should be the provider's, not the attacker's.
	user, err := env.store.Users().GetByID(context.Background(), resp.GetSessionReport().GetUser().GetId())
	require.NoError(t, err)
	assert.Equal(t, "provider@example.com", user.Email, "email must come from provider, not request")
}

// An UNTRUSTED provider that omits the email claim leaves the caller to
// supply the address. Without the fallback the sign-up could never finish
// whenever verification was on: validateSignupEmail refuses an empty
// address, no step in the flow could produce one, and the pending token
// expired with the account never created.
func TestCompleteOAuthSignup_UntrustedProviderWithNoEmail_UsesRequestEmail(t *testing.T) {
	t.Parallel()

	env := setupOAuthTestEnvWithAuthService(t)
	providerID := createTestProviderWithTrustEmail(t, env.store, env.ks, false)
	signupToken := id.Generate()
	insertPendingSignup(t, env.pendingSignups, providerID, signupToken, "", "No Email", "sub-noemail", time.Now().Add(5*time.Minute).UTC())

	resp, err := env.client.CompleteOAuthSignup(signupReq(context.Background(), &leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "noemailuser",
		Email:       "typed@example.com",
	}))
	require.NoError(t, err)

	user, err := env.store.Users().GetByID(context.Background(), resp.GetSessionReport().GetUser().GetId())
	require.NoError(t, err)
	assert.Equal(t, "typed@example.com", user.Email,
		"the caller supplies the address the provider withheld")
	assert.False(t, user.EmailVerified, "an untrusted provider's address is never trusted-verified")
}

// The fallback must not become a substitution channel: a provider that DID
// supply an address still wins, whatever the request says. This is the
// untrusted twin of TestCompleteOAuthSignup_UsesProviderEmail_IgnoresRequestEmail.
func TestCompleteOAuthSignup_UntrustedProviderEmailBeatsRequestEmail(t *testing.T) {
	t.Parallel()

	env := setupOAuthTestEnvWithAuthService(t)
	providerID := createTestProviderWithTrustEmail(t, env.store, env.ks, false)
	signupToken := id.Generate()
	insertPendingSignup(t, env.pendingSignups, providerID, signupToken, "provider@example.com", "Provider", "sub-untrusted", time.Now().Add(5*time.Minute).UTC())

	resp, err := env.client.CompleteOAuthSignup(signupReq(context.Background(), &leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "untrusteduser",
		Email:       "attacker@evil.com",
	}))
	require.NoError(t, err)

	user, err := env.store.Users().GetByID(context.Background(), resp.GetSessionReport().GetUser().GetId())
	require.NoError(t, err)
	assert.Equal(t, "provider@example.com", user.Email,
		"a provider-supplied address is not substitutable")
}

func TestCompleteOAuthSignup_DuplicateUsername(t *testing.T) {
	t.Parallel()

	env := setupOAuthTestEnvWithAuthService(t)
	providerID := createTestProvider(t, env.store, env.ks)
	signupToken := id.Generate()

	insertPendingSignup(t, env.pendingSignups, providerID, signupToken, "dup@example.com", "Dup", "sub-dup", time.Now().Add(5*time.Minute).UTC())

	// Create an existing user with the same username.
	hash, err := password.Hash("testpass")
	require.NoError(t, err)
	err = env.store.Users().Create(context.Background(), store.CreateUserParams{
		ID:                    id.Generate(),
		Username:              "takenname",
		PasswordHash:          hash,
		DisplayName:           "Taken",
		Email:                 "",
		FirstCredentialExempt: true,
		IsAdmin:               false,
	})
	require.NoError(t, err)

	_, err = env.client.CompleteOAuthSignup(signupReq(context.Background(), &leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "takenname",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))

	// The handler should NOT delete the pending row, so the user can retry
	// with a different username.
	_, ok := env.pendingSignups.Get(signupToken)
	assert.True(t, ok)
}

func TestCompleteOAuthSignup_DuplicateEmail(t *testing.T) {
	t.Parallel()

	env := setupOAuthTestEnvWithAuthService(t)
	providerID := createTestProviderWithTrustEmail(t, env.store, env.ks, false) // untrusted provider
	signupToken := id.Generate()

	insertPendingSignup(t, env.pendingSignups, providerID, signupToken, "taken@example.com", "New", "sub-new", time.Now().Add(5*time.Minute).UTC())

	// Create an existing user with the same email.
	hash, err := password.Hash("testpass")
	require.NoError(t, err)
	err = env.store.Users().Create(context.Background(), store.CreateUserParams{
		ID:                    id.Generate(),
		Username:              "emailowner",
		PasswordHash:          hash,
		DisplayName:           "Email Owner",
		Email:                 "taken@example.com",
		FirstCredentialExempt: true,
		IsAdmin:               false,
	})
	require.NoError(t, err)

	// Provider has trust_email=0 and SMTP verification is off,
	// so email goes directly to the email column unverified. Duplicate check should fire.
	_, err = env.client.CompleteOAuthSignup(signupReq(context.Background(), &leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "newuniqueuser",
		Email:       "taken@example.com",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
}

func TestCompleteOAuthSignup_TrustedProviderSetsEmailVerified(t *testing.T) {
	t.Parallel()

	env := setupOAuthTestEnvWithAuthService(t)
	providerID := createTestProvider(t, env.store, env.ks)
	signupToken := id.Generate()

	insertPendingSignup(t, env.pendingSignups, providerID, signupToken, "trusted@example.com", "Trusted", "sub-trusted", time.Now().Add(5*time.Minute).UTC())

	resp, err := env.client.CompleteOAuthSignup(signupReq(context.Background(), &leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "trusteduser",
	}))
	require.NoError(t, err)

	user, err := env.store.Users().GetByID(context.Background(), resp.GetSessionReport().GetUser().GetId())
	require.NoError(t, err)
	assert.Equal(t, "trusted@example.com", user.Email)
	assert.True(t, user.EmailVerified)
	assert.Empty(t, user.PendingEmail)
}

func TestCompleteOAuthSignup_UntrustedFailClosedWhenVerificationEmailFails(t *testing.T) {
	t.Parallel()

	ks := hubtestutil.TestKeystore(t)
	st, err := sqlite.Open(":memory:", sqlitedb.Config{}, ks)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Migrator().Migrate(context.Background()))
	hubtestutil.CreateTestAdmin(t, st)

	cfg := &config.Config{Listen: []string{":4327"}}
	set := servicetest.NewSettingsManager(t, st)
	enableSignup(t, set)
	enableEmailVerification(t, set)

	mux := http.NewServeMux()
	pendingSignups := service.NewPendingOAuthSignupStore()
	idpHandler := service.NewIdPHandler(service.IdPHandlerDeps{
		SessionHasher:       hubtestutil.TestSecretHasher(),
		Store:               st,
		Config:              cfg,
		Settings:            set,
		Lifecycle:           auth.NewCredentialLifecycleEffects(nil, nil, nil),
		PendingOAuthSignups: pendingSignups,
	})
	idpHandler.RegisterRoutes(mux)

	interceptor, _ := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{SessionHasher: hubtestutil.TestSecretHasher(), Store: st})
	authDeps := servicetest.AuthServiceDeps(st, cfg, set, auth.NewCredentialLifecycleEffects(nil, nil, nil))
	authDeps.Mail = &mailSenderDouble{err: errors.New("smtp down")}
	authDeps.PendingOAuthSignups = pendingSignups
	authSvc := newInitializedAuthService(t, authDeps)
	connectServer := connect.NewServer(interceptor)
	leapmuxv1connect.RegisterAuthServiceHandler(connectServer, authSvc)
	connecthttp.Mount(mux, connectServer, connecthttp.WithReadMaxBytes(0))

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := leapmuxv1connect.NewAuthServiceClient(connect.NewClient(connecthttp.NewTransport(server.Client(), server.URL, connecthttp.WithReadMaxBytes(0))))

	providerID := createTestProviderWithTrustEmail(t, st, ks, false)
	signupToken := id.Generate()
	insertPendingSignup(t, pendingSignups, providerID, signupToken, "untrusted@example.com", "Untrusted", "sub-untrusted", time.Now().Add(5*time.Minute).UTC())

	_, err = client.CompleteOAuthSignup(signupReq(context.Background(), &leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "untrusteduser",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))

	_, err = st.Users().GetByUsername(context.Background(), "untrusteduser")
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestCompleteOAuthSignup_UntrustedWithSMTPOnUsesPendingEmail(t *testing.T) {
	t.Parallel()

	env := setupOAuthTestEnvWithAuthService(t)
	enableEmailVerification(t, env.set)
	providerID := createTestProviderWithTrustEmail(t, env.store, env.ks, false)
	signupToken := id.Generate()

	insertPendingSignup(t, env.pendingSignups, providerID, signupToken, "pending@example.com", "Pending", "sub-pending", time.Now().Add(5*time.Minute).UTC())

	resp, err := env.client.CompleteOAuthSignup(signupReq(context.Background(), &leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "pendingoauth",
	}))
	require.NoError(t, err)
	assert.True(t, resp.GetSessionReport().GetEmailVerification().GetVerificationRequired())
	assert.True(t, resp.GetSessionReport().GetEmailVerification().GetVerificationEmailSent())

	user, err := env.store.Users().GetByID(context.Background(), resp.GetSessionReport().GetUser().GetId())
	require.NoError(t, err)
	assert.Empty(t, user.Email)
	assert.False(t, user.EmailVerified)
	assert.Equal(t, "pending@example.com", user.PendingEmail)
	assert.NotEmpty(t, user.PendingEmailToken)
}

func TestCompleteOAuthSignup_InvalidToken(t *testing.T) {
	t.Parallel()

	env := setupOAuthTestEnvWithAuthService(t)

	_, err := env.client.CompleteOAuthSignup(signupReq(context.Background(), &leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: "nonexistent-token",
		Username:    "someuser",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestCompleteOAuthSignup_InvalidUsername(t *testing.T) {
	t.Parallel()

	env := setupOAuthTestEnvWithAuthService(t)
	providerID := createTestProvider(t, env.store, env.ks)
	signupToken := id.Generate()

	insertPendingSignup(t, env.pendingSignups, providerID, signupToken, "valid@example.com", "Valid", "sub-valid", time.Now().Add(5*time.Minute).UTC())

	_, err := env.client.CompleteOAuthSignup(signupReq(context.Background(), &leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "", // empty username
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestCompleteOAuthSignup_RejectsSoloAlways(t *testing.T) {
	t.Parallel()

	env := setupOAuthTestEnvWithAuthService(t)
	providerID := createTestProvider(t, env.store, env.ks)
	signupToken := id.Generate()

	insertPendingSignup(t, env.pendingSignups, providerID, signupToken, "new@example.com", "New", "sub-new", time.Now().Add(5*time.Minute).UTC())

	_, err := env.client.CompleteOAuthSignup(signupReq(context.Background(), &leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "solo",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "reserved")
}

func TestCompleteOAuthSignup_RejectsAdminInPublicSignup(t *testing.T) {
	t.Parallel()

	// setupOAuthTestEnvWithAuthService seeds the admin fixture, so this is
	// a non-setup-mode OAuth signup and the public reservation applies.
	env := setupOAuthTestEnvWithAuthService(t)

	providerID := createTestProvider(t, env.store, env.ks)
	signupToken := id.Generate()
	insertPendingSignup(t, env.pendingSignups, providerID, signupToken, "new@example.com", "New", "sub-new", time.Now().Add(5*time.Minute).UTC())

	_, err := env.client.CompleteOAuthSignup(signupReq(context.Background(), &leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "admin",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "reserved")
}

func TestCompleteOAuthSignup_TokenConsumedOnSuccess(t *testing.T) {
	t.Parallel()

	env := setupOAuthTestEnvWithAuthService(t)
	providerID := createTestProvider(t, env.store, env.ks)
	signupToken := id.Generate()

	insertPendingSignup(t, env.pendingSignups, providerID, signupToken, "consume@example.com", "Consume", "sub-consume", time.Now().Add(5*time.Minute).UTC())

	// First call succeeds.
	_, err := env.client.CompleteOAuthSignup(signupReq(context.Background(), &leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "consumeuser",
	}))
	require.NoError(t, err)

	// Second call with the same token fails.
	_, err = env.client.CompleteOAuthSignup(signupReq(context.Background(), &leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "anotheruser",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// --- Signed callback behavior tests ---

func TestOAuthCallback_NewUser_SignupDisabled(t *testing.T) {
	t.Parallel()

	env := setupOAuthTestEnvWithAuthService(t)
	require.NoError(t, settings.KeySignupEnabled.Set(t.Context(), env.set, false))
	const email, subject = "new-identity@example.com", "new-subject"
	providerID := createCallbackTestProvider(t, env.store, env.ks, email, subject, true)
	response := performLoginCallback(t, env.server, env.states, providerID, "/after-login")
	require.Equal(t, http.StatusForbidden, response.StatusCode)
	assert.Contains(t, readBody(t, response), "sign-up is disabled")
	assert.Empty(t, response.Header.Get("Location"))
	assertNoCallbackSession(t, response)
	for _, cookie := range response.Cookies() {
		assert.False(t, strings.HasPrefix(cookie.Name, auth.OAuthSignupNonceCookieName+"-"))
		assert.False(t, strings.HasPrefix(cookie.Name, auth.SecureOAuthSignupNonceCookieName+"-"))
	}
	_, err := env.store.OAuthUserLinks().Get(t.Context(), store.GetOAuthUserLinkParams{ProviderID: providerID, ProviderSubject: subject})
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = env.store.Users().GetByEmail(t.Context(), email)
	require.ErrorIs(t, err, store.ErrNotFound)
}

// The callback creates the incoming link and a session for the existing account,
// even when public signup is disabled. This test creates neither outcome.
func TestAutoLinkByVerifiedEmail(t *testing.T) {
	t.Parallel()

	for _, signupEnabled := range []bool{true, false} {
		t.Run(strconv.FormatBool(signupEnabled), func(t *testing.T) {
			env := setupOAuthTestEnvWithAuthService(t)
			require.NoError(t, settings.KeySignupEnabled.Set(t.Context(), env.set, signupEnabled))
			const email, subject = "verified-autolink@example.com", "incoming-trusted-subject"
			userID := id.Generate()
			require.NoError(t, env.store.Users().Create(t.Context(), store.CreateUserParams{
				ID: userID, Username: "verified-autolink", PasswordHash: password.PlaceholderHash,
				Email: email, EmailVerified: true,
			}))
			providerID := createCallbackTestProvider(t, env.store, env.ks, email, subject, true)
			require.Zero(t, countSessions(t, env.store, userID))
			_, err := env.store.OAuthUserLinks().Get(t.Context(), store.GetOAuthUserLinkParams{ProviderID: providerID, ProviderSubject: subject})
			require.ErrorIs(t, err, store.ErrNotFound)

			response := performLoginCallback(t, env.server, env.states, providerID, "/after-login")
			require.Equal(t, http.StatusFound, response.StatusCode, readBody(t, response))
			assert.Equal(t, "/after-login", response.Header.Get("Location"))
			link, err := env.store.OAuthUserLinks().Get(t.Context(), store.GetOAuthUserLinkParams{ProviderID: providerID, ProviderSubject: subject})
			require.NoError(t, err)
			assert.Equal(t, userID, link.UserID)
			var sessionCookie *http.Cookie
			for _, cookie := range response.Cookies() {
				if (cookie.Name == auth.CookieName || cookie.Name == auth.SecureCookieName) && cookie.Value != "" && cookie.MaxAge >= 0 {
					sessionCookie = cookie
				}
			}
			require.NotNil(t, sessionCookie)
			assertCallbackSession(t, env.store, sessionCookie.Value, userID)
		})
	}
}

// Either missing provider trust or an unverified existing address prevents
// automatic linking. Signup policy controls the resulting new-account path.
func TestAutoLinkByEmail_RequiresTrustedProviderAndVerifiedAccount(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name                         string
		trustEmail, verified, signup bool
	}{
		{"unverified with signup", true, false, true},
		{"untrusted with signup", false, true, true},
		{"unverified without signup", true, false, false},
		{"untrusted without signup", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := setupOAuthTestEnvWithAuthService(t)
			require.NoError(t, settings.KeySignupEnabled.Set(t.Context(), env.set, tc.signup))
			const email, subject = "not-autolinked@example.com", "incoming-subject"
			userID := id.Generate()
			require.NoError(t, env.store.Users().Create(t.Context(), store.CreateUserParams{
				ID: userID, Username: "not-autolinked", PasswordHash: password.PlaceholderHash,
				Email: email, EmailVerified: tc.verified,
			}))
			providerID := createCallbackTestProvider(t, env.store, env.ks, email, subject, tc.trustEmail)
			response := performLoginCallback(t, env.server, env.states, providerID, "/after-login")
			_, err := env.store.OAuthUserLinks().Get(t.Context(), store.GetOAuthUserLinkParams{ProviderID: providerID, ProviderSubject: subject})
			require.ErrorIs(t, err, store.ErrNotFound)
			assert.Zero(t, countSessions(t, env.store, userID))
			assertNoCallbackSession(t, response)
			if !tc.signup {
				require.Equal(t, http.StatusForbidden, response.StatusCode)
				assert.Contains(t, readBody(t, response), "sign-up is disabled")
				assert.Empty(t, response.Header.Get("Location"))
				return
			}
			require.Equal(t, http.StatusFound, response.StatusCode, readBody(t, response))
			location, err := response.Location()
			require.NoError(t, err)
			require.Equal(t, "/auth/idp/complete-signup", location.Path)
			token := location.Query().Get("token")
			require.NotEmpty(t, token)
			nonce := signupNonceCookie(t, response, token)
			require.NotNil(t, nonce)
			pending, ok := env.pendingSignups.Get(token)
			require.True(t, ok)
			assert.Equal(t, providerID, pending.ProviderID)
			assert.Equal(t, subject, pending.ProviderSubject)
			assert.Equal(t, email, pending.Email)
			assert.Equal(t, "/after-login", pending.RedirectURI)
			assert.NotEmpty(t, nonce.Value)
			assert.NotEqual(t, nonce.Value, pending.NonceHash)
			assert.Equal(t, hashNonceForTest(nonce.Value), pending.NonceHash)
		})
	}
}

func TestOAuthCallback_NewUser_CompletesBoundSignedSignup(t *testing.T) {
	t.Parallel()

	env := setupOAuthTestEnvWithAuthService(t)
	const email, subject = "signed-signup@example.com", "signed-signup-subject"
	providerID := createCallbackTestProvider(t, env.store, env.ks, email, subject, true)
	response := performLoginCallback(t, env.server, env.states, providerID, "/after-signup")
	require.Equal(t, http.StatusFound, response.StatusCode, readBody(t, response))
	assertNoCallbackSession(t, response)
	location, err := response.Location()
	require.NoError(t, err)
	require.Equal(t, "/auth/idp/complete-signup", location.Path)
	token := location.Query().Get("token")
	require.NotEmpty(t, token)
	nonce := signupNonceCookie(t, response, token)
	require.NotNil(t, nonce)
	require.NotEmpty(t, nonce.Value)
	pending, ok := env.pendingSignups.Get(token)
	require.True(t, ok)
	assert.Equal(t, providerID, pending.ProviderID)
	assert.Equal(t, subject, pending.ProviderSubject)
	assert.Equal(t, email, pending.Email)
	assert.Equal(t, hashNonceForTest(nonce.Value), pending.NonceHash)
	assert.NotEqual(t, nonce.Value, pending.NonceHash)
	assert.Equal(t, "/after-signup", pending.RedirectURI)

	// A browser without this flow's cookie cannot read or complete its signup.
	// Refusal must leave the actual browser's pending row available.
	for _, cookieValue := range []string{"", "another-browser"} {
		ctx, info := connect.NewClientContext(t.Context())
		if cookieValue != "" {
			cookie := *nonce
			cookie.Value = cookieValue
			info.RequestHeader().Set("Cookie", cookie.Name+"="+cookie.Value)
		}
		_, err = env.client.GetPendingOAuthSignup(ctx, &leapmuxv1.GetPendingOAuthSignupRequest{SignupToken: token})
		require.Error(t, err)
		assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
		_, err = env.client.CompleteOAuthSignup(ctx, &leapmuxv1.CompleteOAuthSignupRequest{SignupToken: token, Username: "signedsignupuser"})
		require.Error(t, err)
		assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
		_, err = env.store.Users().GetByEmail(t.Context(), email)
		assert.ErrorIs(t, err, store.ErrNotFound)
		_, err = env.store.OAuthUserLinks().Get(t.Context(), store.GetOAuthUserLinkParams{ProviderID: providerID, ProviderSubject: subject})
		assert.ErrorIs(t, err, store.ErrNotFound)
		_, ok = env.pendingSignups.Get(token)
		require.True(t, ok)
		assert.Empty(t, info.ResponseHeader().Get("Set-Cookie"))
	}

	ctx, info := connect.NewClientContext(t.Context())
	info.RequestHeader().Set("Cookie", nonce.Name+"="+nonce.Value)
	preview, err := env.client.GetPendingOAuthSignup(ctx, &leapmuxv1.GetPendingOAuthSignupRequest{SignupToken: token})
	require.NoError(t, err)
	assert.Equal(t, email, preview.GetEmail())
	assert.Equal(t, "Test OIDC", preview.GetProviderName())
	assert.Equal(t, "/after-signup", preview.GetRedirect())
	completed, err := env.client.CompleteOAuthSignup(ctx, &leapmuxv1.CompleteOAuthSignupRequest{SignupToken: token, Username: "signedsignupuser"})
	require.NoError(t, err)
	userID := completed.GetSessionReport().GetUser().GetId()
	require.NotEmpty(t, userID)
	user, err := env.store.Users().GetByID(t.Context(), userID)
	require.NoError(t, err)
	assert.Equal(t, email, user.Email)
	assert.True(t, user.EmailVerified)
	link, err := env.store.OAuthUserLinks().Get(t.Context(), store.GetOAuthUserLinkParams{ProviderID: providerID, ProviderSubject: subject})
	require.NoError(t, err)
	assert.Equal(t, userID, link.UserID)
	assertCallbackSession(t, env.store, sessionFromCookie(t, info.ResponseHeader().Get("Set-Cookie")), userID)
	_, ok = env.pendingSignups.Get(token)
	assert.False(t, ok)

	_, err = env.client.CompleteOAuthSignup(ctx, &leapmuxv1.CompleteOAuthSignupRequest{SignupToken: token, Username: "signedsignupagain"})
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	assert.Equal(t, 1, countSessions(t, env.store, userID))
}

// createCallbackTestProvider selects trust before any flow or dependent row
// exists. The shared signed fixture registers an untrusted provider, and the
// store offers no trust update, so trusted setup recreates that configuration.
func createCallbackTestProvider(t *testing.T, st store.Store, ks *keystore.Keystore, email, subject string, trustEmail bool) string {
	t.Helper()
	providerID := createTestOIDCProviderWithStubClaims(t, st, ks, email, subject)
	if !trustEmail {
		return providerID
	}
	provider, err := st.OAuthProviders().GetByID(t.Context(), providerID)
	require.NoError(t, err)
	require.NoError(t, st.OAuthProviders().Delete(t.Context(), providerID))
	require.NoError(t, st.OAuthProviders().Create(t.Context(), store.CreateOAuthProviderParams{
		ID:           provider.ID,
		ProviderType: provider.ProviderType,
		Name:         provider.Name,
		IssuerURL:    provider.IssuerURL,
		ClientID:     provider.ClientID,
		ClientSecret: provider.ClientSecret,
		Scopes:       provider.Scopes,
		TrustEmail:   true,
		Enabled:      provider.Enabled,
	}))
	return providerID
}

func performLoginCallback(t *testing.T, server *httptest.Server, states service.OAuthStateStore, providerID, redirect string) *http.Response {
	t.Helper()
	login, err := noRedirectClient().Get(server.URL + "/auth/idp/" + providerID + "/login?redirect=" + url.QueryEscape(redirect))
	require.NoError(t, err)
	t.Cleanup(func() { _ = login.Body.Close() })
	require.Equal(t, http.StatusFound, login.StatusCode)
	state := stateFromLoginResponse(t, login)
	nonce := oauthNonceCookie(t, login, state)
	require.NotNil(t, nonce)
	require.NotEmpty(t, nonce.Value)
	storedState, ok := states.Get(state)
	require.True(t, ok)
	assert.Equal(t, providerID, storedState.ProviderID)
	assert.Equal(t, redirect, storedState.RedirectURI)
	assert.NotEqual(t, nonce.Value, storedState.NonceHash)
	assert.Equal(t, hashNonceForTest(nonce.Value), storedState.NonceHash)
	response := completeCallback(t, server, providerID, state, nonce)
	_, ok = states.Get(state)
	assert.False(t, ok)
	return response
}

func assertCallbackSession(t *testing.T, st store.Store, sessionToken, userID string) {
	t.Helper()
	require.NotEmpty(t, sessionToken)
	session, err := st.Sessions().GetByID(t.Context(), hubtestutil.SessionIDFromCookie(t, sessionToken), time.Now().UTC())
	require.NoError(t, err)
	assert.Equal(t, userID, session.UserID)
	authenticated, err := auth.ValidateToken(t.Context(), st, hubtestutil.TestSecretHasher(), sessionToken)
	require.NoError(t, err)
	assert.Equal(t, userID, authenticated.ID.String())
	assert.Equal(t, 1, countSessions(t, st, userID))
}

func assertNoCallbackSession(t *testing.T, response *http.Response) {
	t.Helper()
	for _, cookie := range response.Cookies() {
		assert.NotEqual(t, auth.CookieName, cookie.Name)
		assert.NotEqual(t, auth.SecureCookieName, cookie.Name)
	}
}

// --- A provider sign-in for a consent route asks for a fresh authentication ---
//
// The redirect decides two things the account cannot: whether the provider is
// asked to re-authenticate, and whether that re-authentication counts as the
// factor the session's window stands on. A consent route needs a fresh
// authentication so a provider-only account does not make two provider round
// trips before a consent -- and for that account the re-authentication IS the
// proof. An account that holds a password proves its factor locally instead,
// so its provider sign-in opens no window.

// linkedProviderUser creates an account whose identity the stub issuer will
// assert, and links it. withPassword is the production write that makes the
// account hold a password; without it the account holds none and the provider
// is its only credential.
func linkedProviderUser(t *testing.T, st store.Store, providerID, subject, email string, withPassword bool) string {
	t.Helper()
	ctx := t.Context()
	userID := id.Generate()
	require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
		ID: userID, Username: "provideruser" + userID[:6], DisplayName: "Provider User",
		Email: email, EmailVerified: true,
		PasswordHash: password.PlaceholderHash,
	}))
	if withPassword {
		hash, err := password.Hash("theirpass")
		require.NoError(t, err)
		require.NoError(t, st.Users().UpdatePassword(ctx, store.UpdateUserPasswordParams{ID: userID, PasswordHash: hash}))
	}
	require.NoError(t, st.OAuthUserLinks().Create(ctx, store.CreateOAuthUserLinkParams{
		UserID: userid.MustNew(userID), ProviderID: providerID, ProviderSubject: subject,
	}))
	return userID
}

// beginLogin starts sign-in and returns the flow's state, its
// browser binding cookie, and the URL the browser received from the Hub.
// beginReauth starts reauthentication and returns the same fields.
func beginLogin(t *testing.T, server *httptest.Server, providerID, redirect string) (string, *http.Cookie, *url.URL) {
	t.Helper()
	target := server.URL + "/auth/idp/" + providerID + "/login"
	if redirect != "" {
		target += "?redirect=" + url.QueryEscape(redirect)
	}
	resp, err := noRedirectClient().Get(target)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, http.StatusFound, resp.StatusCode)
	authURL, err := resp.Location()
	require.NoError(t, err)
	state := authURL.Query().Get("state")
	require.NotEmpty(t, state)
	return state, oauthNonceCookie(t, resp, state), authURL
}

// callbackSessionRow reads the session the callback's Set-Cookie carries.
func callbackSessionRow(t *testing.T, st store.Store, response *http.Response) *store.UserSession {
	t.Helper()
	var token string
	for _, c := range response.Cookies() {
		if (c.Name == auth.CookieName || c.Name == auth.SecureCookieName) && c.Value != "" && c.MaxAge >= 0 {
			token = c.Value
		}
	}
	require.NotEmpty(t, token, "the callback must sign the user in")
	row, err := st.Sessions().GetByID(t.Context(), hubtestutil.SessionIDFromCookie(t, token), time.Now().UTC())
	require.NoError(t, err)
	return row
}

func TestOAuthLogin_ConsentRouteForcesARePromptAndStampsTheWindow(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		redirect string
		location string
	}{
		{"/oauth/authorize?client_id=cli", "/oauth/authorize?client_id=cli"},
		{"/oauth/%61uthorize?client_id=cli", "/oauth/%61uthorize?client_id=cli"},
		{"/%6Fauth/devi%63e", "/%6Fauth/devi%63e"},
		{"/oauth//authorize?client_id=cli", "/oauth/authorize?client_id=cli"},
		{"/handoff/../oauth/./device#done", "/oauth/device#done"},
		{"/oauth/other#done/../authorize", "/oauth/authorize"},
		{"/oauth//%61uthorize", "/oauth/%61uthorize"},
		{"/oauth/authorize?client_id=cli#done/../token", "/oauth/authorize?client_id=cli#done/../token"},
	} {
		t.Run(tc.redirect, func(t *testing.T) {
			t.Parallel()

			env := setupOAuthTestEnvWithAuthService(t)
			const email, subject = "consent-route@example.com", "consent-route-subject"
			providerID := createTestOIDCProviderWithStubClaims(t, env.store, env.ks, email, subject)
			linkedProviderUser(t, env.store, providerID, subject, email, false)

			state, nonce, authURL := beginLogin(t, env.server, providerID, tc.redirect)
			stored, ok := env.states.Get(state)
			require.True(t, ok)
			assert.Equal(t, service.OAuthStatePurposeConsentLogin, stored.Purpose)
			// The prompt asks for a fresh authentication. The maximum age
			// is the additional limit that a conforming provider enforces.
			assert.Equal(t, "login", authURL.Query().Get("prompt"))
			assert.Equal(t, strconv.Itoa(int(huboauth.ReauthMaxAge.Seconds())), authURL.Query().Get("max_age"))

			resp := completeCallback(t, env.server, providerID, state, nonce)
			require.Equal(t, http.StatusFound, resp.StatusCode, readBody(t, resp))
			assert.Equal(t, tc.location, resp.Header.Get("Location"))
			row := callbackSessionRow(t, env.store, resp)
			require.NotNil(t, row.ElevationProvenAt)
			require.NotNil(t, row.ElevationExpiresAt)
			assert.Equal(t, auth.FreshAuthenticationWindow, row.ElevationExpiresAt.Sub(*row.ElevationProvenAt))
		})
	}
}

func TestOAuthLogin_ConsentRouteForAnAccountThatHoldsAPasswordStampsNoWindow(t *testing.T) {
	t.Parallel()

	env := setupOAuthTestEnvWithAuthService(t)
	const subject = "password-holder-subject"
	providerID := createTestOIDCProviderWithStubClaims(t, env.store, env.ks, "password-holder@example.com", subject)
	linkedProviderUser(t, env.store, providerID, subject, "password-holder@example.com", true)

	state, nonce, _ := beginLogin(t, env.server, providerID, "/oauth/authorize?client_id=cli")
	resp := completeCallback(t, env.server, providerID, state, nonce)
	require.Equal(t, http.StatusFound, resp.StatusCode, readBody(t, resp))
	row := callbackSessionRow(t, env.store, resp)
	assert.Nil(t, row.ElevationProvenAt,
		"an account that holds a password proves its factor locally, so its provider sign-in opens no window")
	assert.Nil(t, row.ElevationExpiresAt)
}

// factorChangeBeforeLoginStore commits a factor change immediately before the
// callback can take the lock that protects its session insert. The store
// takes that lock in Sessions.Create for an ordinary sign-in, and the
// callback can take it earlier to protect its factor reads as well.
type factorChangeBeforeLoginStore struct {
	store.Store
	commitFactorChange func(context.Context) error
}

func (s factorChangeBeforeLoginStore) RunInUserAuthTransaction(ctx context.Context, userID userid.UserID, fn func(store.Store) error) error {
	if err := s.commitFactorChange(ctx); err != nil {
		return err
	}
	return s.Store.RunInUserAuthTransaction(ctx, userID, fn)
}

func (s factorChangeBeforeLoginStore) Sessions() store.SessionStore {
	return factorChangeBeforeLoginSessions{SessionStore: s.Store.Sessions(), commitFactorChange: s.commitFactorChange}
}

type factorChangeBeforeLoginSessions struct {
	store.SessionStore
	commitFactorChange func(context.Context) error
}

func (s factorChangeBeforeLoginSessions) Create(ctx context.Context, p store.CreateSessionParams) error {
	if err := s.commitFactorChange(ctx); err != nil {
		return err
	}
	return s.SessionStore.Create(ctx, p)
}

func TestOAuthLogin_ConsentSessionChecksFactorsUnderItsInsertLock(t *testing.T) {
	t.Parallel()

	for _, factor := range []string{"password", "passkey"} {
		t.Run(factor, func(t *testing.T) {
			t.Parallel()

			changeRequested := make(chan struct{})
			changeResult := make(chan error, 1)
			server, st, ks, _ := setupOAuthTestServerOver(t, ":4327", func(s store.Store) store.Store {
				return factorChangeBeforeLoginStore{Store: s, commitFactorChange: func(ctx context.Context) error {
					close(changeRequested)
					select {
					case err := <-changeResult:
						return err
					case <-ctx.Done():
						return ctx.Err()
					}
				}}
			})
			const email, subject = "overlap@example.com", "overlap-subject"
			providerID := createTestOIDCProviderWithStubClaims(t, st, ks, email, subject)
			userID := linkedProviderUser(t, st, providerID, subject, email, false)
			uid := userid.MustNew(userID)
			state, nonce, authURL := beginLogin(t, server, providerID, "/oauth/authorize?client_id=cli")
			require.Equal(t, "login", authURL.Query().Get("prompt"))
			before, err := st.Users().GetByID(t.Context(), userID)
			require.NoError(t, err)

			var commitFactorChange func(context.Context) error
			if factor == "password" {
				hash, err := password.Hash("new-password")
				require.NoError(t, err)
				commitFactorChange = func(ctx context.Context) error {
					return st.RunInUserAuthTransaction(ctx, uid, func(tx store.Store) error {
						if err := tx.Users().UpdatePassword(ctx, store.UpdateUserPasswordParams{ID: userID, PasswordHash: hash}); err != nil {
							return err
						}
						_, _, err := auth.RevokeAllUserCredentials(ctx, tx, uid)
						return err
					})
				}
			} else {
				_, credential, wa := prepareProofPasskey(t, st, userID)
				commitFactorChange = func(ctx context.Context) error {
					return st.RunInUserAuthTransaction(ctx, uid, func(tx store.Store) error {
						_, err := wa.StoreCredential(ctx, tx, id.Generate(), userID, credential, "First key")
						return err
					})
				}
			}
			go func() {
				select {
				case <-changeRequested:
					changeResult <- commitFactorChange(t.Context())
				case <-t.Context().Done():
				}
			}()

			resp := completeCallback(t, server, providerID, state, nonce)
			require.Equal(t, http.StatusFound, resp.StatusCode, readBody(t, resp))
			row := callbackSessionRow(t, st, resp)
			current, err := st.Users().GetByID(t.Context(), userID)
			require.NoError(t, err)
			if factor == "password" {
				assert.True(t, current.FirstCredentialExempt)
				assert.Greater(t, current.AuthGeneration, before.AuthGeneration)
			} else {
				count, err := st.PasskeyCredentials().CountByUser(t.Context(), userID)
				require.NoError(t, err)
				assert.EqualValues(t, 1, count)
			}
			assert.Equal(t, current.AuthGeneration, row.AuthGeneration)
			assert.Nil(t, row.ElevationProvenAt, "a provider sign-in cannot prove a newly registered local factor")
			assert.Nil(t, row.ElevationExpiresAt)
		})
	}
}

func TestOAuthLogin_ANonConsentRoutePromptsNothingAndStampsNoWindow(t *testing.T) {
	t.Parallel()

	server, st, ks, _ := setupOAuthTestServerOver(t, ":4327", func(s store.Store) store.Store {
		return proofReadFailingStore{Store: s, failPasskeyCount: true}
	})
	const subject = "plain-login-subject"
	providerID := createTestOIDCProviderWithStubClaims(t, st, ks, "plain-login@example.com", subject)
	linkedProviderUser(t, st, providerID, subject, "plain-login@example.com", false)

	state, nonce, authURL := beginLogin(t, server, providerID, "/")
	assert.Empty(t, authURL.Query().Get("prompt"), "an ordinary login must not force a re-prompt")
	assert.Empty(t, authURL.Query().Get("max_age"),
		"the maximum authentication age rides the same re-authentication the prompt asks for")

	resp := completeCallback(t, server, providerID, state, nonce)
	require.Equal(t, http.StatusFound, resp.StatusCode, readBody(t, resp))
	row := callbackSessionRow(t, st, resp)
	assert.Nil(t, row.ElevationProvenAt, "nothing was re-authenticated, so nothing was proven")
	assert.Nil(t, row.ElevationExpiresAt)
}

// The consent routes are the exact paths the consent handler serves. A longer
// path is a different page and /oauth/token is not a consent at all, so
// neither may ask the provider for a fresh authentication or open a window.
func TestOAuthLogin_NearMissPathsAreNotConsentRoutes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		redirect string
		location string
	}{
		{"/oauth/authorizeX", "/oauth/authorizeX"},
		{"/oauth/token", "/oauth/token"},
		{"/oauth/authorize/../token", "/oauth/token"},
		{"/oauth/authorize#done/../token", "/oauth/token"},
		{"/oauth/%2Fauthorize", "/oauth/%2Fauthorize"},
		{"/oauth/%2561uthorize", "/oauth/%2561uthorize"},
		{"//example.invalid/oauth/authorize", "/"},
	} {
		t.Run(tc.redirect, func(t *testing.T) {
			t.Parallel()
			env := setupOAuthTestEnvWithAuthService(t)
			const subject = "near-miss-subject"
			providerID := createTestOIDCProviderWithStubClaims(t, env.store, env.ks, "near-miss@example.com", subject)
			linkedProviderUser(t, env.store, providerID, subject, "near-miss@example.com", false)

			state, nonce, authURL := beginLogin(t, env.server, providerID, tc.redirect)
			stored, ok := env.states.Get(state)
			require.True(t, ok)
			assert.Equal(t, service.OAuthStatePurposeLogin, stored.Purpose)
			assert.Empty(t, authURL.Query().Get("prompt"), "a near-miss path is not a consent route")
			assert.Empty(t, authURL.Query().Get("max_age"), "a near-miss path is not a consent route")

			resp := completeCallback(t, env.server, providerID, state, nonce)
			require.Equal(t, http.StatusFound, resp.StatusCode, readBody(t, resp))
			assert.Equal(t, tc.location, resp.Header.Get("Location"))
			row := callbackSessionRow(t, env.store, resp)
			assert.Nil(t, row.ElevationProvenAt, "a near-miss path is not a consent route")
			assert.Nil(t, row.ElevationExpiresAt)
		})
	}
}

// errProofReadFailed is what the failing proof reads below return. A
// sentinel, so the one fault a test plants is the one fault the flow meets.
var errProofReadFailed = errors.New("proof read failed")

// proofReadFailingStore injects a fault inside the user-auth transaction.
// Lookups before the proof decision still read the real store.
type proofReadFailingStore struct {
	store.Store
	failUserLookup   bool
	failPasskeyCount bool
}

func (s proofReadFailingStore) RunInUserAuthTransaction(ctx context.Context, userID userid.UserID, fn func(store.Store) error) error {
	return s.Store.RunInUserAuthTransaction(ctx, userID, func(tx store.Store) error {
		s.Store = tx
		return fn(proofReadFailingTx{s})
	})
}

type proofReadFailingTx struct {
	proofReadFailingStore
}

func (s proofReadFailingTx) Users() store.UserStore {
	if !s.failUserLookup {
		return s.Store.Users()
	}
	return proofReadFailingUsers{UserStore: s.Store.Users()}
}

type proofReadFailingUsers struct {
	store.UserStore
}

func (proofReadFailingUsers) GetByID(context.Context, string) (*store.User, error) {
	return nil, errProofReadFailed
}

func (s proofReadFailingTx) PasskeyCredentials() store.PasskeyCredentialStore {
	if !s.failPasskeyCount {
		return s.Store.PasskeyCredentials()
	}
	return proofReadFailingPasskeys{PasskeyCredentialStore: s.Store.PasskeyCredentials()}
}

type proofReadFailingPasskeys struct {
	store.PasskeyCredentialStore
}

func (proofReadFailingPasskeys) CountByUser(context.Context, string) (int64, error) {
	return 0, errProofReadFailed
}

// TestOAuthLogin_AStoreFailureInTheSignInProofRefusesTheSignIn pins the
// fail-visible shape of the sign-in-proof decision.
//
// The decision reads the account twice: the user row, and the passkey count
// behind accountElevatesOnlyThroughAProvider. A failure in either read is a
// store failure and never an answer to the proof question. The sign-in must
// fail visibly rather than record SignInProvedNoFactor, which would read a
// broken lookup as "nothing was proved" and sign the user in on a silent
// downgrade.
func TestOAuthLogin_AStoreFailureInTheSignInProofRefusesTheSignIn(t *testing.T) {
	t.Parallel()

	t.Run("the account lookup fails", func(t *testing.T) {
		t.Parallel()

		server, st, ks, _ := setupOAuthTestServerOver(t, ":4327", func(s store.Store) store.Store {
			return proofReadFailingStore{Store: s, failUserLookup: true}
		})
		// The auto-link path finds the account by email. The injected fault
		// targets the later account lookup for the sign-in proof.
		const email, subject = "proof-fail-user@example.com", "proof-fail-user-subject"
		userID := id.Generate()
		require.NoError(t, st.Users().Create(t.Context(), store.CreateUserParams{
			ID: userID, Username: "proof-fail-user", PasswordHash: password.PlaceholderHash,
			Email: email, EmailVerified: true,
		}))
		providerID := createCallbackTestProvider(t, st, ks, email, subject, true)

		state, nonce, _ := beginLogin(t, server, providerID, "/oauth/authorize?client_id=cli")
		resp := completeCallback(t, server, providerID, state, nonce)
		require.Equal(t, http.StatusInternalServerError, resp.StatusCode, readBody(t, resp))
		assertNoCallbackSession(t, resp)
		assert.Zero(t, countSessions(t, st, userID), "a store failure must not sign anybody in")
	})

	t.Run("the passkey count fails", func(t *testing.T) {
		t.Parallel()

		server, st, ks, _ := setupOAuthTestServerOver(t, ":4327", func(s store.Store) store.Store {
			return proofReadFailingStore{Store: s, failPasskeyCount: true}
		})
		const subject = "proof-fail-count-subject"
		providerID := createTestOIDCProviderWithStubClaims(t, st, ks, "proof-fail-count@example.com", subject)
		userID := linkedProviderUser(t, st, providerID, subject, "proof-fail-count@example.com", false)

		state, nonce, _ := beginLogin(t, server, providerID, "/oauth/authorize?client_id=cli")
		resp := completeCallback(t, server, providerID, state, nonce)
		require.Equal(t, http.StatusInternalServerError, resp.StatusCode, readBody(t, resp))
		assertNoCallbackSession(t, resp)
		assert.Zero(t, countSessions(t, st, userID), "a store failure must not sign anybody in")
	})
}

// The store decorators below let a test observe or shape what the OAuth
// handler reads. Each one embeds the interface it decorates, so a method
// added to store.Store reaches these through the embedded value rather than
// failing to compile here.

// countingProviderReadStore counts the oauth_providers row reads one request
// makes.
type countingProviderReadStore struct {
	store.Store
	reads *atomic.Int64
}

func (s countingProviderReadStore) OAuthProviders() store.OAuthProviderStore {
	return countingProviderReads{OAuthProviderStore: s.Store.OAuthProviders(), reads: s.reads}
}

type countingProviderReads struct {
	store.OAuthProviderStore
	reads *atomic.Int64
}

func (s countingProviderReads) GetByID(ctx context.Context, id string) (*store.OAuthProvider, error) {
	s.reads.Add(1)
	return s.OAuthProviderStore.GetByID(ctx, id)
}

// TestOAuthReauth_ReadsTheProviderRowOnce pins the cost of the start leg.
//
// The leg loads the provider to REFUSE a disabled or missing one before the
// browser leaves the app, and beginOAuthFlow needs the same instance. The
// provider cache holds the built client, not the row, so
// loadEnabledProvider issues GetByID on every call -- two calls were two
// reads off two different snapshots.
func TestOAuthReauth_ReadsTheProviderRowOnce(t *testing.T) {
	t.Parallel()

	reads := &atomic.Int64{}
	server, st, ks, _ := setupOAuthTestServerOver(t, ":4327", func(s store.Store) store.Store {
		return countingProviderReadStore{Store: s, reads: reads}
	})
	providerID := createTestProvider(t, st, ks)
	cookie, _, _ := reauthSession(t, st, providerID, "sub-1")

	reads.Store(0)
	state, nonce, _ := beginReauth(t, server, providerID, cookie, "")
	require.NotEmpty(t, state)
	require.NotNil(t, nonce)

	assert.EqualValues(t, 1, reads.Load(), "the start leg must read the provider row once")
}

// purposeRewritingStates gives the handler a state row whose purpose is a
// value the store refuses to create, which is the only way to drive the
// unknown-purpose branch.
type purposeRewritingStates struct {
	service.OAuthStateStore
	purpose service.OAuthStatePurpose
}

func (s purposeRewritingStates) Get(state string) (*service.OAuthState, bool) {
	row, ok := s.OAuthStateStore.Get(state)
	if !ok {
		return nil, false
	}
	rewritten := *row
	rewritten.Purpose = s.purpose
	return &rewritten, true
}

// TestOAuthCallback_RefusesAnUnknownPurpose pins the fail-CLOSED shape of
// the branch that picks the finishing leg.
//
// The login leg creates: a session, an account, an identity link. It must
// never be the branch a value reaches by not matching something else -- and
// the Go zero value, UNSPECIFIED, is exactly such a value, so a row that lost
// its purpose used to sign somebody in.
func TestOAuthCallback_RefusesAnUnknownPurpose(t *testing.T) {
	t.Parallel()

	// The unset zero, an ordinal above the range, and a negative one: the three
	// shapes a value that is not LOGIN, CONSENT_LOGIN or REAUTH can take.
	for _, purpose := range []service.OAuthStatePurpose{
		service.OAuthStatePurposeUnspecified, 4, -1,
	} {
		t.Run("purpose "+strconv.Itoa(int(purpose)), func(t *testing.T) {
			t.Parallel()

			env := setupOAuthTestEnvOver(t, ":4327", nil, func(s service.OAuthStateStore) service.OAuthStateStore {
				return purposeRewritingStates{OAuthStateStore: s, purpose: purpose}
			})
			providerID := createTestOIDCProviderWithStubClaims(t, env.store, env.ks, "newcomer@example.com", "sub-new")

			loginResp, err := noRedirectClient().Get(env.server.URL + "/auth/idp/" + providerID + "/login")
			require.NoError(t, err)
			defer func() { _ = loginResp.Body.Close() }()
			state := stateFromLoginResponse(t, loginResp)
			flowCookie := oauthNonceCookie(t, loginResp, state)
			require.NotNil(t, flowCookie)

			req, err := http.NewRequest(http.MethodGet,
				env.server.URL+"/auth/idp/"+providerID+"/callback?code=c&state="+state, nil)
			require.NoError(t, err)
			req.AddCookie(flowCookie)
			resp, err := noRedirectClient().Do(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			assert.Equal(t, http.StatusBadRequest, resp.StatusCode, readBody(t, resp))
			assert.Empty(t, resp.Header.Get("Location"),
				"an unrecognised purpose must not reach the sign-up hand-off")
			for _, c := range resp.Cookies() {
				assert.NotEqual(t, auth.CookieName, c.Name, "and must not sign anybody in")
			}
		})
	}
}

// stateReadBarrier holds every caller of OAuthStateStore.Get until `parties`
// of them arrive, so two callbacks provably hold the SAME state row before
// either one consumes it.
type stateReadBarrier struct {
	mu        sync.Mutex
	remaining int
	released  chan struct{}
}

func newStateReadBarrier(parties int) *stateReadBarrier {
	return &stateReadBarrier{remaining: parties, released: make(chan struct{})}
}

// arrive blocks until the last party arrives. It stops after a generous
// deadline so a regression that stops one caller reaching the row fails the
// test instead of hanging the whole package.
func (b *stateReadBarrier) arrive(t *testing.T) {
	t.Helper()
	b.mu.Lock()
	b.remaining--
	if b.remaining == 0 {
		close(b.released)
	}
	b.mu.Unlock()
	select {
	case <-b.released:
	case <-time.After(30 * time.Second):
		t.Error("the callbacks never met at the state row")
	}
}

type barrierStates struct {
	service.OAuthStateStore
	t       *testing.T
	barrier *stateReadBarrier
}

func (s barrierStates) Get(state string) (*service.OAuthState, bool) {
	row, ok := s.OAuthStateStore.Get(state)
	s.barrier.arrive(s.t)
	return row, ok
}

// TestOAuthCallback_ConcurrentCallbacksSpendTheStateOnce pins the single use
// of a flow as the HUB's property.
//
// Two callbacks that carry the same state and the same nonce cookie -- a
// double-clicked callback, a browser prefetch of the Location header, a
// retried navigation -- both pass the nonce check and both reach the delete.
// The delete's row count is what distinguishes them. Without it both
// continued to provider.Exchange, and on the reauth purpose both reached the
// elevation grant; the property then rested on the identity provider
// rejecting the second use of its authorization code, which this hub does
// not control.
// The stub provider here accepts the code twice, exactly like an identity
// provider that is lenient about a retry.
func TestOAuthCallback_ConcurrentCallbacksSpendTheStateOnce(t *testing.T) {
	t.Parallel()

	barrier := newStateReadBarrier(2)
	env := setupOAuthTestEnvOver(t, ":4327", nil, func(s service.OAuthStateStore) service.OAuthStateStore {
		return barrierStates{OAuthStateStore: s, t: t, barrier: barrier}
	})
	providerID := createTestOIDCProviderWithStubClaims(t, env.store, env.ks, "user@example.com", "sub-1")
	cookie, _, _ := reauthSession(t, env.store, providerID, "sub-1")

	state, nonce, _ := beginReauth(t, env.server, providerID, cookie, "/workspace/1")
	require.NotNil(t, nonce)

	statuses := make([]int, 2)
	var wg sync.WaitGroup
	for i := range statuses {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, err := http.NewRequest(http.MethodGet,
				env.server.URL+"/auth/idp/"+providerID+"/callback?code=test&state="+state, nil)
			if !assert.NoError(t, err) {
				return
			}
			req.AddCookie(nonce)
			resp, err := noRedirectClient().Do(req)
			if !assert.NoError(t, err) {
				return
			}
			defer func() { _ = resp.Body.Close() }()
			statuses[i] = resp.StatusCode
		}()
	}
	wg.Wait()

	assert.ElementsMatch(t, []int{http.StatusFound, http.StatusBadRequest}, statuses,
		"exactly one of two racing callbacks may spend the state")
}

// idpAuthReadProbe injects errors only at the reads the HTTP handlers own.
// Fixture writes and assertions use the undecorated store.
type idpAuthReadProbe struct {
	providerError error
	sessionError  error
	providerCalls atomic.Int64
	sessionCalls  atomic.Int64
}

type idpAuthReadStore struct {
	store.Store
	probe *idpAuthReadProbe
}

func (s idpAuthReadStore) OAuthProviders() store.OAuthProviderStore {
	return idpAuthProviderReads{OAuthProviderStore: s.Store.OAuthProviders(), probe: s.probe}
}

func (s idpAuthReadStore) Sessions() store.SessionStore {
	return idpAuthSessionReads{SessionStore: s.Store.Sessions(), probe: s.probe}
}

type idpAuthProviderReads struct {
	store.OAuthProviderStore
	probe *idpAuthReadProbe
}

func (s idpAuthProviderReads) GetByID(ctx context.Context, providerID string) (*store.OAuthProvider, error) {
	s.probe.providerCalls.Add(1)
	if s.probe.providerError != nil {
		return nil, s.probe.providerError
	}
	return s.OAuthProviderStore.GetByID(ctx, providerID)
}

type idpAuthSessionReads struct {
	store.SessionStore
	probe *idpAuthReadProbe
}

func (s idpAuthSessionReads) ValidateWithUser(ctx context.Context, sessionID string, now time.Time) (*store.SessionWithUser, error) {
	s.probe.sessionCalls.Add(1)
	if s.probe.sessionError != nil {
		return nil, s.probe.sessionError
	}
	return s.SessionStore.ValidateWithUser(ctx, sessionID, now)
}

func TestIdPProviderLookup_DistinguishesAbsenceFromStoreFailure(t *testing.T) {
	// CaptureDefaultLogger changes process state, so this test cannot run in parallel.
	logs := utiltest.CaptureDefaultLogger(t)
	const marker = "private-provider-store-failure"
	for _, action := range []string{"login", "reauth", "callback"} {
		for _, outcome := range []struct {
			name     string
			lookup   error
			disabled bool
			status   int
			body     string
		}{
			{name: "absent", lookup: fmt.Errorf("provider lookup: %w", store.ErrNotFound), status: http.StatusNotFound, body: "unknown provider\n"},
			{name: "store failure", lookup: errors.New(marker), status: http.StatusInternalServerError, body: "internal server error\n"},
			{name: "disabled", disabled: true, status: http.StatusForbidden, body: "provider disabled\n"},
			{name: "enabled", status: http.StatusFound},
		} {
			t.Run(action+"/"+outcome.name, func(t *testing.T) {
				probe := &idpAuthReadProbe{providerError: outcome.lookup}
				env := setupOAuthTestEnvOver(t, ":4327", func(st store.Store) store.Store {
					return idpAuthReadStore{Store: st, probe: probe}
				}, nil)
				server, st, ks := env.server, env.store, env.ks
				providerID := ""
				if action == "callback" && outcome.status == http.StatusFound {
					// A local signed-token provider completes the positive callback.
					providerID = createTestOIDCProviderWithStubClaims(t, st, ks, "lookup-control@example.test", "lookup-control")
				} else {
					providerID = createTestProvider(t, st, ks)
				}
				if outcome.disabled {
					require.NoError(t, st.OAuthProviders().UpdateEnabled(context.Background(), store.UpdateOAuthProviderEnabledParams{
						ID: providerID, Enabled: false,
					}))
				}
				target := server.URL + "/auth/idp/" + providerID + "/" + action
				var cookie *http.Cookie
				var state string
				if action == "reauth" {
					cookie, _, _ = reauthSession(t, st, providerID, "lookup-control")
				}
				if action == "callback" {
					if outcome.status == http.StatusFound {
						login, err := noRedirectClient().Get(server.URL + "/auth/idp/" + providerID + "/login")
						require.NoError(t, err)
						require.Equal(t, http.StatusFound, login.StatusCode)
						state = stateFromLoginResponse(t, login)
						cookie = oauthNonceCookie(t, login, state)
						require.NoError(t, login.Body.Close())
					} else {
						const nonce = "provider-lookup-browser-binding"
						state = seedOAuthState(t, env.states, providerID, hashNonceForTest(nonce))
						cookie = auth.BuildOAuthNonceCookie(state, nonce, time.Minute, false)
					}
					require.NotNil(t, cookie)
					target += "?code=test&state=" + state
				}
				req, err := http.NewRequest(http.MethodGet, target, nil)
				require.NoError(t, err)
				if cookie != nil {
					req.AddCookie(cookie)
				}
				probe.providerCalls.Store(0)
				logs.Reset()
				resp, err := noRedirectClient().Do(req)
				require.NoError(t, err)
				defer func() { _ = resp.Body.Close() }()
				body := readBody(t, resp)
				assert.Equal(t, outcome.status, resp.StatusCode, body)
				assert.Equal(t, int64(1), probe.providerCalls.Load(), "the intended provider lookup must run")
				assert.NotContains(t, body, marker)
				assert.NotContains(t, resp.Header.Get("Location"), marker)
				if outcome.status == http.StatusFound {
					assert.NotEmpty(t, resp.Header.Get("Location"))
				} else {
					assert.Equal(t, outcome.body, body)
					assert.Empty(t, resp.Header.Get("Location"))
				}
				if outcome.status == http.StatusInternalServerError {
					assert.Contains(t, logs.String(), "identity provider lookup failed")
					assert.Equal(t, 1, strings.Count(logs.String(), marker), "log the cause exactly once")
				} else {
					assert.NotContains(t, logs.String(), "identity provider lookup failed")
				}
				if action == "callback" {
					_, ok := env.states.Get(state)
					assert.False(t, ok, "browser-bound state is consumed before provider loading")
					assert.NotEmpty(t, resp.Cookies(), "the callback must clear its nonce even when provider loading fails")
				}
				expectedStates := 0
				if action != "callback" && outcome.status == http.StatusFound {
					expectedStates = 1
				} else if action != "callback" {
					assert.Empty(t, resp.Cookies(), "a provider refusal must not issue flow state")
				}
				assert.Equal(t, expectedStates, countOAuthStates(t, env.states))
			})
		}
	}
}

func TestOAuthReauth_SeparatesCredentialRefusalFromStoreFailure(t *testing.T) {
	// CaptureDefaultLogger changes process state, so this test cannot run in parallel.
	logs := utiltest.CaptureDefaultLogger(t)
	const marker = "private-session-store-failure"
	for _, tc := range []struct {
		name         string
		lookup       error
		cookie       string
		crossSite    bool
		status       int
		sessionReads int64
	}{
		{name: "missing cookie", lookup: errors.New(marker), cookie: "missing", status: http.StatusUnauthorized},
		{name: "malformed cookie", lookup: errors.New(marker), cookie: "malformed", status: http.StatusUnauthorized},
		{name: "unknown session", cookie: "unknown", status: http.StatusUnauthorized, sessionReads: 1},
		{name: "wrong secret", cookie: "wrong", status: http.StatusUnauthorized, sessionReads: 1},
		{name: "expired session", cookie: "expired", status: http.StatusUnauthorized, sessionReads: 1},
		{name: "wrapped absence", lookup: fmt.Errorf("session lookup: %w", store.ErrNotFound), status: http.StatusUnauthorized, sessionReads: 1},
		{name: "store failure", lookup: errors.New(marker), status: http.StatusInternalServerError, sessionReads: 1},
		{name: "cross-site navigation", lookup: errors.New(marker), crossSite: true, status: http.StatusForbidden},
		{name: "valid session", status: http.StatusFound, sessionReads: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := &idpAuthReadProbe{sessionError: tc.lookup}
			env := setupOAuthTestEnvOver(t, ":4327", func(st store.Store) store.Store {
				return idpAuthReadStore{Store: st, probe: probe}
			}, nil)
			server, st, ks := env.server, env.store, env.ks
			providerID := createTestProvider(t, st, ks)
			cookie, userID, sessionID := reauthSession(t, st, providerID, "reauth-control")
			switch tc.cookie {
			case "missing":
				cookie = nil
			case "malformed":
				cookie.Value = "not-a-session-token"
			case "unknown":
				cookie.Value = auth.FormatSessionToken(id.Generate(), id.Generate())
			case "wrong":
				cookie.Value = auth.FormatSessionToken(sessionID, id.Generate())
			case "expired":
				expiredID, secret := id.Generate(), id.Generate()
				require.NoError(t, st.Sessions().Create(context.Background(), store.CreateSessionParams{
					ID: expiredID, UserID: userid.MustNew(userID), SecretHash: hubtestutil.TestSecretHasher().Hash(secret),
					ExpiresAt: time.Now().Add(-time.Hour),
				}))
				cookie.Value = auth.FormatSessionToken(expiredID, secret)
			}
			req, err := http.NewRequest(http.MethodGet, server.URL+"/auth/idp/"+providerID+"/reauth", nil)
			require.NoError(t, err)
			if cookie != nil {
				req.AddCookie(cookie)
			}
			if tc.crossSite {
				req.Header.Set("Sec-Fetch-Site", "cross-site")
			}
			logs.Reset()
			resp, err := noRedirectClient().Do(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			body := readBody(t, resp)
			assert.Equal(t, tc.status, resp.StatusCode, body)
			assert.Equal(t, tc.sessionReads, probe.sessionCalls.Load())
			assert.NotContains(t, body, marker)
			assert.NotContains(t, resp.Header.Get("Location"), marker)
			if tc.status == http.StatusFound {
				assert.Equal(t, int64(1), probe.providerCalls.Load())
				assert.NotEmpty(t, stateFromLoginResponse(t, resp))
				assert.Equal(t, 1, countOAuthStates(t, env.states))
			} else {
				assert.Zero(t, probe.providerCalls.Load(), "authentication must finish before provider loading")
				assert.Empty(t, resp.Header.Get("Location"))
				assert.Empty(t, resp.Cookies())
				assert.Zero(t, countOAuthStates(t, env.states), "a refusal must not issue flow state")
			}
			switch tc.status {
			case http.StatusInternalServerError:
				assert.Equal(t, "internal server error\n", body)
				assert.Contains(t, logs.String(), "identity provider reauthentication failed")
				assert.Equal(t, 1, strings.Count(logs.String(), marker), "log the cause exactly once")
			case http.StatusUnauthorized:
				assert.Equal(t, "sign in first: there is no session to verify\n", body)
			}
			if tc.status != http.StatusInternalServerError {
				assert.NotContains(t, logs.String(), "identity provider reauthentication failed")
			}
		})
	}
}

func TestCompleteOAuthSignupRequiresALiveExistingUser(t *testing.T) {
	for _, populated := range []bool{false, true} {
		t.Run(fmt.Sprintf("populated=%t", populated), func(t *testing.T) {
			base := hubtestutil.OpenTestStore(t)
			if populated {
				createRetainedAdministrator(t, base)
			}
			observed := &observedAdmissionStore{Store: base}
			env := setupOAuthSignupEnv(t, observed)
			server, client, st, ks := env.server, env.client, env.store, env.ks
			infoClient := newAuthTestClient(server.Client(), server.URL)
			infoResponse, err := infoClient.GetSystemInfo(t.Context(), &leapmuxv1.GetSystemInfoRequest{})
			require.NoError(t, err)
			require.Equal(t, !populated, infoResponse.GetSetupRequired())
			const email, subject = "pending-before-setup@example.com", "pending-before-setup"
			providerID := createCallbackTestProvider(t, st, ks, email, subject, true)
			response := performLoginCallback(t, server, env.states, providerID, "/after-signup")
			require.Equal(t, http.StatusFound, response.StatusCode)
			location, err := response.Location()
			require.NoError(t, err)
			token := location.Query().Get("token")
			require.NotEmpty(t, token)
			nonce := signupNonceCookie(t, response, token)
			require.NotNil(t, nonce)
			hasUsers, err := st.Users().HasAny(t.Context())
			require.NoError(t, err)
			require.Equal(t, populated, hasUsers)
			if populated {
				admin, err := st.Users().GetFirstAdmin(t.Context())
				require.NoError(t, err)
				require.ErrorIs(t, st.Users().Delete(t.Context(), admin.ID), store.ErrLastAdministrator)
			}
			observed.reads.Store(0)
			ctx, info := connect.NewClientContext(t.Context())
			info.RequestHeader().Set("Cookie", nonce.Name+"="+nonce.Value)
			completed, err := client.CompleteOAuthSignup(ctx, &leapmuxv1.CompleteOAuthSignupRequest{SignupToken: token, Username: "ordinaryoauth"})
			if populated {
				require.NoError(t, err)
				assert.False(t, completed.GetSessionReport().GetUser().GetIsAdmin())
				user, err := base.Users().GetByUsername(t.Context(), "ordinaryoauth")
				require.NoError(t, err)
				assert.False(t, user.IsAdmin)
				link, err := base.OAuthUserLinks().Get(t.Context(), store.GetOAuthUserLinkParams{ProviderID: providerID, ProviderSubject: subject})
				require.NoError(t, err)
				assert.Equal(t, user.ID, link.UserID)
				_, ok := env.pendingSignups.Get(token)
				assert.False(t, ok)
				assert.Zero(t, observed.reads.Load())
				return
			}
			require.Error(t, err, "ordinary OAuth signup must require an existing live account")
			assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
			assert.Nil(t, completed)
			assert.Empty(t, info.ResponseHeader().Values("Set-Cookie"))
			_, err = st.Users().GetByUsername(t.Context(), "ordinaryoauth")
			require.ErrorIs(t, err, store.ErrNotFound)
			_, err = st.OAuthUserLinks().Get(t.Context(), store.GetOAuthUserLinkParams{ProviderID: providerID, ProviderSubject: subject})
			require.ErrorIs(t, err, store.ErrNotFound)
			_, ok := env.pendingSignups.Get(token)
			require.True(t, ok, "refused admission must retain the bound pending signup")
		})
	}
}

func TestIdPHandler_Callback_FormPostAndAppleUserName(t *testing.T) {
	base := hubtestutil.OpenTestStore(t)
	createRetainedAdministrator(t, base)
	env := setupOAuthSignupEnv(t, base)

	nowAuthTime := func() any { return time.Now().Unix() }

	// PUT must be rejected with 405 Method Not Allowed.
	providerEmptyName := createTestOIDCProviderWithMode(t, env.store, env.ks, "apple-user@example.com", "apple-sub-1", "", leapmuxv1.OAuthResponseMode_O_AUTH_RESPONSE_MODE_FORM_POST, nowAuthTime)
	putReq, err := http.NewRequest(http.MethodPut, env.server.URL+"/auth/idp/"+providerEmptyName+"/callback", nil)
	require.NoError(t, err)
	putResp, err := noRedirectClient().Do(putReq)
	require.NoError(t, err)
	_ = putResp.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, putResp.StatusCode)

	// Oversized POST body (> 64 KiB) must fail with 413 Request Entity Too Large.
	bigBody := "code=x&state=y&user=" + strings.Repeat("a", 65<<10)
	bigReq, err := http.NewRequest(http.MethodPost, env.server.URL+"/auth/idp/"+providerEmptyName+"/callback", strings.NewReader(bigBody))
	require.NoError(t, err)
	bigReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	bigResp, err := noRedirectClient().Do(bigReq)
	require.NoError(t, err)
	_ = bigResp.Body.Close()
	assert.Equal(t, http.StatusRequestEntityTooLarge, bigResp.StatusCode)

	// POST callback must ignore query parameters and read only the POST body.
	queryOnlyReq, err := http.NewRequest(http.MethodPost, env.server.URL+"/auth/idp/"+providerEmptyName+"/callback?code=q&state=s", strings.NewReader(""))
	require.NoError(t, err)
	queryOnlyReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	queryOnlyResp, err := noRedirectClient().Do(queryOnlyReq)
	require.NoError(t, err)
	_ = queryOnlyResp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, queryOnlyResp.StatusCode)

	for _, tc := range []struct {
		name         string
		oidcName     string
		responseMode leapmuxv1.OAuthResponseMode
		querySuffix  string
		wantDisplay  string
	}{
		{
			name:         "empty OIDC name falls back to Apple user JSON even with malformed query string",
			oidcName:     "",
			responseMode: leapmuxv1.OAuthResponseMode_O_AUTH_RESPONSE_MODE_FORM_POST,
			querySuffix:  "?%zz=1",
			wantDisplay:  "Jane Appleseed",
		},
		{
			name:         "whitespace-only OIDC name falls back to Apple user JSON",
			oidcName:     "   \t  ",
			responseMode: leapmuxv1.OAuthResponseMode_O_AUTH_RESPONSE_MODE_FORM_POST,
			wantDisplay:  "Jane Appleseed",
		},
		{
			name:         "non-empty OIDC name takes precedence over Apple user JSON",
			oidcName:     "Provider Name",
			responseMode: leapmuxv1.OAuthResponseMode_O_AUTH_RESPONSE_MODE_FORM_POST,
			wantDisplay:  "Provider Name",
		},
		{
			name:         "query-mode OIDC provider ignores Apple user JSON",
			oidcName:     "",
			responseMode: leapmuxv1.OAuthResponseMode_O_AUTH_RESPONSE_MODE_QUERY,
			wantDisplay:  "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			providerID := createTestOIDCProviderWithMode(t, env.store, env.ks, "apple-user@example.com", "apple-sub-"+tc.name, tc.oidcName, tc.responseMode, nowAuthTime)
			state := "apple-state-" + id.Generate()
			nonce := "apple-nonce-" + id.Generate()
			require.NoError(t, env.states.Create(service.CreateOAuthStateParams{
				State:        state,
				ProviderID:   providerID,
				PkceVerifier: "verifier",
				NonceHash:    hashNonceForTest(nonce),
				Purpose:      service.OAuthStatePurposeLogin,
				ExpiresAt:    time.Now().Add(5 * time.Minute).UTC(),
			}))

			form := url.Values{}
			form.Set("code", "apple-code")
			form.Set("state", state)
			form.Set("user", `{"name":{"firstName":"Jane","lastName":"Appleseed"},"email":"apple-user@example.com"}`)
			req, err := http.NewRequest(http.MethodPost, env.server.URL+"/auth/idp/"+providerID+"/callback"+tc.querySuffix, strings.NewReader(form.Encode()))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.AddCookie(auth.BuildOAuthNonceCookieForFormPost(state, nonce, time.Minute, false))

			resp, err := noRedirectClient().Do(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			require.Equal(t, http.StatusFound, resp.StatusCode, readBody(t, resp))
			loc, err := resp.Location()
			require.NoError(t, err)
			assert.Equal(t, service.IdPCompleteSignupPath, loc.Path)
			token := loc.Query().Get("token")
			require.NotEmpty(t, token)

			pending, ok := env.pendingSignups.Get(token)
			require.True(t, ok)
			assert.Equal(t, tc.wantDisplay, pending.DisplayName)
		})
	}
}
