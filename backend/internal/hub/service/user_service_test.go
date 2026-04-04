package service_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/bootstrap"
	"github.com/leapmux/leapmux/internal/hub/db"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/util/id"
)

type userTestEnv struct {
	client  leapmuxv1connect.UserServiceClient
	queries *gendb.Queries
	token   string
	orgID   string
	userID  string
}

func setupUserTest(t *testing.T) *userTestEnv {
	t.Helper()

	sqlDB, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	err = db.Migrate(sqlDB)
	require.NoError(t, err)

	queries := gendb.New(sqlDB)

	userSvc := service.NewUserService(queries, testConfig())

	mux := http.NewServeMux()
	interceptor, _ := auth.NewInterceptor(queries, false, false, false)
	opts := connect.WithInterceptors(interceptor)
	path, handler := leapmuxv1connect.NewUserServiceHandler(userSvc, opts)
	mux.Handle(path, handler)

	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewUserServiceClient(
		server.Client(),
		server.URL,
		connect.WithGRPC(),
	)

	orgID := id.Generate()
	userID := id.Generate()
	hash, _ := password.Hash("pass")

	_ = queries.CreateOrg(context.Background(), gendb.CreateOrgParams{ID: orgID, Name: "testuser"})
	_ = queries.CreateUser(context.Background(), gendb.CreateUserParams{
		ID:           userID,
		OrgID:        orgID,
		Username:     "testuser",
		PasswordHash: hash,
		DisplayName:  "Test User",
		IsAdmin:      1,
	})

	token, _, _, err := auth.Login(context.Background(), queries, "testuser", "pass")
	require.NoError(t, err)

	return &userTestEnv{
		client:  client,
		queries: queries,
		token:   token,
		orgID:   orgID,
		userID:  userID,
	}
}

func TestUserService_UpdateProfile(t *testing.T) {
	env := setupUserTest(t)

	resp, err := env.client.UpdateProfile(context.Background(), authedReq(&leapmuxv1.UpdateProfileRequest{
		Username:    "newname",
		DisplayName: "New Display",
	}, env.token))
	require.NoError(t, err)

	assert.Equal(t, "newname", resp.Msg.GetUsername())
	assert.Equal(t, "New Display", resp.Msg.GetDisplayName())
	assert.Equal(t, "newname", resp.Msg.GetOrgName(), "should rename personal org")

	// Verify the database was actually updated.
	user, err := env.queries.GetUserByID(context.Background(), env.userID)
	require.NoError(t, err)
	assert.Equal(t, "newname", user.Username)
}

func TestUserService_UpdateProfile_SameUsername(t *testing.T) {
	env := setupUserTest(t)

	resp, err := env.client.UpdateProfile(context.Background(), authedReq(&leapmuxv1.UpdateProfileRequest{
		Username:    "testuser",
		DisplayName: "Updated Display",
	}, env.token))
	require.NoError(t, err)

	// OrgName should be empty since username didn't change.
	assert.Empty(t, resp.Msg.GetOrgName(), "username unchanged")
}

func TestUserService_UpdateProfile_DuplicateUsername(t *testing.T) {
	env := setupUserTest(t)

	// Create a second user.
	user2ID := id.Generate()
	hash, _ := password.Hash("pass2")
	_ = env.queries.CreateUser(context.Background(), gendb.CreateUserParams{
		ID:           user2ID,
		OrgID:        env.orgID,
		Username:     "user2",
		PasswordHash: hash,
		DisplayName:  "User 2",
		IsAdmin:      0,
	})

	// Try to change testuser's username to "user2".
	_, err := env.client.UpdateProfile(context.Background(), authedReq(&leapmuxv1.UpdateProfileRequest{
		Username:    "user2",
		DisplayName: "Test User",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestUserService_ChangePassword(t *testing.T) {
	env := setupUserTest(t)

	_, err := env.client.ChangePassword(context.Background(), authedReq(&leapmuxv1.ChangePasswordRequest{
		CurrentPassword: "pass",
		NewPassword:     "newpass123",
	}, env.token))
	require.NoError(t, err)

	// Verify login works with new password.
	_, _, _, err = auth.Login(context.Background(), env.queries, "testuser", "newpass123")
	assert.NoError(t, err)

	// Verify login with old password fails.
	_, _, _, err = auth.Login(context.Background(), env.queries, "testuser", "pass")
	require.Error(t, err)
}

func TestUserService_ChangePassword_WrongCurrent(t *testing.T) {
	env := setupUserTest(t)

	_, err := env.client.ChangePassword(context.Background(), authedReq(&leapmuxv1.ChangePasswordRequest{
		CurrentPassword: "wrongpassword",
		NewPassword:     "newpass123",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestUserService_GetPreferences_Default(t *testing.T) {
	env := setupUserTest(t)

	resp, err := env.client.GetPreferences(context.Background(), authedReq(&leapmuxv1.GetPreferencesRequest{}, env.token))
	require.NoError(t, err)

	prefs := resp.Msg.GetPreferences()
	assert.False(t, prefs.GetUiFontCustomEnabled(), "ui_font_custom_enabled should be false by default")
	assert.False(t, prefs.GetMonoFontCustomEnabled(), "mono_font_custom_enabled should be false by default")
	assert.Empty(t, prefs.GetTheme(), "theme should be empty by default")
	assert.Empty(t, prefs.GetTerminalTheme(), "terminal_theme should be empty by default")
}

func TestUserService_UpdatePreferences(t *testing.T) {
	env := setupUserTest(t)

	_, err := env.client.UpdatePreferences(context.Background(), authedReq(&leapmuxv1.UpdatePreferencesRequest{
		Theme:                 "dark",
		TerminalTheme:         "match-ui",
		UiFontCustomEnabled:   true,
		MonoFontCustomEnabled: true,
		UiFonts:               []string{"Inter", "Roboto"},
		MonoFonts:             []string{"JetBrains Mono"},
	}, env.token))
	require.NoError(t, err)

	// Verify via GetPreferences.
	resp, err := env.client.GetPreferences(context.Background(), authedReq(&leapmuxv1.GetPreferencesRequest{}, env.token))
	require.NoError(t, err)

	prefs := resp.Msg.GetPreferences()
	assert.True(t, prefs.GetUiFontCustomEnabled())
	assert.True(t, prefs.GetMonoFontCustomEnabled())
	assert.Equal(t, "dark", prefs.GetTheme())
	assert.Equal(t, "match-ui", prefs.GetTerminalTheme())
	assert.Len(t, prefs.GetUiFonts(), 2)
	assert.Len(t, prefs.GetMonoFonts(), 1)
}

func TestUserService_UpdatePreferences_InvalidFontName(t *testing.T) {
	env := setupUserTest(t)

	_, err := env.client.UpdatePreferences(context.Background(), authedReq(&leapmuxv1.UpdatePreferencesRequest{
		UiFonts: []string{"  "},
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestUserService_UpdatePreferences_DebugLogging(t *testing.T) {
	env := setupUserTest(t)

	_, err := env.client.UpdatePreferences(context.Background(), authedReq(&leapmuxv1.UpdatePreferencesRequest{
		DebugLogging: true,
	}, env.token))
	require.NoError(t, err)

	resp, err := env.client.GetPreferences(context.Background(), authedReq(&leapmuxv1.GetPreferencesRequest{}, env.token))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetPreferences().GetDebugLogging())

	// Disable again.
	_, err = env.client.UpdatePreferences(context.Background(), authedReq(&leapmuxv1.UpdatePreferencesRequest{
		DebugLogging: false,
	}, env.token))
	require.NoError(t, err)

	resp, err = env.client.GetPreferences(context.Background(), authedReq(&leapmuxv1.GetPreferencesRequest{}, env.token))
	require.NoError(t, err)
	assert.False(t, resp.Msg.GetPreferences().GetDebugLogging())
}

func TestUserService_Unauthenticated(t *testing.T) {
	env := setupUserTest(t)

	_, err := env.client.GetPreferences(context.Background(), connect.NewRequest(&leapmuxv1.GetPreferencesRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestRequestEmailChange_Success(t *testing.T) {
	env := setupUserTest(t)

	// Set an initial email on the user.
	err := env.queries.UpdateUserEmail(context.Background(), gendb.UpdateUserEmailParams{
		Email:         "old@example.com",
		EmailVerified: 1,
		ID:            env.userID,
	})
	require.NoError(t, err)

	// Request an email change.
	resp, err := env.client.RequestEmailChange(context.Background(), authedReq(&leapmuxv1.RequestEmailChangeRequest{
		NewEmail: "new@example.com",
	}, env.token))
	require.NoError(t, err)
	// Admin users get immediate change (no verification required).
	assert.False(t, resp.Msg.GetVerificationRequired())

	// Verify the email was updated in the DB.
	user, err := env.queries.GetUserByID(context.Background(), env.userID)
	require.NoError(t, err)
	assert.Equal(t, "new@example.com", user.Email)
}

func TestRequestEmailChange_EmptyEmail_Rejected(t *testing.T) {
	env := setupUserTest(t)

	_, err := env.client.RequestEmailChange(context.Background(), authedReq(&leapmuxv1.RequestEmailChangeRequest{
		NewEmail: "",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestRequestEmailChange_SameEmail_Rejected(t *testing.T) {
	env := setupUserTest(t)

	// Set an email on the user.
	err := env.queries.UpdateUserEmail(context.Background(), gendb.UpdateUserEmailParams{
		Email:         "same@example.com",
		EmailVerified: 1,
		ID:            env.userID,
	})
	require.NoError(t, err)

	// Try to change to the same email.
	_, err = env.client.RequestEmailChange(context.Background(), authedReq(&leapmuxv1.RequestEmailChangeRequest{
		NewEmail: "same@example.com",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

// --- UpdateProfile: email field removed ---

func TestUpdateProfile_EmailFieldRemoved(t *testing.T) {
	env := setupUserTest(t)

	// Set an email on the user directly in the DB.
	err := env.queries.UpdateUserEmail(context.Background(), gendb.UpdateUserEmailParams{
		Email:         "preserved@example.com",
		EmailVerified: 1,
		ID:            env.userID,
	})
	require.NoError(t, err)

	// Call UpdateProfile (proto has no email field).
	_, err = env.client.UpdateProfile(context.Background(), authedReq(&leapmuxv1.UpdateProfileRequest{
		Username:    "testuser",
		DisplayName: "Updated Display",
	}, env.token))
	require.NoError(t, err)

	// Verify the email is unchanged in the DB.
	user, err := env.queries.GetUserByID(context.Background(), env.userID)
	require.NoError(t, err)
	assert.Equal(t, "preserved@example.com", user.Email)
}

// --- RequestEmailChange: admin immediate change with email_verified ---

func TestRequestEmailChange_Admin_ImmediateChange(t *testing.T) {
	env := setupUserTest(t)

	// The test user is an admin (IsAdmin=1 in setupUserTest).
	resp, err := env.client.RequestEmailChange(context.Background(), authedReq(&leapmuxv1.RequestEmailChangeRequest{
		NewEmail: "admin-new@example.com",
	}, env.token))
	require.NoError(t, err)
	assert.False(t, resp.Msg.GetVerificationRequired())

	// Verify the email was updated in the DB with email_verified=1.
	user, err := env.queries.GetUserByID(context.Background(), env.userID)
	require.NoError(t, err)
	assert.Equal(t, "admin-new@example.com", user.Email)
	assert.Equal(t, int64(1), user.EmailVerified)
}

// --- RequestEmailChange: duplicate email rejected ---

func TestRequestEmailChange_DuplicateEmail_Rejected(t *testing.T) {
	env := setupUserTest(t)

	// Create a second user with an email.
	user2ID := id.Generate()
	hash, _ := password.Hash("pass2")
	_ = env.queries.CreateUser(context.Background(), gendb.CreateUserParams{
		ID:           user2ID,
		OrgID:        env.orgID,
		Username:     "user2",
		PasswordHash: hash,
		DisplayName:  "User 2",
		IsAdmin:      0,
	})
	err := env.queries.UpdateUserEmail(context.Background(), gendb.UpdateUserEmailParams{
		Email:         "claimed@example.com",
		EmailVerified: 1,
		ID:            user2ID,
	})
	require.NoError(t, err)

	// Try to change testuser's email to the claimed email.
	_, err = env.client.RequestEmailChange(context.Background(), authedReq(&leapmuxv1.RequestEmailChangeRequest{
		NewEmail: "claimed@example.com",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
}

// --- RequestEmailChange: config on, pending email ---

// setupVerificationUserTestServer creates a test server with
// EmailVerificationRequired=true and both UserService and AuthService
// registered. It returns a UserService client, queries, and the admin token.
func setupVerificationUserTestServer(t *testing.T) (leapmuxv1connect.UserServiceClient, *gendb.Queries, string) {
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
	interceptor, _ := auth.NewInterceptor(q, false, false, true)
	opts := connect.WithInterceptors(interceptor)

	cfg := testConfig()
	cfg.EmailVerificationRequired = true

	userSvc := service.NewUserService(q, cfg)
	userPath, userHandler := leapmuxv1connect.NewUserServiceHandler(userSvc, opts)
	mux.Handle(userPath, userHandler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewUserServiceClient(server.Client(), server.URL)

	// Log in as admin (bootstrap user).
	token, _, _, err := auth.Login(context.Background(), q, "admin", "admin")
	require.NoError(t, err)

	return client, q, token
}

func TestRequestEmailChange_ConfigOn_PendingEmail(t *testing.T) {
	client, q, adminToken := setupVerificationUserTestServer(t)

	// Create a non-admin user.
	adminUser, err := q.GetUserByUsername(context.Background(), "admin")
	require.NoError(t, err)

	userID := id.Generate()
	hash, _ := password.Hash("userpass")
	err = q.CreateUser(context.Background(), gendb.CreateUserParams{
		ID:           userID,
		OrgID:        adminUser.OrgID,
		Username:     "verifyuser",
		PasswordHash: hash,
		DisplayName:  "Verify User",
		IsAdmin:      0,
	})
	require.NoError(t, err)
	err = q.CreateOrgMember(context.Background(), gendb.CreateOrgMemberParams{
		OrgID:  adminUser.OrgID,
		UserID: userID,
		Role:   leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER,
	})
	require.NoError(t, err)

	// Set email_verified=1 so the user is not gated by verification interceptor.
	err = q.UpdateUserEmail(context.Background(), gendb.UpdateUserEmailParams{
		Email:         "old@example.com",
		EmailVerified: 1,
		ID:            userID,
	})
	require.NoError(t, err)

	// Log in as the non-admin user.
	userToken, _, _, err := auth.Login(context.Background(), q, "verifyuser", "userpass")
	require.NoError(t, err)

	// Request email change.
	resp, err := client.RequestEmailChange(context.Background(), authedReq(&leapmuxv1.RequestEmailChangeRequest{
		NewEmail: "pending@example.com",
	}, userToken))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetVerificationRequired())

	// The stub auto-promotes, so the email should be in the email column.
	user, err := q.GetUserByID(context.Background(), userID)
	require.NoError(t, err)
	assert.Equal(t, "pending@example.com", user.Email)

	// Verify admin gets immediate change (not pending).
	_ = adminToken
}

// --- VerifyEmailChange ---

func TestVerifyEmailChange_Success(t *testing.T) {
	env := setupUserTest(t)

	// Set pending_email + token via DB directly.
	verifyToken := id.Generate()
	err := env.queries.SetPendingEmail(context.Background(), gendb.SetPendingEmailParams{
		PendingEmail:          "verified@example.com",
		PendingEmailToken:     verifyToken,
		PendingEmailExpiresAt: sql.NullTime{Time: time.Now().Add(1 * time.Hour).UTC(), Valid: true},
		ID:                    env.userID,
	})
	require.NoError(t, err)

	// Call VerifyEmailChange.
	resp, err := env.client.VerifyEmailChange(context.Background(), authedReq(&leapmuxv1.VerifyEmailChangeRequest{
		VerificationToken: verifyToken,
	}, env.token))
	require.NoError(t, err)
	assert.Equal(t, "verified@example.com", resp.Msg.GetUser().GetEmail())

	// Verify in DB: email promoted, pending cleared, email_verified=1.
	user, err := env.queries.GetUserByID(context.Background(), env.userID)
	require.NoError(t, err)
	assert.Equal(t, "verified@example.com", user.Email)
	assert.Equal(t, int64(1), user.EmailVerified)
	assert.Empty(t, user.PendingEmail)
	assert.Empty(t, user.PendingEmailToken)
}

func TestVerifyEmailChange_InvalidToken(t *testing.T) {
	env := setupUserTest(t)

	_, err := env.client.VerifyEmailChange(context.Background(), authedReq(&leapmuxv1.VerifyEmailChangeRequest{
		VerificationToken: "bogus-token",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestVerifyEmailChange_ExpiredToken(t *testing.T) {
	env := setupUserTest(t)

	// Set pending_email with an expired token.
	verifyToken := id.Generate()
	err := env.queries.SetPendingEmail(context.Background(), gendb.SetPendingEmailParams{
		PendingEmail:          "expired@example.com",
		PendingEmailToken:     verifyToken,
		PendingEmailExpiresAt: sql.NullTime{Time: time.Now().Add(-1 * time.Hour).UTC(), Valid: true},
		ID:                    env.userID,
	})
	require.NoError(t, err)

	_, err = env.client.VerifyEmailChange(context.Background(), authedReq(&leapmuxv1.VerifyEmailChangeRequest{
		VerificationToken: verifyToken,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestVerifyEmailChange_PendingEmailEmpty(t *testing.T) {
	env := setupUserTest(t)

	// Set a token but with empty pending_email.
	verifyToken := id.Generate()
	err := env.queries.SetPendingEmail(context.Background(), gendb.SetPendingEmailParams{
		PendingEmail:          "",
		PendingEmailToken:     verifyToken,
		PendingEmailExpiresAt: sql.NullTime{Time: time.Now().Add(1 * time.Hour).UTC(), Valid: true},
		ID:                    env.userID,
	})
	require.NoError(t, err)

	_, err = env.client.VerifyEmailChange(context.Background(), authedReq(&leapmuxv1.VerifyEmailChangeRequest{
		VerificationToken: verifyToken,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestVerifyEmailChange_EmailTakenSinceRequest(t *testing.T) {
	env := setupUserTest(t)

	// Set pending_email on the test user.
	verifyToken := id.Generate()
	err := env.queries.SetPendingEmail(context.Background(), gendb.SetPendingEmailParams{
		PendingEmail:          "contested@example.com",
		PendingEmailToken:     verifyToken,
		PendingEmailExpiresAt: sql.NullTime{Time: time.Now().Add(1 * time.Hour).UTC(), Valid: true},
		ID:                    env.userID,
	})
	require.NoError(t, err)

	// Create another user who claims that email in the email column.
	user2ID := id.Generate()
	hash, _ := password.Hash("pass2")
	_ = env.queries.CreateUser(context.Background(), gendb.CreateUserParams{
		ID:           user2ID,
		OrgID:        env.orgID,
		Username:     "claimer",
		PasswordHash: hash,
		DisplayName:  "Claimer",
		IsAdmin:      0,
	})
	err = env.queries.UpdateUserEmail(context.Background(), gendb.UpdateUserEmailParams{
		Email:         "contested@example.com",
		EmailVerified: 1,
		ID:            user2ID,
	})
	require.NoError(t, err)

	// Try to verify -- should fail because the email is now taken.
	_, err = env.client.VerifyEmailChange(context.Background(), authedReq(&leapmuxv1.VerifyEmailChangeRequest{
		VerificationToken: verifyToken,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
}
