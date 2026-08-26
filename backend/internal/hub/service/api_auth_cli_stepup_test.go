package service_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// Step-up for a command-line credential, end to end.
//
// The gate that protects the hub's settings, the user surface and the mint
// used to wave a bearer through: it had no row to stamp, so possession of the
// credential file was the whole of the check. These pin the ceremony that
// replaced that -- ask with the credential, approve in a browser, poll -- and
// the two properties it exists to have: the CLI cannot approve its own
// request, and approving grants a WINDOW rather than a credential.

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
func startStepUp(t *testing.T, env *apiAuthEnv, bearer string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		env.server.URL+"/auth/cli/elevate-authorization",
		strings.NewReader(url.Values{"device_name": {"operator-laptop"}}.Encode()))
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

// pollStepUp performs one /auth/cli/token poll for a step-up grant.
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

	grant := decodeStepUpGrant(t, startStepUp(t, env, bearer))

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
	grant := decodeStepUpGrant(t, startStepUp(t, env, bearer))

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
	grant := decodeStepUpGrant(t, startStepUp(t, env, bearer))

	resp := approveStepUp(t, env, grant.UserCode, env.adminCookie(t))
	assert.NotEqual(t, http.StatusOK, resp.StatusCode)

	row, err := env.store.APITokens().GetByID(ctx, tokenID)
	require.NoError(t, err)
	assert.Nil(t, row.ElevationExpiresAt)
}

// A DELEGATION bearer is refused at the ask. A worker mints it for an agent
// that reads untrusted input, so there is nobody to prompt -- and a ceremony
// it could start would end in a refusal it could not act on.
func TestCLIStepUp_RefusesACredentialThatCannotCarryOne(t *testing.T) {
	env := setupAPIAuth(t)

	// No credential at all.
	assert.Equal(t, http.StatusUnauthorized, startStepUp(t, env, "").StatusCode)

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
	resp := startStepUp(t, env, auth.FormatBearer(auth.BearerKindDelegation, delegationID, secret))
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// One grant, one window. The row is single-use, so a device code kept from an
// earlier ceremony cannot re-elevate a credential whose window has since been
// dropped or lapsed.
func TestCLIStepUp_AGrantIsSingleUse(t *testing.T) {
	env := setupAPIAuth(t)
	_, bearer := cliCredential(t, env, "operator-laptop")
	grant := decodeStepUpGrant(t, startStepUp(t, env, bearer))

	require.Equal(t, http.StatusOK, approveStepUp(t, env, grant.UserCode, env.elevatedAdminCookie(t)).StatusCode)
	require.Equal(t, http.StatusOK, pollStepUp(t, env, grant.DeviceCode).StatusCode)

	env.clock.advance(2 * time.Duration(grant.Interval) * time.Second)
	assert.Equal(t, http.StatusBadRequest, pollStepUp(t, env, grant.DeviceCode).StatusCode,
		"a consumed grant cannot be exchanged twice")
}
