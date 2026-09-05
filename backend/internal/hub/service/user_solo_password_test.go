package service_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/bootstrap"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/servicetest"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/usernames"
	"github.com/leapmux/leapmux/internal/util/userid"
)

func mustUserID(t *testing.T, id string) userid.UserID {
	t.Helper()
	uid, ok := userid.New(id)
	require.True(t, ok)
	return uid
}

// setSoloPasswordForTest gives the solo account a usable Argon2id hash.
func setSoloPasswordForTest(t *testing.T, st store.Store) {
	t.Helper()
	user, err := st.Users().GetByUsername(context.Background(), usernames.Solo)
	require.NoError(t, err)
	hash, err := password.Hash("correct-horse-battery-staple")
	require.NoError(t, err)
	require.NoError(t, st.Users().UpdatePassword(context.Background(), store.UpdateUserPasswordParams{
		PasswordHash: hash, ID: user.ID,
	}))
}

// soloUserService builds a solo-mode UserService with the hub's gate attached,
// and returns the gate so a test can read what the write did to it.
func soloUserService(t *testing.T) (*service.UserService, store.Store, *auth.SoloGate, *auth.UserInfo) {
	t.Helper()
	st := hubtestutil.OpenTestStore(t)
	require.NoError(t, bootstrap.Run(context.Background(), st, true))

	gate := auth.NewSoloGate(true, st)
	svc := service.NewUserService(
		st,
		&config.Config{SoloMode: true},
		servicetest.NewSettingsManager(t, st, nil),
		auth.NewCredentialLifecycleEffects(nil, nil, nil),
		nil,
		mail.Renderer{},
		nil,
	)

	soloUser, err := auth.LoadSoloUser(context.Background(), st)
	require.NoError(t, err)
	return svc, st, gate, soloUser
}

// The feature itself: a solo hub must be able to set its account's password,
// or it can never be published on a network address.
func TestChangePassword_SucceedsInSoloMode(t *testing.T) {
	svc, st, _, soloUser := soloUserService(t)
	ctx := auth.WithUser(context.Background(), soloUser)

	_, err := svc.ChangePassword(ctx, connect.NewRequest(
		&leapmuxv1.ChangePasswordRequest{NewPassword: "correct-horse-battery-staple"}))
	require.NoError(t, err)

	user, err := st.Users().GetByUsername(ctx, usernames.Solo)
	require.NoError(t, err)
	require.True(t, password.IsUsable(user.PasswordHash), "a usable hash must be stored")
	match, err := password.Verify(user.PasswordHash, "correct-horse-battery-staple")
	require.NoError(t, err)
	assert.True(t, match)
}

// Preferences → Account → Password is where a solo owner sets the first
// password, and it renders "Set Password" or "Change Password" from the
// account the hub reports. A fresh solo account must therefore report NO
// password, although its users.password_set column claims one -- the bootstrap
// writes that claim with an empty hash.
func TestGetCurrentUser_ReportsTheSoloAccountsPasswordFromTheHash(t *testing.T) {
	svc, st, gate, soloUser := soloUserService(t)
	ctx := auth.WithUser(context.Background(), soloUser)

	deps := servicetest.AuthServiceDeps(st, &config.Config{SoloMode: true, Listen: "127.0.0.1:4327"},
		servicetest.NewSettingsManager(t, st, nil), auth.NewCredentialLifecycleEffects(nil, nil, nil))
	deps.SoloGate = gate
	authSvc := service.NewAuthService(deps)

	before, err := authSvc.GetCurrentUser(ctx, connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{}))
	require.NoError(t, err)
	assert.False(t, before.Msg.GetUser().GetPasswordSet(),
		"a solo account with no hash holds no password, whatever the column claims")

	_, err = svc.ChangePassword(ctx, connect.NewRequest(
		&leapmuxv1.ChangePasswordRequest{NewPassword: "correct-horse-battery-staple"}))
	require.NoError(t, err)

	after, err := authSvc.GetCurrentUser(ctx, connect.NewRequest(&leapmuxv1.GetCurrentUserRequest{}))
	require.NoError(t, err)
	assert.True(t, after.Msg.GetUser().GetPasswordSet(),
		"the stored password must reach the row that offers to change it")
}

// ChangePassword stays an authenticated account procedure. Local IPC already
// keeps its synthetic identity, so this procedure must not mint another
// session when it sets the first password.
func TestChangePassword_DoesNotCreateASoloSession(t *testing.T) {
	svc, _, _, soloUser := soloUserService(t)
	ctx := auth.WithUser(context.Background(), soloUser)

	resp, err := svc.ChangePassword(ctx, connect.NewRequest(
		&leapmuxv1.ChangePasswordRequest{NewPassword: "correct-horse-battery-staple"}))
	require.NoError(t, err)

	assert.Empty(t, resp.Header().Get("Set-Cookie"))
}

// The OTHER identity kind, beside the synthetic solo one above: a real
// session, elevated so the step-up rule cannot be what produces the empty
// header. It already holds a credential, and a second session would leave the
// old cookie pointing at one the change revoked.
func TestChangePassword_DoesNotHandASessionToAnOrdinaryCaller(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	hubtestutil.CreateTestAdmin(t, st)
	admin, err := st.Users().GetByUsername(context.Background(), hubtestutil.TestAdminUsername)
	require.NoError(t, err)

	svc := service.NewUserService(
		st,
		&config.Config{},
		servicetest.NewSettingsManager(t, st, nil),
		auth.NewCredentialLifecycleEffects(nil, nil, nil),
		nil,
		mail.Renderer{},
		nil,
	)

	sessionID, expiresAt, err := auth.CreateSession(context.Background(), st, mustUserID(t, admin.ID), time.Hour)
	require.NoError(t, err)
	require.False(t, expiresAt.IsZero())
	// The ordinary path demands a proven factor; grant one so the test
	// exercises the handover rule and not the step-up rule. The session is
	// resolved AFTER it, because UserInfo carries the elevation deadline it
	// was read with.
	_, err = st.Sessions().Elevate(context.Background(), store.ElevateSessionParams{
		SessionID:          sessionID,
		UserID:             mustUserID(t, admin.ID),
		ElevationProvenAt:  time.Now().UTC(),
		ElevationExpiresAt: time.Now().UTC().Add(time.Hour),
	}, time.Now().UTC())
	require.NoError(t, err)
	info, err := auth.ValidateToken(context.Background(), st, sessionID)
	require.NoError(t, err)
	require.True(t, info.Elevated(time.Now()), "precondition: the caller is elevated")

	resp, err := svc.ChangePassword(auth.WithUser(context.Background(), info), connect.NewRequest(
		&leapmuxv1.ChangePasswordRequest{NewPassword: "another-good-password"}))
	require.NoError(t, err)
	assert.Empty(t, resp.Header().Get("Set-Cookie"),
		"ChangePassword mints no session for any caller; a caller that already holds one keeps it")
}

// The PASSKEY factor is refused by mode, unlike the elevation surface around
// it.
//
// rejectSoloElevation reads the CALLER, because a solo hub that holds a
// password has real sessions to elevate. But no account there can ever hold a
// passkey -- every management verb is refused and GetSystemInfo reports
// passkey_enabled false for every origin -- so a signed-in solo caller reached
// the WebAuthn engine and was answered "no passkeys registered", which reads as
// a missing credential rather than a feature the hub does not have.
func TestPasskeyElevation_RefusedInSoloForARealSession(t *testing.T) {
	svc, st, _, _ := soloUserService(t)

	// A caller with an ordinary session, which is what a solo hub hands its
	// network callers once the account holds a password.
	user, err := st.Users().GetByUsername(context.Background(), usernames.Solo)
	require.NoError(t, err)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:         userid.MustNew(user.ID),
		Username:   user.Username,
		IsAdmin:    true,
		Credential: auth.SessionCredential("ses_test"),
	})

	_, beginErr := svc.BeginPasskeyElevation(ctx, connect.NewRequest(&leapmuxv1.BeginPasskeyElevationRequest{}))
	require.Error(t, beginErr)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(beginErr))
	assert.Contains(t, beginErr.Error(), "solo mode")

	_, finishErr := svc.FinishPasskeyElevation(ctx, connect.NewRequest(&leapmuxv1.FinishPasskeyElevationRequest{}))
	require.Error(t, finishErr)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(finishErr))
	assert.Contains(t, finishErr.Error(), "solo mode")
}
