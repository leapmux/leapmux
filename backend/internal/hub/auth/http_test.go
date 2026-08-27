package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// The session-cookie rung of AuthenticateHTTP is ASYMMETRIC, exactly as
// OAuthNonceFromRequest is, and for the same reason.
//
// It reads the __Host- spelling first, and on a hub that writes that spelling
// it REFUSES the unprefixed name rather than trying it as a fallback: any
// plain-HTTP page on the registrable domain can plant `leapmux-session`, which
// is precisely what the __Host- prefix exists to prevent. The old option
// carried an ordered list of secure modes, and the two most consequential
// HTTP surfaces -- the /oauth/* consent legs and the OAuth
// re-authentication leg that GRANTS an elevation -- passed `{false, true}`, so
// a planted cookie took priority over the real one.
//
// On a hub that does NOT write the prefixed spelling the fallback is safe and
// stays, so a session issued under TLS still validates after an operator turns
// secure_cookies off.

// httpAuthFixture is a store holding two signed-in accounts, so a case can
// tell WHICH cookie AuthenticateHTTP resolved rather than only that it
// resolved one.
type httpAuthFixture struct {
	st store.Store
	// realToken belongs to the account the browser signed in as.
	realToken string
	realID    string
	// plantedToken belongs to a different account, and stands for the cookie
	// a plain-HTTP page on the registrable domain wrote.
	plantedToken string
	plantedID    string
}

func newHTTPAuthFixture(t *testing.T) httpAuthFixture {
	t.Helper()
	ctx := context.Background()
	st := hubtestutil.OpenTestStore(t)
	hubtestutil.CreateTestAdmin(t, st)

	real, err := st.Users().GetByUsername(ctx, hubtestutil.TestAdminUsername)
	require.NoError(t, err)
	realToken, _, err := auth.CreateSession(ctx, st, userid.MustNew(real.ID), auth.DefaultSessionDuration)
	require.NoError(t, err)

	plantedID := hubtestutil.CreateTestUser(t, st, "attacker", "attackerpass123")
	plantedToken, _, err := auth.CreateSession(ctx, st, userid.MustNew(plantedID), auth.DefaultSessionDuration)
	require.NoError(t, err)

	return httpAuthFixture{
		st: st, realToken: realToken, realID: real.ID,
		plantedToken: plantedToken, plantedID: plantedID,
	}
}

// request builds a GET carrying the named cookies verbatim. The names are
// written out rather than built through BuildSessionCookie, because the
// SPELLING is the subject and a helper that chose it would hide the case.
func (f httpAuthFixture) request(cookies map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/oauth/authorize", nil)
	for name, value := range cookies {
		r.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	return r
}

func (f httpAuthFixture) authenticate(r *http.Request, secureCookies bool) (*auth.UserInfo, error) {
	return auth.AuthenticateHTTP(context.Background(), r, auth.HTTPAuthOpts{
		Store:         f.st,
		ReadCookie:    true,
		SecureCookies: secureCookies,
	})
}

// TestAuthenticateHTTPRefusesThePlantableSpellingOnASecureCookieHub is the
// refusal itself. A hub that writes __Host- must never accept the unprefixed
// name, whatever live session it carries.
func TestAuthenticateHTTPRefusesThePlantableSpellingOnASecureCookieHub(t *testing.T) {
	t.Parallel()
	f := newHTTPAuthFixture(t)

	user, err := f.authenticate(f.request(map[string]string{auth.CookieName: f.realToken}), true)
	require.Error(t, err, "any plain-HTTP page on the domain can write this name")
	assert.ErrorIs(t, err, auth.ErrHTTPUnauthenticated)
	assert.Nil(t, user)
}

// TestAuthenticateHTTPPrefersThePrefixedSpelling is the PRIORITY half, and
// the one the old ordered list got backwards. A browser sends every cookie
// whose name matches, so the planted one arrives beside the real one; the
// rung must resolve the spelling only an https origin could have written.
func TestAuthenticateHTTPPrefersThePrefixedSpelling(t *testing.T) {
	t.Parallel()
	f := newHTTPAuthFixture(t)

	r := f.request(map[string]string{
		auth.SecureCookieName: f.realToken,
		auth.CookieName:       f.plantedToken,
	})
	user, err := f.authenticate(r, true)
	require.NoError(t, err)
	assert.Equal(t, f.realID, user.ID.String(),
		"the planted spelling must never outrank the one only https can set")

	// And the same order holds on a hub that does NOT write the prefixed
	// spelling, so turning secure_cookies off cannot promote a planted cookie.
	user, err = f.authenticate(r, false)
	require.NoError(t, err)
	assert.Equal(t, f.realID, user.ID.String())
}

// TestAuthenticateHTTPKeepsTheFallbackOnAPlainHub is the other direction of
// the asymmetry, and the reason it is an asymmetry rather than a ban.
//
// secure_cookies is read when the session cookie is WRITTEN and again when it
// is read, so an operator who turns it off would otherwise sign every browser
// out at once. Reading the unprefixed name where the hub writes it costs
// nothing.
func TestAuthenticateHTTPKeepsTheFallbackOnAPlainHub(t *testing.T) {
	t.Parallel()
	f := newHTTPAuthFixture(t)

	user, err := f.authenticate(f.request(map[string]string{auth.CookieName: f.realToken}), false)
	require.NoError(t, err)
	assert.Equal(t, f.realID, user.ID.String())

	// The prefixed spelling still resolves there too: a session issued while
	// the setting was on must survive the operator turning it off.
	user, err = f.authenticate(f.request(map[string]string{auth.SecureCookieName: f.realToken}), true)
	require.NoError(t, err)
	assert.Equal(t, f.realID, user.ID.String())
}

// TestAuthenticateHTTPReportsAnInvalidPrefixedCookieRatherThanFallingBack
// keeps the refusal from turning into a retry.
//
// A __Host- cookie carrying a session the hub no longer holds is a signed-out
// browser, not a caller with no credentials. Falling through to the
// unprefixed name there would restore the exact priority this rung removes,
// for every request whose real session lapsed.
func TestAuthenticateHTTPReportsAnInvalidPrefixedCookieRatherThanFallingBack(t *testing.T) {
	t.Parallel()
	f := newHTTPAuthFixture(t)

	r := f.request(map[string]string{
		auth.SecureCookieName: "no-such-session",
		auth.CookieName:       f.plantedToken,
	})
	user, err := f.authenticate(r, true)
	require.Error(t, err)
	assert.ErrorIs(t, err, auth.ErrHTTPUnauthenticated)
	assert.Nil(t, user, "a dead prefixed cookie must not fall back to a plantable one")
}
