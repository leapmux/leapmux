package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/mail"

	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/verifycode"
)

func setupAuthTestServerBase(t *testing.T, cfg *config.Config) (leapmuxv1connect.AuthServiceClient, store.Store) {
	t.Helper()

	st := hubtestutil.OpenTestStore(t)

	mux := http.NewServeMux()
	interceptor, sc := auth.NewInterceptor(st, nil, false, false)
	t.Cleanup(sc.Stop)
	opts := connect.WithInterceptors(interceptor)
	authSvc := service.NewAuthService(st, cfg, sc, nil, mail.NewStubSender())
	path, handler := leapmuxv1connect.NewAuthServiceHandler(authSvc, opts)
	mux.Handle(path, handler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL)
	return client, st
}

// setupEmptyAuthTestServer creates a test auth server with an empty database
// (no users). Used for testing the initial setup flow.
func setupEmptyAuthTestServer(t *testing.T, cfg *config.Config) (leapmuxv1connect.AuthServiceClient, store.Store) {
	return setupAuthTestServerBase(t, cfg)
}

func setupAuthTestServer(t *testing.T, cfg *config.Config) (leapmuxv1connect.AuthServiceClient, store.Store) {
	client, st := setupAuthTestServerBase(t, cfg)
	hubtestutil.CreateTestAdmin(t, st)
	return client, st
}

func TestAuthService_LoginSuccess(t *testing.T) {
	client, _ := setupAuthTestServer(t, testConfig())

	resp, err := client.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
		Username: "admin",
		Password: "admin123",
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
		Password: "wrongpwd",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestAuthService_GetCurrentUser(t *testing.T) {
	client, _ := setupAuthTestServer(t, testConfig())

	// Login first.
	loginResp, err := client.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
		Username: "admin",
		Password: "admin123",
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
		Password: "admin123",
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
		Password: "pass1234",
	}))
	require.NoError(t, err)

	// Second signup with the same username should fail.
	_, err = client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username: "dupuser",
		Password: "pass4567",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
}

func TestAuthService_ChangePassword_WrongOldPassword(t *testing.T) {
	client, st := setupAuthTestServer(t, testConfig())

	// Login to get a token.
	loginResp, err := client.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
		Username: "admin",
		Password: "admin123",
	}))
	require.NoError(t, err)
	token := sessionFromCookie(t, loginResp.Header().Get("Set-Cookie"))

	// Set up a UserService client using the same queries and auth interceptor.
	mux := http.NewServeMux()
	interceptor, _ := auth.NewInterceptor(st, nil, false, false)
	opts := connect.WithInterceptors(interceptor)
	userSvc := service.NewUserService(st, testConfig(), nil, mail.NewStubSender())
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

func TestSignUp_DuplicateEmail_Rejected(t *testing.T) {
	client, st := setupAuthTestServer(t, testConfigWithSignup())

	// Create a user with that email directly in the DB.
	orgID := id.Generate()
	err := st.Orgs().Create(context.Background(), store.CreateOrgParams{ID: orgID, Name: "emailuser"})
	require.NoError(t, err)
	hash, err := password.Hash("testpass")
	require.NoError(t, err)
	err = st.Users().Create(context.Background(), store.CreateUserParams{
		ID:           id.Generate(),
		OrgID:        orgID,
		Username:     "emailuser",
		PasswordHash: hash,
		DisplayName:  "Email User",
		Email:        "taken@example.com",
		PasswordSet:  true,
		IsAdmin:      false,
	})
	require.NoError(t, err)

	// Try to sign up with the same email.
	_, err = client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "newuser",
		Password:    "newpass123",
		DisplayName: "New User",
		Email:       "taken@example.com",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
}

func TestPromotePendingEmail_ClearsCompetingPendingEmails(t *testing.T) {
	_, st := setupAuthTestServer(t, testConfigWithSignup())
	ctx := context.Background()

	// Create two users, both with pending_email = "shared@example.com".
	for _, username := range []string{"user-a", "user-b"} {
		orgID := id.Generate()
		err := st.Orgs().Create(ctx, store.CreateOrgParams{ID: orgID, Name: username + "-org"})
		require.NoError(t, err)
		hash, err := password.Hash("testpass")
		require.NoError(t, err)
		userID := id.Generate()
		err = st.Users().Create(ctx, store.CreateUserParams{
			ID:           userID,
			OrgID:        orgID,
			Username:     username,
			PasswordHash: hash,
			DisplayName:  username,
			Email:        "",
			PasswordSet:  true,
			IsAdmin:      false,
		})
		require.NoError(t, err)
		err = st.Users().SetPendingEmail(ctx, store.SetPendingEmailParams{
			PendingEmail:          "shared@example.com",
			PendingEmailToken:     verifycode.Generate(),
			PendingEmailExpiresAt: ptrTime(time.Now().Add(24 * time.Hour).UTC()),
			ID:                    userID,
		})
		require.NoError(t, err)
	}

	// Verify both have pending_email set.
	userA, err := st.Users().GetByUsername(ctx, "user-a")
	require.NoError(t, err)
	assert.Equal(t, "shared@example.com", userA.PendingEmail)
	userB, err := st.Users().GetByUsername(ctx, "user-b")
	require.NoError(t, err)
	assert.Equal(t, "shared@example.com", userB.PendingEmail)

	// User A promotes — this should also clear user B's pending_email.
	err = st.Users().PromotePendingEmail(ctx, userA.ID)
	require.NoError(t, err)
	err = st.Users().ClearCompetingPendingEmails(ctx, store.ClearCompetingPendingEmailsParams{
		PendingEmail: "shared@example.com",
		ExcludeID:    userA.ID,
	})
	require.NoError(t, err)

	// User A now has verified email.
	userA, err = st.Users().GetByUsername(ctx, "user-a")
	require.NoError(t, err)
	assert.Equal(t, "shared@example.com", userA.Email)
	assert.True(t, userA.EmailVerified)
	assert.Empty(t, userA.PendingEmail)

	// User B's pending_email should be cleared.
	userB, err = st.Users().GetByUsername(ctx, "user-b")
	require.NoError(t, err)
	assert.Empty(t, userB.PendingEmail)
	assert.Empty(t, userB.Email)
}

func TestSignUp_DirectEmail_ClearsCompetingPendingEmails(t *testing.T) {
	client, st := setupAuthTestServer(t, testConfigWithSignup())
	ctx := context.Background()

	// User A sets pending_email = "race@example.com" (unverified).
	orgID := id.Generate()
	err := st.Orgs().Create(ctx, store.CreateOrgParams{ID: orgID, Name: "racer-org"})
	require.NoError(t, err)
	hash, err := password.Hash("testpass")
	require.NoError(t, err)
	userAID := id.Generate()
	err = st.Users().Create(ctx, store.CreateUserParams{
		ID:           userAID,
		OrgID:        orgID,
		Username:     "racer",
		PasswordHash: hash,
		DisplayName:  "Racer",
		Email:        "",
		PasswordSet:  true,
		IsAdmin:      false,
	})
	require.NoError(t, err)
	err = st.Users().SetPendingEmail(ctx, store.SetPendingEmailParams{
		PendingEmail:          "race@example.com",
		PendingEmailToken:     verifycode.Generate(),
		PendingEmailExpiresAt: ptrTime(time.Now().Add(24 * time.Hour).UTC()),
		ID:                    userAID,
	})
	require.NoError(t, err)

	// User B signs up with email = "race@example.com" directly (verification off).
	_, err = client.SignUp(ctx, connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "winner",
		Password:    "pass1234",
		DisplayName: "Winner",
		Email:       "race@example.com",
	}))
	require.NoError(t, err)

	// User A's pending_email should be cleared.
	userA, err := st.Users().GetByUsername(ctx, "racer")
	require.NoError(t, err)
	assert.Empty(t, userA.PendingEmail, "competing pending_email should be cleared when another user claims the email directly")
}

func TestSignUp_EmptyEmail_AllowedMultiple(t *testing.T) {
	client, _ := setupAuthTestServer(t, testConfigWithSignup())

	// First signup with empty email should succeed.
	resp1, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "emptyemail1",
		Password:    "pass1234",
		DisplayName: "User 1",
		Email:       "",
	}))
	require.NoError(t, err)
	assert.Equal(t, "emptyemail1", resp1.Msg.GetUser().GetUsername())

	// Second signup with empty email should also succeed.
	resp2, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "emptyemail2",
		Password:    "pass4567",
		DisplayName: "User 2",
		Email:       "",
	}))
	require.NoError(t, err)
	assert.Equal(t, "emptyemail2", resp2.Msg.GetUser().GetUsername())
}

// setupVerificationGatingTestServer creates a test server with both
// UserService and AuthService, with emailVerificationRequired set as specified.
func setupVerificationGatingTestServer(t *testing.T, emailVerificationRequired bool) (
	leapmuxv1connect.UserServiceClient,
	leapmuxv1connect.AuthServiceClient,
	store.Store,
) {
	t.Helper()

	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	err = st.Migrator().Migrate(context.Background())
	require.NoError(t, err)

	hubtestutil.CreateTestAdmin(t, st)

	mux := http.NewServeMux()
	interceptor, _ := auth.NewInterceptor(st, nil, false, emailVerificationRequired)
	opts := connect.WithInterceptors(interceptor)

	cfg := testConfig()
	cfg.SignupEnabled = true
	cfg.EmailVerificationRequired = emailVerificationRequired

	userSvc := service.NewUserService(st, cfg, nil, mail.NewStubSender())
	userPath, userHandler := leapmuxv1connect.NewUserServiceHandler(userSvc, opts)
	mux.Handle(userPath, userHandler)

	authSvc := service.NewAuthService(st, cfg, nil, nil, mail.NewStubSender())
	authPath, authHandler := leapmuxv1connect.NewAuthServiceHandler(authSvc, opts)
	mux.Handle(authPath, authHandler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	userClient := leapmuxv1connect.NewUserServiceClient(server.Client(), server.URL)
	authClient := leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL)
	return userClient, authClient, st
}

func TestVerificationGating_UnverifiedBlocked(t *testing.T) {
	userClient, _, st := setupVerificationGatingTestServer(t, true)

	// Create a user with email_verified=0 directly via DB.
	orgID := id.Generate()
	userID := id.Generate()
	hash, _ := password.Hash("testpass")
	_ = st.Orgs().Create(context.Background(), store.CreateOrgParams{ID: orgID, Name: "unverified"})
	_ = st.Users().Create(context.Background(), store.CreateUserParams{
		ID:           userID,
		OrgID:        orgID,
		Username:     "unverified",
		PasswordHash: hash,
		DisplayName:  "Unverified User",
		Email:        "unverified@example.com",
		PasswordSet:  true,
		IsAdmin:      false,
	})
	// email_verified defaults to 0 in the DB.

	token, _, _, err := auth.Login(context.Background(), st, "unverified", "testpass")
	require.NoError(t, err)

	// Try UpdateProfile — should be blocked by verification gating.
	_, err = userClient.UpdateProfile(context.Background(), authedReq(&leapmuxv1.UpdateProfileRequest{
		Username:    "unverified",
		DisplayName: "Updated",
	}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

func TestVerificationGating_AdminExempt(t *testing.T) {
	userClient, _, st := setupVerificationGatingTestServer(t, true)

	// The bootstrap admin has email_verified=0 by default (no email set).
	// Verify the admin can still call protected RPCs.
	adminToken, _, _, err := auth.Login(context.Background(), st, "admin", "admin123")
	require.NoError(t, err)

	// Admin should be able to call UpdateProfile even with email_verified=0.
	_, err = userClient.UpdateProfile(context.Background(), authedReq(&leapmuxv1.UpdateProfileRequest{
		Username:    "admin",
		DisplayName: "Admin Updated",
	}, adminToken))
	require.NoError(t, err)
}

func TestVerificationGating_ConfigOff_NotBlocked(t *testing.T) {
	userClient, _, st := setupVerificationGatingTestServer(t, false)

	// Create an unverified user.
	orgID := id.Generate()
	userID := id.Generate()
	hash, _ := password.Hash("testpass")
	_ = st.Orgs().Create(context.Background(), store.CreateOrgParams{ID: orgID, Name: "nogate"})
	_ = st.Users().Create(context.Background(), store.CreateUserParams{
		ID:           userID,
		OrgID:        orgID,
		Username:     "nogate",
		PasswordHash: hash,
		DisplayName:  "No Gate User",
		Email:        "nogate@example.com",
		PasswordSet:  true,
		IsAdmin:      false,
	})
	// email_verified defaults to 0 — but gating is OFF.

	token, _, _, err := auth.Login(context.Background(), st, "nogate", "testpass")
	require.NoError(t, err)

	// Unverified user should be able to call UpdateProfile when gating is off.
	_, err = userClient.UpdateProfile(context.Background(), authedReq(&leapmuxv1.UpdateProfileRequest{
		Username:    "nogate",
		DisplayName: "Updated",
	}, token))
	require.NoError(t, err)
}

// --- SignUp with EmailVerificationRequired ---

func TestSignUp_VerificationRequired_EmailInPendingColumn(t *testing.T) {
	cfg := testConfigWithSignup()
	cfg.EmailVerificationRequired = true

	client, st := setupAuthTestServer(t, cfg)

	resp, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "verifyuser",
		Password:    "password123",
		DisplayName: "Verify User",
		Email:       "verify@example.com",
	}))
	require.NoError(t, err)

	// The response should indicate verification was required and that
	// the (stub) email send succeeded.
	assert.True(t, resp.Msg.GetVerificationRequired())
	assert.True(t, resp.Msg.GetVerificationEmailSent())
	assert.Equal(t, "verifyuser", resp.Msg.GetUser().GetUsername())

	// Email stays in the pending column until the user submits the code.
	// No more stub auto-verify: a real (stub-logged) email was dispatched
	// and verification waits on UserService.VerifyEmail.
	user, err := st.Users().GetByUsername(context.Background(), "verifyuser")
	require.NoError(t, err)
	assert.Empty(t, user.Email)
	assert.False(t, user.EmailVerified)
	assert.Equal(t, "verify@example.com", user.PendingEmail)
	assert.NotEmpty(t, user.PendingEmailToken)

	// Signup must issue a session even when verification is required —
	// otherwise the user can't authenticate to the verify endpoint.
	assert.Contains(t, resp.Header().Get("Set-Cookie"), auth.CookieName+"=")
}

// --- VerifyEmail tests ---

func setupAuthTestServerWithKeystore(t *testing.T, cfg *config.Config) (leapmuxv1connect.AuthServiceClient, store.Store) {
	t.Helper()

	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	err = st.Migrator().Migrate(context.Background())
	require.NoError(t, err)

	hubtestutil.CreateTestAdmin(t, st)

	mux := http.NewServeMux()
	interceptor, _ := auth.NewInterceptor(st, nil, false, false)
	opts := connect.WithInterceptors(interceptor)
	authSvc := service.NewAuthService(st, cfg, nil, nil, mail.NewStubSender())
	path, handler := leapmuxv1connect.NewAuthServiceHandler(authSvc, opts)
	mux.Handle(path, handler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL)
	return client, st
}

func createTestUserWithPendingEmail(t *testing.T, st store.Store, username, pendingEmail, token string, expiresAt *time.Time) string {
	t.Helper()
	orgID := id.Generate()
	userID := id.Generate()
	hash, err := password.Hash("testpass")
	require.NoError(t, err)

	err = st.Orgs().Create(context.Background(), store.CreateOrgParams{ID: orgID, Name: username})
	require.NoError(t, err)
	err = st.Users().Create(context.Background(), store.CreateUserParams{
		ID:           userID,
		OrgID:        orgID,
		Username:     username,
		PasswordHash: hash,
		DisplayName:  username,
		Email:        "",
		PasswordSet:  true,
		IsAdmin:      false,
	})
	require.NoError(t, err)

	// Set pending email directly.
	err = st.Users().SetPendingEmail(context.Background(), store.SetPendingEmailParams{
		PendingEmail:          pendingEmail,
		PendingEmailToken:     token,
		PendingEmailExpiresAt: expiresAt,
		ID:                    userID,
	})
	require.NoError(t, err)

	return userID
}

// VerifyEmail moved from AuthService to UserService and is now
// authenticated. The exhaustive coverage lives in user_service_test.go;
// keeping this file slim avoids accidental drift between two suites
// testing the same handler.

// --- Verification gating: allowed methods ---

func TestVerificationGating_LogoutAllowed(t *testing.T) {
	_, authClient, st := setupVerificationGatingTestServer(t, true)

	// Create an unverified user.
	orgID := id.Generate()
	userID := id.Generate()
	hash, _ := password.Hash("testpass")
	_ = st.Orgs().Create(context.Background(), store.CreateOrgParams{ID: orgID, Name: "logoutgating"})
	_ = st.Users().Create(context.Background(), store.CreateUserParams{
		ID:           userID,
		OrgID:        orgID,
		Username:     "logoutgating",
		PasswordHash: hash,
		DisplayName:  "Logout Gating",
		Email:        "logoutgating@example.com",
		PasswordSet:  true,
		IsAdmin:      false,
	})
	// email_verified defaults to 0.

	token, _, _, err := auth.Login(context.Background(), st, "logoutgating", "testpass")
	require.NoError(t, err)

	// Logout should be allowed for unverified users.
	logoutResp, err := authClient.Logout(context.Background(), authedReq(&leapmuxv1.LogoutRequest{}, token))
	require.NoError(t, err)

	// Verify the cookie is cleared.
	logoutCookie := logoutResp.Header().Get("Set-Cookie")
	assert.Contains(t, logoutCookie, "Max-Age=0")
}

func TestVerificationGating_RequestEmailChangeAllowed(t *testing.T) {
	userClient, _, st := setupVerificationGatingTestServer(t, true)

	// Create an unverified user.
	orgID := id.Generate()
	userID := id.Generate()
	hash, _ := password.Hash("testpass")
	_ = st.Orgs().Create(context.Background(), store.CreateOrgParams{ID: orgID, Name: "emailchangegate"})
	_ = st.Users().Create(context.Background(), store.CreateUserParams{
		ID:           userID,
		OrgID:        orgID,
		Username:     "emailchangegate",
		PasswordHash: hash,
		DisplayName:  "Email Change Gating",
		Email:        "emailchangegate@example.com",
		PasswordSet:  true,
		IsAdmin:      false,
	})
	// email_verified defaults to 0.

	token, _, _, err := auth.Login(context.Background(), st, "emailchangegate", "testpass")
	require.NoError(t, err)

	// RequestEmailChange should be allowed for unverified users
	// (it should not return PermissionDenied from the gating interceptor).
	_, err = userClient.RequestEmailChange(context.Background(), authedReq(&leapmuxv1.RequestEmailChangeRequest{
		NewEmail: "newemail@example.com",
	}, token))
	// The RPC may succeed or fail for business logic reasons, but should NOT
	// be blocked by the verification gating interceptor (no PermissionDenied).
	if err != nil {
		assert.NotEqual(t, connect.CodePermissionDenied, connect.CodeOf(err),
			"RequestEmailChange should not be blocked by verification gating")
	}
}

func TestAuthService_Logout(t *testing.T) {
	client, _ := setupAuthTestServer(t, testConfig())

	// Login.
	loginResp, err := client.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
		Username: "admin",
		Password: "admin123",
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

// --- Setup mode tests ---

func TestSetupSignUp_CreatesAdminWithVerifiedEmail(t *testing.T) {
	// Signup disabled, but no users exist — setup mode should kick in.
	client, st := setupEmptyAuthTestServer(t, testConfig())

	resp, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "myadmin",
		Password:    "strongpass1",
		DisplayName: "My Admin",
		Email:       "admin@example.com",
	}))
	require.NoError(t, err)

	user := resp.Msg.GetUser()
	assert.Equal(t, "myadmin", user.GetUsername())
	assert.True(t, user.GetIsAdmin())
	assert.Equal(t, "admin@example.com", user.GetEmail())
	assert.True(t, user.GetEmailVerified())

	// Session cookie should be set.
	setCookie := resp.Header().Get("Set-Cookie")
	assert.Contains(t, setCookie, auth.CookieName+"=")

	// Verify in DB.
	dbUser, err := st.Users().GetByUsername(context.Background(), "myadmin")
	require.NoError(t, err)
	assert.True(t, dbUser.IsAdmin)
	assert.Equal(t, "admin@example.com", dbUser.Email)
	assert.True(t, dbUser.EmailVerified)
}

func TestSetupSignUp_EmptyEmail(t *testing.T) {
	client, st := setupEmptyAuthTestServer(t, testConfig())

	resp, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "myadmin",
		Password:    "strongpass1",
		DisplayName: "My Admin",
		Email:       "",
	}))
	require.NoError(t, err)

	user := resp.Msg.GetUser()
	assert.True(t, user.GetIsAdmin())
	assert.Empty(t, user.GetEmail())
	assert.False(t, user.GetEmailVerified())

	// Verify in DB.
	dbUser, err := st.Users().GetByUsername(context.Background(), "myadmin")
	require.NoError(t, err)
	assert.True(t, dbUser.IsAdmin)
	assert.False(t, dbUser.EmailVerified)
}

func TestSetupSignUp_GetSystemInfoReturnsSetupRequired(t *testing.T) {
	client, _ := setupEmptyAuthTestServer(t, testConfig())

	// Before setup: setup_required should be true.
	infoResp, err := client.GetSystemInfo(context.Background(), connect.NewRequest(&leapmuxv1.GetSystemInfoRequest{}))
	require.NoError(t, err)
	assert.True(t, infoResp.Msg.GetSetupRequired())

	// Perform setup.
	_, err = client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "myadmin",
		Password:    "strongpass1",
		DisplayName: "My Admin",
	}))
	require.NoError(t, err)

	// After setup: setup_required should be false.
	infoResp, err = client.GetSystemInfo(context.Background(), connect.NewRequest(&leapmuxv1.GetSystemInfoRequest{}))
	require.NoError(t, err)
	assert.False(t, infoResp.Msg.GetSetupRequired())
}

func TestSetupSignUp_RejectedWhenUsersExist(t *testing.T) {
	// Signup disabled, admin user already exists.
	client, _ := setupAuthTestServer(t, testConfig())

	_, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "newuser",
		Password:    "strongpass1",
		DisplayName: "New User",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestSetupSignUp_RejectedInSoloMode(t *testing.T) {
	cfg := testConfig()
	cfg.SoloMode = true

	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	err = st.Migrator().Migrate(context.Background())
	require.NoError(t, err)

	// No solo user is seeded — the test asserts that AuthService rejects setup
	// signup in solo mode at the service layer, independent of the interceptor.
	mux := http.NewServeMux()
	interceptor, sc := auth.NewInterceptor(st, nil, false, false)
	t.Cleanup(sc.Stop)
	opts := connect.WithInterceptors(interceptor)
	authSvc := service.NewAuthService(st, cfg, sc, nil, mail.NewStubSender())
	path, handler := leapmuxv1connect.NewAuthServiceHandler(authSvc, opts)
	mux.Handle(path, handler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL)

	_, err = client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "myadmin",
		Password:    "strongpass1",
		DisplayName: "My Admin",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestSetupSignUp_NormalSignupStillCreatesNonAdmin(t *testing.T) {
	// Signup enabled + users already exist = normal non-admin signup.
	client, st := setupAuthTestServer(t, testConfigWithSignup())

	resp, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "regularuser",
		Password:    "strongpass1",
		DisplayName: "Regular User",
		Email:       "regular@example.com",
	}))
	require.NoError(t, err)

	user := resp.Msg.GetUser()
	assert.False(t, user.GetIsAdmin())

	dbUser, err := st.Users().GetByUsername(context.Background(), "regularuser")
	require.NoError(t, err)
	assert.False(t, dbUser.IsAdmin)
}

func TestSetupSignUp_WithSignupEnabled(t *testing.T) {
	// Signup enabled + no users = setup mode should still create admin.
	client, st := setupEmptyAuthTestServer(t, testConfigWithSignup())

	resp, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "myadmin",
		Password:    "strongpass1",
		DisplayName: "My Admin",
		Email:       "admin@example.com",
	}))
	require.NoError(t, err)

	user := resp.Msg.GetUser()
	assert.True(t, user.GetIsAdmin(), "first user should be admin even when signup is enabled")
	assert.True(t, user.GetEmailVerified())

	dbUser, err := st.Users().GetByUsername(context.Background(), "myadmin")
	require.NoError(t, err)
	assert.True(t, dbUser.IsAdmin)
}

func TestSetupSignUp_RaceCondition(t *testing.T) {
	// Two setup signups — only the first should succeed.
	client, _ := setupEmptyAuthTestServer(t, testConfig())

	// First signup should succeed.
	_, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "admin1",
		Password:    "strongpass1",
		DisplayName: "Admin 1",
	}))
	require.NoError(t, err)

	// Second signup should fail (users now exist, signup disabled).
	_, err = client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "admin2",
		Password:    "strongpass2",
		DisplayName: "Admin 2",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestSetupSignUp_ValidatesInputs(t *testing.T) {
	client, _ := setupEmptyAuthTestServer(t, testConfig())

	tests := []struct {
		name     string
		username string
		password string
		email    string
	}{
		{"empty username", "", "strongpass1", ""},
		{"weak password", "myadmin", "short", ""},
		{"invalid email", "myadmin", "strongpass1", "not-an-email"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
				Username: tt.username,
				Password: tt.password,
				Email:    tt.email,
			}))
			require.Error(t, err)
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		})
	}
}

func TestGetSystemInfo_DevModeReportsSetupRequired(t *testing.T) {
	cfg := testConfig()
	cfg.DevMode = true

	client, _ := setupEmptyAuthTestServer(t, cfg)

	resp, err := client.GetSystemInfo(context.Background(), connect.NewRequest(&leapmuxv1.GetSystemInfoRequest{}))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetSetupRequired(), "dev mode with empty DB should require setup")
}

func TestSignUp_RejectsSoloAlways(t *testing.T) {
	t.Run("setup mode", func(t *testing.T) {
		client, _ := setupEmptyAuthTestServer(t, testConfig())

		for _, input := range []string{"solo", "SOLO", "  solo  "} {
			_, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
				Username:    input,
				Password:    "strongpass1",
				DisplayName: "First",
			}))
			require.Errorf(t, err, "setup mode must reject %q", input)
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		}
	})

	t.Run("public signup", func(t *testing.T) {
		client, _ := setupAuthTestServer(t, testConfigWithSignup())

		for _, input := range []string{"solo", "SOLO"} {
			_, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
				Username:    input,
				Password:    "strongpass1",
				DisplayName: "Someone",
			}))
			require.Errorf(t, err, "public signup must reject %q", input)
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		}
	})
}

func TestSignUp_AllowsAdminInSetupMode(t *testing.T) {
	client, _ := setupEmptyAuthTestServer(t, testConfig())

	resp, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "admin",
		Password:    "strongpass1",
		DisplayName: "First Admin",
	}))
	require.NoError(t, err)
	assert.Equal(t, "admin", resp.Msg.GetUser().GetUsername())
	assert.True(t, resp.Msg.GetUser().GetIsAdmin())
}

func TestSignUp_RejectsAdminInPublicSignup(t *testing.T) {
	// A seeded user exists, so isSetupMode=false and the public reservation applies.
	client, _ := setupAuthTestServer(t, testConfigWithSignup())

	_, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "admin",
		Password:    "strongpass1",
		DisplayName: "Squatter",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}
