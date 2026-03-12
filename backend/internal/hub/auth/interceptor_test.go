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
	"github.com/leapmux/leapmux/internal/hub/service"
)

// setupInterceptorTestServer creates an httptest server with the AuthService
// registered behind the auth interceptor. It returns a ConnectRPC client and
// the bootstrapped admin credentials (username "admin", password "admin").
func setupInterceptorTestServer(t *testing.T) leapmuxv1connect.AuthServiceClient {
	t.Helper()

	q := setupDB(t)

	// Bootstrap creates an admin user (admin/admin).
	err := bootstrap.Run(context.Background(), q, false)
	require.NoError(t, err)

	mux := http.NewServeMux()
	interceptors := connect.WithInterceptors(auth.NewInterceptor(q, false))
	authSvc := service.NewAuthService(q, &config.Config{})
	path, handler := leapmuxv1connect.NewAuthServiceHandler(authSvc, interceptors)
	mux.Handle(path, handler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL)
}

// loginAdmin logs in with the bootstrapped admin credentials and returns the
// session token.
func loginAdmin(t *testing.T, client leapmuxv1connect.AuthServiceClient) string {
	t.Helper()

	resp, err := client.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
		Username: "admin",
		Password: "admin",
	}))
	require.NoError(t, err)
	return resp.Msg.GetToken()
}

func TestInterceptor_PublicProcedure_NoTokenRequired(t *testing.T) {
	client := setupInterceptorTestServer(t)

	// GetSystemInfo is a public procedure -- it should succeed without a token.
	resp, err := client.GetSystemInfo(context.Background(), connect.NewRequest(&leapmuxv1.GetSystemInfoRequest{}))
	require.NoError(t, err)
	assert.NotNil(t, resp.Msg)
}

func TestInterceptor_PrivateProcedure_NoToken(t *testing.T) {
	client := setupInterceptorTestServer(t)

	// GetCurrentUser is a private procedure. Calling it without a Bearer token
	// should produce an Unauthenticated error.
	_, err := client.GetCurrentUser(context.Background(), connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestInterceptor_PrivateProcedure_ValidToken(t *testing.T) {
	client := setupInterceptorTestServer(t)

	token := loginAdmin(t, client)

	// Use the valid token to call a private endpoint.
	req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req.Header().Set("Authorization", "Bearer "+token)

	resp, err := client.GetCurrentUser(context.Background(), req)
	require.NoError(t, err)

	// The interceptor should have attached UserInfo to the context, allowing
	// GetCurrentUser to return the admin user.
	assert.Equal(t, "admin", resp.Msg.GetUser().GetUsername())
	assert.True(t, resp.Msg.GetUser().GetIsAdmin())
}

func TestInterceptor_SoloMode_AutoAuthenticated(t *testing.T) {
	q := setupDB(t)

	// Bootstrap in solo mode creates a user named "solo".
	err := bootstrap.Run(context.Background(), q, true)
	require.NoError(t, err)

	mux := http.NewServeMux()
	interceptors := connect.WithInterceptors(auth.NewInterceptor(q, true))
	authSvc := service.NewAuthService(q, &config.Config{SoloMode: true})
	path, handler := leapmuxv1connect.NewAuthServiceHandler(authSvc, interceptors)
	mux.Handle(path, handler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL)

	// In solo mode, private endpoints should work without any token.
	resp, err := client.GetCurrentUser(context.Background(), connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{}))
	require.NoError(t, err)
	assert.Equal(t, "solo", resp.Msg.GetUser().GetUsername())
	assert.True(t, resp.Msg.GetUser().GetIsAdmin())
}

func TestInterceptor_PrivateProcedure_InvalidToken(t *testing.T) {
	client := setupInterceptorTestServer(t)

	// Use a garbage token on a private endpoint.
	req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req.Header().Set("Authorization", "Bearer totally-invalid-token")

	_, err := client.GetCurrentUser(context.Background(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}
