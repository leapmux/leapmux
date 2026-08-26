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

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/util/verifycode"
)

func startQuery(extra url.Values) url.Values {
	q := url.Values{
		"redirect_uri":   {"http://127.0.0.1:54321/callback"},
		"state":          {"state-1"},
		"code_challenge": {"challenge-1"},
		"device_name":    {"laptop"},
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
// answer: a replayable URL is sent away and comes back to exactly this
// address, so nothing the CLI supplied is lost.
func TestCLIConsent_UnelevatedGetBouncesThroughElevate(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	cookie := env.adminCookie(t) // deliberately NOT elevated

	resp := getWithCookie(t, env.server.URL+"/auth/cli/start?"+startQuery(nil).Encode(), cookie)
	require.Equal(t, http.StatusFound, resp.StatusCode)

	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "/elevate", loc.Path)

	back, err := url.Parse(loc.Query().Get("redirect"))
	require.NoError(t, err)
	assert.Equal(t, "/auth/cli/start", back.Path)
	assert.Equal(t, "challenge-1", back.Query().Get("code_challenge"),
		"the return address must carry every parameter the CLI supplied")
	assert.Equal(t, "1", back.Query().Get("elevated"),
		"the hub marks the return address so a second failure explains instead of looping")
}

// TestCLIConsent_ElevatedMarkerStopsTheLoopWithoutAdmitting pins the second,
// independent loop-prevention layer. The marker may only stop redirecting;
// a hand-written one must buy the caller nothing.
func TestCLIConsent_ElevatedMarkerStopsTheLoopWithoutAdmitting(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	cookie := env.adminCookie(t)

	resp := getWithCookie(t, env.server.URL+"/auth/cli/start?"+startQuery(url.Values{"elevated": {"1"}}).Encode(), cookie)
	// 403, like the POST leg's refusal for the same condition. The page is
	// HTML because a browser reads it, but the status is the other half of
	// the same answer: a refused consent that reported 200 told every
	// machine reader -- a proxy, a health check, a test -- that it
	// succeeded.
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	body := bodyOf(t, resp)
	assert.Contains(t, body, "Verify your identity")
	assert.NotContains(t, body, "/auth/cli/authorize", "the marker must not render the consent form")
}

// TestCLIConsent_UnelevatedPostIsRefusedNotRedirected pins the POST leg's
// different answer, and WHY it differs: the PKCE challenge lives in the
// body, so a redirect would destroy the flow irrecoverably.
func TestCLIConsent_UnelevatedPostIsRefusedNotRedirected(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	cookie := env.adminCookie(t)

	resp, err := postForm(env.server.URL+"/auth/cli/authorize", url.Values{
		"redirect_uri":   {"http://127.0.0.1:54321/callback"},
		"state":          {"state-1"},
		"code_challenge": {"challenge-1"},
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

// TestCLIConsent_SlidesTheWindowLikeEveryOtherGatedAction is the regression
// for the one surface the sliding rule missed.
//
// "Each sensitive action slides that window forward" is the rule the design
// and operating/security.md both state, and the consent legs enforced the
// window without extending it: slideElevation was a *UserService method, so
// only UserService procedures could reach it. Authorizing a command-line
// credential -- the most consequential thing a session can do -- was the one
// gated action that did not count as use, so a user who verified at 11:58 and
// consented at 11:59 was bounced through /elevate again at 12:01.
func TestCLIConsent_SlidesTheWindowLikeEveryOtherGatedAction(t *testing.T) {
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

	resp := getWithCookie(t, env.server.URL+"/auth/cli/start?"+startQuery(nil).Encode(), cookie)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, bodyOf(t, resp), "Authorize CLI access?")

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
	resp := getWithCookie(t, env.server.URL+"/auth/cli/start?"+startQuery(nil).Encode(), cookie)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, bodyOf(t, resp), "Authorize CLI access?")

	// Past it: the same request bounces.
	env.clock.advance(auth.ElevationWindow + time.Minute)
	resp = getWithCookie(t, env.server.URL+"/auth/cli/start?"+startQuery(nil).Encode(), cookie)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Location"), "/elevate")
}

// TestCLIConsent_AdminScopeBindsAtConsentNotAtExchange is the load-bearing
// negative case for the scope. The grant row carries what the browser
// approved; if the exchange read its own form instead, any holder of an
// authorization code could upgrade the grant to hub administration.
func TestCLIConsent_AdminScopeBindsAtConsentNotAtExchange(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	cookie := env.elevatedAdminCookie(t)
	verifier, challenge := pkceVerifierAndChallenge()

	// Consent WITHOUT the admin scope.
	resp := postConsentForm(t, env.server.URL+"/auth/cli/authorize", url.Values{
		"redirect_uri":   {"http://127.0.0.1:54321/callback"},
		"state":          {"state-1"},
		"code_challenge": {challenge},
		"device_name":    {"laptop"},
	}, cookie)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	code := loc.Query().Get("code")
	require.NotEmpty(t, code)

	// Exchange it while ASKING for the admin scope.
	tokenResp, err := http.PostForm(env.server.URL+"/auth/cli/token", url.Values{
		"grant_type":    {service.GrantTypeAuthorizationCode},
		"code":          {code},
		"code_verifier": {verifier},
		"admin":         {"1"},
	})
	require.NoError(t, err)
	defer func() { _ = tokenResp.Body.Close() }()
	require.Equal(t, http.StatusOK, tokenResp.StatusCode)
	assert.NotContains(t, bodyOf(t, tokenResp), "\"admin_scope\":true",
		"the exchange must not be able to widen what the browser approved")

	page, err := env.store.APITokens().ListByUser(context.Background(), store.ListAPITokensByUserParams{
		UserID: userid.MustNew(env.userID), PageParams: store.PageParams{Limit: 50},
	})
	require.NoError(t, err)
	require.Len(t, page.Rows, 1)
	assert.False(t, page.Rows[0].AdminScope, "the stored scope is the CONSENTED one")
}

// TestCLIConsent_AdminScopeGrantedAtConsentReachesTheToken is the positive
// twin: what the browser approved is what the row carries.
func TestCLIConsent_AdminScopeGrantedAtConsentReachesTheToken(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	cookie := env.elevatedAdminCookie(t)
	verifier, challenge := pkceVerifierAndChallenge()

	resp := postConsentForm(t, env.server.URL+"/auth/cli/authorize", url.Values{
		"redirect_uri":   {"http://127.0.0.1:54321/callback"},
		"state":          {"state-1"},
		"code_challenge": {challenge},
		"admin":          {"1"},
	}, cookie)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)

	tokenResp, err := http.PostForm(env.server.URL+"/auth/cli/token", url.Values{
		"grant_type":    {service.GrantTypeAuthorizationCode},
		"code":          {loc.Query().Get("code")},
		"code_verifier": {verifier},
	})
	require.NoError(t, err)
	defer func() { _ = tokenResp.Body.Close() }()
	require.Equal(t, http.StatusOK, tokenResp.StatusCode)
	assert.Contains(t, bodyOf(t, tokenResp), "\"admin_scope\":true",
		"the CLI must be told what it was granted")

	page, err := env.store.APITokens().ListByUser(context.Background(), store.ListAPITokensByUserParams{
		UserID: userid.MustNew(env.userID), PageParams: store.PageParams{Limit: 50},
	})
	require.NoError(t, err)
	require.Len(t, page.Rows, 1)
	assert.True(t, page.Rows[0].AdminScope)
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

	resp := getWithCookie(t, env.server.URL+"/auth/cli/start?"+startQuery(url.Values{"admin": {"1"}}).Encode(), cookie)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Contains(t, bodyOf(t, resp), "not a hub administrator")
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

	// The consent POST is the leg that writes admin_scope onto the
	// authorization code, so holding a code cannot widen what the user
	// approved -- but only if the rule fires here too.
	t.Run("the consent POST", func(t *testing.T) {
		t.Parallel()
		env := setupAPIAuth(t)
		cookie := elevatedPlainUser(t, env)
		_, challenge := pkceVerifierAndChallenge()

		resp, err := postForm(env.server.URL+"/auth/cli/authorize", url.Values{
			"redirect_uri":   {"http://127.0.0.1:54321/callback"},
			"state":          {"state-1"},
			"code_challenge": {challenge},
			"device_name":    {"laptop"},
			"admin":          {"1"},
		}, cookie)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
		assert.Contains(t, bodyOf(t, resp), "not a hub administrator")
		assert.Empty(t, resp.Header.Get("Location"), "a refused consent must not redirect to the CLI callback")
	})

	// The device-activation POST is the same rule on the flow that runs when
	// the browser is on a different machine from the one being authorized.
	t.Run("the device activation POST", func(t *testing.T) {
		t.Parallel()
		env := setupAPIAuth(t)
		cookie := elevatedPlainUser(t, env)

		userCode := verifycode.Generate()
		now := env.clock.now()
		require.NoError(t, env.store.DeviceAuthorizations().Create(ctx, store.CreateDeviceAuthorizationParams{
			DeviceCode: id.Generate(), UserCode: userCode, DeviceName: "headless-box",
			ExpiresAt: now.Add(service.DeviceCodeTTL), IntervalSeconds: 5,
		}))

		resp, err := postForm(env.server.URL+"/auth/cli/activate", url.Values{
			"user_code": {userCode},
			"admin":     {"1"},
		}, cookie)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
		assert.Contains(t, bodyOf(t, resp), "not a hub administrator")

		row, err := env.store.DeviceAuthorizations().GetByUserCode(ctx, userCode)
		require.NoError(t, err)
		assert.False(t, row.AdminScope, "a refused activation must bind no admin scope")
		assert.NotEqual(t, int64(1), row.Approved, "a refused activation must not approve the grant")
	})
}

// TestDeviceActivation_RequiresElevationAndBindsTheScope covers the flow
// used from SSH and containers, where the elevation happens on a DIFFERENT
// machine from the one being authorized.
func TestDeviceActivation_RequiresElevationAndBindsTheScope(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	ctx := context.Background()

	seedGrant := func() string {
		userCode := verifycode.Generate()
		require.NoError(t, env.store.DeviceAuthorizations().Create(ctx, store.CreateDeviceAuthorizationParams{
			DeviceCode: id.Generate(), UserCode: userCode, DeviceName: "headless",
			IntervalSeconds: 5, ExpiresAt: env.clock.now().Add(10 * time.Minute),
		}))
		return userCode
	}

	// Un-elevated: the POST leg refuses, and approves nothing.
	unelevated := env.adminCookie(t)
	code := seedGrant()
	resp, err := postForm(env.server.URL+"/auth/cli/activate", url.Values{"user_code": {code}}, unelevated)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	row, err := env.store.DeviceAuthorizations().GetByUserCode(ctx, code)
	require.NoError(t, err)
	assert.EqualValues(t, 0, row.Approved, "a refused activation must approve nothing")

	// Elevated, with the admin checkbox ticked: the scope binds on the row.
	elevated := env.elevatedAdminCookie(t)
	code = seedGrant()
	resp, err = postForm(env.server.URL+"/auth/cli/activate", url.Values{
		"user_code": {code}, "admin": {"1"},
	}, elevated)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	row, err = env.store.DeviceAuthorizations().GetByUserCode(ctx, code)
	require.NoError(t, err)
	assert.EqualValues(t, 1, row.Approved)
	assert.True(t, row.AdminScope, "the browser's consent is what the row records")
}

// TestDeviceActivation_GetBouncesThroughElevate covers the fourth consent
// leg, and it is the one with no descriptor walk behind it.
//
// The proto-side tripwire (userProcedureElevation) reaches the UserService
// procedures only; these four /auth/cli/* legs are mux routes, so nothing
// fails a suite when one of them ships without its gate. Driving each leg
// un-elevated is what stands in for that, and this is the leg the other
// three tests left out: the GET of the activation page, which must BOUNCE
// (it is replayable from its own URL) rather than refuse.
func TestDeviceActivation_GetBouncesThroughElevate(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	cookie := env.adminCookie(t)

	resp := getWithCookie(t, env.server.URL+"/auth/cli/activate?user_code=ABC-123", cookie)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusFound, resp.StatusCode,
		"a replayable GET leg is sent away and comes back, so the user can prove a factor")

	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "/elevate", loc.Path)

	back, err := url.Parse(loc.Query().Get("redirect"))
	require.NoError(t, err)
	assert.Equal(t, "/auth/cli/activate", back.Path)
	assert.Equal(t, "ABC-123", back.Query().Get("user_code"),
		"the return address must carry the code the user was given")
	assert.Equal(t, "1", back.Query().Get("elevated"),
		"the marker is what turns a second failure into an explanation")
}

// TestDeviceActivation_NamesTheDeviceForACLISuppliedCode covers the one
// fact the consent decision needs and the page did not carry.
//
// The device-authorization endpoint is anonymous, so whoever runs the CLI
// chooses this text. The page therefore renders it only for the code the
// CLI printed into its own complete URI -- a code typed by hand arrives in
// the POST form and gets nothing back.
func TestDeviceActivation_NamesTheDeviceForACLISuppliedCode(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	ctx := context.Background()
	cookie := env.elevatedAdminCookie(t)

	userCode := verifycode.Generate()
	require.NoError(t, env.store.DeviceAuthorizations().Create(ctx, store.CreateDeviceAuthorizationParams{
		DeviceCode: id.Generate(), UserCode: userCode, DeviceName: "ci-runner-7",
		IntervalSeconds: 5, ExpiresAt: env.clock.now().Add(10 * time.Minute),
	}))

	resp := getWithCookie(t, env.server.URL+"/auth/cli/activate?user_code="+url.QueryEscape(verifycode.Format(userCode)), cookie)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, bodyOf(t, resp), "ci-runner-7",
		"the page must say which device the consent is for")

	// A code that matches no live grant renders nothing rather than an
	// error, so the page stays usable for a user about to type one in.
	missing := getWithCookie(t, env.server.URL+"/auth/cli/activate?user_code=ZZZZ-ZZZZ", cookie)
	require.Equal(t, http.StatusOK, missing.StatusCode)
	assert.NotContains(t, bodyOf(t, missing), "Requested by")

	// An EXPIRED grant is a miss too: it cannot be approved, so it has no
	// device worth naming.
	env.clock.advance(11 * time.Minute)
	expired := getWithCookie(t, env.server.URL+"/auth/cli/activate?user_code="+url.QueryEscape(verifycode.Format(userCode)), cookie)
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

	resp := postConsentForm(t, env.server.URL+"/auth/cli/authorize", url.Values{
		"redirect_uri":   {"http://127.0.0.1:54321/callback"},
		"state":          {"state-1"},
		"code_challenge": {challenge},
	}, cookie)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)

	tokenResp, err := http.PostForm(env.server.URL+"/auth/cli/token", url.Values{
		"grant_type":    {service.GrantTypeAuthorizationCode},
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
			h := service.NewAPIAuthHandler(service.APIAuthHandlerDeps{
				Store:     env.store,
				Validator: env.validator,
				Lifecycle: auth.NewCredentialLifecycleEffects(env.cache, noopBearerCloser{}, nil),
				HubURL:    func() string { return srv.URL },
				Mail:      sender,
				Renderer:  mail.Renderer{BaseURL: func() string { return srv.URL }},
			})
			h.RegisterRoutes(mux)

			code := id.Generate()
			require.NoError(t, env.store.CLIAuthorizationCodes().Create(ctx, store.CreateCLIAuthorizationCodeParams{
				Code: code, UserID: userid.MustNew(env.userID), CodeChallenge: "unused",
				DeviceName: "alice@laptop", AdminScope: true, ExpiresAt: time.Now().Add(time.Minute),
			}))
			// Consume the grant directly through the device-code-free path:
			// the PKCE check is not what this test is about, so drive the
			// exchange with a verifier that matches the stored challenge.
			verifier, challenge := pkceVerifierAndChallenge()
			require.NoError(t, env.store.CLIAuthorizationCodes().Create(ctx, store.CreateCLIAuthorizationCodeParams{
				Code: code + "-real", UserID: userid.MustNew(env.userID), CodeChallenge: challenge,
				DeviceName: "alice@laptop", AdminScope: true, ExpiresAt: time.Now().Add(time.Minute),
			}))
			resp, err := http.PostForm(srv.URL+"/auth/cli/token", url.Values{
				"grant_type":    {service.GrantTypeAuthorizationCode},
				"code":          {code + "-real"},
				"code_verifier": {verifier},
			})
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			require.Equal(t, http.StatusOK, resp.StatusCode, "the notice must never be able to fail the mint")

			// The notice is sent DETACHED, on its own goroutine, so that a
			// slow relay cannot sit in front of the token a CLI login is
			// blocked on. The assertion therefore waits for it rather than
			// reading once.
			if !tc.wantEmail {
				// Nothing to wait for: the guard returns before the
				// goroutine is ever started, so no send is queued. Never
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
			assert.Contains(t, last.Subject, "ADMIN", "the notice must distinguish an admin credential")
			assert.Contains(t, last.Body, "alice@laptop", "the notice specifies the device that asked")
		})
	}
}

// mintTokenForRefreshTest walks the consent flow and returns the issued
// pair, so a refresh test starts from a credential the hub really minted.
func mintTokenForRefreshTest(t *testing.T, env *apiAuthEnv) (accessToken, refreshToken string) {
	t.Helper()
	cookie := env.elevatedAdminCookie(t)
	verifier, challenge := pkceVerifierAndChallenge()

	resp := postConsentForm(t, env.server.URL+"/auth/cli/authorize", url.Values{
		"redirect_uri":   {"http://127.0.0.1:54321/callback"},
		"state":          {"state-1"},
		"code_challenge": {challenge},
	}, cookie)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)

	tokenResp, err := http.PostForm(env.server.URL+"/auth/cli/token", url.Values{
		"grant_type":    {service.GrantTypeAuthorizationCode},
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
	// looks correct. The refresh clip bites here just as it did a day out.
	env.clock.advance(auth.AbsoluteTokenLifetime - 30*time.Minute)
	resp, err := http.PostForm(env.server.URL+"/auth/cli/refresh", url.Values{"refresh_token": {refreshToken}})
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
	// working after the hub had already answered the next refresh with
	// "this credential reached its maximum lifetime".
	require.NotNil(t, row.ExpiresAt)
	assert.False(t, row.ExpiresAt.After(ceiling.Add(time.Minute)),
		"the access token must not outlive the credential's ceiling (got %s, ceiling %s)",
		row.ExpiresAt, ceiling)

	// Past the ceiling: the credential is finished, and the answer specifies the
	// remedy rather than looking like a transient failure.
	env.clock.advance(48 * time.Hour)
	dead, err := http.PostForm(env.server.URL+"/auth/cli/refresh", url.Values{"refresh_token": {rotated.RefreshToken}})
	require.NoError(t, err)
	defer func() { _ = dead.Body.Close() }()
	assert.Equal(t, http.StatusUnauthorized, dead.StatusCode)
	body := bodyOf(t, dead)
	assert.Contains(t, body, "invalid_grant")
	assert.Contains(t, body, "auth login")

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
// every rotation, for a credential the hub still honoured. admin_scope has
// no omitempty, so the same response answered a flat false for a credential
// that really did carry the scope.
//
// It is pinned here rather than in the CLI, and that is the point: the
// CLI-side test drives a stub that hard-codes the field, so it asserts what
// the fixture returns and would pass against a hub that sends nothing.
func TestRefresh_ReportsTheRefreshDeadlineAndTheScope(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	_, refreshToken := mintTokenForRefreshTest(t, env)

	resp, err := http.PostForm(env.server.URL+"/auth/cli/refresh", url.Values{"refresh_token": {refreshToken}})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body struct {
		ExpiresIn        int  `json:"expires_in"`
		RefreshExpiresIn int  `json:"refresh_expires_in"`
		AdminScope       bool `json:"admin_scope"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	assert.Positive(t, body.ExpiresIn, "the access window must be reported")
	// The whole window, because this credential is young: the clip only
	// bites near the absolute ceiling, which the sibling test covers.
	assert.InDelta(t, (auth.RefreshTokenTTL).Seconds(), float64(body.RefreshExpiresIn), 60,
		"the rotation must report the deadline that sends the device back to a browser")
	assert.False(t, body.AdminScope, "this grant asked for no admin scope")
}

// TestElevationRequiredHeaderValueIsPinned keeps the Go constant and the
// frontend's copy from drifting silently. The frontend cannot import this
// package, so the two are joined only by the literal below and the comment
// on each side; a rename that changes one and not the other turns every
// step-up prompt into an unexplained failure the user cannot act on.
//
// The literal is written out rather than compared to the constant, so
// renaming the constant alone does not make this pass.
func TestElevationRequiredHeaderValueIsPinned(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "Leapmux-Elevation-Required", service.ElevationRequiredHeader,
		"frontend/src/lib/elevation.ts keys on the lowercased form of this value")
}

// TestElevationFactorRefusalIsMarkedAsACredentialRejection is the guard on
// the failure the E2E suite found: a mistyped step-up password answered
// Unauthenticated, the browser's blanket "Unauthenticated means signed out"
// rule fired, and the prompt ended the very session it protects.
//
// The code stays Unauthenticated -- a rejected credential is what it means
// -- and the marker says WHICH credential, so a client can tell a dead
// session from a wrong answer.
func TestElevationFactorRefusalIsMarkedAsACredentialRejection(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "Leapmux-Credential-Rejected", service.CredentialRejectedHeader,
		"frontend/src/api/transport.ts keys on the lowercased form of this value")
}
