package service_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
)

// The OAuth login CSRF guard.
//
// The state alone identifies a flow, not a browser: it travels in the
// callback URL, so anyone who holds that URL can complete the flow. An
// attacker starts a login with their OWN identity, withholds the callback
// hop, and hands the live (code, state) pair to a victim -- whose browser
// then receives a session cookie for the ATTACKER's account and whose work
// lands in it. The nonce cookie is what makes the row redeemable only by
// the browser that started the flow (RFC 6749 section 10.12).
//
// These tests drive the real HTTP handler, so they pin the guard at the
// only place an attacker can reach it.

func hashNonceForTest(nonce string) string {
	sum := sha256.Sum256([]byte(nonce))
	return hex.EncodeToString(sum[:])
}

// noRedirectClient stops at the 302 so the test reads the hop the browser
// takes, rather than following it to github.com.
func noRedirectClient() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

// readBody returns the response body so a test can tell the binding
// refusal apart from the exchange failure. Both answer 400, so a status
// assertion alone would pass against the unguarded handler too.
func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(b)
}

// oauthNonceCookie finds one flow's binding cookie on a response. The name
// is derived from the state, so it is built the same way the hub builds it
// rather than matched on a prefix -- a prefix match would also catch the
// pending-signup cookie, whose family name starts with the same bytes.
func oauthNonceCookie(t *testing.T, resp *http.Response, state string) *http.Cookie {
	t.Helper()
	insecure := auth.BuildOAuthNonceCookie(state, "", time.Minute, false).Name
	secure := auth.BuildOAuthNonceCookie(state, "", time.Minute, true).Name
	for _, c := range resp.Cookies() {
		if c.Name == insecure || c.Name == secure {
			return c
		}
	}
	return nil
}

// stateFromLoginResponse reads the flow's state off the login leg's
// Location. noRedirectClient stops at the 302, so resp.Request is still the
// /login request and carries no state of its own.
func stateFromLoginResponse(t *testing.T, resp *http.Response) string {
	t.Helper()
	loc, err := resp.Location()
	require.NoError(t, err)
	state := loc.Query().Get("state")
	require.NotEmpty(t, state)
	return state
}

// seedOAuthState writes a state row directly, so a test can drive the
// callback without walking the whole login leg.
func seedOAuthState(t *testing.T, st store.Store, providerID, nonceHash string) string {
	t.Helper()
	state := id.Generate()
	require.NoError(t, st.OAuthStates().Create(context.Background(), store.CreateOAuthStateParams{
		State:        state,
		ProviderID:   providerID,
		PkceVerifier: "verifier",
		NonceHash:    nonceHash,
		ExpiresAt:    time.Now().Add(5 * time.Minute).UTC(),
	}))
	return state
}

func TestOAuthLogin_SetsNonceCookieAndStoresOnlyItsHash(t *testing.T) {
	t.Parallel()

	server, st, ks := setupOAuthTestServer(t)
	providerID := createTestProvider(t, st, ks)

	resp, err := noRedirectClient().Get(server.URL + "/auth/oauth/" + providerID + "/login")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusFound, resp.StatusCode)

	state := stateFromLoginResponse(t, resp)
	cookie := oauthNonceCookie(t, resp, state)
	require.NotNil(t, cookie, "the login leg must mint the browser-binding cookie")
	assert.NotEmpty(t, cookie.Value)
	assert.True(t, cookie.HttpOnly, "script must not be able to read the nonce")
	// Lax, never Strict: the callback is a cross-site top-level navigation
	// from the identity provider, and the browser withholds a Strict cookie
	// on exactly that hop
	// -- which would refuse every legitimate login, not only the forged ones.
	assert.Equal(t, http.SameSiteLaxMode, cookie.SameSite)
	assert.Equal(t, "/", cookie.Path)

	// Only the hash is persisted. A read of oauth_states must not yield a
	// value that completes somebody's in-flight login.
	row, err := st.OAuthStates().Get(context.Background(), state)
	require.NoError(t, err)
	assert.NotEmpty(t, row.NonceHash)
	assert.NotEqual(t, cookie.Value, row.NonceHash, "the raw nonce must never be stored")
	assert.Equal(t, hashNonceForTest(cookie.Value), row.NonceHash)
}

func TestOAuthCallback_WithoutNonceCookie_Refuses(t *testing.T) {
	t.Parallel()

	server, st, ks := setupOAuthTestServer(t)
	providerID := createTestProvider(t, st, ks)
	state := seedOAuthState(t, st, providerID, hashNonceForTest("the-real-nonce"))

	// This is the attack: the pair is valid, the browser is not the one that
	// started the flow, so it carries no nonce cookie.
	resp, err := noRedirectClient().Get(server.URL + "/auth/oauth/" + providerID + "/callback?code=c&state=" + state)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Contains(t, readBody(t, resp), "different browser",
		"the refusal must come from the binding check, not from the exchange that follows it")

	// The row SURVIVES a refusal, which is what makes consumption
	// owner-only. Consuming it here would let anyone who learns a live
	// state destroy the owner's in-flight login with one request, and the
	// owner's real callback would then report "invalid or expired state".
	surviving, err := st.OAuthStates().Get(context.Background(), state)
	require.NoError(t, err, "a refused callback must not consume somebody else's flow")
	assert.Equal(t, state, surviving.State)
}

func TestOAuthCallback_WrongNonceCookie_Refuses(t *testing.T) {
	t.Parallel()

	server, st, ks := setupOAuthTestServer(t)
	providerID := createTestProvider(t, st, ks)
	state := seedOAuthState(t, st, providerID, hashNonceForTest("the-real-nonce"))

	req, err := http.NewRequest(http.MethodGet, server.URL+"/auth/oauth/"+providerID+"/callback?code=c&state="+state, nil)
	require.NoError(t, err)
	req.AddCookie(auth.BuildOAuthNonceCookie(state, "some-other-browsers-nonce", time.Minute, false))

	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Contains(t, readBody(t, resp), "different browser",
		"a nonce from another browser must fail the binding check")
}

// TestOAuthCallback_StateWithoutNonce_Refuses pins the fail-closed half. A
// row carrying no nonce hash is redeemable by anyone who holds its state,
// which is the property the whole guard exists to remove -- so an empty
// hash must refuse rather than skip the check.
func TestOAuthCallback_StateWithoutNonce_Refuses(t *testing.T) {
	t.Parallel()

	server, st, ks := setupOAuthTestServer(t)
	providerID := createTestProvider(t, st, ks)
	state := seedOAuthState(t, st, providerID, "")

	resp, err := noRedirectClient().Get(server.URL + "/auth/oauth/" + providerID + "/callback?code=c&state=" + state)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Contains(t, readBody(t, resp), "different browser",
		"an unbound row must refuse rather than skip the check")
}

// TestOAuthCallback_MatchingNoncePassesTheGuard proves the guard admits the
// browser that started the flow. The exchange then fails because no real
// identity provider answers in a test, which is a DIFFERENT refusal -- and
// that difference is the assertion: reaching the exchange means the binding
// check passed.
func TestOAuthCallback_MatchingNoncePassesTheGuard(t *testing.T) {
	t.Parallel()

	server, st, ks := setupOAuthTestServer(t)
	providerID := createTestOIDCProviderWithStub(t, st, ks)

	loginResp, err := noRedirectClient().Get(server.URL + "/auth/oauth/" + providerID + "/login")
	require.NoError(t, err)
	defer func() { _ = loginResp.Body.Close() }()
	state := stateFromLoginResponse(t, loginResp)
	cookie := oauthNonceCookie(t, loginResp, state)
	require.NotNil(t, cookie)

	req, err := http.NewRequest(http.MethodGet, server.URL+"/auth/oauth/"+providerID+"/callback?code=c&state="+state, nil)
	require.NoError(t, err)
	req.AddCookie(cookie)

	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	// A POSITIVE assertion, not merely the absence of the refusal: any
	// other failure -- a 500 from a broken provider load, a panic rendered
	// as an error page -- would also lack the refusal text and would report
	// a broken guard as a working one.
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Contains(t, readBody(t, resp), "OAuth exchange failed",
		"the browser that started the flow must pass the binding check and reach the exchange")

	// And the callback clears the nonce on the way out: it is single-use, so
	// one left in the browser would be replayed against the next flow.
	cleared := oauthNonceCookie(t, resp, state)
	require.NotNil(t, cleared, "the callback must clear the nonce cookie")
	assert.Empty(t, cleared.Value)
	assert.Less(t, cleared.MaxAge, 0)
}

// TestOAuthCallback_ConcurrentFlowsDoNotEvictEachOther pins the reason the
// hub gives the nonce cookie a name per flow.
//
// With one shared cookie name the second login overwrote the first one's
// nonce, so completing the older tab failed with a refusal aimed at an
// attacker and shown to a user who did nothing wrong. Two browser tabs is
// an ordinary thing to do.
func TestOAuthCallback_ConcurrentFlowsDoNotEvictEachOther(t *testing.T) {
	t.Parallel()

	server, st, ks := setupOAuthTestServer(t)
	providerID := createTestOIDCProviderWithStub(t, st, ks)

	startFlow := func() (state string, cookie *http.Cookie) {
		resp, err := noRedirectClient().Get(server.URL + "/auth/oauth/" + providerID + "/login")
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		s := stateFromLoginResponse(t, resp)
		c := oauthNonceCookie(t, resp, s)
		require.NotNil(t, c)
		return s, c
	}

	firstState, firstCookie := startFlow()
	secondState, secondCookie := startFlow()
	require.NotEqual(t, firstState, secondState)
	assert.NotEqual(t, firstCookie.Name, secondCookie.Name,
		"each flow must carry its own cookie name")

	// The browser holds both. Complete the OLDER flow, which is the one a
	// shared cookie name would evict.
	req, err := http.NewRequest(http.MethodGet, server.URL+"/auth/oauth/"+providerID+"/callback?code=c&state="+firstState, nil)
	require.NoError(t, err)
	req.AddCookie(firstCookie)
	req.AddCookie(secondCookie)

	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Contains(t, readBody(t, resp), "OAuth exchange failed",
		"a second login in the same browser must not break the first")

	// Only the used flow's cookie is cleared; the other stays live so its
	// own tab can still finish.
	var clearedNames []string
	for _, c := range resp.Cookies() {
		if c.MaxAge < 0 {
			clearedNames = append(clearedNames, c.Name)
		}
	}
	assert.Contains(t, clearedNames, firstCookie.Name)
	assert.NotContains(t, clearedNames, secondCookie.Name)
}

// TestOAuthFlowBindingUnderSecureCookies drives the whole flow in the shape
// production runs in.
//
// Every other test here leaves secure_cookies off, so they exercise the
// plain "leapmux-oauth-<state>" name only. With it on, the cookie takes the
// __Host- prefix, and __Host- is honoured by a browser only when the cookie
// also carries Path=/ and no Domain. A cookie that broke either rule would
// be dropped, and the callback would then refuse every legitimate login --
// a failure no insecure-mode test can see.
func TestOAuthFlowBindingUnderSecureCookies(t *testing.T) {
	t.Parallel()

	server, st, ks, set := setupOAuthTestServerWithSettings(t)
	require.NoError(t, settings.KeySecureCookies.Set(context.Background(), set, true))
	providerID := createTestOIDCProviderWithStub(t, st, ks)

	loginResp, err := noRedirectClient().Get(server.URL + "/auth/oauth/" + providerID + "/login")
	require.NoError(t, err)
	defer func() { _ = loginResp.Body.Close() }()

	state := stateFromLoginResponse(t, loginResp)
	cookie := oauthNonceCookie(t, loginResp, state)
	require.NotNil(t, cookie, "the login leg must mint the cookie under secure cookies too")
	assert.True(t, strings.HasPrefix(cookie.Name, auth.SecureOAuthNonceCookieName+"-"))
	assert.True(t, cookie.Secure)
	assert.Equal(t, "/", cookie.Path)
	assert.Empty(t, cookie.Domain)

	row, err := st.OAuthStates().Get(context.Background(), state)
	require.NoError(t, err)
	assert.Equal(t, hashNonceForTest(cookie.Value), row.NonceHash)

	// The matching cookie passes the guard.
	req, err := http.NewRequest(http.MethodGet, server.URL+"/auth/oauth/"+providerID+"/callback?code=c&state="+state, nil)
	require.NoError(t, err)
	req.AddCookie(cookie)
	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Contains(t, readBody(t, resp), "OAuth exchange failed",
		"the matching cookie must pass the guard and reach the exchange")

	// And a browser holding only the INSECURE spelling is refused, so the
	// two names cannot be confused for one another.
	state2 := seedOAuthState(t, st, providerID, hashNonceForTest("n2"))
	req2, err := http.NewRequest(http.MethodGet, server.URL+"/auth/oauth/"+providerID+"/callback?code=c&state="+state2, nil)
	require.NoError(t, err)
	req2.AddCookie(auth.BuildOAuthNonceCookie(state2, "n2", time.Minute, false))
	resp2, err := noRedirectClient().Do(req2)
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()
	assert.Contains(t, readBody(t, resp2), "different browser",
		"the insecure cookie name must not satisfy a secure-cookie hub")
}

// createTestOIDCProviderWithStub registers an OIDC provider whose issuer is
// a local server.
//
// A test that drives the callback PAST the binding check reaches
// provider.Exchange. With the github provider that leg is a real outbound
// POST to github.com carrying fake credentials, which is slow on a network
// that drops rather than refuses, and is not the unit suite's business. The
// stub answers discovery and then refuses the token exchange, so the
// handler reaches its "OAuth exchange failed" branch in process.
func createTestOIDCProviderWithStub(t *testing.T, st store.Store, ks *keystore.Keystore) string {
	t.Helper()

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{
			"issuer": %q,
			"authorization_endpoint": %q,
			"token_endpoint": %q,
			"jwks_uri": %q,
			"id_token_signing_alg_values_supported": ["RS256"]
		}`, server.URL, server.URL+"/authorize", server.URL+"/token", server.URL+"/jwks")
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	})

	providerID := id.Generate()
	encSecret, err := ks.Encrypt([]byte("test-client-secret"), keystore.ProviderAAD(providerID))
	require.NoError(t, err)
	require.NoError(t, st.OAuthProviders().Create(context.Background(), store.CreateOAuthProviderParams{
		ID:           providerID,
		ProviderType: "oidc",
		Name:         "Test OIDC",
		IssuerURL:    server.URL,
		ClientID:     "test-client-id",
		ClientSecret: encSecret,
		Scopes:       "openid email profile",
		TrustEmail:   true,
		Enabled:      true,
	}))
	return providerID
}

// The pending-signup binding.
//
// The state nonce protects the callback, but the NEW-ACCOUNT branch then
// hands the browser a signup token in a URL. Without a second binding the
// token specifies a flow and not a browser, so an attacker who completes their
// OWN callback can deliver that link to a victim: the victim picks a
// username, and the hub creates an account linked to the ATTACKER's OAuth
// identity and signs the victim into it. The attacker can return to that
// account through the provider at any time, and the victim's work lands in
// it. These tests pin the second half of the same property.

// signupNonceCookie finds one pending signup's binding cookie on a
// response, by the name the hub builds rather than by a prefix.
func signupNonceCookie(t *testing.T, resp *http.Response, token string) *http.Cookie {
	t.Helper()
	insecure := auth.BuildOAuthSignupNonceCookie(token, "", time.Minute, false).Name
	secure := auth.BuildOAuthSignupNonceCookie(token, "", time.Minute, true).Name
	for _, c := range resp.Cookies() {
		if c.Name == insecure || c.Name == secure {
			return c
		}
	}
	return nil
}

func TestGetPendingOAuthSignup_WithoutBindingCookie_Refuses(t *testing.T) {
	t.Parallel()

	_, client, st, ks, _ := setupOAuthTestServerWithAuthService(t)
	providerID := createTestProvider(t, st, ks)
	token := id.Generate()
	insertPendingSignup(t, st, ks, providerID, token, "victim@example.com", "Victim", "sub-1",
		time.Now().Add(5*time.Minute).UTC())

	// The attack: a valid token, delivered to a browser that never started
	// the flow, so it carries no binding cookie.
	_, err := client.GetPendingOAuthSignup(context.Background(),
		connect.NewRequest(&leapmuxv1.GetPendingOAuthSignupRequest{SignupToken: token}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "different browser")
}

func TestCompleteOAuthSignup_WithoutBindingCookie_Refuses(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, client, st, ks, _ := setupOAuthTestServerWithAuthService(t)
	providerID := createTestProvider(t, st, ks)
	token := id.Generate()
	insertPendingSignup(t, st, ks, providerID, token, "victim@example.com", "Victim", "sub-1",
		time.Now().Add(5*time.Minute).UTC())

	_, err := client.CompleteOAuthSignup(ctx,
		connect.NewRequest(&leapmuxv1.CompleteOAuthSignupRequest{SignupToken: token, Username: "victim"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "different browser")

	// No account was created, and the pending row survives for the browser
	// that really started the flow.
	_, err = st.Users().GetByUsername(ctx, "victim")
	assert.ErrorIs(t, err, store.ErrNotFound)
	_, err = st.PendingOAuthSignups().Get(ctx, token)
	assert.NoError(t, err, "a refused signup must not consume somebody else's pending row")
}

// TestCompleteOAuthSignup_WrongBindingCookie_Refuses pins that any nonce
// will not do: the cookie must be the one this flow minted.
func TestCompleteOAuthSignup_WrongBindingCookie_Refuses(t *testing.T) {
	t.Parallel()

	_, client, st, ks, _ := setupOAuthTestServerWithAuthService(t)
	providerID := createTestProvider(t, st, ks)
	token := id.Generate()
	insertPendingSignup(t, st, ks, providerID, token, "victim@example.com", "Victim", "sub-1",
		time.Now().Add(5*time.Minute).UTC())

	req := connect.NewRequest(&leapmuxv1.CompleteOAuthSignupRequest{SignupToken: token, Username: "victim"})
	wrong := auth.BuildOAuthSignupNonceCookie(token, "some-other-browsers-nonce", time.Minute, false)
	req.Header().Set("Cookie", wrong.Name+"="+wrong.Value)

	_, err := client.CompleteOAuthSignup(context.Background(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

// TestCompleteOAuthSignup_UnboundRowRefuses pins the fail-closed half: a
// pending row carrying no nonce hash is completable by anyone who holds its
// token, which is the property the binding exists to remove.
func TestCompleteOAuthSignup_UnboundRowRefuses(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, client, st, ks, _ := setupOAuthTestServerWithAuthService(t)
	providerID := createTestProvider(t, st, ks)
	token := id.Generate()
	encAccess, err := ks.Encrypt([]byte("a"), keystore.AccessTokenAAD(token, providerID))
	require.NoError(t, err)
	encRefresh, err := ks.Encrypt([]byte("r"), keystore.RefreshTokenAAD(token, providerID))
	require.NoError(t, err)
	require.NoError(t, st.PendingOAuthSignups().Create(ctx, store.CreatePendingOAuthSignupParams{
		Token:           token,
		ProviderID:      providerID,
		ProviderSubject: "sub-1",
		Email:           "victim@example.com",
		AccessToken:     encAccess,
		RefreshToken:    encRefresh,
		TokenType:       "bearer",
		TokenExpiresAt:  time.Now().Add(time.Hour).UTC(),
		KeyVersion:      int64(ks.ActiveVersion()),
		ExpiresAt:       time.Now().Add(5 * time.Minute).UTC(),
	}))

	_, err = client.CompleteOAuthSignup(ctx, signupReq(&leapmuxv1.CompleteOAuthSignupRequest{
		SignupToken: token, Username: "victim",
	}))
	require.Error(t, err, "an unbound row must refuse rather than skip the check")
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

// TestOAuthCallback_NewUserBindsThePendingSignup drives the real callback
// into its new-account branch and pins that the hand-off carries the
// binding forward: the response mints a signup cookie, and the row stores
// only that nonce's hash.
func TestOAuthCallback_NewUserBindsThePendingSignup(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	server, st, ks, set := setupOAuthTestServerWithSettings(t)
	enableSignup(t, set)
	providerID := createTestOIDCProviderWithStubClaims(t, st, ks, "newcomer@example.com", "sub-new")

	loginResp, err := noRedirectClient().Get(server.URL + "/auth/oauth/" + providerID + "/login")
	require.NoError(t, err)
	defer func() { _ = loginResp.Body.Close() }()
	state := stateFromLoginResponse(t, loginResp)
	flowCookie := oauthNonceCookie(t, loginResp, state)
	require.NotNil(t, flowCookie)

	req, err := http.NewRequest(http.MethodGet,
		server.URL+"/auth/oauth/"+providerID+"/callback?code=c&state="+state, nil)
	require.NoError(t, err)
	req.AddCookie(flowCookie)
	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusFound, resp.StatusCode, readBody(t, resp))
	loc, err := resp.Location()
	require.NoError(t, err)
	require.Equal(t, "/oauth/complete-signup", loc.Path)
	signupToken := loc.Query().Get("token")
	require.NotEmpty(t, signupToken)

	cookie := signupNonceCookie(t, resp, signupToken)
	require.NotNil(t, cookie, "the hand-off must mint a binding cookie for the pending signup")
	assert.NotEmpty(t, cookie.Value)
	assert.True(t, cookie.HttpOnly, "script must not read the signup nonce")
	assert.Equal(t, "/", cookie.Path)

	pending, err := st.PendingOAuthSignups().Get(ctx, signupToken)
	require.NoError(t, err)
	assert.NotEqual(t, cookie.Value, pending.NonceHash, "the raw nonce must never be stored")
	assert.Equal(t, hashNonceForTest(cookie.Value), pending.NonceHash)
}

// createTestOIDCProviderWithStubClaims registers an OIDC provider whose
// issuer is a local server that COMPLETES the exchange, so a test can drive
// the callback into its new-account branch without any outbound call.
//
// The identity provider must sign a real RS256 id_token, because go-oidc
// verifies it against the advertised JWKS -- a stub that returned an
// unsigned token would fail before the branch under test.
func createTestOIDCProviderWithStubClaims(t *testing.T, st store.Store, ks *keystore.Keystore, email, subject string) string {
	t.Helper()
	// A conforming provider re-authenticates when asked, so the default stub
	// reports an auth_time of NOW. The re-authentication leg refuses a
	// provider that claims to verify and then reports nothing, which is the
	// whole point of the check -- see createTestOIDCProviderWithAuthTime for
	// the tests that drive the other answers.
	return createTestOIDCProviderWithAuthTime(t, st, ks, email, subject, func() any { return time.Now().Unix() })
}

// createTestOIDCProviderWithAuthTime is the same stub with the auth_time
// claim under the caller's control, so a test can drive an absent claim (a
// provider that ignored max_age) and a stale one.
//
// authTime returns the claim VALUE, or nil to omit it entirely; it is a
// function rather than a value so a test can pin the instant relative to
// when the token is minted.
func createTestOIDCProviderWithAuthTime(
	t *testing.T,
	st store.Store,
	ks *keystore.Keystore,
	email, subject string,
	authTime func() any,
) string {
	t.Helper()

	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{
			"issuer": %q,
			"authorization_endpoint": %q,
			"token_endpoint": %q,
			"jwks_uri": %q,
			"id_token_signing_alg_values_supported": ["RS256"]
		}`, server.URL, server.URL+"/authorize", server.URL+"/token", server.URL+"/jwks")
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		jwk := jose.JSONWebKey{Key: &privKey.PublicKey, KeyID: "test-key", Algorithm: "RS256", Use: "sig"}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		signer, err := jose.NewSigner(
			jose.SigningKey{Algorithm: jose.RS256, Key: privKey},
			(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test-key"))
		require.NoError(t, err)
		idClaims := map[string]any{
			"iss":            server.URL,
			"sub":            subject,
			"aud":            "test-client-id",
			"exp":            time.Now().Add(time.Hour).Unix(),
			"iat":            time.Now().Unix(),
			"email":          email,
			"email_verified": true,
			"name":           "Newcomer",
		}
		if v := authTime(); v != nil {
			idClaims["auth_time"] = v
		}
		raw, err := jwt.Signed(signer).Claims(idClaims).Serialize()
		require.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":"a","refresh_token":"r","token_type":"Bearer","id_token":%q}`, raw)
	})

	providerID := id.Generate()
	encSecret, err := ks.Encrypt([]byte("test-client-secret"), keystore.ProviderAAD(providerID))
	require.NoError(t, err)
	require.NoError(t, st.OAuthProviders().Create(context.Background(), store.CreateOAuthProviderParams{
		ID:           providerID,
		ProviderType: "oidc",
		Name:         "Test OIDC",
		IssuerURL:    server.URL,
		ClientID:     "test-client-id",
		ClientSecret: encSecret,
		Scopes:       "openid email profile",
		TrustEmail:   false,
		Enabled:      true,
	}))
	return providerID
}
