package service_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/util/verifycode"
)

func startQuery(extra url.Values) url.Values {
	q := url.Values{"client_id": {oauthapp.ControlCLIClientID},
		"response_type":         {"code"},
		"code_challenge_method": {"S256"},
		"redirect_uri":          {"http://127.0.0.1:54321/callback"},
		"state":                 {"state-1"},
		"code_challenge":        {"challenge-1"},
		"installation_name":     {"laptop"},
	}
	for k, v := range extra {
		q[k] = v
	}
	return q
}

func getWithCookie(t *testing.T, target string, cookie *http.Cookie) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, target, nil)
	require.NoError(t, err)
	req.AddCookie(cookie)
	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// postConsentForm POSTs the consent form and STOPS at the redirect: the
// Location points at the CLI's loopback listener, which no test runs.
func postConsentForm(t *testing.T, targetURL string, form url.Values, cookie *http.Cookie) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, targetURL, strings.NewReader(form.Encode()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func bodyOf(t *testing.T, resp *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(body)
}

// TestCLIConsent_UnelevatedGetBouncesThroughElevate pins the GET leg's
// answer: the gate sends a replayable URL away, and it comes back to exactly
// this address, so nothing the CLI supplied is lost.
func TestCLIConsent_UnelevatedGetBouncesThroughElevate(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	cookie := env.adminCookie(t) // deliberately NOT elevated

	resp := getWithCookie(t, env.server.URL+"/oauth/authorize?"+startQuery(nil).Encode(), cookie)
	require.Equal(t, http.StatusFound, resp.StatusCode)

	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "/elevate", loc.Path)

	back, err := url.Parse(loc.Query().Get("redirect"))
	require.NoError(t, err)
	assert.Equal(t, "/oauth/authorize", back.Path)
	assert.Equal(t, "challenge-1", back.Query().Get("code_challenge"),
		"the return address must carry every parameter the CLI supplied")
	assert.Equal(t, "1", back.Query().Get("elevated"),
		"the hub marks the return address so a second failure explains instead of looping")
}

// TestCLIConsent_ElevatedMarkerStopsTheLoopWithoutAdmitting pins the second,
// independent loop-prevention layer. The marker may only stop redirecting;
// a hand-written one must gain the caller nothing.
func TestCLIConsent_ElevatedMarkerStopsTheLoopWithoutAdmitting(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	cookie := env.adminCookie(t)

	resp := getWithCookie(t, env.server.URL+"/oauth/authorize?"+startQuery(url.Values{"elevated": {"1"}}).Encode(), cookie)
	// 403, like the POST leg's refusal for the same condition. The page is
	// HTML because a browser reads it, but the status is the other half of
	// the same answer: a refused consent that reported 200 told every
	// machine reader -- a proxy, a health check, a test -- that it
	// succeeded.
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	body := bodyOf(t, resp)
	assert.Contains(t, body, "Verify your identity")
	assert.NotContains(t, body, "/oauth/consent", "the marker must not render the consent form")
}

// TestCLIConsent_UnelevatedPostIsRefusedNotRedirected pins the POST leg's
// different answer, and WHY it differs: the PKCE challenge lives in the
// body, so a redirect would destroy the flow irrecoverably.
func TestCLIConsent_UnelevatedPostIsRefusedNotRedirected(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	cookie := env.adminCookie(t)

	resp, err := postForm(env.server.URL+"/oauth/consent", url.Values{"client_id": {oauthapp.ControlCLIClientID},
		"response_type":         {"code"},
		"code_challenge_method": {"S256"},
		"redirect_uri":          {"http://127.0.0.1:54321/callback"},
		"state":                 {"state-1"},
		"code_challenge":        {"challenge-1"},
	}, cookie)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Location"), "a POST must never be redirected away from its body")
	assert.Contains(t, bodyOf(t, resp), "Verify your identity")

	// Nothing was minted.
	page, err := env.store.APITokens().ListByUser(context.Background(), store.ListAPITokensByUserParams{
		UserID: userid.MustNew(env.userID), PageParams: store.PageParams{Limit: 50},
	})
	require.NoError(t, err)
	assert.Empty(t, page.Rows)
}

// TestCLIConsent_SlidesTheWindowLikeEveryOtherRestrictedAction is the
// regression for the one surface the sliding rule missed.
//
// "Each sensitive action slides that window forward" is the rule the design
// and operating/security.md both state, and the consent legs enforced the
// window without extending it: slideElevation was a *UserService method, so
// only UserService procedures could reach it. Authorizing a command-line
// credential -- the most consequential thing a session can do -- was the one
// restricted action that did not count as use, so the hub bounced a user who
// verified at 11:58 and consented at 11:59 through /elevate again at 12:01.
func TestCLIConsent_SlidesTheWindowLikeEveryOtherRestrictedAction(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	cookie := env.elevatedAdminCookie(t)
	ctx := context.Background()

	before, err := env.store.Sessions().GetByID(ctx, cookie.Value, time.Now().UTC())
	require.NoError(t, err)
	require.NotNil(t, before.ElevationExpiresAt, "precondition: the session is elevated")

	// The clock moves inside the window, so the slide has somewhere to go.
	// Without it the store's own monotone guard would refuse a deadline that
	// is not ahead of the stored one, and the case would pass for the wrong
	// reason.
	env.clock.advance(30 * time.Minute)

	resp := getWithCookie(t, env.server.URL+"/oauth/authorize?"+startQuery(nil).Encode(), cookie)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	// The control CLI is a BUILT-IN registration, so the consent page states the
	// verified heading: an unverified warning on the hub's own app trained every
	// user to click through the one signal the page exists to raise.
	assert.Contains(t, bodyOf(t, resp), "Authorize LeapMux control CLI?")
	assert.NotContains(t, bodyOf(t, resp), "unverified")

	after, err := env.store.Sessions().GetByID(ctx, cookie.Value, time.Now().UTC())
	require.NoError(t, err)
	require.NotNil(t, after.ElevationExpiresAt)
	assert.True(t, after.ElevationExpiresAt.After(*before.ElevationExpiresAt),
		"consenting is a sensitive action, so it extends the window it just used")

	// The ANCHOR never moves: the absolute cap is measured from the instant
	// the factor was proven, and a slide that reset it would make the sliding
	// window a permanent privilege.
	require.NotNil(t, before.ElevationProvenAt)
	require.NotNil(t, after.ElevationProvenAt)
	assert.WithinDuration(t, *before.ElevationProvenAt, *after.ElevationProvenAt, time.Second)
}

// TestCLIConsent_ExpiredElevationClosesTheGate is the deterministic expiry
// test: the handler's own clock seam moves past the window, so nothing
// sleeps and nothing is faked.
func TestCLIConsent_ExpiredElevationClosesTheGate(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	cookie := env.elevatedAdminCookie(t)

	// Inside the window: the consent page renders.
	resp := getWithCookie(t, env.server.URL+"/oauth/authorize?"+startQuery(nil).Encode(), cookie)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	// The control CLI is a BUILT-IN registration, so the consent page states the
	// verified heading: an unverified warning on the hub's own app trained every
	// user to click through the one signal the page exists to raise.
	assert.Contains(t, bodyOf(t, resp), "Authorize LeapMux control CLI?")
	assert.NotContains(t, bodyOf(t, resp), "unverified")

	// Past it: the same request bounces.
	env.clock.advance(auth.ElevationWindow + time.Minute)
	resp = getWithCookie(t, env.server.URL+"/oauth/authorize?"+startQuery(nil).Encode(), cookie)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Location"), "/elevate")
}

// TestConsent_TheGrantBindsAtConsentNotAtExchange is the load-bearing negative
// case for the whole scope model. The grant row carries what the browser
// approved; if the exchange read its own form instead, any holder of an
// authorization code could upgrade the grant to hub administration.
func TestConsent_TheGrantBindsAtConsentNotAtExchange(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	cookie := env.elevatedAdminCookie(t)
	verifier, challenge := pkceVerifierAndChallenge()

	// Consent WITHOUT the admin scope.
	resp := postConsentForm(t, env.server.URL+"/oauth/consent", url.Values{"client_id": {oauthapp.ControlCLIClientID},
		"response_type":         {"code"},
		"code_challenge_method": {"S256"},
		"redirect_uri":          {"http://127.0.0.1:54321/callback"},
		"state":                 {"state-1"},
		"code_challenge":        {challenge},
		"decision":              {"allow"},
		"installation_name":     {"laptop"},
	}, cookie)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	code := loc.Query().Get("code")
	require.NotEmpty(t, code)

	// Exchange it while ASKING for the admin scope.
	tokenResp, err := http.PostForm(env.server.URL+"/oauth/token", url.Values{
		"grant_type":    {service.GrantTypeAuthorizationCode},
		"client_id":     {oauthapp.ControlCLIClientID},
		"redirect_uri":  {"http://127.0.0.1:54321/callback"},
		"code":          {code},
		"code_verifier": {verifier},
		"scope":         {"admin:read admin:users admin:settings admin:workers"},
	})
	require.NoError(t, err)
	defer func() { _ = tokenResp.Body.Close() }()
	require.Equal(t, http.StatusOK, tokenResp.StatusCode)
	assert.NotContains(t, bodyOf(t, tokenResp), "admin:",
		"the exchange must not be able to widen what the browser approved")

	page, err := env.store.APITokens().ListByUser(context.Background(), store.ListAPITokensByUserParams{
		UserID: userid.MustNew(env.userID), PageParams: store.PageParams{Limit: 50},
	})
	require.NoError(t, err)
	require.Len(t, page.Rows, 1)
	assert.NotContains(t, page.Rows[0].GrantedScopes, "admin:",
		"the stored grant is the CONSENTED one")
}

// TestConsent_WhatTheBrowserApprovedReachesTheToken is the positive twin: what
// the browser approved is what the row carries and what the token response
// reports.
func TestConsent_WhatTheBrowserApprovedReachesTheToken(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	cookie := env.elevatedAdminCookie(t)
	verifier, challenge := pkceVerifierAndChallenge()

	resp := postConsentForm(t, env.server.URL+"/oauth/consent", url.Values{"client_id": {oauthapp.ControlCLIClientID},
		"response_type":         {"code"},
		"code_challenge_method": {"S256"},
		"redirect_uri":          {"http://127.0.0.1:54321/callback"},
		"state":                 {"state-1"},
		"code_challenge":        {challenge},
		"decision":              {"allow"},
		"scope":                 {"admin:read admin:users admin:settings admin:workers"},
	}, cookie)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)

	tokenResp, err := http.PostForm(env.server.URL+"/oauth/token", url.Values{
		"grant_type":    {service.GrantTypeAuthorizationCode},
		"client_id":     {oauthapp.ControlCLIClientID},
		"redirect_uri":  {"http://127.0.0.1:54321/callback"},
		"code":          {loc.Query().Get("code")},
		"code_verifier": {verifier},
	})
	require.NoError(t, err)
	defer func() { _ = tokenResp.Body.Close() }()
	require.Equal(t, http.StatusOK, tokenResp.StatusCode)
	assert.Contains(t, bodyOf(t, tokenResp), "admin:settings",
		"the client must be told what it was granted")

	page, err := env.store.APITokens().ListByUser(context.Background(), store.ListAPITokensByUserParams{
		UserID: userid.MustNew(env.userID), PageParams: store.PageParams{Limit: 50},
	})
	require.NoError(t, err)
	require.Len(t, page.Rows, 1)
	assert.Contains(t, page.Rows[0].GrantedScopes, "admin:settings")
}

// TestCLIConsent_AdminScopeRefusedForANonAdministrator pins that the scope
// cannot exceed the account's authority, and that the refusal is loud: a
// silent downgrade would report a successful `--admin` login and then fail
// on the first admin verb with nothing to point at.
func TestCLIConsent_AdminScopeRefusedForANonAdministrator(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	ctx := context.Background()

	plainID := hubtestutil.CreateTestUser(t, env.store, "plainuser", "plainpass123")
	token, _, _, err := auth.Login(ctx, env.store, "plainuser", "plainpass123", auth.DefaultSessionDuration)
	require.NoError(t, err)
	cookie := &http.Cookie{Name: auth.CookieName, Value: token}
	now := env.clock.now()
	n, err := env.store.Sessions().Elevate(ctx, store.ElevateSessionParams{
		SessionID:          token,
		UserID:             userid.MustNew(plainID),
		ElevationProvenAt:  now,
		ElevationExpiresAt: now.Add(auth.ElevationWindow),
	}, now)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)

	query := startQuery(url.Values{"scope": {"admin:read"}})
	resp := getWithCookie(t, env.server.URL+"/oauth/authorize?"+query.Encode(), cookie)

	// The refusal REDIRECTS, with access_denied, and RFC 6749 section 4.1.2.1
	// is why: the client and its address are both registered by this point, so
	// the answer belongs to the client. A hub page instead would leave a
	// third-party app waiting on a callback that never arrives, and only the
	// app can tell its own user what to do next.
	//
	// The description travels with it, so the app can show the reason rather
	// than the code alone.
	require.Equal(t, http.StatusFound, resp.StatusCode)
	dest, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:54321", dest.Host, "the answer must reach the app that asked")
	assert.Equal(t, "access_denied", dest.Query().Get("error"))
	assert.Contains(t, dest.Query().Get("error_description"), "not a hub administrator")
	assert.Equal(t, query.Get("state"), dest.Query().Get("state"),
		"a client that cannot match the callback to its own request must discard it")
}

// The admin-scope rule runs at THREE legs, and only the first had a refusal
// test. The other two are the ones that BIND the scope onto a grant row, so a
// rule that stopped firing there would mint the credential the first leg
// refused to offer.
func TestCLIConsent_AdminScopeRefusedAtEveryLegThatBindsIt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	elevatedPlainUser := func(t *testing.T, env *apiAuthEnv) *http.Cookie {
		t.Helper()
		plainID := hubtestutil.CreateTestUser(t, env.store, "plainuser", "plainpass123")
		token, _, _, err := auth.Login(ctx, env.store, "plainuser", "plainpass123", auth.DefaultSessionDuration)
		require.NoError(t, err)
		now := env.clock.now()
		n, err := env.store.Sessions().Elevate(ctx, store.ElevateSessionParams{
			SessionID:          token,
			UserID:             userid.MustNew(plainID),
			ElevationProvenAt:  now,
			ElevationExpiresAt: now.Add(auth.ElevationWindow),
		}, now)
		require.NoError(t, err)
		require.EqualValues(t, 1, n)
		return &http.Cookie{Name: auth.CookieName, Value: token}
	}

	// The consent POST is the leg that writes the granted scope onto the
	// authorization code, so holding a code cannot widen what the user
	// approved -- but only if the rule fires here too.
	t.Run("the consent POST", func(t *testing.T) {
		t.Parallel()
		env := setupAPIAuth(t)
		cookie := elevatedPlainUser(t, env)
		_, challenge := pkceVerifierAndChallenge()

		resp := postConsentForm(t, env.server.URL+"/oauth/consent", url.Values{
			"client_id":             {oauthapp.ControlCLIClientID},
			"response_type":         {"code"},
			"code_challenge_method": {"S256"},
			"redirect_uri":          {"http://127.0.0.1:54321/callback"},
			"state":                 {"state-1"},
			"code_challenge":        {challenge},
			"scope":                 {"admin:read"},
			"decision":              {"allow"},
			"installation_name":     {"laptop"},
		}, cookie)

		// It answers as the GET leg does -- the refusal reaches the app, not a
		// hub page -- and what matters here is that NO CODE reaches it.
		require.Equal(t, http.StatusFound, resp.StatusCode)
		dest, err := url.Parse(resp.Header.Get("Location"))
		require.NoError(t, err)
		assert.Equal(t, "access_denied", dest.Query().Get("error"))
		assert.Empty(t, dest.Query().Get("code"),
			"a refused consent must mint no authorization code")
	})

	// The device-activation POST is the same rule on the flow that runs when
	// the browser is on a different machine from the one it authorizes.
	t.Run("the device activation POST", func(t *testing.T) {
		t.Parallel()
		env := setupAPIAuth(t)
		cookie := elevatedPlainUser(t, env)

		userCode := verifycode.Generate()
		now := env.clock.now()
		// The ASK carries the admin scope, and it is on the GRANT ROW: the
		// device flow's request and its consent happen on two machines with a
		// six-character code between them, so the row is the only carrier.
		require.NoError(t, env.store.DeviceAuthorizations().Create(ctx, store.CreateDeviceAuthorizationParams{
			ClientID:        oauthapp.ControlCLIClientID,
			RequestedScopes: "admin:read admin:users admin:settings admin:workers",
			DeviceCode:      id.Generate(), UserCode: userCode, DeviceName: "headless-box",
			ExpiresAt: now.Add(service.DeviceCodeTTL), IntervalSeconds: 5,
		}))

		resp, err := postForm(env.server.URL+"/oauth/device", url.Values{
			"user_code": {userCode},
			"decision":  {"allow"},
		}, cookie)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
		assert.Contains(t, bodyOf(t, resp), "not a hub administrator")

		row, err := env.store.DeviceAuthorizations().GetByUserCode(ctx, userCode)
		require.NoError(t, err)
		assert.Empty(t, row.GrantedScopes, "a refused activation must bind no grant")
		assert.NotEqual(t, int64(1), row.Approved, "a refused activation must not approve the grant")
	})
}

// TestDeviceActivation_RequiresElevationAndBindsTheScope covers the flow
// used from SSH and containers, where the elevation happens on a DIFFERENT
// machine from the one it authorizes.
func TestDeviceActivation_RequiresElevationAndBindsTheScope(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	ctx := context.Background()

	seedGrant := func(ask string) string {
		userCode := verifycode.Generate()
		require.NoError(t, env.store.DeviceAuthorizations().Create(ctx, store.CreateDeviceAuthorizationParams{
			ClientID:        oauthapp.ControlCLIClientID,
			RequestedScopes: ask,
			DeviceCode:      id.Generate(), UserCode: userCode, DeviceName: "headless",
			IntervalSeconds: 5, ExpiresAt: env.clock.now().Add(10 * time.Minute),
		}))
		return userCode
	}
	const adminAsk = "admin:read admin:users admin:settings admin:workers"

	// Un-elevated: the POST leg refuses, and approves nothing.
	unelevated := env.adminCookie(t)
	code := seedGrant(adminAsk)
	resp, err := postForm(env.server.URL+"/oauth/device", url.Values{"user_code": {code}, "decision": {"allow"}}, unelevated)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	row, err := env.store.DeviceAuthorizations().GetByUserCode(ctx, code)
	require.NoError(t, err)
	assert.EqualValues(t, 0, row.Approved, "a refused activation must approve nothing")

	// Elevated, and the approval binds the ASK the row already carries. The
	// browser posts a DECISION and never a scope: the request and the consent
	// happen on two machines, so taking the scope from this form would let
	// whoever types the code widen what the app asked for.
	elevated := env.elevatedAdminCookie(t)
	code = seedGrant(adminAsk)
	resp, err = postForm(env.server.URL+"/oauth/device", url.Values{
		"user_code": {code}, "decision": {"allow"},
	}, elevated)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	row, err = env.store.DeviceAuthorizations().GetByUserCode(ctx, code)
	require.NoError(t, err)
	assert.EqualValues(t, 1, row.Approved)
	assert.Contains(t, row.GrantedScopes, "admin:settings", "the browser's consent is what the row records")
}

// TestDeviceActivation_GetBouncesThroughElevate covers the fourth consent
// leg, and it is the one with no descriptor walk behind it.
//
// The proto-side tripwire (userProcedureElevation) reaches the UserService
// procedures only; these three /oauth/* consent legs are mux routes, so nothing
// fails a suite when one of them ships without its gate. Driving each leg
// un-elevated is what stands in for that, and this is the leg the other tests
// left out: the GET of the activation page, which must BOUNCE (it is
// replayable from its own URL) rather than refuse.
func TestDeviceActivation_GetBouncesThroughElevate(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	cookie := env.adminCookie(t)

	resp := getWithCookie(t, env.server.URL+"/oauth/device?user_code=ABC-123", cookie)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusFound, resp.StatusCode,
		"a replayable GET leg is sent away and comes back, so the user can prove a factor")

	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "/elevate", loc.Path)

	back, err := url.Parse(loc.Query().Get("redirect"))
	require.NoError(t, err)
	assert.Equal(t, "/oauth/device", back.Path)
	assert.Equal(t, "ABC-123", back.Query().Get("user_code"),
		"the return address must carry the code the user was given")
	assert.Equal(t, "1", back.Query().Get("elevated"),
		"the marker is what turns a second failure into an explanation")
}

// The fact the consent decision needs, on the GET leg that carries a code.
//
// It is the APP, not the device label. The device-authorization endpoint is
// anonymous, so whoever runs the CLI chooses that label, and rendering it let a
// stolen credential name the owner's own laptop. A registered client_id is what
// replaced it: the row an administrator or the owner created.
//
// The page answers only for the code the CLI printed into its own complete URI.
// A code typed by hand arrives in the POST form and gets nothing back.
func TestDeviceActivation_IdentifiesTheAppForACLISuppliedCode(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	ctx := context.Background()
	cookie := env.elevatedAdminCookie(t)

	userCode := verifycode.Generate()
	require.NoError(t, env.store.DeviceAuthorizations().Create(ctx, store.CreateDeviceAuthorizationParams{
		ClientID:   oauthapp.ControlCLIClientID,
		DeviceCode: id.Generate(), UserCode: userCode, DeviceName: "ci-runner-7",
		IntervalSeconds: 5, ExpiresAt: env.clock.now().Add(10 * time.Minute),
	}))

	resp := getWithCookie(t, env.server.URL+"/oauth/device?user_code="+url.QueryEscape(verifycode.Format(userCode)), cookie)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := bodyOf(t, resp)
	assert.Contains(t, body, oauthapp.ControlCLIName, "the page must say which app the consent is for")
	assert.NotContains(t, body, "ci-runner-7", "the requester's own label identifies nothing here")

	// A code that matches no live grant renders nothing rather than an
	// error, so the page stays usable for a user about to type one in.
	missing := getWithCookie(t, env.server.URL+"/oauth/device?user_code=ZZZZ-ZZZZ", cookie)
	require.Equal(t, http.StatusOK, missing.StatusCode)
	assert.NotContains(t, bodyOf(t, missing), oauthapp.ControlCLIName)

	// An EXPIRED grant is a miss too: it cannot be approved, so it has no
	// device worth showing.
	env.clock.advance(11 * time.Minute)
	expired := getWithCookie(t, env.server.URL+"/oauth/device?user_code="+url.QueryEscape(verifycode.Format(userCode)), cookie)
	require.Equal(t, http.StatusOK, expired.StatusCode)
	assert.NotContains(t, bodyOf(t, expired), "ci-runner-7")
}

// TestIssuedToken_CarriesTheAbsoluteLifetime pins that a fresh mint writes a
// refresh window inside the credential's absolute ceiling, which is what the
// rotation later clamps against.
func TestIssuedToken_CarriesTheAbsoluteLifetime(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	cookie := env.elevatedAdminCookie(t)
	verifier, challenge := pkceVerifierAndChallenge()

	resp := postConsentForm(t, env.server.URL+"/oauth/consent", url.Values{"client_id": {oauthapp.ControlCLIClientID},
		"response_type":         {"code"},
		"code_challenge_method": {"S256"},
		"redirect_uri":          {"http://127.0.0.1:54321/callback"},
		"state":                 {"state-1"},
		"code_challenge":        {challenge},
		"decision":              {"allow"},
	}, cookie)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)

	tokenResp, err := http.PostForm(env.server.URL+"/oauth/token", url.Values{
		"grant_type":    {service.GrantTypeAuthorizationCode},
		"client_id":     {oauthapp.ControlCLIClientID},
		"redirect_uri":  {"http://127.0.0.1:54321/callback"},
		"code":          {loc.Query().Get("code")},
		"code_verifier": {verifier},
	})
	require.NoError(t, err)
	defer func() { _ = tokenResp.Body.Close() }()
	require.Equal(t, http.StatusOK, tokenResp.StatusCode)

	page, err := env.store.APITokens().ListByUser(context.Background(), store.ListAPITokensByUserParams{
		UserID: userid.MustNew(env.userID), PageParams: store.PageParams{Limit: 50},
	})
	require.NoError(t, err)
	require.Len(t, page.Rows, 1)
	row := page.Rows[0]
	require.NotNil(t, row.RefreshExpiresAt)
	assert.False(t, row.RefreshExpiresAt.After(row.CreatedAt.Add(auth.AbsoluteTokenLifetime).Add(time.Minute)),
		"a mint must not write a refresh window past the credential's ceiling")
	assert.True(t, row.RefreshExpiresAt.After(env.clock.now()))
}

// TestIssuedToken_EmailsTheOwner pins the one signal that does not require
// the user to open Preferences and look, and the two conditions under which
// it stays silent.
func TestIssuedToken_EmailsTheOwner(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		email     string
		verified  bool
		wantEmail bool
	}{
		"verified address is notified":      {email: "admin@example.com", verified: true, wantEmail: true},
		"unverified address is not":         {email: "admin@example.com", verified: false, wantEmail: false},
		"an account with no address is not": {email: "", verified: false, wantEmail: false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			env := setupAPIAuth(t)
			require.NoError(t, env.store.Users().UpdateEmail(ctx, store.UpdateUserEmailParams{
				ID: env.userID, Email: tc.email, EmailVerified: tc.verified,
			}))

			sender := &recordingSender{}
			mux := http.NewServeMux()
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)
			h := service.NewOAuthServerHandler(service.OAuthServerDeps{
				Store:     env.store,
				Validator: env.validator,
				Lifecycle: auth.NewCredentialLifecycleEffects(env.cache, noopBearerCloser{}, nil),
				HubURL:    func() string { return srv.URL },
				Mail:      sender,
				Renderer:  mail.Renderer{BaseURL: func() string { return srv.URL }},
			})
			h.RegisterRoutes(mux)

			code := id.Generate()
			require.NoError(t, env.store.OAuthAuthorizationCodes().Create(ctx, store.CreateOAuthAuthorizationCodeParams{
				ClientID:      oauthapp.ControlCLIClientID,
				GrantedScopes: authscope.NonAdminGrant().String(),
				Code:          code, UserID: userid.MustNew(env.userID), CodeChallenge: "unused",
				InstallationName: "alice@laptop", ExpiresAt: time.Now().Add(time.Minute),
				RedirectURI: "http://127.0.0.1:54321/callback",
			}))
			// Consume the grant directly through the device-code-free path:
			// the PKCE check is not what this test is about, so drive the
			// exchange with a verifier that matches the stored challenge.
			verifier, challenge := pkceVerifierAndChallenge()
			require.NoError(t, env.store.OAuthAuthorizationCodes().Create(ctx, store.CreateOAuthAuthorizationCodeParams{
				ClientID: oauthapp.ControlCLIClientID,
				// An ADMIN scope, because the assertion below is about the
				// subject line that separates an ordinary authorization from
				// one that administers the hub.
				GrantedScopes: "admin:read " + authscope.NonAdminGrant().String(),
				Code:          code + "-real", UserID: userid.MustNew(env.userID), CodeChallenge: challenge,
				InstallationName: "alice@laptop", ExpiresAt: time.Now().Add(time.Minute),
				RedirectURI: "http://127.0.0.1:54321/callback",
			}))
			resp, err := http.PostForm(srv.URL+"/oauth/token", url.Values{
				"grant_type":    {service.GrantTypeAuthorizationCode},
				"client_id":     {oauthapp.ControlCLIClientID},
				"redirect_uri":  {"http://127.0.0.1:54321/callback"},
				"code":          {code + "-real"},
				"code_verifier": {verifier},
			})
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			require.Equal(t, http.StatusOK, resp.StatusCode, "the notice must never be able to fail the mint")

			// The hub sends the notice DETACHED, on its own goroutine, so
			// that a slow relay cannot delay the token a CLI login is
			// blocked on. The assertion therefore waits for it rather than
			// reading once.
			if !tc.wantEmail {
				// Nothing to wait for: the guard returns before anything
				// starts the goroutine, so nothing queues a send. Never
				// rather than Eventually, because this asserts an absence.
				assert.Never(t, func() bool { return sender.last() != nil },
					200*time.Millisecond, 20*time.Millisecond,
					"an address the hub cannot trust must receive no account notice")
				return
			}
			require.Eventually(t, func() bool { return sender.last() != nil },
				2*time.Second, 10*time.Millisecond,
				"the detached notice must reach the mail layer")
			last := sender.last()
			require.NotNil(t, last)
			assert.Equal(t, tc.email, last.To)
			assert.Contains(t, last.Subject, "ADMINISTERS THE HUB",
				"the notice must distinguish an admin credential")
			assert.Contains(t, last.Body, "alice@laptop", "the notice specifies the device that asked")
			// The permissions are listed in full, so the recipient can tell
			// which authorization this was rather than that there was one.
			assert.Contains(t, last.Body, "admin:read")
			assert.Contains(t, last.Body, "workspace:write")
		})
	}
}

// mintTokenForRefreshTest walks the consent flow and returns the issued
// pair, so a refresh test starts from a credential the hub really minted.
func mintTokenForRefreshTest(t *testing.T, env *apiAuthEnv) (accessToken, refreshToken string) {
	t.Helper()
	cookie := env.elevatedAdminCookie(t)
	verifier, challenge := pkceVerifierAndChallenge()

	resp := postConsentForm(t, env.server.URL+"/oauth/consent", url.Values{"client_id": {oauthapp.ControlCLIClientID},
		"response_type":         {"code"},
		"code_challenge_method": {"S256"},
		"redirect_uri":          {"http://127.0.0.1:54321/callback"},
		"state":                 {"state-1"},
		"code_challenge":        {challenge},
		"decision":              {"allow"},
	}, cookie)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)

	tokenResp, err := http.PostForm(env.server.URL+"/oauth/token", url.Values{
		"grant_type":    {service.GrantTypeAuthorizationCode},
		"client_id":     {oauthapp.ControlCLIClientID},
		"redirect_uri":  {"http://127.0.0.1:54321/callback"},
		"code":          {loc.Query().Get("code")},
		"code_verifier": {verifier},
	})
	require.NoError(t, err)
	defer func() { _ = tokenResp.Body.Close() }()
	require.Equal(t, http.StatusOK, tokenResp.StatusCode)

	var body struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	require.NoError(t, json.NewDecoder(tokenResp.Body).Decode(&body))
	require.NotEmpty(t, body.RefreshToken)
	return body.AccessToken, body.RefreshToken
}

// TestRefresh_ClipsToTheAbsoluteLifetime is what stops one browser consent
// living for ever. Every rotation moves the 90-day window forward, so
// without the clip a CLI that refreshes weekly would never sign in again.
func TestRefresh_ClipsToTheAbsoluteLifetime(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	env := setupAPIAuth(t)
	_, refreshToken := mintTokenForRefreshTest(t, env)

	// Late in the credential's life, but not past it: the window clips to
	// what remains of the ceiling instead of adding another 90 days.
	//
	// INSIDE the last AccessTokenTTL, deliberately. That is the only
	// position where the access clip can be observed at all: further out,
	// now+1h still lands under the ceiling and an unclipped access token
	// looks correct. The refresh clip applies here just as it did a day out.
	env.clock.advance(auth.AbsoluteTokenLifetime - 30*time.Minute)
	resp, err := http.PostForm(env.server.URL+"/oauth/token", url.Values{"grant_type": {service.GrantTypeRefreshToken}, "client_id": {oauthapp.ControlCLIClientID}, "refresh_token": {refreshToken}})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var rotated struct {
		RefreshToken string `json:"refresh_token"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&rotated))

	page, err := env.store.APITokens().ListByUser(ctx, store.ListAPITokensByUserParams{
		UserID: userid.MustNew(env.userID), PageParams: store.PageParams{Limit: 50},
	})
	require.NoError(t, err)
	require.Len(t, page.Rows, 1)
	row := page.Rows[0]
	require.NotNil(t, row.RefreshExpiresAt)
	ceiling := row.CreatedAt.Add(auth.AbsoluteTokenLifetime)
	assert.False(t, row.RefreshExpiresAt.After(ceiling.Add(time.Minute)),
		"the rotation must not push the window past the credential's ceiling (got %s, ceiling %s)",
		row.RefreshExpiresAt, ceiling)

	// The ACCESS expiry is clipped to the same ceiling, and it is the one
	// that decides whether the credential still authenticates: validateRow
	// reads expires_at alone. Minted with a full AccessTokenTTL, the last
	// rotation before the ceiling wrote an hour past it, so the bearer kept
	// working after the hub already answered the next refresh with
	// "this credential reached its maximum lifetime".
	require.NotNil(t, row.ExpiresAt)
	assert.False(t, row.ExpiresAt.After(ceiling.Add(time.Minute)),
		"the access token must not outlive the credential's ceiling (got %s, ceiling %s)",
		row.ExpiresAt, ceiling)

	// Past the ceiling: the credential is finished, and the answer says so
	// permanently rather than looking like a transient failure.
	//
	// The description does NOT name a CLI command. One endpoint now serves
	// every registered app, and `leapmux control auth login` is meaningless to
	// a third-party client -- so the hub states the standard code and the fact,
	// and each client renders its own remedy. The CLI's is ErrNotLoggedIn,
	// which names the command; refresh.go turns invalid_grant into exactly
	// that by deleting the stored credential.
	env.clock.advance(48 * time.Hour)
	dead, err := http.PostForm(env.server.URL+"/oauth/token", url.Values{"grant_type": {service.GrantTypeRefreshToken}, "client_id": {oauthapp.ControlCLIClientID}, "refresh_token": {rotated.RefreshToken}})
	require.NoError(t, err)
	defer func() { _ = dead.Body.Close() }()
	// 400, not 401: RFC 6749 section 5.2 reserves 401 for invalid_client, and
	// a library that read 401 here would re-authenticate the client for a
	// grant that can never work again.
	assert.Equal(t, http.StatusBadRequest, dead.StatusCode)
	body := bodyOf(t, dead)
	assert.Contains(t, body, "invalid_grant")
	assert.Contains(t, body, "reached its maximum lifetime")
	assert.NotContains(t, body, "leapmux", "the hub serves every app, so it names no client's command")

	// And the ROW is revoked, not only the cache. This leg is the one that
	// decides a credential is dead by itself, so it has to record it: the
	// access token that was minted alongside the refresh token is still
	// inside its own hour, so a row left with revoked_at NULL keeps
	// authenticating after the hub told the CLI the credential was
	// finished -- and keeps listing as live under Preferences.
	after, err := env.store.APITokens().ListByUser(ctx, store.ListAPITokensByUserParams{
		UserID: userid.MustNew(env.userID), PageParams: store.PageParams{Limit: 50},
	})
	require.NoError(t, err)
	assert.Empty(t, after.Rows,
		"the listing returns live rows only, so a revoked credential must not appear")

	revoked, err := env.store.APITokens().GetByID(ctx, row.ID)
	require.NoError(t, err)
	assert.NotNil(t, revoked.RevokedAt,
		"a credential the hub refuses for ever must carry revoked_at, or its access token keeps working")
}

// TestRefresh_ReportsTheRefreshDeadlineAndTheScope pins the rotation
// response's shape, HUB-side.
//
// The CLI stores refresh_expires_in so `auth status` can say when the device
// must sign in again, and it advances that value only when the field is
// present. The rotation omitted it, so the stored deadline froze at the one
// login wrote and `auth status` printed a date further into the past on
// every rotation, for a credential the hub still honoured. The CLI also
// adopts the reported `scope` as the credential's reachable grant, so the
// rotation must name one -- and an unnamed default excludes the admin
// family rather than defaulting to the app's whole ceiling.
//
// This test pins it here rather than in the CLI, and that is the point: the
// CLI-side test drives a stub that hard-codes the field, so it asserts what
// the fixture returns and would pass against a hub that sends nothing.
func TestRefresh_ReportsTheRefreshDeadlineAndTheScope(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	_, refreshToken := mintTokenForRefreshTest(t, env)

	resp, err := http.PostForm(env.server.URL+"/oauth/token", url.Values{"grant_type": {service.GrantTypeRefreshToken}, "client_id": {oauthapp.ControlCLIClientID}, "refresh_token": {refreshToken}})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body struct {
		ExpiresIn        int    `json:"expires_in"`
		RefreshExpiresIn int    `json:"refresh_expires_in"`
		Scope            string `json:"scope"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	assert.Positive(t, body.ExpiresIn, "the access window must be reported")
	// The whole window, because this credential is young: the clip only
	// applies near the absolute ceiling, which the sibling test covers.
	assert.InDelta(t, (auth.RefreshTokenTTL).Seconds(), float64(body.RefreshExpiresIn), 60,
		"the rotation must report the deadline that sends the device back to a browser")
	assert.NotEmpty(t, body.Scope, "the rotation must report the grant the credential reaches")
	// This consent asked for no scope, and an omitted one excludes the admin
	// family rather than defaulting to the app's whole ceiling.
	for _, token := range strings.Fields(body.Scope) {
		assert.False(t, strings.HasPrefix(token, "admin:"), "an unnamed scope must not reach the admin family: %s", body.Scope)
	}
}

// TestElevationRequiredHeaderValueIsPinned keeps the Go constant and the
// frontend's copy from drifting silently. The frontend cannot import this
// package, so only the literal below and the comment on each side join the
// two; a rename that changes one and not the other turns every step-up
// prompt into an unexplained failure the user cannot act on.
//
// This test writes the literal out rather than compares it to the constant,
// so renaming the constant alone does not make this pass.
func TestElevationRequiredHeaderValueIsPinned(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "Leapmux-Elevation-Required", service.ElevationRequiredHeader,
		"frontend/src/lib/elevation.ts keys on the lowercased form of this value")
}

// TestElevationFactorRefusalIsMarkedAsACredentialRejection is the guard on
// the failure the E2E suite found: a mistyped step-up password answered
// Unauthenticated, the browser's unconditional "Unauthenticated means signed
// out" rule fired, and the prompt ended the very session it protects.
//
// The code stays Unauthenticated -- a rejected credential is what it means
// -- and the marker says WHICH credential, so a client can tell a dead
// session from a wrong answer.
func TestElevationFactorRefusalIsMarkedAsACredentialRejection(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "Leapmux-Credential-Rejected", service.CredentialRejectedHeader,
		"frontend/src/api/transport.ts keys on the lowercased form of this value")
}
