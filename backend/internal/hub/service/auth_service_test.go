package service_test

import (
	"context"
	"database/sql"
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
	"github.com/leapmux/leapmux/internal/hub/bootstrap"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/db"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/util/id"
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
	interceptor, sc := auth.NewInterceptor(q, false, false, false)
	t.Cleanup(sc.Stop)
	opts := connect.WithInterceptors(interceptor)
	authSvc := service.NewAuthService(sqlDB, q, cfg, sc, nil)
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
	client, q := setupAuthTestServer(t, testConfig())

	// Login to get a token.
	loginResp, err := client.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
		Username: "admin",
		Password: "admin123",
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

func TestSignUp_DuplicateEmail_Rejected(t *testing.T) {
	client, q := setupAuthTestServer(t, testConfigWithSignup())

	// Create a user with that email directly in the DB.
	orgID := id.Generate()
	err := q.CreateOrg(context.Background(), gendb.CreateOrgParams{ID: orgID, Name: "emailuser"})
	require.NoError(t, err)
	hash, err := password.Hash("testpass")
	require.NoError(t, err)
	err = q.CreateUser(context.Background(), gendb.CreateUserParams{
		ID:           id.Generate(),
		OrgID:        orgID,
		Username:     "emailuser",
		PasswordHash: hash,
		DisplayName:  "Email User",
		Email:        "taken@example.com",
		PasswordSet:  1,
		IsAdmin:      0,
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
	_, q := setupAuthTestServer(t, testConfigWithSignup())
	ctx := context.Background()

	// Create two users, both with pending_email = "shared@example.com".
	for _, username := range []string{"user-a", "user-b"} {
		orgID := id.Generate()
		err := q.CreateOrg(ctx, gendb.CreateOrgParams{ID: orgID, Name: username + "-org"})
		require.NoError(t, err)
		hash, err := password.Hash("testpass")
		require.NoError(t, err)
		userID := id.Generate()
		err = q.CreateUser(ctx, gendb.CreateUserParams{
			ID:           userID,
			OrgID:        orgID,
			Username:     username,
			PasswordHash: hash,
			DisplayName:  username,
			Email:        "",
			PasswordSet:  1,
			IsAdmin:      0,
		})
		require.NoError(t, err)
		err = q.SetPendingEmail(ctx, gendb.SetPendingEmailParams{
			PendingEmail:      "shared@example.com",
			PendingEmailToken: id.Generate(),
			PendingEmailExpiresAt: sql.NullTime{
				Time:  time.Now().Add(24 * time.Hour).UTC(),
				Valid: true,
			},
			ID: userID,
		})
		require.NoError(t, err)
	}

	// Verify both have pending_email set.
	userA, err := q.GetUserByUsername(ctx, "user-a")
	require.NoError(t, err)
	assert.Equal(t, "shared@example.com", userA.PendingEmail)
	userB, err := q.GetUserByUsername(ctx, "user-b")
	require.NoError(t, err)
	assert.Equal(t, "shared@example.com", userB.PendingEmail)

	// User A promotes — this should also clear user B's pending_email.
	err = q.PromotePendingEmail(ctx, userA.ID)
	require.NoError(t, err)
	err = q.ClearCompetingPendingEmails(ctx, gendb.ClearCompetingPendingEmailsParams{
		PendingEmail: "shared@example.com",
		ID:           userA.ID,
	})
	require.NoError(t, err)

	// User A now has verified email.
	userA, err = q.GetUserByUsername(ctx, "user-a")
	require.NoError(t, err)
	assert.Equal(t, "shared@example.com", userA.Email)
	assert.Equal(t, int64(1), userA.EmailVerified)
	assert.Empty(t, userA.PendingEmail)

	// User B's pending_email should be cleared.
	userB, err = q.GetUserByUsername(ctx, "user-b")
	require.NoError(t, err)
	assert.Empty(t, userB.PendingEmail)
	assert.Empty(t, userB.Email)
}

func TestSignUp_DirectEmail_ClearsCompetingPendingEmails(t *testing.T) {
	client, q := setupAuthTestServer(t, testConfigWithSignup())
	ctx := context.Background()

	// User A sets pending_email = "race@example.com" (unverified).
	orgID := id.Generate()
	err := q.CreateOrg(ctx, gendb.CreateOrgParams{ID: orgID, Name: "racer-org"})
	require.NoError(t, err)
	hash, err := password.Hash("testpass")
	require.NoError(t, err)
	userAID := id.Generate()
	err = q.CreateUser(ctx, gendb.CreateUserParams{
		ID:           userAID,
		OrgID:        orgID,
		Username:     "racer",
		PasswordHash: hash,
		DisplayName:  "Racer",
		Email:        "",
		PasswordSet:  1,
		IsAdmin:      0,
	})
	require.NoError(t, err)
	err = q.SetPendingEmail(ctx, gendb.SetPendingEmailParams{
		PendingEmail:      "race@example.com",
		PendingEmailToken: id.Generate(),
		PendingEmailExpiresAt: sql.NullTime{
			Time:  time.Now().Add(24 * time.Hour).UTC(),
			Valid: true,
		},
		ID: userAID,
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
	userA, err := q.GetUserByUsername(ctx, "racer")
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
	*gendb.Queries,
	*sql.DB,
) {
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
	interceptor, _ := auth.NewInterceptor(q, false, false, emailVerificationRequired)
	opts := connect.WithInterceptors(interceptor)

	cfg := testConfig()
	cfg.SignupEnabled = true
	cfg.EmailVerificationRequired = emailVerificationRequired

	userSvc := service.NewUserService(q, cfg)
	userPath, userHandler := leapmuxv1connect.NewUserServiceHandler(userSvc, opts)
	mux.Handle(userPath, userHandler)

	authSvc := service.NewAuthService(sqlDB, q, cfg, nil, nil)
	authPath, authHandler := leapmuxv1connect.NewAuthServiceHandler(authSvc, opts)
	mux.Handle(authPath, authHandler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	userClient := leapmuxv1connect.NewUserServiceClient(server.Client(), server.URL)
	authClient := leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL)
	return userClient, authClient, q, sqlDB
}

func TestVerificationGating_UnverifiedBlocked(t *testing.T) {
	userClient, _, q, _ := setupVerificationGatingTestServer(t, true)

	// Create a user with email_verified=0 directly via DB.
	orgID := id.Generate()
	userID := id.Generate()
	hash, _ := password.Hash("testpass")
	_ = q.CreateOrg(context.Background(), gendb.CreateOrgParams{ID: orgID, Name: "unverified"})
	_ = q.CreateUser(context.Background(), gendb.CreateUserParams{
		ID:           userID,
		OrgID:        orgID,
		Username:     "unverified",
		PasswordHash: hash,
		DisplayName:  "Unverified User",
		Email:        "unverified@example.com",
		PasswordSet:  1,
		IsAdmin:      0,
	})
	// email_verified defaults to 0 in the DB.

	token, _, _, err := auth.Login(context.Background(), q, "unverified", "testpass")
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
	userClient, _, q, _ := setupVerificationGatingTestServer(t, true)

	// The bootstrap admin has email_verified=0 by default (no email set).
	// Verify the admin can still call protected RPCs.
	adminToken, _, _, err := auth.Login(context.Background(), q, "admin", "admin123")
	require.NoError(t, err)

	// Admin should be able to call UpdateProfile even with email_verified=0.
	_, err = userClient.UpdateProfile(context.Background(), authedReq(&leapmuxv1.UpdateProfileRequest{
		Username:    "admin",
		DisplayName: "Admin Updated",
	}, adminToken))
	require.NoError(t, err)
}

func TestVerificationGating_ConfigOff_NotBlocked(t *testing.T) {
	userClient, _, q, _ := setupVerificationGatingTestServer(t, false)

	// Create an unverified user.
	orgID := id.Generate()
	userID := id.Generate()
	hash, _ := password.Hash("testpass")
	_ = q.CreateOrg(context.Background(), gendb.CreateOrgParams{ID: orgID, Name: "nogate"})
	_ = q.CreateUser(context.Background(), gendb.CreateUserParams{
		ID:           userID,
		OrgID:        orgID,
		Username:     "nogate",
		PasswordHash: hash,
		DisplayName:  "No Gate User",
		Email:        "nogate@example.com",
		PasswordSet:  1,
		IsAdmin:      0,
	})
	// email_verified defaults to 0 — but gating is OFF.

	token, _, _, err := auth.Login(context.Background(), q, "nogate", "testpass")
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

	client, q := setupAuthTestServer(t, cfg)

	resp, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "verifyuser",
		Password:    "password123",
		DisplayName: "Verify User",
		Email:       "verify@example.com",
	}))
	require.NoError(t, err)

	// The response should indicate verification was required.
	assert.True(t, resp.Msg.GetVerificationRequired())
	assert.Equal(t, "verifyuser", resp.Msg.GetUser().GetUsername())

	// Because the stub auto-promotes pending_email, the email should end up
	// in the email column with email_verified=1.
	user, err := q.GetUserByUsername(context.Background(), "verifyuser")
	require.NoError(t, err)
	assert.Equal(t, "verify@example.com", user.Email)
	assert.Equal(t, int64(1), user.EmailVerified)
	// pending_email should be cleared after promotion.
	assert.Empty(t, user.PendingEmail)
}

// --- VerifyEmail tests ---

func setupAuthTestServerWithKeystore(t *testing.T, cfg *config.Config) (leapmuxv1connect.AuthServiceClient, *gendb.Queries, *sql.DB) {
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
	return client, q, sqlDB
}

func createTestUserWithPendingEmail(t *testing.T, q *gendb.Queries, username, pendingEmail, token string, expiresAt sql.NullTime) string {
	t.Helper()
	orgID := id.Generate()
	userID := id.Generate()
	hash, err := password.Hash("testpass")
	require.NoError(t, err)

	err = q.CreateOrg(context.Background(), gendb.CreateOrgParams{ID: orgID, Name: username})
	require.NoError(t, err)
	err = q.CreateUser(context.Background(), gendb.CreateUserParams{
		ID:           userID,
		OrgID:        orgID,
		Username:     username,
		PasswordHash: hash,
		DisplayName:  username,
		Email:        "",
		PasswordSet:  1,
		IsAdmin:      0,
	})
	require.NoError(t, err)

	// Set pending email directly.
	err = q.SetPendingEmail(context.Background(), gendb.SetPendingEmailParams{
		PendingEmail:          pendingEmail,
		PendingEmailToken:     token,
		PendingEmailExpiresAt: expiresAt,
		ID:                    userID,
	})
	require.NoError(t, err)

	return userID
}

func TestVerifyEmail_PromotesPendingEmail(t *testing.T) {
	cfg := testConfig()
	cfg.EmailVerificationRequired = true
	client, q, _ := setupAuthTestServerWithKeystore(t, cfg)

	verifyToken := id.Generate()
	userID := createTestUserWithPendingEmail(t, q, "pendinguser", "pending@example.com", verifyToken,
		sql.NullTime{Time: time.Now().Add(24 * time.Hour).UTC(), Valid: true})

	resp, err := client.VerifyEmail(context.Background(), connect.NewRequest(&leapmuxv1.VerifyEmailRequest{
		VerificationToken: verifyToken,
	}))
	require.NoError(t, err)

	// Email should be promoted.
	assert.Equal(t, "pending@example.com", resp.Msg.GetUser().GetEmail())
	assert.True(t, resp.Msg.GetUser().GetEmailVerified())

	// Session cookie should be set.
	setCookie := resp.Header().Get("Set-Cookie")
	assert.Contains(t, setCookie, auth.CookieName+"=")

	// Verify in DB.
	user, err := q.GetUserByID(context.Background(), userID)
	require.NoError(t, err)
	assert.Equal(t, "pending@example.com", user.Email)
	assert.Equal(t, int64(1), user.EmailVerified)
	assert.Empty(t, user.PendingEmail)
	assert.Empty(t, user.PendingEmailToken)
}

func TestVerifyEmail_InvalidToken(t *testing.T) {
	client, _, _ := setupAuthTestServerWithKeystore(t, testConfig())

	_, err := client.VerifyEmail(context.Background(), connect.NewRequest(&leapmuxv1.VerifyEmailRequest{
		VerificationToken: "nonexistent-token",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestVerifyEmail_ExpiredToken(t *testing.T) {
	cfg := testConfig()
	client, q, _ := setupAuthTestServerWithKeystore(t, cfg)

	verifyToken := id.Generate()
	_ = createTestUserWithPendingEmail(t, q, "expiredverify", "expired@example.com", verifyToken,
		sql.NullTime{Time: time.Now().Add(-1 * time.Hour).UTC(), Valid: true})

	_, err := client.VerifyEmail(context.Background(), connect.NewRequest(&leapmuxv1.VerifyEmailRequest{
		VerificationToken: verifyToken,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

// --- Verification gating: allowed methods ---

func TestVerificationGating_LogoutAllowed(t *testing.T) {
	_, authClient, q, _ := setupVerificationGatingTestServer(t, true)

	// Create an unverified user.
	orgID := id.Generate()
	userID := id.Generate()
	hash, _ := password.Hash("testpass")
	_ = q.CreateOrg(context.Background(), gendb.CreateOrgParams{ID: orgID, Name: "logoutgating"})
	_ = q.CreateUser(context.Background(), gendb.CreateUserParams{
		ID:           userID,
		OrgID:        orgID,
		Username:     "logoutgating",
		PasswordHash: hash,
		DisplayName:  "Logout Gating",
		Email:        "logoutgating@example.com",
		PasswordSet:  1,
		IsAdmin:      0,
	})
	// email_verified defaults to 0.

	token, _, _, err := auth.Login(context.Background(), q, "logoutgating", "testpass")
	require.NoError(t, err)

	// Logout should be allowed for unverified users.
	logoutResp, err := authClient.Logout(context.Background(), authedReq(&leapmuxv1.LogoutRequest{}, token))
	require.NoError(t, err)

	// Verify the cookie is cleared.
	logoutCookie := logoutResp.Header().Get("Set-Cookie")
	assert.Contains(t, logoutCookie, "Max-Age=0")
}

func TestVerificationGating_RequestEmailChangeAllowed(t *testing.T) {
	userClient, _, q, _ := setupVerificationGatingTestServer(t, true)

	// Create an unverified user.
	orgID := id.Generate()
	userID := id.Generate()
	hash, _ := password.Hash("testpass")
	_ = q.CreateOrg(context.Background(), gendb.CreateOrgParams{ID: orgID, Name: "emailchangegate"})
	_ = q.CreateUser(context.Background(), gendb.CreateUserParams{
		ID:           userID,
		OrgID:        orgID,
		Username:     "emailchangegate",
		PasswordHash: hash,
		DisplayName:  "Email Change Gating",
		Email:        "emailchangegate@example.com",
		PasswordSet:  1,
		IsAdmin:      0,
	})
	// email_verified defaults to 0.

	token, _, _, err := auth.Login(context.Background(), q, "emailchangegate", "testpass")
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
