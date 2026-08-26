package service_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/servicetest"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type recordingMailSender struct {
	msgs []mail.Message
	err  error
}

func (r *recordingMailSender) Send(_ context.Context, msg mail.Message) error {
	if r.err != nil {
		return r.err
	}
	r.msgs = append(r.msgs, msg)
	return nil
}

func setupPasswordResetAuthService(t *testing.T, sender mail.Sender) (leapmuxv1connect.AuthServiceClient, store.Store) {
	client, st, _ := setupPasswordResetAuthServiceWithClock(t, sender)
	return client, st
}

// setupPasswordResetAuthServiceWithClock is the same harness, plus the
// service value itself, so a test can move its clock seam.
func setupPasswordResetAuthServiceWithClock(t *testing.T, sender mail.Sender) (leapmuxv1connect.AuthServiceClient, store.Store, *service.AuthService) {
	t.Helper()

	st := hubtestutil.OpenTestStore(t)
	set := servicetest.NewSettingsManager(t, st, nil)
	enableSignup(t, set)

	mux := http.NewServeMux()
	interceptor, sc := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st})
	t.Cleanup(sc.Stop)
	opts := connect.WithInterceptors(interceptor)
	authSvc := service.NewAuthService(service.AuthServiceDeps{
		Store:     st,
		Config:    testConfig(),
		Settings:  set,
		Lifecycle: auth.NewCredentialLifecycleEffects(sc, nil, nil),
		Mail:      sender,
		Renderer:  mail.Renderer{BaseURL: func() string { return "https://hub.example.com" }},
	})
	path, handler := leapmuxv1connect.NewAuthServiceHandler(authSvc, opts)
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL)
	return client, st, authSvc
}

func extractPasswordResetToken(body string) string {
	const marker = "/reset-password?token="
	i := strings.Index(body, marker)
	if i < 0 {
		return ""
	}
	rest := body[i+len(marker):]
	if j := strings.IndexAny(rest, " \n\r\t"); j >= 0 {
		return rest[:j]
	}
	return rest
}

func TestRequestPasswordReset_UnknownUser_EmptySuccess(t *testing.T) {
	t.Parallel()

	sender := &recordingMailSender{}
	client, _ := setupPasswordResetAuthService(t, sender)

	_, err := client.RequestPasswordReset(context.Background(), connect.NewRequest(&leapmuxv1.RequestPasswordResetRequest{
		Identifier: "nobody@example.com",
	}))
	require.NoError(t, err)
	assert.Empty(t, sender.msgs)
}

func TestRequestPasswordReset_KnownUser_SendsMail(t *testing.T) {
	t.Parallel()

	sender := &recordingMailSender{}
	client, st := setupPasswordResetAuthService(t, sender)

	userID := id.Generate()
	hash, err := password.Hash("oldpass123")
	require.NoError(t, err)
	require.NoError(t, st.Users().Create(context.Background(), store.CreateUserParams{
		ID:            userID,
		Username:      "resetme",
		PasswordHash:  hash,
		DisplayName:   "Reset Me",
		Email:         "resetme@example.com",
		EmailVerified: true,
		PasswordSet:   true,
	}))

	_, err = client.RequestPasswordReset(context.Background(), connect.NewRequest(&leapmuxv1.RequestPasswordResetRequest{
		Identifier: "resetme@example.com",
	}))
	require.NoError(t, err)
	require.Len(t, sender.msgs, 1)
	assert.Equal(t, "resetme@example.com", sender.msgs[0].To)

	token := extractPasswordResetToken(sender.msgs[0].Body)
	require.NotEmpty(t, token)
	// The reset secret comes from the shared id mint, like session ids and
	// API access secrets.
	assert.Regexp(t, "^[A-Za-z0-9]{48}$", token)

	user, err := st.Users().GetByID(context.Background(), userID)
	require.NoError(t, err)
	assert.NotEmpty(t, user.PendingPasswordResetToken)
	assert.NotNil(t, user.PendingPasswordResetExpiresAt)

	_, err = client.CompletePasswordReset(context.Background(), connect.NewRequest(&leapmuxv1.CompletePasswordResetRequest{
		Token:       token,
		NewPassword: "newpass123",
	}))
	require.NoError(t, err)

	user, err = st.Users().GetByID(context.Background(), userID)
	require.NoError(t, err)
	assert.True(t, user.PasswordSet)
	match, err := password.Verify(user.PasswordHash, "newpass123")
	require.NoError(t, err)
	assert.True(t, match)
}

func TestRequestPasswordReset_MailFailure_ClearsToken(t *testing.T) {
	t.Parallel()

	sender := &recordingMailSender{err: errors.New("smtp down")}
	client, st := setupPasswordResetAuthService(t, sender)

	userID := id.Generate()
	hash, err := password.Hash("oldpass123")
	require.NoError(t, err)
	require.NoError(t, st.Users().Create(context.Background(), store.CreateUserParams{
		ID:            userID,
		Username:      "mailfail",
		PasswordHash:  hash,
		DisplayName:   "Mail Fail",
		Email:         "mailfail@example.com",
		EmailVerified: true,
		PasswordSet:   true,
	}))

	_, err = client.RequestPasswordReset(context.Background(), connect.NewRequest(&leapmuxv1.RequestPasswordResetRequest{
		Identifier: "mailfail@example.com",
	}))
	require.NoError(t, err)

	user, err := st.Users().GetByID(context.Background(), userID)
	require.NoError(t, err)
	assert.Empty(t, user.PendingPasswordResetToken)
}

func TestCompletePasswordReset_InvalidToken_NotFound(t *testing.T) {
	t.Parallel()

	client, st := setupPasswordResetAuthService(t, mail.NewStubSender())

	userID := id.Generate()
	hash, err := password.Hash("oldpass123")
	require.NoError(t, err)
	require.NoError(t, st.Users().Create(context.Background(), store.CreateUserParams{
		ID:           userID,
		Username:     "badtoken",
		PasswordHash: hash,
		PasswordSet:  true,
	}))

	_, err = client.CompletePasswordReset(context.Background(), connect.NewRequest(&leapmuxv1.CompletePasswordResetRequest{
		Token:       "not-a-real-token",
		NewPassword: "newpass123",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	user, err := st.Users().GetByID(context.Background(), userID)
	require.NoError(t, err)
	match, err := password.Verify(user.PasswordHash, "oldpass123")
	require.NoError(t, err)
	assert.True(t, match)
}

func TestCompletePasswordReset_Success_WipesPasskeysAndSessions(t *testing.T) {
	t.Parallel()

	sender := &recordingMailSender{}
	client, st := setupPasswordResetAuthService(t, sender)

	userID := id.Generate()
	hash, err := password.Hash("oldpass123")
	require.NoError(t, err)
	require.NoError(t, st.Users().Create(context.Background(), store.CreateUserParams{
		ID:            userID,
		Username:      "wipeme",
		PasswordHash:  hash,
		DisplayName:   "Wipe Me",
		Email:         "wipeme@example.com",
		EmailVerified: true,
		PasswordSet:   true,
	}))
	seedPasskeyCredential(t, st, userID, "Laptop")
	uid := userid.MustNew(userID)
	oldSession, _, err := auth.CreateSession(context.Background(), st, uid, auth.DefaultSessionDuration)
	require.NoError(t, err)
	ceremonyID := id.Generate()
	now := time.Now().UTC()
	require.NoError(t, st.WebAuthnSessions().Create(context.Background(), store.CreateWebAuthnSessionParams{
		ID:          ceremonyID,
		Kind:        "login",
		UserID:      userID,
		PayloadJSON: "{}",
		SessionData: []byte("dummy-ceremony"),
		ExpiresAt:   now.Add(5 * time.Minute),
		CreatedAt:   now,
	}))

	_, err = client.RequestPasswordReset(context.Background(), connect.NewRequest(&leapmuxv1.RequestPasswordResetRequest{
		Identifier: "wipeme",
	}))
	require.NoError(t, err)
	require.Len(t, sender.msgs, 1)
	token := extractPasswordResetToken(sender.msgs[0].Body)
	require.NotEmpty(t, token)
	// The reset secret comes from the shared id mint, like session ids and
	// API access secrets.
	assert.Regexp(t, "^[A-Za-z0-9]{48}$", token)

	_, err = client.CompletePasswordReset(context.Background(), connect.NewRequest(&leapmuxv1.CompletePasswordResetRequest{
		Token:       token,
		NewPassword: "newpass123",
	}))
	require.NoError(t, err)

	count, err := st.PasskeyCredentials().CountByUser(context.Background(), userID)
	require.NoError(t, err)
	assert.EqualValues(t, 0, count)

	_, err = st.Sessions().GetByID(context.Background(), oldSession, time.Now().UTC())
	assert.ErrorIs(t, err, store.ErrNotFound)

	_, err = st.WebAuthnSessions().Get(context.Background(), ceremonyID)
	assert.ErrorIs(t, err, store.ErrNotFound)

	_, _, _, err = auth.Login(context.Background(), st, "wipeme", "newpass123", auth.DefaultSessionDuration)
	require.NoError(t, err)
}

func TestCompletePasswordReset_ExpiredToken_NotFound(t *testing.T) {
	t.Parallel()

	sender := &recordingMailSender{}
	client, st := setupPasswordResetAuthService(t, sender)

	userID := id.Generate()
	hash, err := password.Hash("oldpass123")
	require.NoError(t, err)
	require.NoError(t, st.Users().Create(context.Background(), store.CreateUserParams{
		ID:            userID,
		Username:      "expired",
		PasswordHash:  hash,
		Email:         "expired@example.com",
		EmailVerified: true,
		PasswordSet:   true,
	}))

	_, err = client.RequestPasswordReset(context.Background(), connect.NewRequest(&leapmuxv1.RequestPasswordResetRequest{
		Identifier: "expired@example.com",
	}))
	require.NoError(t, err)
	require.Len(t, sender.msgs, 1)
	token := extractPasswordResetToken(sender.msgs[0].Body)
	require.NotEmpty(t, token)
	// The reset secret comes from the shared id mint, like session ids and
	// API access secrets.
	assert.Regexp(t, "^[A-Za-z0-9]{48}$", token)

	user, err := st.Users().GetByID(context.Background(), userID)
	require.NoError(t, err)
	_, err = st.Users().SetPendingPasswordReset(context.Background(), store.SetPendingPasswordResetParams{
		ID:                            userID,
		PendingPasswordResetToken:     user.PendingPasswordResetToken,
		PendingPasswordResetExpiresAt: time.Now().UTC().Add(-time.Hour),
		CooldownCutoff:                time.Now().UTC().Add(time.Hour),
	})
	require.NoError(t, err)

	_, err = client.CompletePasswordReset(context.Background(), connect.NewRequest(&leapmuxv1.CompletePasswordResetRequest{
		Token:       token,
		NewPassword: "newpass123",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestCompletePasswordReset_AttemptLimitBlocksBeforeArgon2(t *testing.T) {
	t.Parallel()

	sender := &recordingMailSender{}
	client, st := setupPasswordResetAuthService(t, sender)

	userID := id.Generate()
	hash, err := password.Hash("oldpass123")
	require.NoError(t, err)
	require.NoError(t, st.Users().Create(context.Background(), store.CreateUserParams{
		ID:            userID,
		Username:      "attemptcap",
		PasswordHash:  hash,
		Email:         "attemptcap@example.com",
		EmailVerified: true,
		PasswordSet:   true,
	}))

	_, err = client.RequestPasswordReset(context.Background(), connect.NewRequest(&leapmuxv1.RequestPasswordResetRequest{
		Identifier: "attemptcap@example.com",
	}))
	require.NoError(t, err)
	require.Len(t, sender.msgs, 1)
	token := extractPasswordResetToken(sender.msgs[0].Body)
	require.NotEmpty(t, token)
	// The reset secret comes from the shared id mint, like session ids and
	// API access secrets.
	assert.Regexp(t, "^[A-Za-z0-9]{48}$", token)

	// Burn the 5 soft attempts with a wrong password that still matches the
	// token row (Consume increments). Use the real token so lookup succeeds;
	// CompletePasswordReset validates password strength before hashing, so a
	// valid new password is required. We force attempts via the store API.
	for i := 0; i < 5; i++ {
		_, err = st.Users().ConsumePasswordResetAttemptByToken(context.Background(), tokenHashForTest(token), time.Now().UTC(), 5)
		require.NoError(t, err)
	}

	// Sixth Complete must refuse after Consume sets attempts>5, before a
	// successful password change (password stays oldpass123).
	_, err = client.CompletePasswordReset(context.Background(), connect.NewRequest(&leapmuxv1.CompletePasswordResetRequest{
		Token:       token,
		NewPassword: "newpass123",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	user, err := st.Users().GetByID(context.Background(), userID)
	require.NoError(t, err)
	match, err := password.Verify(user.PasswordHash, "oldpass123")
	require.NoError(t, err)
	assert.True(t, match, "attempt limit must block before password hash is replaced")
	assert.Greater(t, user.PendingPasswordResetAttempts, int64(5))
}

func TestRequestPasswordReset_InvalidUsername_EmptySuccess(t *testing.T) {
	t.Parallel()

	sender := &recordingMailSender{}
	client, _ := setupPasswordResetAuthService(t, sender)

	_, err := client.RequestPasswordReset(context.Background(), connect.NewRequest(&leapmuxv1.RequestPasswordResetRequest{
		Identifier: "!!!not-a-slug!!!",
	}))
	require.NoError(t, err)
	assert.Empty(t, sender.msgs)
}

func TestRequestPasswordReset_UsernameIsSanitized(t *testing.T) {
	t.Parallel()

	sender := &recordingMailSender{}
	client, st := setupPasswordResetAuthService(t, sender)

	userID := id.Generate()
	hash, err := password.Hash("oldpass123")
	require.NoError(t, err)
	require.NoError(t, st.Users().Create(context.Background(), store.CreateUserParams{
		ID:            userID,
		Username:      "resetme",
		PasswordHash:  hash,
		DisplayName:   "Reset Me",
		Email:         "resetme@example.com",
		EmailVerified: true,
		PasswordSet:   true,
	}))

	_, err = client.RequestPasswordReset(context.Background(), connect.NewRequest(&leapmuxv1.RequestPasswordResetRequest{
		Identifier: "ResetMe",
	}))
	require.NoError(t, err)
	require.Len(t, sender.msgs, 1)
}

func tokenHashForTest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// TestRequestPasswordReset_CooldownKeepsPreviousLink pins the per-account
// resend cooldown: a second request inside the window must not mint a fresh
// token (which would invalidate the link the first email still carries and
// flood the inbox), and the first token must still complete.
func TestRequestPasswordReset_CooldownKeepsPreviousLink(t *testing.T) {
	sender := &recordingMailSender{}
	client, _, st := setupPasswordResetAuthServiceConfigured(t, sender, seedSMTP)

	userID := id.Generate()
	require.NoError(t, st.Users().Create(context.Background(), store.CreateUserParams{
		ID:            userID,
		Username:      "cooldown-user",
		PasswordHash:  password.PlaceholderHash,
		DisplayName:   "Cooldown User",
		Email:         "cooldown@example.com",
		EmailVerified: true,
		PasswordSet:   true,
	}))

	for i := 0; i < 2; i++ {
		_, err := client.RequestPasswordReset(context.Background(), connect.NewRequest(&leapmuxv1.RequestPasswordResetRequest{
			Identifier: "cooldown@example.com",
		}))
		require.NoError(t, err)
	}
	require.Len(t, sender.msgs, 1, "cooldown must suppress the second send")

	token := extractPasswordResetToken(sender.msgs[0].Body)
	require.NotEmpty(t, token)
	// The reset secret comes from the shared id mint, like session ids and
	// API access secrets.
	assert.Regexp(t, "^[A-Za-z0-9]{48}$", token)
	_, err := client.CompletePasswordReset(context.Background(), connect.NewRequest(&leapmuxv1.CompletePasswordResetRequest{
		Token:       token,
		NewPassword: "freshpass123",
	}))
	require.NoError(t, err, "the first link must stay completable inside the cooldown window")
}

// TestSignUp_MailFailureDoesNotLeakTransportDetails pins that a fail-closed
// signup reports a generic error to the anonymous caller: the SMTP
// transport chain specifies the relay host and port, and that detail stays in
// the server log, never in the client response.
func TestSignUp_MailFailureDoesNotLeakTransportDetails(t *testing.T) {
	sender := &recordingMailSender{err: errors.New("dial tcp smtp.secret-relay.example:587: connection refused")}
	client, _, st := setupPasswordResetAuthServiceConfigured(t, sender, seedSMTP)

	// A user must exist so the sign-up takes the public path, not the
	// first-admin setup path (which verifies nothing and sends no mail).
	require.NoError(t, st.Users().Create(context.Background(), store.CreateUserParams{
		ID:            id.Generate(),
		Username:      "existing-admin",
		PasswordHash:  password.PlaceholderHash,
		DisplayName:   "Existing Admin",
		Email:         "admin@example.com",
		EmailVerified: true,
		PasswordSet:   true,
		IsAdmin:       true,
	}))

	_, err := client.SignUp(context.Background(), connect.NewRequest(&leapmuxv1.SignUpRequest{
		Username:    "leakcheck-user",
		Password:    "hunter2abc",
		DisplayName: "Leak Check",
		Email:       "leakcheck@example.com",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	assert.NotContains(t, err.Error(), "smtp.secret-relay.example")
	assert.NotContains(t, err.Error(), ":587")
	assert.NotContains(t, err.Error(), "connection refused")
}

// setupPasswordResetAuthServiceConfigured builds the reset harness with a
// settings callback that runs against the SAME manager the service reads,
// so a seeded SMTP row is visible to the service under test.
func setupPasswordResetAuthServiceConfigured(t *testing.T, sender mail.Sender, configure func(t *testing.T, set *settings.Manager)) (leapmuxv1connect.AuthServiceClient, *settings.Manager, store.Store) {
	t.Helper()

	st := hubtestutil.OpenTestStore(t)
	set := servicetest.NewSettingsManager(t, st, nil)
	enableSignup(t, set)
	configure(t, set)

	mux := http.NewServeMux()
	interceptor, sc := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st})
	t.Cleanup(sc.Stop)
	opts := connect.WithInterceptors(interceptor)
	authSvc := service.NewAuthService(service.AuthServiceDeps{
		Store:     st,
		Config:    testConfig(),
		Settings:  set,
		Lifecycle: auth.NewCredentialLifecycleEffects(sc, nil, nil),
		Mail:      sender,
		Renderer:  mail.Renderer{BaseURL: func() string { return "https://hub.example.com" }},
	})
	path, handler := leapmuxv1connect.NewAuthServiceHandler(authSvc, opts)
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL)
	return client, set, st
}

// TestRequestPasswordReset_AdminUnconfirmedAddressGetsNoLink is the recovery
// route this change closes.
//
// A reset link is a credential, and the Hub only sends one to an address it
// has evidence for. email_verified IS that evidence, and it used to be
// forced true for every administrator -- so an administrator moved to a
// brand-new address kept a live self-service reset route to whatever was
// typed, on the highest-privilege accounts on the hub. The flag now records
// only what somebody confirmed, and the administrator exemption lives at the
// login gate where it belongs.
func TestRequestPasswordReset_AdminUnconfirmedAddressGetsNoLink(t *testing.T) {
	t.Parallel()

	// SMTP configured, so the hub REQUIRES verification -- which is the
	// deployment where a reset link is a credential and the flag decides
	// whether the hub has evidence for the address.
	sender := &recordingMailSender{}
	client, _, st := setupPasswordResetAuthServiceConfigured(t, sender, seedSMTP)
	ctx := context.Background()

	hash, err := password.Hash("oldpass123")
	require.NoError(t, err)

	// An administrator whose address nobody confirmed.
	unconfirmed := id.Generate()
	require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
		ID: unconfirmed, Username: "unconfirmedadmin", PasswordHash: hash,
		DisplayName: "Unconfirmed", Email: "unconfirmed@example.com",
		EmailVerified: false, PasswordSet: true, IsAdmin: true,
	}))

	_, err = client.RequestPasswordReset(ctx, connect.NewRequest(&leapmuxv1.RequestPasswordResetRequest{
		Identifier: "unconfirmed@example.com",
	}))
	require.NoError(t, err, "the response is uniform; only the mail differs")
	assert.Empty(t, sender.msgs, "no link to an address the hub never confirmed, administrator or not")

	row, err := st.Users().GetByID(ctx, unconfirmed)
	require.NoError(t, err)
	assert.Empty(t, row.PendingPasswordResetToken, "and no token was minted")

	// An administrator who really did confirm their address still gets one:
	// the rule is about the ADDRESS, not about the privilege.
	confirmed := id.Generate()
	require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
		ID: confirmed, Username: "confirmedadmin", PasswordHash: hash,
		DisplayName: "Confirmed", Email: "confirmed@example.com",
		EmailVerified: true, PasswordSet: true, IsAdmin: true,
	}))

	_, err = client.RequestPasswordReset(ctx, connect.NewRequest(&leapmuxv1.RequestPasswordResetRequest{
		Identifier: "confirmed@example.com",
	}))
	require.NoError(t, err)
	require.Len(t, sender.msgs, 1)
	assert.Equal(t, "confirmed@example.com", sender.msgs[0].To)
}

// TestRequestPasswordReset_UsesTheServiceClock pins that the reset expiry
// comes from the service's own seam and not from the wall clock.
//
// clockSeam's rule is the whole type, not the elevation path inside it. This
// handler read the wall clock twice, two lines apart, under a comment
// insisting "Both timestamps are on the app clock, the clock that wrote the
// expiry" -- so a test that moved the clock moved neither, and the cooldown
// cutoff and the expiry could answer two different instants inside one
// request.
func TestRequestPasswordReset_UsesTheServiceClock(t *testing.T) {
	t.Parallel()

	sender := &recordingMailSender{}
	client, st, authSvc := setupPasswordResetAuthServiceWithClock(t, sender)

	fixed := time.Now().UTC().Add(72 * time.Hour).Truncate(time.Second)
	authSvc.Now = func() time.Time { return fixed }

	userID := id.Generate()
	hash, err := password.Hash("oldpass123")
	require.NoError(t, err)
	require.NoError(t, st.Users().Create(context.Background(), store.CreateUserParams{
		ID: userID, Username: "clocked", PasswordHash: hash, DisplayName: "Clocked",
		Email: "clocked@example.com", EmailVerified: true, PasswordSet: true,
	}))

	_, err = client.RequestPasswordReset(context.Background(), connect.NewRequest(&leapmuxv1.RequestPasswordResetRequest{
		Identifier: "clocked@example.com",
	}))
	require.NoError(t, err)
	require.Len(t, sender.msgs, 1)

	row, err := st.Users().GetByID(context.Background(), userID)
	require.NoError(t, err)
	require.NotNil(t, row.PendingPasswordResetExpiresAt)
	assert.WithinDuration(t, fixed.Add(time.Hour), *row.PendingPasswordResetExpiresAt, time.Minute,
		"the expiry must be measured from the service's clock, not the wall clock")
}
