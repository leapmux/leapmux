package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/bootstrap"
	"github.com/leapmux/leapmux/internal/hub/db"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/id"
	"github.com/leapmux/leapmux/internal/hub/service"
)

type adminTestEnv struct {
	client  leapmuxv1connect.AdminServiceClient
	queries *gendb.Queries
	token   string
	userID  string
}

func setupAdminTestServer(t *testing.T) *adminTestEnv {
	t.Helper()

	sqlDB, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	err = db.Migrate(sqlDB)
	require.NoError(t, err)

	q := gendb.New(sqlDB)

	err = bootstrap.Run(context.Background(), q)
	require.NoError(t, err)

	adminSvc := service.NewAdminService(q)

	mux := http.NewServeMux()
	opts := connect.WithInterceptors(auth.NewInterceptor(q))
	path, handler := leapmuxv1connect.NewAdminServiceHandler(adminSvc, opts)
	mux.Handle(path, handler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewAdminServiceClient(server.Client(), server.URL)

	token, user, err := auth.Login(context.Background(), q, "admin", "admin")
	require.NoError(t, err)

	return &adminTestEnv{
		client:  client,
		queries: q,
		token:   token,
		userID:  user.ID,
	}
}

func (e *adminTestEnv) createNonAdminUser(t *testing.T) (userID, token string) {
	t.Helper()
	ctx := context.Background()

	adminUser, err := e.queries.GetUserByUsername(ctx, "admin")
	require.NoError(t, err)

	userID = id.Generate()
	hash, _ := bcrypt.GenerateFromPassword([]byte("userpass"), bcrypt.MinCost)
	_ = e.queries.CreateUser(ctx, gendb.CreateUserParams{
		ID:           userID,
		OrgID:        adminUser.OrgID,
		Username:     "regularuser",
		PasswordHash: string(hash),
		DisplayName:  "Regular User",
		IsAdmin:      0,
	})
	_ = e.queries.CreateOrgMember(ctx, gendb.CreateOrgMemberParams{
		OrgID:  adminUser.OrgID,
		UserID: userID,
		Role:   leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER,
	})
	token, _, loginErr := auth.Login(ctx, e.queries, "regularuser", "userpass")
	require.NoError(t, loginErr)
	return
}

// --- GetSettings ---

func TestAdminService_GetSettings(t *testing.T) {
	env := setupAdminTestServer(t)

	resp, err := env.client.GetSettings(context.Background(), authedReq(&leapmuxv1.GetSettingsRequest{}, env.token))
	require.NoError(t, err)

	settings := resp.Msg.GetSettings()
	assert.NotNil(t, settings)
	// Default settings: signup disabled, email verification not required.
	assert.False(t, settings.GetSignupEnabled())
	assert.False(t, settings.GetEmailVerificationRequired())
}

func TestAdminService_GetSettings_NonAdmin(t *testing.T) {
	env := setupAdminTestServer(t)
	_, nonAdminToken := env.createNonAdminUser(t)

	_, err := env.client.GetSettings(context.Background(), authedReq(&leapmuxv1.GetSettingsRequest{}, nonAdminToken))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// --- UpdateSettings ---

func TestAdminService_UpdateSettings(t *testing.T) {
	env := setupAdminTestServer(t)

	// Enable signup.
	resp, err := env.client.UpdateSettings(context.Background(), authedReq(&leapmuxv1.UpdateSettingsRequest{
		Settings: &leapmuxv1.SystemSettings{
			SignupEnabled:             true,
			EmailVerificationRequired: true,
			Smtp: &leapmuxv1.SmtpConfig{
				Host:        "smtp.example.com",
				Port:        587,
				Username:    "mailuser",
				Password:    "mailpass",
				FromAddress: "noreply@example.com",
				UseTls:      true,
			},
		},
	}, env.token))
	require.NoError(t, err)

	settings := resp.Msg.GetSettings()
	assert.True(t, settings.GetSignupEnabled())
	assert.True(t, settings.GetEmailVerificationRequired())
	assert.Equal(t, "smtp.example.com", settings.GetSmtp().GetHost())
	assert.Equal(t, int32(587), settings.GetSmtp().GetPort())
	assert.Equal(t, "mailuser", settings.GetSmtp().GetUsername())
	assert.True(t, settings.GetSmtp().GetPasswordSet())
	assert.Equal(t, "noreply@example.com", settings.GetSmtp().GetFromAddress())
	assert.True(t, settings.GetSmtp().GetUseTls())

	// Verify via GetSettings.
	getResp, err := env.client.GetSettings(context.Background(), authedReq(&leapmuxv1.GetSettingsRequest{}, env.token))
	require.NoError(t, err)
	assert.True(t, getResp.Msg.GetSettings().GetSignupEnabled())
	assert.True(t, getResp.Msg.GetSettings().GetSmtp().GetPasswordSet())
}

func TestAdminService_UpdateSettings_PreservesSmtpPassword(t *testing.T) {
	env := setupAdminTestServer(t)

	// Set an SMTP password first.
	_, err := env.client.UpdateSettings(context.Background(), authedReq(&leapmuxv1.UpdateSettingsRequest{
		Settings: &leapmuxv1.SystemSettings{
			Smtp: &leapmuxv1.SmtpConfig{
				Host:     "smtp.example.com",
				Password: "secretpass",
			},
		},
	}, env.token))
	require.NoError(t, err)

	// Update settings without providing a password (empty string).
	resp, err := env.client.UpdateSettings(context.Background(), authedReq(&leapmuxv1.UpdateSettingsRequest{
		Settings: &leapmuxv1.SystemSettings{
			SignupEnabled: true,
			Smtp: &leapmuxv1.SmtpConfig{
				Host: "smtp.example.com",
				// Password intentionally omitted (empty).
			},
		},
	}, env.token))
	require.NoError(t, err)

	// The password should still be set (preserved from the previous update).
	assert.True(t, resp.Msg.GetSettings().GetSmtp().GetPasswordSet())

	// Verify the actual password in the database was preserved.
	dbSettings, err := env.queries.GetSystemSettings(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "secretpass", dbSettings.SmtpPassword)
}

// --- ListUsers ---

func TestAdminService_ListUsers(t *testing.T) {
	env := setupAdminTestServer(t)

	resp, err := env.client.ListUsers(context.Background(), authedReq(&leapmuxv1.ListUsersRequest{}, env.token))
	require.NoError(t, err)

	// Should have at least the bootstrap admin user.
	require.GreaterOrEqual(t, len(resp.Msg.GetUsers()), 1)

	found := false
	for _, u := range resp.Msg.GetUsers() {
		if u.GetUsername() == "admin" {
			found = true
			assert.True(t, u.GetIsAdmin())
		}
	}
	assert.True(t, found, "admin user should be in the list")
}

func TestAdminService_ListUsers_WithQuery(t *testing.T) {
	env := setupAdminTestServer(t)

	// Create an additional user via the admin RPC.
	_, err := env.client.CreateUser(context.Background(), authedReq(&leapmuxv1.CreateUserRequest{
		Username:    "searchable",
		Password:    "pass123",
		DisplayName: "Searchable User",
		Email:       "search@example.com",
	}, env.token))
	require.NoError(t, err)

	// Search for the user by username.
	resp, err := env.client.ListUsers(context.Background(), authedReq(&leapmuxv1.ListUsersRequest{
		Query: "searchable",
	}, env.token))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetUsers(), 1)
	assert.Equal(t, "searchable", resp.Msg.GetUsers()[0].GetUsername())

	// Search with a query that matches no one.
	resp, err = env.client.ListUsers(context.Background(), authedReq(&leapmuxv1.ListUsersRequest{
		Query: "nonexistentuser",
	}, env.token))
	require.NoError(t, err)
	assert.Empty(t, resp.Msg.GetUsers())
}

// --- GetUser ---

func TestAdminService_GetUser(t *testing.T) {
	env := setupAdminTestServer(t)

	resp, err := env.client.GetUser(context.Background(), authedReq(&leapmuxv1.GetUserRequest{
		UserId: env.userID,
	}, env.token))
	require.NoError(t, err)

	user := resp.Msg.GetUser()
	assert.Equal(t, env.userID, user.GetId())
	assert.Equal(t, "admin", user.GetUsername())
	assert.True(t, user.GetIsAdmin())
	assert.NotEmpty(t, user.GetOrgId())
	assert.NotEmpty(t, user.GetCreatedAt())
}

func TestAdminService_GetUser_NotFound(t *testing.T) {
	env := setupAdminTestServer(t)

	_, err := env.client.GetUser(context.Background(), authedReq(&leapmuxv1.GetUserRequest{
		UserId: "nonexistent-user-id",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// --- CreateUser ---

func TestAdminService_CreateUser(t *testing.T) {
	env := setupAdminTestServer(t)

	resp, err := env.client.CreateUser(context.Background(), authedReq(&leapmuxv1.CreateUserRequest{
		Username:    "newuser",
		Password:    "newpass123",
		DisplayName: "New User",
		Email:       "new@example.com",
	}, env.token))
	require.NoError(t, err)

	user := resp.Msg.GetUser()
	assert.Equal(t, "newuser", user.GetUsername())
	assert.Equal(t, "New User", user.GetDisplayName())
	assert.Equal(t, "new@example.com", user.GetEmail())
	assert.False(t, user.GetIsAdmin())
	assert.NotEmpty(t, user.GetId())
	assert.NotEmpty(t, user.GetOrgId())
	assert.Equal(t, "newuser", user.GetOrgName())
	assert.NotEmpty(t, user.GetCreatedAt())

	// Verify the user can log in.
	_, _, loginErr := auth.Login(context.Background(), env.queries, "newuser", "newpass123")
	assert.NoError(t, loginErr)
}

func TestAdminService_CreateUser_AdminFlag(t *testing.T) {
	env := setupAdminTestServer(t)

	resp, err := env.client.CreateUser(context.Background(), authedReq(&leapmuxv1.CreateUserRequest{
		Username:    "adminuser",
		Password:    "adminpass",
		DisplayName: "Admin User",
		IsAdmin:     true,
	}, env.token))
	require.NoError(t, err)

	user := resp.Msg.GetUser()
	assert.Equal(t, "adminuser", user.GetUsername())
	assert.True(t, user.GetIsAdmin())

	// Verify in the database.
	dbUser, err := env.queries.GetUserByID(context.Background(), user.GetId())
	require.NoError(t, err)
	assert.Equal(t, int64(1), dbUser.IsAdmin)
}

// --- UpdateUser ---

func TestAdminService_UpdateUser(t *testing.T) {
	env := setupAdminTestServer(t)

	// Create a user to update.
	createResp, err := env.client.CreateUser(context.Background(), authedReq(&leapmuxv1.CreateUserRequest{
		Username:    "toupdate",
		Password:    "pass",
		DisplayName: "Original Name",
		Email:       "orig@example.com",
	}, env.token))
	require.NoError(t, err)
	targetID := createResp.Msg.GetUser().GetId()

	// Update the user.
	resp, err := env.client.UpdateUser(context.Background(), authedReq(&leapmuxv1.UpdateUserRequest{
		UserId:      targetID,
		DisplayName: "Updated Name",
		Email:       "updated@example.com",
		IsAdmin:     false,
	}, env.token))
	require.NoError(t, err)

	user := resp.Msg.GetUser()
	assert.Equal(t, "Updated Name", user.GetDisplayName())
	assert.Equal(t, "updated@example.com", user.GetEmail())
	assert.False(t, user.GetIsAdmin())
	// Username should be preserved.
	assert.Equal(t, "toupdate", user.GetUsername())
}

func TestAdminService_UpdateUser_CannotRemoveOwnAdmin(t *testing.T) {
	env := setupAdminTestServer(t)

	// The admin user tries to remove their own admin privileges.
	_, err := env.client.UpdateUser(context.Background(), authedReq(&leapmuxv1.UpdateUserRequest{
		UserId:      env.userID,
		DisplayName: "Admin",
		IsAdmin:     false,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

// --- DeleteUser ---

func TestAdminService_DeleteUser(t *testing.T) {
	env := setupAdminTestServer(t)

	// Create a user to delete.
	createResp, err := env.client.CreateUser(context.Background(), authedReq(&leapmuxv1.CreateUserRequest{
		Username:    "todelete",
		Password:    "pass",
		DisplayName: "Delete Me",
	}, env.token))
	require.NoError(t, err)
	targetID := createResp.Msg.GetUser().GetId()

	// Delete the user.
	_, err = env.client.DeleteUser(context.Background(), authedReq(&leapmuxv1.DeleteUserRequest{
		UserId: targetID,
	}, env.token))
	require.NoError(t, err)

	// Verify the user is gone.
	_, err = env.client.GetUser(context.Background(), authedReq(&leapmuxv1.GetUserRequest{
		UserId: targetID,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestAdminService_DeleteUser_CannotDeleteSelf(t *testing.T) {
	env := setupAdminTestServer(t)

	_, err := env.client.DeleteUser(context.Background(), authedReq(&leapmuxv1.DeleteUserRequest{
		UserId: env.userID,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestAdminService_DeleteUser_NotFound(t *testing.T) {
	env := setupAdminTestServer(t)

	_, err := env.client.DeleteUser(context.Background(), authedReq(&leapmuxv1.DeleteUserRequest{
		UserId: "nonexistent-user-id",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// --- ResetUserPassword ---

func TestAdminService_ResetUserPassword(t *testing.T) {
	env := setupAdminTestServer(t)

	// Create a user whose password we will reset.
	createResp, err := env.client.CreateUser(context.Background(), authedReq(&leapmuxv1.CreateUserRequest{
		Username:    "resetme",
		Password:    "oldpass",
		DisplayName: "Reset Me",
	}, env.token))
	require.NoError(t, err)
	targetID := createResp.Msg.GetUser().GetId()

	// Verify old password works.
	_, _, err = auth.Login(context.Background(), env.queries, "resetme", "oldpass")
	require.NoError(t, err)

	// Reset the password.
	_, err = env.client.ResetUserPassword(context.Background(), authedReq(&leapmuxv1.ResetUserPasswordRequest{
		UserId:      targetID,
		NewPassword: "newpass456",
	}, env.token))
	require.NoError(t, err)

	// Verify old password no longer works.
	_, _, err = auth.Login(context.Background(), env.queries, "resetme", "oldpass")
	require.Error(t, err)

	// Verify new password works.
	_, _, err = auth.Login(context.Background(), env.queries, "resetme", "newpass456")
	assert.NoError(t, err)
}

func TestAdminService_NonAdmin_AllEndpoints(t *testing.T) {
	env := setupAdminTestServer(t)
	_, nonAdminToken := env.createNonAdminUser(t)

	tests := []struct {
		name string
		fn   func() error
	}{
		{"ListUsers", func() error {
			_, err := env.client.ListUsers(context.Background(), authedReq(&leapmuxv1.ListUsersRequest{}, nonAdminToken))
			return err
		}},
		{"GetUser", func() error {
			_, err := env.client.GetUser(context.Background(), authedReq(&leapmuxv1.GetUserRequest{UserId: env.userID}, nonAdminToken))
			return err
		}},
		{"CreateUser", func() error {
			_, err := env.client.CreateUser(context.Background(), authedReq(&leapmuxv1.CreateUserRequest{
				Username: "blocked", Password: "pass", DisplayName: "Blocked",
			}, nonAdminToken))
			return err
		}},
		{"DeleteUser", func() error {
			_, err := env.client.DeleteUser(context.Background(), authedReq(&leapmuxv1.DeleteUserRequest{UserId: env.userID}, nonAdminToken))
			return err
		}},
		{"UpdateSettings", func() error {
			_, err := env.client.UpdateSettings(context.Background(), authedReq(&leapmuxv1.UpdateSettingsRequest{
				Settings: &leapmuxv1.SystemSettings{SignupEnabled: true},
			}, nonAdminToken))
			return err
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.fn()
			require.Error(t, err)
			assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err), "non-admin should get NotFound, not PermissionDenied")
		})
	}
}
