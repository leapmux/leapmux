package service_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
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
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/verifycode"
)

// apiAuthEnv wires the APIAuthHandler against an in-memory store with
// the bootstrap admin already provisioned, plus a SessionCache and the
// RefreshGraceCache the handler depends on.
type apiAuthEnv struct {
	store      store.Store
	validator  *auth.TokenValidator
	cache      *auth.SessionCache
	graceCache *auth.RefreshGraceCache
	server     *httptest.Server
	userID     string
}

func setupAPIAuth(t *testing.T) *apiAuthEnv {
	t.Helper()

	st := hubtestutil.OpenTestStore(t)
	hubtestutil.CreateTestAdmin(t, st)

	pepper := []byte("0123456789abcdef0123456789abcdef")
	tv, err := auth.NewTokenValidator(st, pepper)
	require.NoError(t, err)

	// SessionCache is needed by the handler to evict revoked bearers; we
	// don't run it through the interceptor, so just construct the bare
	// interceptor for its cache side-effect and stop the sweeper.
	_, sc := auth.NewInterceptor(st, nil, false, false)
	t.Cleanup(sc.Stop)

	gc, err := auth.NewRefreshGraceCache(auth.RefreshReuseGrace)
	require.NoError(t, err)

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	h := service.NewAPIAuthHandler(st, tv, sc, gc, srv.URL)
	h.RegisterRoutes(mux)

	u, err := st.Users().GetByUsername(context.Background(), "admin")
	require.NoError(t, err)

	return &apiAuthEnv{
		store:      st,
		validator:  tv,
		cache:      sc,
		graceCache: gc,
		server:     srv,
		userID:     u.ID,
	}
}

// adminCookie logs in as admin via the bootstrap fixture so handlers
// that gate on `requireSession` see an authenticated browser session.
func (e *apiAuthEnv) adminCookie(t *testing.T) *http.Cookie {
	t.Helper()
	tok, _, _, err := auth.Login(context.Background(), e.store, hubtestutil.TestAdminUsername, hubtestutil.TestAdminPassword)
	require.NoError(t, err)
	return &http.Cookie{Name: auth.CookieName, Value: tok}
}

// pkceVerifierAndChallenge generates a fresh verifier and the
// corresponding S256 code_challenge.
func pkceVerifierAndChallenge() (verifier, challenge string) {
	verifier = id.Generate()
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return
}

func TestAPIAuth_LocalRedirect_HappyPath(t *testing.T) {
	env := setupAPIAuth(t)
	cookie := env.adminCookie(t)

	verifier, challenge := pkceVerifierAndChallenge()
	state := id.Generate()
	redirect := "http://127.0.0.1:54321/callback"

	// /auth/cli/start renders the consent page when the session is valid.
	startURL := env.server.URL + "/auth/cli/start?" + url.Values{
		"redirect_uri":   {redirect},
		"state":          {state},
		"code_challenge": {challenge},
		"device_name":    {"laptop"},
	}.Encode()
	req, _ := http.NewRequest(http.MethodGet, startURL, nil)
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// /auth/cli/authorize POST issues the one-shot code and redirects to
	// the loopback URL.
	authClient := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	authURL := env.server.URL + "/auth/cli/authorize"
	form := url.Values{
		"redirect_uri":   {redirect},
		"state":          {state},
		"code_challenge": {challenge},
		"device_name":    {"laptop"},
	}
	req, _ = http.NewRequest(http.MethodPost, authURL, strings.NewReader(form.Encode()))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err = authClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusFound, resp.StatusCode)

	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	gotState := loc.Query().Get("state")
	gotCode := loc.Query().Get("code")
	assert.Equal(t, state, gotState)
	require.NotEmpty(t, gotCode)

	// Exchange code + verifier for a token pair.
	tokenForm := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {gotCode},
		"code_verifier": {verifier},
		"device_name":   {"laptop"},
	}
	resp, err = http.PostForm(env.server.URL+"/auth/cli/token", tokenForm)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("token exchange failed: %d %s", resp.StatusCode, string(body))
	}

	var tokens map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&tokens))
	access, _ := tokens["access_token"].(string)
	refresh, _ := tokens["refresh_token"].(string)
	require.True(t, strings.HasPrefix(access, "lmx_"))
	require.True(t, strings.HasPrefix(refresh, "lmx_"))

	// Bearer must validate against the in-memory token validator.
	info, err := env.validator.ValidateBearer(context.Background(), access)
	require.NoError(t, err)
	assert.Equal(t, env.userID, info.ID)
}

func TestAPIAuth_LocalRedirect_RejectsNonLoopback(t *testing.T) {
	env := setupAPIAuth(t)
	cookie := env.adminCookie(t)

	_, challenge := pkceVerifierAndChallenge()
	form := url.Values{
		"redirect_uri":   {"https://attacker.example/callback"},
		"state":          {"x"},
		"code_challenge": {challenge},
	}
	req, _ := http.NewRequest(http.MethodPost, env.server.URL+"/auth/cli/authorize", strings.NewReader(form.Encode()))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestAPIAuth_LocalRedirect_NotAuthenticated_RedirectsToLogin(t *testing.T) {
	env := setupAPIAuth(t)
	authClient := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := authClient.Get(env.server.URL + "/auth/cli/start?redirect_uri=http://127.0.0.1:1/x&state=s&code_challenge=c")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusFound, resp.StatusCode)
	loc := resp.Header.Get("Location")
	assert.True(t, strings.HasPrefix(loc, "/login?"), "expected redirect to /login, got %q", loc)
}

func TestAPIAuth_LocalRedirect_RejectsCodeReplay(t *testing.T) {
	env := setupAPIAuth(t)
	cookie := env.adminCookie(t)

	verifier, challenge := pkceVerifierAndChallenge()
	state := id.Generate()
	redirect := "http://127.0.0.1:54321/callback"

	// Issue the one-shot code.
	authClient := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	form := url.Values{
		"redirect_uri":   {redirect},
		"state":          {state},
		"code_challenge": {challenge},
	}
	req, _ := http.NewRequest(http.MethodPost, env.server.URL+"/auth/cli/authorize", strings.NewReader(form.Encode()))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := authClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	loc, _ := url.Parse(resp.Header.Get("Location"))
	code := loc.Query().Get("code")

	exchange := func() (int, map[string]any) {
		r, err := http.PostForm(env.server.URL+"/auth/cli/token", url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {code},
			"code_verifier": {verifier},
		})
		require.NoError(t, err)
		defer func() { _ = r.Body.Close() }()
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		return r.StatusCode, body
	}
	st1, _ := exchange()
	require.Equal(t, http.StatusOK, st1)

	// Replaying the code must fail.
	st2, body2 := exchange()
	assert.Equal(t, http.StatusBadRequest, st2)
	assert.Equal(t, "invalid_grant", body2["error"])
}

func TestAPIAuth_LocalRedirect_RejectsBadVerifier(t *testing.T) {
	env := setupAPIAuth(t)
	cookie := env.adminCookie(t)

	_, challenge := pkceVerifierAndChallenge()
	state := id.Generate()
	redirect := "http://127.0.0.1:54321/callback"

	authClient := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	form := url.Values{
		"redirect_uri":   {redirect},
		"state":          {state},
		"code_challenge": {challenge},
	}
	req, _ := http.NewRequest(http.MethodPost, env.server.URL+"/auth/cli/authorize", strings.NewReader(form.Encode()))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := authClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	loc, _ := url.Parse(resp.Header.Get("Location"))
	code := loc.Query().Get("code")

	// Wrong verifier — handler computes S256(verifier) and compares to
	// the stored code_challenge.
	resp, err = http.PostForm(env.server.URL+"/auth/cli/token", url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {"definitely-not-the-verifier"},
	})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	assert.Equal(t, "invalid_grant", body["error"])
}

func TestAPIAuth_DeviceCode_Pending_Approval_Success(t *testing.T) {
	env := setupAPIAuth(t)
	cookie := env.adminCookie(t)

	// Start device authorization.
	resp, err := http.PostForm(env.server.URL+"/auth/cli/device-authorization", url.Values{"device_name": {"server-1"}})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var grant map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&grant))
	deviceCode, _ := grant["device_code"].(string)
	userCode, _ := grant["user_code"].(string)
	require.NotEmpty(t, deviceCode)
	require.NotEmpty(t, userCode)
	assert.Contains(t, userCode, "-", "user_code should be display-formatted with a hyphen")

	// First poll: still pending.
	pollResp, err := http.PostForm(env.server.URL+"/auth/cli/token", url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {deviceCode},
	})
	require.NoError(t, err)
	defer func() { _ = pollResp.Body.Close() }()
	require.Equal(t, http.StatusBadRequest, pollResp.StatusCode)
	var pendingBody map[string]any
	_ = json.NewDecoder(pollResp.Body).Decode(&pendingBody)
	assert.Equal(t, "authorization_pending", pendingBody["error"])

	// User approves via the activation page.
	approveResp, err := postForm(env.server.URL+"/auth/cli/activate", url.Values{
		"user_code": {userCode},
	}, cookie)
	require.NoError(t, err)
	defer func() { _ = approveResp.Body.Close() }()
	require.Equal(t, http.StatusOK, approveResp.StatusCode)

	// Wait long enough for the throttle window since the previous poll.
	time.Sleep(service.DeviceCodePollInterval + 100*time.Millisecond)

	// Successful exchange.
	successResp, err := http.PostForm(env.server.URL+"/auth/cli/token", url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {deviceCode},
	})
	require.NoError(t, err)
	defer func() { _ = successResp.Body.Close() }()
	require.Equal(t, http.StatusOK, successResp.StatusCode)

	var tokens map[string]any
	require.NoError(t, json.NewDecoder(successResp.Body).Decode(&tokens))
	access, _ := tokens["access_token"].(string)
	require.True(t, strings.HasPrefix(access, "lmx_"))

	info, err := env.validator.ValidateBearer(context.Background(), access)
	require.NoError(t, err)
	assert.Equal(t, env.userID, info.ID)
}

func TestAPIAuth_DeviceCode_SlowDown_OnRapidPoll(t *testing.T) {
	env := setupAPIAuth(t)

	resp, err := http.PostForm(env.server.URL+"/auth/cli/device-authorization", url.Values{})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var grant map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&grant))
	deviceCode := grant["device_code"].(string)

	// First poll establishes the LastPolledAt anchor.
	r1, err := http.PostForm(env.server.URL+"/auth/cli/token", url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {deviceCode},
	})
	require.NoError(t, err)
	_ = r1.Body.Close()

	// Immediate second poll — within `interval`, so server replies slow_down.
	r2, err := http.PostForm(env.server.URL+"/auth/cli/token", url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {deviceCode},
	})
	require.NoError(t, err)
	defer func() { _ = r2.Body.Close() }()
	var body map[string]any
	require.NoError(t, json.NewDecoder(r2.Body).Decode(&body))
	assert.Equal(t, "slow_down", body["error"], "rapid poll should be throttled")
}

func TestAPIAuth_DeviceCode_ExpiredToken(t *testing.T) {
	env := setupAPIAuth(t)

	// Manually seed an expired grant directly via the store so we don't
	// have to wait DeviceCodeTTL in the test.
	dc := id.Generate()
	uc := verifycode.Generate()
	require.NoError(t, env.store.DeviceAuthorizations().Create(context.Background(), store.CreateDeviceAuthorizationParams{
		DeviceCode:      dc,
		UserCode:        uc,
		IntervalSeconds: 5,
		ExpiresAt:       time.Now().Add(-time.Minute),
	}))
	resp, err := http.PostForm(env.server.URL+"/auth/cli/token", url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {dc},
	})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "expired_token", body["error"])
}

func TestAPIAuth_DeviceCode_AccessDenied(t *testing.T) {
	env := setupAPIAuth(t)

	dc := id.Generate()
	uc := verifycode.Generate()
	require.NoError(t, env.store.DeviceAuthorizations().Create(context.Background(), store.CreateDeviceAuthorizationParams{
		DeviceCode:      dc,
		UserCode:        uc,
		IntervalSeconds: 0, // disable throttle for this test
		ExpiresAt:       time.Now().Add(time.Hour),
	}))

	// Manually mark the row as denied (Approved=2).
	_, err := env.store.DeviceAuthorizations().Deny(context.Background(), dc)
	require.NoError(t, err)

	resp, err := http.PostForm(env.server.URL+"/auth/cli/token", url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {dc},
	})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "access_denied", body["error"])
}

func TestAPIAuth_DeviceCode_UnknownDeviceCode(t *testing.T) {
	env := setupAPIAuth(t)
	resp, err := http.PostForm(env.server.URL+"/auth/cli/token", url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {"never-existed"},
	})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "invalid_grant", body["error"])
}

func TestAPIAuth_DeviceCode_AlreadyConsumed(t *testing.T) {
	env := setupAPIAuth(t)
	cookie := env.adminCookie(t)

	resp, err := http.PostForm(env.server.URL+"/auth/cli/device-authorization", url.Values{})
	require.NoError(t, err)
	var grant map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&grant))
	_ = resp.Body.Close()
	deviceCode := grant["device_code"].(string)
	userCode := grant["user_code"].(string)

	// Approve.
	approve, err := postForm(env.server.URL+"/auth/cli/activate", url.Values{"user_code": {userCode}}, cookie)
	require.NoError(t, err)
	_ = approve.Body.Close()

	// Wait past throttle, then exchange — should succeed.
	time.Sleep(service.DeviceCodePollInterval + 100*time.Millisecond)
	first, err := http.PostForm(env.server.URL+"/auth/cli/token", url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {deviceCode},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, first.StatusCode)
	_ = first.Body.Close()

	// Replay the same device_code — must be rejected.
	second, err := http.PostForm(env.server.URL+"/auth/cli/token", url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {deviceCode},
	})
	require.NoError(t, err)
	defer func() { _ = second.Body.Close() }()
	require.Equal(t, http.StatusBadRequest, second.StatusCode)
	var body map[string]any
	require.NoError(t, json.NewDecoder(second.Body).Decode(&body))
	assert.Equal(t, "invalid_grant", body["error"])
}

func TestAPIAuth_Activate_NormalizesUserCode(t *testing.T) {
	env := setupAPIAuth(t)
	cookie := env.adminCookie(t)

	resp, err := http.PostForm(env.server.URL+"/auth/cli/device-authorization", url.Values{})
	require.NoError(t, err)
	var grant map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&grant))
	_ = resp.Body.Close()
	displayCode := grant["user_code"].(string) // e.g. "ABC-DEF"

	// Submit the lowercased + extra-whitespace form; verifycode.Normalize
	// must accept it.
	noisy := strings.ToLower(" " + displayCode + " ")
	r, err := postForm(env.server.URL+"/auth/cli/activate", url.Values{"user_code": {noisy}}, cookie)
	require.NoError(t, err)
	defer func() { _ = r.Body.Close() }()
	assert.Equal(t, http.StatusOK, r.StatusCode)
}

func TestAPIAuth_Activate_RejectsUnknownCode(t *testing.T) {
	env := setupAPIAuth(t)
	cookie := env.adminCookie(t)
	r, err := postForm(env.server.URL+"/auth/cli/activate", url.Values{"user_code": {"ABC-DEF"}}, cookie)
	require.NoError(t, err)
	defer func() { _ = r.Body.Close() }()
	assert.Equal(t, http.StatusBadRequest, r.StatusCode)
}

func TestAPIAuth_Refresh_RotatesAndReturnsNewPair(t *testing.T) {
	env := setupAPIAuth(t)

	// Mint an api_token directly so we don't have to traverse the full
	// consent dance for every refresh test.
	tokenID := id.Generate()
	currentRefresh := auth.MintAccessSecret()
	require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:          tokenID,
		UserID:      env.userID,
		ClientType:  "cli",
		ClientName:  "test",
		SecretHash:  env.validator.HashSecret(auth.MintAccessSecret()),
		RefreshHash: env.validator.HashSecret(currentRefresh),
		Scope:       "remote:*",
	}))

	resp, err := http.PostForm(env.server.URL+"/auth/cli/refresh", url.Values{
		"refresh_token": {auth.FormatBearer(auth.BearerKindAPI, tokenID, currentRefresh)},
	})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	access, _ := body["access_token"].(string)
	refresh, _ := body["refresh_token"].(string)
	require.True(t, strings.HasPrefix(access, "lmx_"))
	require.True(t, strings.HasPrefix(refresh, "lmx_"))
	assert.NotEqual(t, auth.FormatBearer(auth.BearerKindAPI, tokenID, currentRefresh), refresh, "refresh must rotate")

	// The rotated access bearer must actually validate against the
	// token validator. ValidateBearer checks the row's secret_hash, so
	// if rotation forgot to write the new access hash the returned
	// bearer is dead-on-arrival.
	info, err := env.validator.ValidateBearer(context.Background(), access)
	require.NoError(t, err, "rotated access bearer must validate")
	assert.Equal(t, env.userID, info.ID)

	// The rotated refresh bearer must still be usable for a subsequent
	// refresh — i.e. it both validates and survives the rotation chain.
	resp2, err := http.PostForm(env.server.URL+"/auth/cli/refresh", url.Values{
		"refresh_token": {refresh},
	})
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()
	require.Equal(t, http.StatusOK, resp2.StatusCode, "second refresh on rotated pair must succeed")
}

func TestAPIAuth_Refresh_ReusedWithinGraceReturnsSamePair(t *testing.T) {
	env := setupAPIAuth(t)

	// Mint and rotate once.
	tokenID := id.Generate()
	prev := auth.MintAccessSecret()
	require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:          tokenID,
		UserID:      env.userID,
		ClientType:  "cli",
		ClientName:  "test",
		SecretHash:  env.validator.HashSecret(auth.MintAccessSecret()),
		RefreshHash: env.validator.HashSecret(prev),
		Scope:       "remote:*",
	}))
	first, err := http.PostForm(env.server.URL+"/auth/cli/refresh", url.Values{
		"refresh_token": {auth.FormatBearer(auth.BearerKindAPI, tokenID, prev)},
	})
	require.NoError(t, err)
	defer func() { _ = first.Body.Close() }()
	var firstBody map[string]any
	require.NoError(t, json.NewDecoder(first.Body).Decode(&firstBody))

	// Replay the rotated-out refresh; within the grace window the handler
	// must re-emit the same access/refresh pair the first call produced.
	retry, err := http.PostForm(env.server.URL+"/auth/cli/refresh", url.Values{
		"refresh_token": {auth.FormatBearer(auth.BearerKindAPI, tokenID, prev)},
	})
	require.NoError(t, err)
	defer func() { _ = retry.Body.Close() }()
	require.Equal(t, http.StatusOK, retry.StatusCode)
	var retryBody map[string]any
	require.NoError(t, json.NewDecoder(retry.Body).Decode(&retryBody))
	assert.Equal(t, firstBody["access_token"], retryBody["access_token"], "grace retry must return cached access token")
	assert.Equal(t, firstBody["refresh_token"], retryBody["refresh_token"], "grace retry must return cached refresh token")
}

func TestAPIAuth_Refresh_ReusedAfterGraceRevokesRow(t *testing.T) {
	env := setupAPIAuth(t)

	// Seed: create an api_token whose previous_refresh_hash is set but
	// already past its grace window. ValidateAPIRefresh must surface
	// ErrRefreshReused, the handler must revoke the row + bust the
	// caches, and a subsequent valid refresh must fail.
	tokenID := id.Generate()
	prev := auth.MintAccessSecret()
	cur := auth.MintAccessSecret()
	expiredGrace := time.Now().Add(-time.Hour)
	require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:          tokenID,
		UserID:      env.userID,
		ClientType:  "cli",
		ClientName:  "test",
		SecretHash:  env.validator.HashSecret(auth.MintAccessSecret()),
		RefreshHash: env.validator.HashSecret(cur),
		Scope:       "remote:*",
	}))
	require.NoError(t, env.store.APITokens().RotateRefresh(context.Background(), store.RotateAPITokenRefreshParams{
		ID:                       tokenID,
		NewSecretHash:            env.validator.HashSecret(auth.MintAccessSecret()),
		NewRefreshHash:           env.validator.HashSecret(cur),
		PreviousRefreshHash:      env.validator.HashSecret(prev),
		PreviousRefreshExpiresAt: &expiredGrace,
	}))

	// Reuse the previous refresh: outside the grace window → revoke.
	resp, err := http.PostForm(env.server.URL+"/auth/cli/refresh", url.Values{
		"refresh_token": {auth.FormatBearer(auth.BearerKindAPI, tokenID, prev)},
	})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// The current refresh must also fail now: reuse-after-grace revokes
	// the underlying row.
	resp2, err := http.PostForm(env.server.URL+"/auth/cli/refresh", url.Values{
		"refresh_token": {auth.FormatBearer(auth.BearerKindAPI, tokenID, cur)},
	})
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()
	assert.Equal(t, http.StatusUnauthorized, resp2.StatusCode)
}

func TestAPIAuth_Revoke_BustsCacheAndRowRevoked(t *testing.T) {
	env := setupAPIAuth(t)

	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:         tokenID,
		UserID:     env.userID,
		ClientType: "cli",
		ClientName: "test",
		SecretHash: env.validator.HashSecret(secret),
		Scope:      "remote:*",
	}))
	bearer := auth.FormatBearer(auth.BearerKindAPI, tokenID, secret)
	// Warm the bearer cache by validating once.
	_, err := env.validator.ValidateBearer(context.Background(), bearer)
	require.NoError(t, err)

	// Revoke via the public endpoint.
	resp, err := http.PostForm(env.server.URL+"/auth/cli/revoke", url.Values{"token": {bearer}})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Row is revoked.
	row, err := env.store.APITokens().GetByID(context.Background(), tokenID)
	require.NoError(t, err)
	assert.NotNil(t, row.RevokedAt, "token row should be revoked")

	// Subsequent validation must fail (ValidateBearer re-reads the row;
	// even before that, the cache should have been evicted).
	_, err = env.validator.ValidateBearer(context.Background(), bearer)
	assert.Error(t, err)
}

func TestAPIAuth_Revoke_DelegationToken_TouchesDelegationsTable(t *testing.T) {
	env := setupAPIAuth(t)

	// Seed a worker + workspace + delegation row. The revoke endpoint
	// must succeed and mark the delegation row revoked when the bearer
	// id resolves to a delegation_tokens row.
	workerID, workspaceID := seedDelegationFixtures(t, env)

	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, env.store.DelegationTokens().Create(context.Background(), store.CreateDelegationTokenParams{
		ID:          tokenID,
		UserID:      env.userID,
		WorkerID:    workerID,
		WorkspaceID: workspaceID,
		SecretHash:  env.validator.HashSecret(secret),
		ExpiresAt:   time.Now().Add(time.Hour),
	}))
	bearer := auth.FormatBearer(auth.BearerKindDelegation, tokenID, secret)

	resp, err := http.PostForm(env.server.URL+"/auth/cli/revoke", url.Values{"token": {bearer}})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	row, err := env.store.DelegationTokens().GetByID(context.Background(), tokenID)
	require.NoError(t, err)
	assert.NotNil(t, row.RevokedAt)
}

func TestAPIAuth_Revoke_BadBearerReturnsBadRequest(t *testing.T) {
	env := setupAPIAuth(t)
	// Empty token is still 400 — that's a missing-required-field error,
	// caught before the secret-verification path.
	resp, err := http.PostForm(env.server.URL+"/auth/cli/revoke", url.Values{"token": {""}})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// Malformed bearer (unparseable) is rejected as 401 by the secret-
	// verification path so the response shape doesn't distinguish
	// malformed-bearer from valid-id-wrong-secret — preventing an
	// attacker from probing for valid token_ids.
	resp2, err := http.PostForm(env.server.URL+"/auth/cli/revoke", url.Values{"token": {"not-a-bearer"}})
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()
	assert.Equal(t, http.StatusUnauthorized, resp2.StatusCode)
}

// TestAPIAuth_Revoke_WrongSecretRejected pins down the security fix for
// the unauthenticated-revoke vulnerability: a caller who knows only the
// non-secret token_id (which we return in JSON responses) MUST NOT be
// able to revoke a victim's session by submitting a bearer with a bogus
// secret. Returns 401, leaves the row untouched, leaves the cache warm.
func TestAPIAuth_Revoke_WrongSecretRejected(t *testing.T) {
	env := setupAPIAuth(t)

	// Real api_token row with a known good secret.
	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:         tokenID,
		UserID:     env.userID,
		ClientType: "cli",
		ClientName: "test",
		SecretHash: env.validator.HashSecret(secret),
		Scope:      "remote:*",
	}))
	goodBearer := auth.FormatBearer(auth.BearerKindAPI, tokenID, secret)
	// Warm the cache so we can later assert it wasn't busted.
	_, err := env.validator.ValidateBearer(context.Background(), goodBearer)
	require.NoError(t, err)

	// Attacker-style bearer: real token_id, wrong secret. RFC 7009 §2.1
	// requires the presented token to be valid; without verification,
	// anyone who learns a token_id (e.g. via a logged JSON response or
	// stale CLI install) could revoke a victim's session.
	attackerBearer := auth.FormatBearer(auth.BearerKindAPI, tokenID, "completely-bogus-secret")
	resp, err := http.PostForm(env.server.URL+"/auth/cli/revoke", url.Values{"token": {attackerBearer}})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// Row is NOT revoked.
	row, err := env.store.APITokens().GetByID(context.Background(), tokenID)
	require.NoError(t, err)
	assert.Nil(t, row.RevokedAt, "row must NOT be revoked when secret didn't verify")

	// Real bearer still validates (cache wasn't poisoned, row is alive).
	user, err := env.validator.ValidateBearer(context.Background(), goodBearer)
	require.NoError(t, err)
	assert.Equal(t, env.userID, user.ID)
}

// TestAPIAuth_Revoke_WrongSecretRejected_DelegationToken pins the same
// guarantee for the delegation_tokens table. delegation token_ids are
// even more abundantly exposed (they appear in mint responses, channel
// registration logs, and audit telemetry), so the secret check matters
// equally here.
func TestAPIAuth_Revoke_WrongSecretRejected_DelegationToken(t *testing.T) {
	env := setupAPIAuth(t)
	workerID, workspaceID := seedDelegationFixtures(t, env)

	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, env.store.DelegationTokens().Create(context.Background(), store.CreateDelegationTokenParams{
		ID:          tokenID,
		UserID:      env.userID,
		WorkerID:    workerID,
		WorkspaceID: workspaceID,
		SecretHash:  env.validator.HashSecret(secret),
		ExpiresAt:   time.Now().Add(time.Hour),
	}))

	attackerBearer := auth.FormatBearer(auth.BearerKindDelegation, tokenID, "wrong")
	resp, err := http.PostForm(env.server.URL+"/auth/cli/revoke", url.Values{"token": {attackerBearer}})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	row, err := env.store.DelegationTokens().GetByID(context.Background(), tokenID)
	require.NoError(t, err)
	assert.Nil(t, row.RevokedAt)
}

// TestAPIAuth_Revoke_UnknownTokenIDRejected guards against using the
// revoke endpoint as a token_id existence oracle. An attacker submitting
// a well-formed bearer for a non-existent token_id receives the same
// 401 as a wrong-secret attempt.
func TestAPIAuth_Revoke_UnknownTokenIDRejected(t *testing.T) {
	env := setupAPIAuth(t)
	bogus := auth.FormatBearer(auth.BearerKindAPI, id.Generate(), auth.MintAccessSecret())
	resp, err := http.PostForm(env.server.URL+"/auth/cli/revoke", url.Values{"token": {bogus}})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// TestAPIAuth_Revoke_AlreadyRevokedIsIdempotent confirms a client that
// retries revoke after a network blip (and presents the same valid
// bearer secret) still gets 200 OK — secret verification accepts
// already-revoked rows so re-revoke is a no-op rather than a 401.
func TestAPIAuth_Revoke_AlreadyRevokedIsIdempotent(t *testing.T) {
	env := setupAPIAuth(t)

	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:         tokenID,
		UserID:     env.userID,
		ClientType: "cli",
		ClientName: "test",
		SecretHash: env.validator.HashSecret(secret),
		Scope:      "remote:*",
	}))
	bearer := auth.FormatBearer(auth.BearerKindAPI, tokenID, secret)

	// First revoke: 200, row becomes revoked.
	resp, err := http.PostForm(env.server.URL+"/auth/cli/revoke", url.Values{"token": {bearer}})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	_ = resp.Body.Close()

	// Second revoke (same bearer): still 200 — VerifyBearerSecret accepts
	// already-revoked rows so the secret-holder doesn't need to handle
	// 401 retries on transient transport errors.
	resp2, err := http.PostForm(env.server.URL+"/auth/cli/revoke", url.Values{"token": {bearer}})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp2.StatusCode)
	_ = resp2.Body.Close()
}

func TestAPIAuth_Token_UnsupportedGrantType(t *testing.T) {
	env := setupAPIAuth(t)
	resp, err := http.PostForm(env.server.URL+"/auth/cli/token", url.Values{"grant_type": {"password"}})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "unsupported_grant_type", body["error"])
}

func TestAPIAuth_GetMethodOnlyHandlers_Reject(t *testing.T) {
	env := setupAPIAuth(t)
	for _, path := range []string{"/auth/cli/authorize", "/auth/cli/device-authorization", "/auth/cli/token", "/auth/cli/refresh", "/auth/cli/revoke"} {
		resp, err := http.Get(env.server.URL + path)
		require.NoError(t, err, path)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode, "GET on %s must be rejected", path)
	}
}

// --- Helpers ---

// postForm POSTs an x-www-form-urlencoded body with an attached cookie.
func postForm(targetURL string, form url.Values, cookies ...*http.Cookie) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, targetURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	return http.DefaultClient.Do(req)
}

func seedDelegationFixtures(t *testing.T, env *apiAuthEnv) (workerID, workspaceID string) {
	t.Helper()
	u, err := env.store.Users().GetByID(context.Background(), env.userID)
	require.NoError(t, err)
	workerID = id.Generate()
	require.NoError(t, env.store.Workers().Create(context.Background(), store.CreateWorkerParams{
		ID:              workerID,
		AuthToken:       id.Generate(),
		RegisteredBy:    env.userID,
		PublicKey:       []byte("test-x25519-key-32-bytes-padding"),
		MlkemPublicKey:  []byte("test-mlkem"),
		SlhdsaPublicKey: []byte("test-slhdsa"),
	}))
	workspaceID = id.Generate()
	require.NoError(t, env.store.Workspaces().Create(context.Background(), store.CreateWorkspaceParams{
		ID:          workspaceID,
		OrgID:       u.OrgID,
		OwnerUserID: env.userID,
		Title:       "ws",
	}))
	return workerID, workspaceID
}
