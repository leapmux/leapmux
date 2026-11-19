package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
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
		Issuance:         store.APITokenIssuanceDeviceCode,
		ID:               tokenID,
		UserID:           userid.MustNew(env.userID),
		ClientID:         oauthapp.ControlCLIClientID,
		InstallationName: name,
		GrantedScopes:    authscope.NonAdminGrant().String(),
		SecretHash:       env.validator.HashSecret(secret),
	}))
	return tokenID, auth.FormatBearer(auth.BearerKindAPI, tokenID, secret)
}

// startStepUp runs the /oauth/step-up leg with a bearer.
//
// deviceName is what the CALLER labels its own request, which is why it is a
// parameter: one test sets it to the owner's laptop to prove the activation
// page never identifies a credential from it.
func startStepUp(t *testing.T, env *apiAuthEnv, bearer, deviceName string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		env.server.URL+"/oauth/step-up",
		strings.NewReader(url.Values{"installation_name": {deviceName}}.Encode()))
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

// pollStepUp polls /oauth/token once for a step-up grant.
func pollStepUp(t *testing.T, env *apiAuthEnv, deviceCode string) *http.Response {
	t.Helper()
	resp, err := env.server.Client().PostForm(env.server.URL+"/oauth/token", url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"client_id":   {oauthapp.ControlCLIClientID},
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
		env.server.URL+"/oauth/device",
		strings.NewReader(url.Values{"user_code": {userCode}, "decision": {"allow"}}.Encode()))
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
	env.advance(2 * time.Duration(grant.Interval) * time.Second)

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
		env.server.URL+"/oauth/device",
		strings.NewReader(url.Values{"user_code": {grant.UserCode}, "decision": {"allow"}}.Encode()))
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
		GrantedScopes: "workspace:read workspace:write worker:read",
		ID:            delegationID,
		UserID:        userid.MustNew(env.userID),
		WorkerID:      workerID,
		SecretHash:    env.validator.HashSecret(secret),
		ExpiresAt:     time.Now().Add(time.Hour),
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

	env.advance(2 * time.Duration(grant.Interval) * time.Second)
	assert.Equal(t, http.StatusBadRequest, pollStepUp(t, env, grant.DeviceCode).StatusCode,
		"a consumed grant cannot be exchanged twice")
}

// activationPage fetches the activation page for a user code, as a browser
// session that already proved a factor, and returns the rendered HTML.
func activationPage(t *testing.T, env *apiAuthEnv, userCode string, cookie *http.Cookie) string {
	t.Helper()
	pageURL := env.server.URL + "/oauth/device?" + url.Values{"user_code": {userCode}, "decision": {"allow"}}.Encode()
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
	h := newOAuthServerHandler(service.OAuthServerDeps{SessionHasher: hubtestutil.TestSecretHasher(),
		Store:     st,
		Validator: env.validator,
		Lifecycle: auth.NewCredentialLifecycleEffects(env.cache, noopBearerCloser{}, nil),
		HubURL:    func() string { return env.server.URL },
	})
	h.Now = env.now
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
	req := httptest.NewRequest(http.MethodPost, "/oauth/device",
		strings.NewReader(url.Values{"user_code": {grant.UserCode}, "decision": {"allow"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(env.elevatedAdminCookie(t))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), forcedErr.Error(), "the refusal must not leak the store error")

	row, err := env.store.DeviceAuthorizations().Get(ctx, hubtestutil.TestSecretHasher().Hash(grant.DeviceCode))
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.DeviceAuthorizationState_DEVICE_AUTHORIZATION_STATE_PENDING, row.State, "a failed elevation must roll the approval back")
	assert.Empty(t, row.UserID, "a rolled-back approval identifies nobody")

	token, err := env.store.APITokens().GetByID(ctx, tokenID)
	require.NoError(t, err)
	assert.Nil(t, token.ElevationExpiresAt)

	// The CLI must be told the truth: still waiting, not verified.
	body := decodeJSONBody(t, pollStepUp(t, env, grant.DeviceCode))
	assert.Equal(t, "authorization_pending", body["error"])
}

// The poll reports the CREDENTIAL's window, never the grant's APPROVED state.
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
	}, env.now().UTC())
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)

	resp := pollStepUp(t, env, grant.DeviceCode)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	body := decodeJSONBody(t, resp)
	assert.Equal(t, "invalid_grant", body["error"])
	assert.NotContains(t, body, "elevated")

	after, err := env.store.DeviceAuthorizations().Get(ctx, hubtestutil.TestSecretHasher().Hash(grant.DeviceCode))
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

	assert.Contains(t, page, "Verify an app credential", "a step-up must not read as an issuance")
	assert.Contains(t, page, storedName, "the page must identify the credential the hub recorded")
	assert.Contains(t, page, env.now().UTC().Format("2006-01-02"), "the page must show when the credential was added")
	assert.NotContains(t, page, chosenName, "the requester's own label must identify nothing here")
	assert.NotContains(t, page, "administer the hub", "a step-up widens nothing, so it offers no scope")
}

// NEITHER leg renders the requester-supplied label, and the issuance leg is
// the one that used to.
//
// It carried the label because the hub knew nothing else about a device that
// held no credential yet, and normalizeInstallationName was what made
// rendering it safe. A registered client_id replaced that: the page now names
// the APP, which is a row an administrator or the owner created, so the only
// requester-controlled string on the page is gone rather than sanitized.
//
// The label still exists and is still normalized -- it names the credential in
// the owner's connected-apps list -- so this pins both halves: absent from the
// page, present and cleaned in the row.
func TestCLIStepUp_TheIssuancePageNamesTheAppAndNotTheRequestersLabel(t *testing.T) {
	env := setupAPIAuth(t)
	ctx := context.Background()

	// A label chosen to impersonate the owner's own machine, which is what
	// rendering it would let a stolen credential do. The control character is
	// the half CleanNameTo removes.
	const chosen = "Owner\u0027s MacBook\u0007 Pro"
	resp, err := env.server.Client().PostForm(env.server.URL+"/oauth/device-authorization",
		url.Values{"client_id": {oauthapp.ControlCLIClientID}, "installation_name": {chosen}})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	grant := decodeJSONBody(t, resp)
	require.Contains(t, grant, "user_code", "the device leg refused: %v", grant)

	page := activationPage(t, env, grant["user_code"].(string), env.elevatedAdminCookie(t))
	assert.Contains(t, page, "Authorize an app")
	assert.Contains(t, page, oauthapp.ControlCLIName, "the page must name the app the code belongs to")
	assert.NotContains(t, page, "MacBook", "the requester's own label must identify nothing here")

	// The label survives where it belongs, normalized. Without this half, the
	// endpoint could stop recording it and the assertions above would still
	// pass.
	row, err := env.store.DeviceAuthorizations().Get(ctx, hubtestutil.TestSecretHasher().Hash(grant["device_code"].(string)))
	require.NoError(t, err)
	assert.Equal(t, "Owner\u0027s MacBook Pro", row.DeviceName,
		"normalizeInstallationName still cleans the stored label")
}

// One wire contract, two legs. The CLI polls a login grant and a step-up grant
// with one code path, so the two responses must carry the same fields and the
// same verification URLs.
func TestCLIStepUp_BothGrantLegsAnswerTheSameShape(t *testing.T) {
	env := setupAPIAuth(t)
	_, bearer := cliCredential(t, env, "operator-laptop")

	stepUp := decodeJSONBody(t, startStepUp(t, env, bearer, "operator-laptop"))
	resp, err := env.server.Client().PostForm(env.server.URL+"/oauth/device-authorization",
		url.Values{"client_id": {oauthapp.ControlCLIClientID},
			"installation_name": {"operator-laptop"}, "scope": {"admin:read"}})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	login := decodeJSONBody(t, resp)

	assert.ElementsMatch(t, slices.Collect(maps.Keys(login)), slices.Collect(maps.Keys(stepUp)),
		"the CLI polls both grants with one code path, so both bodies carry the same fields")
	assert.Equal(t, login["verification_uri"], stepUp["verification_uri"])
	assert.Equal(t, login["expires_in"], stepUp["expires_in"])
	assert.Equal(t, login["interval"], stepUp["interval"])

	// The completion URL carries the user_code and NOTHING else. The scope ask
	// used to ride it as admin=1, which made the URL a second source of truth
	// the page had to trust -- a link an attacker rewrote asked the owner to
	// approve one thing while the grant recorded another. It now lives in the
	// grant row alone, and the page reads it from there.
	for leg, body := range map[string]map[string]any{"login": login, "step-up": stepUp} {
		complete, ok := body["verification_uri_complete"].(string)
		require.Truef(t, ok, "the %s leg answered no completion URL", leg)
		parsed, err := url.Parse(complete)
		require.NoError(t, err)
		assert.Equalf(t, []string{"user_code"}, slices.Sorted(maps.Keys(parsed.Query())),
			"the %s completion URL must carry the code and no other instruction", leg)
	}

	// The ask itself still differs, where it is now recorded. Without this the
	// assertions above would pass with the scope dropped entirely.
	ctx := context.Background()
	loginRow, err := env.store.DeviceAuthorizations().Get(ctx, hubtestutil.TestSecretHasher().Hash(login["device_code"].(string)))
	require.NoError(t, err)
	assert.Equal(t, "admin:read", loginRow.RequestedScopes)

	stepUpRow, err := env.store.DeviceAuthorizations().Get(ctx, hubtestutil.TestSecretHasher().Hash(stepUp["device_code"].(string)))
	require.NoError(t, err)
	assert.Empty(t, stepUpRow.RequestedScopes, "a step-up widens nothing, so it asks for nothing")
}

func TestCLIStepUp_AnElevatedBearerCannotApproveBrowserConsent(t *testing.T) {
	env := setupAPIAuth(t)
	tokenID, bearer := cliCredential(t, env, "already-elevated-app")
	now := env.now()
	rows, err := env.store.APITokens().Elevate(t.Context(), store.ElevateAPITokenParams{
		TokenID: tokenID, UserID: userid.MustNew(env.userID), ElevationProvenAt: now, ElevationExpiresAt: now.Add(auth.ElevationWindow),
	}, now)
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
	actor, err := env.validator.ValidateBearer(t.Context(), bearer)
	require.NoError(t, err)
	require.True(t, actor.Elevated(env.now()), "the rejected app credential must have a live elevation")
	for _, path := range []string{"/oauth/device", "/oauth/consent"} {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, env.server.URL+path,
			strings.NewReader(url.Values{"user_code": {"ABCDEF"}, "decision": {"allow"}}.Encode()))
		require.NoError(t, err)
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Authorization", "Bearer "+bearer)
		response, err := env.server.Client().Do(request)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		assert.Equal(t, http.StatusUnauthorized, response.StatusCode, path)
	}
}

// --- The `step_up` parameter of an authorization request ---
//
// The device-code ceremony above verifies an app credential through its own
// activation page. These cover the browser handoff instead: an authorize
// request that carries `step_up=session:<id>` or `step_up=token:<id>` verifies
// a credential the account ALREADY holds, so the consent page identifies that
// credential from the hub's own record and the approval issues nothing new.

// cliCredentialFor mints a command-line credential for a chosen user, as
// cliCredential does for the environment's own user.
func cliCredentialFor(t *testing.T, env *apiAuthEnv, userID, name string) (tokenID, bearer string) {
	t.Helper()
	tokenID = id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		Issuance:         store.APITokenIssuanceDeviceCode,
		ID:               tokenID,
		UserID:           userid.MustNew(userID),
		ClientID:         oauthapp.ControlCLIClientID,
		InstallationName: name,
		GrantedScopes:    authscope.NonAdminGrant().String(),
		SecretHash:       env.validator.HashSecret(secret),
	}))
	return tokenID, auth.FormatBearer(auth.BearerKindAPI, tokenID, secret)
}

// desktopIssuedSession creates the credential a desktop step-up verifies: a
// session the browser handoff issued to the desktop app.
func desktopIssuedSession(t *testing.T, env *apiAuthEnv, userID, label string) auth.CreatedSession {
	t.Helper()
	sess, err := auth.CreateSession(context.Background(), env.store, hubtestutil.TestSecretHasher(),
		userid.MustNew(userID), auth.DefaultSessionDuration,
		auth.SignInProvedNoFactor,
		auth.SessionMeta{UserAgent: label, IssuedToClient: oauthapp.DesktopClientID})
	require.NoError(t, err)
	return sess
}

// deniedRedirect asserts the redirectable refusal RFC 6749 section 4.1.2.1
// requires -- the answer reaches the app that asked, carrying the state -- and
// returns the destination query for the caller's own assertions.
func deniedRedirect(t *testing.T, resp *http.Response) url.Values {
	t.Helper()
	require.Equal(t, http.StatusFound, resp.StatusCode)
	dest, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "state-1", dest.Query().Get("state"),
		"a client that cannot match the callback to its own request must discard it")
	assert.Equal(t, "access_denied", dest.Query().Get("error"))
	return dest.Query()
}

// hiddenFields reads the consent form's hidden inputs the way a browser
// submits them, so a field the page drops is a value the POST drops.
func hiddenFields(t *testing.T, page string) url.Values {
	t.Helper()
	form := url.Values{}
	for _, m := range regexp.MustCompile(`<input type="hidden" name="([^"]+)" value="([^"]*)"/>`).FindAllStringSubmatch(page, -1) {
		form.Set(m[1], html.UnescapeString(m[2]))
	}
	return form
}

func TestAuthorizeStepUp_AValueThatDoesNotParse(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"foo:x", "session:", "token:"} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			env := setupAPIAuth(t)
			cookie := env.elevatedAdminCookie(t)
			q := startQuery(url.Values{"step_up": {raw}})
			resp := getWithCookie(t, env.server.URL+"/oauth/authorize?"+q.Encode(), cookie)
			deniedRedirect(t, resp)
		})
	}
}

func TestAuthorizeStepUp_SessionOfAnotherUser(t *testing.T) {
	t.Parallel()
	env := setupAPIAuth(t)
	otherID := hubtestutil.CreateTestUser(t, env.store, "otheruser", "otherpass123")
	sess := desktopIssuedSession(t, env, otherID, "LeapMux Desktop on other-laptop")
	cookie := env.elevatedAdminCookie(t)
	q := startQuery(url.Values{"client_id": {oauthapp.DesktopClientID},
		"step_up": {oauthapp.StepUpSessionPrefix + sess.ID}})
	resp := getWithCookie(t, env.server.URL+"/oauth/authorize?"+q.Encode(), cookie)
	deniedRedirect(t, resp)
}

func TestAuthorizeStepUp_BrowserSessionIsNotATarget(t *testing.T) {
	t.Parallel()
	env := setupAPIAuth(t)
	cookie := env.elevatedAdminCookie(t)
	// The approving session itself: a session the Hub's own page created, with
	// no app behind it.
	target := hubtestutil.SessionIDFromCookie(t, cookie.Value)
	q := startQuery(url.Values{"client_id": {oauthapp.DesktopClientID},
		"step_up": {oauthapp.StepUpSessionPrefix + target}})
	resp := getWithCookie(t, env.server.URL+"/oauth/authorize?"+q.Encode(), cookie)
	deniedRedirect(t, resp)
}

func TestAuthorizeStepUp_TokenOfAnotherUser(t *testing.T) {
	t.Parallel()
	env := setupAPIAuth(t)
	otherID := hubtestutil.CreateTestUser(t, env.store, "otheruser", "otherpass123")
	tokenID, _ := cliCredentialFor(t, env, otherID, "other-laptop")
	cookie := env.elevatedAdminCookie(t)
	q := startQuery(url.Values{"step_up": {oauthapp.StepUpTokenPrefix + tokenID}})
	resp := getWithCookie(t, env.server.URL+"/oauth/authorize?"+q.Encode(), cookie)
	deniedRedirect(t, resp)
}

func TestAuthorizeStepUp_TokenOfAnotherApp(t *testing.T) {
	t.Parallel()
	env := setupAPIAuth(t)
	otherApp := seedTestApp(t, env, store.CreateOAuthClientParams{
		ClientName: "Third-party app", RedirectURIs: "https://app.example.com/callback",
		ElevationAllowed: true,
	})
	tokenID, _ := mintAppCredential(t, env, otherApp)
	cookie := env.elevatedAdminCookie(t)
	// The request names the control CLI; the credential belongs to the other app.
	q := startQuery(url.Values{"step_up": {oauthapp.StepUpTokenPrefix + tokenID}})
	resp := getWithCookie(t, env.server.URL+"/oauth/authorize?"+q.Encode(), cookie)
	deniedRedirect(t, resp)
}

func TestAuthorizeStepUp_RevokedToken(t *testing.T) {
	t.Parallel()
	env := setupAPIAuth(t)
	tokenID, _ := cliCredential(t, env, "operator-laptop")
	_, err := env.store.APITokens().Revoke(context.Background(), tokenID)
	require.NoError(t, err)
	cookie := env.elevatedAdminCookie(t)
	q := startQuery(url.Values{"step_up": {oauthapp.StepUpTokenPrefix + tokenID}})
	resp := getWithCookie(t, env.server.URL+"/oauth/authorize?"+q.Encode(), cookie)
	deniedRedirect(t, resp)
}

func TestAuthorizeStepUp_AppWithoutElevationAllowed(t *testing.T) {
	t.Parallel()
	env := setupAPIAuth(t)
	// The column's default is FALSE, and this registration takes it.
	appID := seedTestApp(t, env, store.CreateOAuthClientParams{
		ClientName: "Third-party app", RedirectURIs: "https://app.example.com/callback",
	})
	tokenID, _ := mintAppCredential(t, env, appID)
	cookie := env.elevatedAdminCookie(t)
	q := startQuery(url.Values{"client_id": {appID},
		"redirect_uri": {"https://app.example.com/callback"},
		"step_up":      {oauthapp.StepUpTokenPrefix + tokenID}})
	resp := getWithCookie(t, env.server.URL+"/oauth/authorize?"+q.Encode(), cookie)
	deniedRedirect(t, resp)
}

// A step-up asks for nothing. A request that also asks for a scope is refused
// rather than accepted with a grant riding along on the step-up target.
func TestAuthorizeStepUp_TokenWithAScope(t *testing.T) {
	t.Parallel()
	env := setupAPIAuth(t)
	ctx := context.Background()
	plainID := hubtestutil.CreateTestUser(t, env.store, "plainuser", "plainpass123")
	tokenID, _ := cliCredentialFor(t, env, plainID, "operator-laptop")

	loginResult, _ := hubtestutil.LoginWithoutWindow(t, env.store, hubtestutil.TestSecretHasher(),
		"plainuser", "plainpass123", auth.DefaultSessionDuration)
	cookie := &http.Cookie{Name: auth.CookieName, Value: loginResult.Token}
	now := env.now()
	n, err := env.store.Sessions().Elevate(ctx, store.ElevateSessionParams{
		SessionID:          hubtestutil.SessionIDFromCookie(t, loginResult.Token),
		UserID:             userid.MustNew(plainID),
		ElevationProvenAt:  now,
		ElevationExpiresAt: now.Add(auth.ElevationWindow),
	}, now)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)

	q := startQuery(url.Values{"step_up": {oauthapp.StepUpTokenPrefix + tokenID},
		"scope": {"file:read"}})
	resp := getWithCookie(t, env.server.URL+"/oauth/authorize?"+q.Encode(), cookie)
	dest := deniedRedirect(t, resp)
	assert.Contains(t, dest.Get("error_description"), "step_up",
		"the refusal must come from the step-up parse, not the scope check")
}

// The scope check runs BEFORE the step-up parse, so a desktop request that
// asks for a scope answers invalid_scope from the registration's ceiling.
func TestAuthorizeStepUp_SessionWithAScope(t *testing.T) {
	t.Parallel()
	env := setupAPIAuth(t)
	sess := desktopIssuedSession(t, env, env.userID, "LeapMux Desktop on alice@laptop")
	cookie := env.elevatedAdminCookie(t)
	q := startQuery(url.Values{"client_id": {oauthapp.DesktopClientID},
		"step_up": {oauthapp.StepUpSessionPrefix + sess.ID},
		"scope":   {"file:read"}})
	resp := getWithCookie(t, env.server.URL+"/oauth/authorize?"+q.Encode(), cookie)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	dest, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "invalid_scope", dest.Query().Get("error"),
		"the scope check must answer first; the step-up parse would answer access_denied")
}

// The step-up page identifies the credential from the HUB's record of it, and
// never from the label the requester sent.
func TestAuthorizeStepUp_ThePageShowsTheHubsOwnRecord(t *testing.T) {
	t.Parallel()

	const attackerLabel = "evil"
	const stepUpSentence = "This lets a sign-in that already exists make sensitive changes for the next two hours. It issues nothing new."

	t.Run("session", func(t *testing.T) {
		t.Parallel()
		env := setupAPIAuth(t)
		sess := desktopIssuedSession(t, env, env.userID, "LeapMux Desktop on alice@laptop")
		row, err := env.store.Sessions().GetByID(context.Background(), sess.ID, env.now().UTC())
		require.NoError(t, err)
		cookie := env.elevatedAdminCookie(t)
		q := startQuery(url.Values{"client_id": {oauthapp.DesktopClientID},
			"step_up":           {oauthapp.StepUpSessionPrefix + sess.ID},
			"installation_name": {attackerLabel}})
		page := bodyOf(t, getWithCookie(t, env.server.URL+"/oauth/authorize?"+q.Encode(), cookie))
		assert.Contains(t, page, "Verify LeapMux Desktop?", "the page must name the registered app")
		assert.Contains(t, page, stepUpSentence)
		assert.Contains(t, page, row.UserAgent, "the page must identify the credential the hub recorded")
		assert.Contains(t, page, row.CreatedAt.UTC().Format("2006-01-02"),
			"the page must show when the credential was added")
		assert.NotContains(t, page, attackerLabel,
			"the requester's own label must identify nothing here")
	})

	t.Run("token", func(t *testing.T) {
		t.Parallel()
		env := setupAPIAuth(t)
		tokenID, _ := cliCredential(t, env, "build-server-01")
		row, err := env.store.APITokens().GetByID(context.Background(), tokenID)
		require.NoError(t, err)
		cookie := env.elevatedAdminCookie(t)
		q := startQuery(url.Values{"step_up": {oauthapp.StepUpTokenPrefix + tokenID},
			"installation_name": {attackerLabel}})
		page := bodyOf(t, getWithCookie(t, env.server.URL+"/oauth/authorize?"+q.Encode(), cookie))
		assert.Contains(t, page, "Verify LeapMux control CLI?", "the page must name the registered app")
		assert.Contains(t, page, stepUpSentence)
		assert.Contains(t, page, "LeapMux control CLI on build-server-01",
			"the page must identify the credential the hub recorded")
		assert.Contains(t, page, row.CreatedAt.UTC().Format("2006-01-02"),
			"the page must show when the credential was added")
		assert.NotContains(t, page, attackerLabel,
			"the requester's own label must identify nothing here")
	})
}

// The form carries `step_up` forward. Without the hidden field the POST drops
// the target and the code verifies nothing: the exchange then takes the
// session path and a desktop step-up signs a NEW session in instead.
func TestConsentForm_CarriesStepUpForward(t *testing.T) {
	t.Parallel()
	env := setupAPIAuth(t)
	ctx := context.Background()
	sess := desktopIssuedSession(t, env, env.userID, "LeapMux Desktop on alice@laptop")
	cookie := env.elevatedAdminCookie(t)
	q := startQuery(url.Values{"client_id": {oauthapp.DesktopClientID},
		"step_up": {oauthapp.StepUpSessionPrefix + sess.ID}})
	page := bodyOf(t, getWithCookie(t, env.server.URL+"/oauth/authorize?"+q.Encode(), cookie))

	form := hiddenFields(t, page)
	require.Equal(t, oauthapp.StepUpSessionPrefix+sess.ID, form.Get("step_up"),
		"the form must carry the step-up target forward")
	form.Set("decision", "allow")
	resp := postConsentForm(t, env.server.URL+"/oauth/consent", form, cookie)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	dest, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	code := dest.Query().Get("code")
	require.NotEmpty(t, code)
	row, err := env.store.OAuthAuthorizationCodes().Get(ctx, code)
	require.NoError(t, err)
	assert.Equal(t, sess.ID, row.StepUpSessionID)
	assert.Empty(t, row.StepUpTokenID)
	assert.Empty(t, row.GrantedScopes, "a step-up widens no grant, so the code records none")
}

// recordingCodeStore counts authorization-code writes, so a test can prove a
// refusal reached no Create at all.
type recordingCodeStore struct {
	store.OAuthAuthorizationCodeStore
	creates *atomic.Int64
}

func (s recordingCodeStore) Create(ctx context.Context, p store.CreateOAuthAuthorizationCodeParams) error {
	s.creates.Add(1)
	return s.OAuthAuthorizationCodeStore.Create(ctx, p)
}

type recordingStore struct {
	store.Store
	creates *atomic.Int64
}

func (s recordingStore) OAuthAuthorizationCodes() store.OAuthAuthorizationCodeStore {
	return recordingCodeStore{s.Store.OAuthAuthorizationCodes(), s.creates}
}

// The POST re-validates the `step_up` the form carries, because the form is
// attacker-writable: a hidden field naming ANOTHER user's session must be
// refused and must leave no code behind.
func TestConsentStepUp_TamperedFormIsRefusedWithoutACode(t *testing.T) {
	t.Parallel()
	env := setupAPIAuth(t)
	otherID := hubtestutil.CreateTestUser(t, env.store, "otheruser", "otherpass123")
	sess := desktopIssuedSession(t, env, otherID, "LeapMux Desktop on other-laptop")

	var creates atomic.Int64
	mux := stepUpHandlerOn(t, env, recordingStore{Store: env.store, creates: &creates})
	cookie := env.elevatedAdminCookie(t)
	form := startQuery(url.Values{"client_id": {oauthapp.DesktopClientID},
		"step_up":  {oauthapp.StepUpSessionPrefix + sess.ID},
		"decision": {"allow"}})
	req := httptest.NewRequest(http.MethodPost, "/oauth/consent", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusFound, rec.Code)
	dest, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "access_denied", dest.Query().Get("error"))
	assert.Empty(t, dest.Query().Get("code"), "a refusal must send no code to the app")
	assert.Zero(t, creates.Load(), "a refused tampering must leave no authorization-code row")
}

// Consent alone stamps nothing: the window opens only when the code is
// redeemed, so an approval nobody completes elevates no target.
func TestConsentStepUp_ApprovalWithoutRedemptionStampsNothing(t *testing.T) {
	t.Parallel()
	env := setupAPIAuth(t)
	ctx := context.Background()
	sess := desktopIssuedSession(t, env, env.userID, "LeapMux Desktop on alice@laptop")
	cookie := env.elevatedAdminCookie(t)
	form := startQuery(url.Values{"client_id": {oauthapp.DesktopClientID},
		"step_up":  {oauthapp.StepUpSessionPrefix + sess.ID},
		"decision": {"allow"}})
	resp := postConsentForm(t, env.server.URL+"/oauth/consent", form, cookie)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	dest, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	require.NotEmpty(t, dest.Query().Get("code"), "the approval itself must succeed")

	row, err := env.store.Sessions().GetByID(ctx, sess.ID, env.now().UTC())
	require.NoError(t, err)
	assert.Nil(t, row.ElevationProvenAt, "the approval alone proves nothing")
	assert.Nil(t, row.ElevationExpiresAt, "the target must hold no window before the exchange")
}

// The step-up code exchange authenticates with the cookie rung and the bearer
// rung on. A code presented with the matching session cookie must redeem.
// VB-5 regression: the exchange used to authenticate with the bearer rung
// only, so a step-up code presented with the session cookie answered 401.
func TestStepUpCodeExchange_RedeemsWithMatchingSessionCookie(t *testing.T) {
	t.Parallel()
	env := setupAPIAuth(t)
	ctx := context.Background()

	// Create a session for the admin user.
	cookie := env.adminCookie(t)
	sessionID := hubtestutil.SessionIDFromCookie(t, cookie.Value)

	// Create a step-up code targeting that session.
	code := id.Generate()
	verifier, challenge := pkceVerifierAndChallenge()
	_ = challenge
	require.NoError(t, env.store.OAuthAuthorizationCodes().Create(ctx, store.CreateOAuthAuthorizationCodeParams{
		Code:             code,
		UserID:           userid.MustNew(env.userID),
		ClientID:         oauthapp.DesktopClientID,
		CodeChallenge:    challenge,
		RedirectURI:      "/",
		GrantedScopes:    "",
		InstallationName: "test",
		ExpiresAt:        time.Now().Add(5 * time.Minute),
		StepUpSessionID:  sessionID,
	}))

	// Redeem with the matching session cookie (no bearer).
	resp := redeemStepUpCode(t, env, oauthapp.DesktopClientID, code, verifier, cookie, "")
	require.Equal(t, http.StatusOK, resp.StatusCode, "the cookie rung must authenticate the step-up redemption")

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, true, body["elevated"])

	// The session now carries an elevation window.
	sess, err := env.store.Sessions().GetByID(ctx, sessionID, time.Now().UTC())
	require.NoError(t, err)
	assert.NotNil(t, sess.ElevationExpiresAt, "the redemption stamps the window")
}

// The step-up code exchange refuses a request with no credential at all.
// VB-5 regression: without the cookie rung, even a cookie-carrying request
// fell through to the bearer rung and failed.
func TestStepUpCodeExchange_RefusesNoCredential(t *testing.T) {
	t.Parallel()
	env := setupAPIAuth(t)
	ctx := context.Background()

	cookie := env.adminCookie(t)
	sessionID := hubtestutil.SessionIDFromCookie(t, cookie.Value)

	code := id.Generate()
	verifier, challenge := pkceVerifierAndChallenge()
	_ = challenge
	require.NoError(t, env.store.OAuthAuthorizationCodes().Create(ctx, store.CreateOAuthAuthorizationCodeParams{
		Code:             code,
		UserID:           userid.MustNew(env.userID),
		ClientID:         oauthapp.DesktopClientID,
		CodeChallenge:    challenge,
		RedirectURI:      "/",
		GrantedScopes:    "",
		InstallationName: "test",
		ExpiresAt:        time.Now().Add(5 * time.Minute),
		StepUpSessionID:  sessionID,
	}))

	// No cookie and no bearer.
	resp := redeemStepUpCode(t, env, oauthapp.DesktopClientID, code, verifier, nil, "")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// The code stays valid: a second redemption with the cookie succeeds.
	resp2 := redeemStepUpCode(t, env, oauthapp.DesktopClientID, code, verifier, cookie, "")
	assert.Equal(t, http.StatusOK, resp2.StatusCode, "the code stays valid after a refused redemption")
}

// The step-up code exchange returns invalid_grant when the target session is
// gone, and the consumption rolls back so the code is not burned.
// VB-6 regression: the no-row result of elevateSessionRow was discarded, so
// the consumption committed and the exchange answered success with no window.
func TestStepUpCodeExchange_NoRowRollsBackConsumption(t *testing.T) {
	t.Parallel()
	env := setupAPIAuth(t)
	ctx := context.Background()

	cookie := env.adminCookie(t)
	sessionID := hubtestutil.SessionIDFromCookie(t, cookie.Value)

	// Delete the target session to simulate a session that ended between
	// auth and stamp.
	_, err := env.store.Sessions().Revoke(ctx, sessionID)
	require.NoError(t, err)

	code := id.Generate()
	verifier, challenge := pkceVerifierAndChallenge()
	_ = challenge
	require.NoError(t, env.store.OAuthAuthorizationCodes().Create(ctx, store.CreateOAuthAuthorizationCodeParams{
		Code:             code,
		UserID:           userid.MustNew(env.userID),
		ClientID:         oauthapp.DesktopClientID,
		CodeChallenge:    challenge,
		RedirectURI:      "/",
		GrantedScopes:    "",
		InstallationName: "test",
		ExpiresAt:        time.Now().Add(5 * time.Minute),
		StepUpSessionID:  sessionID,
	}))

	// Present with a DIFFERENT session of the same user so authentication
	// passes but the target row is gone.
	cookie2 := env.adminCookie(t)
	resp := redeemStepUpCode(t, env, oauthapp.DesktopClientID, code, verifier, cookie2, "")
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "a dead target answers invalid_grant")
}

// redeemStepUpCode presents one step-up code to /oauth/token. The request
// authenticates with cookie, with bearer, or with neither when both are
// empty.
func redeemStepUpCode(t *testing.T, env *apiAuthEnv, clientID, code, verifier string, cookie *http.Cookie, bearer string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		env.server.URL+"/oauth/token",
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
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := env.server.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// elevateStampStore wraps a store so one test decides the outcome of the
// elevation stamps the step-up exchange writes. The hooks travel into the
// transaction too, because the stamp runs inside one.
type elevateStampStore struct {
	store.Store
	sessionStamp func() (int64, error)
	grantStamp   func() (int64, error)
}

func (s elevateStampStore) Sessions() store.SessionStore {
	return elevateStampSessions{SessionStore: s.Store.Sessions(), stamp: s.sessionStamp}
}

func (s elevateStampStore) APITokens() store.APITokenStore {
	return elevateStampAPITokens{APITokenStore: s.Store.APITokens(), stamp: s.grantStamp}
}

func (s elevateStampStore) RunInUserAuthTransaction(ctx context.Context, uid userid.UserID, fn func(tx store.Store) error) error {
	return s.Store.RunInUserAuthTransaction(ctx, uid, func(tx store.Store) error {
		return fn(elevateStampStore{Store: tx, sessionStamp: s.sessionStamp, grantStamp: s.grantStamp})
	})
}

type elevateStampSessions struct {
	store.SessionStore
	stamp func() (int64, error)
}

func (s elevateStampSessions) Elevate(ctx context.Context, p store.ElevateSessionParams, now time.Time) (int64, error) {
	if s.stamp != nil {
		return s.stamp()
	}
	return s.SessionStore.Elevate(ctx, p, now)
}

type elevateStampAPITokens struct {
	store.APITokenStore
	stamp func() (int64, error)
}

func (s elevateStampAPITokens) Elevate(ctx context.Context, p store.ElevateAPITokenParams, now time.Time) (int64, error) {
	if s.stamp != nil {
		return s.stamp()
	}
	return s.APITokenStore.Elevate(ctx, p, now)
}

// A stamp the Hub cannot write means it cannot read the credential, which
// section 6.5 answers 500 for. Only the no-row refusals take the
// invalid_grant path, so a store failure under the session stamp must not
// be read as "that credential is no longer available".
func TestStepUpCodeExchange_ASessionStampStoreFailureAnswersInternalError(t *testing.T) {
	t.Parallel()
	env := setupAPIAuthWithStore(t, func(st store.Store) store.Store {
		return elevateStampStore{Store: st, sessionStamp: func() (int64, error) {
			return 0, errors.New("session elevation stamp failed")
		}}
	})
	ctx := context.Background()

	cookie := env.adminCookie(t)
	sessionID := hubtestutil.SessionIDFromCookie(t, cookie.Value)

	code := id.Generate()
	verifier, challenge := pkceVerifierAndChallenge()
	require.NoError(t, env.store.OAuthAuthorizationCodes().Create(ctx, store.CreateOAuthAuthorizationCodeParams{
		Code:             code,
		UserID:           userid.MustNew(env.userID),
		ClientID:         oauthapp.DesktopClientID,
		CodeChallenge:    challenge,
		RedirectURI:      "/",
		InstallationName: "test",
		ExpiresAt:        time.Now().Add(5 * time.Minute),
		StepUpSessionID:  sessionID,
	}))

	resp := redeemStepUpCode(t, env, oauthapp.DesktopClientID, code, verifier, cookie, "")
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode,
		"a stamp the Hub cannot write answers 500, not a credential refusal")
}

// The same rule for the grant stamp: a store failure while the window is
// written on the command-line credential answers 500.
func TestStepUpCodeExchange_AGrantStampStoreFailureAnswersInternalError(t *testing.T) {
	t.Parallel()
	env := setupAPIAuthWithStore(t, func(st store.Store) store.Store {
		return elevateStampStore{Store: st, grantStamp: func() (int64, error) {
			return 0, errors.New("credential elevation stamp failed")
		}}
	})
	ctx := context.Background()

	tokenID, bearer := cliCredential(t, env, "operator-laptop")

	code := id.Generate()
	verifier, challenge := pkceVerifierAndChallenge()
	require.NoError(t, env.store.OAuthAuthorizationCodes().Create(ctx, store.CreateOAuthAuthorizationCodeParams{
		Code:             code,
		UserID:           userid.MustNew(env.userID),
		ClientID:         oauthapp.ControlCLIClientID,
		CodeChallenge:    challenge,
		RedirectURI:      "/",
		InstallationName: "test",
		ExpiresAt:        time.Now().Add(5 * time.Minute),
		StepUpTokenID:    tokenID,
	}))

	resp := redeemStepUpCode(t, env, oauthapp.ControlCLIClientID, code, verifier, nil, bearer)
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode,
		"a stamp the Hub cannot write answers 500, not a credential refusal")
}

// The no-row refusal is the one stamp outcome that stays an invalid_grant:
// the credential the code names is gone, so there is nothing to elevate.
func TestStepUpCodeExchange_AGrantNoRowAnswersInvalidGrant(t *testing.T) {
	t.Parallel()
	env := setupAPIAuthWithStore(t, func(st store.Store) store.Store {
		return elevateStampStore{Store: st, grantStamp: func() (int64, error) {
			return 0, nil // the store's zero-row answer: no credential to stamp
		}}
	})
	ctx := context.Background()

	tokenID, bearer := cliCredential(t, env, "operator-laptop")

	code := id.Generate()
	verifier, challenge := pkceVerifierAndChallenge()
	require.NoError(t, env.store.OAuthAuthorizationCodes().Create(ctx, store.CreateOAuthAuthorizationCodeParams{
		Code:             code,
		UserID:           userid.MustNew(env.userID),
		ClientID:         oauthapp.ControlCLIClientID,
		CodeChallenge:    challenge,
		RedirectURI:      "/",
		InstallationName: "test",
		ExpiresAt:        time.Now().Add(5 * time.Minute),
		StepUpTokenID:    tokenID,
	}))

	resp := redeemStepUpCode(t, env, oauthapp.ControlCLIClientID, code, verifier, nil, bearer)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode,
		"a credential that is no longer there answers invalid_grant")
}

// seedStepUpCode writes the authorization-code row a step-up redemption
// consumes, targeting either a session or a token.
func seedStepUpCode(t *testing.T, env *apiAuthEnv, clientID, stepUpSessionID, stepUpTokenID string) (code, verifier string) {
	t.Helper()
	code = id.Generate()
	verifier, challenge := pkceVerifierAndChallenge()
	require.NoError(t, env.store.OAuthAuthorizationCodes().Create(context.Background(), store.CreateOAuthAuthorizationCodeParams{
		Code:             code,
		UserID:           userid.MustNew(env.userID),
		ClientID:         clientID,
		CodeChallenge:    challenge,
		RedirectURI:      "/",
		GrantedScopes:    "",
		InstallationName: "test",
		ExpiresAt:        time.Now().Add(5 * time.Minute),
		StepUpSessionID:  stepUpSessionID,
		StepUpTokenID:    stepUpTokenID,
	}))
	return code, verifier
}

// The code names the credential it verifies. A session code presented with
// ANOTHER session of the same owner proves nothing about the target: the
// person at the keyboard may be the owner, but not in the session the code
// was approved for. The refusal must stamp neither row.
func TestStepUpCodeExchange_AnotherSessionOfTheSameUserIsRefused(t *testing.T) {
	t.Parallel()
	env := setupAPIAuth(t)
	ctx := context.Background()

	target := env.adminCookie(t)
	targetID := hubtestutil.SessionIDFromCookie(t, target.Value)
	other := env.adminCookie(t)
	otherID := hubtestutil.SessionIDFromCookie(t, other.Value)
	require.NotEqual(t, targetID, otherID, "the two sessions must be distinct for the case to mean anything")

	code, verifier := seedStepUpCode(t, env, oauthapp.DesktopClientID, targetID, "")
	resp := redeemStepUpCode(t, env, oauthapp.DesktopClientID, code, verifier, other, "")
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	body := decodeJSONBody(t, resp)
	assert.Equal(t, "invalid_grant", body["error"])
	assert.Equal(t, "credential does not match the step-up target", body["error_description"],
		"the refusal must name the mismatch, not some other invalid_grant")

	for _, sessionID := range []string{targetID, otherID} {
		row, err := env.store.Sessions().GetByID(ctx, sessionID, time.Now().UTC())
		require.NoError(t, err)
		assert.Nil(t, row.ElevationProvenAt, "a refused redemption must stamp no session")
		assert.Nil(t, row.ElevationExpiresAt)
	}
}

// The same rule for a command-line credential: a code that names token A must
// not redeem with token B's bearer, however both belong to one owner. The
// refusal stamps neither credential.
func TestStepUpCodeExchange_AnotherTokensBearerIsRefused(t *testing.T) {
	t.Parallel()
	env := setupAPIAuth(t)
	ctx := context.Background()

	tokenA, _ := cliCredential(t, env, "credential-A")
	tokenB, bearerB := cliCredential(t, env, "credential-B")

	code, verifier := seedStepUpCode(t, env, oauthapp.ControlCLIClientID, "", tokenA)
	resp := redeemStepUpCode(t, env, oauthapp.ControlCLIClientID, code, verifier, nil, bearerB)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	body := decodeJSONBody(t, resp)
	assert.Equal(t, "invalid_grant", body["error"])
	assert.Equal(t, "credential does not match the step-up target", body["error_description"],
		"the refusal must name the mismatch, not some other invalid_grant")

	for _, tokenID := range []string{tokenA, tokenB} {
		row, err := env.store.APITokens().GetByID(ctx, tokenID)
		require.NoError(t, err)
		assert.Nil(t, row.ElevationProvenAt, "a refused redemption must stamp no credential")
		assert.Nil(t, row.ElevationExpiresAt)
	}
}

// A step-up code is single use. A second presentation answers invalid_grant,
// and the window the first one wrote must sit exactly where it was -- the
// replay is an attack, not a renewal, and moving the anchor would hand the
// attacker a fresh two hours.
func TestStepUpCodeExchange_ASecondPresentationMovesNothing(t *testing.T) {
	t.Parallel()
	env := setupAPIAuth(t)
	ctx := context.Background()

	cookie := env.adminCookie(t)
	sessionID := hubtestutil.SessionIDFromCookie(t, cookie.Value)
	code, verifier := seedStepUpCode(t, env, oauthapp.DesktopClientID, sessionID, "")

	first := redeemStepUpCode(t, env, oauthapp.DesktopClientID, code, verifier, cookie, "")
	require.Equal(t, http.StatusOK, first.StatusCode, "the matching cookie redeems the code")

	stamped, err := env.store.Sessions().GetByID(ctx, sessionID, time.Now().UTC())
	require.NoError(t, err)
	require.NotNil(t, stamped.ElevationProvenAt, "precondition: the redemption wrote the anchor")
	require.NotNil(t, stamped.ElevationExpiresAt)

	second := redeemStepUpCode(t, env, oauthapp.DesktopClientID, code, verifier, cookie, "")
	assert.Equal(t, http.StatusBadRequest, second.StatusCode)
	body := decodeJSONBody(t, second)
	assert.Equal(t, "invalid_grant", body["error"])
	assert.Equal(t, "code expired or already consumed", body["error_description"],
		"the refusal must be the single-use one, not some other invalid_grant")

	after, err := env.store.Sessions().GetByID(ctx, sessionID, time.Now().UTC())
	require.NoError(t, err)
	require.NotNil(t, after.ElevationProvenAt)
	assert.WithinDuration(t, *stamped.ElevationProvenAt, *after.ElevationProvenAt, time.Second,
		"a replayed code must move nothing")
	assert.WithinDuration(t, *stamped.ElevationExpiresAt, *after.ElevationExpiresAt, time.Second,
		"a replayed code must not extend the window either")
}

// The app's permission to elevate is re-read when the code is redeemed, not
// only when the consent was approved. An owner who takes the right away
// between the two must leave no window behind: the approval promised one, and
// the redemption is where that promise is kept or refused.
func TestStepUpCodeExchange_AnAppThatLosesElevationAllowedIsRefused(t *testing.T) {
	t.Parallel()
	env := setupAPIAuth(t)
	ctx := context.Background()

	tokenID, bearer := cliCredential(t, env, "build-server-01")
	cookie := env.elevatedAdminCookie(t)

	// Approve a step-up for that credential, exactly as the browser does. The
	// PKCE pair is drawn here because the redemption below must present the
	// preimage of the challenge the consent recorded.
	verifier, challenge := pkceVerifierAndChallenge()
	const redirect = "http://127.0.0.1:54321/callback"
	q := startQuery(url.Values{"step_up": {oauthapp.StepUpTokenPrefix + tokenID},
		"code_challenge": {challenge}, "redirect_uri": {redirect}})
	form := hiddenFields(t, bodyOf(t, getWithCookie(t, env.server.URL+"/oauth/authorize?"+q.Encode(), cookie)))
	require.Equal(t, oauthapp.StepUpTokenPrefix+tokenID, form.Get("step_up"))
	form.Set("decision", "allow")
	approved := postConsentForm(t, env.server.URL+"/oauth/consent", form, cookie)
	require.Equal(t, http.StatusFound, approved.StatusCode)
	dest, err := url.Parse(approved.Header.Get("Location"))
	require.NoError(t, err)
	code := dest.Query().Get("code")
	require.NotEmpty(t, code, "the approval itself must succeed")

	// The owner takes the right away before the CLI redeems.
	rows, err := env.store.OAuthClients().SetElevationAllowed(ctx, store.SetOAuthClientElevationAllowedParams{
		ClientID: oauthapp.ControlCLIClientID, ElevationAllowed: false,
		CallerUserID: userid.MustNew(env.userID), CallerIsAdmin: true,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		env.server.URL+"/oauth/token",
		strings.NewReader(url.Values{
			"grant_type":    {"authorization_code"},
			"client_id":     {oauthapp.ControlCLIClientID},
			"redirect_uri":  {redirect},
			"code":          {code},
			"code_verifier": {verifier},
		}.Encode()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := env.server.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	body := decodeJSONBody(t, resp)
	assert.Equal(t, "invalid_grant", body["error"])
	assert.Equal(t, "that credential is no longer available", body["error_description"],
		"the refusal must be the store's own elevation_allowed guard, not a PKCE failure")

	row, err := env.store.APITokens().GetByID(ctx, tokenID)
	require.NoError(t, err)
	assert.Nil(t, row.ElevationExpiresAt, "an app that may no longer elevate must leave no window")
	assert.Nil(t, row.ElevationProvenAt)
}
