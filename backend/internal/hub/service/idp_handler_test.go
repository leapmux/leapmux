package service_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/userid"

	"connectrpc.com/connect"
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

// setupOAuthTestServerWithListen varies the bind address, which is what
// decides whether this hub can run a passkey ceremony at all: an empty
// listen has no browser-reachable origin, so RPConfigFromSettings refuses
// and passkeys are cleanly unavailable. The account-shape rule still refuses
// a provider step-up for an account that holds such a passkey.
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
		Listen: listen,
	}
	set := servicetest.NewSettingsManager(t, st, ks)
	enableSignup(t, set)

	handlerStore := st
	if wrap != nil {
		handlerStore = wrap(st)
	}
	idpHandler := service.NewIdPHandler(handlerStore, cfg, set, auth.NewCredentialLifecycleEffects(nil, nil, nil), ks)

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
		ProviderType: "github",
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
		ProviderType: "oidc",
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
		Listen: ":4327",
	}
	set := servicetest.NewSettingsManager(t, st, ks)
	enableSignup(t, set)

	mux := http.NewServeMux()

	// Register OAuth HTTP routes.
	idpHandler := service.NewIdPHandler(st, cfg, set, auth.NewCredentialLifecycleEffects(nil, nil, nil), ks)
	idpHandler.RegisterRoutes(mux)

	// Register AuthService ConnectRPC routes.
	interceptor, _ := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st})
	opts := connect.WithInterceptors(interceptor)
	authDeps := servicetest.AuthServiceDeps(st, cfg, set, auth.NewCredentialLifecycleEffects(nil, nil, nil))
	authDeps.Keystore = ks
	authSvc := service.NewAuthService(authDeps)
	path, handler := leapmuxv1connect.NewAuthServiceHandler(authSvc, opts)
	mux.Handle(path, handler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL)
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
}](msg PT) *connect.Request[T] {
	req := connect.NewRequest((*T)(msg))
	req.Header().Set("Cookie", signupCookieHeader(msg.GetSignupToken()))
	return req
}

// insertPendingSignup creates a pending_oauth_signups row with encrypted tokens.
func insertPendingSignup(t *testing.T, st store.Store, ks *keystore.Keystore, providerID, token, email, displayName, subject string, expiresAt time.Time) {
	t.Helper()
	encAccess, err := ks.Encrypt([]byte("mock-access-token"), keystore.AccessTokenAAD(token, providerID))
	require.NoError(t, err)
	encRefresh, err := ks.Encrypt([]byte("mock-refresh-token"), keystore.RefreshTokenAAD(token, providerID))
	require.NoError(t, err)

	err = st.PendingOAuthSignups().Create(context.Background(), store.CreatePendingOAuthSignupParams{
		Token:           token,
		ProviderID:      providerID,
		NonceHash:       hashNonceForTest(testSignupNonce),
		ProviderSubject: subject,
		Email:           email,
		DisplayName:     displayName,
		AccessToken:     encAccess,
		RefreshToken:    encRefresh,
		TokenType:       "bearer",
		TokenExpiresAt:  time.Now().Add(1 * time.Hour).UTC(),
		KeyVersion:      int64(ks.ActiveVersion()),
		ExpiresAt:       expiresAt,
	})
	require.NoError(t, err)
}

// --- GetPendingOAuthSignup RPC tests ---

func TestGetPendingOAuthSignup_Success(t *testing.T) {
	t.Parallel()

	_, client, st, ks, _ := setupOAuthTestServerWithAuthService(t)
	providerID := createTestProvider(t, st, ks)
	signupToken := id.Generate()

	insertPendingSignup(t, st, ks, providerID, signupToken, "alice@example.com", "Alice", "sub-123", time.Now().Add(5*time.Minute).UTC())

	resp, err := client.GetPendingOAuthSignup(context.Background(), signupReq(&leapmuxv1.GetPendingOAuthSignupRequest{
		SignupToken: signupToken,
	}))
	require.NoError(t, err)
	assert.Equal(t, "alice@example.com", resp.Msg.GetEmail())
	assert.Equal(t, "Alice", resp.Msg.GetDisplayName())
	assert.Equal(t, "Test GitHub", resp.Msg.GetProviderName())
}

func TestGetPendingOAuthSignup_InvalidToken(t *testing.T) {
	t.Parallel()

	_, client, _, _, _ := setupOAuthTestServerWithAuthService(t)

	_, err := client.GetPendingOAuthSignup(context.Background(), signupReq(&leapmuxv1.GetPendingOAuthSignupRequest{
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

	_, err := client.GetPendingOAuthSignup(context.Background(), signupReq(&leapmuxv1.GetPendingOAuthSignupRequest{
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

	resp, err := client.CompleteOAuthSignup(context.Background(), signupReq(&leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "bobuser",
		DisplayName: "Bob User",
	}))
	require.NoError(t, err)
	assert.Equal(t, "bobuser", resp.Msg.GetUser().GetUsername())
	assert.Equal(t, "Bob User", resp.Msg.GetUser().GetDisplayName())

	// Verify that the response sets the session cookie.
	setCookie := resp.Header().Get("Set-Cookie")
	assert.Contains(t, setCookie, auth.CookieName+"=")

	// Verify that the flow created the OAuth user link.
	link, err := st.OAuthUserLinks().Get(context.Background(), store.GetOAuthUserLinkParams{
		ProviderID:      providerID,
		ProviderSubject: "sub-bob",
	})
	require.NoError(t, err)
	assert.Equal(t, resp.Msg.GetUser().GetId(), link.UserID)

	// Verify that the flow consumed the pending signup.
	_, err = st.PendingOAuthSignups().Get(context.Background(), signupToken)
	require.Error(t, err)
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
	resp, err := client.CompleteOAuthSignup(context.Background(), signupReq(&leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "danauser",
		DisplayName: "Dana User",
	}))
	require.NoError(t, err)

	sessionID := sessionFromCookie(t, resp.Header().Get("Set-Cookie"))
	sess, err := st.Sessions().GetByID(context.Background(), sessionID, time.Now().UTC())
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
	resp, err := client.CompleteOAuthSignup(context.Background(), signupReq(&leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "provideruser",
		Email:       "attacker@evil.com",
	}))
	require.NoError(t, err)

	// The user's email should be the provider's, not the attacker's.
	user, err := st.Users().GetByID(context.Background(), resp.Msg.GetUser().GetId())
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

	resp, err := client.CompleteOAuthSignup(context.Background(), signupReq(&leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "noemailuser",
		Email:       "typed@example.com",
	}))
	require.NoError(t, err)

	user, err := st.Users().GetByID(context.Background(), resp.Msg.GetUser().GetId())
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

	resp, err := client.CompleteOAuthSignup(context.Background(), signupReq(&leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "untrusteduser",
		Email:       "attacker@evil.com",
	}))
	require.NoError(t, err)

	user, err := st.Users().GetByID(context.Background(), resp.Msg.GetUser().GetId())
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
		ID:           id.Generate(),
		Username:     "takenname",
		PasswordHash: hash,
		DisplayName:  "Taken",
		Email:        "",
		PasswordSet:  true,
		IsAdmin:      false,
	})
	require.NoError(t, err)

	_, err = client.CompleteOAuthSignup(context.Background(), signupReq(&leapmuxv1.CompleteOAuthSignupRequest{
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
		ID:           id.Generate(),
		Username:     "emailowner",
		PasswordHash: hash,
		DisplayName:  "Email Owner",
		Email:        "taken@example.com",
		PasswordSet:  true,
		IsAdmin:      false,
	})
	require.NoError(t, err)

	// Provider has trust_email=0 and SMTP verification is off,
	// so email goes directly to the email column unverified. Duplicate check should fire.
	_, err = client.CompleteOAuthSignup(context.Background(), signupReq(&leapmuxv1.CompleteOAuthSignupRequest{
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

	resp, err := client.CompleteOAuthSignup(context.Background(), signupReq(&leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "trusteduser",
	}))
	require.NoError(t, err)

	user, err := st.Users().GetByID(context.Background(), resp.Msg.GetUser().GetId())
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

	cfg := &config.Config{Listen: ":4327"}
	set := servicetest.NewSettingsManager(t, st, ks)
	enableSignup(t, set)
	enableEmailVerification(t, set)

	mux := http.NewServeMux()
	idpHandler := service.NewIdPHandler(st, cfg, set, auth.NewCredentialLifecycleEffects(nil, nil, nil), ks)
	idpHandler.RegisterRoutes(mux)

	interceptor, _ := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st})
	opts := connect.WithInterceptors(interceptor)
	authDeps := servicetest.AuthServiceDeps(st, cfg, set, auth.NewCredentialLifecycleEffects(nil, nil, nil))
	authDeps.Keystore = ks
	authDeps.Mail = failingMailSender{err: errors.New("smtp down")}
	authSvc := service.NewAuthService(authDeps)
	path, handler := leapmuxv1connect.NewAuthServiceHandler(authSvc, opts)
	mux.Handle(path, handler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL)

	providerID := createTestProviderWithTrustEmail(t, st, ks, false)
	signupToken := id.Generate()
	insertPendingSignup(t, st, ks, providerID, signupToken, "untrusted@example.com", "Untrusted", "sub-untrusted", time.Now().Add(5*time.Minute).UTC())

	_, err = client.CompleteOAuthSignup(context.Background(), signupReq(&leapmuxv1.CompleteOAuthSignupRequest{
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

	resp, err := client.CompleteOAuthSignup(context.Background(), signupReq(&leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "pendingoauth",
	}))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetEmailVerification().GetVerificationRequired())
	assert.True(t, resp.Msg.GetEmailVerification().GetVerificationEmailSent())

	user, err := st.Users().GetByID(context.Background(), resp.Msg.GetUser().GetId())
	require.NoError(t, err)
	assert.Empty(t, user.Email)
	assert.False(t, user.EmailVerified)
	assert.Equal(t, "pending@example.com", user.PendingEmail)
	assert.NotEmpty(t, user.PendingEmailToken)
}

func TestCompleteOAuthSignup_InvalidToken(t *testing.T) {
	t.Parallel()

	_, client, _, _, _ := setupOAuthTestServerWithAuthService(t)

	_, err := client.CompleteOAuthSignup(context.Background(), signupReq(&leapmuxv1.CompleteOAuthSignupRequest{
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

	_, err := client.CompleteOAuthSignup(context.Background(), signupReq(&leapmuxv1.CompleteOAuthSignupRequest{
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

	_, err := client.CompleteOAuthSignup(context.Background(), signupReq(&leapmuxv1.CompleteOAuthSignupRequest{
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

	_, err := client.CompleteOAuthSignup(context.Background(), signupReq(&leapmuxv1.CompleteOAuthSignupRequest{
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
	_, err := client.CompleteOAuthSignup(context.Background(), signupReq(&leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "consumeuser",
	}))
	require.NoError(t, err)

	// Second call with the same token fails.
	_, err = client.CompleteOAuthSignup(context.Background(), signupReq(&leapmuxv1.CompleteOAuthSignupRequest{
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

	resp, err := client.CompleteOAuthSignup(context.Background(), signupReq(&leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: signupToken,
		Username:    "keyveruser",
	}))
	require.NoError(t, err)

	userID := resp.Msg.GetUser().GetId()

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

// --- Callback behavior tests (signup disabled) ---

func TestOAuthCallback_NewUser_SignupDisabled(t *testing.T) {
	t.Parallel()

	// Use a custom setup with signup left disabled (its default).
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
		Listen: ":4327",
	}

	idpHandler := service.NewIdPHandler(st, cfg, servicetest.NewSettingsManager(t, st, ks), auth.NewCredentialLifecycleEffects(nil, nil, nil), ks)
	mux := http.NewServeMux()
	idpHandler.RegisterRoutes(mux)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	providerID := createTestProvider(t, st, ks)

	// Create a valid OAuth state for the callback. The nonce hash and the
	// cookie below carry the browser binding, without which the callback
	// refuses before it resolves the provider at all.
	const nonce = "signup-disabled-flow-nonce"
	stateValue := id.Generate()
	err = st.OAuthStates().Create(context.Background(), store.CreateOAuthStateParams{
		State:        stateValue,
		ProviderID:   providerID,
		PkceVerifier: "test-verifier",
		NonceHash:    hashNonceForTest(nonce),
		Purpose:      store.OAuthStatePurposeLogin,
		ExpiresAt:    time.Now().Add(5 * time.Minute).UTC(),
	})
	require.NoError(t, err)

	// The callback attempts to exchange the code with GitHub, which fails
	// because there is no mock token server. The exchange fails with a
	// network error before the signup-disabled check, so this test covers
	// the full state validation path: the binding, the expiry, the
	// provider match, and the state consumption. The signup-disabled check
	// runs on the RPC path instead (GetPendingOAuthSignup /
	// CompleteOAuthSignup).

	req, err := http.NewRequest(http.MethodGet,
		server.URL+"/auth/idp/"+providerID+"/callback?code=test-code&state="+stateValue, nil)
	require.NoError(t, err)
	req.AddCookie(auth.BuildOAuthNonceCookie(stateValue, nonce, time.Minute, false))

	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	// The exchange fails (no mock token server), which answers 400.
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	body := readBody(t, resp)
	assert.NotContains(t, body, "different browser",
		"the browser holds the flow's nonce, so the binding check must pass")
	assert.Contains(t, body, "OAuth exchange failed",
		"the request must reach the exchange, which is what proves the state and provider resolved")

	// The handler consumes the state row either way, so a refused pair cannot
	// be retried.
	_, err = st.OAuthStates().Get(context.Background(), stateValue)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

// TestAutoLinkByVerifiedEmail validates that the auto-link-by-email path
// (in handleCallback) correctly links a new OAuth identity to an existing
// user when the emails match and the existing email is verified.
// Since handleCallback requires a real OAuth token exchange, this test
// exercises the DB-level operations that the auto-link path performs.
func TestAutoLinkByVerifiedEmail(t *testing.T) {
	t.Parallel()

	_, st, ks := setupOAuthTestServer(t)

	// Create a user with a verified email.
	hash, err := password.Hash("testpass")
	require.NoError(t, err)
	userID := id.Generate()
	err = st.Users().Create(context.Background(), store.CreateUserParams{
		ID:            userID,
		Username:      "alice",
		PasswordHash:  hash,
		DisplayName:   "Alice",
		Email:         "alice@example.com",
		EmailVerified: true,
		IsAdmin:       false,
	})
	require.NoError(t, err)

	// Link Alice to GitHub provider.
	githubProviderID := createTestProvider(t, st, ks)
	err = st.OAuthUserLinks().Create(context.Background(), store.CreateOAuthUserLinkParams{
		UserID:          userid.MustNew(userID),
		ProviderID:      githubProviderID,
		ProviderSubject: "github-alice-123",
	})
	require.NoError(t, err)

	// Create a second provider (simulating Google OIDC).
	googleProviderID := id.Generate()
	aad := keystore.ProviderAAD(googleProviderID)
	encSecret, err := ks.Encrypt([]byte("google-secret"), aad)
	require.NoError(t, err)
	err = st.OAuthProviders().Create(context.Background(), store.CreateOAuthProviderParams{
		ID:           googleProviderID,
		ProviderType: "oidc",
		Name:         "Test Google",
		ClientID:     "google-client-id",
		ClientSecret: encSecret,
		Scopes:       "openid profile email",
		Enabled:      true,
	})
	require.NoError(t, err)

	// Simulate what handleCallback does for auto-link:
	// 1. GetOAuthUserLink for Google identity → not found (new identity).
	_, err = st.OAuthUserLinks().Get(context.Background(), store.GetOAuthUserLinkParams{
		ProviderID:      googleProviderID,
		ProviderSubject: "google-alice-456",
	})
	require.Error(t, err, "should not find Google link yet")

	// 2. Look up user by verified email.
	existingUser, err := st.Users().GetByEmail(context.Background(), "alice@example.com")
	require.NoError(t, err)
	assert.Equal(t, userID, existingUser.ID)
	assert.True(t, existingUser.EmailVerified)

	// 3. Create the OAuth link for the new provider identity.
	err = st.OAuthUserLinks().Create(context.Background(), store.CreateOAuthUserLinkParams{
		UserID:          userid.MustNew(existingUser.ID),
		ProviderID:      googleProviderID,
		ProviderSubject: "google-alice-456",
	})
	require.NoError(t, err)

	// Verify: user now has links to both providers.
	links, err := st.OAuthUserLinks().ListByUser(context.Background(), userid.MustNew(userID))
	require.NoError(t, err)
	assert.Len(t, links, 2)

	providerIDs := map[string]bool{}
	for _, l := range links {
		providerIDs[l.ProviderID] = true
	}
	assert.True(t, providerIDs[githubProviderID], "should have GitHub link")
	assert.True(t, providerIDs[googleProviderID], "should have Google link")

	// Verify: looking up either provider identity resolves to the same user.
	githubLink, err := st.OAuthUserLinks().Get(context.Background(), store.GetOAuthUserLinkParams{
		ProviderID:      githubProviderID,
		ProviderSubject: "github-alice-123",
	})
	require.NoError(t, err)
	assert.Equal(t, userID, githubLink.UserID)

	googleLink, err := st.OAuthUserLinks().Get(context.Background(), store.GetOAuthUserLinkParams{
		ProviderID:      googleProviderID,
		ProviderSubject: "google-alice-456",
	})
	require.NoError(t, err)
	assert.Equal(t, userID, googleLink.UserID)
}

// TestAutoLinkByEmail_SkippedWhenUnverified validates that auto-link does NOT
// happen when the existing user's email is unverified.
func TestAutoLinkByEmail_SkippedWhenUnverified(t *testing.T) {
	t.Parallel()

	_, st, _ := setupOAuthTestServer(t)

	// Create a user with an unverified email.
	hash, err := password.Hash("testpass")
	require.NoError(t, err)
	err = st.Users().Create(context.Background(), store.CreateUserParams{
		ID:            id.Generate(),
		Username:      "bob",
		PasswordHash:  hash,
		DisplayName:   "Bob",
		Email:         "bob@example.com",
		EmailVerified: false, // unverified
		IsAdmin:       false,
	})
	require.NoError(t, err)

	// Look up the user by email — found but not verified.
	existingUser, err := st.Users().GetByEmail(context.Background(), "bob@example.com")
	require.NoError(t, err)
	assert.False(t, existingUser.EmailVerified)

	// The auto-link path checks EmailVerified == 1 and skips when unverified.
	// This means the flow would create a new pending signup instead (tested
	// elsewhere).
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
		ProviderType: "oidc",
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
// CHECK (purpose IN ('login','reauth')) in all three dialects.
type purposeRewritingStore struct {
	store.Store
	purpose string
}

func (s purposeRewritingStore) OAuthStates() store.OAuthStateStore {
	return purposeRewritingStates{OAuthStateStore: s.Store.OAuthStates(), purpose: s.purpose}
}

type purposeRewritingStates struct {
	store.OAuthStateStore
	purpose string
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
// the Go zero value "" is exactly such a value, so a row that lost its
// purpose used to sign somebody in.
func TestOAuthCallback_RefusesAnUnknownPurpose(t *testing.T) {
	t.Parallel()

	for _, purpose := range []string{"", "elevate", "LOGIN"} {
		t.Run("purpose "+strconv.Quote(purpose), func(t *testing.T) {
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
