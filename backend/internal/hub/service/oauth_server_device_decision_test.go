package service_test

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Deny is EVERY value that is not "allow", including an ABSENT one, and the
// absent case is the one worth a test.
//
// A consent form that lost its button, a client that posted the code alone, or
// a rename of the field all arrive here as a missing parameter. Reading that as
// approval would grant an app everything it asked for because a form field went
// missing -- so the default is the refusal, and it is FINAL: the approve
// statement matches a pending row only, so a denied grant can never later
// become approved.
func TestDeviceApproval_AnAbsentDecisionDeniesFinally(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	ctx := context.Background()
	cookie := env.elevatedAdminCookie(t)

	deviceCode, userCode := startDeviceAuthorization(t, env, nil)

	resp, err := postForm(env.server.URL+"/oauth/device", url.Values{"user_code": {userCode}}, cookie)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, bodyOf(t, resp), "Refused")

	row, err := env.store.DeviceAuthorizations().Get(ctx, deviceCode)
	require.NoError(t, err)
	assert.EqualValues(t, 2, row.Approved, "an absent decision must record a denial")
	assert.Empty(t, row.GrantedScopes, "a denial grants nothing")

	// A SECOND post, this time saying allow, must change nothing. Without
	// this the refusal would only be a pause.
	retry, err := postForm(env.server.URL+"/oauth/device",
		url.Values{"user_code": {userCode}, "decision": {"allow"}}, cookie)
	require.NoError(t, err)
	defer func() { _ = retry.Body.Close() }()

	after, err := env.store.DeviceAuthorizations().Get(ctx, deviceCode)
	require.NoError(t, err)
	assert.EqualValues(t, 2, after.Approved, "a denial is final")
	assert.Empty(t, after.UserID, "a denied grant must never bind an account")
}

// TestDeviceApproval_DenyingAnUnknownCodeIsNotSuccess pins the deny branch's
// nil check. The allow branch already answered errDeviceGrantNotApprovable for
// a code that is unknown, mistyped or already answered; the deny branch used to
// render the "Refused" success page anyway, which told the person at the
// keyboard they blocked a grant that stayed pending -- and for an
// already-approved grant, that a poller would keep a credential they believed
// they had stopped.
func TestDeviceApproval_DenyingAnUnknownCodeIsNotSuccess(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	cookie := env.elevatedAdminCookie(t)

	for name, code := range map[string]string{
		"a mistyped code": "ZZZZZZ",
		"an empty code":   "",
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := postForm(env.server.URL+"/oauth/device",
				url.Values{"user_code": {code}, "decision": {"deny"}}, cookie)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode,
				"the person at the keyboard can correct the code they typed")
		})
	}

	// An ANSWERED grant denies nothing further: the twin statement matches a
	// pending row only, and the second submit must not re-render success.
	_, userCode := startDeviceAuthorization(t, env, nil)
	allow, err := postForm(env.server.URL+"/oauth/device",
		url.Values{"user_code": {userCode}, "decision": {"allow"}}, cookie)
	require.NoError(t, err)
	defer func() { _ = allow.Body.Close() }()
	require.Equal(t, http.StatusOK, allow.StatusCode)

	deny, err := postForm(env.server.URL+"/oauth/device",
		url.Values{"user_code": {userCode}, "decision": {"deny"}}, cookie)
	require.NoError(t, err)
	defer func() { _ = deny.Body.Close() }()
	assert.Equal(t, http.StatusBadRequest, deny.StatusCode,
		"a grant that was already answered keeps its answer")
}
