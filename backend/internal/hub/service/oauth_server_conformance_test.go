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
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// seedTestApp registers one app directly in the store, so a conformance case
// can pick the exact shape it needs -- a second client, a confidential one, an
// https redirect -- without walking the registration surface.
func seedTestApp(t *testing.T, env *apiAuthEnv, p store.CreateOAuthClientParams) string {
	t.Helper()
	if p.ClientID == "" {
		p.ClientID = id.Generate()
	}
	if p.ClientName == "" {
		p.ClientName = "Test app"
	}
	if p.Scopes == "" {
		p.Scopes = authscope.NonAdminGrant().String()
	}
	if p.GrantTypes == "" {
		p.GrantTypes = "authorization_code refresh_token"
	}
	if p.RegistrationSource == "" {
		p.RegistrationSource = store.OAuthClientSourceAdmin
	}
	_, err := env.store.OAuthClients().Create(context.Background(), p)
	require.NoError(t, err)
	return p.ClientID
}

// seedCode writes an authorization code the token leg can redeem.
func seedCode(t *testing.T, env *apiAuthEnv, clientID, redirectURI, challenge, scopes string) string {
	t.Helper()
	code := id.Generate()
	require.NoError(t, env.store.OAuthAuthorizationCodes().Create(context.Background(),
		store.CreateOAuthAuthorizationCodeParams{
			Code: code, UserID: userid.MustNew(env.userID), ClientID: clientID,
			CodeChallenge: challenge, RedirectURI: redirectURI, GrantedScopes: scopes,
			InstallationName: "laptop", ExpiresAt: time.Now().Add(time.Minute),
		}))
	return code
}

// postTokenForm posts to the token endpoint and returns the status and body.
func postTokenForm(t *testing.T, env *apiAuthEnv, form url.Values) (int, map[string]any) {
	t.Helper()
	resp, err := http.PostForm(env.server.URL+"/oauth/token", form)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	return resp.StatusCode, body
}

// A code belongs to ONE app, and the token leg proves it.
//
// RFC 6749 section 4.1.3 requires the client that redeems a code to be the one
// it was issued to. Without the check, an app that observed another's callback
// -- a shared machine, a log, a browser extension -- could redeem it as itself
// and hold a credential the account granted to somebody else.
func TestAuthorizationServer_ACodeIsRedeemableOnlyByItsOwnApp(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	verifier, challenge := pkceVerifierAndChallenge()
	const redirect = "https://app-a.example.com/callback"

	appA := seedTestApp(t, env, store.CreateOAuthClientParams{
		ClientName: "App A", RedirectURIs: redirect,
	})
	appB := seedTestApp(t, env, store.CreateOAuthClientParams{
		ClientName: "App B", RedirectURIs: "https://app-b.example.com/callback",
	})
	code := seedCode(t, env, appA, redirect, challenge, "workspace:read")

	status, body := postTokenForm(t, env, url.Values{
		"grant_type":    {service.GrantTypeAuthorizationCode},
		"client_id":     {appB},
		"redirect_uri":  {redirect},
		"code":          {code},
		"code_verifier": {verifier},
	})
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, "invalid_grant", body["error"])

	// And the code SURVIVES the refused attempt, so the app it belongs to can
	// still redeem it. A refusal that consumed the code would let any client
	// deny another one its credential.
	status, body = postTokenForm(t, env, url.Values{
		"grant_type":    {service.GrantTypeAuthorizationCode},
		"client_id":     {appA},
		"redirect_uri":  {redirect},
		"code":          {code},
		"code_verifier": {verifier},
	})
	require.Equalf(t, http.StatusOK, status, "the owning app must still redeem it: %v", body)
	assert.NotEmpty(t, body["access_token"])
}

// The token leg COMPARES redirect_uri, per RFC 6749 section 4.1.3.
//
// Without it, a code intercepted at one registered address could be redeemed as
// though it came from another -- which matters for an app that registers
// several, because the interception and the redemption need not be the same
// address.
func TestAuthorizationServer_TokenLegComparesTheRedirectURI(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	const used = "https://app.example.com/callback"
	const other = "https://app.example.com/other"
	clientID := seedTestApp(t, env, store.CreateOAuthClientParams{
		RedirectURIs: used + "\n" + other,
	})

	for name, presented := range map[string]string{
		"omitted":                     "",
		"a second registered address": other,
		"an unregistered address":     "https://evil.example.com/callback",
	} {
		t.Run(name+" is refused", func(t *testing.T) {
			verifier, challenge := pkceVerifierAndChallenge()
			code := seedCode(t, env, clientID, used, challenge, "workspace:read")

			form := url.Values{
				"grant_type":    {service.GrantTypeAuthorizationCode},
				"client_id":     {clientID},
				"code":          {code},
				"code_verifier": {verifier},
			}
			if presented != "" {
				form.Set("redirect_uri", presented)
			}
			status, body := postTokenForm(t, env, form)
			assert.Equal(t, http.StatusBadRequest, status)
			assert.Equal(t, "invalid_grant", body["error"])
		})
	}

	// The address the authorization ACTUALLY used still works, so the check is
	// a comparison rather than a refusal of everything.
	verifier, challenge := pkceVerifierAndChallenge()
	code := seedCode(t, env, clientID, used, challenge, "workspace:read")
	status, body := postTokenForm(t, env, url.Values{
		"grant_type":    {service.GrantTypeAuthorizationCode},
		"client_id":     {clientID},
		"redirect_uri":  {used},
		"code":          {code},
		"code_verifier": {verifier},
	})
	require.Equalf(t, http.StatusOK, status, "%v", body)
}

// A PUBLIC client presenting a secret is refused rather than served.
//
// The two client types are distinguished by whether the hub stored a secret
// hash, so a public client has nothing to compare against. Ignoring the secret
// and serving the request would let an attacker who guessed at a client's type
// learn nothing -- but it would also mean a confidential client downgraded to
// public kept working, with its secret silently unchecked.
func TestAuthorizationServer_APublicClientPresentingASecretIsRefused(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	verifier, challenge := pkceVerifierAndChallenge()
	const redirect = "https://app.example.com/callback"
	clientID := seedTestApp(t, env, store.CreateOAuthClientParams{RedirectURIs: redirect})
	code := seedCode(t, env, clientID, redirect, challenge, "workspace:read")

	status, body := postTokenForm(t, env, url.Values{
		"grant_type":    {service.GrantTypeAuthorizationCode},
		"client_id":     {clientID},
		"client_secret": {"a-secret-this-client-does-not-have"},
		"redirect_uri":  {redirect},
		"code":          {code},
		"code_verifier": {verifier},
	})
	assert.Equal(t, http.StatusUnauthorized, status)
	assert.Equal(t, "invalid_client", body["error"])
}

// A CONFIDENTIAL client must present its secret, and the right one.
func TestAuthorizationServer_AConfidentialClientMustPresentItsSecret(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	const redirect = "https://server.example.com/callback"
	secret := id.Generate()
	clientID := seedTestApp(t, env, store.CreateOAuthClientParams{
		RedirectURIs: redirect, SecretHash: env.validator.HashSecret(secret),
	})

	exchange := func(t *testing.T, present string, has bool) (int, map[string]any) {
		t.Helper()
		verifier, challenge := pkceVerifierAndChallenge()
		code := seedCode(t, env, clientID, redirect, challenge, "workspace:read")
		form := url.Values{
			"grant_type":    {service.GrantTypeAuthorizationCode},
			"client_id":     {clientID},
			"redirect_uri":  {redirect},
			"code":          {code},
			"code_verifier": {verifier},
		}
		if has {
			form.Set("client_secret", present)
		}
		return postTokenForm(t, env, form)
	}

	status, body := exchange(t, "", false)
	assert.Equal(t, http.StatusUnauthorized, status, "an absent secret must be refused")
	assert.Equal(t, "invalid_client", body["error"])

	status, body = exchange(t, "the-wrong-secret", true)
	assert.Equal(t, http.StatusUnauthorized, status)
	assert.Equal(t, "invalid_client", body["error"])

	status, body = exchange(t, secret, true)
	require.Equalf(t, http.StatusOK, status, "the right secret must be served: %v", body)
	assert.NotEmpty(t, body["access_token"])
}

// A refresh may NARROW a grant, and the narrowing persists.
//
// RFC 6749 section 6 allows it, and it is what an app does when it no longer
// needs a permission. It may never WIDEN, because the account holder is not
// there to approve one -- so a later refresh cannot walk back up.
func TestAuthorizationServer_RefreshNarrowsAndCannotReWiden(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	tokenID := id.Generate()
	refresh := auth.MintAccessSecret()
	require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID: tokenID, UserID: userid.MustNew(env.userID), ClientID: oauthapp.ControlCLIClientID,
		InstallationName: "laptop",
		GrantedScopes:    "workspace:read workspace:write worker:read file:read",
		SecretHash:       env.validator.HashSecret(auth.MintAccessSecret()),
		RefreshHash:      env.validator.HashSecret(refresh),
	}))

	// NARROW to a subset.
	status, body := postTokenForm(t, env, url.Values{
		"grant_type":    {service.GrantTypeRefreshToken},
		"client_id":     {oauthapp.ControlCLIClientID},
		"refresh_token": {auth.FormatBearer(auth.BearerKindAPI, tokenID, refresh)},
		"scope":         {"workspace:read"},
	})
	require.Equalf(t, http.StatusOK, status, "%v", body)
	granted, _ := body["scope"].(string)
	assert.NotContains(t, strings.Fields(granted), "file:read", "the refresh must narrow")
	assert.Contains(t, strings.Fields(granted), "workspace:read")

	// It PERSISTS on the row, so the next request is bound by it rather than
	// by what the consent originally granted.
	row, err := env.store.APITokens().GetByID(context.Background(), tokenID)
	require.NoError(t, err)
	assert.NotContains(t, strings.Fields(row.GrantedScopes), "file:read")

	// And it cannot be walked back up.
	next, _ := body["refresh_token"].(string)
	require.NotEmpty(t, next)
	status, body = postTokenForm(t, env, url.Values{
		"grant_type":    {service.GrantTypeRefreshToken},
		"client_id":     {oauthapp.ControlCLIClientID},
		"refresh_token": {next},
		"scope":         {"workspace:read workspace:write file:read"},
	})
	assert.Equal(t, http.StatusBadRequest, status, "a refresh must never widen a grant")
	assert.Equal(t, "invalid_scope", body["error"])

	after, err := env.store.APITokens().GetByID(context.Background(), tokenID)
	require.NoError(t, err)
	assert.NotContains(t, strings.Fields(after.GrantedScopes), "file:read",
		"a refused widening must leave the stored grant alone")
}

// A loopback redirect matches on any PORT and no other difference.
//
// RFC 8252 section 7.3: a native app binds whatever port is free at launch, so
// the registered address carries none. Everything else is compared literally --
// a different path is a different address, and treating it as a match would
// turn one registration into a redirect to anywhere on that host.
func TestAuthorizationServer_LoopbackMatchesAnyPortAndNothingElse(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	cookie := env.elevatedAdminCookie(t)
	_, challenge := pkceVerifierAndChallenge()

	authorize := func(t *testing.T, redirect string) *http.Response {
		t.Helper()
		params := url.Values{
			"client_id":             {oauthapp.ControlCLIClientID},
			"response_type":         {"code"},
			"code_challenge_method": {"S256"},
			"redirect_uri":          {redirect},
			"state":                 {"state-1"},
			"code_challenge":        {challenge},
		}
		return getWithCookie(t, env.server.URL+"/oauth/authorize?"+params.Encode(), cookie)
	}

	for _, accepted := range []string{
		"http://127.0.0.1/callback",
		"http://127.0.0.1:54321/callback",
		"http://127.0.0.1:1/callback",
		"http://127.0.0.1:65535/callback",
	} {
		t.Run("accepts "+accepted, func(t *testing.T) {
			resp := authorize(t, accepted)
			assert.Equal(t, http.StatusOK, resp.StatusCode)
		})
	}

	for _, refused := range []string{
		// A different PATH on the same loopback host.
		"http://127.0.0.1:54321/other",
		// A different loopback HOST than the one registered.
		"http://[::1]:54321/callback",
		// A non-loopback host that merely starts the same way.
		"http://127.0.0.1.evil.example.com/callback",
		// HTTPS where the registration says http.
		"https://127.0.0.1:54321/callback",
	} {
		t.Run("refuses "+refused, func(t *testing.T) {
			resp := authorize(t, refused)
			// The hub's OWN page, and no redirect: an unmatched address is
			// exactly the case RFC 6749 section 4.1.2.1 forbids redirecting
			// to, because doing so is a working open redirect.
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
			assert.Empty(t, resp.Header.Get("Location"))
			assert.NotContains(t, bodyOf(t, resp), "name=\"decision\"",
				"a refused address must render no consent form")
		})
	}
}

// An unregistered client_id renders the hub's own page and redirects NOWHERE.
//
// Until the client and its address are both known, there is no address the hub
// may send a browser to. Redirecting an error to a caller-supplied address is a
// working open redirect, aimed by whoever wrote the link.
func TestAuthorizationServer_AnUnknownClientRedirectsNowhere(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	cookie := env.elevatedAdminCookie(t)
	_, challenge := pkceVerifierAndChallenge()

	params := url.Values{
		"client_id":             {"no-such-app"},
		"response_type":         {"code"},
		"code_challenge_method": {"S256"},
		"redirect_uri":          {"https://evil.example.com/callback"},
		"state":                 {"state-marker-the-caller-chose"},
		"code_challenge":        {challenge},
		"installation_name":     {"installation-marker-the-caller-chose"},
	}
	resp := getWithCookie(t, env.server.URL+"/oauth/authorize?"+params.Encode(), cookie)

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Location"))
	body := bodyOf(t, resp)
	assert.NotContains(t, body, "name=\"decision\"")

	// EVERY caller-chosen value in the request, absent from the page.
	//
	// This is the last place attacker text could reach a browser -- the hub
	// may not redirect to an unregistered address, so it renders instead --
	// and the page's data carries one field, a sentence the hub wrote. A
	// single omission here would be a reflected-content surface on the one
	// page a phishing link lands somebody on.
	for _, chosen := range []string{
		"no-such-app",
		"evil.example.com",
		"state-marker-the-caller-chose",
		"installation-marker-the-caller-chose",
	} {
		assert.NotContainsf(t, body, chosen,
			"the page must not echo %q, which the caller chose", chosen)
	}
	assert.Contains(t, body, "That app is not registered on this hub",
		"and it must state the hub's own reason, so the refusal is still actionable")
}

// A REGISTERED app presenting an unregistered redirect_uri is the other half of
// the same rule. The client RESOLVES here, so a naive implementation has an
// address it could bounce an error to -- and RFC 6749 section 4.1.2.1 forbids
// exactly that, because the redirect would hand the attacker's page the state
// and any code the error response still carried. The hub renders its own page
// instead, on its own origin, from its own closed sentence set.
func TestAuthorizationServer_AMismatchedRedirectURIForARegisteredAppRedirectsNowhere(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	cookie := env.elevatedAdminCookie(t)
	_, challenge := pkceVerifierAndChallenge()

	clientID := seedTestApp(t, env, store.CreateOAuthClientParams{
		RedirectURIs: "https://app.example.com/callback",
	})

	params := url.Values{
		"client_id":             {clientID},
		"response_type":         {"code"},
		"code_challenge_method": {"S256"},
		"redirect_uri":          {"https://app.example.com.evil.io/callback"},
		"state":                 {"state-marker-the-caller-chose"},
		"code_challenge":        {challenge},
		"installation_name":     {"installation-marker-the-caller-chose"},
	}
	resp := getWithCookie(t, env.server.URL+"/oauth/authorize?"+params.Encode(), cookie)

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Location"))
	body := bodyOf(t, resp)
	assert.NotContains(t, body, "name=\"decision\"")

	// Every caller-chosen value stays off the page, for the same reason as the
	// unknown-client case: this is the one refusal the hub cannot redirect, so
	// the page is the last place attacker text could reach a browser.
	for _, chosen := range []string{
		"evil.io",
		"state-marker-the-caller-chose",
		"installation-marker-the-caller-chose",
	} {
		assert.NotContainsf(t, body, chosen,
			"the page must not echo %q, which the caller chose", chosen)
	}
	assert.Contains(t, body, "The address this app asked the hub to return to is not one it registered")
}

// The code-exchange leg answers the scope the account GRANTED, never a scope
// this request names. RFC 6749 fixes the token leg to the authorization the
// consent step recorded; a `scope` parameter is a section 6 narrowing that the
// refresh leg accepts, and the exchange leg does not accept at all. Answering a
// wider one would let any holder of an intercepted code widen what the user
// consented to.
func TestAuthorizationServer_TheTokenLegAnswersTheGrantedScopeNotTheRequestedOne(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	verifier, challenge := pkceVerifierAndChallenge()
	const redirect = "https://app.example.com/callback"
	clientID := seedTestApp(t, env, store.CreateOAuthClientParams{RedirectURIs: redirect})
	code := seedCode(t, env, clientID, redirect, challenge, "workspace:read")

	status, body := postTokenForm(t, env, url.Values{
		"grant_type":    {service.GrantTypeAuthorizationCode},
		"client_id":     {clientID},
		"redirect_uri":  {redirect},
		"code":          {code},
		"code_verifier": {verifier},
		"scope":         {"workspace:read agent:write admin:users"},
	})
	require.Equalf(t, http.StatusOK, status, "%v", body)
	assert.Equal(t, "workspace:read", body["scope"],
		"the response must report what the account granted, not what this request asked for")
}

// postRevokeForm posts to the revocation endpoint, optionally with HTTP Basic
// client authentication, and returns the status and decoded error body.
func postRevokeForm(t *testing.T, env *apiAuthEnv, form url.Values, basicUser, basicPass string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, env.server.URL+"/oauth/revoke", strings.NewReader(form.Encode()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if basicUser != "" {
		req.SetBasicAuth(basicUser, basicPass)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	return resp.StatusCode, body
}

// seedReversibleToken mints an api_tokens row for clientID whose secret the
// test knows, so a revocation case can present the FULL bearer.
func seedReversibleToken(t *testing.T, env *apiAuthEnv, clientID string) (bearer string, tokenID string) {
	t.Helper()
	tokenID = id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:               tokenID,
		UserID:           userid.MustNew(env.userID),
		ClientID:         clientID,
		InstallationName: "test",
		GrantedScopes:    authscope.NonAdminGrant().String(),
		SecretHash:       env.validator.HashSecret(secret),
	}))
	return auth.FormatBearer(auth.BearerKindAPI, tokenID, secret), tokenID
}

// RFC 7009 section 2.1's client rules, on the leg that ends a credential.
//
// A CONFIDENTIAL app must authenticate (Basic or body secret) before anything
// else; a PUBLIC app must name itself with its client_id; and neither may end
// another app's credential. The bearer secret stays the other half of the
// proof, so a leaked token_id alone still revokes nothing -- but possession of
// one app's materials must not let it tear down a different app's
// installations either.
func TestAuthorizationServer_RevocationAuthenticatesTheClient(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	const confSecret = "a-secret-only-the-confidential-app-holds"
	confidential := seedTestApp(t, env, store.CreateOAuthClientParams{
		ClientName: "Confidential app", SecretHash: env.validator.HashSecret(confSecret),
	})
	public := seedTestApp(t, env, store.CreateOAuthClientParams{ClientName: "Public app"})
	otherPublic := seedTestApp(t, env, store.CreateOAuthClientParams{ClientName: "Another public app"})

	revoked := func(t *testing.T, tokenID string) bool {
		t.Helper()
		row, err := env.store.APITokens().GetByID(context.Background(), tokenID)
		require.NoError(t, err)
		return row.RevokedAt != nil
	}

	t.Run("a confidential app must authenticate", func(t *testing.T) {
		bearer, tokenID := seedReversibleToken(t, env, confidential)
		status, body := postRevokeForm(t, env, url.Values{"token": {bearer}}, "", "")
		assert.Equal(t, http.StatusUnauthorized, status)
		assert.Equal(t, "invalid_client", body["error"])
		assert.False(t, revoked(t, tokenID))
	})

	t.Run("a confidential app with the wrong secret is refused", func(t *testing.T) {
		bearer, tokenID := seedReversibleToken(t, env, confidential)
		status, body := postRevokeForm(t, env, url.Values{
			"token": {bearer}, "client_id": {confidential}, "client_secret": {"wrong"},
		}, "", "")
		assert.Equal(t, http.StatusUnauthorized, status)
		assert.Equal(t, "invalid_client", body["error"])
		assert.False(t, revoked(t, tokenID))
	})

	t.Run("a confidential app authenticating with Basic revokes", func(t *testing.T) {
		bearer, tokenID := seedReversibleToken(t, env, confidential)
		status, _ := postRevokeForm(t, env, url.Values{"token": {bearer}}, confidential, confSecret)
		assert.Equal(t, http.StatusOK, status)
		assert.True(t, revoked(t, tokenID))
	})

	t.Run("a public app must name itself", func(t *testing.T) {
		bearer, tokenID := seedReversibleToken(t, env, public)
		status, body := postRevokeForm(t, env, url.Values{"token": {bearer}}, "", "")
		assert.Equal(t, http.StatusBadRequest, status)
		assert.Equal(t, "invalid_request", body["error"])
		assert.False(t, revoked(t, tokenID))
	})

	t.Run("one app cannot end another's credential", func(t *testing.T) {
		bearer, tokenID := seedReversibleToken(t, env, public)
		status, body := postRevokeForm(t, env, url.Values{
			"token": {bearer}, "client_id": {otherPublic},
		}, "", "")
		assert.Equal(t, http.StatusBadRequest, status)
		assert.Equal(t, "invalid_grant", body["error"])
		assert.False(t, revoked(t, tokenID))

		// The OWNER still can, which is what makes the refusal a refusal
		// rather than an outage.
		status, _ = postRevokeForm(t, env, url.Values{
			"token": {bearer}, "client_id": {public},
		}, "", "")
		assert.Equal(t, http.StatusOK, status)
		assert.True(t, revoked(t, tokenID))
	})

	t.Run("a public client presenting a secret is refused", func(t *testing.T) {
		bearer, tokenID := seedReversibleToken(t, env, public)
		status, body := postRevokeForm(t, env, url.Values{
			"token": {bearer}, "client_id": {public}, "client_secret": {"a-secret-it-should-not-hold"},
		}, "", "")
		assert.Equal(t, http.StatusUnauthorized, status)
		assert.Equal(t, "invalid_client", body["error"])
		assert.False(t, revoked(t, tokenID))
	})

	// A delegation bearer has no app to bind to: the secret is its whole
	// proof, and demanding a client_id would strand the worker-side surface
	// the extension exists for.
	t.Run("a delegation bearer needs no client", func(t *testing.T) {
		workerID, _ := seedDelegationFixtures(t, env)
		delegID := id.Generate()
		secret := auth.MintAccessSecret()
		require.NoError(t, env.store.DelegationTokens().Create(context.Background(), store.CreateDelegationTokenParams{
			GrantedScopes: "workspace:read worker:read",
			ID:            delegID, UserID: userid.MustNew(env.userID), WorkerID: workerID,
			SecretHash: env.validator.HashSecret(secret), ExpiresAt: time.Now().Add(time.Hour),
		}))
		status, _ := postRevokeForm(t, env, url.Values{
			"token": {auth.FormatBearer(auth.BearerKindDelegation, delegID, secret)},
		}, "", "")
		assert.Equal(t, http.StatusOK, status)
		row, err := env.store.DelegationTokens().GetByID(context.Background(), delegID)
		require.NoError(t, err)
		assert.NotNil(t, row.RevokedAt)
	})
}

// The scope a request names is answered BEFORE anything renders, and the
// answer belongs to the client: by the time scopes are read, RFC 6749 section
// 4.1.2.1 has both the client and its address verified, so invalid_scope
// travels back to the redirect address rather than onto a hub page.
//
// The refusal-before-render is also what keeps caller-chosen text off the
// consent screen: a page that echoed an unrecognised scope string would let
// `scope=workspace:read Also give them your password` write an extra bullet
// into the permission list.
func TestAuthorizationServer_AnUnknownScopeIsRefusedBeforeRendering(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	cookie := env.adminCookie(t)
	env.elevate(t, cookie)

	t.Run("a scope nobody defined", func(t *testing.T) {
		query := startQuery(url.Values{"scope": {"workspace:read not-a-permission"}})
		resp := getWithCookie(t, env.server.URL+"/oauth/authorize?"+query.Encode(), cookie)

		require.Equal(t, http.StatusFound, resp.StatusCode)
		dest, err := url.Parse(resp.Header.Get("Location"))
		require.NoError(t, err)
		assert.Equal(t, "127.0.0.1:54321", dest.Host, "the answer must reach the app that asked")
		assert.Equal(t, "invalid_scope", dest.Query().Get("error"))
		assert.Equal(t, query.Get("state"), dest.Query().Get("state"),
			"a client that cannot match the callback to its own request must discard it")
	})

	t.Run("a scope beyond the app's registered ceiling", func(t *testing.T) {
		const redirect = "https://narrow.example.com/callback"
		narrow := seedTestApp(t, env, store.CreateOAuthClientParams{
			ClientName: "Narrow app", RedirectURIs: redirect, Scopes: "workspace:read",
		})
		query := url.Values{
			"client_id":             {narrow},
			"response_type":         {"code"},
			"code_challenge_method": {"S256"},
			"redirect_uri":          {redirect},
			"state":                 {"state-1"},
			"code_challenge":        {strings.Repeat("c", 43)},
			"scope":                 {"workspace:read terminal:write"},
		}
		resp := getWithCookie(t, env.server.URL+"/oauth/authorize?"+query.Encode(), cookie)

		require.Equal(t, http.StatusFound, resp.StatusCode)
		dest, err := url.Parse(resp.Header.Get("Location"))
		require.NoError(t, err)
		assert.Equal(t, "narrow.example.com", dest.Host)
		assert.Equal(t, "invalid_scope", dest.Query().Get("error"))
		assert.Contains(t, dest.Query().Get("error_description"), "not registered")
	})
}

// The credential's row can vanish between the two reads the endpoint makes of
// it -- the secret verifies against one lookup, the client binding reads it
// back with another -- and losing it there is a revoke that already happened,
// not a refusal a retrying client would have to distinguish from a real one.
func TestAuthorizationServer_RevocationTreatsAVanishedRowAsAlreadyEnded(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	public := seedTestApp(t, env, store.CreateOAuthClientParams{ClientName: "Public app"})
	bearer, _ := seedReversibleToken(t, env, public)

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	// The VALIDATOR keeps the real store, so the secret verifies; only the
	// handler's own read-back loses the row. That is the interleaving a
	// concurrent hard-delete produces, compressed into one request.
	service.NewOAuthServerHandler(service.OAuthServerDeps{
		Store: apiTokenOverrideStore{
			Store: env.store,
			api:   apiLookupFailTokens{APITokenStore: env.store.APITokens(), err: store.ErrNotFound},
		},
		Validator: env.validator,
		Lifecycle: auth.NewCredentialLifecycleEffects(env.cache, noopBearerCloser{}, nil),
		HubURL:    func() string { return srv.URL },
	}).RegisterRoutes(mux)

	resp, err := http.PostForm(srv.URL+"/oauth/revoke", url.Values{
		"token":     {bearer},
		"client_id": {public},
	})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// A credential on a RETIRED app still ends, without client authentication.
//
// Retiring an app revokes its credentials in the same transaction, but the
// revocation endpoint's secret check deliberately still matches such a row
// (an idempotent re-revoke is a 200). Demanding a client_id before repeating
// what the cascade already did would turn every disconnect retry after an app
// retirement into a refusal nothing can act on.
func TestAuthorizationServer_RevocationOfARetiredAppsCredentialIsIdempotent(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	public := seedTestApp(t, env, store.CreateOAuthClientParams{ClientName: "Public app"})
	bearer, tokenID := seedReversibleToken(t, env, public)

	_, err := env.store.OAuthClients().Revoke(context.Background(), store.OAuthClientOwnershipParams{
		ClientID: public, CallerUserID: userid.MustNew(env.userID), CallerIsAdmin: true,
	})
	require.NoError(t, err)

	// No client_id: on a LIVE app this is the 400 the binding rules demand.
	resp, err := http.PostForm(env.server.URL+"/oauth/revoke", url.Values{"token": {bearer}})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	row, err := env.store.APITokens().GetByID(context.Background(), tokenID)
	require.NoError(t, err)
	assert.NotNil(t, row.RevokedAt, "the credential must still be ended, not merely forgiven")
}

// The mint response's `scope` states what the credential REACHES, not what the
// consent alone says.
//
// An owner can shrink a registration inside a code's ten-minute TTL, and the
// CLI persists the response's scope into its credential file and prints it at
// `auth status`. Reporting the stored consent there named a permission
// loadBearer refuses on the credential's next call -- the exact inconsistency
// the refresh leg's own doc forbids, and the one reachableGrantOf exists to
// remove from every reporting surface.
func TestAuthorizationServer_MintReportsTheReachableScope(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	verifier, challenge := pkceVerifierAndChallenge()
	const redirect = "https://narrow.example.com/callback"

	app := seedTestApp(t, env, store.CreateOAuthClientParams{
		ClientName: "Narrowed app", RedirectURIs: redirect,
		Scopes: "workspace:read workspace:write",
	})
	code := seedCode(t, env, app, redirect, challenge, "workspace:read workspace:write")

	// The owner shrinks the registration between the consent and the exchange.
	_, err := env.store.OAuthClients().Update(context.Background(), store.UpdateOAuthClientParams{
		ClientID: app, ClientName: "Narrowed app", RedirectURIs: redirect,
		Scopes: "workspace:read", GrantTypes: "authorization_code refresh_token",
		CallerUserID: userid.MustNew(env.userID), CallerIsAdmin: true,
	})
	require.NoError(t, err)

	status, body := postTokenForm(t, env, url.Values{
		"grant_type":    {service.GrantTypeAuthorizationCode},
		"client_id":     {app},
		"redirect_uri":  {redirect},
		"code":          {code},
		"code_verifier": {verifier},
	})
	require.Equalf(t, http.StatusOK, status, "the exchange itself succeeds: %v", body)
	assert.Equal(t, "workspace:read", body["scope"],
		"the response names the consent intersected with the registration as it stands at the exchange")
}
