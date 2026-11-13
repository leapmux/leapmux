package auth_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/bootstrap"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/peer"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/servicetest"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/usernames"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// The solo rung must ASK SoloGate, in BOTH ladders, and ask the same one.
//
// solo_gate_test.go tests what the gate decides. This tests that the decision
// is consulted, which is the half a third ladder would forget: a gate that is
// correct and unwired hands an unauthenticated network caller the
// administrator, and every test of the gate itself still passes.
//
// The default fixture models TCP. The bearer-precedence fixture marks its
// server contexts explicitly to simulate an admitted local IPC connection.
type soloRungFixture struct {
	store     store.Store
	gate      *auth.SoloGate
	user      *auth.UserInfo
	server    *httptest.Server
	client    leapmuxv1connect.AdminSettingsServiceClient
	validator *auth.TokenValidator
}

func newSoloRungFixture(t *testing.T) soloRungFixture {
	t.Helper()
	return newSoloRungFixtureForTransport(t, false)
}

func newSoloRungFixtureForTransport(t *testing.T, localIPC bool) soloRungFixture {
	t.Helper()
	st := hubtestutil.OpenTestStore(t)
	require.NoError(t, bootstrap.Run(context.Background(), st, true))
	soloUser, err := auth.LoadSoloUser(context.Background(), st)
	require.NoError(t, err)

	// ONE gate for both ladders, which is how the hub wires it. Sharing it
	// here is what makes "the two cannot disagree" a tested property rather
	// than a comment.
	tv, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	gate := auth.NewSoloGate(true, st)
	interceptor, _ := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{SessionHasher: hubtestutil.TestSecretHasher(),
		Store: st, SoloUser: soloUser, SoloGate: gate, TokenValidator: tv,
	})
	svc := service.NewAdminSettingsService(
		servicetest.NewSettingsManager(t, st, nil), &config.Config{SoloMode: true}, st)
	connectServer := connect.NewServer(interceptor)
	leapmuxv1connect.RegisterAdminSettingsServiceHandler(connectServer, svc)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, connectServer)
	server := httptest.NewUnstartedServer(mux)
	if localIPC {
		server.Config.BaseContext = func(net.Listener) context.Context { return peer.WithLocalIPC(context.Background()) }
	}
	server.Start()
	t.Cleanup(server.Close)

	return soloRungFixture{
		store:     st,
		validator: tv,
		gate:      gate,
		user:      soloUser,
		server:    server,
		client:    leapmuxv1connect.NewAdminSettingsServiceClient(connect.NewClient(connecthttp.NewTransport(server.Client(), server.URL, connecthttp.WithReadMaxBytes(0)))),
	}
}

// connectCall runs one Connect request carrying the given headers.
func (f soloRungFixture) connectCall(headers map[string]string) error {
	ctx, info := connect.NewClientContext(context.Background())
	for name, value := range headers {
		info.RequestHeader().Set(name, value)
	}
	_, err := f.client.ListSettings(ctx, &leapmuxv1.ListSettingsRequest{})
	return err
}

// httpCall runs AuthenticateHTTP over a request carrying the given cookie,
// with the SAME gate the Connect ladder holds.
func (f soloRungFixture) httpCall(cookie *http.Cookie) (*auth.UserInfo, error) {
	r := httptest.NewRequest(http.MethodGet, "/ws/userevents", nil)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	return auth.AuthenticateHTTP(context.Background(), r, auth.HTTPAuthOpts{SessionHasher: hubtestutil.TestSecretHasher(),
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

// A passwordless TCP caller can use only the public initial-password RPC. The
// ordinary authentication ladders must not grant the synthetic administrator.
func TestSoloRung_TCPNeedsCredentialsBeforeTheAccountHasAPassword(t *testing.T) {
	f := newSoloRungFixture(t)

	err := f.connectCall(nil)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	_, httpErr := f.httpCall(nil)
	require.Error(t, httpErr)
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

	createdSession, err := auth.CreateSession(
		context.Background(), f.store, hubtestutil.TestSecretHasher(), userid.MustNew(f.user.ID.String()), auth.DefaultSessionDuration)
	require.NoError(t, err)
	// secure=false, matching the zero Policy this fixture's interceptor holds:
	// the plain cookie name is the one both ladders look for here.
	cookie := auth.BuildSessionCookie(createdSession.Token, createdSession.ExpiresAt, false)

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
// for admitted IPC even while the account has no password. The default
// TCP fixture remains credential-required in either password state.
func TestSoloRung_YieldsToAPresentedBearerEvenWithNoPassword(t *testing.T) {
	f := newSoloRungFixtureForTransport(t, true)
	require.NoError(t, f.connectCall(nil))
	mint := func(grant string) (string, string) {
		tokenID, secret := id.Generate(), auth.MintAccessSecret()
		require.NoError(t, f.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
			ID: tokenID, UserID: f.user.ID, ClientID: oauthapp.ControlCLIClientID,
			InstallationName: "IPC precedence", GrantedScopes: grant, SecretHash: f.validator.HashSecret(secret),
		}))
		return tokenID, auth.FormatBearer(auth.BearerKindAPI, tokenID, secret)
	}
	_, admitted := mint("admin:read")
	_, narrow := mint("file:read")
	revokedID, revoked := mint("admin:read")
	_, err := f.store.APITokens().Revoke(context.Background(), revokedID)
	require.NoError(t, err)
	require.NoError(t, f.connectCall(map[string]string{"Authorization": "Bearer " + admitted}))
	for _, tc := range []struct {
		name, bearer string
		code         connect.Code
	}{
		{"narrow", narrow, connect.CodePermissionDenied},
		{"malformed", "lmx_only-one-piece", connect.CodeUnauthenticated},
		{"revoked", revoked, connect.CodeUnauthenticated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := f.connectCall(map[string]string{"Authorization": "Bearer " + tc.bearer})
			require.Error(t, err)
			assert.Equal(t, tc.code, connect.CodeOf(err))
		})
	}
	httpCall := func(bearer string) (*auth.UserInfo, error) {
		ctx := peer.WithLocalIPC(context.Background())
		r := httptest.NewRequest(http.MethodGet, "/ws/userevents", nil).WithContext(ctx)
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		return auth.AuthenticateHTTP(ctx, r, auth.HTTPAuthOpts{SessionHasher: hubtestutil.TestSecretHasher(),
			Store: f.store, SoloUser: f.user, SoloGate: f.gate, Validator: f.validator, ReadCookie: true})
	}
	user, err := httpCall("")
	require.NoError(t, err)
	require.True(t, user.SoloAuthenticated())
	user, err = httpCall(narrow)
	require.NoError(t, err)
	require.False(t, user.SoloAuthenticated())
	assert.Equal(t, f.user.ID, user.ID)
	assert.True(t, user.Scopes.Allows(leapmuxv1.Scope_SCOPE_FILE_READ))
	assert.False(t, user.Scopes.Allows(leapmuxv1.Scope_SCOPE_ADMIN_READ))
	for _, bearer := range []string{"lmx_only-one-piece", revoked} {
		user, err := httpCall(bearer)
		require.ErrorIs(t, err, auth.ErrHTTPUnauthenticated)
		assert.Nil(t, user)
	}
}
