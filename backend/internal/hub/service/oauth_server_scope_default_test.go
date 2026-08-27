package service_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// An OMITTED scope is the app's registered ceiling MINUS the admin family, on
// every leg that binds one.
//
// RFC 6749 section 3.3 lets the server choose this default, and the choice is
// load-bearing. Reading silence as the whole ceiling made a missing form field
// the widest possible ask: the consent page then offered an administrator hub
// administration for an app that never requested it, buried in a permission
// list of eighteen lines. An admin permission must be named.
//
// Both legs are covered because they resolve the ask separately -- the consent
// POST binds it into the authorization code, the device approval binds it into
// the grant row -- and a fix to one would leave the other open.
func TestOmittedScopeDefaultsBelowTheAdminFamily(t *testing.T) {
	t.Parallel()

	adminTokens := func() []string {
		var out []string
		for _, scope := range authscope.AdminScopes() {
			token, ok := authscope.Token(scope)
			require.True(t, ok)
			out = append(out, token)
		}
		return out
	}()
	// The CLI's registration reaches admin:*, so the default is a real
	// subtraction here rather than a set that never held them.
	require.NotEmpty(t, adminTokens)

	assertNoAdminScope := func(t *testing.T, granted string) {
		t.Helper()
		require.NotEmpty(t, granted, "the default must still grant the app's ordinary permissions")
		for _, token := range adminTokens {
			assert.NotContainsf(t, strings.Fields(granted), token,
				"an omitted scope must not grant %s", token)
		}
	}

	t.Run("the consent POST", func(t *testing.T) {
		t.Parallel()
		env := setupAPIAuth(t)
		verifier, challenge := pkceVerifierAndChallenge()

		resp := postConsentForm(t, env.server.URL+"/oauth/consent", url.Values{
			"client_id":             {oauthapp.ControlCLIClientID},
			"response_type":         {"code"},
			"code_challenge_method": {"S256"},
			"redirect_uri":          {"http://127.0.0.1:54321/callback"},
			"state":                 {"state-1"},
			"code_challenge":        {challenge},
			"decision":              {"allow"},
			"installation_name":     {"laptop"},
		}, env.elevatedAdminCookie(t))
		require.Equal(t, http.StatusFound, resp.StatusCode)
		loc, err := url.Parse(resp.Header.Get("Location"))
		require.NoError(t, err)

		token, err := http.PostForm(env.server.URL+"/oauth/token", url.Values{
			"grant_type":    {"authorization_code"},
			"client_id":     {oauthapp.ControlCLIClientID},
			"redirect_uri":  {"http://127.0.0.1:54321/callback"},
			"code":          {loc.Query().Get("code")},
			"code_verifier": {verifier},
		})
		require.NoError(t, err)
		defer func() { _ = token.Body.Close() }()
		require.Equal(t, http.StatusOK, token.StatusCode)
		body := decodeJSONBody(t, token)
		granted, _ := body["scope"].(string)
		assertNoAdminScope(t, granted)
	})

	t.Run("the device approval", func(t *testing.T) {
		t.Parallel()
		env := setupAPIAuth(t)
		cookie := env.elevatedAdminCookie(t)

		deviceCode, userCode := startDeviceAuthorization(t, env, nil)
		approve, err := postForm(env.server.URL+"/oauth/device",
			url.Values{"user_code": {userCode}, "decision": {"allow"}}, cookie)
		require.NoError(t, err)
		defer func() { _ = approve.Body.Close() }()
		require.Equal(t, http.StatusOK, approve.StatusCode)

		row, err := env.store.DeviceAuthorizations().Get(context.Background(), deviceCode)
		require.NoError(t, err)
		assertNoAdminScope(t, row.GrantedScopes)
	})

	// An app that ASKS still gets it, so the default is a default and not a
	// ceiling nobody can reach.
	t.Run("a named admin scope is still granted", func(t *testing.T) {
		t.Parallel()
		env := setupAPIAuth(t)
		cookie := env.elevatedAdminCookie(t)

		deviceCode, userCode := startDeviceAuthorization(t, env, url.Values{"scope": {"admin:read"}})
		approve, err := postForm(env.server.URL+"/oauth/device",
			url.Values{"user_code": {userCode}, "decision": {"allow"}}, cookie)
		require.NoError(t, err)
		defer func() { _ = approve.Body.Close() }()
		require.Equal(t, http.StatusOK, approve.StatusCode)

		row, err := env.store.DeviceAuthorizations().Get(context.Background(), deviceCode)
		require.NoError(t, err)
		assert.Equal(t, "admin:read", row.GrantedScopes)

		// And it reaches the stored credential, not just the grant row.
		token, err := http.PostForm(env.server.URL+"/oauth/token", url.Values{
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"client_id":   {oauthapp.ControlCLIClientID},
			"device_code": {deviceCode},
		})
		require.NoError(t, err)
		defer func() { _ = token.Body.Close() }()
		require.Equal(t, http.StatusOK, token.StatusCode)

		page, err := env.store.APITokens().ListByUser(context.Background(), store.ListAPITokensByUserParams{
			UserID: userid.MustNew(env.userID), PageParams: store.PageParams{Limit: 50},
		})
		require.NoError(t, err)
		require.Len(t, page.Rows, 1)
		assert.Equal(t, "admin:read", page.Rows[0].GrantedScopes)
	})
}
