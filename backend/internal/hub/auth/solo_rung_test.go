package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/bootstrap"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/servicetest"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/usernames"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// The solo rung must ASK SoloGate, in BOTH ladders, and ask the same one.
//
// solo_gate_test.go tests what the gate decides. This tests that the decision
// is consulted, which is the half a third ladder would forget: a gate that is
// correct and unwired hands an unauthenticated network caller the
// administrator, and every test of the gate itself still passes.
//
// Every request here arrives over TCP -- httptest.NewServer binds one, and a
// direct AuthenticateHTTP call carries no local-IPC mark -- so this is the
// exposed case of the rule table. The IPC case is the gate's own test.
type soloRungFixture struct {
	store  store.Store
	gate   *auth.SoloGate
	user   *auth.UserInfo
	server *httptest.Server
	client leapmuxv1connect.AdminSettingsServiceClient
}

func newSoloRungFixture(t *testing.T) soloRungFixture {
	t.Helper()
	st := hubtestutil.OpenTestStore(t)
	require.NoError(t, bootstrap.Run(context.Background(), st, true))
	soloUser, err := auth.LoadSoloUser(context.Background(), st)
	require.NoError(t, err)

	// ONE gate for both ladders, which is how the hub wires it. Sharing it
	// here is what makes "the two cannot disagree" a tested property rather
	// than a comment.
	gate := auth.NewSoloGate(true, st)
	interceptor, _ := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{
		Store: st, SoloUser: soloUser, SoloGate: gate,
	})
	svc := service.NewAdminSettingsService(
		servicetest.NewSettingsManager(t, st, nil), &config.Config{SoloMode: true}, st)
	path, handler := leapmuxv1connect.NewAdminSettingsServiceHandler(svc, connect.WithInterceptors(interceptor))
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return soloRungFixture{
		store:  st,
		gate:   gate,
		user:   soloUser,
		server: server,
		client: leapmuxv1connect.NewAdminSettingsServiceClient(server.Client(), server.URL),
	}
}

// connectCall runs one Connect request carrying the given headers.
func (f soloRungFixture) connectCall(headers map[string]string) error {
	req := connect.NewRequest(&leapmuxv1.ListSettingsRequest{})
	for name, value := range headers {
		req.Header().Set(name, value)
	}
	_, err := f.client.ListSettings(context.Background(), req)
	return err
}

// httpCall runs AuthenticateHTTP over a request carrying the given cookie,
// with the SAME gate the Connect ladder holds.
func (f soloRungFixture) httpCall(cookie *http.Cookie) (*auth.UserInfo, error) {
	r := httptest.NewRequest(http.MethodGet, "/ws/userevents", nil)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	return auth.AuthenticateHTTP(context.Background(), r, auth.HTTPAuthOpts{
		Store: f.store, SoloUser: f.user, SoloGate: f.gate, ReadCookie: true,
	})
}

// setSoloPassword stores a usable hash on the solo account.
func (f soloRungFixture) setSoloPassword(t *testing.T) {
	t.Helper()
	user, err := f.store.Users().GetByUsername(context.Background(), usernames.Solo)
	require.NoError(t, err)
	hash, err := password.Hash("correct-horse-battery-staple")
	require.NoError(t, err)
	require.NoError(t, f.store.Users().UpdatePassword(context.Background(), store.UpdateUserPasswordParams{
		PasswordHash: hash, ID: user.ID,
	}))
}

// The bootstrap state: TCP is credential-free, because a hub reached from a
// browser over TCP and nothing else would otherwise have no way to set its
// first password.
func TestSoloRung_TCPIsCredentialFreeWhileTheAccountHasNoPassword(t *testing.T) {
	f := newSoloRungFixture(t)

	require.NoError(t, f.connectCall(nil), "the Connect ladder must admit a bare TCP caller")

	user, err := f.httpCall(nil)
	require.NoError(t, err, "the HTTP ladder must admit the same caller")
	assert.True(t, user.SoloAuthenticated())
}

// The rule arms itself the moment the password lands, in both ladders and with
// no restart: the gate re-reads the store while its latch is false.
func TestSoloRung_TCPNeedsCredentialsOnceTheAccountHasAPassword(t *testing.T) {
	f := newSoloRungFixture(t)
	f.setSoloPassword(t)

	err := f.connectCall(nil)
	require.Error(t, err, "the Connect ladder must stop admitting a bare TCP caller")
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	_, httpErr := f.httpCall(nil)
	require.Error(t, httpErr, "the HTTP ladder must refuse the same caller, or the WebSocket relays stay open")
}

// And a session gets the caller back in, which is the other half: the rule
// demands credentials, it does not lock the owner out.
func TestSoloRung_ASessionCookieAuthenticatesOverTCP(t *testing.T) {
	f := newSoloRungFixture(t)
	f.setSoloPassword(t)

	sessionID, expiresAt, err := auth.CreateSession(
		context.Background(), f.store, userid.MustNew(f.user.ID.String()), auth.DefaultSessionDuration)
	require.NoError(t, err)
	// secure=false, matching the zero Policy this fixture's interceptor holds:
	// the plain cookie name is the one both ladders look for here.
	cookie := auth.BuildSessionCookie(sessionID, expiresAt, false)

	// Name=value alone. cookie.String() renders the SET-Cookie form, whose
	// attributes do not belong in a request header.
	require.NoError(t, f.connectCall(map[string]string{"Cookie": cookie.Name + "=" + cookie.Value}))

	user, httpErr := f.httpCall(cookie)
	require.NoError(t, httpErr)
	assert.Equal(t, f.user.ID.String(), user.ID.String())
	assert.False(t, user.SoloAuthenticated(),
		"a cookie-authenticated caller holds a real session, not the synthetic solo identity")
}

// The rung yields on the PRESENCE of a leapmux bearer, not on its validity.
//
// A broken bearer must be refused, never answered "you are the solo user, you
// may do anything" -- which would make a revoked credential stronger than a
// working one, and would leave the scope model inert on a solo hub. This holds
// while the account has NO password, which is the state the solo rung would
// otherwise admit outright.
func TestSoloRung_YieldsToAPresentedBearerEvenWithNoPassword(t *testing.T) {
	f := newSoloRungFixture(t)

	err := f.connectCall(map[string]string{
		"Authorization": "Bearer " + auth.FormatBearer(auth.BearerKindDelegation, "not-a-token", "not-a-secret"),
	})
	require.Error(t, err, "a presented bearer must be judged on its own, not fall back to the solo rung")
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}
