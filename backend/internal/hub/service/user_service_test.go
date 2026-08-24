package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/userid"

	"connectrpc.com/connect"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/mail"
	hubwebauthn "github.com/leapmux/leapmux/internal/hub/webauthn"

	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/servicetest"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/usersettings"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/verifycode"
)

type userTestEnv struct {
	client leapmuxv1connect.UserServiceClient
	store  store.Store
	token  string
	userID string
}

func setupUserTest(t *testing.T) *userTestEnv {
	t.Helper()

	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	err = st.Migrator().Migrate(context.Background())
	require.NoError(t, err)

	mux := http.NewServeMux()
	interceptor, contexts := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st})
	t.Cleanup(contexts.Stop)
	ksKey, kErr := keystore.GenerateKey()
	require.NoError(t, kErr)
	ks, kErr := keystore.New(map[uint32][32]byte{1: ksKey})
	require.NoError(t, kErr)
	set := servicetest.NewSettingsManager(t, st, nil)
	// Passkeys need a hub URL; ListPasskeys reports availability through
	// the same derivation the handlers use.
	require.NoError(t, settings.KeyPublicURL.Set(context.Background(), set, "http://localhost:4327"))
	userSvc := service.NewUserService(st, testConfig(), set, auth.NewCredentialLifecycleEffects(contexts, nil, nil), mail.NewStubSender(), mail.Renderer{}, ks)
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

	userID := id.Generate()
	hash, _ := password.Hash("testpass")

	_ = st.Users().Create(context.Background(), store.CreateUserParams{
		ID:           userID,
		Username:     "testuser",
		PasswordHash: hash,
		DisplayName:  "Test User",
		PasswordSet:  true,
		IsAdmin:      true,
	})

	token, _, _, err := auth.Login(context.Background(), st, "testuser", "testpass", auth.DefaultSessionDuration)
	require.NoError(t, err)

	return &userTestEnv{
		client: client,
		store:  st,
		token:  token,
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

	userSvc := service.NewUserService(st, testConfig(), servicetest.NewSettingsManager(t, st, nil), auth.NewCredentialLifecycleEffects(nil, nil, nil), mail.NewStubSender(), mail.Renderer{}, nil)

	mux := http.NewServeMux()
	interceptor, contexts := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st})
	t.Cleanup(contexts.Stop)
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

	userID := id.Generate()

	_ = st.Users().Create(context.Background(), store.CreateUserParams{
		ID:            userID,
		Username:      "testuser",
		PasswordHash:  password.PlaceholderHash,
		DisplayName:   "Test User",
		Email:         "testuser@example.com",
		EmailVerified: true,
		PasswordSet:   false,
		IsAdmin:       true,
	})

	// PasswordSet is false, so password Login refuses. Mint a session directly.
	token, _, err := auth.CreateSession(context.Background(), st, userid.MustNew(userID), auth.DefaultSessionDuration)
	require.NoError(t, err)

	return &userTestEnv{
		client: client,
		store:  st,
		token:  token,
		userID: userID,
	}
}

func TestUserService_UpdateProfile(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)

	resp, err := env.client.UpdateProfile(context.Background(), authedReq(&leapmuxv1.UpdateProfileRequest{
		Username:    "newname",
		DisplayName: "New Display",
	}, env.token))
	require.NoError(t, err)

	assert.Equal(t, "newname", resp.Msg.GetUsername())
	assert.Equal(t, "New Display", resp.Msg.GetDisplayName())

	// Verify the database was actually updated.
	user, err := env.store.Users().GetByID(context.Background(), env.userID)
	require.NoError(t, err)
	assert.Equal(t, "newname", user.Username)
}

func TestUserService_UpdateProfile_SameUsername(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)

	_, err := env.client.UpdateProfile(context.Background(), authedReq(&leapmuxv1.UpdateProfileRequest{
		Username:    "testuser",
		DisplayName: "Updated Display",
	}, env.token))
	require.NoError(t, err)

	// The display-name update must still persist...
	user, err := env.store.Users().GetByID(context.Background(), env.userID)
	require.NoError(t, err)
	assert.Equal(t, "Updated Display", user.DisplayName)

	// ...but a display-name-only edit changes no cached UserInfo field
	// (username is the only one this mutation touches), so it must emit no
	// fleet-wide user_info cache-invalidation event.
	published, err := env.store.RevocationEvents().PublishPending(context.Background(), 10)
	require.NoError(t, err)
	assert.Zero(t, published, "display-name-only profile edit must not emit a user_info event")
}

func TestUserService_UpdateProfile_DuplicateUsername(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)

	// Create a second user.
	user2ID := id.Generate()
	hash, _ := password.Hash("testpass2")
	_ = env.store.Users().Create(context.Background(), store.CreateUserParams{
		ID:           user2ID,
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
	t.Parallel()

	env := setupUserTest(t)

	_, err := env.client.ChangePassword(context.Background(), authedReq(&leapmuxv1.ChangePasswordRequest{
		CurrentPassword: "testpass",
		NewPassword:     "newpass123",
	}, env.token))
	require.NoError(t, err)

	// Verify login works with new password.
	_, _, _, err = auth.Login(context.Background(), env.store, "testuser", "newpass123", auth.DefaultSessionDuration)
	assert.NoError(t, err)

	// Verify login with old password fails.
	_, _, _, err = auth.Login(context.Background(), env.store, "testuser", "testpass", auth.DefaultSessionDuration)
	require.Error(t, err)
}

func TestUserService_ChangePassword_WrongCurrent(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)

	_, err := env.client.ChangePassword(context.Background(), authedReq(&leapmuxv1.ChangePasswordRequest{
		CurrentPassword: "wrongpassword",
		NewPassword:     "newpass123",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestUserService_ListUserSettings_Default(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)

	resp, err := env.client.ListUserSettings(context.Background(), authedReq(&leapmuxv1.ListUserSettingsRequest{}, env.token))
	require.NoError(t, err)

	require.NotEmpty(t, resp.Msg.GetDescriptors(), "every declared account setting has a descriptor")
	byKey := map[string]*leapmuxv1.SettingValue{}
	for _, v := range resp.Msg.GetValues() {
		byKey[v.GetKey()] = v
	}
	for _, d := range resp.Msg.GetDescriptors() {
		require.Contains(t, byKey, d.GetKey())
		assert.False(t, byKey[d.GetKey()].GetCustomized(), "a fresh user has no stored value for %q", d.GetKey())
	}
	// The effective value of an absent key is the decoded default.
	assert.JSONEq(t, `{"name":"default","mode":"system"}`, byKey["theme"].GetEffectiveJson())
	assert.JSONEq(t, `{"enabled":false}`, byKey["ui_fonts"].GetEffectiveJson())
}

func TestUserService_UpdateUserSetting_PerKeyMerge(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)

	_, err := env.client.UpdateUserSetting(context.Background(), authedReq(&leapmuxv1.UpdateUserSettingRequest{
		Key:         "theme",
		PartialJson: `{"name":"nord","mode":"dark"}`,
	}, env.token))
	require.NoError(t, err)

	// A second update touching a DIFFERENT key must leave the first
	// untouched — the whole-blob clobber the per-key merge replaced.
	_, err = env.client.UpdateUserSetting(context.Background(), authedReq(&leapmuxv1.UpdateUserSettingRequest{
		Key:         "ui_fonts",
		PartialJson: `{"enabled":true,"fonts":["Inter","Roboto"]}`,
	}, env.token))
	require.NoError(t, err)

	resp, err := env.client.ListUserSettings(context.Background(), authedReq(&leapmuxv1.ListUserSettingsRequest{}, env.token))
	require.NoError(t, err)
	byKey := map[string]*leapmuxv1.SettingValue{}
	for _, v := range resp.Msg.GetValues() {
		byKey[v.GetKey()] = v
	}
	assert.True(t, byKey["theme"].GetCustomized())
	assert.JSONEq(t, `{"name":"nord","mode":"dark"}`, byKey["theme"].GetEffectiveJson())
	assert.True(t, byKey["ui_fonts"].GetCustomized())
	assert.JSONEq(t, `{"enabled":true,"fonts":["Inter","Roboto"]}`, byKey["ui_fonts"].GetEffectiveJson())
}

func TestUserService_UpdateUserSetting_PartialMergeWithinKey(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)

	_, err := env.client.UpdateUserSetting(context.Background(), authedReq(&leapmuxv1.UpdateUserSettingRequest{
		Key:         "ui_fonts",
		PartialJson: `{"enabled":true,"fonts":["Hack NF"]}`,
	}, env.token))
	require.NoError(t, err)

	// A partial document merges onto the current value: omitting fonts
	// keeps the stored stack.
	resp, err := env.client.UpdateUserSetting(context.Background(), authedReq(&leapmuxv1.UpdateUserSettingRequest{
		Key:         "ui_fonts",
		PartialJson: `{"enabled":false}`,
	}, env.token))
	require.NoError(t, err)
	assert.JSONEq(t, `{"enabled":false,"fonts":["Hack NF"]}`, resp.Msg.GetValue().GetEffectiveJson())

	// An unknown field name is refused, not silently ignored.
	_, err = env.client.UpdateUserSetting(context.Background(), authedReq(&leapmuxv1.UpdateUserSettingRequest{
		Key:         "ui_fonts",
		PartialJson: `{"bogus":true}`,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestUserService_UpdateUserSetting_InvalidValueStoresNothing(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)

	_, err := env.client.UpdateUserSetting(context.Background(), authedReq(&leapmuxv1.UpdateUserSettingRequest{
		Key:         "ui_fonts",
		PartialJson: `{"enabled":true,"fonts":["  "]}`,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = env.client.UpdateUserSetting(context.Background(), authedReq(&leapmuxv1.UpdateUserSettingRequest{
		Key:         "nope",
		PartialJson: `"x"`,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	resp, err := env.client.ListUserSettings(context.Background(), authedReq(&leapmuxv1.ListUserSettingsRequest{}, env.token))
	require.NoError(t, err)
	for _, v := range resp.Msg.GetValues() {
		assert.False(t, v.GetCustomized(), "a rejected write stores nothing")
	}
}

func TestUserService_UpdateUserSetting_KeybindingCap(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)

	entries := make([]map[string]any, usersettings.MaxKeybindings+1)
	for i := range entries {
		entries[i] = map[string]any{"key": "ctrl+k", "command": fmt.Sprintf("cmd.%d", i)}
	}
	blob, err := json.Marshal(entries)
	require.NoError(t, err)

	_, err = env.client.UpdateUserSetting(context.Background(), authedReq(&leapmuxv1.UpdateUserSettingRequest{
		Key:         "keybindings",
		PartialJson: string(blob),
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	entries = entries[:usersettings.MaxKeybindings]
	blob, err = json.Marshal(entries)
	require.NoError(t, err)
	_, err = env.client.UpdateUserSetting(context.Background(), authedReq(&leapmuxv1.UpdateUserSettingRequest{
		Key:         "keybindings",
		PartialJson: string(blob),
	}, env.token))
	require.NoError(t, err)
}

func TestUserService_UpdateUserSetting_EmptyKeyAndMalformedBlob(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)

	_, err := env.client.UpdateUserSetting(context.Background(), authedReq(&leapmuxv1.UpdateUserSettingRequest{
		PartialJson: `"dark"`,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "key is required")

	_, err = env.client.ResetUserSetting(context.Background(), authedReq(&leapmuxv1.ResetUserSettingRequest{}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "key is required")

	require.NoError(t, env.store.Users().UpdatePrefs(context.Background(), store.UpdateUserPrefsParams{
		ID:    env.userID,
		Prefs: `[1,2,3]`,
	}))
	_, err = env.client.UpdateUserSetting(context.Background(), authedReq(&leapmuxv1.UpdateUserSettingRequest{
		Key:         "theme",
		PartialJson: `{"name":"nord","mode":"dark"}`,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "malformed")
}

func TestUserService_ResetUserSetting(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)

	_, err := env.client.UpdateUserSetting(context.Background(), authedReq(&leapmuxv1.UpdateUserSettingRequest{
		Key:         "turn_end_sound_volume",
		PartialJson: `42`,
	}, env.token))
	require.NoError(t, err)

	resp, err := env.client.ResetUserSetting(context.Background(), authedReq(&leapmuxv1.ResetUserSettingRequest{
		Key: "turn_end_sound_volume",
	}, env.token))
	require.NoError(t, err)
	assert.False(t, resp.Msg.GetValue().GetCustomized())
	assert.JSONEq(t, `100`, resp.Msg.GetValue().GetEffectiveJson())
}

func TestUserService_UserSettings_Unauthenticated(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)

	_, err := env.client.ListUserSettings(context.Background(), connect.NewRequest(&leapmuxv1.ListUserSettingsRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestRequestEmailChange_Success(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

	env := setupUserTest(t)

	_, err := env.client.RequestEmailChange(context.Background(), authedReq(&leapmuxv1.RequestEmailChangeRequest{
		NewEmail: "",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestRequestEmailChange_SameEmail_Rejected(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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

// --- RequestEmailChange: non-admin, verification off, immediate unverified change ---

// TestRequestEmailChange_NonAdmin_VerificationNotRequired_LandsUnverified covers
// the non-admin arm of the collapsed immediate-change branch: with verification
// off, the change applies immediately but the new address must land UNVERIFIED
// (verified == userInfo.IsAdmin == false), unlike the admin arm which trusts it.
func TestRequestEmailChange_NonAdmin_VerificationNotRequired_LandsUnverified(t *testing.T) {
	t.Parallel()

	client, st, _ := setupVerificationUserTestServer(t, false)

	userID := id.Generate()
	hash, _ := password.Hash("userpass")
	require.NoError(t, st.Users().Create(context.Background(), store.CreateUserParams{
		ID:           userID,
		Username:     "plainuser",
		PasswordHash: hash,
		DisplayName:  "Plain User",
		PasswordSet:  true,
		IsAdmin:      false,
	}))
	// Start from a verified address so the interceptor doesn't gate the call.
	require.NoError(t, st.Users().UpdateEmail(context.Background(), store.UpdateUserEmailParams{
		Email:         "old@example.com",
		EmailVerified: true,
		ID:            userID,
	}))

	userToken, _, _, err := auth.Login(context.Background(), st, "plainuser", "userpass", auth.DefaultSessionDuration)
	require.NoError(t, err)

	resp, err := client.RequestEmailChange(context.Background(), authedReq(&leapmuxv1.RequestEmailChangeRequest{
		NewEmail: "new@example.com",
	}, userToken))
	require.NoError(t, err)
	assert.False(t, resp.Msg.GetVerificationRequired())

	user, err := st.Users().GetByID(context.Background(), userID)
	require.NoError(t, err)
	assert.Equal(t, "new@example.com", user.Email, "the change applies immediately when verification is off")
	assert.False(t, user.EmailVerified, "a non-admin immediate change must land unverified (verified == IsAdmin)")
}

// --- RequestEmailChange: duplicate email rejected ---

// RequestEmailChange had no cooldown at all: the address is
// caller-supplied and nothing rate-limits the procedure, so a logged-in
// user could send one verification message per request to any address
// they named. The conditional mint is what closes that -- it refuses
// while a previous code is live, whatever address the second request
// asks for.
func TestRequestEmailChange_CooldownRefusesASecondAddress(t *testing.T) {
	t.Parallel()

	client, st, _ := setupVerificationUserTestServer(t, true)

	userID := id.Generate()
	hash, _ := password.Hash("userpass")
	require.NoError(t, st.Users().Create(context.Background(), store.CreateUserParams{
		ID:           userID,
		Username:     "flooduser",
		PasswordHash: hash,
		DisplayName:  "Flood User",
		PasswordSet:  true,
	}))
	require.NoError(t, st.Users().UpdateEmail(context.Background(), store.UpdateUserEmailParams{
		Email: "old@example.com", EmailVerified: true, ID: userID,
	}))
	userToken, _, _, err := auth.Login(context.Background(), st, "flooduser", "userpass", auth.DefaultSessionDuration)
	require.NoError(t, err)

	_, err = client.RequestEmailChange(context.Background(), authedReq(&leapmuxv1.RequestEmailChangeRequest{
		NewEmail: "first@example.com",
	}, userToken))
	require.NoError(t, err)

	// A DIFFERENT address on the second request: the old read-then-check
	// would have minted and sent again, because it compared nothing.
	_, err = client.RequestEmailChange(context.Background(), authedReq(&leapmuxv1.RequestEmailChangeRequest{
		NewEmail: "victim@example.com",
	}, userToken))
	require.Error(t, err)
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err),
		"a second address inside the cooldown must be refused, not sent")

	// The first request's pending row is intact: the refusal changed nothing.
	user, err := st.Users().GetByID(context.Background(), userID)
	require.NoError(t, err)
	assert.Equal(t, "first@example.com", user.PendingEmail,
		"the refused mint must not overwrite the live code's address")
}

func TestRequestEmailChange_DuplicateEmail_Rejected(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)

	// Create a second user with an email.
	user2ID := id.Generate()
	hash, _ := password.Hash("testpass2")
	_ = env.store.Users().Create(context.Background(), store.CreateUserParams{
		ID:           user2ID,
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

// setupVerificationUserTestServer creates a test server with the given
// Email verification is controlled by SMTP (EmailVerificationEffective). Both UserService and AuthService
// registered. The interceptor and the services read the rule from the
// same settings. It returns a UserService client, st, and the admin token.
func setupVerificationUserTestServer(t *testing.T, emailVerificationRequired bool) (leapmuxv1connect.UserServiceClient, store.Store, string) {
	t.Helper()

	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	err = st.Migrator().Migrate(context.Background())
	require.NoError(t, err)

	hubtestutil.CreateTestAdmin(t, st)

	set := servicetest.NewSettingsManager(t, st, nil)
	if emailVerificationRequired {
		enableEmailVerification(t, set)
	}

	mux := http.NewServeMux()
	interceptor, contexts := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st, Policy: servicetest.AuthPolicy(set)})
	t.Cleanup(contexts.Stop)
	opts := connect.WithInterceptors(interceptor)

	userSvc := service.NewUserService(st, testConfig(), set, auth.NewCredentialLifecycleEffects(nil, nil, nil), mail.NewStubSender(), mail.Renderer{}, nil)
	userPath, userHandler := leapmuxv1connect.NewUserServiceHandler(userSvc, opts)
	mux.Handle(userPath, userHandler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewUserServiceClient(server.Client(), server.URL)

	// Log in as admin (bootstrap user).
	token, _, _, err := auth.Login(context.Background(), st, "admin", "admin123", auth.DefaultSessionDuration)
	require.NoError(t, err)

	return client, st, token
}

func TestRequestEmailChange_ConfigOn_PendingEmail(t *testing.T) {
	t.Parallel()

	client, st, adminToken := setupVerificationUserTestServer(t, true)

	// Create a non-admin user.
	userID := id.Generate()
	hash, _ := password.Hash("userpass")
	err := st.Users().Create(context.Background(), store.CreateUserParams{
		ID:           userID,
		Username:     "verifyuser",
		PasswordHash: hash,
		DisplayName:  "Verify User",
		PasswordSet:  true,
		IsAdmin:      false,
	})
	require.NoError(t, err)

	// Set email_verified=1 so the verification interceptor does not refuse the user.
	err = st.Users().UpdateEmail(context.Background(), store.UpdateUserEmailParams{
		Email:         "old@example.com",
		EmailVerified: true,
		ID:            userID,
	})
	require.NoError(t, err)

	// Log in as the non-admin user.
	userToken, _, _, err := auth.Login(context.Background(), st, "verifyuser", "userpass", auth.DefaultSessionDuration)
	require.NoError(t, err)

	// Request email change.
	resp, err := client.RequestEmailChange(context.Background(), authedReq(&leapmuxv1.RequestEmailChangeRequest{
		NewEmail: "pending@example.com",
	}, userToken))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetVerificationRequired())

	// New flow: the email column stays pinned to the existing verified
	// address until the user submits the code. The new email lands in
	// pending_email along with a 6-char token and a 30-minute expiry.
	user, err := st.Users().GetByID(context.Background(), userID)
	require.NoError(t, err)
	assert.Equal(t, "old@example.com", user.Email)
	assert.True(t, user.EmailVerified)
	assert.Equal(t, "pending@example.com", user.PendingEmail)
	assert.Equal(t, verifycode.Length, len(user.PendingEmailToken))
	require.NotNil(t, user.PendingEmailExpiresAt)
	assert.True(t, user.PendingEmailExpiresAt.After(time.Now()),
		"expires_at should be in the future for a fresh code")

	_ = adminToken
}

// --- VerifyEmail (per-user, authenticated) ---

func TestVerifyEmail_Success(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)

	// Seed pending_email + a 6-char verifycode-shaped token.
	verifyToken := verifycode.Generate()
	_, err := env.store.Users().SetPendingEmail(context.Background(), store.SetPendingEmailParams{
		PendingEmail:          "verified@example.com",
		PendingEmailToken:     verifyToken,
		PendingEmailExpiresAt: ptrTime(time.Now().Add(1 * time.Hour).UTC()),
		ID:                    env.userID,
		CooldownCutoff:        store.UnconditionalMintCutoff(),
	})
	require.NoError(t, err)

	// User submits the display form; backend Normalize handles the hyphen.
	resp, err := env.client.VerifyEmail(context.Background(), authedReq(&leapmuxv1.VerifyEmailRequest{
		VerificationToken: verifycode.Format(verifyToken),
	}, env.token))
	require.NoError(t, err)
	assert.Equal(t, "verified@example.com", resp.Msg.GetUser().GetEmail())

	user, err := env.store.Users().GetByID(context.Background(), env.userID)
	require.NoError(t, err)
	assert.Equal(t, "verified@example.com", user.Email)
	assert.True(t, user.EmailVerified)
	assert.Empty(t, user.PendingEmail)
	assert.Empty(t, user.PendingEmailToken)
	assert.Zero(t, user.PendingEmailAttempts)
}

// TestVerifyEmail_AcceptsLowercaseInput exercises the contract that the
// stored verification code is canonical (uppercase, drawn from
// verifycode.Charset) and that Normalize uppercases user input — so a
// user typing "abc-def" verifies against a stored "ABCDEF" via
// constant-time compare without any per-call ToUpper on the stored side.
func TestVerifyEmail_AcceptsLowercaseInput(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)

	verifyToken := verifycode.Generate()
	_, err := env.store.Users().SetPendingEmail(context.Background(), store.SetPendingEmailParams{
		PendingEmail:          "lowercase@example.com",
		PendingEmailToken:     verifyToken,
		PendingEmailExpiresAt: ptrTime(time.Now().Add(1 * time.Hour).UTC()),
		ID:                    env.userID,
		CooldownCutoff:        store.UnconditionalMintCutoff(),
	})
	require.NoError(t, err)

	// Submit the display form lower-cased ("abc-def" instead of "ABC-DEF").
	resp, err := env.client.VerifyEmail(context.Background(), authedReq(&leapmuxv1.VerifyEmailRequest{
		VerificationToken: strings.ToLower(verifycode.Format(verifyToken)),
	}, env.token))
	require.NoError(t, err)
	assert.Equal(t, "lowercase@example.com", resp.Msg.GetUser().GetEmail())
}

func TestVerifyEmail_InvalidShape(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)

	// Bad shape never makes it past Normalize → InvalidArgument, regardless
	// of whether a pending row exists for this user.
	_, err := env.client.VerifyEmail(context.Background(), authedReq(&leapmuxv1.VerifyEmailRequest{
		VerificationToken: "bogus-token",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestVerifyEmail_ExpiredOrMismatchSurfacesIdentically(t *testing.T) {
	t.Parallel()

	// The whole point of collapsing expiry and mismatch into one error is
	// that callers can't distinguish them — that closes a timing oracle on
	// "is there a code at all?". Assert both code AND message are equal.
	env := setupUserTest(t)

	expiredToken := verifycode.Generate()
	_, err := env.store.Users().SetPendingEmail(context.Background(), store.SetPendingEmailParams{
		PendingEmail:          "expired@example.com",
		PendingEmailToken:     expiredToken,
		PendingEmailExpiresAt: ptrTime(time.Now().Add(-1 * time.Hour).UTC()),
		ID:                    env.userID,
		CooldownCutoff:        store.UnconditionalMintCutoff(),
	})
	require.NoError(t, err)

	_, expiredErr := env.client.VerifyEmail(context.Background(), authedReq(&leapmuxv1.VerifyEmailRequest{
		VerificationToken: verifycode.Format(expiredToken),
	}, env.token))
	require.Error(t, expiredErr)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(expiredErr))

	// Reset to a live token but submit a different valid-shape code.
	liveToken := verifycode.Generate()
	wrongToken := verifycode.Generate()
	for wrongToken == liveToken {
		wrongToken = verifycode.Generate()
	}
	minted, err := env.store.Users().SetPendingEmail(context.Background(), store.SetPendingEmailParams{
		PendingEmail:          "live@example.com",
		PendingEmailToken:     liveToken,
		PendingEmailExpiresAt: ptrTime(time.Now().Add(1 * time.Hour).UTC()),
		ID:                    env.userID,
		CooldownCutoff:        store.UnconditionalMintCutoff(),
	})
	require.NoError(t, err)
	require.True(t, minted)
	_, mismatchErr := env.client.VerifyEmail(context.Background(), authedReq(&leapmuxv1.VerifyEmailRequest{
		VerificationToken: verifycode.Format(wrongToken),
	}, env.token))
	require.Error(t, mismatchErr)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(mismatchErr))
	assert.Equal(t, expiredErr.Error(), mismatchErr.Error(),
		"expired and mismatch must be byte-identical to avoid an oracle")
}

func TestVerifyEmail_PendingEmailEmpty(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)

	// Set a token but with empty pending_email — represents a "nothing
	// to verify" precondition error, distinct from invalid/expired codes.
	verifyToken := verifycode.Generate()
	_, err := env.store.Users().SetPendingEmail(context.Background(), store.SetPendingEmailParams{
		PendingEmail:          "",
		PendingEmailToken:     verifyToken,
		PendingEmailExpiresAt: ptrTime(time.Now().Add(1 * time.Hour).UTC()),
		ID:                    env.userID,
		CooldownCutoff:        store.UnconditionalMintCutoff(),
	})
	require.NoError(t, err)

	_, err = env.client.VerifyEmail(context.Background(), authedReq(&leapmuxv1.VerifyEmailRequest{
		VerificationToken: verifycode.Format(verifyToken),
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestVerifyEmail_RateLimitForceExpires(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)

	live := verifycode.Generate()
	minted, err := env.store.Users().SetPendingEmail(context.Background(), store.SetPendingEmailParams{
		PendingEmail:          "burned@example.com",
		PendingEmailToken:     live,
		PendingEmailExpiresAt: ptrTime(time.Now().Add(1 * time.Hour).UTC()),
		ID:                    env.userID,
		CooldownCutoff:        store.UnconditionalMintCutoff(),
	})
	require.NoError(t, err)
	require.True(t, minted)

	// Five wrong attempts: each one should fail with NotFound but the
	// row stays alive.
	for i := 0; i < 5; i++ {
		bad := verifycode.Generate()
		for bad == live {
			bad = verifycode.Generate()
		}
		_, err := env.client.VerifyEmail(context.Background(), authedReq(&leapmuxv1.VerifyEmailRequest{
			VerificationToken: verifycode.Format(bad),
		}, env.token))
		require.Error(t, err)
		assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	}

	// 6th attempt — even with the *correct* code — must fail with
	// ResourceExhausted because the previous attempt force-expired the row.
	_, err = env.client.VerifyEmail(context.Background(), authedReq(&leapmuxv1.VerifyEmailRequest{
		VerificationToken: verifycode.Format(live),
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
}

// --- ResendVerificationEmail ---

// setupResendUserTest provisions a UserService backed by a recordingSender
// so tests can assert the resent email's recipient + body.
func setupResendUserTest(t *testing.T) (*userTestEnv, *recordingSender) {
	t.Helper()
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	require.NoError(t, st.Migrator().Migrate(context.Background()))

	rec := &recordingSender{}
	userSvc := service.NewUserService(st, testConfig(), servicetest.NewSettingsManager(t, st, nil), auth.NewCredentialLifecycleEffects(nil, nil, nil), rec, mail.Renderer{}, nil)

	mux := http.NewServeMux()
	interceptor, contexts := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st})
	t.Cleanup(contexts.Stop)
	opts := connect.WithInterceptors(interceptor)
	path, handler := leapmuxv1connect.NewUserServiceHandler(userSvc, opts)
	mux.Handle(path, handler)

	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewUserServiceClient(server.Client(), server.URL, connect.WithGRPC())

	userID := id.Generate()
	hash, _ := password.Hash("testpass")
	_ = st.Users().Create(context.Background(), store.CreateUserParams{
		ID: userID, Username: "resender", PasswordHash: hash,
		DisplayName: "Resender", PasswordSet: true,
	})
	token, _, _, err := auth.Login(context.Background(), st, "resender", "testpass", auth.DefaultSessionDuration)
	require.NoError(t, err)

	return &userTestEnv{client: client, store: st, token: token, userID: userID}, rec
}

func TestResendVerificationEmail_RequiresAuth(t *testing.T) {
	t.Parallel()

	env, _ := setupResendUserTest(t)
	_, err := env.client.ResendVerificationEmail(context.Background(), connect.NewRequest(&leapmuxv1.ResendVerificationEmailRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestResendVerificationEmail_RequiresPendingEmail(t *testing.T) {
	t.Parallel()

	env, _ := setupResendUserTest(t)
	// User has no pending email — there's nothing to resend.
	_, err := env.client.ResendVerificationEmail(context.Background(), authedReq(&leapmuxv1.ResendVerificationEmailRequest{}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestResendVerificationEmail_RotatesCodeAndSends(t *testing.T) {
	t.Parallel()

	env, sender := setupResendUserTest(t)

	// Seed a pending row with an "old" expires_at far enough back that
	// the cooldown window has elapsed (TTL is 30min, cooldown 60s — set
	// expires_at = now+25min so issued_at = now-5min).
	originalCode := verifycode.Generate()
	minted, err := env.store.Users().SetPendingEmail(context.Background(), store.SetPendingEmailParams{
		ID:                    env.userID,
		PendingEmail:          "u@example.com",
		PendingEmailToken:     originalCode,
		PendingEmailExpiresAt: ptrTime(time.Now().Add(25 * time.Minute).UTC()),
		CooldownCutoff:        store.UnconditionalMintCutoff(),
	})
	require.NoError(t, err)
	require.True(t, minted)

	resp, err := env.client.ResendVerificationEmail(context.Background(), authedReq(&leapmuxv1.ResendVerificationEmailRequest{}, env.token))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetEmailSent())

	// A fresh code must replace the original — otherwise users who lost
	// the email could still verify with the leaked-but-presumed-private
	// stale code from logs/notifications.
	got, err := env.store.Users().GetByID(context.Background(), env.userID)
	require.NoError(t, err)
	assert.NotEqual(t, originalCode, got.PendingEmailToken,
		"resend must rotate the code, not reuse the previous one")
	assert.Equal(t, "u@example.com", got.PendingEmail)
	assert.Zero(t, got.PendingEmailAttempts, "attempts counter must reset on resend")

	last := sender.last()
	require.NotNil(t, last)
	assert.Equal(t, "u@example.com", last.To)
	assert.Contains(t, last.Body, verifycode.Format(got.PendingEmailToken),
		"the email body must carry the *new* code")
}

func TestResendVerificationEmail_CooldownEnforced(t *testing.T) {
	t.Parallel()

	// Seed a pending row whose implied "issued_at" is just now: the
	// cooldown must reject a back-to-back resend so a runaway client
	// (or hostile caller) can't flood the user's inbox.
	env, _ := setupResendUserTest(t)

	minted, err := env.store.Users().SetPendingEmail(context.Background(), store.SetPendingEmailParams{
		ID:                    env.userID,
		PendingEmail:          "u@example.com",
		PendingEmailToken:     verifycode.Generate(),
		PendingEmailExpiresAt: ptrTime(time.Now().Add(30 * time.Minute).UTC()),
		CooldownCutoff:        store.UnconditionalMintCutoff(),
	})
	require.NoError(t, err)
	require.True(t, minted)

	_, err = env.client.ResendVerificationEmail(context.Background(), authedReq(&leapmuxv1.ResendVerificationEmailRequest{}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
}

func TestVerifyEmail_EmailTakenSinceRequest(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)

	verifyToken := verifycode.Generate()
	_, err := env.store.Users().SetPendingEmail(context.Background(), store.SetPendingEmailParams{
		PendingEmail:          "contested@example.com",
		PendingEmailToken:     verifyToken,
		PendingEmailExpiresAt: ptrTime(time.Now().Add(1 * time.Hour).UTC()),
		ID:                    env.userID,
		CooldownCutoff:        store.UnconditionalMintCutoff(),
	})
	require.NoError(t, err)

	// Create another user who claims that email in the email column.
	user2ID := id.Generate()
	hash, _ := password.Hash("testpass2")
	_ = env.store.Users().Create(context.Background(), store.CreateUserParams{
		ID:           user2ID,
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
	_, err = env.client.VerifyEmail(context.Background(), authedReq(&leapmuxv1.VerifyEmailRequest{
		VerificationToken: verifycode.Format(verifyToken),
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
}

func TestVerifyEmail_CrossUser_NoOracle(t *testing.T) {
	t.Parallel()

	// Per-user lookup: if user B submits user A's code, B's row simply
	// doesn't have a matching token, so they get the same generic
	// NotFound as anyone typing a wrong code. There's nothing to leak.
	env := setupUserTest(t)

	victimToken := verifycode.Generate()
	_, err := env.store.Users().SetPendingEmail(context.Background(), store.SetPendingEmailParams{
		PendingEmail:          "stolen@example.com",
		PendingEmailToken:     victimToken,
		PendingEmailExpiresAt: ptrTime(time.Now().Add(1 * time.Hour).UTC()),
		ID:                    env.userID,
		CooldownCutoff:        store.UnconditionalMintCutoff(),
	})
	require.NoError(t, err)

	// Create a different user and log in as them. Important: the
	// attacker has *no* pending row of their own, so any submission
	// they make hits the FailedPrecondition path. Give them one too so
	// we exercise the actual mismatch case.
	attackerID := id.Generate()
	attackerHash, _ := password.Hash("testpass2")
	_ = env.store.Users().Create(context.Background(), store.CreateUserParams{
		ID:           attackerID,
		Username:     "attacker",
		PasswordHash: attackerHash,
		DisplayName:  "Attacker",
		PasswordSet:  true,
		IsAdmin:      false,
	})
	attackerOwnToken := verifycode.Generate()
	minted, err := env.store.Users().SetPendingEmail(context.Background(), store.SetPendingEmailParams{
		PendingEmail:          "attacker@example.com",
		PendingEmailToken:     attackerOwnToken,
		PendingEmailExpiresAt: ptrTime(time.Now().Add(1 * time.Hour).UTC()),
		ID:                    attackerID,
		CooldownCutoff:        store.UnconditionalMintCutoff(),
	})
	require.NoError(t, err)
	require.True(t, minted)
	attackerToken, _, _, err := auth.Login(context.Background(), env.store, "attacker", "testpass2", auth.DefaultSessionDuration)
	require.NoError(t, err)

	_, err = env.client.VerifyEmail(context.Background(), authedReq(&leapmuxv1.VerifyEmailRequest{
		VerificationToken: verifycode.Format(victimToken),
	}, attackerToken))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err),
		"cross-user submissions must look identical to plain typos")
}

func TestChangePassword_InvalidatesOtherSessions(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)

	// Create a second session for the same user (simulates another device).
	otherSession, _, err := auth.CreateSession(context.Background(), env.store, userid.MustNew(env.userID), auth.DefaultSessionDuration)
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

// onUserAuthTxStore fires a one-shot side effect when a user-auth transaction is
// opened, letting a test inject a concurrent mutation into ChangePassword.
type onUserAuthTxStore struct {
	store.Store
	once   sync.Once
	before func()
}

func (s *onUserAuthTxStore) RunInUserAuthTransaction(ctx context.Context, userID userid.UserID, fn func(tx store.Store) error) error {
	s.once.Do(s.before)
	return s.Store.RunInUserAuthTransaction(ctx, userID, fn)
}

// TestChangePassword_ToleratesConcurrentActingSessionDeletion verifies that a
// same-user logout / admin force-logout deleting the acting session mid-request
// (Sessions().Delete does not contend on the user-auth lock ChangePassword
// holds) does not roll back an otherwise-valid password change: RefreshAuth-
// Generation matching zero rows for the now-absent session is tolerated, not
// fatal. Against the pre-fix `n != 1` guard this request failed with a spurious
// CodeInternal and left the password unchanged.
func TestChangePassword_ToleratesConcurrentActingSessionDeletion(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Migrator().Migrate(ctx))

	userID := id.Generate()
	hash, _ := password.Hash("testpass")
	require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
		ID: userID, Username: "testuser", PasswordHash: hash,
		DisplayName: "Test User", PasswordSet: true,
	}))

	token, _, _, err := auth.Login(ctx, st, "testuser", "testpass", auth.DefaultSessionDuration)
	require.NoError(t, err)
	userInfo, err := auth.ValidateToken(ctx, st, token)
	require.NoError(t, err)
	sessionID := userInfo.Credential.SessionID()
	require.NotEmpty(t, sessionID)

	// The interceptor validates the token against the live session, then the
	// handler opens its transaction on the wrapped store -- at which point we
	// delete the acting session, exactly as a concurrent logout would.
	var deleteN int64
	var deleteErr error
	hooked := &onUserAuthTxStore{Store: st, before: func() {
		deleteN, deleteErr = st.Sessions().Delete(ctx, sessionID)
	}}

	mux := http.NewServeMux()
	interceptor, contexts := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st})
	t.Cleanup(contexts.Stop)
	userSvc := service.NewUserService(hooked, testConfig(), servicetest.NewSettingsManager(t, hooked, nil), auth.NewCredentialLifecycleEffects(contexts, nil, nil), mail.NewStubSender(), mail.Renderer{}, nil)
	path, handler := leapmuxv1connect.NewUserServiceHandler(userSvc, connect.WithInterceptors(interceptor))
	mux.Handle(path, handler)
	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	client := leapmuxv1connect.NewUserServiceClient(server.Client(), server.URL, connect.WithGRPC())

	_, err = client.ChangePassword(ctx, authedReq(&leapmuxv1.ChangePasswordRequest{
		CurrentPassword: "testpass",
		NewPassword:     "newpass123",
	}, token))
	require.NoError(t, err, "password change must survive a concurrently-deleted acting session")
	require.NoError(t, deleteErr)
	require.Equal(t, int64(1), deleteN, "test must have deleted the acting session mid-request")

	// The password actually changed: the old one no longer authenticates and the
	// new one does.
	_, _, _, err = auth.Login(ctx, st, "testuser", "testpass", auth.DefaultSessionDuration)
	require.Error(t, err, "old password must be rejected after the change")
	_, _, _, err = auth.Login(ctx, st, "testuser", "newpass123", auth.DefaultSessionDuration)
	require.NoError(t, err, "new password must authenticate after the change")
}

// --- ChangePassword tests for OAuth users ---

func TestChangePassword_OAuthUser_CanSetWithoutCurrentPassword(t *testing.T) {
	t.Parallel()

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
	_, _, _, err = auth.Login(context.Background(), env.store, "testuser", "newpass123", auth.DefaultSessionDuration)
	require.NoError(t, err)
}

func TestChangePassword_PasswordUser_RequiresCurrentPassword(t *testing.T) {
	t.Parallel()

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

func TestChangePassword_PasskeyOnly_RequiresReauthProof(t *testing.T) {
	t.Parallel()

	env := setupOAuthUserTest(t)
	seedPasskeyCredential(t, env.store, env.userID, "Phone")

	_, err := env.client.ChangePassword(context.Background(), authedReq(&leapmuxv1.ChangePasswordRequest{
		NewPassword: "newpass123",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	proof := seedReauthProof(t, env.store, env.userID)
	_, err = env.client.ChangePassword(context.Background(), authedReq(&leapmuxv1.ChangePasswordRequest{
		NewPassword: "newpass123",
		ReauthProof: proof,
	}, env.token))
	require.NoError(t, err)

	user, err := env.store.Users().GetByID(context.Background(), env.userID)
	require.NoError(t, err)
	assert.True(t, user.PasswordSet)

	_, _, _, err = auth.Login(context.Background(), env.store, "testuser", "newpass123", auth.DefaultSessionDuration)
	require.NoError(t, err)
}

// --- UnlinkOAuthProvider tests ---

func TestUnlinkOAuthProvider_Success(t *testing.T) {
	t.Parallel()

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
		UserID: userid.MustNew(env.userID), ProviderID: "github-1", ProviderSubject: "gh-sub",
	})
	require.NoError(t, err)
	err = env.store.OAuthUserLinks().Create(context.Background(), store.CreateOAuthUserLinkParams{
		UserID: userid.MustNew(env.userID), ProviderID: "google-1", ProviderSubject: "g-sub",
	})
	require.NoError(t, err)

	// Unlink GitHub — should succeed (Google still linked).
	_, err = env.client.UnlinkOAuthProvider(context.Background(), authedReq(&leapmuxv1.UnlinkOAuthProviderRequest{
		ProviderId: "github-1",
	}, env.token))
	require.NoError(t, err)

	// Verify only Google link remains.
	links, err := env.store.OAuthUserLinks().ListByUser(context.Background(), userid.MustNew(env.userID))
	require.NoError(t, err)
	assert.Len(t, links, 1)
	assert.Equal(t, "google-1", links[0].ProviderID)
}

func TestUnlinkOAuthProvider_LastLink_WithPassword(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)

	// User has password_set = 1 (default from setupUserTest).
	err := env.store.OAuthProviders().Create(context.Background(), store.CreateOAuthProviderParams{
		ID: "github-2", ProviderType: "github", Name: "GitHub",
		ClientID: "c1", ClientSecret: []byte("s1"), Scopes: "read:user", Enabled: true,
	})
	require.NoError(t, err)
	err = env.store.OAuthUserLinks().Create(context.Background(), store.CreateOAuthUserLinkParams{
		UserID: userid.MustNew(env.userID), ProviderID: "github-2", ProviderSubject: "gh-sub",
	})
	require.NoError(t, err)

	// Should succeed because user has a password.
	_, err = env.client.UnlinkOAuthProvider(context.Background(), authedReq(&leapmuxv1.UnlinkOAuthProviderRequest{
		ProviderId: "github-2",
	}, env.token))
	require.NoError(t, err)

	links, err := env.store.OAuthUserLinks().ListByUser(context.Background(), userid.MustNew(env.userID))
	require.NoError(t, err)
	assert.Empty(t, links)
}

func TestUnlinkOAuthProvider_LastLink_NoPassword_Blocked(t *testing.T) {
	t.Parallel()

	env := setupOAuthUserTest(t)

	err := env.store.OAuthProviders().Create(context.Background(), store.CreateOAuthProviderParams{
		ID: "github-3", ProviderType: "github", Name: "GitHub",
		ClientID: "c1", ClientSecret: []byte("s1"), Scopes: "read:user", Enabled: true,
	})
	require.NoError(t, err)
	err = env.store.OAuthUserLinks().Create(context.Background(), store.CreateOAuthUserLinkParams{
		UserID: userid.MustNew(env.userID), ProviderID: "github-3", ProviderSubject: "gh-sub",
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
	links, err := env.store.OAuthUserLinks().ListByUser(context.Background(), userid.MustNew(env.userID))
	require.NoError(t, err)
	assert.Len(t, links, 1)
}

func TestUnlinkOAuthProvider_NotFound(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)

	_, err := env.client.UnlinkOAuthProvider(context.Background(), authedReq(&leapmuxv1.UnlinkOAuthProviderRequest{
		ProviderId: "nonexistent",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// --- Passkey management ---

func seedPasskeyCredential(t *testing.T, st store.Store, userID, friendlyName string) string {
	t.Helper()
	pkID := id.Generate()
	now := time.Now().UTC()
	require.NoError(t, st.PasskeyCredentials().Create(context.Background(), store.CreatePasskeyCredentialParams{
		ID:           pkID,
		UserID:       userID,
		CredentialID: []byte("cred-" + pkID),
		PublicKey:    []byte("pubkey"),
		SignCount:    0,
		Transports:   "[]",
		FriendlyName: friendlyName,
		KeyVersion:   1,
		CreatedAt:    now,
	}))
	return pkID
}

func seedReauthProof(t *testing.T, st store.Store, userID string) string {
	t.Helper()
	proofID := id.Generate()
	now := time.Now().UTC()
	require.NoError(t, st.WebAuthnSessions().Create(context.Background(), store.CreateWebAuthnSessionParams{
		ID:          proofID,
		Kind:        "reauth_proof",
		UserID:      userID,
		PayloadJSON: "{}",
		SessionData: []byte("dummy"),
		ExpiresAt:   now.Add(5 * time.Minute),
		CreatedAt:   now,
	}))
	return proofID
}

func TestUserService_ListPasskeys(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)
	seedPasskeyCredential(t, env.store, env.userID, "Laptop")
	seedPasskeyCredential(t, env.store, env.userID, "Phone")

	resp, err := env.client.ListPasskeys(context.Background(), authedReq(&leapmuxv1.ListPasskeysRequest{}, env.token))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetPasskeys(), 2)
	names := map[string]struct{}{}
	for _, pk := range resp.Msg.GetPasskeys() {
		names[pk.GetFriendlyName()] = struct{}{}
	}
	assert.Contains(t, names, "Laptop")
	assert.Contains(t, names, "Phone")
}

func TestUserService_RenamePasskey(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)
	pkID := seedPasskeyCredential(t, env.store, env.userID, "Old Name")

	resp, err := env.client.RenamePasskey(context.Background(), authedReq(&leapmuxv1.RenamePasskeyRequest{
		Id:              pkID,
		FriendlyName:    "New Name",
		CurrentPassword: "testpass",
	}, env.token))
	require.NoError(t, err)
	assert.Equal(t, "New Name", resp.Msg.GetPasskey().GetFriendlyName())

	row, err := env.store.PasskeyCredentials().GetByID(context.Background(), pkID)
	require.NoError(t, err)
	assert.Equal(t, "New Name", row.FriendlyName)
}

func TestUserService_RenamePasskey_RequiresPassword(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)
	pkID := seedPasskeyCredential(t, env.store, env.userID, "Old Name")

	_, err := env.client.RenamePasskey(context.Background(), authedReq(&leapmuxv1.RenamePasskeyRequest{
		Id:           pkID,
		FriendlyName: "New Name",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestUserService_RenamePasskey_WithReauthProof(t *testing.T) {
	t.Parallel()

	env := setupOAuthUserTest(t)
	pkID := seedPasskeyCredential(t, env.store, env.userID, "Old Name")
	proof := seedReauthProof(t, env.store, env.userID)

	resp, err := env.client.RenamePasskey(context.Background(), authedReq(&leapmuxv1.RenamePasskeyRequest{
		Id:           pkID,
		FriendlyName: "New Name",
		ReauthProof:  proof,
	}, env.token))
	require.NoError(t, err)
	assert.Equal(t, "New Name", resp.Msg.GetPasskey().GetFriendlyName())

	_, err = env.store.WebAuthnSessions().Get(context.Background(), proof)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestUserService_DeletePasskey_WithPassword(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)
	pkID := seedPasskeyCredential(t, env.store, env.userID, "To Delete")

	_, err := env.client.DeletePasskey(context.Background(), authedReq(&leapmuxv1.DeletePasskeyRequest{
		Id:              pkID,
		CurrentPassword: "testpass",
	}, env.token))
	require.NoError(t, err)

	_, err = env.store.PasskeyCredentials().GetByID(context.Background(), pkID)
	require.Error(t, err)
	assert.True(t, errors.Is(err, store.ErrNotFound))
}

func TestUserService_DeletePasskey_WithReauthProof(t *testing.T) {
	t.Parallel()

	env := setupOAuthUserTest(t)
	pk1 := seedPasskeyCredential(t, env.store, env.userID, "Keep")
	pk2 := seedPasskeyCredential(t, env.store, env.userID, "Remove")
	proof := seedReauthProof(t, env.store, env.userID)

	_, err := env.client.DeletePasskey(context.Background(), authedReq(&leapmuxv1.DeletePasskeyRequest{
		Id:          pk2,
		ReauthProof: proof,
	}, env.token))
	require.NoError(t, err)

	_, err = env.store.PasskeyCredentials().GetByID(context.Background(), pk2)
	require.Error(t, err)
	assert.True(t, errors.Is(err, store.ErrNotFound))

	_, err = env.store.PasskeyCredentials().GetByID(context.Background(), pk1)
	require.NoError(t, err)
}

func TestUserService_DeletePasskey_LastPasskeyRejected(t *testing.T) {
	t.Parallel()

	env := setupOAuthUserTest(t)
	pkID := seedPasskeyCredential(t, env.store, env.userID, "Only One")
	proof := seedReauthProof(t, env.store, env.userID)

	_, err := env.client.DeletePasskey(context.Background(), authedReq(&leapmuxv1.DeletePasskeyRequest{
		Id:          pkID,
		ReauthProof: proof,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "new_password")

	_, err = env.store.PasskeyCredentials().GetByID(context.Background(), pkID)
	require.NoError(t, err)

	// Rejection must not consume the reauth proof.
	_, err = env.store.WebAuthnSessions().Get(context.Background(), proof)
	require.NoError(t, err)
}

func TestUserService_DeletePasskey_LastPasskeyWithNewPassword_Deactivates(t *testing.T) {
	t.Parallel()

	env := setupOAuthUserTest(t)
	pkID := seedPasskeyCredential(t, env.store, env.userID, "Only One")
	proof := seedReauthProof(t, env.store, env.userID)

	otherSession, _, err := auth.CreateSession(context.Background(), env.store, userid.MustNew(env.userID), auth.DefaultSessionDuration)
	require.NoError(t, err)

	_, err = env.client.DeletePasskey(context.Background(), authedReq(&leapmuxv1.DeletePasskeyRequest{
		Id:          pkID,
		ReauthProof: proof,
		NewPassword: "newpass123",
	}, env.token))
	require.NoError(t, err)

	user, err := env.store.Users().GetByID(context.Background(), env.userID)
	require.NoError(t, err)
	assert.True(t, user.PasswordSet)

	rows, err := env.store.PasskeyCredentials().ListByUser(context.Background(), env.userID)
	require.NoError(t, err)
	assert.Empty(t, rows)

	_, _, _, err = auth.Login(context.Background(), env.store, "testuser", "newpass123", auth.DefaultSessionDuration)
	require.NoError(t, err)

	_, err = auth.ValidateToken(context.Background(), env.store, env.token)
	assert.NoError(t, err, "acting session must survive")
	_, err = auth.ValidateToken(context.Background(), env.store, otherSession)
	assert.Error(t, err, "other sessions must be revoked after passkey-only deactivation")
}

func TestUserService_DeactivatePasskeyAuth_WithPassword(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)
	seedPasskeyCredential(t, env.store, env.userID, "One")
	seedPasskeyCredential(t, env.store, env.userID, "Two")

	_, err := env.client.DeactivatePasskeyAuth(context.Background(), authedReq(&leapmuxv1.DeactivatePasskeyAuthRequest{
		CurrentPassword: "testpass",
	}, env.token))
	require.NoError(t, err)

	rows, err := env.store.PasskeyCredentials().ListByUser(context.Background(), env.userID)
	require.NoError(t, err)
	assert.Empty(t, rows)
}

func TestUserService_DeactivatePasskeyAuth_WithReauthAndNewPassword(t *testing.T) {
	t.Parallel()

	env := setupOAuthUserTest(t)
	seedPasskeyCredential(t, env.store, env.userID, "Only")
	proof := seedReauthProof(t, env.store, env.userID)

	_, err := env.client.DeactivatePasskeyAuth(context.Background(), authedReq(&leapmuxv1.DeactivatePasskeyAuthRequest{
		ReauthProof: proof,
		NewPassword: "newpass123",
	}, env.token))
	require.NoError(t, err)

	user, err := env.store.Users().GetByID(context.Background(), env.userID)
	require.NoError(t, err)
	assert.True(t, user.PasswordSet)

	rows, err := env.store.PasskeyCredentials().ListByUser(context.Background(), env.userID)
	require.NoError(t, err)
	assert.Empty(t, rows)

	_, _, _, err = auth.Login(context.Background(), env.store, "testuser", "newpass123", auth.DefaultSessionDuration)
	require.NoError(t, err)
}

func TestUserService_DeactivatePasskeyAuth_InvalidatesOtherSessions(t *testing.T) {
	t.Parallel()

	env := setupOAuthUserTest(t)
	seedPasskeyCredential(t, env.store, env.userID, "Only")
	proof := seedReauthProof(t, env.store, env.userID)

	otherSession, _, err := auth.CreateSession(context.Background(), env.store, userid.MustNew(env.userID), auth.DefaultSessionDuration)
	require.NoError(t, err)

	_, err = env.client.DeactivatePasskeyAuth(context.Background(), authedReq(&leapmuxv1.DeactivatePasskeyAuthRequest{
		ReauthProof: proof,
		NewPassword: "newpass123",
	}, env.token))
	require.NoError(t, err)

	_, err = auth.ValidateToken(context.Background(), env.store, env.token)
	assert.NoError(t, err, "acting session must survive")
	_, err = auth.ValidateToken(context.Background(), env.store, otherSession)
	assert.Error(t, err, "other sessions must be revoked")
}

func TestVerifyEmail_IncludesPasskeyCount(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)
	seedPasskeyCredential(t, env.store, env.userID, "One")
	seedPasskeyCredential(t, env.store, env.userID, "Two")

	verifyToken := verifycode.Generate()
	minted, err := env.store.Users().SetPendingEmail(context.Background(), store.SetPendingEmailParams{
		PendingEmail:          "counted@example.com",
		PendingEmailToken:     verifyToken,
		PendingEmailExpiresAt: ptrTime(time.Now().Add(1 * time.Hour).UTC()),
		ID:                    env.userID,
		CooldownCutoff:        store.UnconditionalMintCutoff(),
	})
	require.NoError(t, err)
	require.True(t, minted)

	resp, err := env.client.VerifyEmail(context.Background(), authedReq(&leapmuxv1.VerifyEmailRequest{
		VerificationToken: verifycode.Format(verifyToken),
	}, env.token))
	require.NoError(t, err)
	assert.Equal(t, int32(2), resp.Msg.GetUser().GetPasskeyCount())
}

func TestResendVerificationEmail_SMTPEnableTransition(t *testing.T) {
	t.Parallel()

	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Migrator().Migrate(context.Background()))

	set := servicetest.NewSettingsManager(t, st, nil)
	seedSMTP(t, set)

	rec := &recordingSender{}
	userSvc := service.NewUserService(st, testConfig(), set, auth.NewCredentialLifecycleEffects(nil, nil, nil), rec, mail.Renderer{}, nil)

	mux := http.NewServeMux()
	interceptor, contexts := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st})
	t.Cleanup(contexts.Stop)
	path, handler := leapmuxv1connect.NewUserServiceHandler(userSvc, connect.WithInterceptors(interceptor))
	mux.Handle(path, handler)

	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewUserServiceClient(server.Client(), server.URL, connect.WithGRPC())

	userID := id.Generate()
	hash, _ := password.Hash("testpass")
	require.NoError(t, st.Users().Create(context.Background(), store.CreateUserParams{
		ID: userID, Username: "smtpuser", PasswordHash: hash,
		DisplayName: "SMTP User", PasswordSet: true,
	}))
	require.NoError(t, st.Users().UpdateEmail(context.Background(), store.UpdateUserEmailParams{
		Email: "unverified@example.com", EmailVerified: false, ID: userID,
	}))

	token, _, _, err := auth.Login(context.Background(), st, "smtpuser", "testpass", auth.DefaultSessionDuration)
	require.NoError(t, err)

	resp, err := client.ResendVerificationEmail(context.Background(), authedReq(&leapmuxv1.ResendVerificationEmailRequest{}, token))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetEmailSent())
	require.NotNil(t, resp.Msg.GetNextResendAvailableAt())

	user, err := st.Users().GetByID(context.Background(), userID)
	require.NoError(t, err)
	assert.Equal(t, "unverified@example.com", user.PendingEmail)
	assert.NotEmpty(t, user.PendingEmailToken)

	last := rec.last()
	require.NotNil(t, last)
	assert.Equal(t, "unverified@example.com", last.To)
}

func TestResendVerificationEmail_NextResendAvailableAt(t *testing.T) {
	t.Parallel()

	env, _ := setupResendUserTest(t)

	minted, err := env.store.Users().SetPendingEmail(context.Background(), store.SetPendingEmailParams{
		ID:                    env.userID,
		PendingEmail:          "u@example.com",
		PendingEmailToken:     verifycode.Generate(),
		PendingEmailExpiresAt: ptrTime(time.Now().Add(25 * time.Minute).UTC()),
		CooldownCutoff:        store.UnconditionalMintCutoff(),
	})
	require.NoError(t, err)
	require.True(t, minted)

	before := time.Now().UTC()
	resp, err := env.client.ResendVerificationEmail(context.Background(), authedReq(&leapmuxv1.ResendVerificationEmailRequest{}, env.token))
	require.NoError(t, err)

	nextAt := resp.Msg.GetNextResendAvailableAt().AsTime()
	assert.True(t, nextAt.After(before.Add(59*time.Second)))
	assert.True(t, nextAt.Before(before.Add(61*time.Second)))
}

func TestBeginPasskeyRegistration_OAuthOnly_NoReauthRequired(t *testing.T) {
	t.Parallel()

	env := setupOAuthUserTest(t)

	req := authedReq(&leapmuxv1.BeginPasskeyRegistrationRequest{}, env.token)
	_, err := env.client.BeginPasskeyRegistration(context.Background(), req)
	// Ceremony may fail for missing keystore/RP config; auth must not require reauth proof.
	if err != nil {
		assert.NotEqual(t, connect.CodeUnauthenticated, connect.CodeOf(err))
		assert.NotContains(t, err.Error(), "reauth proof required")
		assert.NotContains(t, err.Error(), "verify your email")
	}
}

func TestBeginPasskeyRegistration_UnverifiedShell_Rejected(t *testing.T) {
	t.Parallel()

	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Migrator().Migrate(context.Background()))

	key, err := keystore.GenerateKey()
	require.NoError(t, err)
	ks, err := keystore.New(map[uint32][32]byte{1: key})
	require.NoError(t, err)

	userSvc := service.NewUserService(st, testConfig(), servicetest.NewSettingsManager(t, st, nil), auth.NewCredentialLifecycleEffects(nil, nil, nil), mail.NewStubSender(), mail.Renderer{}, ks)
	mux := http.NewServeMux()
	interceptor, contexts := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st})
	t.Cleanup(contexts.Stop)
	path, handler := leapmuxv1connect.NewUserServiceHandler(userSvc, connect.WithInterceptors(interceptor))
	mux.Handle(path, handler)
	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	client := leapmuxv1connect.NewUserServiceClient(server.Client(), server.URL, connect.WithGRPC())

	userID := id.Generate()
	require.NoError(t, st.Users().Create(context.Background(), store.CreateUserParams{
		ID: userID, Username: "shell", PasswordHash: password.PlaceholderHash,
		DisplayName: "Shell", EmailVerified: false, PasswordSet: false, IsAdmin: true,
	}))
	token, _, err := auth.CreateSession(context.Background(), st, userid.MustNew(userID), auth.DefaultSessionDuration)
	require.NoError(t, err)

	_, err = client.BeginPasskeyRegistration(context.Background(), authedReq(&leapmuxv1.BeginPasskeyRegistrationRequest{}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "verify your email")
}

func TestFinishPasskeyRegistration_FailedCeremonyPreservesReauthProof(t *testing.T) {
	t.Parallel()

	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Migrator().Migrate(context.Background()))

	key, err := keystore.GenerateKey()
	require.NoError(t, err)
	ks, err := keystore.New(map[uint32][32]byte{1: key})
	require.NoError(t, err)

	userSvc := service.NewUserService(st, testConfig(), servicetest.NewSettingsManager(t, st, nil), auth.NewCredentialLifecycleEffects(nil, nil, nil), mail.NewStubSender(), mail.Renderer{}, ks)
	mux := http.NewServeMux()
	interceptor, contexts := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st})
	t.Cleanup(contexts.Stop)
	path, handler := leapmuxv1connect.NewUserServiceHandler(userSvc, connect.WithInterceptors(interceptor))
	mux.Handle(path, handler)
	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	client := leapmuxv1connect.NewUserServiceClient(server.Client(), server.URL, connect.WithGRPC())

	userID := id.Generate()
	require.NoError(t, st.Users().Create(context.Background(), store.CreateUserParams{
		ID: userID, Username: "pkonly", PasswordHash: password.PlaceholderHash,
		DisplayName: "PK Only", PasswordSet: false, IsAdmin: true,
	}))
	seedPasskeyCredential(t, st, userID, "Phone")
	proof := seedReauthProof(t, st, userID)
	token, _, err := auth.CreateSession(context.Background(), st, userid.MustNew(userID), auth.DefaultSessionDuration)
	require.NoError(t, err)

	_, err = client.FinishPasskeyRegistration(context.Background(), authedReq(&leapmuxv1.FinishPasskeyRegistrationRequest{
		SessionId:      "missing-session",
		CredentialJson: "{}",
		FriendlyName:   "New",
		ReauthProof:    proof,
	}, token))
	require.Error(t, err)

	_, getErr := st.WebAuthnSessions().Get(context.Background(), proof)
	require.NoError(t, getErr, "failed Finish must not consume the reauth proof")
}

// TestFinishPasskeyReauth_MalformedCredentialIsUnauthenticated pins the
// error CLASS of a rejected reauth ceremony. A malformed credential_json on
// a live ceremony is the caller's fault, so it must surface as
// CodeUnauthenticated (the rate limiter's proof-failure class) -- before the
// shared validateAssertion pipeline existed, FinishReauth returned the raw
// parse error and the service wrapper mapped it to CodeInternal.
func TestFinishPasskeyReauth_MalformedCredentialIsUnauthenticated(t *testing.T) {
	t.Parallel()

	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Migrator().Migrate(context.Background()))

	key, err := keystore.GenerateKey()
	require.NoError(t, err)
	ks, err := keystore.New(map[uint32][32]byte{1: key})
	require.NoError(t, err)
	wa, err := hubwebauthn.NewService(hubwebauthn.RPConfig{
		RPID: "localhost", RPDisplayName: "LeapMux", RPOrigins: []string{"http://localhost:4327"},
	}, st, ks)
	require.NoError(t, err)

	// The service derives its RP config from public_url (testConfig has no
	// listen address), mirroring the ceremony-test harness.
	set := servicetest.NewSettingsManager(t, st, nil)
	require.NoError(t, settings.KeyPublicURL.Set(context.Background(), set, "http://localhost:4327"))
	userSvc := service.NewUserService(st, testConfig(), set, auth.NewCredentialLifecycleEffects(nil, nil, nil), mail.NewStubSender(), mail.Renderer{}, ks)
	mux := http.NewServeMux()
	interceptor, contexts := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st})
	t.Cleanup(contexts.Stop)
	path, handler := leapmuxv1connect.NewUserServiceHandler(userSvc, connect.WithInterceptors(interceptor))
	mux.Handle(path, handler)
	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	client := leapmuxv1connect.NewUserServiceClient(server.Client(), server.URL, connect.WithGRPC())

	userID := id.Generate()
	require.NoError(t, st.Users().Create(context.Background(), store.CreateUserParams{
		ID: userID, Username: "reauth-badjson", PasswordHash: password.PlaceholderHash,
		DisplayName: "Reauth", PasswordSet: false, IsAdmin: true,
	}))
	credRow := id.Generate()
	encPub, keyVersion, encErr := wa.EncryptPublicKey(credRow, []byte("pubkey"))
	require.NoError(t, encErr)
	require.NoError(t, st.PasskeyCredentials().Create(context.Background(), store.CreatePasskeyCredentialParams{
		ID: credRow, UserID: userID, CredentialID: []byte("cred-reauth"), PublicKey: encPub,
		Transports: "[]", FriendlyName: "Phone", KeyVersion: keyVersion, CreatedAt: time.Now().UTC(),
	}))

	// A LIVE reauth ceremony (BeginReauth writes the properly encrypted
	// session row), so the failure lands on the parse arm, not the consume.
	sessionID, _, _, err := wa.BeginReauth(context.Background(), userID, "")
	require.NoError(t, err)
	token, _, err := auth.CreateSession(context.Background(), st, userid.MustNew(userID), auth.DefaultSessionDuration)
	require.NoError(t, err)

	_, err = client.FinishPasskeyReauth(context.Background(), authedReq(&leapmuxv1.FinishPasskeyReauthRequest{
		SessionId:      sessionID,
		CredentialJson: "not-a-credential",
	}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err),
		"a rejected ceremony is the caller's proof failure, not a server error")
}

func TestBeginPasskeyRegistration_DoesNotConsumeReauthProof(t *testing.T) {
	t.Parallel()

	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Migrator().Migrate(context.Background()))

	key, err := keystore.GenerateKey()
	require.NoError(t, err)
	ks, err := keystore.New(map[uint32][32]byte{1: key})
	require.NoError(t, err)

	userSvc := service.NewUserService(st, testConfig(), servicetest.NewSettingsManager(t, st, nil), auth.NewCredentialLifecycleEffects(nil, nil, nil), mail.NewStubSender(), mail.Renderer{}, ks)
	mux := http.NewServeMux()
	interceptor, contexts := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st})
	t.Cleanup(contexts.Stop)
	path, handler := leapmuxv1connect.NewUserServiceHandler(userSvc, connect.WithInterceptors(interceptor))
	mux.Handle(path, handler)
	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	client := leapmuxv1connect.NewUserServiceClient(server.Client(), server.URL, connect.WithGRPC())

	userID := id.Generate()
	require.NoError(t, st.Users().Create(context.Background(), store.CreateUserParams{
		ID: userID, Username: "pkonly", PasswordHash: password.PlaceholderHash,
		DisplayName: "PK Only", PasswordSet: false, IsAdmin: true,
	}))
	seedPasskeyCredential(t, st, userID, "Phone")
	proof := seedReauthProof(t, st, userID)

	token, _, err := auth.CreateSession(context.Background(), st, userid.MustNew(userID), auth.DefaultSessionDuration)
	require.NoError(t, err)

	req := authedReq(&leapmuxv1.BeginPasskeyRegistrationRequest{ReauthProof: proof}, token)
	req.Header().Set("Origin", "https://localhost")
	_, err = client.BeginPasskeyRegistration(context.Background(), req)
	// Ceremony may fail for RP config, but auth must succeed without consuming the proof.
	_, getErr := st.WebAuthnSessions().Get(context.Background(), proof)
	require.NoError(t, getErr, "Begin must not consume the reauth proof (Finish does)")
	_ = err
}

func TestDeletePasskey_ReauthProofReplayRejected(t *testing.T) {
	t.Parallel()

	env := setupOAuthUserTest(t)
	_ = seedPasskeyCredential(t, env.store, env.userID, "KeepA")
	pk1 := seedPasskeyCredential(t, env.store, env.userID, "KeepB")
	pk2 := seedPasskeyCredential(t, env.store, env.userID, "Remove")
	proof := seedReauthProof(t, env.store, env.userID)

	_, err := env.client.DeletePasskey(context.Background(), authedReq(&leapmuxv1.DeletePasskeyRequest{
		Id: pk2, ReauthProof: proof,
	}, env.token))
	require.NoError(t, err)

	// Still more than one passkey left, so this hits auth (not last-passkey
	// deactivation) and must reject the consumed proof.
	_, err = env.client.DeletePasskey(context.Background(), authedReq(&leapmuxv1.DeletePasskeyRequest{
		Id: pk1, ReauthProof: proof,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	_, err = env.store.PasskeyCredentials().GetByID(context.Background(), pk1)
	require.NoError(t, err, "replay must not delete the remaining passkey")
}

func TestListPasskeys_IncludesCredentialId(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)
	pkID := seedPasskeyCredential(t, env.store, env.userID, "Laptop")

	resp, err := env.client.ListPasskeys(context.Background(), authedReq(&leapmuxv1.ListPasskeysRequest{}, env.token))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetPasskeys(), 1)
	assert.Equal(t, pkID, resp.Msg.GetPasskeys()[0].GetId())
	assert.NotEmpty(t, resp.Msg.GetPasskeys()[0].GetCredentialId())
}

func TestUserService_ListAndRenamePasskeys_SoloRejected(t *testing.T) {
	t.Parallel()

	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Migrator().Migrate(context.Background()))

	cfg := testConfig()
	cfg.SoloMode = true
	userSvc := service.NewUserService(st, cfg, servicetest.NewSettingsManager(t, st, nil), auth.NewCredentialLifecycleEffects(nil, nil, nil), mail.NewStubSender(), mail.Renderer{}, nil)
	mux := http.NewServeMux()
	interceptor, contexts := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st})
	t.Cleanup(contexts.Stop)
	path, handler := leapmuxv1connect.NewUserServiceHandler(userSvc, connect.WithInterceptors(interceptor))
	mux.Handle(path, handler)
	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	client := leapmuxv1connect.NewUserServiceClient(server.Client(), server.URL, connect.WithGRPC())

	userID := id.Generate()
	hash, err := password.Hash("testpass")
	require.NoError(t, err)
	require.NoError(t, st.Users().Create(context.Background(), store.CreateUserParams{
		ID: userID, Username: "solouser", PasswordHash: hash, DisplayName: "Solo", PasswordSet: true, IsAdmin: true,
	}))
	token, _, err := auth.CreateSession(context.Background(), st, userid.MustNew(userID), auth.DefaultSessionDuration)
	require.NoError(t, err)
	pkID := seedPasskeyCredential(t, st, userID, "Laptop")

	_, err = client.ListPasskeys(context.Background(), authedReq(&leapmuxv1.ListPasskeysRequest{}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "solo mode")

	_, err = client.RenamePasskey(context.Background(), authedReq(&leapmuxv1.RenamePasskeyRequest{
		Id: pkID, FriendlyName: "Phone", CurrentPassword: "testpass",
	}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "solo mode")
}
