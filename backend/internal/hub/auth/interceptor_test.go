package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/bootstrap"
	"github.com/leapmux/leapmux/internal/hub/config"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/service"
)

// setupInterceptorTestServer creates an httptest server with the AuthService
// registered behind the auth interceptor. It returns a ConnectRPC client and
// the bootstrapped admin credentials (username "admin", password "admin").
func setupInterceptorTestServer(t *testing.T) leapmuxv1connect.AuthServiceClient {
	t.Helper()

	sqlDB, q := setupDB(t)

	// Bootstrap creates an admin user (admin/admin).
	err := bootstrap.Run(context.Background(), sqlDB, q, false)
	require.NoError(t, err)

	mux := http.NewServeMux()
	interceptor, _ := auth.NewInterceptor(q, false, false, false)
	interceptors := connect.WithInterceptors(interceptor)
	authSvc := service.NewAuthService(sqlDB, q, &config.Config{}, nil, nil)
	path, handler := leapmuxv1connect.NewAuthServiceHandler(authSvc, interceptors)
	mux.Handle(path, handler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL)
}

// loginAdmin logs in with the bootstrapped admin credentials and returns the
// session token extracted from the Set-Cookie response header.
func loginAdmin(t *testing.T, client leapmuxv1connect.AuthServiceClient) string {
	t.Helper()

	resp, err := client.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
		Username: "admin",
		Password: "admin",
	}))
	require.NoError(t, err)
	return extractSessionFromCookie(t, resp.Header().Get("Set-Cookie"))
}

// extractSessionFromCookie parses the session ID from a Set-Cookie header value.
func extractSessionFromCookie(t *testing.T, setCookie string) string {
	t.Helper()
	require.NotEmpty(t, setCookie, "Set-Cookie header must be present")
	// Parse "leapmux-session=<value>; ..." format.
	for _, part := range strings.Split(setCookie, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, auth.CookieName+"=") {
			return strings.TrimPrefix(part, auth.CookieName+"=")
		}
	}
	t.Fatalf("session cookie %q not found in Set-Cookie header: %s", auth.CookieName, setCookie)
	return ""
}

func TestInterceptor_PublicProcedure_NoTokenRequired(t *testing.T) {
	client := setupInterceptorTestServer(t)

	// GetSystemInfo is a public procedure -- it should succeed without a cookie.
	resp, err := client.GetSystemInfo(context.Background(), connect.NewRequest(&leapmuxv1.GetSystemInfoRequest{}))
	require.NoError(t, err)
	assert.NotNil(t, resp.Msg)
}

func TestInterceptor_PrivateProcedure_NoCookie(t *testing.T) {
	client := setupInterceptorTestServer(t)

	// GetCurrentUser is a private procedure. Calling it without a session cookie
	// should produce an Unauthenticated error.
	_, err := client.GetCurrentUser(context.Background(), connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestInterceptor_PrivateProcedure_ValidCookie(t *testing.T) {
	client := setupInterceptorTestServer(t)

	token := loginAdmin(t, client)

	// Use the valid session ID in a cookie to call a private endpoint.
	req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req.Header().Set("Cookie", auth.CookieName+"="+token)

	resp, err := client.GetCurrentUser(context.Background(), req)
	require.NoError(t, err)

	// The interceptor should have attached UserInfo to the context, allowing
	// GetCurrentUser to return the admin user.
	assert.Equal(t, "admin", resp.Msg.GetUser().GetUsername())
	assert.True(t, resp.Msg.GetUser().GetIsAdmin())
}

func TestInterceptor_SoloMode_AutoAuthenticated(t *testing.T) {
	sqlDB, q := setupDB(t)

	// Bootstrap in solo mode creates a user named "solo".
	err := bootstrap.Run(context.Background(), sqlDB, q, true)
	require.NoError(t, err)

	mux := http.NewServeMux()
	interceptor, _ := auth.NewInterceptor(q, true, false, false)
	interceptors := connect.WithInterceptors(interceptor)
	authSvc := service.NewAuthService(sqlDB, q, &config.Config{SoloMode: true}, nil, nil)
	path, handler := leapmuxv1connect.NewAuthServiceHandler(authSvc, interceptors)
	mux.Handle(path, handler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL)

	// In solo mode, private endpoints should work without any cookie.
	resp, err := client.GetCurrentUser(context.Background(), connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{}))
	require.NoError(t, err)
	assert.Equal(t, "solo", resp.Msg.GetUser().GetUsername())
	assert.True(t, resp.Msg.GetUser().GetIsAdmin())
}

func TestInterceptor_PrivateProcedure_InvalidCookie(t *testing.T) {
	client := setupInterceptorTestServer(t)

	// Use a garbage session ID in a cookie on a private endpoint.
	req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req.Header().Set("Cookie", auth.CookieName+"=totally-invalid-token")

	_, err := client.GetCurrentUser(context.Background(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestInterceptor_BearerTokenNotAccepted(t *testing.T) {
	client := setupInterceptorTestServer(t)

	token := loginAdmin(t, client)

	// Try using Bearer token in Authorization header — should NOT be accepted.
	req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req.Header().Set("Authorization", "Bearer "+token)

	_, err := client.GetCurrentUser(context.Background(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// setupInterceptorTestServerWithCache is like setupInterceptorTestServer but
// wires the SessionCache into the AuthService (so Logout evicts entries) and
// returns the queries handle for DB inspection.
func setupInterceptorTestServerWithCache(t *testing.T) (leapmuxv1connect.AuthServiceClient, *gendb.Queries) {
	t.Helper()

	sqlDB, q := setupDB(t)

	err := bootstrap.Run(context.Background(), sqlDB, q, false)
	require.NoError(t, err)

	mux := http.NewServeMux()
	interceptor, sc := auth.NewInterceptor(q, false, false, false)
	t.Cleanup(sc.Stop)
	interceptors := connect.WithInterceptors(interceptor)
	authSvc := service.NewAuthService(sqlDB, q, &config.Config{}, sc, nil)
	path, handler := leapmuxv1connect.NewAuthServiceHandler(authSvc, interceptors)
	mux.Handle(path, handler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL), q
}

func TestTouchSession_ThrottledWithinThreshold(t *testing.T) {
	client, q := setupInterceptorTestServerWithCache(t)

	token := loginAdmin(t, client)

	// First authenticated request — triggers touchSession and updates last_active_at.
	req1 := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req1.Header().Set("Cookie", auth.CookieName+"="+token)
	_, err := client.GetCurrentUser(context.Background(), req1)
	require.NoError(t, err)

	sess1, err := q.GetUserSessionByID(context.Background(), token)
	require.NoError(t, err)
	t1 := sess1.LastActiveAt

	// Second authenticated request immediately — should be throttled (no DB write).
	req2 := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req2.Header().Set("Cookie", auth.CookieName+"="+token)
	_, err = client.GetCurrentUser(context.Background(), req2)
	require.NoError(t, err)

	sess2, err := q.GetUserSessionByID(context.Background(), token)
	require.NoError(t, err)
	t2 := sess2.LastActiveAt

	assert.Equal(t, t1, t2, "last_active_at should not change on rapid successive requests (throttled)")
}

func TestLogout_EvictsSessionFromCache(t *testing.T) {
	client, _ := setupInterceptorTestServerWithCache(t)

	token := loginAdmin(t, client)

	// Verify the session is valid.
	req1 := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req1.Header().Set("Cookie", auth.CookieName+"="+token)
	resp, err := client.GetCurrentUser(context.Background(), req1)
	require.NoError(t, err)
	assert.Equal(t, "admin", resp.Msg.GetUser().GetUsername())

	// Logout — deletes session from DB and evicts from cache.
	logoutReq := connect.NewRequest(&leapmuxv1.LogoutRequest{})
	logoutReq.Header().Set("Cookie", auth.CookieName+"="+token)
	_, err = client.Logout(context.Background(), logoutReq)
	require.NoError(t, err)

	// Using the same token should now fail with Unauthenticated.
	req2 := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req2.Header().Set("Cookie", auth.CookieName+"="+token)
	_, err = client.GetCurrentUser(context.Background(), req2)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestSessionCache_RapidRequestsSucceed(t *testing.T) {
	client, _ := setupInterceptorTestServerWithCache(t)

	token := loginAdmin(t, client)

	// Issue multiple rapid requests — the session cache should serve
	// the cached UserInfo without repeated DB queries.
	for i := 0; i < 5; i++ {
		req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
		req.Header().Set("Cookie", auth.CookieName+"="+token)
		resp, err := client.GetCurrentUser(context.Background(), req)
		require.NoError(t, err)
		assert.Equal(t, "admin", resp.Msg.GetUser().GetUsername())
	}
}

func TestSessionCache_EvictInvalidatesCache(t *testing.T) {
	// This test verifies that logging out (which evicts from cache) immediately
	// invalidates the session, even if it was recently cached.
	client, _ := setupInterceptorTestServerWithCache(t)

	token := loginAdmin(t, client)

	// Warm the session cache.
	req := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req.Header().Set("Cookie", auth.CookieName+"="+token)
	_, err := client.GetCurrentUser(context.Background(), req)
	require.NoError(t, err)

	// Logout evicts from cache.
	logoutReq := connect.NewRequest(&leapmuxv1.LogoutRequest{})
	logoutReq.Header().Set("Cookie", auth.CookieName+"="+token)
	_, err = client.Logout(context.Background(), logoutReq)
	require.NoError(t, err)

	// The cached session must be gone — request should fail immediately.
	req2 := connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{})
	req2.Header().Set("Cookie", auth.CookieName+"="+token)
	_, err = client.GetCurrentUser(context.Background(), req2)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}
