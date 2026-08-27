package service_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/settings"
)

// The two HTTP surfaces that read a session cookie for themselves, driven on
// a hub in its PRODUCTION shape: secure_cookies on.
//
// AuthenticateHTTP reads the __Host- spelling first and REFUSES the
// unprefixed name where the hub writes the prefixed one, because any
// plain-HTTP page on the registrable domain can plant `leapmux-session`. Each
// leg passes its own SecureCookies argument, so the rule holds only where the
// leg reads the hub's setting rather than a constant -- and these two legs
// are exactly where a planted cookie used to take priority: the consent leg
// mints a command-line credential, and the re-authentication leg grants an
// elevation. http_test.go in internal/hub/auth pins the rule; these observe
// the wiring.

// sessionCookieNamed carries one session id under one spelling.
func sessionCookieNamed(name, sessionID string) *http.Cookie {
	return &http.Cookie{Name: name, Value: sessionID}
}

// TestCLIConsentLegReadsTheHubsCookieSpelling covers /auth/cli/start.
//
// A signed-in, elevated browser reaches the consent page. The same session
// under the plantable spelling reaches the sign-in bounce instead, because
// this hub writes the prefixed name and the leg must not accept a name a
// plain-HTTP page could have set.
func TestCLIConsentLegReadsTheHubsCookieSpelling(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	require.NoError(t, settings.KeySecureCookies.Set(context.Background(), env.set, true))
	cookie := env.elevatedAdminCookie(t)
	target := env.server.URL + "/auth/cli/start?" + startQuery(nil).Encode()

	resp := getWithCookie(t, target, sessionCookieNamed(auth.SecureCookieName, cookie.Value))
	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"the prefixed spelling is the one this hub writes, so the consent page must render")

	resp = getWithCookie(t, target, sessionCookieNamed(auth.CookieName, cookie.Value))
	require.Equal(t, http.StatusFound, resp.StatusCode,
		"the plantable spelling must not authenticate a consent leg")
	loc, err := resp.Location()
	require.NoError(t, err)
	assert.Equal(t, "/login", loc.Path,
		"an unauthenticated consent leg bounces the browser to sign in")
}

// TestOAuthReauthLegReadsTheHubsCookieSpelling covers the other leg, and it
// is the one that GRANTS: a session it authenticates here comes back
// elevated, which admits every gate the window protects including the
// command-line credential mint.
func TestOAuthReauthLegReadsTheHubsCookieSpelling(t *testing.T) {
	t.Parallel()

	server, st, ks, set := setupOAuthTestServerWithSettings(t)
	require.NoError(t, settings.KeySecureCookies.Set(context.Background(), set, true))
	providerID := createTestProvider(t, st, ks)
	cookie, _, _ := reauthSession(t, st, providerID, "sub-spelling")

	// The prefixed spelling authenticates, so the leg sends the browser on to
	// the identity provider.
	state, _, _ := beginReauth(t, server, providerID,
		sessionCookieNamed(auth.SecureCookieName, cookie.Value), "")
	assert.NotEmpty(t, state)

	// The plantable spelling does not, and the refusal is the one that says
	// there is no session rather than a redirect to a provider.
	req, err := http.NewRequest(http.MethodGet, server.URL+"/auth/oauth/"+providerID+"/reauth", nil)
	require.NoError(t, err)
	req.AddCookie(sessionCookieNamed(auth.CookieName, cookie.Value))
	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"a planted cookie must not start a step-up that ends in an elevation")
	assert.Contains(t, readBody(t, resp), "sign in first")
}
