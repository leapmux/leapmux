package auth_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
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
	realSession, err := auth.CreateSession(ctx, st, hubtestutil.TestSecretHasher(), userid.MustNew(real.ID), auth.DefaultSessionDuration)
	require.NoError(t, err)

	plantedID := hubtestutil.CreateTestUser(t, st, "attacker", "attackerpass123")
	plantedSession, err := auth.CreateSession(ctx, st, hubtestutil.TestSecretHasher(), userid.MustNew(plantedID), auth.DefaultSessionDuration)
	require.NoError(t, err)

	return httpAuthFixture{
		st: st, realToken: realSession.Token, realID: real.ID,
		plantedToken: plantedSession.Token, plantedID: plantedID,
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
	return auth.AuthenticateHTTP(context.Background(), r, auth.HTTPAuthOpts{SessionHasher: hubtestutil.TestSecretHasher(),
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

// workerTokenStore replaces Workers().GetByAuthTokenHashIncludeDeleted with a fixed
// answer and leaves the rest of the store real.
type workerTokenStore struct {
	store.Store
	err error
}

func (s workerTokenStore) Workers() store.WorkerStore {
	return workerTokenLookup{WorkerStore: s.Store.Workers(), err: s.err}
}

type workerTokenLookup struct {
	store.WorkerStore
	err error
}

func (l workerTokenLookup) GetByAuthTokenHashIncludeDeleted(context.Context, []byte) (*store.Worker, error) {
	return nil, l.err
}

// AuthenticateWorkerBearer answers a rejected credential with one of its
// sentinels, and a store failure with an error that is none of them. Unauthenticated
// tells the caller that its credential is bad, which a store fault does not
// show, so a failed lookup must never read as a rejected token.
func TestAuthenticateWorkerBearer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := hubtestutil.OpenTestStore(t)
	owner := storetest.SeedUser(t, st, "worker-bearer-owner")
	worker, workerToken := hubtestutil.CreateTestWorker(t, st, owner.ID)

	t.Run("resolves a live worker", func(t *testing.T) {
		got, err := auth.AuthenticateWorkerBearer(ctx, st, hubtestutil.TestSecretHasher(), "Bearer "+workerToken)
		require.NoError(t, err)
		assert.Equal(t, worker.ID, got.ID)
		assert.Equal(t, owner.ID, got.RegisteredBy)
	})

	for _, tc := range []struct {
		name   string
		header string
	}{
		{"no header", ""},
		{"a token with no scheme", workerToken},
		{"the scheme alone", "Bearer"},
		{"the scheme and blanks", "Bearer   "},
		{"another scheme", "Basic " + workerToken},
	} {
		t.Run("refuses "+tc.name, func(t *testing.T) {
			got, err := auth.AuthenticateWorkerBearer(ctx, st, hubtestutil.TestSecretHasher(), tc.header)
			assert.ErrorIs(t, err, auth.ErrMissingBearer)
			assert.Nil(t, got)
			assert.True(t, auth.IsWorkerCredentialError(err))
		})
	}

	t.Run("refuses an unknown token", func(t *testing.T) {
		got, err := auth.AuthenticateWorkerBearer(ctx, st, hubtestutil.TestSecretHasher(), "Bearer no-such-token")
		assert.ErrorIs(t, err, auth.ErrInvalidWorkerToken)
		assert.Nil(t, got)
		assert.True(t, auth.IsWorkerCredentialError(err))
	})

	t.Run("reports a store failure as no verdict on the credential", func(t *testing.T) {
		boom := errors.New("database is locked")
		got, err := auth.AuthenticateWorkerBearer(ctx, workerTokenStore{Store: st, err: boom}, hubtestutil.TestSecretHasher(), "Bearer "+workerToken)
		require.Error(t, err)
		assert.Nil(t, got)
		assert.ErrorIs(t, err, boom, "the store's error stays reachable for the log")
		assert.NotErrorIs(t, err, auth.ErrInvalidWorkerToken)
		assert.False(t, auth.IsWorkerCredentialError(err), "a store failure must not read as a rejected token")
	})

	t.Run("still resolves a deregistering worker", func(t *testing.T) {
		leaving, leavingToken := hubtestutil.CreateTestWorker(t, st, owner.ID)
		n, err := st.Workers().Deregister(ctx, store.DeregisterWorkerParams{ID: leaving.ID, RegisteredBy: userid.MustNew(owner.ID)})
		require.NoError(t, err)
		require.Equal(t, int64(1), n)

		got, err := auth.AuthenticateWorkerBearer(ctx, st, hubtestutil.TestSecretHasher(), "Bearer "+leavingToken)
		require.NoError(t, err, "a deregistering worker keeps its calls until it is told to stop")
		assert.Equal(t, leaving.ID, got.ID)
	})

	t.Run("refuses a deleted worker", func(t *testing.T) {
		gone, goneToken := hubtestutil.CreateTestWorker(t, st, owner.ID)
		require.NoError(t, st.Workers().MarkDeleted(ctx, gone.ID))

		got, err := auth.AuthenticateWorkerBearer(ctx, st, hubtestutil.TestSecretHasher(), "Bearer "+goneToken)
		assert.ErrorIs(t, err, auth.ErrWorkerDeleted)
		assert.Nil(t, got)
		assert.True(t, auth.IsWorkerCredentialError(err))
	})
}

func TestIsWorkerCredentialError(t *testing.T) {
	t.Parallel()
	for _, sentinel := range []error{auth.ErrMissingBearer, auth.ErrInvalidWorkerToken, auth.ErrWorkerDeleted} {
		assert.Truef(t, auth.IsWorkerCredentialError(sentinel), "%v", sentinel)
		assert.Truef(t, auth.IsWorkerCredentialError(fmt.Errorf("connect: %w", sentinel)), "a wrapped %v", sentinel)
	}
	assert.False(t, auth.IsWorkerCredentialError(nil))
	assert.False(t, auth.IsWorkerCredentialError(errors.New("invalid worker auth token")),
		"an unrelated error with the same words is not the sentinel")
	assert.False(t, auth.IsWorkerCredentialError(auth.ErrHTTPUnauthenticated),
		"the HTTP sentinels belong to another helper")
}

func TestAuthenticateHTTPRefusesAnUnprovenSessionSecret(t *testing.T) {
	t.Parallel()
	fixture := newHTTPAuthFixture(t)
	sessionID, secret, ok := auth.ParseSessionToken(fixture.realToken)
	require.True(t, ok)
	replacement := "A"
	if secret[0] == 'A' {
		replacement = "B"
	}
	for _, value := range []string{sessionID, auth.FormatSessionToken(sessionID, replacement+secret[1:])} {
		request := fixture.request(map[string]string{auth.CookieName: value})
		user, err := auth.AuthenticateHTTP(t.Context(), request, auth.HTTPAuthOpts{Store: fixture.st, SessionHasher: hubtestutil.TestSecretHasher(), ReadCookie: true})
		assert.Nil(t, user)
		assert.ErrorIs(t, err, auth.ErrHTTPUnauthenticated)
	}
	user, err := auth.AuthenticateHTTP(t.Context(), fixture.request(map[string]string{auth.CookieName: fixture.realToken}), auth.HTTPAuthOpts{Store: fixture.st, SessionHasher: hubtestutil.TestSecretHasher(), ReadCookie: true})
	require.NoError(t, err)
	assert.Equal(t, fixture.realID, user.ID.String())
}

func TestAuthenticateHTTPRequiresACookieHasher(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	_, err := auth.AuthenticateHTTP(t.Context(), request, auth.HTTPAuthOpts{ReadCookie: true})
	require.ErrorContains(t, err, "SessionHasher")
	assert.NotErrorIs(t, err, auth.ErrHTTPUnauthenticated)
}
