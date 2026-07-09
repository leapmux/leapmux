package service_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
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
// the bootstrap admin already provisioned, plus the token validator and
// session cache the handler depends on.
type apiAuthEnv struct {
	store     store.Store
	validator *auth.TokenValidator
	cache     *auth.AuthContextRegistry
	closer    *recordingBearerCloser
	server    *httptest.Server
	userID    string
}

type recordingBearerCloser struct {
	mu             sync.Mutex
	tokenIDs       []string
	kinds          []auth.BearerKind
	rescheduledIDs []string
}

type noopBearerCloser struct{}

func (noopBearerCloser) CloseChannelsByBearer(auth.BearerRef) int        { return 0 }
func (noopBearerCloser) CloseChannelsBySession(string) int               { return 0 }
func (noopBearerCloser) CloseChannelsByUserRevocation(string, int64) int { return 0 }
func (noopBearerCloser) RestampSessionGeneration(string, int64)          {}

func TestNewAPIAuthHandlerRequiresCredentialLifecycleEffects(t *testing.T) {
	require.Panics(t, func() {
		service.NewAPIAuthHandler(nil, nil, nil, "")
	})
}

func (c *recordingBearerCloser) CloseChannelsByBearer(ref auth.BearerRef) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.kinds = append(c.kinds, ref.Kind())
	c.tokenIDs = append(c.tokenIDs, ref.TokenID())
	return 0
}

func (*recordingBearerCloser) CloseChannelsBySession(string) int               { return 0 }
func (*recordingBearerCloser) CloseChannelsByUserRevocation(string, int64) int { return 0 }
func (*recordingBearerCloser) RestampSessionGeneration(string, int64)          {}

// recordingBearerCloser doubles as the ChannelExpiryRescheduler so one fake
// records both bearer teardown and rotation-driven expiry extension.
func (c *recordingBearerCloser) RescheduleExpiryByBearer(ref auth.BearerRef, _ auth.CredentialDeadline) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rescheduledIDs = append(c.rescheduledIDs, ref.TokenID())
}

func (*recordingBearerCloser) RescheduleExpiryBySession(string, auth.CredentialDeadline) {}

func (c *recordingBearerCloser) rescheduled(tokenID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, id := range c.rescheduledIDs {
		if id == tokenID {
			return true
		}
	}
	return false
}

func (c *recordingBearerCloser) closed(tokenID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, got := range c.tokenIDs {
		if got == tokenID {
			return true
		}
	}
	return false
}

func setupAPIAuth(t *testing.T) *apiAuthEnv {
	t.Helper()

	st := hubtestutil.OpenTestStore(t)
	hubtestutil.CreateTestAdmin(t, st)

	pepper := []byte("0123456789abcdef0123456789abcdef")
	tv, err := auth.NewTokenValidator(st, pepper)
	require.NoError(t, err)

	// AuthContextRegistry is needed by the handler to evict revoked bearers; we
	// don't run it through the interceptor, so just construct the bare
	// interceptor for its cache side-effect and stop the sweeper.
	_, sc := auth.NewInterceptor(st, nil, false, false)
	t.Cleanup(sc.Stop)

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	closer := &recordingBearerCloser{}
	h := service.NewAPIAuthHandler(st, tv, auth.NewCredentialLifecycleEffects(sc, closer, closer), srv.URL)
	h.RegisterRoutes(mux)

	u, err := st.Users().GetByUsername(context.Background(), "admin")
	require.NoError(t, err)

	return &apiAuthEnv{
		store:     st,
		validator: tv,
		cache:     sc,
		closer:    closer,
		server:    srv,
		userID:    u.ID,
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
		require.Failf(t, "token exchange failed", "%d %s", resp.StatusCode, string(body))
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

func TestAPIAuth_LocalRedirect_ConcurrentExchangeIssuesOneToken(t *testing.T) {
	env := setupAPIAuth(t)
	verifier, challenge := pkceVerifierAndChallenge()
	code := id.Generate()
	require.NoError(t, env.store.CLIAuthorizationCodes().Create(context.Background(), store.CreateCLIAuthorizationCodeParams{
		Code: code, UserID: env.userID, CodeChallenge: challenge, DeviceName: "test", ExpiresAt: time.Now().Add(time.Minute),
	}))
	before, err := env.store.APITokens().ListByUser(context.Background(), store.ListAPITokensByUserParams{UserID: env.userID})
	require.NoError(t, err)

	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	for range 2 {
		go func() {
			defer wg.Done()
			resp, postErr := http.PostForm(env.server.URL+"/auth/cli/token", url.Values{
				"grant_type": {service.GrantTypeAuthorizationCode}, "code": {code}, "code_verifier": {verifier},
			})
			if postErr != nil {
				statuses <- 0
				return
			}
			_ = resp.Body.Close()
			statuses <- resp.StatusCode
		}()
	}
	wg.Wait()
	close(statuses)
	got := make([]int, 0, 2)
	for status := range statuses {
		got = append(got, status)
	}
	assert.ElementsMatch(t, []int{http.StatusOK, http.StatusBadRequest}, got)
	after, err := env.store.APITokens().ListByUser(context.Background(), store.ListAPITokensByUserParams{UserID: env.userID})
	require.NoError(t, err)
	assert.Len(t, after, len(before)+1)
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

	// A failed proof must not burn the authorization code. The legitimate
	// client still holding the verifier must be able to exchange it.
	verifier, challenge := pkceVerifierAndChallenge()
	retryCode := id.Generate()
	require.NoError(t, env.store.CLIAuthorizationCodes().Create(context.Background(), store.CreateCLIAuthorizationCodeParams{
		Code:          retryCode,
		UserID:        env.userID,
		CodeChallenge: challenge,
		DeviceName:    "test",
		ExpiresAt:     time.Now().Add(time.Minute),
	}))
	bad, err := http.PostForm(env.server.URL+"/auth/cli/token", url.Values{
		"grant_type":    {service.GrantTypeAuthorizationCode},
		"code":          {retryCode},
		"code_verifier": {"wrong-verifier"},
	})
	require.NoError(t, err)
	_ = bad.Body.Close()
	require.Equal(t, http.StatusBadRequest, bad.StatusCode)

	good, err := http.PostForm(env.server.URL+"/auth/cli/token", url.Values{
		"grant_type":    {service.GrantTypeAuthorizationCode},
		"code":          {retryCode},
		"code_verifier": {verifier},
	})
	require.NoError(t, err)
	defer func() { _ = good.Body.Close() }()
	assert.Equal(t, http.StatusOK, good.StatusCode)
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
	assert.True(t, env.closer.rescheduled(tokenID),
		"a refresh rotation must extend (reschedule) the bearer's channel expiry, not close it")
	assert.False(t, env.closer.closed(tokenID), "a rotation must not close the bearer's channels")

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

func TestAPIAuth_Refresh_DoesNotPoisonFlightWithCanceledLeaderContext(t *testing.T) {
	env := setupAPIAuth(t)

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

	mux := http.NewServeMux()
	service.NewAPIAuthHandler(env.store, env.validator, auth.NewCredentialLifecycleEffects(env.cache, env.closer, env.closer), env.server.URL).RegisterRoutes(mux)
	form := url.Values{
		"refresh_token": {auth.FormatBearer(auth.BearerKindAPI, tokenID, currentRefresh)},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/auth/cli/refresh", strings.NewReader(form.Encode())).WithContext(ctx)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code,
		"refresh work inside the singleflight must not inherit the leader request cancellation")
	var body map[string]any
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
	assert.NotEmpty(t, body["access_token"])
	assert.NotEmpty(t, body["refresh_token"])
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
	assert.Equal(t, firstBody["access_token"], retryBody["access_token"], "grace retry must return the same derived access token")
	assert.Equal(t, firstBody["refresh_token"], retryBody["refresh_token"], "grace retry must return the same derived refresh token")
}

func TestAPIAuth_Refresh_GraceRetryReportsStoredRemainingLifetime(t *testing.T) {
	env := setupAPIAuth(t)
	tokenID := id.Generate()
	previousRefresh := auth.MintAccessSecret()
	previousHash := env.validator.HashSecret(previousRefresh)
	now := time.Now()
	derived := env.validator.DeriveRefreshBearerPair(
		auth.BearerKindAPI, tokenID, previousHash, now, auth.AccessTokenTTL, auth.RefreshTokenTTL,
	)
	require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID: tokenID, UserID: env.userID, ClientType: "cli", ClientName: "test",
		SecretHash: env.validator.HashSecret(auth.MintAccessSecret()), RefreshHash: previousHash, Scope: "remote:*",
	}))
	storedExpiry := now.Add(10 * time.Second)
	refreshExpiry := now.Add(time.Hour)
	graceExpiry := now.Add(auth.RefreshReuseGrace)
	rotated, err := env.store.APITokens().RotateRefresh(context.Background(), store.RotateAPITokenRefreshParams{
		ID: tokenID, NewSecretHash: derived.AccessHash, NewExpiresAt: &storedExpiry,
		NewRefreshHash: derived.RefreshHash, NewRefreshExpiresAt: &refreshExpiry,
		PreviousRefreshHash: previousHash, PreviousRefreshExpiresAt: &graceExpiry,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), rotated)

	resp, err := http.PostForm(env.server.URL+"/auth/cli/refresh", url.Values{
		"refresh_token": {auth.FormatBearer(auth.BearerKindAPI, tokenID, previousRefresh)},
	})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	expiresIn, ok := body["expires_in"].(float64)
	require.True(t, ok)
	assert.Positive(t, expiresIn)
	assert.LessOrEqual(t, expiresIn, float64(10), "retry must report the stored access-token deadline, not reset the TTL")
}

func TestAPIAuth_Refresh_RetryAcrossHandlersReturnsSamePair(t *testing.T) {
	env := setupAPIAuth(t)
	tokenID := id.Generate()
	previousRefresh := auth.MintAccessSecret()
	require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:          tokenID,
		UserID:      env.userID,
		ClientType:  "cli",
		ClientName:  "test",
		SecretHash:  env.validator.HashSecret(auth.MintAccessSecret()),
		RefreshHash: env.validator.HashSecret(previousRefresh),
		Scope:       "remote:*",
	}))

	first, err := http.PostForm(env.server.URL+"/auth/cli/refresh", url.Values{
		"refresh_token": {auth.FormatBearer(auth.BearerKindAPI, tokenID, previousRefresh)},
	})
	require.NoError(t, err)
	defer func() { _ = first.Body.Close() }()
	require.Equal(t, http.StatusOK, first.StatusCode)
	var firstBody map[string]any
	require.NoError(t, json.NewDecoder(first.Body).Decode(&firstBody))

	// A different Hub shares only durable state and the server pepper.
	otherMux := http.NewServeMux()
	otherServer := httptest.NewServer(otherMux)
	t.Cleanup(otherServer.Close)
	otherValidator, err := auth.NewTokenValidator(env.store, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	service.NewAPIAuthHandler(env.store, otherValidator, auth.NewCredentialLifecycleEffects(env.cache, noopBearerCloser{}, nil), otherServer.URL).RegisterRoutes(otherMux)

	retry, err := http.PostForm(otherServer.URL+"/auth/cli/refresh", url.Values{
		"refresh_token": {auth.FormatBearer(auth.BearerKindAPI, tokenID, previousRefresh)},
	})
	require.NoError(t, err)
	defer func() { _ = retry.Body.Close() }()
	require.Equal(t, http.StatusOK, retry.StatusCode)
	var retryBody map[string]any
	require.NoError(t, json.NewDecoder(retry.Body).Decode(&retryBody))
	assert.Equal(t, firstBody["access_token"], retryBody["access_token"])
	assert.Equal(t, firstBody["refresh_token"], retryBody["refresh_token"])
}

func TestAPIAuth_Refresh_CASMissDoesNotReturnDerivedPairWithoutRotation(t *testing.T) {
	env := setupAPIAuth(t)

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
	wrapped := apiTokenOverrideStore{
		Store: env.store,
		api: apiRotateTokens{
			APITokenStore: env.store.APITokens(),
			rotate: func(context.Context, store.RotateAPITokenRefreshParams) (int64, error) {
				return 0, nil
			},
		},
	}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	service.NewAPIAuthHandler(wrapped, env.validator, auth.NewCredentialLifecycleEffects(env.cache, noopBearerCloser{}, nil), srv.URL).RegisterRoutes(mux)

	resp, err := http.PostForm(srv.URL+"/auth/cli/refresh", url.Values{
		"refresh_token": {auth.FormatBearer(auth.BearerKindAPI, tokenID, currentRefresh)},
	})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "invalid_grant", body["error"])
}

func TestAPIAuth_Refresh_CASRecoveryReportsWinnerRemainingLifetime(t *testing.T) {
	env := setupAPIAuth(t)
	tokenID := id.Generate()
	currentRefresh := auth.MintAccessSecret()
	require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID: tokenID, UserID: env.userID, ClientType: "cli", ClientName: "test",
		SecretHash: env.validator.HashSecret(auth.MintAccessSecret()), RefreshHash: env.validator.HashSecret(currentRefresh), Scope: "remote:*",
	}))
	underlying := env.store.APITokens()
	wrapper := apiTokenOverrideStore{
		Store: env.store,
		api: apiRotateTokens{APITokenStore: underlying, rotate: func(ctx context.Context, p store.RotateAPITokenRefreshParams) (int64, error) {
			winnerExpiry := time.Now().Add(10 * time.Second)
			p.NewExpiresAt = &winnerExpiry
			if _, err := underlying.RotateRefresh(ctx, p); err != nil {
				return 0, err
			}
			return 0, nil
		}},
	}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	service.NewAPIAuthHandler(wrapper, env.validator, auth.NewCredentialLifecycleEffects(env.cache, noopBearerCloser{}, nil), srv.URL).RegisterRoutes(mux)

	resp, err := http.PostForm(srv.URL+"/auth/cli/refresh", url.Values{
		"refresh_token": {auth.FormatBearer(auth.BearerKindAPI, tokenID, currentRefresh)},
	})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	expiresIn, ok := body["expires_in"].(float64)
	require.True(t, ok)
	assert.Positive(t, expiresIn)
	assert.LessOrEqual(t, expiresIn, float64(10), "CAS loser must report the winner's persisted deadline")
}

func TestAPIAuth_Refresh_CASMissAfterRevocationRejectsRefresh(t *testing.T) {
	env := setupAPIAuth(t)

	tokenID := id.Generate()
	currentRefresh := auth.MintAccessSecret()
	currentRefreshHash := env.validator.HashSecret(currentRefresh)
	require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:          tokenID,
		UserID:      env.userID,
		ClientType:  "cli",
		ClientName:  "test",
		SecretHash:  env.validator.HashSecret(auth.MintAccessSecret()),
		RefreshHash: currentRefreshHash,
		Scope:       "remote:*",
	}))
	wrapped := apiTokenOverrideStore{
		Store: env.store,
		api: apiRotateTokens{
			APITokenStore: env.store.APITokens(),
			rotate: func(ctx context.Context, p store.RotateAPITokenRefreshParams) (int64, error) {
				_, err := env.store.APITokens().Revoke(ctx, p.ID)
				return 0, err
			},
		},
	}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	service.NewAPIAuthHandler(wrapped, env.validator, auth.NewCredentialLifecycleEffects(env.cache, noopBearerCloser{}, nil), srv.URL).RegisterRoutes(mux)

	resp, err := http.PostForm(srv.URL+"/auth/cli/refresh", url.Values{
		"refresh_token": {auth.FormatBearer(auth.BearerKindAPI, tokenID, currentRefresh)},
	})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
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
		RefreshHash: env.validator.HashSecret(prev),
		Scope:       "remote:*",
	}))
	_, err := env.store.APITokens().RotateRefresh(context.Background(), store.RotateAPITokenRefreshParams{
		ID:                       tokenID,
		NewSecretHash:            env.validator.HashSecret(auth.MintAccessSecret()),
		NewRefreshHash:           env.validator.HashSecret(cur),
		PreviousRefreshHash:      env.validator.HashSecret(prev),
		PreviousRefreshExpiresAt: &expiredGrace,
	})
	require.NoError(t, err)

	// Reuse the previous refresh: outside the grace window → revoke.
	resp, err := http.PostForm(env.server.URL+"/auth/cli/refresh", url.Values{
		"refresh_token": {auth.FormatBearer(auth.BearerKindAPI, tokenID, prev)},
	})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.True(t, env.closer.closed(tokenID), "refresh reuse must close channels authorized by the compromised bearer")

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

type apiTokenOverrideStore struct {
	store.Store
	api store.APITokenStore
}

func (s apiTokenOverrideStore) APITokens() store.APITokenStore {
	return s.api
}

type apiRotateTokens struct {
	store.APITokenStore
	rotate func(context.Context, store.RotateAPITokenRefreshParams) (int64, error)
}

func (s apiRotateTokens) RotateRefresh(ctx context.Context, p store.RotateAPITokenRefreshParams) (int64, error) {
	return s.rotate(ctx, p)
}

type apiRevokeFailTokens struct {
	store.APITokenStore
}

func (s apiRevokeFailTokens) Revoke(context.Context, string) (int64, error) {
	return 0, errors.New("forced revoke failure")
}

type apiLookupFailTokens struct {
	store.APITokenStore
	err error
}

type deadlineRecordingTokens struct {
	store.APITokenStore
	deadline time.Time
}

func (s *deadlineRecordingTokens) GetByID(ctx context.Context, _ string) (*store.APIToken, error) {
	s.deadline, _ = ctx.Deadline()
	return nil, errors.New("forced lookup failure")
}

type userLookupFailStore struct {
	store.Store
	users store.UserStore
}

type deviceAuthorizationOverrideStore struct {
	store.Store
	device store.DeviceAuthorizationStore
}

func (s deviceAuthorizationOverrideStore) DeviceAuthorizations() store.DeviceAuthorizationStore {
	return s.device
}

func (s deviceAuthorizationOverrideStore) RunInUserAuthTransaction(ctx context.Context, userID string, fn func(store.Store) error) error {
	return s.Store.RunInUserAuthTransaction(ctx, userID, func(tx store.Store) error {
		override := s.device.(deviceAuthorizationOverride)
		return fn(deviceAuthorizationOverrideStore{
			Store: tx,
			device: deviceAuthorizationOverride{
				DeviceAuthorizationStore: tx.DeviceAuthorizations(),
				get:                      override.get,
				touchPoll:                override.touchPoll,
				consume:                  override.consume,
			},
		})
	})
}

type deviceAuthorizationOverride struct {
	store.DeviceAuthorizationStore
	get       func(context.Context, string) (*store.DeviceAuthorization, error)
	touchPoll func(context.Context, string) error
	consume   func(context.Context, string) (int64, error)
}

func (s deviceAuthorizationOverride) Get(ctx context.Context, code string) (*store.DeviceAuthorization, error) {
	if s.get != nil {
		return s.get(ctx, code)
	}
	return s.DeviceAuthorizationStore.Get(ctx, code)
}

func (s deviceAuthorizationOverride) TouchPoll(ctx context.Context, code string) error {
	if s.touchPoll != nil {
		return s.touchPoll(ctx, code)
	}
	return s.DeviceAuthorizationStore.TouchPoll(ctx, code)
}

func (s deviceAuthorizationOverride) Consume(ctx context.Context, code string) (int64, error) {
	if s.consume != nil {
		return s.consume(ctx, code)
	}
	return s.DeviceAuthorizationStore.Consume(ctx, code)
}

func (s userLookupFailStore) Users() store.UserStore { return s.users }

func (s userLookupFailStore) RunInUserAuthTransaction(ctx context.Context, userID string, fn func(store.Store) error) error {
	return s.Store.RunInUserAuthTransaction(ctx, userID, func(tx store.Store) error {
		return fn(userLookupFailStore{Store: tx, users: s.users})
	})
}

type getByIDFailUsers struct {
	store.UserStore
}

func (s getByIDFailUsers) GetByID(context.Context, string) (*store.User, error) {
	return nil, errors.New("forced user lookup failure")
}

func TestAPIAuth_Refresh_DetachedWorkHasDeadline(t *testing.T) {
	env := setupAPIAuth(t)
	recording := &deadlineRecordingTokens{APITokenStore: env.store.APITokens()}
	wrapped := apiTokenOverrideStore{Store: env.store, api: recording}
	validator, err := auth.NewTokenValidator(wrapped, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	mux := http.NewServeMux()
	service.NewAPIAuthHandler(wrapped, validator, auth.NewCredentialLifecycleEffects(env.cache, noopBearerCloser{}, nil), env.server.URL).RegisterRoutes(mux)

	form := url.Values{"refresh_token": {auth.FormatBearer(auth.BearerKindAPI, id.Generate(), auth.MintAccessSecret())}}
	req := httptest.NewRequest(http.MethodPost, "/auth/cli/refresh", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.False(t, recording.deadline.IsZero(), "detached refresh work must carry a replacement deadline")
	remaining := time.Until(recording.deadline)
	assert.Positive(t, remaining)
	assert.LessOrEqual(t, remaining, service.RefreshWorkTimeout)
}

func TestAPIAuth_Token_UserLookupFailureDoesNotLeaveToken(t *testing.T) {
	env := setupAPIAuth(t)
	verifier, challenge := pkceVerifierAndChallenge()
	code := id.Generate()
	require.NoError(t, env.store.CLIAuthorizationCodes().Create(context.Background(), store.CreateCLIAuthorizationCodeParams{
		Code:          code,
		UserID:        env.userID,
		CodeChallenge: challenge,
		DeviceName:    "test",
		ExpiresAt:     time.Now().Add(time.Minute),
	}))
	failing := userLookupFailStore{
		Store: env.store,
		users: getByIDFailUsers{UserStore: env.store.Users()},
	}
	mux := http.NewServeMux()
	service.NewAPIAuthHandler(failing, env.validator, auth.NewCredentialLifecycleEffects(env.cache, noopBearerCloser{}, nil), env.server.URL).RegisterRoutes(mux)

	before, err := env.store.APITokens().ListByUser(context.Background(), store.ListAPITokensByUserParams{UserID: env.userID})
	require.NoError(t, err)
	form := url.Values{
		"grant_type":    {service.GrantTypeAuthorizationCode},
		"code":          {code},
		"code_verifier": {verifier},
	}
	req := httptest.NewRequest(http.MethodPost, "/auth/cli/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusInternalServerError, rec.Code)

	after, err := env.store.APITokens().ListByUser(context.Background(), store.ListAPITokensByUserParams{UserID: env.userID})
	require.NoError(t, err)
	assert.Len(t, after, len(before), "failed issuance must roll back the undisclosed token row")

	retry, err := http.PostForm(env.server.URL+"/auth/cli/token", form)
	require.NoError(t, err)
	defer func() { _ = retry.Body.Close() }()
	assert.Equal(t, http.StatusOK, retry.StatusCode, "failed issuance must leave the authorization code retryable")
}

func TestAPIAuth_DeviceCode_UserLookupFailureLeavesGrantRetryable(t *testing.T) {
	env := setupAPIAuth(t)
	deviceCode := id.Generate()
	require.NoError(t, env.store.DeviceAuthorizations().Create(context.Background(), store.CreateDeviceAuthorizationParams{
		DeviceCode: deviceCode, UserCode: verifycode.Generate(), DeviceName: "test", ExpiresAt: time.Now().Add(time.Minute),
	}))
	rows, err := env.store.DeviceAuthorizations().Approve(context.Background(), store.ApproveDeviceAuthorizationParams{
		DeviceCode: deviceCode, UserID: env.userID,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)

	failing := userLookupFailStore{Store: env.store, users: getByIDFailUsers{UserStore: env.store.Users()}}
	mux := http.NewServeMux()
	service.NewAPIAuthHandler(failing, env.validator, auth.NewCredentialLifecycleEffects(env.cache, noopBearerCloser{}, nil), env.server.URL).RegisterRoutes(mux)
	form := url.Values{"grant_type": {service.GrantTypeDeviceCode}, "device_code": {deviceCode}}
	req := httptest.NewRequest(http.MethodPost, "/auth/cli/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, "internal server error\n", rec.Body.String())

	// The failed issuance rolled back Consume, so the grant is still
	// unconsumed and exchangeable. Its poll was recorded regardless
	// (TouchPoll now runs outside the issuance transaction), so an
	// immediate retry is correctly throttled with slow_down; wait past
	// the interval, then confirm a clean retry succeeds -- proving the
	// grant stayed retryable.
	time.Sleep(service.DeviceCodePollInterval + 100*time.Millisecond)
	retry, err := http.PostForm(env.server.URL+"/auth/cli/token", form)
	require.NoError(t, err)
	defer func() { _ = retry.Body.Close() }()
	assert.Equal(t, http.StatusOK, retry.StatusCode, "failed issuance must leave the device grant retryable")
}

func TestAPIAuth_DeviceCode_TouchPollFailureIsInternal(t *testing.T) {
	env := setupAPIAuth(t)
	deviceCode := id.Generate()
	require.NoError(t, env.store.DeviceAuthorizations().Create(context.Background(), store.CreateDeviceAuthorizationParams{
		DeviceCode: deviceCode, UserCode: verifycode.Generate(), ExpiresAt: time.Now().Add(time.Minute),
	}))
	forcedErr := errors.New("sensitive poll failure")
	device := deviceAuthorizationOverride{DeviceAuthorizationStore: env.store.DeviceAuthorizations(), touchPoll: func(context.Context, string) error {
		return forcedErr
	}}
	wrapped := deviceAuthorizationOverrideStore{Store: env.store, device: device}
	mux := http.NewServeMux()
	service.NewAPIAuthHandler(wrapped, env.validator, auth.NewCredentialLifecycleEffects(env.cache, noopBearerCloser{}, nil), env.server.URL).RegisterRoutes(mux)
	form := url.Values{"grant_type": {service.GrantTypeDeviceCode}, "device_code": {deviceCode}}
	req := httptest.NewRequest(http.MethodPost, "/auth/cli/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, "internal server error\n", rec.Body.String())
	assert.NotContains(t, rec.Body.String(), forcedErr.Error())
}

func TestAPIAuth_DeviceCode_LookupFailureIsInternal(t *testing.T) {
	env := setupAPIAuth(t)
	forcedErr := errors.New("sensitive device lookup failure")
	device := deviceAuthorizationOverride{DeviceAuthorizationStore: env.store.DeviceAuthorizations(), get: func(context.Context, string) (*store.DeviceAuthorization, error) {
		return nil, forcedErr
	}}
	wrapped := deviceAuthorizationOverrideStore{Store: env.store, device: device}
	mux := http.NewServeMux()
	service.NewAPIAuthHandler(wrapped, env.validator, auth.NewCredentialLifecycleEffects(env.cache, noopBearerCloser{}, nil), env.server.URL).RegisterRoutes(mux)
	form := url.Values{"grant_type": {service.GrantTypeDeviceCode}, "device_code": {"test-device-code"}}
	req := httptest.NewRequest(http.MethodPost, "/auth/cli/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, "internal server error\n", rec.Body.String())
	assert.NotContains(t, rec.Body.String(), forcedErr.Error())
}

func TestAPIAuth_DeviceCode_ConsumeRequiresOneRow(t *testing.T) {
	env := setupAPIAuth(t)
	deviceCode := id.Generate()
	require.NoError(t, env.store.DeviceAuthorizations().Create(context.Background(), store.CreateDeviceAuthorizationParams{
		DeviceCode: deviceCode, UserCode: verifycode.Generate(), ExpiresAt: time.Now().Add(time.Minute),
	}))
	rows, err := env.store.DeviceAuthorizations().Approve(context.Background(), store.ApproveDeviceAuthorizationParams{
		DeviceCode: deviceCode, UserID: env.userID,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
	device := deviceAuthorizationOverride{DeviceAuthorizationStore: env.store.DeviceAuthorizations(), consume: func(context.Context, string) (int64, error) {
		return 0, nil
	}}
	wrapped := deviceAuthorizationOverrideStore{Store: env.store, device: device}
	mux := http.NewServeMux()
	service.NewAPIAuthHandler(wrapped, env.validator, auth.NewCredentialLifecycleEffects(env.cache, noopBearerCloser{}, nil), env.server.URL).RegisterRoutes(mux)
	form := url.Values{"grant_type": {service.GrantTypeDeviceCode}, "device_code": {deviceCode}}
	req := httptest.NewRequest(http.MethodPost, "/auth/cli/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var body map[string]any
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
	assert.Equal(t, "invalid_grant", body["error"])
}

// TestAPIAuth_DeviceCode_ApprovedPollAdvancesThrottleDespiteIssuanceFailure
// locks in the throttle contract: an approved poll advances last_polled_at
// even when issuance fails transiently and rolls back, so a client hammering
// an approved-but-failing grant still gets slow_down. This only holds because
// TouchPoll runs outside the issuance transaction; if it ran inside, the
// rollback would discard the anchor and the rapid re-poll would retry issuance
// instead of being throttled.
func TestAPIAuth_DeviceCode_ApprovedPollAdvancesThrottleDespiteIssuanceFailure(t *testing.T) {
	env := setupAPIAuth(t)
	deviceCode := id.Generate()
	require.NoError(t, env.store.DeviceAuthorizations().Create(context.Background(), store.CreateDeviceAuthorizationParams{
		DeviceCode: deviceCode, UserCode: verifycode.Generate(), IntervalSeconds: 5, ExpiresAt: time.Now().Add(time.Minute),
	}))
	rows, err := env.store.DeviceAuthorizations().Approve(context.Background(), store.ApproveDeviceAuthorizationParams{
		DeviceCode: deviceCode, UserID: env.userID,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)

	// Inject a transient (non-terminal) store error inside the issuance
	// transaction so token creation rolls back. Consume runs inside the
	// transaction; TouchPoll runs outside it, so last_polled_at must survive.
	device := deviceAuthorizationOverride{DeviceAuthorizationStore: env.store.DeviceAuthorizations(), consume: func(context.Context, string) (int64, error) {
		return 0, errors.New("transient consume failure")
	}}
	wrapped := deviceAuthorizationOverrideStore{Store: env.store, device: device}
	mux := http.NewServeMux()
	service.NewAPIAuthHandler(wrapped, env.validator, auth.NewCredentialLifecycleEffects(env.cache, noopBearerCloser{}, nil), env.server.URL).RegisterRoutes(mux)
	form := url.Values{"grant_type": {service.GrantTypeDeviceCode}, "device_code": {deviceCode}}

	// First poll: issuance fails transiently (500), but the throttle anchor
	// must have advanced regardless, and the grant must stay retryable.
	req := httptest.NewRequest(http.MethodPost, "/auth/cli/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusInternalServerError, rec.Code)

	afterRow, err := env.store.DeviceAuthorizations().Get(context.Background(), deviceCode)
	require.NoError(t, err)
	require.NotNil(t, afterRow.LastPolledAt, "failed issuance must still advance last_polled_at")
	require.Nil(t, afterRow.ConsumedAt, "rolled-back issuance must leave the grant retryable")

	// Immediate second poll: within the interval window, so the advanced
	// anchor throttles it with slow_down instead of re-attempting issuance.
	req2 := httptest.NewRequest(http.MethodPost, "/auth/cli/token", strings.NewReader(form.Encode()))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	require.Equal(t, http.StatusBadRequest, rec2.Code)
	var body map[string]any
	require.NoError(t, json.NewDecoder(rec2.Body).Decode(&body))
	assert.Equal(t, "slow_down", body["error"], "rapid re-poll of a transiently-failing approved grant must be throttled")
}

func TestAPIAuth_Revoke_AcceptsRefreshSecrets(t *testing.T) {
	for _, previous := range []bool{false, true} {
		name := "current"
		if previous {
			name = "previous"
		}
		t.Run(name, func(t *testing.T) {
			env := setupAPIAuth(t)
			tokenID := id.Generate()
			refreshSecret := auth.MintAccessSecret()
			require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
				ID:          tokenID,
				UserID:      env.userID,
				ClientType:  "cli",
				ClientName:  "test",
				SecretHash:  env.validator.HashSecret(auth.MintAccessSecret()),
				RefreshHash: env.validator.HashSecret(refreshSecret),
				Scope:       "remote:*",
			}))
			if previous {
				currentRefresh := auth.MintAccessSecret()
				graceExpiry := time.Now().Add(auth.RefreshReuseGrace)
				_, err := env.store.APITokens().RotateRefresh(context.Background(), store.RotateAPITokenRefreshParams{
					ID:                       tokenID,
					NewSecretHash:            env.validator.HashSecret(auth.MintAccessSecret()),
					NewRefreshHash:           env.validator.HashSecret(currentRefresh),
					PreviousRefreshHash:      env.validator.HashSecret(refreshSecret),
					PreviousRefreshExpiresAt: &graceExpiry,
				})
				require.NoError(t, err)
			}

			resp, err := http.PostForm(env.server.URL+"/auth/cli/revoke", url.Values{
				"token": {auth.FormatBearer(auth.BearerKindAPI, tokenID, refreshSecret)},
			})
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			require.Equal(t, http.StatusOK, resp.StatusCode)
			row, err := env.store.APITokens().GetByID(context.Background(), tokenID)
			require.NoError(t, err)
			assert.NotNil(t, row.RevokedAt)
		})
	}
}

func (s apiLookupFailTokens) GetByID(context.Context, string) (*store.APIToken, error) {
	return nil, s.err
}

func TestAPIAuth_Refresh_InternalFailureDoesNotLeakDetails(t *testing.T) {
	env := setupAPIAuth(t)
	wrapped := apiTokenOverrideStore{
		Store: env.store,
		api: apiLookupFailTokens{
			APITokenStore: env.store.APITokens(),
			err:           errors.New("sensitive database failure"),
		},
	}
	validator, err := auth.NewTokenValidator(wrapped, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	mux := http.NewServeMux()
	service.NewAPIAuthHandler(wrapped, validator, auth.NewCredentialLifecycleEffects(env.cache, noopBearerCloser{}, nil), env.server.URL).RegisterRoutes(mux)

	form := url.Values{"refresh_token": {
		auth.FormatBearer(auth.BearerKindAPI, id.Generate(), auth.MintAccessSecret()),
	}}
	req := httptest.NewRequest(http.MethodPost, "/auth/cli/refresh", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, "internal server error\n", rec.Body.String())
}

func TestAPIAuth_Revoke_StoreFailureReturnsServerError(t *testing.T) {
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

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	wrapped := apiTokenOverrideStore{
		Store: env.store,
		api:   apiRevokeFailTokens{APITokenStore: env.store.APITokens()},
	}
	service.NewAPIAuthHandler(wrapped, env.validator, auth.NewCredentialLifecycleEffects(env.cache, noopBearerCloser{}, nil), srv.URL).RegisterRoutes(mux)

	resp, err := http.PostForm(srv.URL+"/auth/cli/revoke", url.Values{"token": {bearer}})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "internal server error\n", string(body))

	row, err := env.store.APITokens().GetByID(context.Background(), tokenID)
	require.NoError(t, err)
	assert.Nil(t, row.RevokedAt, "failed revoke must not be reported as success")
}

func TestAPIAuth_Revoke_VerifyLookupFailureReturnsServerError(t *testing.T) {
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

	wrapped := apiTokenOverrideStore{
		Store: env.store,
		api: apiLookupFailTokens{
			APITokenStore: env.store.APITokens(),
			err:           errors.New("forced lookup failure"),
		},
	}
	validator, err := auth.NewTokenValidator(wrapped, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	service.NewAPIAuthHandler(wrapped, validator, auth.NewCredentialLifecycleEffects(env.cache, noopBearerCloser{}, nil), srv.URL).RegisterRoutes(mux)

	resp, err := http.PostForm(srv.URL+"/auth/cli/revoke", url.Values{"token": {bearer}})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "internal server error\n", string(body))

	row, err := env.store.APITokens().GetByID(context.Background(), tokenID)
	require.NoError(t, err)
	assert.Nil(t, row.RevokedAt, "failed verification lookup must not revoke or be reported as invalid token")
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
