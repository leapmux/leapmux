package service_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hub/ratelimit"
	"github.com/leapmux/leapmux/internal/util/userid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/servicetest"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/verifycode"
)

// apiAuthEnv wires the OAuthServerHandler against an in-memory store with
// the bootstrap admin already provisioned, plus the token validator and
// session cache the handler depends on.
type apiAuthEnv struct {
	store     store.Store
	validator *auth.TokenValidator
	cache     *auth.AuthContextRegistry
	closer    *recordingBearerCloser
	server    *httptest.Server
	userID    string
	// now is the handler clock every handler this env builds installs, and the
	// ONE instant the tests read. Sharing one func is what keeps a second
	// handler on the same notion of time as the first, which the step-up and
	// activation cases both depend on.
	//
	// The mock clock behind it is not a field on purpose. It reads a FROZEN
	// instant, so a row a test stamped from it and a deadline the handler
	// derived from now would disagree by the test's own runtime -- and every
	// site that reached for the wrong one compiled.
	now func() time.Time
	// advance moves the offset both now and the handler read.
	advance func(time.Duration)
	// set is the hub's own settings, wired exactly as production wires them.
	// secure_cookies is the one key the handler reads from it, and it decides
	// which session-cookie spelling the consent legs accept.
	set *settings.Manager
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

func TestNewOAuthServerHandlerRequiresCredentialLifecycleEffects(t *testing.T) {
	t.Parallel()

	require.Panics(t, func() {
		service.NewOAuthServerHandler(service.OAuthServerDeps{HubURL: func() string { return "" }})
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

	// The handler needs AuthContextRegistry to evict revoked bearers; we
	// don't run it through the interceptor, so just construct the bare
	// interceptor for its cache side-effect and stop the sweeper.
	_, sc := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st})
	t.Cleanup(sc.Stop)

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	closer := &recordingBearerCloser{}
	clock := quartz.NewMock(t).WithLogger(quartz.NoOpLogger)
	clock.Set(time.Now())
	// The handler reads real time PLUS the offset the test advanced. It never
	// reads a frozen instant. It mints deadlines that assertions compare against
	// limits the test derives from time.Now(). A clock stopped at setup drifts
	// out of those limits by the test's own runtime.
	// TestAPIAuth_Refresh_GraceRetryReportsStoredRemainingLifetime shows the
	// cost: remainingExpiresIn rounds the remaining seconds UP, so a handler
	// clock behind real time by any amount reports 11 where the test allows 10.
	//
	// An advance moves the offset. Each advance in these files steps past the
	// device-code slow_down window or the elevation window without a sleep.
	base := clock.Now()
	now := func() time.Time { return time.Now().Add(clock.Now().Sub(base)) }
	advance := func(d time.Duration) { clock.Advance(d).MustWait(t.Context()) }
	set := servicetest.NewSettingsManager(t, st, nil)
	h := service.NewOAuthServerHandler(service.OAuthServerDeps{
		Store:     st,
		Validator: tv,
		Lifecycle: auth.NewCredentialLifecycleEffects(sc, closer, closer),
		Settings:  set,
		HubURL:    func() string { return srv.URL },
	})
	h.Now = now
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
		now:       now,
		advance:   advance,
		set:       set,
	}
}

// adminCookie logs in as admin via the bootstrap fixture so handlers
// that depend on `requireSession` see an authenticated browser session.
func (e *apiAuthEnv) adminCookie(t *testing.T) *http.Cookie {
	t.Helper()
	tok, _, _, err := auth.Login(context.Background(), e.store, hubtestutil.TestAdminUsername, hubtestutil.TestAdminPassword, auth.DefaultSessionDuration)
	require.NoError(t, err)
	return &http.Cookie{Name: auth.CookieName, Value: tok}
}

// elevate stamps a live elevation window on the session the cookie specifies,
// through the REAL store write the RPCs use, so the dialect's own mapping of
// the two columns is exercised rather than faked.
//
// Every instant comes from e.now, the same seam the handler reads, so a test
// that advances the clock past the window sees the gate close.
func (e *apiAuthEnv) elevate(t *testing.T, cookie *http.Cookie) {
	t.Helper()
	now := e.now()
	n, err := e.store.Sessions().Elevate(context.Background(), store.ElevateSessionParams{
		SessionID:          cookie.Value,
		UserID:             userid.MustNew(e.userID),
		ElevationProvenAt:  now,
		ElevationExpiresAt: now.Add(auth.ElevationWindow),
	}, now)
	require.NoError(t, err)
	require.EqualValues(t, 1, n, "the session must exist and be live to elevate")
}

// elevatedAdminCookie is what the CLI consent legs need: a browser session
// that proved a factor. The plain adminCookie is deliberately kept for
// the tests that exercise the gate itself.
func (e *apiAuthEnv) elevatedAdminCookie(t *testing.T) *http.Cookie {
	t.Helper()
	cookie := e.adminCookie(t)
	e.elevate(t, cookie)
	return cookie
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
	t.Parallel()

	env := setupAPIAuth(t)
	cookie := env.elevatedAdminCookie(t)

	verifier, challenge := pkceVerifierAndChallenge()
	state := id.Generate()
	redirect := "http://127.0.0.1:54321/callback"

	// /oauth/authorize renders the consent page when the session is valid.
	startURL := env.server.URL + "/oauth/authorize?" + url.Values{"client_id": {oauthapp.ControlCLIClientID},
		"response_type":         {"code"},
		"code_challenge_method": {"S256"},
		"redirect_uri":          {redirect},
		"state":                 {state},
		"code_challenge":        {challenge},
		"decision":              {"allow"},
		"installation_name":     {"laptop"},
	}.Encode()
	req, _ := http.NewRequest(http.MethodGet, startURL, nil)
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// /oauth/consent POST issues the one-shot code and redirects to
	// the loopback URL.
	authClient := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	authURL := env.server.URL + "/oauth/consent"
	form := url.Values{"client_id": {oauthapp.ControlCLIClientID},
		"response_type":         {"code"},
		"code_challenge_method": {"S256"},
		"redirect_uri":          {redirect},
		"state":                 {state},
		"code_challenge":        {challenge},
		"decision":              {"allow"},
		"installation_name":     {"laptop"},
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
		"grant_type":        {"authorization_code"},
		"client_id":         {oauthapp.ControlCLIClientID},
		"redirect_uri":      {"http://127.0.0.1:54321/callback"},
		"code":              {gotCode},
		"code_verifier":     {verifier},
		"installation_name": {"laptop"},
	}
	resp, err = http.PostForm(env.server.URL+"/oauth/token", tokenForm)
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
	assert.Equal(t, env.userID, info.ID.String())
}

func TestAPIAuth_LocalRedirect_RejectsNonLoopback(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	cookie := env.elevatedAdminCookie(t)

	_, challenge := pkceVerifierAndChallenge()
	form := url.Values{"client_id": {oauthapp.ControlCLIClientID},
		"response_type":         {"code"},
		"code_challenge_method": {"S256"},
		"redirect_uri":          {"https://attacker.example/callback"},
		"state":                 {"x"},
		"code_challenge":        {challenge},
		"decision":              {"allow"},
	}
	req, _ := http.NewRequest(http.MethodPost, env.server.URL+"/oauth/consent", strings.NewReader(form.Encode()))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestAPIAuth_LocalRedirect_NotAuthenticated_RedirectsToLogin(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	authClient := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := authClient.Get(env.server.URL + "/oauth/authorize?redirect_uri=http://127.0.0.1:1/x&state=s&code_challenge=c")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusFound, resp.StatusCode)
	loc := resp.Header.Get("Location")
	assert.True(t, strings.HasPrefix(loc, "/login?"), "expected redirect to /login, got %q", loc)
}

func TestAPIAuth_LocalRedirect_RejectsCodeReplay(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	cookie := env.elevatedAdminCookie(t)

	verifier, challenge := pkceVerifierAndChallenge()
	state := id.Generate()
	redirect := "http://127.0.0.1:54321/callback"

	// Issue the one-shot code.
	authClient := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	form := url.Values{"client_id": {oauthapp.ControlCLIClientID},
		"response_type":         {"code"},
		"code_challenge_method": {"S256"},
		"redirect_uri":          {redirect},
		"state":                 {state},
		"code_challenge":        {challenge},
		"decision":              {"allow"},
	}
	req, _ := http.NewRequest(http.MethodPost, env.server.URL+"/oauth/consent", strings.NewReader(form.Encode()))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := authClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	loc, _ := url.Parse(resp.Header.Get("Location"))
	code := loc.Query().Get("code")

	exchange := func() (int, map[string]any) {
		r, err := http.PostForm(env.server.URL+"/oauth/token", url.Values{
			"grant_type":    {"authorization_code"},
			"client_id":     {oauthapp.ControlCLIClientID},
			"redirect_uri":  {"http://127.0.0.1:54321/callback"},
			"code":          {code},
			"code_verifier": {verifier},
		})
		require.NoError(t, err)
		defer func() { _ = r.Body.Close() }()
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		return r.StatusCode, body
	}
	st1, body1 := exchange()
	require.Equal(t, http.StatusOK, st1)
	minted, _ := body1["token_id"].(string)
	require.NotEmpty(t, minted)

	// Replaying the code must fail.
	st2, body2 := exchange()
	assert.Equal(t, http.StatusBadRequest, st2)
	assert.Equal(t, "invalid_grant", body2["error"])

	// AND the credential the FIRST exchange minted is revoked, which is RFC
	// 6749 section 4.1.2's remedy. A second presentation means the code
	// reached somebody who should not hold it, so what it already produced is
	// presumed compromised -- and refusing the replay alone would leave the
	// attacker's real prize, the credential from the first exchange, live.
	row, err := env.store.APITokens().GetByID(context.Background(), minted)
	require.NoError(t, err)
	assert.NotNil(t, row.RevokedAt, "the replay must revoke the credential the code already minted")
	assert.True(t, env.closer.closed(minted),
		"the revocation must also close the channels that credential opened")
}

func TestAPIAuth_LocalRedirect_ConcurrentExchangeIssuesOneToken(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	verifier, challenge := pkceVerifierAndChallenge()
	code := id.Generate()
	require.NoError(t, env.store.OAuthAuthorizationCodes().Create(context.Background(), store.CreateOAuthAuthorizationCodeParams{
		ClientID:      oauthapp.ControlCLIClientID,
		GrantedScopes: authscope.NonAdminGrant().String(),
		Code:          code, UserID: userid.MustNew(env.userID), CodeChallenge: challenge, InstallationName: "test", ExpiresAt: time.Now().Add(time.Minute),
		RedirectURI: "http://127.0.0.1:54321/callback",
	}))
	before, err := env.store.OAuthClients().CountTokens(context.Background(), oauthapp.ControlCLIClientID)
	require.NoError(t, err)

	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	for range 2 {
		go func() {
			defer wg.Done()
			resp, postErr := http.PostForm(env.server.URL+"/oauth/token", url.Values{
				"grant_type":   {service.GrantTypeAuthorizationCode},
				"client_id":    {oauthapp.ControlCLIClientID},
				"redirect_uri": {"http://127.0.0.1:54321/callback"}, "code": {code}, "code_verifier": {verifier},
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

	// EXACTLY ONE credential row was ever created from the one code, counted
	// with revoked rows included.
	//
	// Whether it SURVIVES depends on the interleaving, and both outcomes are
	// correct. If the loser reaches the endpoint before the winner commits, it
	// finds the code still active and its own consume matches no row: a plain
	// refusal. If it arrives after, the code is gone and RFC 6749 section 4.1.2
	// applies -- a code used twice means it leaked, so the credential the first
	// exchange minted is revoked. Asserting a LIVE token here made the test
	// depend on which of the two happened.
	//
	// TestAPIAuth_LocalRedirect_RejectsCodeReplay covers the sequential replay,
	// where the revocation is the whole subject and is deterministic.
	after, err := env.store.OAuthClients().CountTokens(context.Background(), oauthapp.ControlCLIClientID)
	require.NoError(t, err)
	assert.Equal(t, before+1, after, "one code mints one credential, whoever wins the race")
}

func TestAPIAuth_LocalRedirect_RejectsBadVerifier(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	cookie := env.elevatedAdminCookie(t)

	_, challenge := pkceVerifierAndChallenge()
	state := id.Generate()
	redirect := "http://127.0.0.1:54321/callback"

	authClient := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	form := url.Values{"client_id": {oauthapp.ControlCLIClientID},
		"response_type":         {"code"},
		"code_challenge_method": {"S256"},
		"redirect_uri":          {redirect},
		"state":                 {state},
		"code_challenge":        {challenge},
		"decision":              {"allow"},
	}
	req, _ := http.NewRequest(http.MethodPost, env.server.URL+"/oauth/consent", strings.NewReader(form.Encode()))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := authClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	loc, _ := url.Parse(resp.Header.Get("Location"))
	code := loc.Query().Get("code")

	// Wrong verifier — handler computes S256(verifier) and compares to
	// the stored code_challenge.
	resp, err = http.PostForm(env.server.URL+"/oauth/token", url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {oauthapp.ControlCLIClientID},
		"redirect_uri":  {"http://127.0.0.1:54321/callback"},
		"code":          {code},
		"code_verifier": {"definitely-not-the-verifier-but-long-enough-to-pass-shape-checks"},
	})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	assert.Equal(t, "invalid_grant", body["error"])

	// A failed proof must not consume the authorization code. The legitimate
	// client still holding the verifier must be able to exchange it.
	verifier, challenge := pkceVerifierAndChallenge()
	retryCode := id.Generate()
	require.NoError(t, env.store.OAuthAuthorizationCodes().Create(context.Background(), store.CreateOAuthAuthorizationCodeParams{
		ClientID:         oauthapp.ControlCLIClientID,
		GrantedScopes:    authscope.NonAdminGrant().String(),
		Code:             retryCode,
		UserID:           userid.MustNew(env.userID),
		CodeChallenge:    challenge,
		InstallationName: "test",
		ExpiresAt:        time.Now().Add(time.Minute),
		RedirectURI:      "http://127.0.0.1:54321/callback",
	}))
	bad, err := http.PostForm(env.server.URL+"/oauth/token", url.Values{
		"grant_type":    {service.GrantTypeAuthorizationCode},
		"client_id":     {oauthapp.ControlCLIClientID},
		"redirect_uri":  {"http://127.0.0.1:54321/callback"},
		"code":          {retryCode},
		"code_verifier": {"wrong-verifier-but-long-enough-to-pass-the-shape-checks-ok"},
	})
	require.NoError(t, err)
	_ = bad.Body.Close()
	require.Equal(t, http.StatusBadRequest, bad.StatusCode)

	good, err := http.PostForm(env.server.URL+"/oauth/token", url.Values{
		"grant_type":    {service.GrantTypeAuthorizationCode},
		"client_id":     {oauthapp.ControlCLIClientID},
		"redirect_uri":  {"http://127.0.0.1:54321/callback"},
		"code":          {retryCode},
		"code_verifier": {verifier},
	})
	require.NoError(t, err)
	defer func() { _ = good.Body.Close() }()
	assert.Equal(t, http.StatusOK, good.StatusCode)
}

func TestAPIAuth_DeviceCode_Pending_Approval_Success(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	cookie := env.elevatedAdminCookie(t)

	// Start device authorization.
	deviceCode, userCode := startDeviceAuthorization(t, env, url.Values{"installation_name": {"server-1"}})
	assert.Contains(t, userCode, "-", "user_code should be display-formatted with a hyphen")

	// First poll: still pending.
	pollResp, err := http.PostForm(env.server.URL+"/oauth/token", url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"client_id":   {oauthapp.ControlCLIClientID},
		"device_code": {deviceCode},
	})
	require.NoError(t, err)
	defer func() { _ = pollResp.Body.Close() }()
	require.Equal(t, http.StatusBadRequest, pollResp.StatusCode)
	var pendingBody map[string]any
	_ = json.NewDecoder(pollResp.Body).Decode(&pendingBody)
	assert.Equal(t, "authorization_pending", pendingBody["error"])

	// User approves via the activation page.
	approveResp, err := postForm(env.server.URL+"/oauth/device", url.Values{
		"user_code": {userCode}, "decision": {"allow"},
	}, cookie)
	require.NoError(t, err)
	defer func() { _ = approveResp.Body.Close() }()
	require.Equal(t, http.StatusOK, approveResp.StatusCode)

	// Step past the throttle window since the previous poll.
	env.advance(service.DeviceCodePollInterval + 100*time.Millisecond)

	// Successful exchange.
	successResp, err := http.PostForm(env.server.URL+"/oauth/token", url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"client_id":   {oauthapp.ControlCLIClientID},
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
	assert.Equal(t, env.userID, info.ID.String())
}

func TestAPIAuth_DeviceCode_SlowDown_OnRapidPoll(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)

	deviceCode, _ := startDeviceAuthorization(t, env, nil)

	// First poll establishes the LastPolledAt anchor.
	r1, err := http.PostForm(env.server.URL+"/oauth/token", url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"client_id":   {oauthapp.ControlCLIClientID},
		"device_code": {deviceCode},
	})
	require.NoError(t, err)
	_ = r1.Body.Close()

	// Immediate second poll — within `interval`, so server replies slow_down.
	r2, err := http.PostForm(env.server.URL+"/oauth/token", url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"client_id":   {oauthapp.ControlCLIClientID},
		"device_code": {deviceCode},
	})
	require.NoError(t, err)
	defer func() { _ = r2.Body.Close() }()
	var body map[string]any
	require.NoError(t, json.NewDecoder(r2.Body).Decode(&body))
	assert.Equal(t, "slow_down", body["error"], "rapid poll should be throttled")
}

func TestAPIAuth_DeviceCode_ExpiredToken(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)

	// Manually seed an expired grant directly via the store so we don't
	// have to wait DeviceCodeTTL in the test.
	dc := id.Generate()
	uc := verifycode.Generate()
	require.NoError(t, env.store.DeviceAuthorizations().Create(context.Background(), store.CreateDeviceAuthorizationParams{
		ClientID:        oauthapp.ControlCLIClientID,
		DeviceCode:      dc,
		UserCode:        uc,
		IntervalSeconds: 5,
		ExpiresAt:       time.Now().Add(-time.Minute),
	}))
	resp, err := http.PostForm(env.server.URL+"/oauth/token", url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"client_id":   {oauthapp.ControlCLIClientID},
		"device_code": {dc},
	})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "expired_token", body["error"])
}

func TestAPIAuth_DeviceCode_AccessDenied(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)

	dc := id.Generate()
	uc := verifycode.Generate()
	require.NoError(t, env.store.DeviceAuthorizations().Create(context.Background(), store.CreateDeviceAuthorizationParams{
		ClientID:        oauthapp.ControlCLIClientID,
		DeviceCode:      dc,
		UserCode:        uc,
		IntervalSeconds: 0, // disable throttle for this test
		ExpiresAt:       time.Now().Add(time.Hour),
	}))

	// Manually mark the row as denied (Approved=2).
	_, err := env.store.DeviceAuthorizations().DenyByUserCode(context.Background(), uc)
	require.NoError(t, err)

	resp, err := http.PostForm(env.server.URL+"/oauth/token", url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"client_id":   {oauthapp.ControlCLIClientID},
		"device_code": {dc},
	})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "access_denied", body["error"])
}

func TestAPIAuth_DeviceCode_UnknownDeviceCode(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	resp, err := http.PostForm(env.server.URL+"/oauth/token", url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"client_id":   {oauthapp.ControlCLIClientID},
		"device_code": {"never-existed"},
	})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "invalid_grant", body["error"])
}

func TestAPIAuth_DeviceCode_AlreadyConsumed(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	cookie := env.elevatedAdminCookie(t)

	deviceCode, userCode := startDeviceAuthorization(t, env, nil)

	// Approve.
	approve, err := postForm(env.server.URL+"/oauth/device", url.Values{"user_code": {userCode}, "decision": {"allow"}}, cookie)
	require.NoError(t, err)
	_ = approve.Body.Close()

	// Step past the throttle, then exchange -- should succeed.
	env.advance(service.DeviceCodePollInterval + 100*time.Millisecond)
	first, err := http.PostForm(env.server.URL+"/oauth/token", url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"client_id":   {oauthapp.ControlCLIClientID},
		"device_code": {deviceCode},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, first.StatusCode)
	_ = first.Body.Close()

	// Replay the same device_code — must be rejected.
	second, err := http.PostForm(env.server.URL+"/oauth/token", url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"client_id":   {oauthapp.ControlCLIClientID},
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
	t.Parallel()

	env := setupAPIAuth(t)
	cookie := env.elevatedAdminCookie(t)

	_, displayCode := startDeviceAuthorization(t, env, nil) // e.g. "ABC-DEF"

	// Submit the lowercased + extra-whitespace form; verifycode.Normalize
	// must accept it.
	noisy := strings.ToLower(" " + displayCode + " ")
	r, err := postForm(env.server.URL+"/oauth/device", url.Values{"user_code": {noisy}, "decision": {"allow"}}, cookie)
	require.NoError(t, err)
	defer func() { _ = r.Body.Close() }()
	assert.Equal(t, http.StatusOK, r.StatusCode)
}

func TestAPIAuth_Activate_RejectsUnknownCode(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	cookie := env.elevatedAdminCookie(t)
	r, err := postForm(env.server.URL+"/oauth/device", url.Values{"user_code": {"ABC-DEF"}, "decision": {"allow"}}, cookie)
	require.NoError(t, err)
	defer func() { _ = r.Body.Close() }()
	assert.Equal(t, http.StatusBadRequest, r.StatusCode)
}

func TestAPIAuth_Refresh_RotatesAndReturnsNewPair(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)

	// Mint an api_token directly so we don't have to traverse the full
	// consent flow for every refresh test.
	tokenID := id.Generate()
	currentRefresh := auth.MintAccessSecret()
	require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:               tokenID,
		UserID:           userid.MustNew(env.userID),
		ClientID:         oauthapp.ControlCLIClientID,
		InstallationName: "test",
		GrantedScopes:    authscope.NonAdminGrant().String(),
		SecretHash:       env.validator.HashSecret(auth.MintAccessSecret()),
		RefreshHash:      env.validator.HashSecret(currentRefresh),
	}))

	resp, err := http.PostForm(env.server.URL+"/oauth/token", url.Values{
		"grant_type":    {service.GrantTypeRefreshToken},
		"client_id":     {oauthapp.ControlCLIClientID},
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
	// bearer never works.
	info, err := env.validator.ValidateBearer(context.Background(), access)
	require.NoError(t, err, "rotated access bearer must validate")
	assert.Equal(t, env.userID, info.ID.String())

	// The rotated refresh bearer must still be usable for a subsequent
	// refresh — i.e. it both validates and survives the rotation chain.
	resp2, err := http.PostForm(env.server.URL+"/oauth/token", url.Values{
		"grant_type":    {service.GrantTypeRefreshToken},
		"client_id":     {oauthapp.ControlCLIClientID},
		"refresh_token": {refresh},
	})
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()
	require.Equal(t, http.StatusOK, resp2.StatusCode, "second refresh on rotated pair must succeed")
}

func TestAPIAuth_Refresh_DoesNotPoisonFlightWithCanceledLeaderContext(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)

	tokenID := id.Generate()
	currentRefresh := auth.MintAccessSecret()
	require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:               tokenID,
		UserID:           userid.MustNew(env.userID),
		ClientID:         oauthapp.ControlCLIClientID,
		InstallationName: "test",
		GrantedScopes:    authscope.NonAdminGrant().String(),
		SecretHash:       env.validator.HashSecret(auth.MintAccessSecret()),
		RefreshHash:      env.validator.HashSecret(currentRefresh),
	}))

	mux := http.NewServeMux()
	service.NewOAuthServerHandler(service.OAuthServerDeps{Store: env.store, Validator: env.validator, Lifecycle: auth.NewCredentialLifecycleEffects(env.cache, env.closer, env.closer), HubURL: func() string { return env.server.URL }}).RegisterRoutes(mux)
	form := url.Values{
		"grant_type":    {service.GrantTypeRefreshToken},
		"client_id":     {oauthapp.ControlCLIClientID},
		"refresh_token": {auth.FormatBearer(auth.BearerKindAPI, tokenID, currentRefresh)},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode())).WithContext(ctx)
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
	t.Parallel()

	env := setupAPIAuth(t)

	// Mint and rotate once.
	tokenID := id.Generate()
	prev := auth.MintAccessSecret()
	require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:               tokenID,
		UserID:           userid.MustNew(env.userID),
		ClientID:         oauthapp.ControlCLIClientID,
		InstallationName: "test",
		GrantedScopes:    authscope.NonAdminGrant().String(),
		SecretHash:       env.validator.HashSecret(auth.MintAccessSecret()),
		RefreshHash:      env.validator.HashSecret(prev),
	}))
	first, err := http.PostForm(env.server.URL+"/oauth/token", url.Values{
		"grant_type":    {service.GrantTypeRefreshToken},
		"client_id":     {oauthapp.ControlCLIClientID},
		"refresh_token": {auth.FormatBearer(auth.BearerKindAPI, tokenID, prev)},
	})
	require.NoError(t, err)
	defer func() { _ = first.Body.Close() }()
	var firstBody map[string]any
	require.NoError(t, json.NewDecoder(first.Body).Decode(&firstBody))

	// Replay the rotated-out refresh; within the grace window the handler
	// must re-emit the same access/refresh pair the first call produced.
	retry, err := http.PostForm(env.server.URL+"/oauth/token", url.Values{
		"grant_type":    {service.GrantTypeRefreshToken},
		"client_id":     {oauthapp.ControlCLIClientID},
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
	t.Parallel()

	env := setupAPIAuth(t)
	tokenID := id.Generate()
	previousRefresh := auth.MintAccessSecret()
	previousHash := env.validator.HashSecret(previousRefresh)
	now := time.Now()
	derived := env.validator.DeriveRefreshBearerPair(
		auth.BearerKindAPI, tokenID, previousHash, now, auth.AccessTokenTTL, auth.RefreshTokenTTL,
	)
	require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID: tokenID, UserID: userid.MustNew(env.userID), ClientID: oauthapp.ControlCLIClientID, InstallationName: "test", GrantedScopes: authscope.NonAdminGrant().String(),
		SecretHash: env.validator.HashSecret(auth.MintAccessSecret()), RefreshHash: previousHash,
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

	resp, err := http.PostForm(env.server.URL+"/oauth/token", url.Values{
		"grant_type":    {service.GrantTypeRefreshToken},
		"client_id":     {oauthapp.ControlCLIClientID},
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
	t.Parallel()

	env := setupAPIAuth(t)
	tokenID := id.Generate()
	previousRefresh := auth.MintAccessSecret()
	require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:               tokenID,
		UserID:           userid.MustNew(env.userID),
		ClientID:         oauthapp.ControlCLIClientID,
		InstallationName: "test",
		GrantedScopes:    authscope.NonAdminGrant().String(),
		SecretHash:       env.validator.HashSecret(auth.MintAccessSecret()),
		RefreshHash:      env.validator.HashSecret(previousRefresh),
	}))

	first, err := http.PostForm(env.server.URL+"/oauth/token", url.Values{
		"grant_type":    {service.GrantTypeRefreshToken},
		"client_id":     {oauthapp.ControlCLIClientID},
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
	service.NewOAuthServerHandler(service.OAuthServerDeps{Store: env.store, Validator: otherValidator, Lifecycle: auth.NewCredentialLifecycleEffects(env.cache, noopBearerCloser{}, nil), HubURL: func() string { return otherServer.URL }}).RegisterRoutes(otherMux)

	retry, err := http.PostForm(otherServer.URL+"/oauth/token", url.Values{
		"grant_type":    {service.GrantTypeRefreshToken},
		"client_id":     {oauthapp.ControlCLIClientID},
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
	t.Parallel()

	env := setupAPIAuth(t)

	tokenID := id.Generate()
	currentRefresh := auth.MintAccessSecret()
	require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:               tokenID,
		UserID:           userid.MustNew(env.userID),
		ClientID:         oauthapp.ControlCLIClientID,
		InstallationName: "test",
		GrantedScopes:    authscope.NonAdminGrant().String(),
		SecretHash:       env.validator.HashSecret(auth.MintAccessSecret()),
		RefreshHash:      env.validator.HashSecret(currentRefresh),
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
	service.NewOAuthServerHandler(service.OAuthServerDeps{Store: wrapped, Validator: env.validator, Lifecycle: auth.NewCredentialLifecycleEffects(env.cache, noopBearerCloser{}, nil), HubURL: func() string { return srv.URL }}).RegisterRoutes(mux)

	resp, err := http.PostForm(srv.URL+"/oauth/token", url.Values{
		"grant_type":    {service.GrantTypeRefreshToken},
		"client_id":     {oauthapp.ControlCLIClientID},
		"refresh_token": {auth.FormatBearer(auth.BearerKindAPI, tokenID, currentRefresh)},
	})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "invalid_grant", body["error"])
}

func TestAPIAuth_Refresh_CASRecoveryReportsWinnerRemainingLifetime(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	tokenID := id.Generate()
	currentRefresh := auth.MintAccessSecret()
	require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID: tokenID, UserID: userid.MustNew(env.userID), ClientID: oauthapp.ControlCLIClientID, InstallationName: "test", GrantedScopes: authscope.NonAdminGrant().String(),
		SecretHash: env.validator.HashSecret(auth.MintAccessSecret()), RefreshHash: env.validator.HashSecret(currentRefresh),
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
	service.NewOAuthServerHandler(service.OAuthServerDeps{Store: wrapper, Validator: env.validator, Lifecycle: auth.NewCredentialLifecycleEffects(env.cache, noopBearerCloser{}, nil), HubURL: func() string { return srv.URL }}).RegisterRoutes(mux)

	resp, err := http.PostForm(srv.URL+"/oauth/token", url.Values{
		"grant_type":    {service.GrantTypeRefreshToken},
		"client_id":     {oauthapp.ControlCLIClientID},
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
	t.Parallel()

	env := setupAPIAuth(t)

	tokenID := id.Generate()
	currentRefresh := auth.MintAccessSecret()
	currentRefreshHash := env.validator.HashSecret(currentRefresh)
	require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:               tokenID,
		UserID:           userid.MustNew(env.userID),
		ClientID:         oauthapp.ControlCLIClientID,
		InstallationName: "test",
		GrantedScopes:    authscope.NonAdminGrant().String(),
		SecretHash:       env.validator.HashSecret(auth.MintAccessSecret()),
		RefreshHash:      currentRefreshHash,
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
	service.NewOAuthServerHandler(service.OAuthServerDeps{Store: wrapped, Validator: env.validator, Lifecycle: auth.NewCredentialLifecycleEffects(env.cache, noopBearerCloser{}, nil), HubURL: func() string { return srv.URL }}).RegisterRoutes(mux)

	resp, err := http.PostForm(srv.URL+"/oauth/token", url.Values{
		"grant_type":    {service.GrantTypeRefreshToken},
		"client_id":     {oauthapp.ControlCLIClientID},
		"refresh_token": {auth.FormatBearer(auth.BearerKindAPI, tokenID, currentRefresh)},
	})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestAPIAuth_Refresh_ReusedAfterGraceRevokesRow(t *testing.T) {
	t.Parallel()

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
		ID:               tokenID,
		UserID:           userid.MustNew(env.userID),
		ClientID:         oauthapp.ControlCLIClientID,
		InstallationName: "test",
		GrantedScopes:    authscope.NonAdminGrant().String(),
		SecretHash:       env.validator.HashSecret(auth.MintAccessSecret()),
		RefreshHash:      env.validator.HashSecret(prev),
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
	resp, err := http.PostForm(env.server.URL+"/oauth/token", url.Values{
		"grant_type":    {service.GrantTypeRefreshToken},
		"client_id":     {oauthapp.ControlCLIClientID},
		"refresh_token": {auth.FormatBearer(auth.BearerKindAPI, tokenID, prev)},
	})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.True(t, env.closer.closed(tokenID), "refresh reuse must close channels authorized by the compromised bearer")

	// The current refresh must also fail now: reuse-after-grace revokes
	// the underlying row.
	resp2, err := http.PostForm(env.server.URL+"/oauth/token", url.Values{
		"grant_type":    {service.GrantTypeRefreshToken},
		"client_id":     {oauthapp.ControlCLIClientID},
		"refresh_token": {auth.FormatBearer(auth.BearerKindAPI, tokenID, cur)},
	})
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()
	assert.Equal(t, http.StatusBadRequest, resp2.StatusCode)
}

func TestAPIAuth_Revoke_BustsCacheAndRowRevoked(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)

	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:               tokenID,
		UserID:           userid.MustNew(env.userID),
		ClientID:         oauthapp.ControlCLIClientID,
		InstallationName: "test",
		GrantedScopes:    authscope.NonAdminGrant().String(),
		SecretHash:       env.validator.HashSecret(secret),
	}))
	bearer := auth.FormatBearer(auth.BearerKindAPI, tokenID, secret)
	// Warm the bearer cache by validating once.
	_, err := env.validator.ValidateBearer(context.Background(), bearer)
	require.NoError(t, err)

	// Revoke via the public endpoint.
	resp, err := http.PostForm(env.server.URL+"/oauth/revoke", url.Values{
		"token":     {bearer},
		"client_id": {oauthapp.ControlCLIClientID},
	})
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

func (s deviceAuthorizationOverrideStore) RunInUserAuthTransaction(ctx context.Context, userID userid.UserID, fn func(store.Store) error) error {
	return s.Store.RunInUserAuthTransaction(ctx, userID, func(tx store.Store) error {
		return fn(s.rebind(tx))
	})
}

// RunInTransaction re-binds the overrides to the TRANSACTION store, exactly as
// RunInUserAuthTransaction does. The approval leg writes through this
// boundary, so an override that stopped at it would silently not apply to the
// write the test means to fail.
func (s deviceAuthorizationOverrideStore) RunInTransaction(ctx context.Context, fn func(store.Store) error) error {
	return s.Store.RunInTransaction(ctx, func(tx store.Store) error {
		return fn(s.rebind(tx))
	})
}

// rebind carries the override functions onto another store, so the same fault
// applies inside and outside a transaction.
func (s deviceAuthorizationOverrideStore) rebind(tx store.Store) deviceAuthorizationOverrideStore {
	override := s.device.(deviceAuthorizationOverride)
	override.DeviceAuthorizationStore = tx.DeviceAuthorizations()
	return deviceAuthorizationOverrideStore{Store: tx, device: override}
}

type deviceAuthorizationOverride struct {
	store.DeviceAuthorizationStore
	create        func(context.Context, store.CreateDeviceAuthorizationParams) error
	get           func(context.Context, string) (*store.DeviceAuthorization, error)
	getByUserCode func(context.Context, string) (*store.DeviceAuthorization, error)
	touchPoll     func(context.Context, string, time.Time) error
	consume       func(context.Context, string) (int64, error)
}

func (s deviceAuthorizationOverride) Create(ctx context.Context, p store.CreateDeviceAuthorizationParams) error {
	if s.create != nil {
		return s.create(ctx, p)
	}
	return s.DeviceAuthorizationStore.Create(ctx, p)
}

func (s deviceAuthorizationOverride) Get(ctx context.Context, code string) (*store.DeviceAuthorization, error) {
	if s.get != nil {
		return s.get(ctx, code)
	}
	return s.DeviceAuthorizationStore.Get(ctx, code)
}

func (s deviceAuthorizationOverride) GetByUserCode(ctx context.Context, code string) (*store.DeviceAuthorization, error) {
	if s.getByUserCode != nil {
		return s.getByUserCode(ctx, code)
	}
	return s.DeviceAuthorizationStore.GetByUserCode(ctx, code)
}

func (s deviceAuthorizationOverride) TouchPoll(ctx context.Context, code string, now time.Time) error {
	if s.touchPoll != nil {
		return s.touchPoll(ctx, code, now)
	}
	return s.DeviceAuthorizationStore.TouchPoll(ctx, code, now)
}

func (s deviceAuthorizationOverride) Consume(ctx context.Context, code string, now time.Time) (int64, error) {
	if s.consume != nil {
		return s.consume(ctx, code)
	}
	return s.DeviceAuthorizationStore.Consume(ctx, code, now)
}

func (s userLookupFailStore) Users() store.UserStore { return s.users }

func (s userLookupFailStore) RunInUserAuthTransaction(ctx context.Context, userID userid.UserID, fn func(store.Store) error) error {
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
	t.Parallel()

	env := setupAPIAuth(t)
	recording := &deadlineRecordingTokens{APITokenStore: env.store.APITokens()}
	wrapped := apiTokenOverrideStore{Store: env.store, api: recording}
	validator, err := auth.NewTokenValidator(wrapped, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	mux := http.NewServeMux()
	service.NewOAuthServerHandler(service.OAuthServerDeps{Store: wrapped, Validator: validator, Lifecycle: auth.NewCredentialLifecycleEffects(env.cache, noopBearerCloser{}, nil), HubURL: func() string { return env.server.URL }}).RegisterRoutes(mux)

	form := url.Values{"grant_type": {service.GrantTypeRefreshToken}, "client_id": {oauthapp.ControlCLIClientID}, "refresh_token": {auth.FormatBearer(auth.BearerKindAPI, id.Generate(), auth.MintAccessSecret())}}
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.False(t, recording.deadline.IsZero(), "detached refresh work must carry a replacement deadline")
	remaining := time.Until(recording.deadline)
	assert.Positive(t, remaining)
	assert.LessOrEqual(t, remaining, service.RefreshWorkTimeout)
}

func TestAPIAuth_Token_UserLookupFailureDoesNotLeaveToken(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	verifier, challenge := pkceVerifierAndChallenge()
	code := id.Generate()
	require.NoError(t, env.store.OAuthAuthorizationCodes().Create(context.Background(), store.CreateOAuthAuthorizationCodeParams{
		ClientID:         oauthapp.ControlCLIClientID,
		GrantedScopes:    authscope.NonAdminGrant().String(),
		Code:             code,
		UserID:           userid.MustNew(env.userID),
		CodeChallenge:    challenge,
		InstallationName: "test",
		ExpiresAt:        time.Now().Add(time.Minute),
		RedirectURI:      "http://127.0.0.1:54321/callback",
	}))
	failing := userLookupFailStore{
		Store: env.store,
		users: getByIDFailUsers{UserStore: env.store.Users()},
	}
	mux := http.NewServeMux()
	service.NewOAuthServerHandler(service.OAuthServerDeps{Store: failing, Validator: env.validator, Lifecycle: auth.NewCredentialLifecycleEffects(env.cache, noopBearerCloser{}, nil), HubURL: func() string { return env.server.URL }}).RegisterRoutes(mux)

	before, err := env.store.APITokens().ListByUser(context.Background(), store.ListAPITokensByUserParams{UserID: userid.MustNew(env.userID), PageParams: store.PageParams{Limit: 50}})
	require.NoError(t, err)
	form := url.Values{
		"grant_type":    {service.GrantTypeAuthorizationCode},
		"client_id":     {oauthapp.ControlCLIClientID},
		"redirect_uri":  {"http://127.0.0.1:54321/callback"},
		"code":          {code},
		"code_verifier": {verifier},
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusInternalServerError, rec.Code)

	after, err := env.store.APITokens().ListByUser(context.Background(), store.ListAPITokensByUserParams{UserID: userid.MustNew(env.userID), PageParams: store.PageParams{Limit: 50}})
	require.NoError(t, err)
	assert.Len(t, after.Rows, len(before.Rows), "failed issuance must roll back the undisclosed token row")

	retry, err := http.PostForm(env.server.URL+"/oauth/token", form)
	require.NoError(t, err)
	defer func() { _ = retry.Body.Close() }()
	assert.Equal(t, http.StatusOK, retry.StatusCode, "failed issuance must leave the authorization code retryable")
}

func TestAPIAuth_DeviceCode_UserLookupFailureLeavesGrantRetryable(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	deviceCode := id.Generate()
	require.NoError(t, env.store.DeviceAuthorizations().Create(context.Background(), store.CreateDeviceAuthorizationParams{
		ClientID:   oauthapp.ControlCLIClientID,
		DeviceCode: deviceCode, UserCode: verifycode.Generate(), DeviceName: "test", ExpiresAt: time.Now().Add(time.Minute),
	}))
	rows, err := env.store.DeviceAuthorizations().Approve(context.Background(), store.ApproveDeviceAuthorizationParams{
		DeviceCode: deviceCode, UserID: userid.MustNew(env.userID),
	}, env.now().UTC())
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)

	failing := userLookupFailStore{Store: env.store, users: getByIDFailUsers{UserStore: env.store.Users()}}
	mux := http.NewServeMux()
	service.NewOAuthServerHandler(service.OAuthServerDeps{Store: failing, Validator: env.validator, Lifecycle: auth.NewCredentialLifecycleEffects(env.cache, noopBearerCloser{}, nil), HubURL: func() string { return env.server.URL }}).RegisterRoutes(mux)
	form := url.Values{
		"grant_type":  {service.GrantTypeDeviceCode},
		"client_id":   {oauthapp.ControlCLIClientID},
		"device_code": {deviceCode},
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, "internal server error\n", rec.Body.String())

	// The failed issuance rolled back Consume, so the grant is still
	// unconsumed and exchangeable. Its poll was recorded regardless
	// (TouchPoll now runs outside the issuance transaction), so an
	// immediate retry is correctly throttled with slow_down; step past
	// the interval, then confirm a clean retry succeeds -- proving the
	// grant stayed retryable. The retry goes to env.server, whose handler
	// is the one on env's clock.
	env.advance(service.DeviceCodePollInterval + 100*time.Millisecond)
	retry, err := http.PostForm(env.server.URL+"/oauth/token", form)
	require.NoError(t, err)
	defer func() { _ = retry.Body.Close() }()
	assert.Equal(t, http.StatusOK, retry.StatusCode, "failed issuance must leave the device grant retryable")
}

func TestAPIAuth_DeviceCode_TouchPollFailureIsInternal(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	deviceCode := id.Generate()
	require.NoError(t, env.store.DeviceAuthorizations().Create(context.Background(), store.CreateDeviceAuthorizationParams{
		ClientID:   oauthapp.ControlCLIClientID,
		DeviceCode: deviceCode, UserCode: verifycode.Generate(), ExpiresAt: time.Now().Add(time.Minute),
	}))
	forcedErr := errors.New("sensitive poll failure")
	device := deviceAuthorizationOverride{DeviceAuthorizationStore: env.store.DeviceAuthorizations(), touchPoll: func(context.Context, string, time.Time) error {
		return forcedErr
	}}
	wrapped := deviceAuthorizationOverrideStore{Store: env.store, device: device}
	mux := http.NewServeMux()
	service.NewOAuthServerHandler(service.OAuthServerDeps{Store: wrapped, Validator: env.validator, Lifecycle: auth.NewCredentialLifecycleEffects(env.cache, noopBearerCloser{}, nil), HubURL: func() string { return env.server.URL }}).RegisterRoutes(mux)
	form := url.Values{
		"grant_type":  {service.GrantTypeDeviceCode},
		"client_id":   {oauthapp.ControlCLIClientID},
		"device_code": {deviceCode},
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, "internal server error\n", rec.Body.String())
	assert.NotContains(t, rec.Body.String(), forcedErr.Error())
}

func TestAPIAuth_DeviceCode_LookupFailureIsInternal(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	forcedErr := errors.New("sensitive device lookup failure")
	device := deviceAuthorizationOverride{DeviceAuthorizationStore: env.store.DeviceAuthorizations(), get: func(context.Context, string) (*store.DeviceAuthorization, error) {
		return nil, forcedErr
	}}
	wrapped := deviceAuthorizationOverrideStore{Store: env.store, device: device}
	mux := http.NewServeMux()
	service.NewOAuthServerHandler(service.OAuthServerDeps{Store: wrapped, Validator: env.validator, Lifecycle: auth.NewCredentialLifecycleEffects(env.cache, noopBearerCloser{}, nil), HubURL: func() string { return env.server.URL }}).RegisterRoutes(mux)
	form := url.Values{"grant_type": {service.GrantTypeDeviceCode}, "device_code": {"test-device-code"}}
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, "internal server error\n", rec.Body.String())
	assert.NotContains(t, rec.Body.String(), forcedErr.Error())
}

func TestAPIAuth_DeviceCode_ConsumeRequiresOneRow(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	deviceCode := id.Generate()
	require.NoError(t, env.store.DeviceAuthorizations().Create(context.Background(), store.CreateDeviceAuthorizationParams{
		ClientID:   oauthapp.ControlCLIClientID,
		DeviceCode: deviceCode, UserCode: verifycode.Generate(), ExpiresAt: time.Now().Add(time.Minute),
	}))
	rows, err := env.store.DeviceAuthorizations().Approve(context.Background(), store.ApproveDeviceAuthorizationParams{
		DeviceCode: deviceCode, UserID: userid.MustNew(env.userID),
	}, env.now().UTC())
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
	device := deviceAuthorizationOverride{DeviceAuthorizationStore: env.store.DeviceAuthorizations(), consume: func(context.Context, string) (int64, error) {
		return 0, nil
	}}
	wrapped := deviceAuthorizationOverrideStore{Store: env.store, device: device}
	mux := http.NewServeMux()
	service.NewOAuthServerHandler(service.OAuthServerDeps{Store: wrapped, Validator: env.validator, Lifecycle: auth.NewCredentialLifecycleEffects(env.cache, noopBearerCloser{}, nil), HubURL: func() string { return env.server.URL }}).RegisterRoutes(mux)
	form := url.Values{
		"grant_type":  {service.GrantTypeDeviceCode},
		"client_id":   {oauthapp.ControlCLIClientID},
		"device_code": {deviceCode},
	}
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
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
// even when issuance fails transiently and rolls back, so a client that polls
// an approved-but-failing grant rapidly still gets slow_down. This only holds
// because TouchPoll runs outside the issuance transaction; if it ran inside,
// the rollback would discard the anchor and the rapid re-poll would retry
// issuance instead of being throttled.
func TestAPIAuth_DeviceCode_ApprovedPollAdvancesThrottleDespiteIssuanceFailure(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	deviceCode := id.Generate()
	require.NoError(t, env.store.DeviceAuthorizations().Create(context.Background(), store.CreateDeviceAuthorizationParams{
		ClientID:   oauthapp.ControlCLIClientID,
		DeviceCode: deviceCode, UserCode: verifycode.Generate(), IntervalSeconds: 5, ExpiresAt: time.Now().Add(time.Minute),
	}))
	rows, err := env.store.DeviceAuthorizations().Approve(context.Background(), store.ApproveDeviceAuthorizationParams{
		DeviceCode: deviceCode, UserID: userid.MustNew(env.userID),
	}, env.now().UTC())
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)

	// Inject a transient (non-final) store error inside the issuance
	// transaction so token creation rolls back. Consume runs inside the
	// transaction; TouchPoll runs outside it, so last_polled_at must survive.
	device := deviceAuthorizationOverride{DeviceAuthorizationStore: env.store.DeviceAuthorizations(), consume: func(context.Context, string) (int64, error) {
		return 0, errors.New("transient consume failure")
	}}
	wrapped := deviceAuthorizationOverrideStore{Store: env.store, device: device}
	mux := http.NewServeMux()
	service.NewOAuthServerHandler(service.OAuthServerDeps{Store: wrapped, Validator: env.validator, Lifecycle: auth.NewCredentialLifecycleEffects(env.cache, noopBearerCloser{}, nil), HubURL: func() string { return env.server.URL }}).RegisterRoutes(mux)
	form := url.Values{
		"grant_type":  {service.GrantTypeDeviceCode},
		"client_id":   {oauthapp.ControlCLIClientID},
		"device_code": {deviceCode},
	}

	// First poll: issuance fails transiently (500), but the throttle anchor
	// must have advanced regardless, and the grant must stay retryable.
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
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
	req2 := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	require.Equal(t, http.StatusBadRequest, rec2.Code)
	var body map[string]any
	require.NoError(t, json.NewDecoder(rec2.Body).Decode(&body))
	assert.Equal(t, "slow_down", body["error"], "rapid re-poll of a transiently-failing approved grant must be throttled")
}

func TestAPIAuth_Revoke_AcceptsRefreshSecrets(t *testing.T) {
	t.Parallel()

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
				ID:               tokenID,
				UserID:           userid.MustNew(env.userID),
				ClientID:         oauthapp.ControlCLIClientID,
				InstallationName: "test",
				GrantedScopes:    authscope.NonAdminGrant().String(),
				SecretHash:       env.validator.HashSecret(auth.MintAccessSecret()),
				RefreshHash:      env.validator.HashSecret(refreshSecret),
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

			resp, err := http.PostForm(env.server.URL+"/oauth/revoke", url.Values{
				"token":     {auth.FormatBearer(auth.BearerKindAPI, tokenID, refreshSecret)},
				"client_id": {oauthapp.ControlCLIClientID},
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
	t.Parallel()

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
	service.NewOAuthServerHandler(service.OAuthServerDeps{Store: wrapped, Validator: validator, Lifecycle: auth.NewCredentialLifecycleEffects(env.cache, noopBearerCloser{}, nil), HubURL: func() string { return env.server.URL }}).RegisterRoutes(mux)

	form := url.Values{"grant_type": {service.GrantTypeRefreshToken}, "client_id": {oauthapp.ControlCLIClientID}, "refresh_token": {
		auth.FormatBearer(auth.BearerKindAPI, id.Generate(), auth.MintAccessSecret()),
	}}
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, "internal server error\n", rec.Body.String())
}

func TestAPIAuth_Revoke_StoreFailureReturnsServerError(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)

	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:               tokenID,
		UserID:           userid.MustNew(env.userID),
		ClientID:         oauthapp.ControlCLIClientID,
		InstallationName: "test",
		GrantedScopes:    authscope.NonAdminGrant().String(),
		SecretHash:       env.validator.HashSecret(secret),
	}))
	bearer := auth.FormatBearer(auth.BearerKindAPI, tokenID, secret)

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	wrapped := apiTokenOverrideStore{
		Store: env.store,
		api:   apiRevokeFailTokens{APITokenStore: env.store.APITokens()},
	}
	service.NewOAuthServerHandler(service.OAuthServerDeps{Store: wrapped, Validator: env.validator, Lifecycle: auth.NewCredentialLifecycleEffects(env.cache, noopBearerCloser{}, nil), HubURL: func() string { return srv.URL }}).RegisterRoutes(mux)

	resp, err := http.PostForm(srv.URL+"/oauth/revoke", url.Values{
		"token":     {bearer},
		"client_id": {oauthapp.ControlCLIClientID},
	})
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
	t.Parallel()

	env := setupAPIAuth(t)

	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:               tokenID,
		UserID:           userid.MustNew(env.userID),
		ClientID:         oauthapp.ControlCLIClientID,
		InstallationName: "test",
		GrantedScopes:    authscope.NonAdminGrant().String(),
		SecretHash:       env.validator.HashSecret(secret),
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
	service.NewOAuthServerHandler(service.OAuthServerDeps{Store: wrapped, Validator: validator, Lifecycle: auth.NewCredentialLifecycleEffects(env.cache, noopBearerCloser{}, nil), HubURL: func() string { return srv.URL }}).RegisterRoutes(mux)

	resp, err := http.PostForm(srv.URL+"/oauth/revoke", url.Values{"token": {bearer}})
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
	t.Parallel()

	env := setupAPIAuth(t)

	// Seed a worker + workspace + delegation row. The revoke endpoint
	// must succeed and mark the delegation row revoked when the bearer
	// id resolves to a delegation_tokens row.
	workerID, _ := seedDelegationFixtures(t, env)

	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, env.store.DelegationTokens().Create(context.Background(), store.CreateDelegationTokenParams{
		GrantedScopes: "workspace:read workspace:write worker:read",
		ID:            tokenID,
		UserID:        userid.MustNew(env.userID),
		WorkerID:      workerID,
		SecretHash:    env.validator.HashSecret(secret),
		ExpiresAt:     time.Now().Add(time.Hour),
	}))
	bearer := auth.FormatBearer(auth.BearerKindDelegation, tokenID, secret)

	resp, err := http.PostForm(env.server.URL+"/oauth/revoke", url.Values{"token": {bearer}})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	row, err := env.store.DelegationTokens().GetByID(context.Background(), tokenID)
	require.NoError(t, err)
	assert.NotNil(t, row.RevokedAt)
}

// The two answers RFC 7009 separates, and the line between them is what the
// caller can DO about the response.
//
// A missing `token` is a malformed request: the client sent no credential at
// all, and section 2.2.1 makes that invalid_request. An UNPARSEABLE token is
// not -- section 2.2 requires 200 there, because a client cannot act on
// "your token was already invalid" in any way that differs from success.
//
// The uniform 200 is also the stronger non-disclosure. This test previously
// pinned 401 so that a malformed bearer and a valid-id-with-wrong-secret
// answered alike, which kept an attacker from probing for live token_ids.
// Answering 200 to every presented token keeps that property and widens it:
// the response now separates nothing at all, including a real revocation.
func TestAPIAuth_Revoke_AbsentTokenIsTheOnlyRefusal(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	resp, err := http.PostForm(env.server.URL+"/oauth/revoke", url.Values{"token": {""}})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	resp2, err := http.PostForm(env.server.URL+"/oauth/revoke", url.Values{"token": {"not-a-bearer"}})
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp2.StatusCode,
		"RFC 7009 section 2.2: an invalid token is not an error the client can handle")
}

// The security fix for the unauthenticated-revoke vulnerability: a caller who
// knows only the non-secret token_id -- which every JSON response carries --
// MUST NOT be able to revoke a victim's credential with a wrong secret.
//
// The STATUS is 200, per RFC 7009 section 2.2, and it is the row and the cache
// that carry the guarantee. That is deliberate rather than a weakening: an
// attacker who could tell a refusal from a success would learn which token_ids
// are live, so a response that reveals the outcome is itself the leak. What
// must not happen is the revocation, and the two assertions below are what say
// it did not.
func TestAPIAuth_Revoke_WrongSecretRejected(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)

	// Real api_token row with a known good secret.
	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:               tokenID,
		UserID:           userid.MustNew(env.userID),
		ClientID:         oauthapp.ControlCLIClientID,
		InstallationName: "test",
		GrantedScopes:    authscope.NonAdminGrant().String(),
		SecretHash:       env.validator.HashSecret(secret),
	}))
	goodBearer := auth.FormatBearer(auth.BearerKindAPI, tokenID, secret)
	// Warm the cache so we can later assert that nothing evicted it.
	_, err := env.validator.ValidateBearer(context.Background(), goodBearer)
	require.NoError(t, err)

	// Attacker-style bearer: real token_id, wrong secret. RFC 7009 §2.1
	// requires the presented token to be valid; without verification,
	// anyone who learns a token_id (e.g. via a logged JSON response or
	// stale CLI install) could revoke a victim's session.
	attackerBearer := auth.FormatBearer(auth.BearerKindAPI, tokenID, "completely-bogus-secret")
	resp, err := http.PostForm(env.server.URL+"/oauth/revoke", url.Values{"token": {attackerBearer}})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"the answer must not separate a refused revocation from a real one")

	// Row is NOT revoked.
	row, err := env.store.APITokens().GetByID(context.Background(), tokenID)
	require.NoError(t, err)
	assert.Nil(t, row.RevokedAt, "row must NOT be revoked when secret didn't verify")

	// Real bearer still validates (cache wasn't poisoned, row is alive).
	user, err := env.validator.ValidateBearer(context.Background(), goodBearer)
	require.NoError(t, err)
	assert.Equal(t, env.userID, user.ID.String())
}

// TestAPIAuth_Revoke_WrongSecretRejected_DelegationToken pins the same
// guarantee for the delegation_tokens table. delegation token_ids are
// even more abundantly exposed (they appear in mint responses, channel
// registration logs, and audit telemetry), so the secret check matters
// equally here.
func TestAPIAuth_Revoke_WrongSecretRejected_DelegationToken(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	workerID, _ := seedDelegationFixtures(t, env)

	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, env.store.DelegationTokens().Create(context.Background(), store.CreateDelegationTokenParams{
		GrantedScopes: "workspace:read workspace:write worker:read",
		ID:            tokenID,
		UserID:        userid.MustNew(env.userID),
		WorkerID:      workerID,
		SecretHash:    env.validator.HashSecret(secret),
		ExpiresAt:     time.Now().Add(time.Hour),
	}))

	attackerBearer := auth.FormatBearer(auth.BearerKindDelegation, tokenID, "wrong")
	resp, err := http.PostForm(env.server.URL+"/oauth/revoke", url.Values{"token": {attackerBearer}})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	row, err := env.store.DelegationTokens().GetByID(context.Background(), tokenID)
	require.NoError(t, err)
	assert.Nil(t, row.RevokedAt)
}

// The endpoint must not be a token_id existence oracle, and this is the test
// that can still fail once every answer is 200.
//
// Asserting one status on one bad input proved that only while a refusal had
// its own code: with a uniform 200 such an assertion passes whatever the other
// branches do. So it compares the FOUR outcomes to each other -- unknown id,
// wrong secret, unparseable, and a real revocation -- byte for byte across the
// status, the body and the headers a client can read. A branch that grew its
// own answer, an error body, or a WWW-Authenticate header fails here.
func TestAPIAuth_Revoke_AnswersEveryPresentedTokenIdentically(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)

	// A REAL credential, so the success branch is one of the four compared.
	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:               tokenID,
		UserID:           userid.MustNew(env.userID),
		ClientID:         oauthapp.ControlCLIClientID,
		InstallationName: "test",
		GrantedScopes:    authscope.NonAdminGrant().String(),
		SecretHash:       env.validator.HashSecret(secret),
	}))

	type answer struct {
		Status  int
		Body    string
		Headers []string
	}
	ask := func(t *testing.T, token string) answer {
		t.Helper()
		// The client_id rides along so the four answers stay comparable once
		// revocation is bound to the app the credential was issued to: the
		// indistinguishability that matters is among the three INVALID
		// presentations, and a caller that cannot present the app cannot make
		// the valid one answer differently either.
		resp, err := http.PostForm(env.server.URL+"/oauth/revoke", url.Values{
			"token":     {token},
			"client_id": {oauthapp.ControlCLIClientID},
		})
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		// Only the headers a CLIENT can read separate outcomes. Date and
		// Content-Length vary for reasons that carry nothing.
		var headers []string
		for _, name := range []string{"Www-Authenticate", "Content-Type", "Cache-Control"} {
			if v := resp.Header.Get(name); v != "" {
				headers = append(headers, name+": "+v)
			}
		}
		return answer{Status: resp.StatusCode, Body: string(body), Headers: headers}
	}

	unknown := ask(t, auth.FormatBearer(auth.BearerKindAPI, id.Generate(), auth.MintAccessSecret()))
	wrongSecret := ask(t, auth.FormatBearer(auth.BearerKindAPI, tokenID, "wrong-secret"))
	malformed := ask(t, "not-a-bearer")
	// LAST, because it is the one that changes the row.
	real := ask(t, auth.FormatBearer(auth.BearerKindAPI, tokenID, secret))

	assert.Equal(t, http.StatusOK, real.Status)
	assert.Equal(t, real, unknown, "an unknown token_id must answer as a real revocation does")
	assert.Equal(t, real, wrongSecret, "a wrong secret must answer as a real revocation does")
	assert.Equal(t, real, malformed, "an unparseable token must answer as a real revocation does")

	// And the revocation that DID happen, happened. Without this the endpoint
	// could answer 200 to everything by doing nothing at all.
	row, err := env.store.APITokens().GetByID(context.Background(), tokenID)
	require.NoError(t, err)
	assert.NotNil(t, row.RevokedAt)
}

// TestAPIAuth_Revoke_AlreadyRevokedIsIdempotent confirms a client that
// retries revoke after a brief network failure (and presents the same valid
// bearer secret) still gets 200 OK — secret verification accepts
// already-revoked rows so re-revoke is a no-op rather than a 401.
func TestAPIAuth_Revoke_AlreadyRevokedIsIdempotent(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)

	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:               tokenID,
		UserID:           userid.MustNew(env.userID),
		ClientID:         oauthapp.ControlCLIClientID,
		InstallationName: "test",
		GrantedScopes:    authscope.NonAdminGrant().String(),
		SecretHash:       env.validator.HashSecret(secret),
	}))
	bearer := auth.FormatBearer(auth.BearerKindAPI, tokenID, secret)

	// First revoke: 200, row becomes revoked.
	resp, err := http.PostForm(env.server.URL+"/oauth/revoke", url.Values{
		"token":     {bearer},
		"client_id": {oauthapp.ControlCLIClientID},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	_ = resp.Body.Close()

	// Second revoke (same bearer): still 200 — VerifyBearerSecret accepts
	// already-revoked rows so the secret-holder doesn't need to handle
	// 401 retries on transient transport errors.
	resp2, err := http.PostForm(env.server.URL+"/oauth/revoke", url.Values{
		"token":     {bearer},
		"client_id": {oauthapp.ControlCLIClientID},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp2.StatusCode)
	_ = resp2.Body.Close()
}

func TestAPIAuth_Token_UnsupportedGrantType(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	resp, err := http.PostForm(env.server.URL+"/oauth/token", url.Values{"grant_type": {"password"}})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "unsupported_grant_type", body["error"])
}

func TestAPIAuth_GetMethodOnlyHandlers_Reject(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	for _, path := range []string{"/oauth/consent", "/oauth/device-authorization", "/oauth/token", "/oauth/token", "/oauth/revoke"} {
		resp, err := http.Get(env.server.URL + path)
		require.NoError(t, err, path)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode, "GET on %s must be rejected", path)
	}
}

// TestAPIAuth_ConsentLegChecksTheMethodBeforeTheGate pins the ORDER inside
// consentLeg, which is the part that can silently go wrong.
//
// The gate answers a GET by bouncing to /elevate, so a gate that ran first
// would send an anonymous caller through a verification round trip for a
// request the leg refuses on arrival -- and the sibling assertion above,
// that a GET on a POST-only leg answers 405, would break with it.
func TestAPIAuth_ConsentLegChecksTheMethodBeforeTheGate(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)

	// The consent page answers GET only. A POST is 405, NOT the gate's 401.
	resp, err := postForm(env.server.URL+"/oauth/authorize", url.Values{})
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode,
		"the method check runs before the gate, so a POST here never reaches it")

	// And an unsupported method on the activation page, which answers three.
	req, err := http.NewRequest(http.MethodDelete, env.server.URL+"/oauth/device", nil)
	require.NoError(t, err)
	del, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	_ = del.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, del.StatusCode)
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
	workerID = id.Generate()
	require.NoError(t, env.store.Workers().Create(context.Background(), store.CreateWorkerParams{
		ID:              workerID,
		AuthToken:       id.Generate(),
		RegisteredBy:    userid.MustNew(env.userID),
		PublicKey:       []byte("test-x25519-key-32-bytes-padding"),
		MlkemPublicKey:  []byte("test-mlkem"),
		SlhdsaPublicKey: []byte("test-slhdsa"),
	}))
	workspaceID = id.Generate()
	require.NoError(t, env.store.Workspaces().Create(context.Background(), store.CreateWorkspaceParams{
		ID:          workspaceID,
		OwnerUserID: userid.MustNew(env.userID),
		Title:       "ws",
	}))
	return workerID, workspaceID
}

// blankGrantCodeStore hands the authorization-code exchange a grant row whose
// user_id is blank.
//
// oauth_authorization_codes.user_id is a plain column, so a blank one is corrupt
// data rather than a programmer error -- and unlike every other mint site in
// the hub, /oauth/token is UNAUTHENTICATED: anyone who holds the code reaches
// it. Injecting the row is the only way to exercise the guard now that the
// store API refuses to create a blank-id user.
type blankGrantCodeStore struct {
	store.Store
	codes store.OAuthAuthorizationCodeStore
}

func (s blankGrantCodeStore) OAuthAuthorizationCodes() store.OAuthAuthorizationCodeStore {
	return s.codes
}

type blankGrantCodes struct {
	store.OAuthAuthorizationCodeStore
	row *store.OAuthAuthorizationCode
}

func (s blankGrantCodes) GetActive(context.Context, string, time.Time) (*store.OAuthAuthorizationCode, error) {
	return s.row, nil
}

// TestAPIAuth_AuthorizationCode_BlankUserIDIsInvalidGrantNotPanic pins the mint
// guard in handleTokenAuthorizationCode.
//
// This test sets PKCE verification up to SUCCEED here, so the request gets
// all the way past the checks that would otherwise mask the guard: only the
// blank user_id can produce the invalid_grant. Fold that mint into MustNew
// and this becomes a panic inside an unauthenticated HTTP handler -- a torn
// connection for the caller, and in a shared process it takes the request
// goroutine with it.
func TestAPIAuth_AuthorizationCode_BlankUserIDIsInvalidGrantNotPanic(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	hubtestutil.CreateTestAdmin(t, st)

	tv, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	_, sc := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st})
	t.Cleanup(sc.Stop)

	verifier, challenge := pkceVerifierAndChallenge()
	wrapped := blankGrantCodeStore{
		Store: st,
		codes: blankGrantCodes{
			OAuthAuthorizationCodeStore: st.OAuthAuthorizationCodes(),
			row: &store.OAuthAuthorizationCode{
				Code: "grant-code", UserID: "", CodeChallenge: challenge,
				InstallationName: "laptop", ExpiresAt: time.Now().Add(time.Hour),
			},
		},
	}

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	closer := &recordingBearerCloser{}
	service.NewOAuthServerHandler(service.OAuthServerDeps{Store: wrapped, Validator: tv, Lifecycle: auth.NewCredentialLifecycleEffects(sc, closer, closer), HubURL: func() string { return srv.URL }}).
		RegisterRoutes(mux)

	resp, err := http.PostForm(srv.URL+"/oauth/token", url.Values{
		"grant_type":    {service.GrantTypeAuthorizationCode},
		"client_id":     {oauthapp.ControlCLIClientID},
		"redirect_uri":  {"http://127.0.0.1:54321/callback"},
		"code":          {"grant-code"},
		"code_verifier": {verifier},
	})
	require.NoError(t, err, "the handler answers rather than tearing down the connection")
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "invalid_grant", body["error"],
		"a blank grant user_id reads as an unusable grant, not an internal error")
}

// A store read failure on the activation leg must answer 500. It must NOT
// render the page for the other kind of grant.
//
// The lookup used to fold a transient failure into the same nil as "unknown
// code": the GET then drew the ISSUANCE heading, with the hub-administration
// checkbox, for a step-up grant that widens nothing -- and the POST beside it
// resolved a scope, approved the grant, and skipped the elevation the approval
// exists to write.
func TestAPIAuth_Activate_GrantLookupFailureIsInternal(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	ctx := context.Background()
	userCode := verifycode.Generate()
	deviceCode := id.Generate()
	require.NoError(t, env.store.DeviceAuthorizations().Create(ctx, store.CreateDeviceAuthorizationParams{
		ClientID:   oauthapp.ControlCLIClientID,
		DeviceCode: deviceCode, UserCode: userCode, DeviceName: "laptop", ExpiresAt: time.Now().Add(time.Hour),
	}))
	forcedErr := errors.New("sensitive grant lookup failure")
	wrapped := deviceAuthorizationOverrideStore{
		Store: env.store,
		device: deviceAuthorizationOverride{
			DeviceAuthorizationStore: env.store.DeviceAuthorizations(),
			getByUserCode: func(context.Context, string) (*store.DeviceAuthorization, error) {
				return nil, forcedErr
			},
		},
	}
	mux := http.NewServeMux()
	h := service.NewOAuthServerHandler(service.OAuthServerDeps{
		Store:     wrapped,
		Validator: env.validator,
		Lifecycle: auth.NewCredentialLifecycleEffects(env.cache, noopBearerCloser{}, nil),
		HubURL:    func() string { return env.server.URL },
	})
	h.Now = env.now
	h.RegisterRoutes(mux)
	cookie := env.elevatedAdminCookie(t)

	get := httptest.NewRequest(http.MethodGet, "/oauth/device?user_code="+verifycode.Format(userCode), nil)
	get.AddCookie(cookie)
	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, get)
	assert.Equal(t, http.StatusInternalServerError, getRec.Code)
	assert.NotContains(t, getRec.Body.String(), forcedErr.Error())
	assert.NotContains(t, getRec.Body.String(), "Authorize CLI device",
		"a grant the hub could not read must not render as an issuance")

	post := httptest.NewRequest(http.MethodPost, "/oauth/device",
		strings.NewReader(url.Values{"user_code": {verifycode.Format(userCode)}, "decision": {"allow"}}.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.AddCookie(cookie)
	postRec := httptest.NewRecorder()
	mux.ServeHTTP(postRec, post)
	assert.Equal(t, http.StatusInternalServerError, postRec.Code)
	assert.NotContains(t, postRec.Body.String(), forcedErr.Error())

	row, err := env.store.DeviceAuthorizations().Get(ctx, deviceCode)
	require.NoError(t, err)
	assert.Zero(t, row.Approved, "a lookup the hub could not perform must approve nothing")
}

// conflictingDeviceAuthorizations fails the first `remaining` inserts with the
// uniqueness conflict a user-code collision raises, and records every code the
// handler drew.
type conflictingDeviceAuthorizations struct {
	store.DeviceAuthorizationStore
	remaining *int
	drawn     *[]string
}

func (s conflictingDeviceAuthorizations) Create(ctx context.Context, p store.CreateDeviceAuthorizationParams) error {
	*s.drawn = append(*s.drawn, p.UserCode)
	if *s.remaining > 0 {
		*s.remaining--
		return fmt.Errorf("%w: device_authorizations.user_code", store.ErrConflict)
	}
	return s.DeviceAuthorizationStore.Create(ctx, p)
}

// A user code is six characters from a 31-symbol alphabet, and the column is
// UNIQUE, so a healthy hub reaches a collision. The insert draws a fresh code
// and tries again rather than answering 500 to an anonymous caller.
func TestAPIAuth_DeviceCode_CollidingUserCodeIsRedrawn(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		collisions int
		wantDraws  int
		wantStatus int
	}{
		{
			name: "one collision is redrawn", collisions: 1,
			wantDraws: 2, wantStatus: http.StatusOK,
		},
		{
			name: "the last allowed draw still succeeds", collisions: service.DeviceGrantDrawLimit - 1,
			wantDraws: service.DeviceGrantDrawLimit, wantStatus: http.StatusOK,
		},
		{
			name: "a store that only collides is an internal error", collisions: service.DeviceGrantDrawLimit,
			wantDraws: service.DeviceGrantDrawLimit, wantStatus: http.StatusInternalServerError,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			env := setupAPIAuth(t)
			remaining := tc.collisions
			var drawn []string
			wrapped := deviceAuthorizationOverrideStore{
				Store: env.store,
				device: deviceAuthorizationOverride{
					DeviceAuthorizationStore: env.store.DeviceAuthorizations(),
					create: conflictingDeviceAuthorizations{
						DeviceAuthorizationStore: env.store.DeviceAuthorizations(),
						remaining:                &remaining,
						drawn:                    &drawn,
					}.Create,
				},
			}
			mux := http.NewServeMux()
			h := service.NewOAuthServerHandler(service.OAuthServerDeps{
				Store:     wrapped,
				Validator: env.validator,
				Lifecycle: auth.NewCredentialLifecycleEffects(env.cache, noopBearerCloser{}, nil),
				HubURL:    func() string { return env.server.URL },
			})
			h.Now = env.now
			h.RegisterRoutes(mux)

			req := httptest.NewRequest(http.MethodPost, "/oauth/device-authorization",
				strings.NewReader(url.Values{"client_id": {oauthapp.ControlCLIClientID}, "installation_name": {"laptop"}}.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			require.Equal(t, tc.wantStatus, rec.Code)

			// Every attempt draws a FRESH code. Retrying the same one would
			// collide for ever.
			assert.Len(t, drawn, tc.wantDraws)
			assert.Len(t, slices.Compact(slices.Sorted(slices.Values(drawn))), len(drawn),
				"each attempt must draw a new user code")

			if tc.wantStatus != http.StatusOK {
				return
			}
			var body map[string]any
			require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
			assert.Equal(t, verifycode.Format(drawn[len(drawn)-1]), body["user_code"],
				"the response must report the code that was actually stored")
			row, err := env.store.DeviceAuthorizations().GetByUserCode(context.Background(), drawn[len(drawn)-1])
			require.NoError(t, err)
			assert.Equal(t, body["device_code"], row.DeviceCode)
		})
	}
}

// An approval is ONCE, and the second submit changes nothing.
//
// The approve statement used to match any unconsumed live row, so a second
// POST rewrote user_id and the granted scope on a grant nobody consumed yet. The
// credential then went to whoever approved LAST -- and carried whatever scope
// that submit asked for -- while the first approver read "Device authorized".
// A double click, a re-submitted form, or a second person given the code all
// reach it, and the window is a whole grant TTL for a code nobody polls.
func TestAPIAuth_DeviceCode_ASecondApprovalChangesNothing(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	cookie := env.elevatedAdminCookie(t)

	deviceCode, userCode := startDeviceAuthorization(t, env, url.Values{
		"installation_name": {"server-1"}, "scope": {"workspace:read"},
	})

	first, err := postForm(env.server.URL+"/oauth/device", url.Values{"user_code": {userCode}, "decision": {"allow"}}, cookie)
	require.NoError(t, err)
	defer func() { _ = first.Body.Close() }()
	require.Equal(t, http.StatusOK, first.StatusCode)

	// A second submit of the same form.
	second, err := postForm(env.server.URL+"/oauth/device",
		url.Values{"user_code": {userCode}, "decision": {"allow"}}, cookie)
	require.NoError(t, err)
	defer func() { _ = second.Body.Close() }()
	assert.Equal(t, http.StatusBadRequest, second.StatusCode, "an approved grant cannot be approved again")

	row, err := env.store.DeviceAuthorizations().Get(context.Background(), deviceCode)
	require.NoError(t, err)
	assert.Equal(t, "workspace:read", row.GrantedScopes,
		"the refused submit must not change what the first approval bound")

	// The credential the CLI collects carries what the FIRST approval granted.
	token, err := http.PostForm(env.server.URL+"/oauth/token", url.Values{
		"grant_type":  {service.GrantTypeDeviceCode},
		"client_id":   {oauthapp.ControlCLIClientID},
		"device_code": {deviceCode},
	})
	require.NoError(t, err)
	defer func() { _ = token.Body.Close() }()
	require.Equal(t, http.StatusOK, token.StatusCode)
	var minted map[string]any
	require.NoError(t, json.NewDecoder(token.Body).Decode(&minted))
	assert.Equal(t, "workspace:read", minted["scope"],
		"the minted credential carries the first consent, not the second")
}

// TestAPIAuth_Refresh_RequiresClientAuthentication pins the RFC 6749
// section 6/3.2.1 rule the code, device and revocation legs already held: a
// client that was issued credentials authenticates on the refresh grant too.
// Without it, a leaked CONFIDENTIAL refresh bearer rotated freely -- the app
// secret, the half of the proof the registration exists to add, protected
// nothing on exactly the leg that mints the long-lived pair.
func TestAPIAuth_Refresh_RequiresClientAuthentication(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)

	mint := func() (tokenID, refresh string) {
		tokenID = id.Generate()
		refresh = auth.MintAccessSecret()
		require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
			ID:               tokenID,
			UserID:           userid.MustNew(env.userID),
			ClientID:         oauthapp.ControlCLIClientID,
			InstallationName: "test",
			GrantedScopes:    authscope.NonAdminGrant().String(),
			SecretHash:       env.validator.HashSecret(auth.MintAccessSecret()),
			RefreshHash:      env.validator.HashSecret(refresh),
		}))
		return tokenID, refresh
	}

	t.Run("no client_id is an invalid_client 401", func(t *testing.T) {
		tokenID, refresh := mint()
		resp, err := http.PostForm(env.server.URL+"/oauth/token", url.Values{
			"grant_type":    {service.GrantTypeRefreshToken},
			"refresh_token": {auth.FormatBearer(auth.BearerKindAPI, tokenID, refresh)},
		})
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		var body map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		assert.Equal(t, "invalid_client", body["error"])
	})

	t.Run("a different app's client_id is refused", func(t *testing.T) {
		tokenID, refresh := mint()
		resp, err := http.PostForm(env.server.URL+"/oauth/token", url.Values{
			"grant_type":    {service.GrantTypeRefreshToken},
			"client_id":     {oauthapp.ServiceAccountClientID},
			"refresh_token": {auth.FormatBearer(auth.BearerKindAPI, tokenID, refresh)},
		})
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		var body map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		assert.Equal(t, "invalid_grant", body["error"],
			"the grant row binds the credential to the app it was issued to")
	})
}

// TestAPIAuth_Revoke_ANamedRetiredAppIsTheIdempotentSuccess pins the RFC 7009
// section 2.2 branch for the caller that names itself: the retirement cascade
// already revoked the credential, so the retrying client receives the
// documented 200 rather than a 401 that reads as "your credentials are wrong".
func TestAPIAuth_Revoke_ANamedRetiredAppIsTheIdempotentSuccess(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)

	tokenID := id.Generate()
	refresh := auth.MintAccessSecret()
	require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:               tokenID,
		UserID:           userid.MustNew(env.userID),
		ClientID:         oauthapp.ControlCLIClientID,
		InstallationName: "test",
		GrantedScopes:    authscope.NonAdminGrant().String(),
		SecretHash:       env.validator.HashSecret(auth.MintAccessSecret()),
		RefreshHash:      env.validator.HashSecret(refresh),
	}))
	_, err := env.store.OAuthClients().Revoke(context.Background(), store.OAuthClientOwnershipParams{
		ClientID: oauthapp.ControlCLIClientID, CallerUserID: userid.MustNew(env.userID), CallerIsAdmin: true,
	})
	require.NoError(t, err)

	resp, err := http.PostForm(env.server.URL+"/oauth/revoke", url.Values{
		"token":     {auth.FormatBearer(auth.BearerKindAPI, tokenID, auth.MintAccessSecret())},
		"client_id": {oauthapp.ControlCLIClientID},
	})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"a retired app's credentials were revoked by the cascade; the retrying client gets the 200")
}

// TestAPIAuth_Refresh_GraceRetryKeepsTheCallersOwnNarrowing pins the CAS-race
// half of the retry path: a caller that loses the rotation race to a winner
// asking a DIFFERENT narrowing still receives ITS narrowing in the response's
// scope. Re-emitting the winner's reachable grant handed a caller a response
// that stated permissions it explicitly asked to drop -- the exact outcome the
// flight-key comment promises cannot happen, and which singleflight cannot
// prevent across two processes or two hubs.
func TestAPIAuth_Refresh_GraceRetryKeepsTheCallersOwnNarrowing(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	tokenID := id.Generate()
	previousRefresh := auth.MintAccessSecret()
	previousHash := env.validator.HashSecret(previousRefresh)
	now := time.Now()
	derived := env.validator.DeriveRefreshBearerPair(
		auth.BearerKindAPI, tokenID, previousHash, now, auth.AccessTokenTTL, auth.RefreshTokenTTL,
	)
	require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID: tokenID, UserID: userid.MustNew(env.userID), ClientID: oauthapp.ControlCLIClientID, InstallationName: "test",
		GrantedScopes: authscope.NonAdminGrant().String(),
		SecretHash:    env.validator.HashSecret(auth.MintAccessSecret()), RefreshHash: previousHash,
	}))
	storedExpiry := now.Add(30 * time.Second)
	refreshExpiry := now.Add(time.Hour)
	graceExpiry := now.Add(auth.RefreshReuseGrace)
	rotated, err := env.store.APITokens().RotateRefresh(context.Background(), store.RotateAPITokenRefreshParams{
		ID: tokenID, NewSecretHash: derived.AccessHash, NewExpiresAt: &storedExpiry,
		NewRefreshHash: derived.RefreshHash, NewRefreshExpiresAt: &refreshExpiry,
		// The winner's rotation kept the grant unchanged, as a rotation with
		// no narrowing ask does.
		NewGrantedScopes:         authscope.NonAdminGrant().String(),
		PreviousRefreshHash:      previousHash,
		PreviousRefreshExpiresAt: &graceExpiry,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), rotated)

	// The racing caller presents the PREVIOUS refresh (the grace path) and
	// asks to narrow to one permission of the wide stored grant.
	resp, err := http.PostForm(env.server.URL+"/oauth/token", url.Values{
		"grant_type":    {service.GrantTypeRefreshToken},
		"client_id":     {oauthapp.ControlCLIClientID},
		"refresh_token": {auth.FormatBearer(auth.BearerKindAPI, tokenID, previousRefresh)},
		"scope":         {"file:read"},
	})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	// file:read closes to include worker:read (its channel), in the canonical
	// enum order. The wide grant's other permissions -- git:write, terminal
	// writes, everything else -- must NOT appear.
	assert.Equal(t, "worker:read file:read", body["scope"],
		"the retry reports the caller's own narrowing closed, not the winner's stored grant")
}

// TestAPIAuth_TheRevokeLegCarriesTheAnonymousBudget pins the fourth anonymous
// leg's mounting. The budget used to be a per-handler first line, and revoke
// was the leg that shipped without it: an unauthenticated script could post
// token guesses against the store with no budget. anonymousLeg now applies it
// at mounting, so the second request from one address within the window is
// answered before any store read runs.
func TestAPIAuth_TheRevokeLegCarriesTheAnonymousBudget(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)

	set := servicetest.NewSettingsManager(t, env.store, nil)
	key, ok := ratelimit.LimitKey(ratelimit.OpOAuthAnonymous)
	require.True(t, ok)
	require.NoError(t, key.Set(context.Background(), set, ratelimit.LimitValue{
		Enabled: true, MaxAttempts: 1, WindowSeconds: 600,
	}))
	limiter := ratelimit.NewManager(set)

	mux := http.NewServeMux()
	service.NewOAuthServerHandler(service.OAuthServerDeps{
		Store: env.store, Validator: env.validator,
		Lifecycle: auth.NewCredentialLifecycleEffects(env.cache, env.closer, env.closer),
		HubURL:    func() string { return env.server.URL },
		Limiter:   limiter,
	}).RegisterRoutes(mux)

	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/oauth/revoke",
			strings.NewReader(url.Values{"token": {"lmx_a1_guess"}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = "203.0.113.7:51234"
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	first := post()
	assert.Equal(t, http.StatusOK, first.Code, "an unknown token is the RFC 7009 idempotent 200")

	second := post()
	assert.Equal(t, http.StatusTooManyRequests, second.Code,
		"the revoke leg answers from the shared anonymous budget, not from a per-handler line")
	var body map[string]any
	require.NoError(t, json.NewDecoder(second.Body).Decode(&body))
	assert.Equal(t, "slow_down", body["error"])
}
