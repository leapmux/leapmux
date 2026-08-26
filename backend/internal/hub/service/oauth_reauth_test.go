package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	huboauth "github.com/leapmux/leapmux/internal/hub/oauth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// The OAuth re-authentication leg.
//
// An OAuth-only account holds no password and no passkey, so the identity
// provider is the only thing that can confirm the person is still there.
// The leg therefore ELEVATES a session that already exists -- it must never
// create a session, an account, or an identity link, because each of those
// is a credential change wearing the clothes of a verification.

// reauthSession creates a user with a linked OAuth identity and a live
// session, and returns the session cookie plus the ids.
func reauthSession(t *testing.T, st store.Store, providerID, subject string) (cookie *http.Cookie, userID, sessionID string) {
	t.Helper()
	ctx := context.Background()

	userID = id.Generate()
	require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
		ID: userID, Username: "oauthonly" + userID[:6], PasswordHash: "hash",
		DisplayName: "OAuth Only", Email: userID[:6] + "@example.com", EmailVerified: true,
	}))
	require.NoError(t, st.OAuthUserLinks().Create(ctx, store.CreateOAuthUserLinkParams{
		UserID: userid.MustNew(userID), ProviderID: providerID, ProviderSubject: subject,
	}))
	sessionID, _, err := auth.CreateSession(ctx, st, userid.MustNew(userID), auth.DefaultSessionDuration)
	require.NoError(t, err)
	return &http.Cookie{Name: auth.CookieName, Value: sessionID}, userID, sessionID
}

// completeCallback drives the callback leg with the browser's nonce cookie,
// which is the only way past the binding guard.
func completeCallback(t *testing.T, server *httptest.Server, providerID, state string, nonce *http.Cookie) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet,
		server.URL+"/auth/oauth/"+providerID+"/callback?code=test&state="+state, nil)
	require.NoError(t, err)
	req.AddCookie(nonce)
	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// countSessions reports how many live sessions the user holds, so a test can
// prove the step-up created none.
func countSessions(t *testing.T, st store.Store, userID string) int {
	t.Helper()
	page, err := st.Sessions().ListByUserID(context.Background(), store.ListUserSessionsParams{
		UserID:     userid.MustNew(userID),
		PageParams: store.PageParams{Limit: 100},
	}, time.Now().UTC())
	require.NoError(t, err)
	return len(page.Rows)
}

// beginReauth walks the start leg and returns the state, the nonce cookie,
// and the authorization URL the browser was sent to.
func beginReauth(t *testing.T, server *httptest.Server, providerID string, cookie *http.Cookie, redirect string) (state string, nonce *http.Cookie, authURL *url.URL) {
	t.Helper()
	target := server.URL + "/auth/oauth/" + providerID + "/reauth"
	if redirect != "" {
		target += "?redirect=" + url.QueryEscape(redirect)
	}
	req, err := http.NewRequest(http.MethodGet, target, nil)
	require.NoError(t, err)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, http.StatusFound, resp.StatusCode)

	authURL, err = resp.Location()
	require.NoError(t, err)
	state = authURL.Query().Get("state")
	require.NotEmpty(t, state)
	return state, oauthNonceCookie(t, resp, state), authURL
}

// TestOAuthReauth_RefusesACrossSiteNavigation is the CSRF regression.
//
// The session cookie is SameSite=Lax, which a top-level cross-site navigation
// CARRIES, so an attacker page could point the victim's browser at this leg:
// the hub authenticated the session, minted a reauth row bound to it, and
// sent the browser to the provider with prompt=login. A victim who signed in
// at that prompt came back with the session ELEVATED without ever choosing to
// verify -- and whoever else held a copy of that cookie then passed every
// gate the window protects, the command-line credential mint included.
//
// The three allowed shapes are pinned beside it, because a guard that also
// refuses the app's own link is an outage rather than a fix.
func TestOAuthReauth_RefusesACrossSiteNavigation(t *testing.T) {
	t.Parallel()

	server, st, ks := setupOAuthTestServer(t)
	providerID := createTestProvider(t, st, ks)
	cookie, _, _ := reauthSession(t, st, providerID, "sub-1")

	get := func(t *testing.T, fetchSite string, set bool) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, server.URL+"/auth/oauth/"+providerID+"/reauth", nil)
		require.NoError(t, err)
		req.AddCookie(cookie)
		if set {
			req.Header.Set("Sec-Fetch-Site", fetchSite)
		}
		resp, err := noRedirectClient().Do(req)
		require.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}

	for _, site := range []string{"cross-site", "same-site"} {
		t.Run("refuses "+site, func(t *testing.T) {
			resp := get(t, site, true)
			assert.Equal(t, http.StatusForbidden, resp.StatusCode,
				"a navigation another document started must not begin a step-up")
			assert.Empty(t, resp.Header.Get("Location"), "the browser must not reach the provider")
		})
	}

	for _, tc := range []struct {
		name string
		site string
		set  bool
	}{
		{name: "admits the app's own link", site: "same-origin", set: true},
		{name: "admits a typed or bookmarked address", site: "none", set: true},
		{name: "admits a browser that sends no fetch metadata", set: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := get(t, tc.site, tc.set)
			assert.Equal(t, http.StatusFound, resp.StatusCode)
			assert.NotEmpty(t, resp.Header.Get("Location"))
		})
	}
}

func TestOAuthReauth_RequiresASession(t *testing.T) {
	t.Parallel()

	server, st, ks := setupOAuthTestServer(t)
	providerID := createTestProvider(t, st, ks)

	resp, err := noRedirectClient().Get(server.URL + "/auth/oauth/" + providerID + "/reauth")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"there is nothing to elevate without a session")
}

// TestOAuthReauth_RecordsThePurposeAndTheSessionAndForcesAPrompt pins the
// three things the start leg must do that a plain login does not.
func TestOAuthReauth_RecordsThePurposeAndTheSessionAndForcesAPrompt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	server, st, ks := setupOAuthTestServer(t)
	providerID := createTestOIDCProviderWithStubClaims(t, st, ks, "user@example.com", "sub-1")
	cookie, _, sessionID := reauthSession(t, st, providerID, "sub-1")

	state, nonce, authURL := beginReauth(t, server, providerID, cookie, "/auth/cli/start?state=s")
	require.NotNil(t, nonce, "the reauth leg must bind the flow to this browser, exactly as login does")

	// Without prompt=login the provider silently reuses its own session, the
	// browser bounces back in a fraction of a second, and the click proves
	// nothing at all.
	assert.Equal(t, "login", authURL.Query().Get("prompt"))

	row, err := st.OAuthStates().Get(ctx, state)
	require.NoError(t, err)
	assert.Equal(t, store.OAuthStatePurposeReauth, row.Purpose)
	assert.Equal(t, sessionID, row.SessionID,
		"the state specifies the session it will elevate, so the callback cannot be aimed elsewhere")
	assert.Equal(t, "/auth/cli/start?state=s", row.RedirectURI)
}

func TestOAuthLogin_RecordsTheLoginPurposeAndNoSession(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	server, st, ks := setupOAuthTestServer(t)
	providerID := createTestProvider(t, st, ks)

	resp, err := noRedirectClient().Get(server.URL + "/auth/oauth/" + providerID + "/login")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	state := stateFromLoginResponse(t, resp)

	row, err := st.OAuthStates().Get(ctx, state)
	require.NoError(t, err)
	assert.Equal(t, store.OAuthStatePurposeLogin, row.Purpose)
	assert.Empty(t, row.SessionID, "a login elevates nothing")

	loc, err := resp.Location()
	require.NoError(t, err)
	assert.Empty(t, loc.Query().Get("prompt"), "an ordinary login must not force a re-prompt")
}

// TestOAuthReauth_ElevatesTheSessionAndCreatesNothing is the leg's whole
// contract, driven end to end against a signing identity provider.
func TestOAuthReauth_ElevatesTheSessionAndCreatesNothing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	server, st, ks := setupOAuthTestServer(t)
	providerID := createTestOIDCProviderWithStubClaims(t, st, ks, "user@example.com", "sub-1")
	cookie, userID, sessionID := reauthSession(t, st, providerID, "sub-1")

	sessionsBefore := countSessions(t, st, userID)
	state, nonce, _ := beginReauth(t, server, providerID, cookie, "/auth/cli/start?state=s")
	require.NotNil(t, nonce)

	resp := completeCallback(t, server, providerID, state, nonce)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	loc, err := resp.Location()
	require.NoError(t, err)
	assert.Equal(t, "/auth/cli/start", loc.Path, "the browser returns to where it came from")

	// The session is elevated...
	row, err := st.Sessions().GetByID(ctx, sessionID, time.Now().UTC())
	require.NoError(t, err)
	require.NotNil(t, row.ElevationExpiresAt)
	assert.True(t, row.ElevationExpiresAt.After(time.Now().UTC()))

	// ...and NOTHING was created. No second session (a Set-Cookie here would
	// be a silent session swap), and no extra identity link.
	assert.Equal(t, sessionsBefore, countSessions(t, st, userID), "a step-up must not create a session")
	for _, c := range resp.Cookies() {
		assert.NotEqual(t, auth.CookieName, c.Name, "a step-up must not issue a session cookie")
	}
	links, err := st.OAuthUserLinks().ListByUser(ctx, userid.MustNew(userID))
	require.NoError(t, err)
	assert.Len(t, links, 1, "a step-up must not attach an identity")
}

// TestOAuthReauth_RefusesAnIdentityTheUserDoesNotHold is the load-bearing
// check. A different account's identity, or an unlinked one, proves nothing
// about who is at the keyboard of THIS session -- and linking it here would
// turn a verification into a credential change.
func TestOAuthReauth_RefusesAnIdentityTheUserDoesNotHold(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	server, st, ks := setupOAuthTestServer(t)
	// The provider returns subject "attacker", which is NOT the subject
	// linked to the acting user.
	providerID := createTestOIDCProviderWithStubClaims(t, st, ks, "attacker@example.com", "attacker")
	cookie, _, sessionID := reauthSession(t, st, providerID, "victim-subject")

	state, nonce, _ := beginReauth(t, server, providerID, cookie, "")
	require.NotNil(t, nonce)

	resp := completeCallback(t, server, providerID, state, nonce)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Contains(t, readBody(t, resp), "not linked")

	row, err := st.Sessions().GetByID(ctx, sessionID, time.Now().UTC())
	require.NoError(t, err)
	assert.Nil(t, row.ElevationExpiresAt, "a refused step-up must elevate nothing")
}

// TestOAuthReauth_RefusesAnUnlinkedIdentity covers the other half: the
// subject belongs to nobody at all.
func TestOAuthReauth_RefusesAnUnlinkedIdentity(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	server, st, ks := setupOAuthTestServer(t)
	providerID := createTestOIDCProviderWithStubClaims(t, st, ks, "stranger@example.com", "stranger")

	// A user with NO link for this provider at all.
	userID := id.Generate()
	require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
		ID: userID, Username: "nolinks", PasswordHash: "hash", DisplayName: "No Links",
		Email: "nolinks@example.com", EmailVerified: true,
	}))
	sessionID, _, err := auth.CreateSession(ctx, st, userid.MustNew(userID), auth.DefaultSessionDuration)
	require.NoError(t, err)
	cookie := &http.Cookie{Name: auth.CookieName, Value: sessionID}

	state, nonce, _ := beginReauth(t, server, providerID, cookie, "")
	require.NotNil(t, nonce)

	resp := completeCallback(t, server, providerID, state, nonce)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	row, err := st.Sessions().GetByID(ctx, sessionID, time.Now().UTC())
	require.NoError(t, err)
	assert.Nil(t, row.ElevationExpiresAt)

	// And no account was created for the stranger's identity.
	links, err := st.OAuthUserLinks().ListByUser(ctx, userid.MustNew(userID))
	require.NoError(t, err)
	assert.Empty(t, links)
}

// TestOAuthReauth_RefusesWithoutTheBrowserBinding pins that the reauth leg
// keeps the same nonce guard as login. Without it a state that anybody could
// redeem would hand an attacker's completed provider flow the VICTIM's
// elevation, because the row specifies the session to stamp.
func TestOAuthReauth_RefusesWithoutTheBrowserBinding(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	server, st, ks := setupOAuthTestServer(t)
	providerID := createTestOIDCProviderWithStubClaims(t, st, ks, "user@example.com", "sub-1")
	cookie, _, sessionID := reauthSession(t, st, providerID, "sub-1")

	state, _, _ := beginReauth(t, server, providerID, cookie, "")

	// The callback with NO nonce cookie: a browser that never started it.
	req, err := http.NewRequest(http.MethodGet,
		server.URL+"/auth/oauth/"+providerID+"/callback?code=test&state="+state, nil)
	require.NoError(t, err)
	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Contains(t, readBody(t, resp), "different browser")

	row, err := st.Sessions().GetByID(ctx, sessionID, time.Now().UTC())
	require.NoError(t, err)
	assert.Nil(t, row.ElevationExpiresAt)

	// The refusal leaves the row alone, so the real browser can still finish.
	_, err = st.OAuthStates().Get(ctx, state)
	assert.NoError(t, err)
}

// The account-shape rule.
//
// The provider arm is legal only for an account that holds NEITHER a
// password NOR a passkey, because for that account the provider IS the
// sign-in credential and the elevation is exactly as strong as the sign-in
// it stands on. On an account that holds a password it is a DOWNGRADE:
// "the browser can still reach the provider session" would buy the same
// two-hour window as knowing the password, and on GitHub -- which cannot
// force a re-authentication -- it would buy it with one click and no
// credential at all.
//
// The rule is checked at BOTH legs, and the two tests below are why: the
// start leg keeps the browser from ever leaving, and the callback leg is
// where the grant happens.

func TestOAuthReauth_RefusesAnAccountThatHoldsAPassword(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	server, st, ks := setupOAuthTestServer(t)
	providerID := createTestOIDCProviderWithStubClaims(t, st, ks, "user@example.com", "sub-1")
	cookie, userID, _ := reauthSession(t, st, providerID, "sub-1")
	require.NoError(t, st.Users().UpdatePassword(ctx, store.UpdateUserPasswordParams{
		PasswordHash: "hash", ID: userID,
	}))

	req, err := http.NewRequest(http.MethodGet, server.URL+"/auth/oauth/"+providerID+"/reauth", nil)
	require.NoError(t, err)
	req.AddCookie(cookie)
	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Contains(t, readBody(t, resp), "password or a passkey")

	// Nothing was minted, so the browser never leaves for the provider.
	assert.Equal(t, 0, countOAuthStates(t, st), "a refused start leg must mint no state row")
}

func TestOAuthReauth_RefusesAnAccountThatHoldsAPasskey(t *testing.T) {
	t.Parallel()

	server, st, ks := setupOAuthTestServer(t)
	providerID := createTestOIDCProviderWithStubClaims(t, st, ks, "user@example.com", "sub-1")
	cookie, userID, _ := reauthSession(t, st, providerID, "sub-1")
	seedPasskeyCredential(t, st, userID, "laptop")

	req, err := http.NewRequest(http.MethodGet, server.URL+"/auth/oauth/"+providerID+"/reauth", nil)
	require.NoError(t, err)
	req.AddCookie(cookie)
	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"a passkey is a factor the account can present, so the provider arm is a downgrade")
	assert.Contains(t, readBody(t, resp), "password or a passkey")
}

// A passkey the hub CANNOT run still refuses the arm, and that is the cut
// rather than an oversight.
//
// There used to be a second tier here: an account whose only factor was a
// passkey this hub could not run was bridged by a provider that fills
// auth_time. It is gone. A hub that cannot run a ceremony has one cause --
// no usable browser origin, because public_url is unset and nothing is
// listening -- so the tier existed only to rescue users from an
// administrator's misconfiguration whose real remedy is to restore the
// address. Refusing outright is stricter and simpler: the account waits for
// the hub to be repaired rather than elevating on a weaker claim than the
// passkey it holds.
//
// Both providers are covered, because the rule no longer asks which one:
// the OIDC provider that used to qualify is refused exactly like GitHub.
func TestOAuthReauth_RefusesAPasskeyEvenOnAHubThatCannotRunOne(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name         string
		makeProvider func(t *testing.T, st store.Store, ks *keystore.Keystore) string
	}{
		{"oidc", func(t *testing.T, st store.Store, ks *keystore.Keystore) string {
			return createTestOIDCProviderWithStubClaims(t, st, ks, "user@example.com", "sub-1")
		}},
		{"github", func(t *testing.T, st store.Store, ks *keystore.Keystore) string {
			return createTestProvider(t, st, ks)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// An empty listen leaves no browser-reachable origin, so no
			// ceremony can run: the account holds a passkey the hub cannot
			// use.
			server, st, ks, _ := setupOAuthTestServerWithListen(t, "")
			providerID := tc.makeProvider(t, st, ks)
			cookie, userID, _ := reauthSession(t, st, providerID, "sub-1")
			seedPasskeyCredential(t, st, userID, "laptop")

			req, err := http.NewRequest(http.MethodGet, server.URL+"/auth/oauth/"+providerID+"/reauth", nil)
			require.NoError(t, err)
			req.AddCookie(cookie)
			resp, err := noRedirectClient().Do(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			assert.Equal(t, http.StatusForbidden, resp.StatusCode,
				"a provider must not stand in for a passkey the account holds")
			assert.Contains(t, readBody(t, resp), "password or a passkey")
		})
	}
}

// A PASSWORD disqualifies the arm whatever the provider proves and whatever
// the hub can run. It is the one shape where the account chose a secret, and
// the arm would let somebody past it without knowing it.
func TestOAuthReauth_RefusesAPasswordEvenOnAHubThatCannotRunPasskeys(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	server, st, ks, _ := setupOAuthTestServerWithListen(t, "")
	providerID := createTestOIDCProviderWithStubClaims(t, st, ks, "user@example.com", "sub-1")
	cookie, userID, _ := reauthSession(t, st, providerID, "sub-1")
	require.NoError(t, st.Users().UpdatePassword(ctx, store.UpdateUserPasswordParams{
		PasswordHash: "hash", ID: userID,
	}))

	req, err := http.NewRequest(http.MethodGet, server.URL+"/auth/oauth/"+providerID+"/reauth", nil)
	require.NoError(t, err)
	req.AddCookie(cookie)
	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

// TestOAuthReauth_RefusesAPasswordThatArrivedBeforeTheCallback pins why the
// grant leg checks the shape AGAIN. The state row lives for oauthStateExpiry,
// and the first-credential rule lets an account with nothing attach a
// password in another tab inside that window -- so a row minted while the
// account held nothing would otherwise buy provider-strength elevation for
// an account that now holds a password.
func TestOAuthReauth_RefusesAPasswordThatArrivedBeforeTheCallback(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	server, st, ks := setupOAuthTestServer(t)
	providerID := createTestOIDCProviderWithStubClaims(t, st, ks, "user@example.com", "sub-1")
	cookie, userID, sessionID := reauthSession(t, st, providerID, "sub-1")

	// The start leg passes: the account holds nothing yet.
	state, nonce, _ := beginReauth(t, server, providerID, cookie, "")
	require.NotNil(t, nonce)

	// A password lands while the browser is at the provider.
	require.NoError(t, st.Users().UpdatePassword(ctx, store.UpdateUserPasswordParams{
		PasswordHash: "hash", ID: userID,
	}))

	resp := completeCallback(t, server, providerID, state, nonce)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Contains(t, readBody(t, resp), "password or a passkey")

	row, err := st.Sessions().GetByID(ctx, sessionID, time.Now().UTC())
	require.NoError(t, err)
	assert.Nil(t, row.ElevationExpiresAt, "a refused step-up must elevate nothing")
}

// countOAuthStates reports how many flow rows exist, so a test can prove a
// refused start leg minted none.
func countOAuthStates(t *testing.T, st store.Store) int {
	t.Helper()
	// There is no listing for oauth_states, so sweep with a cutoff far in the
	// future: the count is what the sweep deletes.
	n, err := st.Cleanup().DeleteExpiredOAuthStates(context.Background(), time.Now().Add(24*time.Hour))
	require.NoError(t, err)
	return int(n)
}

// The hub grants on the completed ROUND TRIP and reads nothing the provider
// says about when the person authenticated. These two cases pin that, in the
// two shapes a provider can answer in.
//
// It used to demand a fresh auth_time from any provider whose TYPE was oidc,
// which made a dead end: Google documents that it does not re-authenticate on
// request and gates auth_time behind a console opt-in, and Microsoft Entra
// carries it as an optional claim the app registration must ask for. A
// sole-credential account on either got a 403 on the only arm its step-up
// screen offered, every time.
//
// The exposure that buys is stated plainly in operating/security.md: the arm
// proves "this browser still holds a live provider session for the linked
// account", not "the provider re-authenticated this person just now". For an
// account whose only credential IS that provider, somebody holding both
// cookies could sign in from scratch anyway.

func TestOAuthReauth_GrantsWithoutAnAuthTime(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	server, st, ks := setupOAuthTestServer(t)
	// A provider that ignored max_age: the claim is simply absent.
	providerID := createTestOIDCProviderWithAuthTime(t, st, ks, "user@example.com", "sub-1",
		func() any { return nil })
	cookie, _, sessionID := reauthSession(t, st, providerID, "sub-1")

	state, nonce, _ := beginReauth(t, server, providerID, cookie, "")
	require.NotNil(t, nonce)

	resp := completeCallback(t, server, providerID, state, nonce)
	assert.Equal(t, http.StatusFound, resp.StatusCode)

	row, err := st.Sessions().GetByID(ctx, sessionID, time.Now().UTC())
	require.NoError(t, err)
	assert.NotNil(t, row.ElevationExpiresAt,
		"the round trip is the proof for an account whose only credential is the provider")
}

// A STALE auth_time grants too, and refusing it would be the same defect one
// level down: it would punish the provider that tells the hub MORE and admit
// the one that tells it nothing. Google, when it does send auth_time, reports
// the ORIGINAL sign-in, which any freshness window rejects.
func TestOAuthReauth_GrantsOnAStaleAuthTime(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	server, st, ks := setupOAuthTestServer(t)
	providerID := createTestOIDCProviderWithAuthTime(t, st, ks, "user@example.com", "sub-1",
		func() any { return time.Now().Add(-3 * time.Hour).Unix() })
	cookie, _, sessionID := reauthSession(t, st, providerID, "sub-1")

	state, nonce, _ := beginReauth(t, server, providerID, cookie, "")
	require.NotNil(t, nonce)

	resp := completeCallback(t, server, providerID, state, nonce)
	assert.Equal(t, http.StatusFound, resp.StatusCode)

	row, err := st.Sessions().GetByID(ctx, sessionID, time.Now().UTC())
	require.NoError(t, err)
	assert.NotNil(t, row.ElevationExpiresAt)
}

// The request must still CARRY max_age, and that is now the ONLY thing with
// teeth in this flow.
//
// prompt=login is a SHOULD, so a provider that ignores it answers exactly
// like one that honoured it. max_age states a number the same section
// obliges a conforming provider to enforce at ITS end. The hub reads nothing
// back -- so if this parameter goes, the re-authentication becomes a request
// nobody is bound by, and no other test would notice.
func TestOAuthReauth_AsksForAMaximumAuthenticationAge(t *testing.T) {
	t.Parallel()

	server, st, ks := setupOAuthTestServer(t)
	providerID := createTestOIDCProviderWithStubClaims(t, st, ks, "user@example.com", "sub-1")
	cookie, _, _ := reauthSession(t, st, providerID, "sub-1")

	_, _, authURL := beginReauth(t, server, providerID, cookie, "")

	assert.Equal(t, "login", authURL.Query().Get("prompt"))
	// A POSITIVE value. max_age=0 is "equivalent to prompt=login" by the
	// spec, so it adds nothing beside the prompt, and two deployed providers
	// mishandle the zero.
	assert.Equal(t, "300", authURL.Query().Get("max_age"))
	assert.EqualValues(t, 300, huboauth.ReauthMaxAge.Seconds(),
		"the wire value comes from the exported constant, not a literal")
	// No `claims` parameter: it asked for auth_time as an Essential Claim,
	// and nothing reads auth_time any more. Sending it would be dead weight
	// that some providers reject.
	assert.Empty(t, authURL.Query().Get("claims"))
}
