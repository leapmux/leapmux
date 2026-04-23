package service_test

import (
	"context"
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

	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
)

type userTestEnv struct {
	client leapmuxv1connect.UserServiceClient
	store  store.Store
	token  string
	orgID  string
	userID string
}

func setupUserTest(t *testing.T) *userTestEnv {
	t.Helper()

	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	err = st.Migrator().Migrate(context.Background())
	require.NoError(t, err)

	userSvc := service.NewUserService(st, testConfig(), nil)

	mux := http.NewServeMux()
	interceptor, _ := auth.NewInterceptor(st, nil, false, false)
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
	hash, _ := password.Hash("testpass")

	_ = st.Orgs().Create(context.Background(), store.CreateOrgParams{ID: orgID, Name: "testuser"})
	_ = st.Users().Create(context.Background(), store.CreateUserParams{
		ID:           userID,
		OrgID:        orgID,
		Username:     "testuser",
		PasswordHash: hash,
		DisplayName:  "Test User",
		PasswordSet:  true,
		IsAdmin:      true,
	})

	token, _, _, err := auth.Login(context.Background(), st, "testuser", "testpass")
	require.NoError(t, err)

	return &userTestEnv{
		client: client,
		store:  st,
		token:  token,
		orgID:  orgID,
		userID: userID,
	}
}

// setupOAuthUserTest creates a test env with an OAuth-only user (PasswordSet=false).
func setupOAuthUserTest(t *testing.T) *userTestEnv {
	t.Helper()

	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	err = st.Migrator().Migrate(context.Background())
	require.NoError(t, err)

	userSvc := service.NewUserService(st, testConfig(), nil)

	mux := http.NewServeMux()
	interceptor, _ := auth.NewInterceptor(st, nil, false, false)
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
	hash, _ := password.Hash("testpass")

	_ = st.Orgs().Create(context.Background(), store.CreateOrgParams{ID: orgID, Name: "testuser"})
	_ = st.Users().Create(context.Background(), store.CreateUserParams{
		ID:           userID,
		OrgID:        orgID,
		Username:     "testuser",
		PasswordHash: hash,
		DisplayName:  "Test User",
		PasswordSet:  false,
		IsAdmin:      true,
	})

	token, _, _, err := auth.Login(context.Background(), st, "testuser", "testpass")
	require.NoError(t, err)

	return &userTestEnv{
		client: client,
		store:  st,
		token:  token,
		orgID:  orgID,
		userID: userID,
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
	user, err := env.store.Users().GetByID(context.Background(), env.userID)
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
	hash, _ := password.Hash("testpass2")
	_ = env.store.Users().Create(context.Background(), store.CreateUserParams{
		ID:           user2ID,
		OrgID:        env.orgID,
		Username:     "user2",
		PasswordHash: hash,
		DisplayName:  "User 2",
		PasswordSet:  true,
		IsAdmin:      false,
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
		CurrentPassword: "testpass",
		NewPassword:     "newpass123",
	}, env.token))
	require.NoError(t, err)

	// Verify login works with new password.
	_, _, _, err = auth.Login(context.Background(), env.store, "testuser", "newpass123")
	assert.NoError(t, err)

	// Verify login with old password fails.
	_, _, _, err = auth.Login(context.Background(), env.store, "testuser", "testpass")
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
	err := env.store.Users().UpdateEmail(context.Background(), store.UpdateUserEmailParams{
		Email:         "old@example.com",
		EmailVerified: true,
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
	user, err := env.store.Users().GetByID(context.Background(), env.userID)
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
	err := env.store.Users().UpdateEmail(context.Background(), store.UpdateUserEmailParams{
		Email:         "same@example.com",
		EmailVerified: true,
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
	err := env.store.Users().UpdateEmail(context.Background(), store.UpdateUserEmailParams{
		Email:         "preserved@example.com",
		EmailVerified: true,
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
	user, err := env.store.Users().GetByID(context.Background(), env.userID)
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
	user, err := env.store.Users().GetByID(context.Background(), env.userID)
	require.NoError(t, err)
	assert.Equal(t, "admin-new@example.com", user.Email)
	assert.True(t, user.EmailVerified)
}

// --- RequestEmailChange: duplicate email rejected ---

func TestRequestEmailChange_DuplicateEmail_Rejected(t *testing.T) {
	env := setupUserTest(t)

	// Create a second user with an email.
	user2ID := id.Generate()
	hash, _ := password.Hash("testpass2")
	_ = env.store.Users().Create(context.Background(), store.CreateUserParams{
		ID:           user2ID,
		OrgID:        env.orgID,
		Username:     "user2",
		PasswordHash: hash,
		DisplayName:  "User 2",
		PasswordSet:  true,
		IsAdmin:      false,
	})
	err := env.store.Users().UpdateEmail(context.Background(), store.UpdateUserEmailParams{
		Email:         "claimed@example.com",
		EmailVerified: true,
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
// registered. It returns a UserService client, st, and the admin token.
func setupVerificationUserTestServer(t *testing.T) (leapmuxv1connect.UserServiceClient, store.Store, string) {
	t.Helper()

	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	err = st.Migrator().Migrate(context.Background())
	require.NoError(t, err)

	hubtestutil.CreateTestAdmin(t, st)

	mux := http.NewServeMux()
	interceptor, _ := auth.NewInterceptor(st, nil, false, true)
	opts := connect.WithInterceptors(interceptor)

	cfg := testConfig()
	cfg.EmailVerificationRequired = true

	userSvc := service.NewUserService(st, cfg, nil)
	userPath, userHandler := leapmuxv1connect.NewUserServiceHandler(userSvc, opts)
	mux.Handle(userPath, userHandler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewUserServiceClient(server.Client(), server.URL)

	// Log in as admin (bootstrap user).
	token, _, _, err := auth.Login(context.Background(), st, "admin", "admin123")
	require.NoError(t, err)

	return client, st, token
}

func TestRequestEmailChange_ConfigOn_PendingEmail(t *testing.T) {
	client, st, adminToken := setupVerificationUserTestServer(t)

	// Create a non-admin user.
	adminUser, err := st.Users().GetByUsername(context.Background(), "admin")
	require.NoError(t, err)

	userID := id.Generate()
	hash, _ := password.Hash("userpass")
	err = st.Users().Create(context.Background(), store.CreateUserParams{
		ID:           userID,
		OrgID:        adminUser.OrgID,
		Username:     "verifyuser",
		PasswordHash: hash,
		DisplayName:  "Verify User",
		PasswordSet:  true,
		IsAdmin:      false,
	})
	require.NoError(t, err)
	err = st.OrgMembers().Create(context.Background(), store.CreateOrgMemberParams{
		OrgID:  adminUser.OrgID,
		UserID: userID,
		Role:   leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER,
	})
	require.NoError(t, err)

	// Set email_verified=1 so the user is not gated by verification interceptor.
	err = st.Users().UpdateEmail(context.Background(), store.UpdateUserEmailParams{
		Email:         "old@example.com",
		EmailVerified: true,
		ID:            userID,
	})
	require.NoError(t, err)

	// Log in as the non-admin user.
	userToken, _, _, err := auth.Login(context.Background(), st, "verifyuser", "userpass")
	require.NoError(t, err)

	// Request email change.
	resp, err := client.RequestEmailChange(context.Background(), authedReq(&leapmuxv1.RequestEmailChangeRequest{
		NewEmail: "pending@example.com",
	}, userToken))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetVerificationRequired())

	// The stub auto-promotes, so the email should be in the email column.
	user, err := st.Users().GetByID(context.Background(), userID)
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
	err := env.store.Users().SetPendingEmail(context.Background(), store.SetPendingEmailParams{
		PendingEmail:          "verified@example.com",
		PendingEmailToken:     verifyToken,
		PendingEmailExpiresAt: ptrTime(time.Now().Add(1 * time.Hour).UTC()),
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
	user, err := env.store.Users().GetByID(context.Background(), env.userID)
	require.NoError(t, err)
	assert.Equal(t, "verified@example.com", user.Email)
	assert.True(t, user.EmailVerified)
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
	err := env.store.Users().SetPendingEmail(context.Background(), store.SetPendingEmailParams{
		PendingEmail:          "expired@example.com",
		PendingEmailToken:     verifyToken,
		PendingEmailExpiresAt: ptrTime(time.Now().Add(-1 * time.Hour).UTC()),
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
	err := env.store.Users().SetPendingEmail(context.Background(), store.SetPendingEmailParams{
		PendingEmail:          "",
		PendingEmailToken:     verifyToken,
		PendingEmailExpiresAt: ptrTime(time.Now().Add(1 * time.Hour).UTC()),
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
	err := env.store.Users().SetPendingEmail(context.Background(), store.SetPendingEmailParams{
		PendingEmail:          "contested@example.com",
		PendingEmailToken:     verifyToken,
		PendingEmailExpiresAt: ptrTime(time.Now().Add(1 * time.Hour).UTC()),
		ID:                    env.userID,
	})
	require.NoError(t, err)

	// Create another user who claims that email in the email column.
	user2ID := id.Generate()
	hash, _ := password.Hash("testpass2")
	_ = env.store.Users().Create(context.Background(), store.CreateUserParams{
		ID:           user2ID,
		OrgID:        env.orgID,
		Username:     "claimer",
		PasswordHash: hash,
		DisplayName:  "Claimer",
		PasswordSet:  true,
		IsAdmin:      false,
	})
	err = env.store.Users().UpdateEmail(context.Background(), store.UpdateUserEmailParams{
		Email:         "contested@example.com",
		EmailVerified: true,
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

func TestVerifyEmailChange_CrossUser_Rejected(t *testing.T) {
	env := setupUserTest(t)

	// Set pending_email on the test user.
	verifyToken := id.Generate()
	err := env.store.Users().SetPendingEmail(context.Background(), store.SetPendingEmailParams{
		PendingEmail:          "stolen@example.com",
		PendingEmailToken:     verifyToken,
		PendingEmailExpiresAt: ptrTime(time.Now().Add(1 * time.Hour).UTC()),
		ID:                    env.userID,
	})
	require.NoError(t, err)

	// Create a different user and log in as them.
	attackerID := id.Generate()
	attackerHash, _ := password.Hash("testpass2")
	_ = env.store.Users().Create(context.Background(), store.CreateUserParams{
		ID:           attackerID,
		OrgID:        env.orgID,
		Username:     "attacker",
		PasswordHash: attackerHash,
		DisplayName:  "Attacker",
		PasswordSet:  true,
		IsAdmin:      false,
	})
	attackerToken, _, _, err := auth.Login(context.Background(), env.store, "attacker", "testpass2")
	require.NoError(t, err)

	// Attacker tries to verify the first user's email change token.
	_, err = env.client.VerifyEmailChange(context.Background(), authedReq(&leapmuxv1.VerifyEmailChangeRequest{
		VerificationToken: verifyToken,
	}, attackerToken))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

func TestChangePassword_InvalidatesOtherSessions(t *testing.T) {
	env := setupUserTest(t)

	// Create a second session for the same user (simulates another device).
	otherSession, _, err := auth.CreateSession(context.Background(), env.store, env.userID)
	require.NoError(t, err)

	// Verify both sessions are valid.
	_, err = auth.ValidateToken(context.Background(), env.store, env.token)
	require.NoError(t, err)
	_, err = auth.ValidateToken(context.Background(), env.store, otherSession)
	require.NoError(t, err)

	// Change password using the original session.
	_, err = env.client.ChangePassword(context.Background(), authedReq(&leapmuxv1.ChangePasswordRequest{
		CurrentPassword: "testpass",
		NewPassword:     "newpass123",
	}, env.token))
	require.NoError(t, err)

	// Original session should still be valid (it's the current session).
	_, err = auth.ValidateToken(context.Background(), env.store, env.token)
	assert.NoError(t, err)

	// The other session should be invalidated.
	_, err = auth.ValidateToken(context.Background(), env.store, otherSession)
	assert.Error(t, err, "other sessions should be invalidated after password change")
}

// --- ChangePassword tests for OAuth users ---

func TestChangePassword_OAuthUser_CanSetWithoutCurrentPassword(t *testing.T) {
	env := setupOAuthUserTest(t)

	// Should succeed with empty current password.
	_, err := env.client.ChangePassword(context.Background(), authedReq(&leapmuxv1.ChangePasswordRequest{
		CurrentPassword: "",
		NewPassword:     "newpass123",
	}, env.token))
	require.NoError(t, err)

	// Verify password_set is now 1.
	user, err := env.store.Users().GetByID(context.Background(), env.userID)
	require.NoError(t, err)
	assert.True(t, user.PasswordSet)

	// Verify the new password works via login.
	_, _, _, err = auth.Login(context.Background(), env.store, "testuser", "newpass123")
	require.NoError(t, err)
}

func TestChangePassword_PasswordUser_RequiresCurrentPassword(t *testing.T) {
	env := setupUserTest(t)

	// Attempt with empty current password — should fail.
	_, err := env.client.ChangePassword(context.Background(), authedReq(&leapmuxv1.ChangePasswordRequest{
		CurrentPassword: "",
		NewPassword:     "newpass123",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	// Attempt with wrong current password — should fail.
	_, err = env.client.ChangePassword(context.Background(), authedReq(&leapmuxv1.ChangePasswordRequest{
		CurrentPassword: "wrongpass",
		NewPassword:     "newpass123",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// --- UnlinkOAuthProvider tests ---

func TestUnlinkOAuthProvider_Success(t *testing.T) {
	env := setupUserTest(t)

	// Create two OAuth providers.
	err := env.store.OAuthProviders().Create(context.Background(), store.CreateOAuthProviderParams{
		ID: "github-1", ProviderType: "github", Name: "GitHub",
		ClientID: "c1", ClientSecret: []byte("s1"), Scopes: "read:user", Enabled: true,
	})
	require.NoError(t, err)
	err = env.store.OAuthProviders().Create(context.Background(), store.CreateOAuthProviderParams{
		ID: "google-1", ProviderType: "oidc", Name: "Google",
		ClientID: "c2", ClientSecret: []byte("s2"), Scopes: "openid", Enabled: true,
	})
	require.NoError(t, err)

	// Link both to the user.
	err = env.store.OAuthUserLinks().Create(context.Background(), store.CreateOAuthUserLinkParams{
		UserID: env.userID, ProviderID: "github-1", ProviderSubject: "gh-sub",
	})
	require.NoError(t, err)
	err = env.store.OAuthUserLinks().Create(context.Background(), store.CreateOAuthUserLinkParams{
		UserID: env.userID, ProviderID: "google-1", ProviderSubject: "g-sub",
	})
	require.NoError(t, err)

	// Unlink GitHub — should succeed (Google still linked).
	_, err = env.client.UnlinkOAuthProvider(context.Background(), authedReq(&leapmuxv1.UnlinkOAuthProviderRequest{
		ProviderId: "github-1",
	}, env.token))
	require.NoError(t, err)

	// Verify only Google link remains.
	links, err := env.store.OAuthUserLinks().ListByUser(context.Background(), env.userID)
	require.NoError(t, err)
	assert.Len(t, links, 1)
	assert.Equal(t, "google-1", links[0].ProviderID)
}

func TestUnlinkOAuthProvider_LastLink_WithPassword(t *testing.T) {
	env := setupUserTest(t)

	// User has password_set = 1 (default from setupUserTest).
	err := env.store.OAuthProviders().Create(context.Background(), store.CreateOAuthProviderParams{
		ID: "github-2", ProviderType: "github", Name: "GitHub",
		ClientID: "c1", ClientSecret: []byte("s1"), Scopes: "read:user", Enabled: true,
	})
	require.NoError(t, err)
	err = env.store.OAuthUserLinks().Create(context.Background(), store.CreateOAuthUserLinkParams{
		UserID: env.userID, ProviderID: "github-2", ProviderSubject: "gh-sub",
	})
	require.NoError(t, err)

	// Should succeed because user has a password.
	_, err = env.client.UnlinkOAuthProvider(context.Background(), authedReq(&leapmuxv1.UnlinkOAuthProviderRequest{
		ProviderId: "github-2",
	}, env.token))
	require.NoError(t, err)

	links, err := env.store.OAuthUserLinks().ListByUser(context.Background(), env.userID)
	require.NoError(t, err)
	assert.Empty(t, links)
}

func TestUnlinkOAuthProvider_LastLink_NoPassword_Blocked(t *testing.T) {
	env := setupOAuthUserTest(t)

	err := env.store.OAuthProviders().Create(context.Background(), store.CreateOAuthProviderParams{
		ID: "github-3", ProviderType: "github", Name: "GitHub",
		ClientID: "c1", ClientSecret: []byte("s1"), Scopes: "read:user", Enabled: true,
	})
	require.NoError(t, err)
	err = env.store.OAuthUserLinks().Create(context.Background(), store.CreateOAuthUserLinkParams{
		UserID: env.userID, ProviderID: "github-3", ProviderSubject: "gh-sub",
	})
	require.NoError(t, err)

	// Should be blocked — last link and no password.
	_, err = env.client.UnlinkOAuthProvider(context.Background(), authedReq(&leapmuxv1.UnlinkOAuthProviderRequest{
		ProviderId: "github-3",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "set a password first")

	// Link should still exist.
	links, err := env.store.OAuthUserLinks().ListByUser(context.Background(), env.userID)
	require.NoError(t, err)
	assert.Len(t, links, 1)
}

func TestUnlinkOAuthProvider_NotFound(t *testing.T) {
	env := setupUserTest(t)

	_, err := env.client.UnlinkOAuthProvider(context.Background(), authedReq(&leapmuxv1.UnlinkOAuthProviderRequest{
		ProviderId: "nonexistent",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}
