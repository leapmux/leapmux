package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/peer"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/sendq"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// The two long-lived WebSocket endpoints are the only doors on the hub that
// carry a leapmux bearer and are NOT Connect procedures, so the interceptor's
// scope rung never sees them. Each states its own required scope at
// construction instead; these tests drive both poles of that rung.
//
// The refusal is asserted EXACTLY (403) and the admission is asserted only as
// "not the refusal", because what an admitted request does next is the
// endpoint's business: /ws/channel fails the upgrade, /ws/userevents reaches
// its registry. Pinning either of those here would make this test fail for a
// reason that has nothing to do with the grant.
//
// A test whose caller carries NO credential names 401 as well, because there
// "not 403" is what an unauthenticated answer gives: the request would never
// reach the scope rung, and the test would report a pass for a door it never
// opened.

// mintScopedAPIToken mints an app credential with an arbitrary grant and
// returns the bearer.
func mintScopedAPIToken(t *testing.T, st store.Store, tv *auth.TokenValidator, granted string) string {
	t.Helper()
	u, err := st.Users().GetByUsername(context.Background(), "admin")
	require.NoError(t, err)
	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID: tokenID, UserID: userid.MustNew(u.ID), ClientID: oauthapp.ControlCLIClientID,
		InstallationName: "test", GrantedScopes: granted,
		SecretHash: tv.HashSecret(secret),
	}))
	return auth.FormatBearer(auth.BearerKindAPI, tokenID, secret)
}

// wsScopeEnv is the pair of handlers plus the store both authenticate against,
// so one credential can be presented to each endpoint in turn.
type wsScopeEnv struct {
	channel    *ChannelRelayHandler
	userEvents *UserEventsHandler
	store      store.Store
	validator  *auth.TokenValidator
}

func newWSScopeEnv(t *testing.T, soloUser *auth.UserInfo) wsScopeEnv {
	t.Helper()
	st := hubtestutil.OpenTestStore(t)
	hubtestutil.CreateTestAdmin(t, st)
	tv, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	return wsScopeEnv{
		channel: NewChannelRelayHandler(st, newTestRegistry(), nil, wsScopeAuthContexts(t, st, soloUser), soloUser,
			insecureCookies, sendq.NewMaxBytesPoolForTest()).WithTokenValidator(tv),
		userEvents: NewUserEventsHandler(st, unreachableCRDTRegistry(), wsScopeAuthContexts(t, st, soloUser), soloUser,
			insecureCookies, sendq.NewMaxBytesPoolForTest()).WithTokenValidator(tv),
		store:     st,
		validator: tv,
	}
}

// wsScopeAuthContexts builds the registry whose SoloGate the WebSocket doors
// read. The gate must be told that this hub IS solo.
//
// `newTestAuthContexts` passes no SoloUser, so its gate answers "not a solo
// hub" and refuses every caller. A solo test built on that gate reached no
// solo rung at all: an absent credential answered 401, which satisfied an
// assertion written as "not the 403 refusal", so the test passed without the
// behavior it names.
func wsScopeAuthContexts(t *testing.T, st store.Store, soloUser *auth.UserInfo) *auth.AuthContextRegistry {
	t.Helper()
	if soloUser == nil {
		return newTestAuthContexts(t)
	}
	_, registry := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st, SoloUser: soloUser})
	t.Cleanup(registry.Stop)
	return registry
}

// unreachableCRDTRegistry is a registry whose factory always fails.
//
// /ws/userevents reaches it one step PAST the scope rung, so a nil one turns
// an admitted request into a nil dereference rather than a status. Its answer
// is never asserted here -- only that it is not the refusal -- so failing is
// the cheapest way to reach that step without building a document.
func unreachableCRDTRegistry() *crdt.Registry {
	return crdt.NewRegistry(func(context.Context, userid.UserID) (*crdt.Manager, error) {
		return nil, errors.New("this test never builds a document")
	}, nil)
}

// serveWithBearer runs one request against one handler and reports the status.
//
// The request arrives on the LOCAL IPC socket, which is the one transport the
// solo rung admits without a credential. A TCP request takes the bearer and
// cookie rungs like any other caller's, so a solo test built on one would pass
// whether or not the rung exists. serveOverTCP is that other transport, for
// the tests that assert the refusal.
func serveWithBearer(t *testing.T, handler http.Handler, path, bearer string) int {
	t.Helper()
	return serveWS(t, handler, path, bearer, peer.WithLocalIPC(context.Background()))
}

// serveOverTCP is the same request on a TCP connection.
func serveOverTCP(t *testing.T, handler http.Handler, path, bearer string) int {
	t.Helper()
	return serveWS(t, handler, path, bearer, context.Background())
}

func serveWS(t *testing.T, handler http.Handler, path, bearer string, ctx context.Context) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Code
}

// TestWebSocketEndpointsRefuseACredentialWithoutTheirScope is the pair that
// makes the rung real: each endpoint admits its OWN scope and refuses the
// other's.
//
// Asserting one direction alone would pass for a handler that refuses
// everything, which is why the neighbour scope is presented as well.
func TestWebSocketEndpointsRefuseACredentialWithoutTheirScope(t *testing.T) {
	t.Parallel()

	env := newWSScopeEnv(t, nil)
	workspaceOnly := mintScopedAPIToken(t, env.store, env.validator, "workspace:read")
	workerOnly := mintScopedAPIToken(t, env.store, env.validator, "worker:read")

	t.Run("channel refuses a grant without worker:read", func(t *testing.T) {
		assert.Equal(t, http.StatusForbidden,
			serveWithBearer(t, env.channel, "/ws/channel", workspaceOnly),
			"opening a channel is reaching a MACHINE; workspace:read is not that permission")
	})
	t.Run("channel admits worker:read", func(t *testing.T) {
		assert.NotEqual(t, http.StatusForbidden,
			serveWithBearer(t, env.channel, "/ws/channel", workerOnly))
	})
	t.Run("user events refuses a grant without workspace:read", func(t *testing.T) {
		assert.Equal(t, http.StatusForbidden,
			serveWithBearer(t, env.userEvents, "/ws/userevents", workerOnly),
			"the stream IS the account's layout document; worker:read is not that permission")
	})
	t.Run("user events admits workspace:read", func(t *testing.T) {
		assert.NotEqual(t, http.StatusForbidden,
			serveWithBearer(t, env.userEvents, "/ws/userevents", workspaceOnly))
	})
}

// TestWebSocketScopeRefusalIsForbiddenNotUnauthorized pins the STATUS, because
// the two answers ask the client for different things and only one of them is
// true here. A 401 would send an app back through a whole sign-in ceremony
// that ends in the same refusal.
func TestWebSocketScopeRefusalIsForbiddenNotUnauthorized(t *testing.T) {
	t.Parallel()

	env := newWSScopeEnv(t, nil)
	narrow := mintScopedAPIToken(t, env.store, env.validator, "workspace:read")

	assert.Equal(t, http.StatusForbidden, serveWithBearer(t, env.channel, "/ws/channel", narrow))
	// And an ABSENT credential still answers 401, so the new status did not
	// swallow the old one.
	assert.Equal(t, http.StatusUnauthorized, serveWithBearer(t, env.channel, "/ws/channel", ""))
}

// TestUnscopedCredentialPassesTheWebSocketScopeRung is the composition rule as
// a test: a scope SUBTRACTS from the account's own authority and never adds to
// it, so an unscoped caller passes this rung unconditionally.
//
// Solo mode is the unscoped caller used here because it needs no cookie: its
// synthetic user carries authscope.UnscopedGrant() explicitly, which is the
// same value a browser session carries.
func TestUnscopedCredentialPassesTheWebSocketScopeRung(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	hubtestutil.CreateTestAdmin(t, st)
	solo := &auth.UserInfo{
		ID:       userid.MustNew("u-solo"),
		Username: "solo",
		IsAdmin:  true,
		Scopes:   authscope.UnscopedGrant(),
		Solo:     true,
	}
	env := newWSScopeEnv(t, solo)

	for _, door := range []struct {
		name    string
		handler http.Handler
		path    string
	}{
		{"channel", env.channel, "/ws/channel"},
		{"userevents", env.userEvents, "/ws/userevents"},
	} {
		status := serveWithBearer(t, door.handler, door.path, "")
		assert.NotEqualf(t, http.StatusForbidden, status, "%s must not refuse an unscoped caller", door.name)
		// 401 too, and this is the assertion that makes the test real. The
		// admission is asserted as "not the refusal" because what an admitted
		// request does next is the endpoint's business -- but an
		// UNAUTHENTICATED answer is also "not 403", so a test that named only
		// the 403 passed without ever reaching the scope rung.
		assert.NotEqualf(t, http.StatusUnauthorized, status,
			"%s must admit the solo caller before the scope rung answers", door.name)
	}
}

// The other side of the same rung, and the property the restricted TCP setup
// flow rests on: the WebSocket doors are the two credential-carrying surfaces
// that are not Connect procedures, so the interceptor's refusal does not cover
// them. A solo TCP caller with no credential must be refused here as well, or
// the synthetic administrator is one handshake away from anyone who reaches
// the port.
func TestSoloModeRefusesATCPCallerWithoutACredential(t *testing.T) {
	t.Parallel()

	solo := &auth.UserInfo{
		ID:       userid.MustNew("u-solo"),
		Username: "solo",
		IsAdmin:  true,
		Scopes:   authscope.UnscopedGrant(),
		Solo:     true,
	}
	env := newWSScopeEnv(t, solo)

	assert.Equal(t, http.StatusUnauthorized, serveOverTCP(t, env.channel, "/ws/channel", ""))
	assert.Equal(t, http.StatusUnauthorized, serveOverTCP(t, env.userEvents, "/ws/userevents", ""))
}

// TestSoloModeYieldsToAPresentedBearer is the WebSocket twin of the
// interceptor's solo test. Solo authenticates a LOCAL IPC caller as its one
// account, so without the yield this request would pass the rung on the
// synthetic user's unscoped grant -- discarding the narrowing the credential's
// owner accepted, which is the one thing the scope model exists to prevent.
// The request below arrives on that socket, because a TCP one never meets the
// rung and would prove nothing about the yield.
func TestSoloModeYieldsToAPresentedBearer(t *testing.T) {
	t.Parallel()

	solo := &auth.UserInfo{
		ID:       userid.MustNew("u-solo"),
		Username: "solo",
		IsAdmin:  true,
		Scopes:   authscope.UnscopedGrant(),
		Solo:     true,
	}
	env := newWSScopeEnv(t, solo)
	narrow := mintScopedAPIToken(t, env.store, env.validator, "workspace:read")

	assert.Equal(t, http.StatusForbidden, serveWithBearer(t, env.channel, "/ws/channel", narrow),
		"the solo rung must yield to a presented bearer, so its grant binds on the WebSocket doors too")
	// Presence, not validity: a malformed bearer is refused by the bearer
	// rung rather than falling back to solo.
	assert.Equal(t, http.StatusUnauthorized, serveWithBearer(t, env.channel, "/ws/channel", "lmx_only-one-piece"))
}

// TestNewWSAuthenticatorPanicsOnANonGrantableScope pins the construction-time
// refusal.
//
// The zero value is the one that matters: a new endpoint whose author forgot
// the argument would otherwise construct an authenticator that demands
// SCOPE_UNSPECIFIED, which no grant carries -- so the endpoint would refuse
// every scoped credential in production while every test that uses a session
// passed. A boot panic is the answer, matching the Worker registrar's.
func TestNewWSAuthenticatorPanicsOnANonGrantableScope(t *testing.T) {
	t.Parallel()

	for _, scope := range []leapmuxv1.Scope{
		leapmuxv1.Scope_SCOPE_UNSPECIFIED,
		leapmuxv1.Scope_SCOPE_NEVER,
		leapmuxv1.Scope_SCOPE_ALL,
	} {
		t.Run(scope.String(), func(t *testing.T) {
			assert.Panics(t, func() {
				newWSAuthenticator(nil, newTestAuthContexts(t), nil, insecureCookies, scope)
			})
		})
	}
}
