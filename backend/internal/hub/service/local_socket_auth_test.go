package service_test

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/bootstrap"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
)

// shortSocketPath builds a path under os.TempDir() short enough to fit
// the platform's sun_path limit (~104 chars on macOS). t.TempDir()
// produces directories under /var/folders/.../T/<long-test-name>/...
// which routinely exceed the limit and fail bind() with EINVAL.
func shortSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(os.TempDir(), "leapmux-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "h.sock")
}

// The plan calls out: a multi-user hub listening on a Unix domain socket
// must NOT auto-authenticate any caller just because the connection
// arrived locally. Solo mode is the only scenario where local-socket
// auto-auth is acceptable.
//
// In the current code there is no separate `IsUnixSocket()` auto-auth
// branch — the only auto-auth path fires when the interceptor is
// constructed with `soloUser != nil`. These tests pin that invariant
// down so any future refactor that adds a "trust local sockets"
// shortcut breaks loudly.

// newUnixSocketAuthClient brings up a real unix-domain listener
// running an AuthServiceHandler behind the auth interceptor, then
// returns a connect client dialled over it.
func newUnixSocketAuthClient(t *testing.T, st store.Store, interceptor connect.Interceptor) leapmuxv1connect.AuthServiceClient {
	t.Helper()

	mux := http.NewServeMux()
	authSvc := service.NewAuthService(st, &config.Config{}, auth.NewCredentialLifecycleEffects(nil, nil, nil), nil, mail.NewStubSender(), mail.Renderer{})
	path, handler := leapmuxv1connect.NewAuthServiceHandler(authSvc, connect.WithInterceptors(interceptor))
	mux.Handle(path, handler)

	ln, err := net.Listen("unix", shortSocketPath(t))
	require.NoError(t, err)

	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Shutdown(context.Background())
		_ = ln.Close()
	})

	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", ln.Addr().String())
			},
		},
	}
	return leapmuxv1connect.NewAuthServiceClient(httpClient, "http://unix")
}

func TestLocalSocket_MultiUser_RejectsUnauthenticated(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	hubtestutil.CreateTestAdmin(t, st)

	interceptor, _ := auth.NewInterceptor(st, nil, false, false)
	client := newUnixSocketAuthClient(t, st, interceptor)

	// No cookie, no bearer — multi-user hub on a unix socket must reject.
	_, err := client.GetCurrentUser(context.Background(), connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestLocalSocket_SoloMode_AutoAuths(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	require.NoError(t, bootstrap.Run(context.Background(), st, true))
	soloUser, err := auth.LoadSoloUser(context.Background(), st)
	require.NoError(t, err)

	interceptor, _ := auth.NewInterceptor(st, soloUser, false, false)
	client := newUnixSocketAuthClient(t, st, interceptor)

	resp, err := client.GetCurrentUser(context.Background(), connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{}))
	require.NoError(t, err)
	assert.Equal(t, "solo", resp.Msg.GetUser().GetUsername())
	assert.True(t, resp.Msg.GetUser().GetIsAdmin())
}

func TestLocalSocket_MultiUser_AcceptsBearer(t *testing.T) {
	// The plan's design: multi-user hub on a unix socket accepts a
	// freshly-issued lmx_* bearer the same way it would over TCP. This
	// is what makes "headless service accounts on a multi-user hub"
	// work — admin issues a token, CLI presents it.
	st := hubtestutil.OpenTestStore(t)
	hubtestutil.CreateTestAdmin(t, st)

	pepper := []byte("0123456789abcdef0123456789abcdef")
	tv, err := auth.NewTokenValidator(st, pepper)
	require.NoError(t, err)

	interceptor, _ := auth.NewInterceptorWithTokens(st, nil, tv, false, false)
	client := newUnixSocketAuthClient(t, st, interceptor)

	u, err := st.Users().GetByUsername(context.Background(), "admin")
	require.NoError(t, err)
	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:         tokenID,
		UserID:     u.ID,
		ClientType: "cli",
		ClientName: "test",
		SecretHash: tv.HashSecret(secret),
		Scope:      "remote:*",
	}))

	req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req.Header().Set("Authorization", "Bearer "+auth.FormatBearer(auth.BearerKindAPI, tokenID, secret))
	resp, err := client.GetCurrentUser(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "admin", resp.Msg.GetUser().GetUsername())
}

func TestLocalSocket_MultiUser_RejectsRevokedBearer(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	hubtestutil.CreateTestAdmin(t, st)

	pepper := []byte("0123456789abcdef0123456789abcdef")
	tv, err := auth.NewTokenValidator(st, pepper)
	require.NoError(t, err)

	interceptor, _ := auth.NewInterceptorWithTokens(st, nil, tv, false, false)
	client := newUnixSocketAuthClient(t, st, interceptor)

	u, err := st.Users().GetByUsername(context.Background(), "admin")
	require.NoError(t, err)
	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID: tokenID, UserID: u.ID, ClientType: "cli", ClientName: "test",
		SecretHash: tv.HashSecret(secret), Scope: "remote:*",
	}))
	_, err = st.APITokens().Revoke(context.Background(), tokenID)
	require.NoError(t, err)

	req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req.Header().Set("Authorization", "Bearer "+auth.FormatBearer(auth.BearerKindAPI, tokenID, secret))
	_, err = client.GetCurrentUser(context.Background(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}
