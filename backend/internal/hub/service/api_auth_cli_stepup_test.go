package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/util/verifycode"
)

// Step-up for a command-line credential, end to end.
//
// The gate that protects the hub's settings, the user surface and the mint
// used to admit a bearer with no check: it had no row to stamp, so possession
// of the credential file was the whole of the check. These pin the ceremony
// that replaced that -- ask with the credential, approve in a browser, poll
// -- and the two properties it exists to have: the CLI cannot approve its
// own request, and approving grants a WINDOW rather than a credential.

// cliCredential mints a command-line credential for the environment's user and
// returns its row id and its bearer secret.
func cliCredential(t *testing.T, env *apiAuthEnv, name string) (tokenID, bearer string) {
	t.Helper()
	tokenID = id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:         tokenID,
		UserID:     userid.MustNew(env.userID),
		ClientType: "cli",
		ClientName: name,
		SecretHash: env.validator.HashSecret(secret),
	}))
	return tokenID, auth.FormatBearer(auth.BearerKindAPI, tokenID, secret)
}

// startStepUp runs the /auth/cli/elevate-authorization leg with a bearer.
//
// deviceName is what the CALLER labels its own request, which is why it is a
// parameter: one test sets it to the owner's laptop to prove the activation
// page never identifies a credential from it.
func startStepUp(t *testing.T, env *apiAuthEnv, bearer, deviceName string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		env.server.URL+"/auth/cli/elevate-authorization",
		strings.NewReader(url.Values{"device_name": {deviceName}}.Encode()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := env.server.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

type stepUpGrant struct {
	DeviceCode string `json:"device_code"`
	UserCode   string `json:"user_code"`
	Interval   int    `json:"interval"`
	ExpiresIn  int    `json:"expires_in"`
}

func decodeStepUpGrant(t *testing.T, resp *http.Response) stepUpGrant {
	t.Helper()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var grant stepUpGrant
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&grant))
	require.NotEmpty(t, grant.DeviceCode)
	require.NotEmpty(t, grant.UserCode)
	return grant
}

// pollStepUp polls /auth/cli/token once for a step-up grant.
func pollStepUp(t *testing.T, env *apiAuthEnv, deviceCode string) *http.Response {
	t.Helper()
	resp, err := env.server.Client().PostForm(env.server.URL+"/auth/cli/token", url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {deviceCode},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// approveStepUp posts the activation form as an elevated browser session.
func approveStepUp(t *testing.T, env *apiAuthEnv, userCode string, cookie *http.Cookie) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		env.server.URL+"/auth/cli/activate",
		strings.NewReader(url.Values{"user_code": {userCode}}.Encode()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	resp, err := env.server.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestCLIStepUp_BrowserApprovalStampsTheWindow(t *testing.T) {
	env := setupAPIAuth(t)
	ctx := context.Background()
	tokenID, bearer := cliCredential(t, env, "operator-laptop")

	grant := decodeStepUpGrant(t, startStepUp(t, env, bearer, "operator-laptop"))

	// Pending until a human approves. The poll answers the same
	// authorization_pending a login poll does, so the CLI runs one loop.
	pending := pollStepUp(t, env, grant.DeviceCode)
	assert.Equal(t, http.StatusBadRequest, pending.StatusCode)

	before, err := env.store.APITokens().GetByID(ctx, tokenID)
	require.NoError(t, err)
	require.Nil(t, before.ElevationExpiresAt, "nothing is granted before the approval")

	require.Equal(t, http.StatusOK, approveStepUp(t, env, grant.UserCode, env.elevatedAdminCookie(t)).StatusCode)

	after, err := env.store.APITokens().GetByID(ctx, tokenID)
	require.NoError(t, err)
	require.NotNil(t, after.ElevationProvenAt, "the approval proves the factor")
	require.NotNil(t, after.ElevationExpiresAt)
	assert.WithinDuration(t, after.ElevationProvenAt.Add(auth.ElevationWindow), *after.ElevationExpiresAt, time.Second)

	// Past the poll throttle: the pending poll above stamped last_polled_at,
	// and a second poll inside the interval answers slow_down.
	env.clock.advance(2 * time.Duration(grant.Interval) * time.Second)

	// The poll now reports success, and reports it as an ELEVATION -- a
	// step-up mints nothing, so a token pair here would turn a request to
	// verify a credential into a second credential.
	done := pollStepUp(t, env, grant.DeviceCode)
	require.Equal(t, http.StatusOK, done.StatusCode)
	var body map[string]any
	require.NoError(t, json.NewDecoder(done.Body).Decode(&body))
	assert.Equal(t, true, body["elevated"])
	assert.NotContains(t, body, "access_token", "a step-up issues no credential")
	assert.NotContains(t, body, "refresh_token")
}

// The property the whole design rests on: the credential file cannot approve
// its own request. Only a browser session that proved a factor can, which is
// deliberately somewhere this process cannot reach.
func TestCLIStepUp_TheCredentialCannotApproveItself(t *testing.T) {
	env := setupAPIAuth(t)
	ctx := context.Background()
	tokenID, bearer := cliCredential(t, env, "operator-laptop")
	grant := decodeStepUpGrant(t, startStepUp(t, env, bearer, "operator-laptop"))

	// The activation leg accepts session cookies only, so a bearer reaches
	// nothing there at all.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		env.server.URL+"/auth/cli/activate",
		strings.NewReader(url.Values{"user_code": {grant.UserCode}}.Encode()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := env.server.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	row, err := env.store.APITokens().GetByID(ctx, tokenID)
	require.NoError(t, err)
	assert.Nil(t, row.ElevationExpiresAt, "a bearer approving its own request grants nothing")
}

// An UNELEVATED session cannot approve either. The activation leg is a consent
// leg, so it demands a proven factor before it runs at all -- otherwise a
// stolen cookie would verify a stolen credential file.
func TestCLIStepUp_AnUnelevatedSessionCannotApprove(t *testing.T) {
	env := setupAPIAuth(t)
	ctx := context.Background()
	tokenID, bearer := cliCredential(t, env, "operator-laptop")
	grant := decodeStepUpGrant(t, startStepUp(t, env, bearer, "operator-laptop"))

	resp := approveStepUp(t, env, grant.UserCode, env.adminCookie(t))
	assert.NotEqual(t, http.StatusOK, resp.StatusCode)

	row, err := env.store.APITokens().GetByID(ctx, tokenID)
	require.NoError(t, err)
	assert.Nil(t, row.ElevationExpiresAt)
}

// The hub refuses a DELEGATION bearer at the ask. A worker mints it for an
// agent that reads untrusted input, so there is nobody to prompt -- and a
// ceremony
// it could start would end in a refusal it could not act on.
func TestCLIStepUp_RefusesACredentialThatCannotCarryOne(t *testing.T) {
	env := setupAPIAuth(t)

	// No credential at all.
	assert.Equal(t, http.StatusUnauthorized, startStepUp(t, env, "", "operator-laptop").StatusCode)

	workerID, _ := seedDelegationFixtures(t, env)
	delegationID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, env.store.DelegationTokens().Create(context.Background(), store.CreateDelegationTokenParams{
		ID:         delegationID,
		UserID:     userid.MustNew(env.userID),
		WorkerID:   workerID,
		SecretHash: env.validator.HashSecret(secret),
		ExpiresAt:  time.Now().Add(time.Hour),
	}))
	resp := startStepUp(t, env, auth.FormatBearer(auth.BearerKindDelegation, delegationID, secret), "operator-laptop")
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// One grant, one window. The row is single-use, so a device code kept from an
// earlier ceremony cannot re-elevate a credential whose window lapsed or
// dropped since then.
func TestCLIStepUp_AGrantIsSingleUse(t *testing.T) {
	env := setupAPIAuth(t)
	_, bearer := cliCredential(t, env, "operator-laptop")
	grant := decodeStepUpGrant(t, startStepUp(t, env, bearer, "operator-laptop"))

	require.Equal(t, http.StatusOK, approveStepUp(t, env, grant.UserCode, env.elevatedAdminCookie(t)).StatusCode)
	require.Equal(t, http.StatusOK, pollStepUp(t, env, grant.DeviceCode).StatusCode)

	env.clock.advance(2 * time.Duration(grant.Interval) * time.Second)
	assert.Equal(t, http.StatusBadRequest, pollStepUp(t, env, grant.DeviceCode).StatusCode,
		"a consumed grant cannot be exchanged twice")
}

// activationPage fetches the activation page for a user code, as a browser
// session that already proved a factor, and returns the rendered HTML.
func activationPage(t *testing.T, env *apiAuthEnv, userCode string, cookie *http.Cookie) string {
	t.Helper()
	pageURL := env.server.URL + "/auth/cli/activate?" + url.Values{"user_code": {userCode}}.Encode()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, pageURL, nil)
	require.NoError(t, err)
	req.AddCookie(cookie)
	resp, err := env.server.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(body)
}

// decodeJSONBody reads one JSON object response.
func decodeJSONBody(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	return body
}

// stepUpHandlerOn mounts a SECOND handler over a wrapped store, on the
// environment's own clock and the environment's session cache, so a test can
// fail one store write and watch the whole approval roll back.
func stepUpHandlerOn(t *testing.T, env *apiAuthEnv, st store.Store) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	h := service.NewAPIAuthHandler(service.APIAuthHandlerDeps{
		Store:     st,
		Validator: env.validator,
		Lifecycle: auth.NewCredentialLifecycleEffects(env.cache, noopBearerCloser{}, nil),
		HubURL:    func() string { return env.server.URL },
	})
	h.Now = env.clock.now
	h.RegisterRoutes(mux)
	return mux
}

// elevateFailStore fails the credential elevation write.
//
// RunInTransaction re-wraps the transaction store, so the failure lands INSIDE
// the approval's transaction. That is the whole point: the test proves the
// approval rolls back with it.
type elevateFailStore struct {
	store.Store
	err error
}

func (s elevateFailStore) APITokens() store.APITokenStore {
	return elevateFailTokens{APITokenStore: s.Store.APITokens(), err: s.err}
}

func (s elevateFailStore) RunInTransaction(ctx context.Context, fn func(store.Store) error) error {
	return s.Store.RunInTransaction(ctx, func(tx store.Store) error {
		return fn(elevateFailStore{Store: tx, err: s.err})
	})
}

type elevateFailTokens struct {
	store.APITokenStore
	err error
}

func (s elevateFailTokens) Elevate(context.Context, store.ElevateAPITokenParams, time.Time) (int64, error) {
	return 0, s.err
}

// The approval and the elevation are ONE fact. A failed elevation must leave
// no approved grant behind, because the token leg reads that grant and the CLI
// acts on what it answers.
//
// Before the two writes shared a transaction, this shape survived: approved,
// with a live elevate_token_id and no window. The poll then reported
// "verified", the CLI retried the restricted command, and the hub refused it
// again with nothing the user could act on.
func TestCLIStepUp_AFailedElevationRollsBackTheApproval(t *testing.T) {
	env := setupAPIAuth(t)
	ctx := context.Background()
	tokenID, bearer := cliCredential(t, env, "operator-laptop")
	grant := decodeStepUpGrant(t, startStepUp(t, env, bearer, "operator-laptop"))

	forcedErr := errors.New("sensitive elevation failure")
	mux := stepUpHandlerOn(t, env, elevateFailStore{Store: env.store, err: forcedErr})
	req := httptest.NewRequest(http.MethodPost, "/auth/cli/activate",
		strings.NewReader(url.Values{"user_code": {grant.UserCode}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(env.elevatedAdminCookie(t))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), forcedErr.Error(), "the refusal must not leak the store error")

	row, err := env.store.DeviceAuthorizations().Get(ctx, grant.DeviceCode)
	require.NoError(t, err)
	assert.Zero(t, row.Approved, "a failed elevation must roll the approval back")
	assert.Empty(t, row.UserID, "a rolled-back approval identifies nobody")

	token, err := env.store.APITokens().GetByID(ctx, tokenID)
	require.NoError(t, err)
	assert.Nil(t, token.ElevationExpiresAt)

	// The CLI must be told the truth: still waiting, not verified.
	body := decodeJSONBody(t, pollStepUp(t, env, grant.DeviceCode))
	assert.Equal(t, "authorization_pending", body["error"])
}

// The poll reports the CREDENTIAL's window, never the grant's approval flag.
//
// The approved-with-no-window shape is what a hub that dies between two writes
// leaves behind, and a restore or a manual repair can write it too. Answering
// "elevated" for it sends the CLI into a retry the hub refuses again.
func TestCLIStepUp_ThePollReadsTheCredentialNotTheApprovalFlag(t *testing.T) {
	env := setupAPIAuth(t)
	ctx := context.Background()
	tokenID, bearer := cliCredential(t, env, "operator-laptop")
	grant := decodeStepUpGrant(t, startStepUp(t, env, bearer, "operator-laptop"))

	// Approve the grant through the store alone, so nothing stamps a window.
	rows, err := env.store.DeviceAuthorizations().ApproveByUserCode(ctx, store.ApproveDeviceAuthorizationByUserCodeParams{
		UserCode: verifycode.Normalize(grant.UserCode),
		UserID:   userid.MustNew(env.userID),
	}, env.clock.now().UTC())
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)

	resp := pollStepUp(t, env, grant.DeviceCode)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	body := decodeJSONBody(t, resp)
	assert.Equal(t, "invalid_grant", body["error"])
	assert.NotContains(t, body, "elevated")

	after, err := env.store.DeviceAuthorizations().Get(ctx, grant.DeviceCode)
	require.NoError(t, err)
	assert.Nil(t, after.ConsumedAt, "a refusal must not spend the grant")

	token, err := env.store.APITokens().GetByID(ctx, tokenID)
	require.NoError(t, err)
	assert.Nil(t, token.ElevationExpiresAt, "the refusal grants nothing")
}

// The activation page identifies the credential from the HUB's record of it,
// and never from the label the requester sent.
//
// The step-up endpoint authenticates the caller by the credential alone, so a
// stolen credential can start a ceremony and choose its own device_name. If
// the page rendered that name, the attacker would label their step-up with the
// owner's own laptop and phish the owner into re-arming it.
func TestCLIStepUp_TheActivationPageIdentifiesTheStoredCredential(t *testing.T) {
	env := setupAPIAuth(t)
	const storedName = "build-server-01"
	const chosenName = "MacBook-Pro-of-the-owner"
	_, bearer := cliCredential(t, env, storedName)
	grant := decodeStepUpGrant(t, startStepUp(t, env, bearer, chosenName))

	page := activationPage(t, env, grant.UserCode, env.elevatedAdminCookie(t))

	assert.Contains(t, page, "Verify a CLI credential", "a step-up must not read as an issuance")
	assert.Contains(t, page, storedName, "the page must identify the credential the hub recorded")
	assert.Contains(t, page, env.clock.now().UTC().Format("2006-01-02"), "the page must show when the credential was added")
	assert.NotContains(t, page, chosenName, "the requester's own label must identify nothing here")
	assert.NotContains(t, page, "administer the hub", "a step-up widens nothing, so it offers no scope")
}

// The ISSUANCE flow keeps the requester-supplied label. It is the only thing
// the hub knows about a device that holds no credential yet, and
// normalizeDeviceName is what makes it safe to render.
func TestCLIStepUp_TheIssuancePageStillShowsTheRequestedDeviceName(t *testing.T) {
	env := setupAPIAuth(t)
	resp, err := env.server.Client().PostForm(env.server.URL+"/auth/cli/device-authorization",
		url.Values{"device_name": {"ci-runner-7"}})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	grant := decodeJSONBody(t, resp)

	page := activationPage(t, env, grant["user_code"].(string), env.elevatedAdminCookie(t))
	assert.Contains(t, page, "Authorize CLI device")
	assert.Contains(t, page, "ci-runner-7")
}

// One wire contract, two legs. The CLI polls a login grant and a step-up grant
// with one code path, so the two responses must carry the same fields and the
// same verification URLs.
func TestCLIStepUp_BothGrantLegsAnswerTheSameShape(t *testing.T) {
	env := setupAPIAuth(t)
	_, bearer := cliCredential(t, env, "operator-laptop")

	stepUp := decodeJSONBody(t, startStepUp(t, env, bearer, "operator-laptop"))
	resp, err := env.server.Client().PostForm(env.server.URL+"/auth/cli/device-authorization",
		url.Values{"device_name": {"operator-laptop"}, "admin": {"1"}})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	login := decodeJSONBody(t, resp)

	assert.ElementsMatch(t, slices.Collect(maps.Keys(login)), slices.Collect(maps.Keys(stepUp)),
		"the CLI polls both grants with one code path, so both bodies carry the same fields")
	assert.Equal(t, login["verification_uri"], stepUp["verification_uri"])
	assert.Equal(t, login["expires_in"], stepUp["expires_in"])
	assert.Equal(t, login["interval"], stepUp["interval"])

	// The admin ask is the ONE difference, and it travels one way only: a
	// login can carry it into the activation page, a step-up widens nothing.
	assert.Contains(t, login["verification_uri_complete"], "admin=1")
	assert.NotContains(t, stepUp["verification_uri_complete"], "admin=1")
	assert.Contains(t, stepUp["verification_uri_complete"], "user_code=")
}
