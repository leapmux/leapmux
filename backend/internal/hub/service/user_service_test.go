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

	"github.com/leapmux/leapmux/internal/authscope"
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

	"github.com/leapmux/leapmux/internal/hub/oauthapp"
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
	// contexts is the interceptor's UserInfo cache. A test that writes the
	// elevation columns directly must drop it, because the RPC that writes
	// them in production drops it too (through
	// lifecycle.UserInfoInvalidated). Without that, the next call reads a
	// cache entry minted before the elevation existed.
	contexts *auth.AuthContextRegistry
	// tv backs the interceptor's bearer rung, so a test can present a REAL
	// API credential and not just a session cookie. The two rungs answer
	// the elevation gate differently -- a bearer has no row to stamp -- and
	// only a live bearer exercises that.
	tv *auth.TokenValidator
}

// bearerFor mints a working API credential for the env's user and returns
// the Authorization value. The secret is hashed through the interceptor's
// own validator, so the credential authenticates for real rather than
// resolving to a row that can never match.
func (e *userTestEnv) bearerFor(t *testing.T) string {
	t.Helper()
	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	expires := time.Now().Add(time.Hour)
	require.NoError(t, e.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:               tokenID,
		UserID:           userid.MustNew(e.userID),
		ClientID:         oauthapp.ControlCLIClientID,
		InstallationName: "test-cli",
		GrantedScopes:    authscope.NonAdminGrant().String(),
		SecretHash:       e.tv.HashSecret(secret),
		ExpiresAt:        &expires,
	}))
	return auth.FormatBearer(auth.BearerKindAPI, tokenID, secret)
}

// bearerReq is authedReq's bearer twin: the same request, carrying a CLI
// credential instead of a session cookie.
func bearerReq[T any](msg *T, bearer string) *connect.Request[T] {
	req := connect.NewRequest(msg)
	req.Header().Set("Authorization", "Bearer "+bearer)
	return req
}

// elevate stamps a live elevation window on the env's session, through the
// REAL store write ElevateSession performs. Writing the row rather than
// faking a UserInfo is deliberate: it exercises the dialect's own mapping of
// the two columns and the read on the hot auth path that carries them back.
func (e *userTestEnv) elevate(t *testing.T) {
	t.Helper()
	now := time.Now().UTC()
	e.setElevationRow(t, now, now.Add(auth.ElevationWindow))
}

// lapseElevation writes an elevation whose window ALREADY closed, so a test
// can prove the gate re-closes without sleeping.
func (e *userTestEnv) lapseElevation(t *testing.T) {
	t.Helper()
	now := time.Now().UTC()
	e.setElevationRow(t, now.Add(-3*time.Hour), now.Add(-time.Hour))
}

// setElevationRow writes both columns verbatim, so a test can place the
// anchor and the deadline independently -- which is what the absolute cap
// needs: an OLD anchor with a deadline that is still live.
func (e *userTestEnv) setElevationRow(t *testing.T, provenAt, expiresAt time.Time) {
	t.Helper()
	elevateSessionRow(t, e.store, e.token, e.userID, provenAt, expiresAt)
	e.contexts.EvictByUserID(e.userID)
}

// elevateSessionRow is the one place a test writes the elevation columns.
// It goes through the REAL store write ElevateSession performs, so it
// exercises both the dialect's own mapping of the pair and the read on the
// hot auth path that carries them back.
func elevateSessionRow(t *testing.T, st store.Store, sessionID, userID string, provenAt, expiresAt time.Time) {
	t.Helper()
	n, err := st.Sessions().Elevate(context.Background(), store.ElevateSessionParams{
		SessionID:          sessionID,
		UserID:             userid.MustNew(userID),
		ElevationProvenAt:  provenAt,
		ElevationExpiresAt: expiresAt,
	}, time.Now().UTC())
	require.NoError(t, err)
	require.EqualValues(t, 1, n, "the session must exist and be live to elevate")
}

// elevateSession stamps the ordinary window on a session a test logged in by
// hand, for the harnesses that return a bare token rather than a
// userTestEnv. Call it BEFORE the first RPC on that token: there is no
// UserInfo cache entry to evict yet, which is why no registry is needed.
func elevateSession(t *testing.T, st store.Store, sessionID, userID string) {
	t.Helper()
	now := time.Now().UTC()
	elevateSessionRow(t, st, sessionID, userID, now, now.Add(auth.ElevationWindow))
}

// elevated is the env with a proven factor, which is what every sensitive
// RPC needs. Tests that exercise the gate itself use the plain env.
func (e *userTestEnv) elevated(t *testing.T) *userTestEnv {
	t.Helper()
	e.elevate(t)
	return e
}

func setupUserTest(t *testing.T) *userTestEnv {
	t.Helper()

	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	err = st.Migrator().Migrate(context.Background())
	require.NoError(t, err)

	mux := http.NewServeMux()
	// The bearer rung is wired so a test can present a CLI credential.
	// Without a validator the rung is unwired: a bearer answers
	// Unauthenticated at the interceptor and never reaches the gate.
	tv, tvErr := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, tvErr)
	interceptor, contexts := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st, TokenValidator: tv})
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
		client:   client,
		store:    st,
		token:    token,
		userID:   userID,
		tv:       tv,
		contexts: contexts,
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
	// Same bearer rung as setupUserTest, so bearerFor works on either env.
	tv, tvErr := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, tvErr)
	interceptor, contexts := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st, TokenValidator: tv})
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
		client:   client,
		store:    st,
		token:    token,
		userID:   userID,
		tv:       tv,
		contexts: contexts,
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

	// Verify that the update reached the database.
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

	env := setupUserTest(t).elevated(t)

	_, err := env.client.ChangePassword(context.Background(), authedReq(&leapmuxv1.ChangePasswordRequest{
		NewPassword: "newpass123",
	}, env.token))
	require.NoError(t, err)

	// Verify login works with new password.
	_, _, _, err = auth.Login(context.Background(), env.store, "testuser", "newpass123", auth.DefaultSessionDuration)
	assert.NoError(t, err)

	// Verify login with old password fails.
	_, _, _, err = auth.Login(context.Background(), env.store, "testuser", "testpass", auth.DefaultSessionDuration)
	require.Error(t, err)
}

// TestUserService_ChangePassword_LapsedElevationIsRefused is the deadline
// test that needs no sleep: the row carries a window that already closed, so
// the predicate -- which compares at the point of use, not when the UserInfo
// was cached -- must refuse.
func TestUserService_ChangePassword_LapsedElevationIsRefused(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)
	env.lapseElevation(t)

	_, err := env.client.ChangePassword(context.Background(), authedReq(&leapmuxv1.ChangePasswordRequest{
		NewPassword: "newpass123",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	_, _, _, err = auth.Login(context.Background(), env.store, "testuser", "testpass", auth.DefaultSessionDuration)
	require.NoError(t, err, "a refused change must leave the password alone")
}

// TestUserService_ElevateSession_WrongPasswordThenRight drives the password
// path end to end and pins that a wrong password answers Unauthenticated --
// the code the rate limiter counts -- while the refusal for a MISSING
// elevation is FailedPrecondition. Confusing the two is what would make a
// lapsed window spend the user's attempt budget.
func TestUserService_ElevateSession_WrongPasswordThenRight(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)

	_, err := env.client.ElevateSession(context.Background(), authedReq(&leapmuxv1.ElevateSessionRequest{
		CurrentPassword: "wrongpassword",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	resp, err := env.client.ElevateSession(context.Background(), authedReq(&leapmuxv1.ElevateSessionRequest{
		CurrentPassword: "testpass",
	}, env.token))
	require.NoError(t, err)
	require.NotNil(t, resp.Msg.GetElevationExpiresAt())
	assert.WithinDuration(t, time.Now().Add(auth.ElevationWindow),
		resp.Msg.GetElevationExpiresAt().AsTime(), time.Minute)

	// The window admits the sensitive action that was refused before it.
	_, err = env.client.ChangePassword(context.Background(), authedReq(&leapmuxv1.ChangePasswordRequest{
		NewPassword: "newpass123",
	}, env.token))
	require.NoError(t, err)
}

// TestUserService_DropElevation_ClosesTheWindow pins that ending an
// elevation takes effect immediately, and that ending one twice is success
// rather than NotFound: the caller asked for a state the session is already
// in.
func TestUserService_DropElevation_ClosesTheWindow(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t).elevated(t)

	_, err := env.client.DropElevation(context.Background(), authedReq(&leapmuxv1.DropElevationRequest{}, env.token))
	require.NoError(t, err)
	_, err = env.client.DropElevation(context.Background(), authedReq(&leapmuxv1.DropElevationRequest{}, env.token))
	require.NoError(t, err, "dropping an elevation nobody holds is the state the caller asked for")

	_, err = env.client.ChangePassword(context.Background(), authedReq(&leapmuxv1.ChangePasswordRequest{
		NewPassword: "newpass123",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

// TestUserService_ElevationSlidesOnSuccess pins the slide: a committed
// sensitive action pushes the deadline forward, so a user working through
// several settings answers one prompt rather than one per action.
func TestUserService_ElevationSlidesOnSuccess(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)
	// Elevated an hour ago: the window is live, and a slide has room to
	// move the deadline measurably.
	anchor := time.Now().UTC().Add(-time.Hour)
	env.setElevationRow(t, anchor, anchor.Add(auth.ElevationWindow))
	before := elevationDeadline(t, env)

	_, err := env.client.ChangePassword(context.Background(), authedReq(&leapmuxv1.ChangePasswordRequest{
		NewPassword: "newpass123",
	}, env.token))
	require.NoError(t, err)

	after := elevationDeadline(t, env)
	assert.True(t, after.After(before),
		"a committed sensitive action must slide the window forward (before=%s after=%s)", before, after)
}

// TestUserService_ElevationSlideIsCappedInSQL pins the ceiling, and pins it
// where it is enforced. The anchor is set far enough back that the ordinary
// window would run past the absolute cap; the clamp lives in the UPDATE, so
// no Go path can over-extend whatever it passes.
func TestUserService_ElevationSlideIsCappedInSQL(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)
	// The anchor is 30 minutes short of the ceiling, and the deadline is
	// still live: the ordinary slide would push it to now + ElevationWindow,
	// which is well past elevation_proven_at + ElevationMaxTotal.
	anchor := time.Now().UTC().Add(30 * time.Minute).Add(-auth.ElevationMaxTotal)
	ceiling := anchor.Add(auth.ElevationMaxTotal)
	env.setElevationRow(t, anchor, ceiling)

	_, err := env.client.ChangePassword(context.Background(), authedReq(&leapmuxv1.ChangePasswordRequest{
		NewPassword: "newpass123",
	}, env.token))
	require.NoError(t, err)

	after := elevationDeadline(t, env)
	assert.False(t, after.After(ceiling.Add(time.Second)),
		"the slide must clamp to elevation_proven_at + ElevationMaxTotal (deadline=%s ceiling=%s)", after, ceiling)
	assert.True(t, after.After(time.Now().UTC()),
		"the clamp must not close a window that is still below its ceiling")
}

// elevationDeadline reads the stored elevation_expires_at for the env's session.
func elevationDeadline(t *testing.T, env *userTestEnv) time.Time {
	t.Helper()
	row, err := env.store.Sessions().GetByID(context.Background(), env.token, time.Now().UTC())
	require.NoError(t, err)
	require.NotNil(t, row.ElevationExpiresAt, "the session must carry an elevation")
	return row.ElevationExpiresAt.UTC()
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
	// untouched — the whole-blob overwrite the per-key merge replaced.
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

	// The handler refuses an unknown field name rather than ignoring it.
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

	env := setupUserTest(t).elevated(t)

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

	// Verify that the change reached the DB.
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

	env := setupUserTest(t).elevated(t)

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

// --- RequestEmailChange: admin immediate change, unverified ---

// TestRequestEmailChange_Admin_ImmediateChangeLandsUnverified pins the half of
// the administrator rule that is NOT an exemption.
//
// An administrator still gets the change with no verification round trip,
// under any configuration. What they do not get is a raised email_verified:
// nobody confirmed the new address, and the column records exactly that.
// Raising it made an administrator's unconfirmed address a valid
// self-service account-recovery target, because RequestAccountRecovery reads the
// column and cannot take the sign-in exemption -- the same force this change
// removed from account creation and from the admin edit of another user.
func TestRequestEmailChange_Admin_ImmediateChangeLandsUnverified(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t).elevated(t)

	// The test user is an admin (IsAdmin=1 in setupUserTest).
	resp, err := env.client.RequestEmailChange(context.Background(), authedReq(&leapmuxv1.RequestEmailChangeRequest{
		NewEmail: "admin-new@example.com",
	}, env.token))
	require.NoError(t, err)
	assert.False(t, resp.Msg.GetVerificationRequired())

	user, err := env.store.Users().GetByID(context.Background(), env.userID)
	require.NoError(t, err)
	assert.Equal(t, "admin-new@example.com", user.Email)
	assert.False(t, user.EmailVerified,
		"an administrator's new address is no more confirmed than anybody else's")
}

// --- RequestEmailChange: non-admin, verification off, immediate unverified change ---

// TestRequestEmailChange_NonAdmin_VerificationNotRequired_LandsUnverified
// covers the non-admin case of the collapsed immediate-change branch: with
// verification off, the change applies immediately but the new address must
// land UNVERIFIED (verified == userInfo.IsAdmin == false), unlike the admin
// case which trusts it.
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
	// Start from a verified address so the interceptor does not refuse the call.
	require.NoError(t, st.Users().UpdateEmail(context.Background(), store.UpdateUserEmailParams{
		Email:         "old@example.com",
		EmailVerified: true,
		ID:            userID,
	}))

	userToken, _, _, err := auth.Login(context.Background(), st, "plainuser", "userpass", auth.DefaultSessionDuration)
	require.NoError(t, err)
	elevateSession(t, st, userToken, userID)

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
// they gave. The conditional mint is what closes that -- it refuses
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
	elevateSession(t, st, userToken, userID)

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

	env := setupUserTest(t).elevated(t)

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

// setupVerificationUserTestServer creates a test server where SMTP controls
// email verification (EmailVerificationEffective). It registers both
// UserService and AuthService. The interceptor and the services read the
// rule from the same settings. It returns a UserService client, st, and the
// admin token.
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
	elevateSession(t, st, userToken, userID)

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
		PendingEmail:            "verified@example.com",
		PendingEmailUnblockedAt: time.Now().Add(-5 * time.Minute).UTC(),
		PendingEmailToken:       verifyToken,
		PendingEmailExpiresAt:   ptrTime(time.Now().Add(1 * time.Hour).UTC()),
		ID:                      env.userID,
		Now:                     time.Now().UTC(),
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
		PendingEmail:            "lowercase@example.com",
		PendingEmailUnblockedAt: time.Now().Add(-5 * time.Minute).UTC(),
		PendingEmailToken:       verifyToken,
		PendingEmailExpiresAt:   ptrTime(time.Now().Add(1 * time.Hour).UTC()),
		ID:                      env.userID,
		Now:                     time.Now().UTC(),
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

	// Bad shape never passes Normalize → InvalidArgument, regardless
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
	// that callers cannot distinguish them — that closes a timing oracle on
	// "is there a code at all?". Assert both code AND message are equal.
	env := setupUserTest(t)

	expiredToken := verifycode.Generate()
	_, err := env.store.Users().SetPendingEmail(context.Background(), store.SetPendingEmailParams{
		PendingEmail:            "expired@example.com",
		PendingEmailUnblockedAt: time.Now().Add(-5 * time.Minute).UTC(),
		PendingEmailToken:       expiredToken,
		PendingEmailExpiresAt:   ptrTime(time.Now().Add(-1 * time.Hour).UTC()),
		ID:                      env.userID,
		Now:                     time.Now().UTC(),
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
		PendingEmail:            "live@example.com",
		PendingEmailUnblockedAt: time.Now().Add(-5 * time.Minute).UTC(),
		PendingEmailToken:       liveToken,
		PendingEmailExpiresAt:   ptrTime(time.Now().Add(1 * time.Hour).UTC()),
		ID:                      env.userID,
		Now:                     time.Now().UTC(),
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
		PendingEmail:            "",
		PendingEmailUnblockedAt: time.Now().Add(-5 * time.Minute).UTC(),
		PendingEmailToken:       verifyToken,
		PendingEmailExpiresAt:   ptrTime(time.Now().Add(1 * time.Hour).UTC()),
		ID:                      env.userID,
		Now:                     time.Now().UTC(),
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
		PendingEmail:            "burned@example.com",
		PendingEmailUnblockedAt: time.Now().Add(-5 * time.Minute).UTC(),
		PendingEmailToken:       live,
		PendingEmailExpiresAt:   ptrTime(time.Now().Add(1 * time.Hour).UTC()),
		ID:                      env.userID,
		Now:                     time.Now().UTC(),
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

// setupResendUserTest provisions a UserService backed by a mailSenderDouble
// so tests can assert the resent email's recipient + body.
func setupResendUserTest(t *testing.T) (*userTestEnv, *mailSenderDouble) {
	t.Helper()
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	require.NoError(t, st.Migrator().Migrate(context.Background()))

	rec := &mailSenderDouble{}
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
	// User has no pending email — there is nothing to resend.
	_, err := env.client.ResendVerificationEmail(context.Background(), authedReq(&leapmuxv1.ResendVerificationEmailRequest{}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestResendVerificationEmail_RotatesCodeAndSends(t *testing.T) {
	t.Parallel()

	env, sender := setupResendUserTest(t)

	// Seed a pending row with an "old" expires_at far enough back that
	// the cooldown window already elapsed (TTL is 30min, cooldown 60s — set
	// deadline elapsed 5 minutes ago).
	originalCode := verifycode.Generate()
	minted, err := env.store.Users().SetPendingEmail(context.Background(), store.SetPendingEmailParams{
		ID:                      env.userID,
		PendingEmail:            "u@example.com",
		PendingEmailUnblockedAt: time.Now().Add(-5 * time.Minute).UTC(),
		PendingEmailToken:       originalCode,
		PendingEmailExpiresAt:   ptrTime(time.Now().Add(25 * time.Minute).UTC()),
		Now:                     time.Now().UTC(),
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

	// Seed a pending row issued just now: the cooldown must reject a
	// back-to-back resend so a runaway client (or hostile caller) cannot
	// flood the user's inbox.
	env, _ := setupResendUserTest(t)

	now := time.Now().UTC()
	minted, err := env.store.Users().SetPendingEmail(context.Background(), store.SetPendingEmailParams{
		ID:                      env.userID,
		PendingEmail:            "u@example.com",
		PendingEmailToken:       verifycode.Generate(),
		PendingEmailExpiresAt:   ptrTime(now.Add(30 * time.Minute)),
		PendingEmailUnblockedAt: now.Add(time.Minute),
		Now:                     now,
	})
	require.NoError(t, err)
	require.True(t, minted)

	_, err = env.client.ResendVerificationEmail(context.Background(), authedReq(&leapmuxv1.ResendVerificationEmailRequest{}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
}

// TestResendVerificationEmail_BurnedBudgetKeepsCooldown pins the closed
// hole. Burning the 5-guess budget force-expires the code in SQL, and an
// expiry-derived gate read that as "issued a full lifetime ago" and let a
// resend land immediately -- burn and resend became a mail loop at seven
// cheap RPCs per email. The gate reads the issued-at column, which no
// attempt path moves, so a burned code waits out the same cooldown a live
// one does.
func TestResendVerificationEmail_BurnedBudgetKeepsCooldown(t *testing.T) {
	t.Parallel()

	env, _ := setupResendUserTest(t)
	ctx := context.Background()

	now := time.Now().UTC()
	minted, err := env.store.Users().SetPendingEmail(ctx, store.SetPendingEmailParams{
		ID:                      env.userID,
		PendingEmail:            "u@example.com",
		PendingEmailToken:       verifycode.Generate(),
		PendingEmailExpiresAt:   ptrTime(now.Add(30 * time.Minute)),
		PendingEmailUnblockedAt: now.Add(time.Minute),
		Now:                     now,
	})
	require.NoError(t, err)
	require.True(t, minted)

	// Burn the whole wrong-guess budget; the 6th attempt force-expires the
	// code in SQL (the expiry moves to now).
	for i := 0; i < 6; i++ {
		_, err = env.client.VerifyEmail(ctx, authedReq(&leapmuxv1.VerifyEmailRequest{
			VerificationToken: verifycode.Generate(),
		}, env.token))
		require.Error(t, err, "a wrong code must never verify")
	}

	row, err := env.store.Users().GetByID(ctx, env.userID)
	require.NoError(t, err)
	require.NotNil(t, row.PendingEmailExpiresAt)
	assert.False(t, time.Now().UTC().Before(row.PendingEmailExpiresAt.UTC()),
		"the burned code must be force-expired for this pin to mean anything")

	// The code is expired, but it was issued seconds ago: the resend must
	// still hit the cooldown.
	_, err = env.client.ResendVerificationEmail(ctx, authedReq(&leapmuxv1.ResendVerificationEmailRequest{}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err),
		"burning the guess budget must not reset the resend cooldown")
}

func TestVerifyEmail_EmailTakenSinceRequest(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)

	verifyToken := verifycode.Generate()
	_, err := env.store.Users().SetPendingEmail(context.Background(), store.SetPendingEmailParams{
		PendingEmail:            "contested@example.com",
		PendingEmailUnblockedAt: time.Now().Add(-5 * time.Minute).UTC(),
		PendingEmailToken:       verifyToken,
		PendingEmailExpiresAt:   ptrTime(time.Now().Add(1 * time.Hour).UTC()),
		ID:                      env.userID,
		Now:                     time.Now().UTC(),
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
	// does not have a matching token, so they get the same generic
	// NotFound as anyone typing a wrong code. There is nothing to leak.
	env := setupUserTest(t)

	victimToken := verifycode.Generate()
	_, err := env.store.Users().SetPendingEmail(context.Background(), store.SetPendingEmailParams{
		PendingEmail:            "stolen@example.com",
		PendingEmailUnblockedAt: time.Now().Add(-5 * time.Minute).UTC(),
		PendingEmailToken:       victimToken,
		PendingEmailExpiresAt:   ptrTime(time.Now().Add(1 * time.Hour).UTC()),
		ID:                      env.userID,
		Now:                     time.Now().UTC(),
	})
	require.NoError(t, err)

	// Create a different user and log in as them. Important: the
	// attacker has *no* pending row of their own, so any submission
	// they make reaches the FailedPrecondition path. Give them one too so
	// this exercises the actual mismatch case.
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
		PendingEmail:            "attacker@example.com",
		PendingEmailUnblockedAt: time.Now().Add(-5 * time.Minute).UTC(),
		PendingEmailToken:       attackerOwnToken,
		PendingEmailExpiresAt:   ptrTime(time.Now().Add(1 * time.Hour).UTC()),
		ID:                      attackerID,
		Now:                     time.Now().UTC(),
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
	env.elevate(t)
	_, err = env.client.ChangePassword(context.Background(), authedReq(&leapmuxv1.ChangePasswordRequest{
		NewPassword: "newpass123",
	}, env.token))
	require.NoError(t, err)

	// Original session should still be valid (it is the current session).
	_, err = auth.ValidateToken(context.Background(), env.store, env.token)
	assert.NoError(t, err)

	// The handler should invalidate the other session.
	_, err = auth.ValidateToken(context.Background(), env.store, otherSession)
	assert.Error(t, err, "other sessions should be invalidated after password change")
}

// onUserAuthTxStore fires a one-shot side effect when a caller opens a
// user-auth transaction, letting a test inject a concurrent mutation into
// ChangePassword.
type onUserAuthTxStore struct {
	store.Store
	once   sync.Once
	before func()
}

func (s *onUserAuthTxStore) RunInUserAuthTransaction(ctx context.Context, userID userid.UserID, fn func(tx store.Store) error) error {
	s.once.Do(s.before)
	return s.Store.RunInUserAuthTransaction(ctx, userID, fn)
}

// concurrentSessionRemovalEnv is the fixture both tests below share: an
// elevated session, and a UserService whose user-auth transaction takes the
// acting session away at the instant the handler acquires the lock.
type concurrentSessionRemovalEnv struct {
	store     store.Store
	token     string
	sessionID string
	client    leapmuxv1connect.UserServiceClient
	// removed reports what the injected removal did. Each test asserts it,
	// or a removal that silently matched no row would make the whole
	// scenario vacuous.
	removed   func() int64
	removeErr func() error
}

// newConcurrentSessionRemovalEnv wires that fixture. `remove` runs ONCE,
// when ChangePassword opens its user-auth transaction: neither
// Sessions().Delete nor Sessions().Revoke contends on that lock, so either
// can delete the acting session while the mutation waits on it.
func newConcurrentSessionRemovalEnv(t *testing.T, remove func(ctx context.Context, st store.Store, sessionID string) (int64, error)) *concurrentSessionRemovalEnv {
	t.Helper()
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

	// ChangePassword needs an elevated session, so stamp one before the
	// concurrent removal these tests are actually about.
	now := time.Now().UTC()
	elevated, err := st.Sessions().Elevate(ctx, store.ElevateSessionParams{
		SessionID:          sessionID,
		UserID:             userid.MustNew(userID),
		ElevationProvenAt:  now,
		ElevationExpiresAt: now.Add(auth.ElevationWindow),
	}, now)
	require.NoError(t, err)
	require.EqualValues(t, 1, elevated)

	// The interceptor validates the token against the live session, then the
	// handler opens its transaction on the wrapped store -- at which point
	// the injected removal runs.
	var removedN int64
	var removeErr error
	hooked := &onUserAuthTxStore{Store: st, before: func() {
		removedN, removeErr = remove(ctx, st, sessionID)
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

	return &concurrentSessionRemovalEnv{
		store:     st,
		token:     token,
		sessionID: sessionID,
		client:    leapmuxv1connect.NewUserServiceClient(server.Client(), server.URL, connect.WithGRPC()),
		removed:   func() int64 { return removedN },
		removeErr: func() error { return removeErr },
	}
}

// TestRequestEmailChange_RefusesAConcurrentActingSessionRevocation is the
// regression for the widest of the three windows the elevation gate left open.
//
// The gate at the top of the handler answers from a CACHED UserInfo, and a
// revoke raised on another hub reaches this process only on the revocation
// watcher's next sweep -- so "elevated" could be true of a session an
// administrator already took away. The account email receives the
// recovery link, so a change that landed on that authority gave the
// account away.
//
// It is the SAME shape the passkey mutations use: the write moved inside the
// user-auth transaction, and the handler re-reads the authority under the
// lock. Only the
// SMTP send stayed outside, because it must never hold SQLite's single writer
// lock.
//
// The first assertion is what fails against the pre-fix code: the handler
// opened no user-auth transaction at all, so the injected revoke had nothing
// to hook, `removed()` was zero, and the RPC went on to write the address.
func TestRequestEmailChange_RefusesAConcurrentActingSessionRevocation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	env := newConcurrentSessionRemovalEnv(t, func(ctx context.Context, st store.Store, sessionID string) (int64, error) {
		return st.Sessions().Revoke(ctx, sessionID)
	})

	_, err := env.client.RequestEmailChange(ctx, authedReq(&leapmuxv1.RequestEmailChangeRequest{
		NewEmail: "moved@example.com",
	}, env.token))

	require.NoError(t, env.removeErr())
	require.EqualValues(t, 1, env.removed(),
		"the handler must open a user-auth transaction; the revoke had nothing to hook otherwise")
	require.Error(t, err, "a revoked acting session must not move the recovery address")

	// Nothing landed: not the address, and not a pending one.
	users, err := env.store.Users().ListAll(ctx, store.ListAllUsersParams{PageParams: store.PageParams{Limit: 10}})
	require.NoError(t, err)
	require.Len(t, users.Rows, 1)
	assert.Empty(t, users.Rows[0].PendingEmail, "the pending address must not survive the refusal")
	assert.Empty(t, users.Rows[0].Email)
}

// TestChangePassword_ToleratesConcurrentActingSessionDeletion verifies that a
// same-user logout deleting the acting session mid-request (Sessions().Delete
// does not contend on the user-auth lock ChangePassword holds) does not roll
// back an otherwise-valid password change: the handler tolerates
// RefreshAuthGeneration matching zero rows for the now-absent session rather
// than treating it as fatal. Against the pre-fix
// `n != 1` guard this request failed with a spurious CodeInternal and left the
// password unchanged.
func TestChangePassword_ToleratesConcurrentActingSessionDeletion(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	env := newConcurrentSessionRemovalEnv(t, func(ctx context.Context, st store.Store, sessionID string) (int64, error) {
		return st.Sessions().Delete(ctx, sessionID)
	})

	_, err := env.client.ChangePassword(ctx, authedReq(&leapmuxv1.ChangePasswordRequest{
		NewPassword: "newpass123",
	}, env.token))
	require.NoError(t, err, "password change must survive a concurrently-deleted acting session")
	require.NoError(t, env.removeErr())
	require.Equal(t, int64(1), env.removed(), "test must have deleted the acting session mid-request")

	// The password actually changed: the old one no longer authenticates and the
	// new one does.
	_, _, _, err = auth.Login(ctx, env.store, "testuser", "testpass", auth.DefaultSessionDuration)
	require.Error(t, err, "old password must be rejected after the change")
	_, _, _, err = auth.Login(ctx, env.store, "testuser", "newpass123", auth.DefaultSessionDuration)
	require.NoError(t, err, "new password must authenticate after the change")
}

// TestChangePassword_RefusesConcurrentActingSessionRevocation is the other
// half, and the reason the two removals are separate store verbs.
//
// An administrator revoking THIS ONE session must not sign the account's
// other sessions out, so it leaves the credential epoch where it is -- and
// the epoch is what recheckCredentialEpochUnderLock reads. Both removals
// delete the same row, so the absent row cannot separate them either. The
// tolerance above therefore covered the revoke as well, and a queued
// password change committed on the authority of a session an administrator
// already took away. The revocation event kind is what separates them.
func TestChangePassword_RefusesConcurrentActingSessionRevocation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	env := newConcurrentSessionRemovalEnv(t, func(ctx context.Context, st store.Store, sessionID string) (int64, error) {
		return st.Sessions().Revoke(ctx, sessionID)
	})

	_, err := env.client.ChangePassword(ctx, authedReq(&leapmuxv1.ChangePasswordRequest{
		NewPassword: "newpass123",
	}, env.token))
	require.Error(t, err, "a revoked acting session must not authorize the change it queued")
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.NoError(t, env.removeErr())
	require.Equal(t, int64(1), env.removed(), "test must have revoked the acting session mid-request")

	// The password did NOT change: the old one still authenticates and the
	// new one does not.
	_, _, _, err = auth.Login(ctx, env.store, "testuser", "testpass", auth.DefaultSessionDuration)
	require.NoError(t, err, "the refused change must leave the old password working")
	_, _, _, err = auth.Login(ctx, env.store, "testuser", "newpass123", auth.DefaultSessionDuration)
	require.Error(t, err, "the refused change must not have committed the new password")
}

// --- ChangePassword tests for OAuth users ---

// TestChangePassword_OAuthUser_SetsFirstPasswordWithoutElevation is the
// deadlock guard. An account with no password and no passkey has nothing to
// elevate WITH, so requiring elevation here would lock it out for ever: it
// could never attach the credential that would let it elevate. The
// first-credential rule is a sibling branch checked FIRST, not a fallback.
func TestChangePassword_OAuthUser_SetsFirstPasswordWithoutElevation(t *testing.T) {
	t.Parallel()

	env := setupOAuthUserTest(t)

	// Deliberately NOT elevated.
	_, err := env.client.ChangePassword(context.Background(), authedReq(&leapmuxv1.ChangePasswordRequest{
		NewPassword: "newpass123",
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

func TestChangePassword_PasswordUser_RequiresElevatedSession(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)

	_, err := env.client.ChangePassword(context.Background(), authedReq(&leapmuxv1.ChangePasswordRequest{
		NewPassword: "newpass123",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err),
		"the refusal must not be Unauthenticated: the frontend reads that as signed out")
}

// TestChangePassword_PasskeyOnly_RequiresElevation pins the OTHER side of
// the fork: once the account holds a passkey it has something to prove, so
// the first-credential rule no longer applies and elevation is required.
func TestChangePassword_PasskeyOnly_RequiresElevation(t *testing.T) {
	t.Parallel()

	env := setupOAuthUserTest(t)
	seedPasskeyCredential(t, env.store, env.userID, "Phone")

	_, err := env.client.ChangePassword(context.Background(), authedReq(&leapmuxv1.ChangePasswordRequest{
		NewPassword: "newpass123",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	env.elevate(t)
	_, err = env.client.ChangePassword(context.Background(), authedReq(&leapmuxv1.ChangePasswordRequest{
		NewPassword: "newpass123",
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

	env := setupUserTest(t).elevated(t)

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

	env := setupUserTest(t).elevated(t)

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

	env := setupOAuthUserTest(t).elevated(t)

	err := env.store.OAuthProviders().Create(context.Background(), store.CreateOAuthProviderParams{
		ID: "github-3", ProviderType: "github", Name: "GitHub",
		ClientID: "c1", ClientSecret: []byte("s1"), Scopes: "read:user", Enabled: true,
	})
	require.NoError(t, err)
	err = env.store.OAuthUserLinks().Create(context.Background(), store.CreateOAuthUserLinkParams{
		UserID: userid.MustNew(env.userID), ProviderID: "github-3", ProviderSubject: "gh-sub",
	})
	require.NoError(t, err)

	// The rule should block this — last link and no password.
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

// The last-login-method rule counts what would REMAIN, not how many links
// exist, and it runs again under the user-auth lock.
//
// It used to test `len(links) <= 1` from a list read before the lock, so two
// requests each removing one of the account's last two links both passed it
// and the account kept no login method at all. Re-running the rule
// inside the transaction is what closes that, and counting the remainder is
// what makes the rule correct against the locked list -- which cannot assume
// the request specifies a link the account still holds.
func TestUnlinkOAuthProvider_ConcurrentRequestsCannotStripEveryLoginMethod(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	env := setupOAuthUserTest(t).elevated(t)

	// Two links, no password, no passkey: removing EITHER is allowed, and
	// removing BOTH is not.
	for _, id := range []string{"gh-a", "gh-b"} {
		require.NoError(t, env.store.OAuthProviders().Create(ctx, store.CreateOAuthProviderParams{
			ID: id, ProviderType: "github", Name: id,
			ClientID: "c1", ClientSecret: []byte("s1"), Scopes: "read:user", Enabled: true,
		}))
		require.NoError(t, env.store.OAuthUserLinks().Create(ctx, store.CreateOAuthUserLinkParams{
			UserID: userid.MustNew(env.userID), ProviderID: id, ProviderSubject: "sub-" + id,
		}))
	}

	// The first removal is allowed: one link remains.
	_, err := env.client.UnlinkOAuthProvider(ctx, authedReq(&leapmuxv1.UnlinkOAuthProviderRequest{
		ProviderId: "gh-a",
	}, env.token))
	require.NoError(t, err)

	// The second is refused: nothing would remain. This is the state a lost
	// race produced when the rule counted a list read before the lock.
	_, err = env.client.UnlinkOAuthProvider(ctx, authedReq(&leapmuxv1.UnlinkOAuthProviderRequest{
		ProviderId: "gh-b",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "set a password first")

	links, err := env.store.OAuthUserLinks().ListByUser(ctx, userid.MustNew(env.userID))
	require.NoError(t, err)
	require.Len(t, links, 1, "the account keeps the login method it has left")
	assert.Equal(t, "gh-b", links[0].ProviderID)
}

// A link whose provider an administrator DISABLED is not a login method, so
// it cannot be the reason an unlink is allowed.
//
// The rule counted links by id alone, so an account with no password, no
// passkey and two links -- one live, one disabled -- could remove the LIVE one
// and be told that was fine. Nothing behind the survivor works:
// loadEnabledProvider answers 403 "provider disabled" at every OAuth leg, so
// the account is locked out permanently, which is the exact outcome this rule
// exists to refuse.
func TestUnlinkOAuthProvider_ADisabledLinkIsNotALoginMethod(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	env := setupOAuthUserTest(t).elevated(t)

	for _, p := range []struct {
		id      string
		enabled bool
	}{
		{"gh-live", true},
		{"gh-dead", false},
	} {
		require.NoError(t, env.store.OAuthProviders().Create(ctx, store.CreateOAuthProviderParams{
			ID: p.id, ProviderType: "github", Name: p.id,
			ClientID: "c1", ClientSecret: []byte("s1"), Scopes: "read:user", Enabled: p.enabled,
		}))
		require.NoError(t, env.store.OAuthUserLinks().Create(ctx, store.CreateOAuthUserLinkParams{
			UserID: userid.MustNew(env.userID), ProviderID: p.id, ProviderSubject: "sub-" + p.id,
		}))
	}

	// The disabled link is present, so a rule that counted ids alone would
	// admit this. It must not: removing the live one leaves nothing usable.
	_, err := env.client.UnlinkOAuthProvider(ctx, authedReq(&leapmuxv1.UnlinkOAuthProviderRequest{
		ProviderId: "gh-live",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "set a password first")

	// The DISABLED one may still go: removing it takes away nothing the
	// account could have used, and the owner must be able to detach it.
	_, err = env.client.UnlinkOAuthProvider(ctx, authedReq(&leapmuxv1.UnlinkOAuthProviderRequest{
		ProviderId: "gh-dead",
	}, env.token))
	require.NoError(t, err)

	links, err := env.store.OAuthUserLinks().ListByUser(ctx, userid.MustNew(env.userID))
	require.NoError(t, err)
	require.Len(t, links, 1)
	assert.Equal(t, "gh-live", links[0].ProviderID)
}

func TestUnlinkOAuthProvider_NotFound(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t).elevated(t)

	_, err := env.client.UnlinkOAuthProvider(context.Background(), authedReq(&leapmuxv1.UnlinkOAuthProviderRequest{
		ProviderId: "nonexistent",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// --- The plain elevation gate: RequestEmailChange and UnlinkOAuthProvider ---
//
// Both move a durable identity. The account email receives the
// recovery link, and on an admin edit it lands VERIFIED with no round
// trip; an OAuth link is the login method -- and for an OAuth-only account,
// the very factor that account elevates with. A session alone must not move
// either.

// assertElevationRequired pins the refusal SHAPE, not just the code.
//
// The marker is what a client keys on to open a step-up prompt, and
// FailedPrecondition is what stops the frontend's global interceptor from
// reading the refusal as a dead session and signing the user out. A test
// that checked the code alone would pass while either half was missing.
func assertElevationRequired(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, "1", connectErr.Meta().Get(service.ElevationRequiredHeader),
		"the refusal must carry the marker a client opens the prompt for")
}

// assertCannotVerify is the OTHER refusal, and the marker is the whole
// difference. A caller that cannot prove a factor on this surface must never
// receive a prompt, because the retry would meet the same answer: the client
// would run a browser ceremony and report the same refusal after it.
func assertCannotVerify(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	assert.Empty(t, connectErr.Meta().Get(service.ElevationRequiredHeader),
		"a credential with no remedy must not be offered a prompt")
}

// assertOutOfScope is the THIRD refusal, and it is the one an app meets on the
// account's own authenticators.
//
// Those procedures are ScopeNever: they add a factor, remove a sign-in method,
// or manage another credential, and every one of those outlives the app's
// connection. No consent screen offers them, so no ceremony can ever grant
// them -- which is why the answer must carry NO elevation marker. A marker
// here would send the CLI to a browser for a permission the browser cannot
// give, and the retry would meet this same refusal.
func assertOutOfScope(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	assert.Empty(t, connectErr.Meta().Get(service.ElevationRequiredHeader),
		"a refusal no ceremony can clear must not be offered a prompt")
}

// A CLI credential carries a step-up window of its own, proven in a browser
// through /oauth/step-up. Every restricted RPC answers it
// the SAME way, and the marker says so: the remedy is real, so the CLI runs
// the ceremony and retries rather than reporting a refusal it cannot act on.
//
// The step-up MUTATION surface answers a command-line credential at one of TWO
// rungs, and which one is the point of this test.
//
// ChangePassword takes account:write, so a credential that holds it reaches the
// elevation gate and gets the marker: the remedy is real, and the CLI runs the
// step-up leg. Everything that manages the account's AUTHENTICATORS -- the
// passkey verbs, the recovery address, an unlinked provider -- is ScopeNever,
// so the scope rung answers first and carries no marker. Those permissions
// appear on no consent screen, so no browser ceremony can grant them, and a
// marker would send the CLI to a prompt whose retry meets the same refusal.
//
// The two answers must stay distinguishable, because a client picks its next
// move from the marker alone.
//
// The account shape below matters: it holds a password, so the account has a
// factor to prove and requireElevation is the rule that applies where the
// scope rung lets the request through.
// TestGatedRPCs_RefuseACommandLineCredentialWithNothingToElevateWith covers
// the other shape, where the answer is different for a stated reason.
//
// This is unreachable from the shipped CLI, which calls no restricted UserService
// procedure, and from a worker-spawned tab, whose delegation bearer the
// interceptor refuses several layers earlier. This pins the rule anyway: the
// hub must not depend on its clients to keep a refusal honest.
func TestGatedRPCs_AnswerACommandLineCredentialByItsRemedy(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)
	bearer := env.bearerFor(t)
	require.NoError(t, env.store.Users().UpdateEmail(context.Background(), store.UpdateUserEmailParams{
		Email: "old@example.com", EmailVerified: true, ID: env.userID,
	}))

	_, err := env.client.ChangePassword(context.Background(), bearerReq(&leapmuxv1.ChangePasswordRequest{
		NewPassword: "newpass123",
	}, bearer))
	assertElevationRequired(t, err)

	// The recovery address and the sign-in methods are ScopeNever, so the
	// scope rung answers before the elevation gate runs. That is a DIFFERENT
	// refusal with a different remedy, and assertOutOfScope pins the
	// difference the client acts on: no marker, so no prompt.
	_, err = env.client.RequestEmailChange(context.Background(), bearerReq(&leapmuxv1.RequestEmailChangeRequest{
		NewEmail: "moved@example.com",
	}, bearer))
	assertOutOfScope(t, err)

	_, err = env.client.UnlinkOAuthProvider(context.Background(), bearerReq(&leapmuxv1.UnlinkOAuthProviderRequest{
		ProviderId: "github-1",
	}, bearer))
	assertOutOfScope(t, err)

	_, err = env.client.RenamePasskey(context.Background(), bearerReq(&leapmuxv1.RenamePasskeyRequest{
		Id: "pk-1", FriendlyName: "Renamed",
	}, bearer))
	assertOutOfScope(t, err)

	// The whole passkey-management surface answers alike.
	_, err = env.client.DeletePasskey(context.Background(), bearerReq(&leapmuxv1.DeletePasskeyRequest{
		Id: "pk-1",
	}, bearer))
	assertOutOfScope(t, err)

	_, err = env.client.DeactivatePasskeyAuth(context.Background(),
		bearerReq(&leapmuxv1.DeactivatePasskeyAuthRequest{NewPassword: "newpass123"}, bearer))
	assertOutOfScope(t, err)

	// Registration is refused at the same rung for the same reason.
	_, err = env.client.BeginPasskeyRegistration(context.Background(),
		bearerReq(&leapmuxv1.BeginPasskeyRegistrationRequest{}, bearer))
	assertOutOfScope(t, err)

	// No path of the seven wrote anything.
	user, err := env.store.Users().GetByID(context.Background(), env.userID)
	require.NoError(t, err)
	assert.Equal(t, "old@example.com", user.Email)
	_, _, _, loginErr := auth.Login(context.Background(), env.store, "testuser", "testpass", auth.DefaultSessionDuration)
	require.NoError(t, loginErr, "a refused change must leave the password alone")
}

// TestGatedRPCs_RefuseACommandLineCredentialWithNothingToElevateWith is the
// one shape where the step-up mutations still answer a command-line
// credential without a prompt.
//
// The account holds no password and no passkey, so the elevation branch has
// nothing to read and the first-credential rule decides instead. That rule
// rests on a recent SIGN-IN, and the hub mints a command-line credential once
// and lets it live until a revoke ends it -- so its creation time says
// nothing about who holds it now. The message states the remedy the caller
// can act on, and the absent marker keeps the client from opening a prompt
// whose retry would meet the same answer.
func TestGatedRPCs_RefuseACommandLineCredentialWithNothingToElevateWith(t *testing.T) {
	t.Parallel()

	env := setupOAuthUserTest(t)
	bearer := env.bearerFor(t)

	user, err := env.store.Users().GetByID(context.Background(), env.userID)
	require.NoError(t, err)
	require.False(t, user.PasswordSet, "precondition: the account has nothing to elevate with")

	_, err = env.client.ChangePassword(context.Background(), bearerReq(&leapmuxv1.ChangePasswordRequest{
		NewPassword: "newpass123",
	}, bearer))
	assertCannotVerify(t, err)

	// Registering a passkey ADDS an authenticator that outlives the app, so it
	// is out of scope for a credential rather than a factor it could prove.
	_, err = env.client.BeginPasskeyRegistration(context.Background(),
		bearerReq(&leapmuxv1.BeginPasskeyRegistrationRequest{}, bearer))
	assertOutOfScope(t, err)

	after, err := env.store.Users().GetByID(context.Background(), env.userID)
	require.NoError(t, err)
	assert.False(t, after.PasswordSet, "a refused change must attach no first password")
}

// TestGatedRPCs_TellASessionToElevate is the contrast that gives the test
// above its meaning: the SAME procedures, refused with the marker, because
// this caller has a remedy.
func TestGatedRPCs_TellASessionToElevate(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)
	require.NoError(t, env.store.Users().UpdateEmail(context.Background(), store.UpdateUserEmailParams{
		Email: "old@example.com", EmailVerified: true, ID: env.userID,
	}))

	_, err := env.client.ChangePassword(context.Background(), authedReq(&leapmuxv1.ChangePasswordRequest{
		NewPassword: "newpass123",
	}, env.token))
	assertElevationRequired(t, err)

	_, err = env.client.RequestEmailChange(context.Background(), authedReq(&leapmuxv1.RequestEmailChangeRequest{
		NewEmail: "moved@example.com",
	}, env.token))
	assertElevationRequired(t, err)

	_, err = env.client.UnlinkOAuthProvider(context.Background(), authedReq(&leapmuxv1.UnlinkOAuthProviderRequest{
		ProviderId: "github-1",
	}, env.token))
	assertElevationRequired(t, err)

	_, err = env.client.RenamePasskey(context.Background(), authedReq(&leapmuxv1.RenamePasskeyRequest{
		Id: "pk-1", FriendlyName: "Renamed",
	}, env.token))
	assertElevationRequired(t, err)

	// The four passkey legs the list above used to omit. userProcedureElevation
	// classifies each one as protectedByPasskeyManagementAuth, and that table
	// records the DECISION -- it cannot observe the handler, so a leg whose
	// gate was never wired would sit there classified and ship with no gate.
	// These are what observe it.
	//
	// The hub refuses every one BEFORE its own argument validation, which is
	// the order that matters: an un-elevated caller must not learn whether a
	// passkey id exists, and a Begin must refuse before it writes a
	// ceremony row.
	_, err = env.client.BeginPasskeyRegistration(context.Background(),
		authedReq(&leapmuxv1.BeginPasskeyRegistrationRequest{}, env.token))
	assertElevationRequired(t, err)

	_, err = env.client.FinishPasskeyRegistration(context.Background(), authedReq(&leapmuxv1.FinishPasskeyRegistrationRequest{
		SessionId: "ceremony-1", CredentialJson: "{}", FriendlyName: "Added",
	}, env.token))
	assertElevationRequired(t, err)

	_, err = env.client.DeletePasskey(context.Background(), authedReq(&leapmuxv1.DeletePasskeyRequest{
		Id: "pk-1",
	}, env.token))
	assertElevationRequired(t, err)

	_, err = env.client.DeactivatePasskeyAuth(context.Background(),
		authedReq(&leapmuxv1.DeactivatePasskeyAuthRequest{}, env.token))
	assertElevationRequired(t, err)
}

func TestRequestEmailChange_UnelevatedIsRefused(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)
	require.NoError(t, env.store.Users().UpdateEmail(context.Background(), store.UpdateUserEmailParams{
		Email: "old@example.com", EmailVerified: true, ID: env.userID,
	}))

	_, err := env.client.RequestEmailChange(context.Background(), authedReq(&leapmuxv1.RequestEmailChangeRequest{
		NewEmail: "attacker@example.com",
	}, env.token))
	assertElevationRequired(t, err)

	user, err := env.store.Users().GetByID(context.Background(), env.userID)
	require.NoError(t, err)
	assert.Equal(t, "old@example.com", user.Email, "a refused change must leave the address alone")
	assert.Empty(t, user.PendingEmail, "a refused change must not leave a pending address either")
}

// TestRequestEmailChange_LapsedElevationIsRefused is the deadline test that
// needs no sleep: the window on the row already closed.
func TestRequestEmailChange_LapsedElevationIsRefused(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)
	env.lapseElevation(t)

	_, err := env.client.RequestEmailChange(context.Background(), authedReq(&leapmuxv1.RequestEmailChangeRequest{
		NewEmail: "attacker@example.com",
	}, env.token))
	assertElevationRequired(t, err)
}

// TestRequestEmailChange_UnelevatedCannotProbeWhoOwnsAnAddress is why the
// gate sits ABOVE the availability check rather than below it. The probe
// answers a question about OTHER accounts, so an un-elevated caller must get
// the same refusal for a taken address as for a free one -- otherwise the
// RPC is an account-enumeration oracle that needs no factor at all.
func TestRequestEmailChange_UnelevatedCannotProbeWhoOwnsAnAddress(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)
	other := id.Generate()
	require.NoError(t, env.store.Users().Create(context.Background(), store.CreateUserParams{
		ID: other, Username: "otheruser", PasswordHash: "x", DisplayName: "Other", PasswordSet: true,
	}))
	require.NoError(t, env.store.Users().UpdateEmail(context.Background(), store.UpdateUserEmailParams{
		Email: "taken@example.com", EmailVerified: true, ID: other,
	}))

	_, takenErr := env.client.RequestEmailChange(context.Background(), authedReq(&leapmuxv1.RequestEmailChangeRequest{
		NewEmail: "taken@example.com",
	}, env.token))
	_, freeErr := env.client.RequestEmailChange(context.Background(), authedReq(&leapmuxv1.RequestEmailChangeRequest{
		NewEmail: "free@example.com",
	}, env.token))

	assertElevationRequired(t, takenErr)
	assertElevationRequired(t, freeErr)
	assert.Equal(t, freeErr.Error(), takenErr.Error(),
		"a taken address and a free one must be indistinguishable without the factor")
}

// TestRequestEmailChange_MalformedAddressIsReportedWithoutAPrompt pins the
// other side of that ordering. The syntax checks run FIRST, because a
// malformed address is the caller's own typing: prompting for a factor and
// then reporting the typo would make the user prove themselves for nothing.
func TestRequestEmailChange_MalformedAddressIsReportedWithoutAPrompt(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)

	for _, bad := range []string{"", "not-an-email"} {
		_, err := env.client.RequestEmailChange(context.Background(), authedReq(&leapmuxv1.RequestEmailChangeRequest{
			NewEmail: bad,
		}, env.token))
		require.Error(t, err)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "input %q", bad)
	}
}

// TestRequestEmailChange_SlidesTheWindow: a user who changes their address
// and then does a second sensitive action answers ONE prompt, which is the
// property the window exists for.
func TestRequestEmailChange_SlidesTheWindow(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)
	anchor := time.Now().UTC()
	// A window that is live but nearly closed, so a slide is visible.
	env.setElevationRow(t, anchor, anchor.Add(5*time.Minute))
	require.NoError(t, env.store.Users().UpdateEmail(context.Background(), store.UpdateUserEmailParams{
		Email: "old@example.com", EmailVerified: true, ID: env.userID,
	}))

	_, err := env.client.RequestEmailChange(context.Background(), authedReq(&leapmuxv1.RequestEmailChangeRequest{
		NewEmail: "new@example.com",
	}, env.token))
	require.NoError(t, err)

	row, err := env.store.Sessions().GetByID(context.Background(), env.token, time.Now().UTC())
	require.NoError(t, err)
	require.NotNil(t, row.ElevationExpiresAt)
	assert.True(t, row.ElevationExpiresAt.After(anchor.Add(time.Hour)),
		"a committed change must slide the deadline forward, not leave it about to close")
	require.NotNil(t, row.ElevationProvenAt)
	assert.WithinDuration(t, anchor, *row.ElevationProvenAt, time.Second,
		"the slide must never move the anchor, or the absolute cap would never be reached")
}

func TestUnlinkOAuthProvider_UnelevatedIsRefused(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)
	require.NoError(t, env.store.OAuthProviders().Create(context.Background(), store.CreateOAuthProviderParams{
		ID: "github-1", ProviderType: "github", Name: "GitHub",
		ClientID: "c1", ClientSecret: []byte("s1"), Scopes: "read:user", Enabled: true,
	}))
	require.NoError(t, env.store.OAuthUserLinks().Create(context.Background(), store.CreateOAuthUserLinkParams{
		UserID: userid.MustNew(env.userID), ProviderID: "github-1", ProviderSubject: "gh-sub",
	}))

	_, err := env.client.UnlinkOAuthProvider(context.Background(), authedReq(&leapmuxv1.UnlinkOAuthProviderRequest{
		ProviderId: "github-1",
	}, env.token))
	assertElevationRequired(t, err)

	links, err := env.store.OAuthUserLinks().ListByUser(context.Background(), userid.MustNew(env.userID))
	require.NoError(t, err)
	assert.Len(t, links, 1, "a refused call must leave the link in place")
}

func TestUnlinkOAuthProvider_LapsedElevationIsRefused(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)
	env.lapseElevation(t)

	_, err := env.client.UnlinkOAuthProvider(context.Background(), authedReq(&leapmuxv1.UnlinkOAuthProviderRequest{
		ProviderId: "github-1",
	}, env.token))
	assertElevationRequired(t, err)
}

// TestUnlinkOAuthProvider_UnelevatedCannotProbeWhichProvidersAreLinked: the
// gate runs before the "no linked account for provider" answer, so an
// un-elevated caller cannot walk provider ids to learn which ones the
// account holds.
func TestUnlinkOAuthProvider_UnelevatedCannotProbeWhichProvidersAreLinked(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)
	require.NoError(t, env.store.OAuthProviders().Create(context.Background(), store.CreateOAuthProviderParams{
		ID: "github-1", ProviderType: "github", Name: "GitHub",
		ClientID: "c1", ClientSecret: []byte("s1"), Scopes: "read:user", Enabled: true,
	}))
	require.NoError(t, env.store.OAuthUserLinks().Create(context.Background(), store.CreateOAuthUserLinkParams{
		UserID: userid.MustNew(env.userID), ProviderID: "github-1", ProviderSubject: "gh-sub",
	}))

	_, linkedErr := env.client.UnlinkOAuthProvider(context.Background(), authedReq(&leapmuxv1.UnlinkOAuthProviderRequest{
		ProviderId: "github-1",
	}, env.token))
	_, absentErr := env.client.UnlinkOAuthProvider(context.Background(), authedReq(&leapmuxv1.UnlinkOAuthProviderRequest{
		ProviderId: "nonexistent",
	}, env.token))

	assertElevationRequired(t, linkedErr)
	assertElevationRequired(t, absentErr)
	assert.Equal(t, absentErr.Error(), linkedErr.Error(),
		"a linked provider and an unknown one must be indistinguishable without the factor")
}

func TestUnlinkOAuthProvider_SlidesTheWindow(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)
	anchor := time.Now().UTC()
	env.setElevationRow(t, anchor, anchor.Add(5*time.Minute))
	for _, p := range []string{"github-1", "google-1"} {
		require.NoError(t, env.store.OAuthProviders().Create(context.Background(), store.CreateOAuthProviderParams{
			ID: p, ProviderType: "oidc", Name: p,
			ClientID: "c-" + p, ClientSecret: []byte("s"), Scopes: "openid", Enabled: true,
		}))
		require.NoError(t, env.store.OAuthUserLinks().Create(context.Background(), store.CreateOAuthUserLinkParams{
			UserID: userid.MustNew(env.userID), ProviderID: p, ProviderSubject: "sub-" + p,
		}))
	}

	_, err := env.client.UnlinkOAuthProvider(context.Background(), authedReq(&leapmuxv1.UnlinkOAuthProviderRequest{
		ProviderId: "github-1",
	}, env.token))
	require.NoError(t, err)

	row, err := env.store.Sessions().GetByID(context.Background(), env.token, time.Now().UTC())
	require.NoError(t, err)
	require.NotNil(t, row.ElevationExpiresAt)
	assert.True(t, row.ElevationExpiresAt.After(anchor.Add(time.Hour)),
		"a committed change must slide the deadline forward")
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

	env.elevate(t)
	resp, err := env.client.RenamePasskey(context.Background(), authedReq(&leapmuxv1.RenamePasskeyRequest{
		Id:           pkID,
		FriendlyName: "New Name",
	}, env.token))
	require.NoError(t, err)
	assert.Equal(t, "New Name", resp.Msg.GetPasskey().GetFriendlyName())

	row, err := env.store.PasskeyCredentials().GetByID(context.Background(), pkID)
	require.NoError(t, err)
	assert.Equal(t, "New Name", row.FriendlyName)
}

func TestUserService_RenamePasskey_RequiresElevation(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)
	pkID := seedPasskeyCredential(t, env.store, env.userID, "Old Name")

	_, err := env.client.RenamePasskey(context.Background(), authedReq(&leapmuxv1.RenamePasskeyRequest{
		Id:           pkID,
		FriendlyName: "New Name",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	row, err := env.store.PasskeyCredentials().GetByID(context.Background(), pkID)
	require.NoError(t, err)
	assert.Equal(t, "Old Name", row.FriendlyName, "a refused rename must change nothing")
}

// TestUserService_RenamePasskey_PasswordlessAccountElevates pins that a
// passkey-only account uses the SAME elevation as a password account. Before
// unification the two surfaces presented different secrets; now the session
// carries the answer whichever factor proved it.
func TestUserService_RenamePasskey_PasswordlessAccountElevates(t *testing.T) {
	t.Parallel()

	env := setupOAuthUserTest(t)
	pkID := seedPasskeyCredential(t, env.store, env.userID, "Old Name")
	env.elevate(t)

	resp, err := env.client.RenamePasskey(context.Background(), authedReq(&leapmuxv1.RenamePasskeyRequest{
		Id:           pkID,
		FriendlyName: "New Name",
	}, env.token))
	require.NoError(t, err)
	assert.Equal(t, "New Name", resp.Msg.GetPasskey().GetFriendlyName())

	// The elevation SURVIVES the mutation: it is a window, not a
	// single-use proof, so the next action in the window needs no prompt.
	resp, err = env.client.RenamePasskey(context.Background(), authedReq(&leapmuxv1.RenamePasskeyRequest{
		Id:           pkID,
		FriendlyName: "Newer Name",
	}, env.token))
	require.NoError(t, err)
	assert.Equal(t, "Newer Name", resp.Msg.GetPasskey().GetFriendlyName())
}

func TestUserService_DeletePasskey_WhenElevated(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t).elevated(t)
	pkID := seedPasskeyCredential(t, env.store, env.userID, "To Delete")

	_, err := env.client.DeletePasskey(context.Background(), authedReq(&leapmuxv1.DeletePasskeyRequest{
		Id: pkID,
	}, env.token))
	require.NoError(t, err)

	_, err = env.store.PasskeyCredentials().GetByID(context.Background(), pkID)
	require.Error(t, err)
	assert.True(t, errors.Is(err, store.ErrNotFound))
}

func TestUserService_DeletePasskey_PasswordlessAccountElevates(t *testing.T) {
	t.Parallel()

	env := setupOAuthUserTest(t)
	pk1 := seedPasskeyCredential(t, env.store, env.userID, "Keep")
	pk2 := seedPasskeyCredential(t, env.store, env.userID, "Remove")
	env.elevate(t)

	_, err := env.client.DeletePasskey(context.Background(), authedReq(&leapmuxv1.DeletePasskeyRequest{
		Id: pk2,
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
	env.elevate(t)

	_, err := env.client.DeletePasskey(context.Background(), authedReq(&leapmuxv1.DeletePasskeyRequest{
		Id: pkID,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "new_password")

	_, err = env.store.PasskeyCredentials().GetByID(context.Background(), pkID)
	require.NoError(t, err)
}

func TestUserService_DeletePasskey_LastPasskeyWithNewPassword_Deactivates(t *testing.T) {
	t.Parallel()

	env := setupOAuthUserTest(t)
	pkID := seedPasskeyCredential(t, env.store, env.userID, "Only One")
	env.elevate(t)

	otherSession, _, err := auth.CreateSession(context.Background(), env.store, userid.MustNew(env.userID), auth.DefaultSessionDuration)
	require.NoError(t, err)

	_, err = env.client.DeletePasskey(context.Background(), authedReq(&leapmuxv1.DeletePasskeyRequest{
		Id:          pkID,
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

func TestUserService_DeactivatePasskeyAuth_WhenElevated(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t).elevated(t)
	seedPasskeyCredential(t, env.store, env.userID, "One")
	seedPasskeyCredential(t, env.store, env.userID, "Two")

	_, err := env.client.DeactivatePasskeyAuth(context.Background(), authedReq(&leapmuxv1.DeactivatePasskeyAuthRequest{}, env.token))
	require.NoError(t, err)

	rows, err := env.store.PasskeyCredentials().ListByUser(context.Background(), env.userID)
	require.NoError(t, err)
	assert.Empty(t, rows)
}

func TestUserService_DeactivatePasskeyAuth_ElevatedWithNewPassword(t *testing.T) {
	t.Parallel()

	env := setupOAuthUserTest(t)
	seedPasskeyCredential(t, env.store, env.userID, "Only")
	env.elevate(t)

	_, err := env.client.DeactivatePasskeyAuth(context.Background(), authedReq(&leapmuxv1.DeactivatePasskeyAuthRequest{
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

// TestUserService_DeactivatePasskeyAuth_EmptyPasswordAnswersTheRule pins WHICH
// complaint an empty new_password gets on a passwordless account.
//
// It went straight to the hash, so validate.ValidatePassword answered with a
// password-STRENGTH rule -- how long a password must be, and which characters
// it must hold -- about a field the caller never filled. The real fault is
// that this account keeps no other way to sign in, and the refusal has to say
// so, because that is what tells the user to type one.
func TestUserService_DeactivatePasskeyAuth_EmptyPasswordAnswersTheRule(t *testing.T) {
	t.Parallel()

	env := setupOAuthUserTest(t)
	pkID := seedPasskeyCredential(t, env.store, env.userID, "Only")
	env.elevate(t)

	_, err := env.client.DeactivatePasskeyAuth(context.Background(),
		authedReq(&leapmuxv1.DeactivatePasskeyAuthRequest{NewPassword: ""}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err),
		"the rule is a precondition of the account, not a malformed argument")
	assert.Contains(t, err.Error(), "new_password")
	assert.Contains(t, err.Error(), "without a password")
	// NOT the sibling's wording. lastPasskeyNeedsPasswordError offers
	// DeactivatePasskeyAuth as the remedy, which inside DeactivatePasskeyAuth
	// tells the caller to make the call that is already running.
	assert.NotContains(t, err.Error(), "DeactivatePasskeyAuth")

	// The handler deleted nothing, so the account still signs in with the
	// passkey it was refused permission to remove.
	_, err = env.store.PasskeyCredentials().GetByID(context.Background(), pkID)
	require.NoError(t, err)
	user, err := env.store.Users().GetByID(context.Background(), env.userID)
	require.NoError(t, err)
	assert.False(t, user.PasswordSet)
}

func TestUserService_DeactivatePasskeyAuth_InvalidatesOtherSessions(t *testing.T) {
	t.Parallel()

	env := setupOAuthUserTest(t)
	seedPasskeyCredential(t, env.store, env.userID, "Only")
	env.elevate(t)

	otherSession, _, err := auth.CreateSession(context.Background(), env.store, userid.MustNew(env.userID), auth.DefaultSessionDuration)
	require.NoError(t, err)

	_, err = env.client.DeactivatePasskeyAuth(context.Background(), authedReq(&leapmuxv1.DeactivatePasskeyAuthRequest{
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
		PendingEmail:            "counted@example.com",
		PendingEmailUnblockedAt: time.Now().Add(-5 * time.Minute).UTC(),
		PendingEmailToken:       verifyToken,
		PendingEmailExpiresAt:   ptrTime(time.Now().Add(1 * time.Hour).UTC()),
		ID:                      env.userID,
		Now:                     time.Now().UTC(),
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

	rec := &mailSenderDouble{}
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
		ID:                      env.userID,
		PendingEmail:            "u@example.com",
		PendingEmailUnblockedAt: time.Now().Add(-5 * time.Minute).UTC(),
		PendingEmailToken:       verifycode.Generate(),
		PendingEmailExpiresAt:   ptrTime(time.Now().Add(25 * time.Minute).UTC()),
		Now:                     time.Now().UTC(),
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

// A failed send reports the FAILURE window it leaves behind: the response
// carries the deadline the mint gate now enforces, so the countdown a
// client renders never invites a retry the hub then refuses. The reported
// window is the failure cooldown (10s by default), never the full minute a
// successful send leaves.
func TestResendVerificationEmail_FailedSendReportsFailureWindow(t *testing.T) {
	t.Parallel()

	env, rec := setupResendUserTest(t)

	minted, err := env.store.Users().SetPendingEmail(context.Background(), store.SetPendingEmailParams{
		ID:                      env.userID,
		PendingEmail:            "u@example.com",
		PendingEmailToken:       verifycode.Generate(),
		PendingEmailExpiresAt:   ptrTime(time.Now().Add(25 * time.Minute).UTC()),
		PendingEmailUnblockedAt: time.Now().UTC().Add(-5 * time.Minute).Add(time.Minute),
		Now:                     time.Now().UTC().Add(-5 * time.Minute),
	})
	require.NoError(t, err)
	require.True(t, minted)

	rec.err = errors.New("smtp unavailable")
	before := time.Now().UTC()
	resp, err := env.client.ResendVerificationEmail(context.Background(), authedReq(&leapmuxv1.ResendVerificationEmailRequest{}, env.token))
	require.NoError(t, err)
	assert.False(t, resp.Msg.GetEmailSent())
	require.NotNil(t, resp.Msg.GetNextResendAvailableAt(),
		"a failed send arms the failure window, so the response must report it")
	nextAt := resp.Msg.GetNextResendAvailableAt().AsTime()
	assert.True(t, nextAt.After(before.Add(8*time.Second)),
		"the reported window is the failure cooldown")
	assert.True(t, nextAt.Before(before.Add(12*time.Second)),
		"and never the full resend cooldown a successful send leaves")

	// The immediate retry the failure message invites is refused for that
	// window: the gate and the countdown read one column.
	_, err = env.client.ResendVerificationEmail(context.Background(), authedReq(&leapmuxv1.ResendVerificationEmailRequest{}, env.token))
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err),
		"the retry lands inside the failure window the response just reported")
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

// TestFinishPasskeyElevation_MalformedCredentialIsUnauthenticated pins the
// error CLASS of a rejected step-up ceremony. A malformed credential_json on
// a live ceremony is the caller's fault, so it must surface as
// CodeUnauthenticated -- the class the rate limiter counts. Reporting it as
// Internal would leave an attacker's guesses uncounted.
func TestFinishPasskeyElevation_MalformedCredentialIsUnauthenticated(t *testing.T) {
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

	// A LIVE elevation ceremony (BeginElevation writes the properly
	// encrypted session row), so the failure lands on the parse branch.
	sessionID, _, err := wa.BeginElevation(context.Background(), userID, "")
	require.NoError(t, err)
	token, _, err := auth.CreateSession(context.Background(), st, userid.MustNew(userID), auth.DefaultSessionDuration)
	require.NoError(t, err)

	_, err = client.FinishPasskeyElevation(context.Background(), authedReq(&leapmuxv1.FinishPasskeyElevationRequest{
		SessionId:      sessionID,
		CredentialJson: "not-a-credential",
	}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err),
		"a rejected ceremony is the caller's credential failure, not a server error")

	// A rejected ceremony must leave the session UN-elevated: a failed
	// assertion that granted a window would be worse than no step-up at all.
	row, err := st.Sessions().GetByID(context.Background(), token, time.Now().UTC())
	require.NoError(t, err)
	assert.Nil(t, row.ElevationExpiresAt, "a failed assertion must not elevate")
}

// TestBeginPasskeyRegistration_RefusesBeforeTheBrowserPrompt pins WHERE the
// gate runs. Begin writes no credential, so refusing an un-elevated caller
// there costs nothing -- and admitting it would put a biometric prompt on
// screen that the Finish leg would always refuse.
func TestBeginPasskeyRegistration_RefusesBeforeTheBrowserPrompt(t *testing.T) {
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
		ID: userID, Username: "pkonly", PasswordHash: password.PlaceholderHash,
		DisplayName: "PK Only", PasswordSet: false, IsAdmin: true,
	}))
	// A REAL credential row: BeginRegistration lists the user's passkeys to
	// build excludeCredentials, so it decrypts every stored public key.
	credRow := id.Generate()
	encPub, keyVersion, encErr := wa.EncryptPublicKey(credRow, []byte("pubkey"))
	require.NoError(t, encErr)
	require.NoError(t, st.PasskeyCredentials().Create(context.Background(), store.CreatePasskeyCredentialParams{
		ID: credRow, UserID: userID, CredentialID: []byte("cred-begin"), PublicKey: encPub,
		Transports: "[]", FriendlyName: "Phone", KeyVersion: keyVersion, CreatedAt: time.Now().UTC(),
	}))

	token, _, err := auth.CreateSession(context.Background(), st, userid.MustNew(userID), auth.DefaultSessionDuration)
	require.NoError(t, err)

	begin := func() (*connect.Response[leapmuxv1.BeginPasskeyRegistrationResponse], error) {
		req := authedReq(&leapmuxv1.BeginPasskeyRegistrationRequest{}, token)
		req.Header().Set("Origin", "http://localhost:4327")
		return client.BeginPasskeyRegistration(context.Background(), req)
	}

	_, err = begin()
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	// Elevated, the same call starts the ceremony. The direct write bypasses
	// the RPC that would drop the interceptor's cached UserInfo, so this
	// drops it explicitly -- exactly what lifecycle.UserInfoInvalidated does
	// in production.
	now := time.Now().UTC()
	n, err := st.Sessions().Elevate(context.Background(), store.ElevateSessionParams{
		SessionID: token, UserID: userid.MustNew(userID),
		ElevationProvenAt: now, ElevationExpiresAt: now.Add(auth.ElevationWindow),
	}, now)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	contexts.EvictByUserID(userID)

	begun, err := begin()
	require.NoError(t, err)
	assert.NotEmpty(t, begun.Msg.GetSessionId())
}

// TestDeletePasskey_ElevationWindowCoversRepeatedActions is the property
// the elevation window exists for, and the one a single-use proof did NOT
// have: a second sensitive action inside the window needs no new factor.
// The risk this accepts is stated in the plan -- one ceremony admits every
// sensitive action for the window -- so this pins it rather than assuming it.
func TestDeletePasskey_ElevationWindowCoversRepeatedActions(t *testing.T) {
	t.Parallel()

	env := setupOAuthUserTest(t)
	_ = seedPasskeyCredential(t, env.store, env.userID, "KeepA")
	pk1 := seedPasskeyCredential(t, env.store, env.userID, "KeepB")
	pk2 := seedPasskeyCredential(t, env.store, env.userID, "Remove")
	env.elevate(t)

	_, err := env.client.DeletePasskey(context.Background(), authedReq(&leapmuxv1.DeletePasskeyRequest{
		Id: pk2,
	}, env.token))
	require.NoError(t, err)

	// A SECOND deletion, with no further prompt.
	_, err = env.client.DeletePasskey(context.Background(), authedReq(&leapmuxv1.DeletePasskeyRequest{
		Id: pk1,
	}, env.token))
	require.NoError(t, err)

	_, err = env.store.PasskeyCredentials().GetByID(context.Background(), pk1)
	require.ErrorIs(t, err, store.ErrNotFound)
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
		Id: pkID, FriendlyName: "Phone",
	}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "solo mode")

	// The elevation surface refuses solo too: there is no session row to
	// stamp, so admitting it would report an elevation nothing recorded.
	_, err = client.ElevateSession(context.Background(), authedReq(&leapmuxv1.ElevateSessionRequest{
		CurrentPassword: "testpass",
	}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "solo mode")
}
