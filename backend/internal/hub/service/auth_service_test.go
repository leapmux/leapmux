package service_test

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
	"github.com/leapmux/leapmux/internal/hub/db"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/service"
)

func setupAuthTestServer(t *testing.T, cfg *config.Config) (leapmuxv1connect.AuthServiceClient, *gendb.Queries) {
	t.Helper()

	sqlDB, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	err = db.Migrate(sqlDB)
	require.NoError(t, err)

	q := gendb.New(sqlDB)

	err = bootstrap.Run(context.Background(), sqlDB, q, false)
	require.NoError(t, err)

	mux := http.NewServeMux()
	interceptor, _ := auth.NewInterceptor(q, false, false, false)
	opts := connect.WithInterceptors(interceptor)
	authSvc := service.NewAuthService(sqlDB, q, cfg, nil, nil)
	path, handler := leapmuxv1connect.NewAuthServiceHandler(authSvc, opts)
	mux.Handle(path, handler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL)
	return client, q
}

func TestAuthService_LoginSuccess(t *testing.T) {
	client, _ := setupAuthTestServer(t, testConfig())

	resp, err := client.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
		Username: "admin",
		Password: "admin",
	}))
	require.NoError(t, err)

	assert.Equal(t, "admin", resp.Msg.GetUser().GetUsername())
	assert.True(t, resp.Msg.GetUser().GetIsAdmin())

	// Verify Set-Cookie header is present with session cookie.
	setCookie := resp.Header().Get("Set-Cookie")
	assert.NotEmpty(t, setCookie)
	assert.Contains(t, setCookie, auth.CookieName+"=")
	assert.Contains(t, setCookie, "HttpOnly")
}

func TestAuthService_LoginInvalidPassword(t *testing.T) {
	client, _ := setupAuthTestServer(t, testConfig())

	_, err := client.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
		Username: "admin",
		Password: "wrong",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestAuthService_GetCurrentUser(t *testing.T) {
	client, _ := setupAuthTestServer(t, testConfig())

	// Login first.
	loginResp, err := client.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
		Username: "admin",
		Password: "admin",
	}))
	require.NoError(t, err)

	// Get current user with token.
	req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req.Header().Set("Cookie", auth.CookieName+"="+sessionFromCookie(t, loginResp.Header().Get("Set-Cookie")))

	resp, err := client.GetCurrentUser(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "admin", resp.Msg.GetUser().GetUsername())
}

func TestAuthService_GetCurrentUser_NoToken(t *testing.T) {
	client, _ := setupAuthTestServer(t, testConfig())

	_, err := client.GetCurrentUser(context.Background(), connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestAuthService_Login_EmptyUsername(t *testing.T) {
	client, _ := setupAuthTestServer(t, testConfig())

	_, err := client.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
		Username: "",
		Password: "admin",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestAuthService_Login_EmptyPassword(t *testing.T) {
	client, _ := setupAuthTestServer(t, testConfig())

	_, err := client.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
		Username: "admin",
		Password: "",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestAuthService_SignUp_WhenEnabled(t *testing.T) {
	client, _ := setupAuthTestServer(t, testConfigWithSignup())

	resp, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "newuser",
		Password:    "newpass123",
		DisplayName: "New User",
		Email:       "new@example.com",
	}))
	require.NoError(t, err)
	// Token assertion replaced by Set-Cookie check above
	assert.Equal(t, "newuser", resp.Msg.GetUser().GetUsername())
	assert.Equal(t, "New User", resp.Msg.GetUser().GetDisplayName())

	// Verify Set-Cookie header is present.
	setCookie := resp.Header().Get("Set-Cookie")
	assert.Contains(t, setCookie, auth.CookieName+"=")
	assert.Contains(t, setCookie, "HttpOnly")
}

func TestAuthService_SignUp_WhenDisabled(t *testing.T) {
	client, _ := setupAuthTestServer(t, testConfig())

	// Signup is disabled by default.
	_, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username: "newuser",
		Password: "newpass123",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestAuthService_SignUp_DuplicateUsername(t *testing.T) {
	client, _ := setupAuthTestServer(t, testConfigWithSignup())

	// First signup should succeed.
	_, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username: "dupuser",
		Password: "pass123",
	}))
	require.NoError(t, err)

	// Second signup with the same username should fail.
	_, err = client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username: "dupuser",
		Password: "pass456",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
}

func TestAuthService_ChangePassword_WrongOldPassword(t *testing.T) {
	client, q := setupAuthTestServer(t, testConfig())

	// Login to get a token.
	loginResp, err := client.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
		Username: "admin",
		Password: "admin",
	}))
	require.NoError(t, err)
	token := sessionFromCookie(t, loginResp.Header().Get("Set-Cookie"))

	// Set up a UserService client using the same queries and auth interceptor.
	mux := http.NewServeMux()
	interceptor, _ := auth.NewInterceptor(q, false, false, false)
	opts := connect.WithInterceptors(interceptor)
	userSvc := service.NewUserService(q, testConfig())
	path, handler := leapmuxv1connect.NewUserServiceHandler(userSvc, opts)
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	userClient := leapmuxv1connect.NewUserServiceClient(server.Client(), server.URL)

	// Try to change password with wrong old password.
	req := connect.NewRequest(&leapmuxv1.ChangePasswordRequest{
		CurrentPassword: "wrongpassword",
		NewPassword:     "newpass123",
	})
	req.Header().Set("Cookie", auth.CookieName+"="+token)
	_, err = userClient.ChangePassword(context.Background(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestAuthService_Logout(t *testing.T) {
	client, _ := setupAuthTestServer(t, testConfig())

	// Login.
	loginResp, err := client.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
		Username: "admin",
		Password: "admin",
	}))
	require.NoError(t, err)

	token := sessionFromCookie(t, loginResp.Header().Get("Set-Cookie"))

	// Logout.
	logoutReq := connect.NewRequest(&leapmuxv1.LogoutRequest{})
	logoutReq.Header().Set("Cookie", auth.CookieName+"="+token)
	logoutResp, err := client.Logout(context.Background(), logoutReq)
	require.NoError(t, err)

	// Verify logout response clears the cookie.
	logoutCookie := logoutResp.Header().Get("Set-Cookie")
	assert.Contains(t, logoutCookie, auth.CookieName+"=")
	assert.Contains(t, logoutCookie, "Max-Age=0")

	// Token should be invalidated.
	getUserReq := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	getUserReq.Header().Set("Cookie", auth.CookieName+"="+token)
	_, err = client.GetCurrentUser(context.Background(), getUserReq)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}
