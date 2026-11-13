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

func setupOAuthTestServer(t *testing.T) (*httptest.Server, store.Store, *keystore.Keystore) {
	t.Helper()
	server, st, ks, _ := setupOAuthTestServerWithSettings(t)
	return server, st, ks
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

	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	err = st.Migrator().Migrate(context.Background())
	require.NoError(t, err)

	hubtestutil.CreateTestAdmin(t, st)

	key, err := keystore.GenerateKey()
	require.NoError(t, err)
	ks, err := keystore.New(map[uint32][32]byte{1: key})
	require.NoError(t, err)

	cfg := &config.Config{
		Listen: bindSetForTest(t, listen),
	}
	set := servicetest.NewSettingsManager(t, st, ks)
	enableSignup(t, set)

	handlerStore := st
	if wrap != nil {
		handlerStore = wrap(st)
	}
	idpHandler := service.NewIdPHandler(handlerStore, hubtestutil.TestSecretHasher(), cfg, set, auth.NewCredentialLifecycleEffects(nil, nil, nil), ks)

	mux := http.NewServeMux()
	idpHandler.RegisterRoutes(mux)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return server, st, ks, set
}

func createTestProvider(t *testing.T, st store.Store, ks *keystore.Keystore) string {
	return createTestProviderWithTrustEmail(t, st, ks, true)
}

func createTestProviderWithTrustEmail(t *testing.T, st store.Store, ks *keystore.Keystore, trustEmail bool) string {
	t.Helper()
	providerID := id.Generate()
	aad := keystore.ProviderAAD(providerID)
	encSecret, err := ks.Encrypt([]byte("test-client-secret"), aad)
	require.NoError(t, err)

	err = st.OAuthProviders().Create(context.Background(), store.CreateOAuthProviderParams{
		ID:           providerID,
		ProviderType: leapmuxv1.IdentityProviderType_IDENTITY_PROVIDER_TYPE_GITHUB,
		Name:         "Test GitHub",
		ClientID:     "test-client-id",
		ClientSecret: encSecret,
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

	server, st, ks := setupOAuthTestServer(t)
	providerID := createTestProvider(t, st, ks)

	const nonce = "expired-flow-nonce"
	// Create an already-expired state.
	err := st.OAuthStates().Create(context.Background(), store.CreateOAuthStateParams{
		State:        "expired-state",
		ProviderID:   providerID,
		PkceVerifier: "test-verifier",
		NonceHash:    hashNonceForTest(nonce),
		Purpose:      store.OAuthStatePurposeLogin,
		ExpiresAt:    time.Now().Add(-1 * time.Minute).UTC(),
	})
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodGet,
		server.URL+"/auth/idp/"+providerID+"/callback?code=test&state=expired-state", nil)
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

	server, st, ks := setupOAuthTestServer(t)
	providerID := createTestProvider(t, st, ks)

	const nonce = "mismatch-flow-nonce"
	state := seedOAuthState(t, st, providerID, hashNonceForTest(nonce))

	// A SECOND provider's callback route. The mismatch check runs before
	// the handler resolves the provider, so the row's own provider is what
	// the guard compares against.
	other := createTestProvider(t, st, ks)
	require.NotEqual(t, providerID, other)
	req, err := http.NewRequest(http.MethodGet,
		server.URL+"/auth/idp/"+other+"/callback?code=test&state="+state, nil)
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
	aad := keystore.ProviderAAD(disabledID)
	encSecret, _ := ks.Encrypt([]byte("secret"), aad)
	_ = st.OAuthProviders().Create(context.Background(), store.CreateOAuthProviderParams{
		ID:           disabledID,
		ProviderType: leapmuxv1.IdentityProviderType_IDENTITY_PROVIDER_TYPE_OIDC,
		Name:         "Disabled OIDC",
		ClientID:     "disabled-client",
		ClientSecret: encSecret,
		Scopes:       "openid",
		Enabled:      false,
	})

	providers, err := st.OAuthProviders().ListEnabled(context.Background())
	require.NoError(t, err)

	// The listing should show only the enabled provider.
	assert.Len(t, providers, 1)
	assert.Equal(t, enabledID, providers[0].ID)
}

func TestOAuthTokenStorage_EncryptedInDB(t *testing.T) {
	t.Parallel()

	_, _, ks := setupOAuthTestServer(t)

	plainAccess := "access-token-plaintext"
	plainRefresh := "refresh-token-plaintext"
	userID := "test-user"
	providerID := "test-provider"

	accessAAD := keystore.AccessTokenAAD(userID, providerID)
	refreshAAD := keystore.RefreshTokenAAD(userID, providerID)

	encAccess, err := ks.Encrypt([]byte(plainAccess), accessAAD)
	require.NoError(t, err)
	encRefresh, err := ks.Encrypt([]byte(plainRefresh), refreshAAD)
	require.NoError(t, err)

	// Verify ciphertext is different from plaintext.
	assert.NotEqual(t, []byte(plainAccess), encAccess)
	assert.NotEqual(t, []byte(plainRefresh), encRefresh)

	// Verify decryption returns original values.
	gotAccess, err := ks.Decrypt(encAccess, accessAAD)
	require.NoError(t, err)
	assert.Equal(t, plainAccess, string(gotAccess))

	gotRefresh, err := ks.Decrypt(encRefresh, refreshAAD)
	require.NoError(t, err)
	assert.Equal(t, plainRefresh, string(gotRefresh))

	// Verify wrong AAD fails.
	_, err = ks.Decrypt(encAccess, []byte("wrong-aad"))
	assert.Error(t, err)
}

func TestOAuthTokenStorage_KeyVersionMatches(t *testing.T) {
	t.Parallel()

	_, _, ks := setupOAuthTestServer(t)

	ct, err := ks.Encrypt([]byte("test"), nil)
	require.NoError(t, err)

	ver, err := keystore.CiphertextVersion(ct)
	require.NoError(t, err)
	assert.Equal(t, ks.ActiveVersion(), ver)
}

// setupOAuthTestServerWithAuthService sets up both IdPHandler (HTTP routes) and
// AuthService (ConnectRPC) on a single test server, enabling tests that exercise
// the pending-signup → complete-signup flow via RPC.
func setupOAuthTestServerWithAuthService(t *testing.T) (
	*httptest.Server,
	leapmuxv1connect.AuthServiceClient,
	store.Store,
	*keystore.Keystore,
	*settings.Manager,
) {
	t.Helper()

	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	err = st.Migrator().Migrate(context.Background())
	require.NoError(t, err)

	hubtestutil.CreateTestAdmin(t, st)

	key, err := keystore.GenerateKey()
	require.NoError(t, err)
	ks, err := keystore.New(map[uint32][32]byte{1: key})
	require.NoError(t, err)

	cfg := &config.Config{
		Listen: []string{":4327"},
	}
	set := servicetest.NewSettingsManager(t, st, ks)
	enableSignup(t, set)

	mux := http.NewServeMux()

	// Register OAuth HTTP routes.
	idpHandler := service.NewIdPHandler(st, hubtestutil.TestSecretHasher(), cfg, set, auth.NewCredentialLifecycleEffects(nil, nil, nil), ks)
	idpHandler.RegisterRoutes(mux)

	// Register AuthService ConnectRPC routes.
	interceptor, registry := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{SessionHasher: hubtestutil.TestSecretHasher(), Store: st})
	t.Cleanup(registry.Stop)
	authDeps := servicetest.AuthServiceDeps(st, cfg, set, auth.NewCredentialLifecycleEffects(nil, nil, nil))
	authDeps.Keystore = ks
	authSvc := service.NewAuthService(authDeps)
	connectServer := connect.NewServer(interceptor)
	leapmuxv1connect.RegisterAuthServiceHandler(connectServer, authSvc)
	connecthttp.Mount(mux, connectServer, connecthttp.WithReadMaxBytes(0))

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewAuthServiceClient(connect.NewClient(connecthttp.NewTransport(server.Client(), server.URL, connecthttp.WithReadMaxBytes(0))))
	return server, client, st, ks, set
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

// insertPendingSignup creates a pending_oauth_signups row with encrypted tokens
// and no redirect.
func insertPendingSignup(t *testing.T, st store.Store, ks *keystore.Keystore, providerID, token, email, displayName, subject string, expiresAt time.Time) {
	t.Helper()
	seedPendingSignup(t, st, ks, store.CreatePendingOAuthSignupParams{
		Token:           token,
		ProviderID:      providerID,
		ProviderSubject: subject,
		Email:           email,
		DisplayName:     displayName,
		ExpiresAt:       expiresAt,
	})
}

// seedPendingSignup creates the pending_oauth_signups row that p describes. It
// fills what every seeded row shares, as the OAuth callback would: the
// encrypted token pair, the token type and expiry, the active key version, and
// the hash of testSignupNonce.
func seedPendingSignup(t *testing.T, st store.Store, ks *keystore.Keystore, p store.CreatePendingOAuthSignupParams) {
	t.Helper()
	encAccess, err := ks.Encrypt([]byte("mock-access-token"), keystore.AccessTokenAAD(p.Token, p.ProviderID))
	require.NoError(t, err)
	encRefresh, err := ks.Encrypt([]byte("mock-refresh-token"), keystore.RefreshTokenAAD(p.Token, p.ProviderID))
	require.NoError(t, err)

	p.NonceHash = hashNonceForTest(testSignupNonce)
	p.AccessToken = encAccess
	p.RefreshToken = encRefresh
	p.TokenType = "bearer"
	p.TokenExpiresAt = time.Now().Add(1 * time.Hour).UTC()
	p.KeyVersion = int64(ks.ActiveVersion())
	require.NoError(t, st.PendingOAuthSignups().Create(context.Background(), p))
}

// --- GetPendingOAuthSignup RPC tests ---

func TestGetPendingOAuthSignup_Success(t *testing.T) {
	t.Parallel()

	_, client, st, ks, _ := setupOAuthTestServerWithAuthService(t)
	providerID := createTestProvider(t, st, ks)
	signupToken := id.Generate()

	insertPendingSignup(t, st, ks, providerID, signupToken, "alice@example.com", "Alice", "sub-123", time.Now().Add(5*time.Minute).UTC())

	resp, err := client.GetPendingOAuthSignup(signupReq(context.Background(), &leapmuxv1.GetPendingOAuthSignupRequest{
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

	_, client, st, ks, _ := setupOAuthTestServerWithAuthService(t)
	providerID := createTestProvider(t, st, ks)
	signupToken := id.Generate()
	seedPendingSignup(t, st, ks, store.CreatePendingOAuthSignupParams{
		Token:           signupToken,
		ProviderID:      providerID,
		ProviderSubject: "sub-redirect",
		Email:           "redirect@example.com",
		DisplayName:     "Redirect",
		RedirectURI:     "/oauth/authorize?client_id=cli",
		ExpiresAt:       time.Now().Add(5 * time.Minute).UTC(),
	})

	resp, err := client.GetPendingOAuthSignup(signupReq(context.Background(), &leapmuxv1.GetPendingOAuthSignupRequest{
		SignupToken: signupToken,
	}))
	require.NoError(t, err)
	assert.Equal(t, "/oauth/authorize?client_id=cli", resp.GetRedirect())
}

func TestGetPendingOAuthSignup_InvalidToken(t *testing.T) {
	t.Parallel()

	_, client, _, _, _ := setupOAuthTestServerWithAuthService(t)

	_, err := client.GetPendingOAuthSignup(signupReq(context.Background(), &leapmuxv1.GetPendingOAuthSignupRequest{
		SignupToken: "nonexistent-token",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestGetPendingOAuthSignup_ExpiredToken(t *testing.T) {
	t.Parallel()

	_, client, st, ks, _ := setupOAuthTestServerWithAuthService(t)
	providerID := createTestProvider(t, st, ks)
	signupToken := id.Generate()

	// Insert an already-expired pending signup.
	insertPendingSignup(t, st, ks, providerID, signupToken, "expired@example.com", "Expired", "sub-expired", time.Now().Add(-1*time.Minute).UTC())

	_, err := client.GetPendingOAuthSignup(signupReq(context.Background(), &leapmuxv1.GetPendingOAuthSignupRequest{
		SignupToken: signupToken,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// --- CompleteOAuthSignup RPC tests ---

func TestCompleteOAuthSignup_Success(t *testing.T) {
	t.Parallel()

	_, client, st, ks, _ := setupOAuthTestServerWithAuthService(t)
	providerID := createTestProvider(t, st, ks)
	signupToken := id.Generate()

	insertPendingSignup(t, st, ks, providerID, signupToken, "bob@example.com", "Bob", "sub-bob", time.Now().Add(5*time.Minute).UTC())

	callCtx, info := connect.NewClientContext(context.Background())
	resp, err := client.CompleteOAuthSignup(signupReq(callCtx, &leapmuxv1.CompleteOAuthSignupRequest{
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
	link, err := st.OAuthUserLinks().Get(context.Background(), store.GetOAuthUserLinkParams{
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
	_, err = st.PendingOAuthSignups().Get(context.Background(), signupToken)
	require.Error(t, err)
}

func TestCompleteOAuthSignup_RechecksAdmission(t *testing.T) {
	for _, policy := range []string{"signup", "provider"} {
		t.Run(policy, func(t *testing.T) {
			_, client, st, ks, set := setupOAuthTestServerWithAuthService(t)
			providerID, token := createTestProvider(t, st, ks), id.Generate()
			insertPendingSignup(t, st, ks, providerID, token, "closed@example.com", "Closed", "closed-subject", time.Now().Add(5*time.Minute).UTC())
			if policy == "signup" {
				require.NoError(t, settings.KeySignupEnabled.Set(t.Context(), set, false))
			} else {
				require.NoError(t, st.OAuthProviders().UpdateEnabled(t.Context(), store.UpdateOAuthProviderEnabledParams{ID: providerID, Enabled: false}))
			}
			ctx, info := connect.NewClientContext(t.Context())
			_, err := client.CompleteOAuthSignup(signupReq(ctx, &leapmuxv1.CompleteOAuthSignupRequest{SignupToken: token, Username: "closed-user"}))
			require.Error(t, err)
			assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
			_, err = st.Users().GetByUsername(t.Context(), "closed-user")
			assert.ErrorIs(t, err, store.ErrNotFound)
			_, err = st.OAuthUserLinks().Get(t.Context(), store.GetOAuthUserLinkParams{ProviderID: providerID, ProviderSubject: "closed-subject"})
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
	_, client, st, ks, set := setupOAuthTestServerWithAuthService(t)
	setSessionDuration(t, set, configured)
	providerID := createTestProvider(t, st, ks)
	signupToken := id.Generate()

	insertPendingSignup(t, st, ks, providerID, signupToken, "dana@example.com", "Dana", "sub-dana", time.Now().Add(5*time.Minute).UTC())

	before := time.Now()
	callCtx, info := connect.NewClientContext(context.Background())
	_, err := client.CompleteOAuthSignup(signupReq(callCtx, &leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "danauser",
		DisplayName: "Dana User",
	}))
	require.NoError(t, err)

	sessionToken := sessionFromCookie(t, info.ResponseHeader().Get("Set-Cookie"))
	sess, err := st.Sessions().GetByID(context.Background(), hubtestutil.SessionIDFromCookie(t, sessionToken), time.Now().UTC())
	require.NoError(t, err)
	hubtestutil.AssertSessionLifetime(t, before, configured, sess.ExpiresAt)
}

func TestCompleteOAuthSignup_UsesProviderEmail_IgnoresRequestEmail(t *testing.T) {
	t.Parallel()

	_, client, st, ks, _ := setupOAuthTestServerWithAuthService(t)
	providerID := createTestProvider(t, st, ks) // trust_email=1 by default
	signupToken := id.Generate()

	// Pending signup has provider email "provider@example.com".
	insertPendingSignup(t, st, ks, providerID, signupToken, "provider@example.com", "Provider", "sub-provider", time.Now().Add(5*time.Minute).UTC())

	// The request tries to override with a different email; the handler must
	// ignore it.
	resp, err := client.CompleteOAuthSignup(signupReq(context.Background(), &leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "provideruser",
		Email:       "attacker@evil.com",
	}))
	require.NoError(t, err)

	// The user's email should be the provider's, not the attacker's.
	user, err := st.Users().GetByID(context.Background(), resp.GetSessionReport().GetUser().GetId())
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

	_, client, st, ks, _ := setupOAuthTestServerWithAuthService(t)
	providerID := createTestProviderWithTrustEmail(t, st, ks, false)
	signupToken := id.Generate()
	insertPendingSignup(t, st, ks, providerID, signupToken, "", "No Email", "sub-noemail", time.Now().Add(5*time.Minute).UTC())

	resp, err := client.CompleteOAuthSignup(signupReq(context.Background(), &leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "noemailuser",
		Email:       "typed@example.com",
	}))
	require.NoError(t, err)

	user, err := st.Users().GetByID(context.Background(), resp.GetSessionReport().GetUser().GetId())
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

	_, client, st, ks, _ := setupOAuthTestServerWithAuthService(t)
	providerID := createTestProviderWithTrustEmail(t, st, ks, false)
	signupToken := id.Generate()
	insertPendingSignup(t, st, ks, providerID, signupToken, "provider@example.com", "Provider", "sub-untrusted", time.Now().Add(5*time.Minute).UTC())

	resp, err := client.CompleteOAuthSignup(signupReq(context.Background(), &leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "untrusteduser",
		Email:       "attacker@evil.com",
	}))
	require.NoError(t, err)

	user, err := st.Users().GetByID(context.Background(), resp.GetSessionReport().GetUser().GetId())
	require.NoError(t, err)
	assert.Equal(t, "provider@example.com", user.Email,
		"a provider-supplied address is not substitutable")
}

func TestCompleteOAuthSignup_DuplicateUsername(t *testing.T) {
	t.Parallel()

	_, client, st, ks, _ := setupOAuthTestServerWithAuthService(t)
	providerID := createTestProvider(t, st, ks)
	signupToken := id.Generate()

	insertPendingSignup(t, st, ks, providerID, signupToken, "dup@example.com", "Dup", "sub-dup", time.Now().Add(5*time.Minute).UTC())

	// Create an existing user with the same username.
	hash, err := password.Hash("testpass")
	require.NoError(t, err)
	err = st.Users().Create(context.Background(), store.CreateUserParams{
		ID:                    id.Generate(),
		Username:              "takenname",
		PasswordHash:          hash,
		DisplayName:           "Taken",
		Email:                 "",
		FirstCredentialExempt: true,
		IsAdmin:               false,
	})
	require.NoError(t, err)

	_, err = client.CompleteOAuthSignup(signupReq(context.Background(), &leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "takenname",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))

	// The handler should NOT delete the pending row, so the user can retry
	// with a different username.
	_, err = st.PendingOAuthSignups().Get(context.Background(), signupToken)
	require.NoError(t, err)
}

func TestCompleteOAuthSignup_DuplicateEmail(t *testing.T) {
	t.Parallel()

	_, client, st, ks, _ := setupOAuthTestServerWithAuthService(t)
	providerID := createTestProviderWithTrustEmail(t, st, ks, false) // untrusted provider
	signupToken := id.Generate()

	insertPendingSignup(t, st, ks, providerID, signupToken, "taken@example.com", "New", "sub-new", time.Now().Add(5*time.Minute).UTC())

	// Create an existing user with the same email.
	hash, err := password.Hash("testpass")
	require.NoError(t, err)
	err = st.Users().Create(context.Background(), store.CreateUserParams{
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
	_, err = client.CompleteOAuthSignup(signupReq(context.Background(), &leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "newuniqueuser",
		Email:       "taken@example.com",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
}

func TestCompleteOAuthSignup_TrustedProviderSetsEmailVerified(t *testing.T) {
	t.Parallel()

	_, client, st, ks, _ := setupOAuthTestServerWithAuthService(t)
	providerID := createTestProvider(t, st, ks)
	signupToken := id.Generate()

	insertPendingSignup(t, st, ks, providerID, signupToken, "trusted@example.com", "Trusted", "sub-trusted", time.Now().Add(5*time.Minute).UTC())

	resp, err := client.CompleteOAuthSignup(signupReq(context.Background(), &leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "trusteduser",
	}))
	require.NoError(t, err)

	user, err := st.Users().GetByID(context.Background(), resp.GetSessionReport().GetUser().GetId())
	require.NoError(t, err)
	assert.Equal(t, "trusted@example.com", user.Email)
	assert.True(t, user.EmailVerified)
	assert.Empty(t, user.PendingEmail)
}

func TestCompleteOAuthSignup_UntrustedFailClosedWhenVerificationEmailFails(t *testing.T) {
	t.Parallel()

	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Migrator().Migrate(context.Background()))
	hubtestutil.CreateTestAdmin(t, st)

	key, err := keystore.GenerateKey()
	require.NoError(t, err)
	ks, err := keystore.New(map[uint32][32]byte{1: key})
	require.NoError(t, err)

	cfg := &config.Config{Listen: []string{":4327"}}
	set := servicetest.NewSettingsManager(t, st, ks)
	enableSignup(t, set)
	enableEmailVerification(t, set)

	mux := http.NewServeMux()
	idpHandler := service.NewIdPHandler(st, hubtestutil.TestSecretHasher(), cfg, set, auth.NewCredentialLifecycleEffects(nil, nil, nil), ks)
	idpHandler.RegisterRoutes(mux)

	interceptor, _ := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{SessionHasher: hubtestutil.TestSecretHasher(), Store: st})
	authDeps := servicetest.AuthServiceDeps(st, cfg, set, auth.NewCredentialLifecycleEffects(nil, nil, nil))
	authDeps.Keystore = ks
	authDeps.Mail = &mailSenderDouble{err: errors.New("smtp down")}
	authSvc := service.NewAuthService(authDeps)
	connectServer := connect.NewServer(interceptor)
	leapmuxv1connect.RegisterAuthServiceHandler(connectServer, authSvc)
	connecthttp.Mount(mux, connectServer, connecthttp.WithReadMaxBytes(0))

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := leapmuxv1connect.NewAuthServiceClient(connect.NewClient(connecthttp.NewTransport(server.Client(), server.URL, connecthttp.WithReadMaxBytes(0))))

	providerID := createTestProviderWithTrustEmail(t, st, ks, false)
	signupToken := id.Generate()
	insertPendingSignup(t, st, ks, providerID, signupToken, "untrusted@example.com", "Untrusted", "sub-untrusted", time.Now().Add(5*time.Minute).UTC())

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

	_, client, st, ks, set := setupOAuthTestServerWithAuthService(t)
	enableEmailVerification(t, set)
	providerID := createTestProviderWithTrustEmail(t, st, ks, false)
	signupToken := id.Generate()

	insertPendingSignup(t, st, ks, providerID, signupToken, "pending@example.com", "Pending", "sub-pending", time.Now().Add(5*time.Minute).UTC())

	resp, err := client.CompleteOAuthSignup(signupReq(context.Background(), &leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "pendingoauth",
	}))
	require.NoError(t, err)
	assert.True(t, resp.GetSessionReport().GetEmailVerification().GetVerificationRequired())
	assert.True(t, resp.GetSessionReport().GetEmailVerification().GetVerificationEmailSent())

	user, err := st.Users().GetByID(context.Background(), resp.GetSessionReport().GetUser().GetId())
	require.NoError(t, err)
	assert.Empty(t, user.Email)
	assert.False(t, user.EmailVerified)
	assert.Equal(t, "pending@example.com", user.PendingEmail)
	assert.NotEmpty(t, user.PendingEmailToken)
}

func TestCompleteOAuthSignup_InvalidToken(t *testing.T) {
	t.Parallel()

	_, client, _, _, _ := setupOAuthTestServerWithAuthService(t)

	_, err := client.CompleteOAuthSignup(signupReq(context.Background(), &leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: "nonexistent-token",
		Username:    "someuser",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestCompleteOAuthSignup_InvalidUsername(t *testing.T) {
	t.Parallel()

	_, client, st, ks, _ := setupOAuthTestServerWithAuthService(t)
	providerID := createTestProvider(t, st, ks)
	signupToken := id.Generate()

	insertPendingSignup(t, st, ks, providerID, signupToken, "valid@example.com", "Valid", "sub-valid", time.Now().Add(5*time.Minute).UTC())

	_, err := client.CompleteOAuthSignup(signupReq(context.Background(), &leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "", // empty username
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestCompleteOAuthSignup_RejectsSoloAlways(t *testing.T) {
	t.Parallel()

	_, client, st, ks, _ := setupOAuthTestServerWithAuthService(t)
	providerID := createTestProvider(t, st, ks)
	signupToken := id.Generate()

	insertPendingSignup(t, st, ks, providerID, signupToken, "new@example.com", "New", "sub-new", time.Now().Add(5*time.Minute).UTC())

	_, err := client.CompleteOAuthSignup(signupReq(context.Background(), &leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "solo",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "reserved")
}

func TestCompleteOAuthSignup_RejectsAdminInPublicSignup(t *testing.T) {
	t.Parallel()

	// setupOAuthTestServerWithAuthService seeds the admin fixture, so this is
	// a non-setup-mode OAuth signup and the public reservation applies.
	_, client, st, ks, _ := setupOAuthTestServerWithAuthService(t)

	providerID := createTestProvider(t, st, ks)
	signupToken := id.Generate()
	insertPendingSignup(t, st, ks, providerID, signupToken, "new@example.com", "New", "sub-new", time.Now().Add(5*time.Minute).UTC())

	_, err := client.CompleteOAuthSignup(signupReq(context.Background(), &leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "admin",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "reserved")
}

func TestCompleteOAuthSignup_TokenConsumedOnSuccess(t *testing.T) {
	t.Parallel()

	_, client, st, ks, _ := setupOAuthTestServerWithAuthService(t)
	providerID := createTestProvider(t, st, ks)
	signupToken := id.Generate()

	insertPendingSignup(t, st, ks, providerID, signupToken, "consume@example.com", "Consume", "sub-consume", time.Now().Add(5*time.Minute).UTC())

	// First call succeeds.
	_, err := client.CompleteOAuthSignup(signupReq(context.Background(), &leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "consumeuser",
	}))
	require.NoError(t, err)

	// Second call with the same token fails.
	_, err = client.CompleteOAuthSignup(signupReq(context.Background(), &leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "anotheruser",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestCompleteOAuthSignup_ReencryptsTokensWithActiveKeyVersion(t *testing.T) {
	t.Parallel()

	_, client, st, ks, _ := setupOAuthTestServerWithAuthService(t)
	providerID := createTestProvider(t, st, ks)
	signupToken := id.Generate()

	insertPendingSignup(t, st, ks, providerID, signupToken, "keyver@example.com", "KeyVer", "sub-keyver", time.Now().Add(5*time.Minute).UTC())

	resp, err := client.CompleteOAuthSignup(signupReq(context.Background(), &leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "keyveruser",
	}))
	require.NoError(t, err)

	userID := resp.GetSessionReport().GetUser().GetId()

	// Verify stored tokens use the active key version and can be decrypted
	// with the user ID as AAD (not the signup token).
	tok, err := st.OAuthTokens().Get(context.Background(), store.GetOAuthTokensParams{
		UserID:     userid.MustNew(userID),
		ProviderID: providerID,
	})
	require.NoError(t, err)

	assert.Equal(t, int64(ks.ActiveVersion()), tok.KeyVersion)

	ver, err := keystore.CiphertextVersion(tok.AccessToken)
	require.NoError(t, err)
	assert.Equal(t, ks.ActiveVersion(), ver, "access token ciphertext should use active key version")

	// Decrypt with user ID AAD should succeed.
	plainAccess, err := ks.Decrypt(tok.AccessToken, keystore.AccessTokenAAD(userID, providerID))
	require.NoError(t, err)
	assert.Equal(t, "mock-access-token", string(plainAccess))

	plainRefresh, err := ks.Decrypt(tok.RefreshToken, keystore.RefreshTokenAAD(userID, providerID))
	require.NoError(t, err)
	assert.Equal(t, "mock-refresh-token", string(plainRefresh))
}

// --- Signed callback behavior tests ---

func TestOAuthCallback_NewUser_SignupDisabled(t *testing.T) {
	t.Parallel()

	server, _, st, ks, set := setupOAuthTestServerWithAuthService(t)
	require.NoError(t, settings.KeySignupEnabled.Set(t.Context(), set, false))
	const email, subject = "new-identity@example.com", "new-subject"
	providerID := createCallbackTestProvider(t, st, ks, email, subject, true)
	response := performLoginCallback(t, server, st, providerID, "/after-login")
	require.Equal(t, http.StatusForbidden, response.StatusCode)
	assert.Contains(t, readBody(t, response), "sign-up is disabled")
	assert.Empty(t, response.Header.Get("Location"))
	assertNoCallbackSession(t, response)
	for _, cookie := range response.Cookies() {
		assert.False(t, strings.HasPrefix(cookie.Name, auth.OAuthSignupNonceCookieName+"-"))
		assert.False(t, strings.HasPrefix(cookie.Name, auth.SecureOAuthSignupNonceCookieName+"-"))
	}
	_, err := st.OAuthUserLinks().Get(t.Context(), store.GetOAuthUserLinkParams{ProviderID: providerID, ProviderSubject: subject})
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = st.Users().GetByEmail(t.Context(), email)
	require.ErrorIs(t, err, store.ErrNotFound)
}

// The callback creates the incoming link and a session for the existing account,
// even when public signup is disabled. This test creates neither outcome.
func TestAutoLinkByVerifiedEmail(t *testing.T) {
	t.Parallel()

	for _, signupEnabled := range []bool{true, false} {
		t.Run(strconv.FormatBool(signupEnabled), func(t *testing.T) {
			server, _, st, ks, set := setupOAuthTestServerWithAuthService(t)
			require.NoError(t, settings.KeySignupEnabled.Set(t.Context(), set, signupEnabled))
			const email, subject = "verified-autolink@example.com", "incoming-trusted-subject"
			userID := id.Generate()
			require.NoError(t, st.Users().Create(t.Context(), store.CreateUserParams{
				ID: userID, Username: "verified-autolink", PasswordHash: password.PlaceholderHash,
				Email: email, EmailVerified: true,
			}))
			providerID := createCallbackTestProvider(t, st, ks, email, subject, true)
			require.Zero(t, countSessions(t, st, userID))
			_, err := st.OAuthUserLinks().Get(t.Context(), store.GetOAuthUserLinkParams{ProviderID: providerID, ProviderSubject: subject})
			require.ErrorIs(t, err, store.ErrNotFound)

			response := performLoginCallback(t, server, st, providerID, "/after-login")
			require.Equal(t, http.StatusFound, response.StatusCode, readBody(t, response))
			assert.Equal(t, "/after-login", response.Header.Get("Location"))
			link, err := st.OAuthUserLinks().Get(t.Context(), store.GetOAuthUserLinkParams{ProviderID: providerID, ProviderSubject: subject})
			require.NoError(t, err)
			assert.Equal(t, userID, link.UserID)
			var sessionCookie *http.Cookie
			for _, cookie := range response.Cookies() {
				if (cookie.Name == auth.CookieName || cookie.Name == auth.SecureCookieName) && cookie.Value != "" && cookie.MaxAge >= 0 {
					sessionCookie = cookie
				}
			}
			require.NotNil(t, sessionCookie)
			assertCallbackSession(t, st, sessionCookie.Value, userID)
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
			server, _, st, ks, set := setupOAuthTestServerWithAuthService(t)
			require.NoError(t, settings.KeySignupEnabled.Set(t.Context(), set, tc.signup))
			const email, subject = "not-autolinked@example.com", "incoming-subject"
			userID := id.Generate()
			require.NoError(t, st.Users().Create(t.Context(), store.CreateUserParams{
				ID: userID, Username: "not-autolinked", PasswordHash: password.PlaceholderHash,
				Email: email, EmailVerified: tc.verified,
			}))
			providerID := createCallbackTestProvider(t, st, ks, email, subject, tc.trustEmail)
			response := performLoginCallback(t, server, st, providerID, "/after-login")
			_, err := st.OAuthUserLinks().Get(t.Context(), store.GetOAuthUserLinkParams{ProviderID: providerID, ProviderSubject: subject})
			require.ErrorIs(t, err, store.ErrNotFound)
			assert.Zero(t, countSessions(t, st, userID))
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
			pending, err := st.PendingOAuthSignups().Get(t.Context(), token)
			require.NoError(t, err)
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

	server, client, st, ks, _ := setupOAuthTestServerWithAuthService(t)
	const email, subject = "signed-signup@example.com", "signed-signup-subject"
	providerID := createCallbackTestProvider(t, st, ks, email, subject, true)
	response := performLoginCallback(t, server, st, providerID, "/after-signup")
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
	pending, err := st.PendingOAuthSignups().Get(t.Context(), token)
	require.NoError(t, err)
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
		_, err = client.GetPendingOAuthSignup(ctx, &leapmuxv1.GetPendingOAuthSignupRequest{SignupToken: token})
		require.Error(t, err)
		assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
		_, err = client.CompleteOAuthSignup(ctx, &leapmuxv1.CompleteOAuthSignupRequest{SignupToken: token, Username: "signedsignupuser"})
		require.Error(t, err)
		assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
		_, err = st.Users().GetByEmail(t.Context(), email)
		assert.ErrorIs(t, err, store.ErrNotFound)
		_, err = st.OAuthUserLinks().Get(t.Context(), store.GetOAuthUserLinkParams{ProviderID: providerID, ProviderSubject: subject})
		assert.ErrorIs(t, err, store.ErrNotFound)
		_, err = st.PendingOAuthSignups().Get(t.Context(), token)
		require.NoError(t, err)
		assert.Empty(t, info.ResponseHeader().Get("Set-Cookie"))
	}

	ctx, info := connect.NewClientContext(t.Context())
	info.RequestHeader().Set("Cookie", nonce.Name+"="+nonce.Value)
	preview, err := client.GetPendingOAuthSignup(ctx, &leapmuxv1.GetPendingOAuthSignupRequest{SignupToken: token})
	require.NoError(t, err)
	assert.Equal(t, email, preview.GetEmail())
	assert.Equal(t, "Test OIDC", preview.GetProviderName())
	assert.Equal(t, "/after-signup", preview.GetRedirect())
	completed, err := client.CompleteOAuthSignup(ctx, &leapmuxv1.CompleteOAuthSignupRequest{SignupToken: token, Username: "signedsignupuser"})
	require.NoError(t, err)
	userID := completed.GetSessionReport().GetUser().GetId()
	require.NotEmpty(t, userID)
	user, err := st.Users().GetByID(t.Context(), userID)
	require.NoError(t, err)
	assert.Equal(t, email, user.Email)
	assert.True(t, user.EmailVerified)
	link, err := st.OAuthUserLinks().Get(t.Context(), store.GetOAuthUserLinkParams{ProviderID: providerID, ProviderSubject: subject})
	require.NoError(t, err)
	assert.Equal(t, userID, link.UserID)
	assertCallbackSession(t, st, sessionFromCookie(t, info.ResponseHeader().Get("Set-Cookie")), userID)
	_, err = st.PendingOAuthSignups().Get(t.Context(), token)
	assert.ErrorIs(t, err, store.ErrNotFound)

	// The signed provider's token pair must survive the pending-to-user change
	// of additional authenticated data without a second token exchange.
	tokens, err := st.OAuthTokens().Get(t.Context(), store.GetOAuthTokensParams{UserID: userid.MustNew(userID), ProviderID: providerID})
	require.NoError(t, err)
	accessToken, err := ks.Decrypt(tokens.AccessToken, keystore.AccessTokenAAD(userID, providerID))
	require.NoError(t, err)
	assert.Equal(t, "a", string(accessToken))
	refreshToken, err := ks.Decrypt(tokens.RefreshToken, keystore.RefreshTokenAAD(userID, providerID))
	require.NoError(t, err)
	assert.Equal(t, "r", string(refreshToken))

	_, err = client.CompleteOAuthSignup(ctx, &leapmuxv1.CompleteOAuthSignupRequest{SignupToken: token, Username: "signedsignupagain"})
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	assert.Equal(t, 1, countSessions(t, st, userID))
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

func performLoginCallback(t *testing.T, server *httptest.Server, st store.Store, providerID, redirect string) *http.Response {
	t.Helper()
	login, err := noRedirectClient().Get(server.URL + "/auth/idp/" + providerID + "/login?redirect=" + url.QueryEscape(redirect))
	require.NoError(t, err)
	t.Cleanup(func() { _ = login.Body.Close() })
	require.Equal(t, http.StatusFound, login.StatusCode)
	state := stateFromLoginResponse(t, login)
	nonce := oauthNonceCookie(t, login, state)
	require.NotNil(t, nonce)
	require.NotEmpty(t, nonce.Value)
	storedState, err := st.OAuthStates().Get(t.Context(), state)
	require.NoError(t, err)
	assert.Equal(t, providerID, storedState.ProviderID)
	assert.Equal(t, redirect, storedState.RedirectURI)
	assert.NotEqual(t, nonce.Value, storedState.NonceHash)
	assert.Equal(t, hashNonceForTest(nonce.Value), storedState.NonceHash)
	response := completeCallback(t, server, providerID, state, nonce)
	_, err = st.OAuthStates().Get(t.Context(), state)
	assert.ErrorIs(t, err, store.ErrNotFound)
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

func TestDeleteOAuthTokens_ScopedToProvider(t *testing.T) {
	t.Parallel()

	_, st, ks := setupOAuthTestServer(t)

	// Create two OAuth providers.
	providerA := createTestProvider(t, st, ks)
	providerBID := id.Generate()
	aad := keystore.ProviderAAD(providerBID)
	encSecret, err := ks.Encrypt([]byte("secret-b"), aad)
	require.NoError(t, err)
	err = st.OAuthProviders().Create(context.Background(), store.CreateOAuthProviderParams{
		ID:           providerBID,
		ProviderType: leapmuxv1.IdentityProviderType_IDENTITY_PROVIDER_TYPE_OIDC,
		Name:         "Test OIDC",
		ClientID:     "client-b",
		ClientSecret: encSecret,
		Scopes:       "openid",
		Enabled:      true,
	})
	require.NoError(t, err)

	// Use the bootstrap admin as the token owner.
	admin, err := st.Users().GetByUsername(context.Background(), "admin")
	require.NoError(t, err)

	// Insert tokens for both providers.
	err = st.OAuthTokens().Upsert(context.Background(), store.UpsertOAuthTokensParams{
		UserID:       userid.MustNew(admin.ID),
		ProviderID:   providerA,
		AccessToken:  []byte("dummy"),
		RefreshToken: []byte("dummy"),
		TokenType:    "bearer",
		ExpiresAt:    time.Now().Add(1 * time.Hour).UTC(),
		KeyVersion:   int64(ks.ActiveVersion()),
	})
	require.NoError(t, err)

	err = st.OAuthTokens().Upsert(context.Background(), store.UpsertOAuthTokensParams{
		UserID:       userid.MustNew(admin.ID),
		ProviderID:   providerBID,
		AccessToken:  []byte("dummy"),
		RefreshToken: []byte("dummy"),
		TokenType:    "bearer",
		ExpiresAt:    time.Now().Add(1 * time.Hour).UTC(),
		KeyVersion:   int64(ks.ActiveVersion()),
	})
	require.NoError(t, err)

	// Delete tokens for provider A only.
	err = st.OAuthTokens().DeleteByUserAndProvider(context.Background(), store.DeleteOAuthTokensByUserAndProviderParams{
		UserID:     userid.MustNew(admin.ID),
		ProviderID: providerA,
	})
	require.NoError(t, err)

	// Provider A's tokens should be gone.
	_, err = st.OAuthTokens().Get(context.Background(), store.GetOAuthTokensParams{
		UserID:     userid.MustNew(admin.ID),
		ProviderID: providerA,
	})
	require.Error(t, err, "provider A tokens should have been deleted")

	// Provider B's tokens should still exist.
	tok, err := st.OAuthTokens().Get(context.Background(), store.GetOAuthTokensParams{
		UserID:     userid.MustNew(admin.ID),
		ProviderID: providerBID,
	})
	require.NoError(t, err, "provider B tokens should still exist")
	assert.Equal(t, providerBID, tok.ProviderID)
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

// purposeRewritingStore gives the handler a state row whose purpose is a
// value the schema refuses to store, which is the only way to drive the
// unknown-purpose branch. oauth_states.purpose carries
// CHECK (purpose BETWEEN 1 AND 2) in all three dialects, so no such row is
// STORABLE -- this store fabricates one on the read path instead.
type purposeRewritingStore struct {
	store.Store
	purpose leapmuxv1.OAuthStatePurpose
}

func (s purposeRewritingStore) OAuthStates() store.OAuthStateStore {
	return purposeRewritingStates{OAuthStateStore: s.Store.OAuthStates(), purpose: s.purpose}
}

type purposeRewritingStates struct {
	store.OAuthStateStore
	purpose leapmuxv1.OAuthStatePurpose
}

func (s purposeRewritingStates) Get(ctx context.Context, state string) (*store.OAuthState, error) {
	row, err := s.OAuthStateStore.Get(ctx, state)
	if err != nil {
		return nil, err
	}
	rewritten := *row
	rewritten.Purpose = s.purpose
	return &rewritten, nil
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
	// shapes a value that is not LOGIN or REAUTH can take.
	for _, purpose := range []leapmuxv1.OAuthStatePurpose{
		leapmuxv1.OAuthStatePurpose_OAUTH_STATE_PURPOSE_UNSPECIFIED, 3, -1,
	} {
		t.Run("purpose "+strconv.Itoa(int(purpose)), func(t *testing.T) {
			t.Parallel()

			server, st, ks, _ := setupOAuthTestServerOver(t, ":4327", func(s store.Store) store.Store {
				return purposeRewritingStore{Store: s, purpose: purpose}
			})
			providerID := createTestOIDCProviderWithStubClaims(t, st, ks, "newcomer@example.com", "sub-new")

			loginResp, err := noRedirectClient().Get(server.URL + "/auth/idp/" + providerID + "/login")
			require.NoError(t, err)
			defer func() { _ = loginResp.Body.Close() }()
			state := stateFromLoginResponse(t, loginResp)
			flowCookie := oauthNonceCookie(t, loginResp, state)
			require.NotNil(t, flowCookie)

			req, err := http.NewRequest(http.MethodGet,
				server.URL+"/auth/idp/"+providerID+"/callback?code=c&state="+state, nil)
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

// stateReadBarrier holds every caller of OAuthStates().Get until `parties`
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

type barrierStateStore struct {
	store.Store
	t       *testing.T
	barrier *stateReadBarrier
}

func (s barrierStateStore) OAuthStates() store.OAuthStateStore {
	return barrierStates{OAuthStateStore: s.Store.OAuthStates(), t: s.t, barrier: s.barrier}
}

type barrierStates struct {
	store.OAuthStateStore
	t       *testing.T
	barrier *stateReadBarrier
}

func (s barrierStates) Get(ctx context.Context, state string) (*store.OAuthState, error) {
	row, err := s.OAuthStateStore.Get(ctx, state)
	s.barrier.arrive(s.t)
	return row, err
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
	server, st, ks, _ := setupOAuthTestServerOver(t, ":4327", func(s store.Store) store.Store {
		return barrierStateStore{Store: s, t: t, barrier: barrier}
	})
	providerID := createTestOIDCProviderWithStubClaims(t, st, ks, "user@example.com", "sub-1")
	cookie, _, _ := reauthSession(t, st, providerID, "sub-1")

	state, nonce, _ := beginReauth(t, server, providerID, cookie, "/workspace/1")
	require.NotNil(t, nonce)

	statuses := make([]int, 2)
	var wg sync.WaitGroup
	for i := range statuses {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, err := http.NewRequest(http.MethodGet,
				server.URL+"/auth/idp/"+providerID+"/callback?code=test&state="+state, nil)
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
				server, st, ks, _ := setupOAuthTestServerOver(t, ":4327", func(st store.Store) store.Store {
					return idpAuthReadStore{Store: st, probe: probe}
				})
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
						state = seedOAuthState(t, st, providerID, hashNonceForTest(nonce))
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
					_, err := st.OAuthStates().Get(context.Background(), state)
					assert.ErrorIs(t, err, store.ErrNotFound, "browser-bound state is consumed before provider loading")
					assert.NotEmpty(t, resp.Cookies(), "the callback must clear its nonce even when provider loading fails")
				}
				expectedStates := 0
				if action != "callback" && outcome.status == http.StatusFound {
					expectedStates = 1
				} else if action != "callback" {
					assert.Empty(t, resp.Cookies(), "a provider refusal must not issue flow state")
				}
				assert.Equal(t, expectedStates, countOAuthStates(t, st))
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
			server, st, ks, _ := setupOAuthTestServerOver(t, ":4327", func(st store.Store) store.Store {
				return idpAuthReadStore{Store: st, probe: probe}
			})
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
				assert.Equal(t, 1, countOAuthStates(t, st))
			} else {
				assert.Zero(t, probe.providerCalls.Load(), "authentication must finish before provider loading")
				assert.Empty(t, resp.Header.Get("Location"))
				assert.Empty(t, resp.Cookies())
				assert.Zero(t, countOAuthStates(t, st), "a refusal must not issue flow state")
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
	server, client, st, ks, _ := setupOAuthTestServerWithAuthService(t)
	const email, subject = "pending-after-final-deletion@example.com", "pending-after-final-deletion"
	providerID := createCallbackTestProvider(t, st, ks, email, subject, true)
	response := performLoginCallback(t, server, st, providerID, "/after-signup")
	require.Equal(t, http.StatusFound, response.StatusCode)
	location, err := response.Location()
	require.NoError(t, err)
	token := location.Query().Get("token")
	require.NotEmpty(t, token)
	nonce := signupNonceCookie(t, response, token)
	require.NotNil(t, nonce)
	admin, err := st.Users().GetByUsername(t.Context(), "admin")
	require.NoError(t, err)
	require.NoError(t, st.Users().Delete(t.Context(), admin.ID))
	hasUsers, err := st.Users().HasAny(t.Context())
	require.NoError(t, err)
	require.False(t, hasUsers)
	ctx, info := connect.NewClientContext(t.Context())
	info.RequestHeader().Set("Cookie", nonce.Name+"="+nonce.Value)
	completed, err := client.CompleteOAuthSignup(ctx, &leapmuxv1.CompleteOAuthSignupRequest{SignupToken: token, Username: "ordinaryoauth"})
	require.Error(t, err, "ordinary OAuth signup must require an existing live account")
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Nil(t, completed)
	assert.Empty(t, info.ResponseHeader().Values("Set-Cookie"))
	_, err = st.Users().GetByUsername(t.Context(), "ordinaryoauth")
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = st.OAuthUserLinks().Get(t.Context(), store.GetOAuthUserLinkParams{ProviderID: providerID, ProviderSubject: subject})
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = st.PendingOAuthSignups().Get(t.Context(), token)
	require.NoError(t, err, "refused admission must retain the bound pending signup")
}
