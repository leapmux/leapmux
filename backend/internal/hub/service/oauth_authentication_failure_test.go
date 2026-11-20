package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type oauthAuthenticationFaults struct {
	sessionErr, tokenErr                  error
	sessionReads, tokenReads, grantWrites atomic.Int64
}
type oauthAuthenticationStore struct {
	store.Store
	faults *oauthAuthenticationFaults
}

func (s oauthAuthenticationStore) Sessions() store.SessionStore {
	return oauthAuthenticationSessions{s.Store.Sessions(), s.faults}
}
func (s oauthAuthenticationStore) APITokens() store.APITokenStore {
	return oauthAuthenticationTokens{s.Store.APITokens(), s.faults}
}
func (s oauthAuthenticationStore) DeviceAuthorizations() store.DeviceAuthorizationStore {
	return oauthAuthenticationDevices{s.Store.DeviceAuthorizations(), s.faults}
}
func (s oauthAuthenticationStore) OAuthAuthorizationCodes() store.OAuthAuthorizationCodeStore {
	return oauthAuthenticationCodes{s.Store.OAuthAuthorizationCodes(), s.faults}
}

type oauthAuthenticationSessions struct {
	store.SessionStore
	faults *oauthAuthenticationFaults
}

func (s oauthAuthenticationSessions) ValidateWithUser(ctx context.Context, session string, now time.Time) (*store.SessionWithUser, error) {
	s.faults.sessionReads.Add(1)
	if s.faults.sessionErr != nil {
		return nil, s.faults.sessionErr
	}
	return s.SessionStore.ValidateWithUser(ctx, session, now)
}

type oauthAuthenticationTokens struct {
	store.APITokenStore
	faults *oauthAuthenticationFaults
}

func (s oauthAuthenticationTokens) GetByID(ctx context.Context, token string) (*store.APIToken, error) {
	s.faults.tokenReads.Add(1)
	if s.faults.tokenErr != nil {
		return nil, s.faults.tokenErr
	}
	return s.APITokenStore.GetByID(ctx, token)
}
func (s oauthAuthenticationTokens) Create(ctx context.Context, p store.CreateAPITokenParams) error {
	s.faults.grantWrites.Add(1)
	return s.APITokenStore.Create(ctx, p)
}

type oauthAuthenticationDevices struct {
	store.DeviceAuthorizationStore
	faults *oauthAuthenticationFaults
}

func (s oauthAuthenticationDevices) Create(ctx context.Context, p store.CreateDeviceAuthorizationParams) error {
	s.faults.grantWrites.Add(1)
	return s.DeviceAuthorizationStore.Create(ctx, p)
}

type oauthAuthenticationCodes struct {
	store.OAuthAuthorizationCodeStore
	faults *oauthAuthenticationFaults
}

func (s oauthAuthenticationCodes) Create(ctx context.Context, p store.CreateOAuthAuthorizationCodeParams) error {
	s.faults.grantWrites.Add(1)
	return s.OAuthAuthorizationCodeStore.Create(ctx, p)
}

func oauthAuthenticationServer(t *testing.T, env *apiAuthEnv, faults *oauthAuthenticationFaults) *httptest.Server {
	t.Helper()
	st := oauthAuthenticationStore{env.store, faults}
	validator, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	h := newOAuthServerHandler(service.OAuthServerDeps{
		SessionHasher: hubtestutil.TestSecretHasher(), Store: st, Validator: validator,
		Lifecycle: auth.NewCredentialLifecycleEffects(nil, nil, nil), Settings: env.set,
		HubURL: func() string { return env.server.URL },
	})
	h.Now = env.now
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestOAuthConsentAuthenticationPreservesInfrastructureErrors(t *testing.T) {
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/oauth/authorize"}, {http.MethodHead, "/oauth/authorize"}, {http.MethodPost, "/oauth/consent"},
		{http.MethodGet, "/oauth/device"}, {http.MethodHead, "/oauth/device"}, {http.MethodPost, "/oauth/device"},
	} {
		t.Run(route.method+route.path, func(t *testing.T) {
			logs := testutil.CaptureDefaultLogger(t)
			env := setupAPIAuth(t)
			cookie := env.adminCookie(t)
			faults := &oauthAuthenticationFaults{sessionErr: errors.New("sensitive consent database fault")}
			server := oauthAuthenticationServer(t, env, faults)
			req, err := http.NewRequestWithContext(t.Context(), route.method, server.URL+route.path+"?keep=return", nil)
			require.NoError(t, err)
			req.AddCookie(cookie)
			response, err := noRedirectClient().Do(req)
			require.NoError(t, err)
			defer func() { _ = response.Body.Close() }()
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			assert.Equal(t, http.StatusInternalServerError, response.StatusCode)
			assert.Empty(t, response.Header.Get("Location"))
			assert.Empty(t, response.Header.Get("Set-Cookie"))
			assert.NotContains(t, string(body), "sensitive consent database fault")
			if route.method == http.MethodHead {
				assert.Empty(t, body)
			} else {
				assert.Equal(t, "internal server error\n", string(body))
			}
			assert.EqualValues(t, 1, faults.sessionReads.Load())
			assert.Zero(t, faults.tokenReads.Load())
			assert.Zero(t, faults.grantWrites.Load())
			assert.Contains(t, logs.String(), "OAuth session authentication failed")
			assert.Contains(t, logs.String(), "sensitive consent database fault")
			assert.NotContains(t, logs.String(), cookie.Value)
		})
	}
}

func TestOAuthConsentKeepsCredentialRefusalsAndMethodChecks(t *testing.T) {
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/oauth/authorize"}, {http.MethodHead, "/oauth/authorize"}, {http.MethodPost, "/oauth/consent"},
		{http.MethodGet, "/oauth/device"}, {http.MethodHead, "/oauth/device"}, {http.MethodPost, "/oauth/device"},
	} {
		for _, credential := range []string{"absent", "malformed", "wrong secret", "missing row", "wrong method"} {
			t.Run(route.method+route.path+"/"+credential, func(t *testing.T) {
				logs := testutil.CaptureDefaultLogger(t)
				env := setupAPIAuth(t)
				cookie := env.adminCookie(t)
				faults := &oauthAuthenticationFaults{}
				if credential == "missing row" {
					faults.sessionErr = fmt.Errorf("lookup: %w", store.ErrNotFound)
				}
				if credential == "wrong method" {
					faults.sessionErr = errors.New("must not read authentication")
				}
				server := oauthAuthenticationServer(t, env, faults)
				method := route.method
				if credential == "wrong method" {
					method = http.MethodPut
				}
				req, err := http.NewRequestWithContext(t.Context(), method, server.URL+route.path+"?keep=return", nil)
				require.NoError(t, err)
				switch credential {
				case "absent":
				case "malformed":
					req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: "invalid"})
				case "wrong secret":
					session, _, ok := auth.ParseSessionToken(cookie.Value)
					require.True(t, ok)
					req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: auth.FormatSessionToken(session, id.Generate())})
				default:
					req.AddCookie(cookie)
				}
				response, err := noRedirectClient().Do(req)
				require.NoError(t, err)
				defer func() { _ = response.Body.Close() }()
				body, err := io.ReadAll(response.Body)
				require.NoError(t, err)
				if credential == "wrong method" {
					assert.Equal(t, http.StatusMethodNotAllowed, response.StatusCode)
					assert.Zero(t, faults.sessionReads.Load())
				} else if method == http.MethodPost {
					assert.Equal(t, http.StatusUnauthorized, response.StatusCode)
					assert.Empty(t, response.Header.Get("Location"))
				} else {
					assert.Equal(t, http.StatusFound, response.StatusCode)
					location, err := response.Location()
					require.NoError(t, err)
					assert.Equal(t, "/login", location.Path)
					assert.Equal(t, route.path+"?keep=return", location.Query().Get("redirect"))
				}
				if method == http.MethodHead {
					assert.Empty(t, body)
				}
				assert.Empty(t, response.Header.Get("Set-Cookie"))
				assert.Zero(t, faults.grantWrites.Load())
				assert.NotContains(t, logs.String(), "OAuth session authentication failed")
			})
		}
	}
}

func TestOAuthAppStepUpAuthenticationPreservesInfrastructureErrors(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)
	env := setupAPIAuth(t)
	_, bearer := cliCredential(t, env, "authentication fault")
	faults := &oauthAuthenticationFaults{tokenErr: errors.New("sensitive app database fault")}
	server := oauthAuthenticationServer(t, env, faults)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/oauth/step-up", strings.NewReader(url.Values{"installation_name": {"fault"}}.Encode()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+bearer)
	response, err := server.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusInternalServerError, response.StatusCode)
	assert.Equal(t, "internal server error\n", string(body))
	assert.Empty(t, response.Header.Get("WWW-Authenticate"))
	assert.Empty(t, response.Header.Get("Location"))
	assert.NotContains(t, string(body), "invalid_token")
	assert.NotContains(t, string(body), "sensitive app database fault")
	assert.EqualValues(t, 1, faults.tokenReads.Load())
	assert.Zero(t, faults.grantWrites.Load())
	assert.Contains(t, logs.String(), "app step-up authentication failed")
	assert.Contains(t, logs.String(), "sensitive app database fault")
	assert.NotContains(t, logs.String(), bearer)
}

func TestOAuthAppStepUpKeepsCredentialRefusalsAndMethodCheck(t *testing.T) {
	for _, credential := range []string{"absent", "malformed", "wrong secret", "missing row", "wrong method"} {
		t.Run(credential, func(t *testing.T) {
			env := setupAPIAuth(t)
			token, bearer := cliCredential(t, env, "refused authentication")
			faults := &oauthAuthenticationFaults{}
			if credential == "missing row" {
				faults.tokenErr = fmt.Errorf("lookup: %w", store.ErrNotFound)
			}
			if credential == "wrong method" {
				faults.tokenErr = errors.New("must not read authentication")
			}
			server := oauthAuthenticationServer(t, env, faults)
			method := http.MethodPost
			if credential == "wrong method" {
				method = http.MethodGet
			}
			req, err := http.NewRequestWithContext(t.Context(), method, server.URL+"/oauth/step-up", nil)
			require.NoError(t, err)
			switch credential {
			case "absent":
			case "malformed":
				req.Header.Set("Authorization", "Bearer lmx_bad")
			case "wrong secret":
				req.Header.Set("Authorization", "Bearer "+auth.FormatBearer(auth.BearerKindAPI, token, auth.MintAccessSecret()))
			default:
				req.Header.Set("Authorization", "Bearer "+bearer)
			}
			response, err := server.Client().Do(req)
			require.NoError(t, err)
			defer func() { _ = response.Body.Close() }()
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			if credential == "wrong method" {
				assert.Equal(t, http.StatusMethodNotAllowed, response.StatusCode)
				assert.Zero(t, faults.tokenReads.Load())
			} else {
				assert.Equal(t, http.StatusUnauthorized, response.StatusCode)
				assert.Contains(t, string(body), `"error":"invalid_token"`)
				assert.Equal(t, `Bearer error="invalid_token"`, response.Header.Get("WWW-Authenticate"))
			}
			assert.Zero(t, faults.grantWrites.Load())
		})
	}
}

// A credential issued through a browser on its own computer must verify
// through that browser, not through the device ceremony.
func TestOAuthAppStepUp_BrowserIssuedCredentialRefused(t *testing.T) {
	t.Parallel()
	env := setupAPIAuth(t)

	// Real authorization-code exchange: the bearer of the minted token.
	code, verifier := issueLocalRedirectCode(t, env)
	resp, err := http.PostForm(env.server.URL+"/oauth/token", url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {oauthapp.ControlCLIClientID},
		"redirect_uri":  {"http://127.0.0.1:54321/callback"},
		"code":          {code},
		"code_verifier": {verifier},
	})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var tokens map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&tokens))
	bearer, _ := tokens["access_token"].(string)
	require.NotEmpty(t, bearer)

	// /oauth/step-up refuses with browser_step_up_required.
	stepUpResp := startStepUp(t, env, bearer, "browser-issued")
	assert.Equal(t, http.StatusBadRequest, stepUpResp.StatusCode)
	body, err := io.ReadAll(stepUpResp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), `"error":"browser_step_up_required"`)
}

// A credential from a device-code exchange or IssueAPIToken can start the
// device ceremony.
func TestOAuthAppStepUp_OtherIssuancesAllowed(t *testing.T) {
	t.Parallel()
	env := setupAPIAuth(t)

	// Device-code issuance: the cliCredential helper mints one.
	_, bearer := cliCredential(t, env, "device-issued")
	stepUpResp := startStepUp(t, env, bearer, "device-issued")
	assert.Equal(t, http.StatusOK, stepUpResp.StatusCode)
}

// redeemStepUpCodeAt presents one step-up code to a chosen server, so a test
// can drive the failing store and then the working one over the same code.
func redeemStepUpCodeAt(t *testing.T, server *httptest.Server, clientID, code, verifier string, cookie *http.Cookie) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		server.URL+"/oauth/token",
		strings.NewReader(url.Values{
			"grant_type":    {"authorization_code"},
			"client_id":     {clientID},
			"redirect_uri":  {"/"},
			"code":          {code},
			"code_verifier": {verifier},
		}.Encode()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := server.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// A redemption whose credential the Hub cannot read answers 500, not a
// credential refusal: the caller did nothing wrong and must not be told the
// code is spent. The exchange consumes the code only inside the transaction
// that follows authentication, so the same code must redeem once the store
// reads again.
func TestStepUpCodeExchange_AFailingCredentialReadAnswersInternalErrorAndKeepsTheCode(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	ctx := context.Background()
	cookie := env.adminCookie(t)
	sessionID := hubtestutil.SessionIDFromCookie(t, cookie.Value)
	code, verifier := seedStepUpCode(t, env, oauthapp.DesktopClientID, sessionID, "")

	faults := &oauthAuthenticationFaults{sessionErr: errors.New("sensitive credential read failure")}
	failing := oauthAuthenticationServer(t, env, faults)
	refused := redeemStepUpCodeAt(t, failing, oauthapp.DesktopClientID, code, verifier, cookie)
	assert.Equal(t, http.StatusInternalServerError, refused.StatusCode,
		"a credential the Hub cannot read is a Hub failure, not a refusal of the code")
	assert.NotContains(t, readBody(t, refused), "sensitive credential read failure")

	working := oauthAuthenticationServer(t, env, &oauthAuthenticationFaults{})
	redeemed := redeemStepUpCodeAt(t, working, oauthapp.DesktopClientID, code, verifier, cookie)
	require.Equal(t, http.StatusOK, redeemed.StatusCode,
		"the failed read must leave the code redeemable")
	assert.Equal(t, true, decodeJSONBody(t, redeemed)["elevated"])

	row, err := env.store.Sessions().GetByID(ctx, sessionID, time.Now().UTC())
	require.NoError(t, err)
	assert.NotNil(t, row.ElevationExpiresAt, "the working store's redemption stamps the window")
}

// The token rung of the same exchange: a step-up code that identifies a
// command-line credential redeems with THAT credential's bearer, and the
// window lands on the credential's own row.
func TestStepUpCodeExchange_ATokenCodeRedeemsWithItsBearer(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	ctx := context.Background()
	tokenID, bearer := cliCredential(t, env, "operator-laptop")
	code, verifier := seedStepUpCode(t, env, oauthapp.ControlCLIClientID, "", tokenID)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		env.server.URL+"/oauth/token",
		strings.NewReader(url.Values{
			"grant_type":    {"authorization_code"},
			"client_id":     {oauthapp.ControlCLIClientID},
			"redirect_uri":  {"/"},
			"code":          {code},
			"code_verifier": {verifier},
		}.Encode()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := env.server.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	require.Equal(t, http.StatusOK, resp.StatusCode, "the bearer that the code identifies redeems it")
	assert.Equal(t, true, decodeJSONBody(t, resp)["elevated"])

	row, err := env.store.APITokens().GetByID(ctx, tokenID)
	require.NoError(t, err)
	assert.NotNil(t, row.ElevationProvenAt, "the token is elevated")
	assert.NotNil(t, row.ElevationExpiresAt)
}
