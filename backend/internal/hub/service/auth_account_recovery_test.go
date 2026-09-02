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
	"github.com/leapmux/leapmux/internal/hub/bootstrap"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/servicetest"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/usernames"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

func setupAccountRecoveryAuthService(t *testing.T, sender mail.Sender) (leapmuxv1connect.AuthServiceClient, store.Store) {
	client, st, _ := setupAccountRecoveryAuthServiceWithClock(t, sender, nil)
	return client, st
}

// setupAccountRecoveryAuthServiceWithClock builds the recovery harness:
// store, settings, interceptor, service, and a connect client against an
// httptest server. configure runs against the SAME manager the service
// reads, so a seeded SMTP row is visible to the service under test; nil
// leaves the defaults.
func setupAccountRecoveryAuthServiceWithClock(t *testing.T, sender mail.Sender, configure func(t *testing.T, set *settings.Manager)) (leapmuxv1connect.AuthServiceClient, store.Store, *service.AuthService) {
	t.Helper()

	st := hubtestutil.OpenTestStore(t)
	set := servicetest.NewSettingsManager(t, st, nil)
	enableSignup(t, set)
	if configure != nil {
		configure(t, set)
	}

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

func extractAccountRecoveryToken(body string) string {
	const marker = "/recover-account/complete?token="
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

// assertRecoveryTokenShape pins the recovery link's token form: the shared
// id mint's 48-character alphanumeric secret, the same shape session ids
// and API access secrets carry.
func assertRecoveryTokenShape(t *testing.T, token string) {
	t.Helper()
	assert.Regexp(t, "^[A-Za-z0-9]{48}$", token)
}

func TestRequestAccountRecovery_UnknownUser_EmptySuccess(t *testing.T) {
	t.Parallel()

	sender := &mailSenderDouble{}
	client, _ := setupAccountRecoveryAuthService(t, sender)

	_, err := client.RequestAccountRecovery(context.Background(), connect.NewRequest(&leapmuxv1.RequestAccountRecoveryRequest{
		Identifier: "nobody@example.com",
	}))
	require.NoError(t, err)
	assert.Empty(t, sender.snapshot())
}

func TestRequestAccountRecovery_KnownUser_SendsMail(t *testing.T) {
	t.Parallel()

	sender := &mailSenderDouble{}
	client, st := setupAccountRecoveryAuthService(t, sender)

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

	_, err = client.RequestAccountRecovery(context.Background(), connect.NewRequest(&leapmuxv1.RequestAccountRecoveryRequest{
		Identifier: "resetme@example.com",
	}))
	require.NoError(t, err)
	require.Len(t, sender.snapshot(), 1)
	assert.Equal(t, "resetme@example.com", sender.snapshot()[0].To)

	token := extractAccountRecoveryToken(sender.snapshot()[0].Body)
	require.NotEmpty(t, token)
	assertRecoveryTokenShape(t, token)

	user, err := st.Users().GetByID(context.Background(), userID)
	require.NoError(t, err)
	assert.NotEmpty(t, user.PendingRecoveryToken)
	assert.NotNil(t, user.PendingRecoveryExpiresAt)

	_, err = client.CompleteAccountRecoveryPassword(context.Background(), connect.NewRequest(&leapmuxv1.CompleteAccountRecoveryPasswordRequest{
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

func TestRequestAccountRecovery_MailFailure_ClearsToken(t *testing.T) {
	t.Parallel()

	sender := &mailSenderDouble{err: errors.New("smtp down")}
	client, st := setupAccountRecoveryAuthService(t, sender)

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

	_, err = client.RequestAccountRecovery(context.Background(), connect.NewRequest(&leapmuxv1.RequestAccountRecoveryRequest{
		Identifier: "mailfail@example.com",
	}))
	require.NoError(t, err)

	user, err := st.Users().GetByID(context.Background(), userID)
	require.NoError(t, err)
	assert.Empty(t, user.PendingRecoveryToken)
}

func TestCompleteAccountRecoveryPassword_InvalidToken_NotFound(t *testing.T) {
	t.Parallel()

	client, st := setupAccountRecoveryAuthService(t, mail.NewStubSender())

	userID := id.Generate()
	hash, err := password.Hash("oldpass123")
	require.NoError(t, err)
	require.NoError(t, st.Users().Create(context.Background(), store.CreateUserParams{
		ID:           userID,
		Username:     "badtoken",
		PasswordHash: hash,
		PasswordSet:  true,
	}))

	_, err = client.CompleteAccountRecoveryPassword(context.Background(), connect.NewRequest(&leapmuxv1.CompleteAccountRecoveryPasswordRequest{
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

func TestCompleteAccountRecoveryPassword_EmptyToken_InvalidArgument(t *testing.T) {
	t.Parallel()

	client, _ := setupAccountRecoveryAuthService(t, mail.NewStubSender())

	_, err := client.CompleteAccountRecoveryPassword(context.Background(), connect.NewRequest(&leapmuxv1.CompleteAccountRecoveryPasswordRequest{
		Token:       "",
		NewPassword: "newpass123",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestCompleteAccountRecoveryPassword_WeakPasswordDoesNotCharge(t *testing.T) {
	t.Parallel()

	sender := &mailSenderDouble{}
	client, st := setupAccountRecoveryAuthService(t, sender)

	userID := id.Generate()
	hash, err := password.Hash("oldpass123")
	require.NoError(t, err)
	require.NoError(t, st.Users().Create(context.Background(), store.CreateUserParams{
		ID:            userID,
		Username:      "weakpw",
		PasswordHash:  hash,
		Email:         "weakpw@example.com",
		EmailVerified: true,
		PasswordSet:   true,
	}))

	_, err = client.RequestAccountRecovery(context.Background(), connect.NewRequest(&leapmuxv1.RequestAccountRecoveryRequest{
		Identifier: "weakpw@example.com",
	}))
	require.NoError(t, err)
	require.Len(t, sender.snapshot(), 1)
	token := extractAccountRecoveryToken(sender.snapshot()[0].Body)
	require.NotEmpty(t, token)

	_, err = client.CompleteAccountRecoveryPassword(context.Background(), connect.NewRequest(&leapmuxv1.CompleteAccountRecoveryPasswordRequest{
		Token:       token,
		NewPassword: "short",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	user, err := st.Users().GetByID(context.Background(), userID)
	require.NoError(t, err)
	assert.Zero(t, user.PendingRecoveryAttempts, "password-format refusal must run before the attempt charge")
	require.NotEmpty(t, user.PendingRecoveryToken)

	_, err = client.CompleteAccountRecoveryPassword(context.Background(), connect.NewRequest(&leapmuxv1.CompleteAccountRecoveryPasswordRequest{
		Token:       token,
		NewPassword: "newpass123",
	}))
	require.NoError(t, err)
}

func TestCompleteAccountRecoveryPassword_Success_WipesPasskeysAndSessions(t *testing.T) {
	t.Parallel()

	sender := &mailSenderDouble{}
	client, st := setupAccountRecoveryAuthService(t, sender)

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

	_, err = client.RequestAccountRecovery(context.Background(), connect.NewRequest(&leapmuxv1.RequestAccountRecoveryRequest{
		Identifier: "wipeme",
	}))
	require.NoError(t, err)
	require.Len(t, sender.snapshot(), 1)
	token := extractAccountRecoveryToken(sender.snapshot()[0].Body)
	require.NotEmpty(t, token)
	assertRecoveryTokenShape(t, token)

	_, err = client.CompleteAccountRecoveryPassword(context.Background(), connect.NewRequest(&leapmuxv1.CompleteAccountRecoveryPasswordRequest{
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

// TestRequestAccountRecovery_PasswordlessAccount_SendsLink pins the rule the
// docs promise: recovery verifies the account's EMAIL, not the method the
// user lost. A passwordless account (passkey-only or provider-only — the
// store cannot tell the difference, and neither may the flow) with a
// verified address gets the same link a password account gets.
func TestRequestAccountRecovery_PasswordlessAccount_SendsLink(t *testing.T) {
	t.Parallel()

	sender := &mailSenderDouble{}
	client, st := setupAccountRecoveryAuthService(t, sender)

	userID := id.Generate()
	require.NoError(t, st.Users().Create(context.Background(), store.CreateUserParams{
		ID:            userID,
		Username:      "passkeyonly",
		PasswordHash:  password.PlaceholderHash,
		DisplayName:   "Passkey Only",
		Email:         "passkeyonly@example.com",
		EmailVerified: true,
		PasswordSet:   false,
	}))

	_, err := client.RequestAccountRecovery(context.Background(), connect.NewRequest(&leapmuxv1.RequestAccountRecoveryRequest{
		Identifier: "passkeyonly",
	}))
	require.NoError(t, err)
	require.Len(t, sender.snapshot(), 1)
	assert.Equal(t, "passkeyonly@example.com", sender.snapshot()[0].To)
	token := extractAccountRecoveryToken(sender.snapshot()[0].Body)
	require.NotEmpty(t, token)

	row, err := st.Users().GetByID(context.Background(), userID)
	require.NoError(t, err)
	assert.NotEmpty(t, row.PendingRecoveryToken)
}

// TestCompleteAccountRecoveryPassword_SetsFirstPassword pins the passwordless half of
// the flow: spending the link on an account that never had a password sets
// its FIRST one, wipes the passkeys it could no longer use, and leaves the
// account signing in with the new password.
func TestCompleteAccountRecoveryPassword_SetsFirstPassword(t *testing.T) {
	t.Parallel()

	sender := &mailSenderDouble{}
	client, st := setupAccountRecoveryAuthService(t, sender)

	userID := id.Generate()
	require.NoError(t, st.Users().Create(context.Background(), store.CreateUserParams{
		ID:            userID,
		Username:      "recoverless",
		PasswordHash:  password.PlaceholderHash,
		DisplayName:   "Recover Less",
		Email:         "recoverless@example.com",
		EmailVerified: true,
		PasswordSet:   false,
	}))
	seedPasskeyCredential(t, st, userID, "Lost Laptop")

	_, err := client.RequestAccountRecovery(context.Background(), connect.NewRequest(&leapmuxv1.RequestAccountRecoveryRequest{
		Identifier: "recoverless@example.com",
	}))
	require.NoError(t, err)
	require.Len(t, sender.snapshot(), 1)
	token := extractAccountRecoveryToken(sender.snapshot()[0].Body)
	require.NotEmpty(t, token)

	_, err = client.CompleteAccountRecoveryPassword(context.Background(), connect.NewRequest(&leapmuxv1.CompleteAccountRecoveryPasswordRequest{
		Token:       token,
		NewPassword: "firstpass123",
	}))
	require.NoError(t, err)

	row, err := st.Users().GetByID(context.Background(), userID)
	require.NoError(t, err)
	assert.True(t, row.PasswordSet, "recovery must set the account's first password")
	match, err := password.Verify(row.PasswordHash, "firstpass123")
	require.NoError(t, err)
	assert.True(t, match)

	count, err := st.PasskeyCredentials().CountByUser(context.Background(), userID)
	require.NoError(t, err)
	assert.EqualValues(t, 0, count, "the lost passkey must be wiped by the break-glass posture")

	_, _, _, err = auth.Login(context.Background(), st, "recoverless", "firstpass123", auth.DefaultSessionDuration)
	require.NoError(t, err)
}

func TestCompleteAccountRecoveryPassword_ExpiredToken_NotFound(t *testing.T) {
	t.Parallel()

	sender := &mailSenderDouble{}
	client, st := setupAccountRecoveryAuthService(t, sender)

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

	_, err = client.RequestAccountRecovery(context.Background(), connect.NewRequest(&leapmuxv1.RequestAccountRecoveryRequest{
		Identifier: "expired@example.com",
	}))
	require.NoError(t, err)
	require.Len(t, sender.snapshot(), 1)
	token := extractAccountRecoveryToken(sender.snapshot()[0].Body)
	require.NotEmpty(t, token)
	assertRecoveryTokenShape(t, token)

	user, err := st.Users().GetByID(context.Background(), userID)
	require.NoError(t, err)
	// Overwrite the live link with an expired one: the gate's Now moves
	// past the deadline the live mint armed, so this seed lands.
	_, err = st.Users().SetPendingRecovery(context.Background(), store.SetPendingRecoveryParams{
		ID:                         userID,
		PendingRecoveryUnblockedAt: time.Now().Add(-5 * time.Minute).UTC(),
		PendingRecoveryToken:       user.PendingRecoveryToken,
		PendingRecoveryExpiresAt:   time.Now().UTC().Add(-time.Hour),
		Now:                        time.Now().UTC().Add(2 * time.Minute),
	})
	require.NoError(t, err)

	_, err = client.CompleteAccountRecoveryPassword(context.Background(), connect.NewRequest(&leapmuxv1.CompleteAccountRecoveryPasswordRequest{
		Token:       token,
		NewPassword: "newpass123",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestCompleteAccountRecoveryPassword_AttemptLimitBlocksBeforeArgon2(t *testing.T) {
	t.Parallel()

	sender := &mailSenderDouble{}
	client, st := setupAccountRecoveryAuthService(t, sender)

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

	_, err = client.RequestAccountRecovery(context.Background(), connect.NewRequest(&leapmuxv1.RequestAccountRecoveryRequest{
		Identifier: "attemptcap@example.com",
	}))
	require.NoError(t, err)
	require.Len(t, sender.snapshot(), 1)
	token := extractAccountRecoveryToken(sender.snapshot()[0].Body)
	require.NotEmpty(t, token)
	assertRecoveryTokenShape(t, token)

	// Force the 5 charges through the store: CompleteAccountRecoveryPassword
	// validates password format before it charges, so a well-formed new
	// password would spend the token rather than burn an attempt.
	for i := 0; i < 5; i++ {
		_, err = st.Users().ConsumeRecoveryAttemptByToken(context.Background(), tokenHashForTest(token), time.Now().UTC(), 5)
		require.NoError(t, err)
	}

	// Sixth Complete must refuse after Consume sets attempts>5, before a
	// successful password change (password stays oldpass123).
	_, err = client.CompleteAccountRecoveryPassword(context.Background(), connect.NewRequest(&leapmuxv1.CompleteAccountRecoveryPasswordRequest{
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
	assert.Greater(t, user.PendingRecoveryAttempts, int64(5))
}

func TestRequestAccountRecovery_InvalidUsername_EmptySuccess(t *testing.T) {
	t.Parallel()

	sender := &mailSenderDouble{}
	client, _ := setupAccountRecoveryAuthService(t, sender)

	_, err := client.RequestAccountRecovery(context.Background(), connect.NewRequest(&leapmuxv1.RequestAccountRecoveryRequest{
		Identifier: "!!!not-a-slug!!!",
	}))
	require.NoError(t, err)
	assert.Empty(t, sender.snapshot())
}

func TestRequestAccountRecovery_UsernameIsSanitized(t *testing.T) {
	t.Parallel()

	sender := &mailSenderDouble{}
	client, st := setupAccountRecoveryAuthService(t, sender)

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

	_, err = client.RequestAccountRecovery(context.Background(), connect.NewRequest(&leapmuxv1.RequestAccountRecoveryRequest{
		Identifier: "ResetMe",
	}))
	require.NoError(t, err)
	require.Len(t, sender.snapshot(), 1)
}

func tokenHashForTest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// TestRequestAccountRecovery_CooldownKeepsPreviousLink pins the per-account
// resend cooldown: a second request inside the window must not mint a fresh
// token (which would invalidate the link the first email still carries and
// flood the inbox), and the first token must still complete.
func TestRequestAccountRecovery_CooldownKeepsPreviousLink(t *testing.T) {
	sender := &mailSenderDouble{}
	client, st := setupAccountRecoveryAuthServiceConfigured(t, sender, seedSMTP)

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
		_, err := client.RequestAccountRecovery(context.Background(), connect.NewRequest(&leapmuxv1.RequestAccountRecoveryRequest{
			Identifier: "cooldown@example.com",
		}))
		require.NoError(t, err)
	}
	require.Len(t, sender.snapshot(), 1, "cooldown must suppress the second send")

	token := extractAccountRecoveryToken(sender.snapshot()[0].Body)
	require.NotEmpty(t, token)
	assertRecoveryTokenShape(t, token)
	_, err := client.CompleteAccountRecoveryPassword(context.Background(), connect.NewRequest(&leapmuxv1.CompleteAccountRecoveryPasswordRequest{
		Token:       token,
		NewPassword: "freshpass123",
	}))
	require.NoError(t, err, "the first link must stay completable inside the cooldown window")
}

// TestRequestAccountRecovery_BurnedBudgetKeepsCooldown is the recovery twin
// of the burned-budget verification pin. Charging all five token attempts
// force-expires the link in SQL, and an expiry-derived gate read that as
// "issued a full lifetime ago" and re-minted immediately. The gate reads
// the issued-at column, so burning the budget the holder already owns
// cannot hurry the next email.
func TestRequestAccountRecovery_BurnedBudgetKeepsCooldown(t *testing.T) {
	sender := &mailSenderDouble{}
	client, st := setupAccountRecoveryAuthServiceConfigured(t, sender, seedSMTP)
	ctx := context.Background()

	userID := id.Generate()
	require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
		ID:            userID,
		Username:      "burned-budget",
		PasswordHash:  password.PlaceholderHash,
		DisplayName:   "Burned Budget",
		Email:         "burned@example.com",
		EmailVerified: true,
		PasswordSet:   true,
	}))

	_, err := client.RequestAccountRecovery(ctx, connect.NewRequest(&leapmuxv1.RequestAccountRecoveryRequest{
		Identifier: "burned@example.com",
	}))
	require.NoError(t, err)
	require.Len(t, sender.snapshot(), 1)
	token := extractAccountRecoveryToken(sender.snapshot()[0].Body)
	require.NotEmpty(t, token)

	// Burn the whole attempt budget against the real token; the 6th charge
	// force-expires the link in SQL (the expiry moves to now).
	for i := 0; i < 6; i++ {
		_, err = st.Users().ConsumeRecoveryAttemptByToken(ctx, tokenHashForTest(token), time.Now().UTC(), 5)
		require.NoError(t, err)
	}

	// The link is expired, but it was issued seconds ago: the next request
	// must answer the same empty success and send nothing.
	_, err = client.RequestAccountRecovery(ctx, connect.NewRequest(&leapmuxv1.RequestAccountRecoveryRequest{
		Identifier: "burned@example.com",
	}))
	require.NoError(t, err, "the response is uniform; only the mail differs")
	require.Len(t, sender.snapshot(), 1, "burning the attempt budget must not reset the resend cooldown")
}

// TestSignUp_MailFailureDoesNotLeakTransportDetails pins that a fail-closed
// signup reports a generic error to the anonymous caller: the SMTP
// transport chain specifies the relay host and port, and that detail stays in
// the server log, never in the client response.
func TestSignUp_MailFailureDoesNotLeakTransportDetails(t *testing.T) {
	sender := &mailSenderDouble{err: errors.New("dial tcp smtp.secret-relay.example:587: connection refused")}
	client, st := setupAccountRecoveryAuthServiceConfigured(t, sender, seedSMTP)

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

// setupAccountRecoveryAuthServiceConfigured is the harness with a settings
// callback, for the tests that seed SMTP or flip a flag.
func setupAccountRecoveryAuthServiceConfigured(t *testing.T, sender mail.Sender, configure func(t *testing.T, set *settings.Manager)) (leapmuxv1connect.AuthServiceClient, store.Store) {
	client, st, _ := setupAccountRecoveryAuthServiceWithClock(t, sender, configure)
	return client, st
}

// TestRequestAccountRecovery_AdminUnconfirmedAddressGetsNoLink is the recovery
// route this change closes.
//
// A recovery link is a credential, and the Hub only sends one to an address it
// has evidence for. email_verified IS that evidence, and the hub used to
// force it true for every administrator -- so an administrator moved to a
// brand-new address kept a live self-service recovery route to whatever address
// the request carried, on the highest-privilege accounts on the hub. The
// flag now records only what somebody confirmed, and the administrator
// exemption lives at the login gate where it belongs.
func TestRequestAccountRecovery_AdminUnconfirmedAddressGetsNoLink(t *testing.T) {
	t.Parallel()

	// SMTP configured, so the hub REQUIRES verification -- which is the
	// deployment where a recovery link is a credential and the flag decides
	// whether the hub has evidence for the address.
	sender := &mailSenderDouble{}
	client, st := setupAccountRecoveryAuthServiceConfigured(t, sender, seedSMTP)
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

	_, err = client.RequestAccountRecovery(ctx, connect.NewRequest(&leapmuxv1.RequestAccountRecoveryRequest{
		Identifier: "unconfirmed@example.com",
	}))
	require.NoError(t, err, "the response is uniform; only the mail differs")
	assert.Empty(t, sender.snapshot(), "no link to an address the hub never confirmed, administrator or not")

	row, err := st.Users().GetByID(ctx, unconfirmed)
	require.NoError(t, err)
	assert.Empty(t, row.PendingRecoveryToken, "and no token was minted")

	// An administrator who really did confirm their address still gets one:
	// the rule is about the ADDRESS, not about the privilege.
	confirmed := id.Generate()
	require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
		ID: confirmed, Username: "confirmedadmin", PasswordHash: hash,
		DisplayName: "Confirmed", Email: "confirmed@example.com",
		EmailVerified: true, PasswordSet: true, IsAdmin: true,
	}))

	_, err = client.RequestAccountRecovery(ctx, connect.NewRequest(&leapmuxv1.RequestAccountRecoveryRequest{
		Identifier: "confirmed@example.com",
	}))
	require.NoError(t, err)
	require.Len(t, sender.snapshot(), 1)
	assert.Equal(t, "confirmed@example.com", sender.snapshot()[0].To)
}

// TestRequestAccountRecovery_UsesTheServiceClock pins that the recovery expiry
// comes from the service's own seam and not from the wall clock.
//
// clockSeam's rule is the whole type, not the elevation path inside it. This
// handler read the wall clock twice, two lines apart, under a comment
// insisting "Both timestamps are on the app clock, the clock that wrote the
// expiry" -- so a test that moved the clock moved neither, and the cooldown
// cutoff and the expiry could answer two different instants inside one
// request.
func TestRequestAccountRecovery_UsesTheServiceClock(t *testing.T) {
	t.Parallel()

	sender := &mailSenderDouble{}
	client, st, authSvc := setupAccountRecoveryAuthServiceWithClock(t, sender, nil)

	fixed := time.Now().UTC().Add(72 * time.Hour).Truncate(time.Second)
	authSvc.Now = func() time.Time { return fixed }

	userID := id.Generate()
	hash, err := password.Hash("oldpass123")
	require.NoError(t, err)
	require.NoError(t, st.Users().Create(context.Background(), store.CreateUserParams{
		ID: userID, Username: "clocked", PasswordHash: hash, DisplayName: "Clocked",
		Email: "clocked@example.com", EmailVerified: true, PasswordSet: true,
	}))

	_, err = client.RequestAccountRecovery(context.Background(), connect.NewRequest(&leapmuxv1.RequestAccountRecoveryRequest{
		Identifier: "clocked@example.com",
	}))
	require.NoError(t, err)
	require.Len(t, sender.snapshot(), 1)

	row, err := st.Users().GetByID(context.Background(), userID)
	require.NoError(t, err)
	require.NotNil(t, row.PendingRecoveryExpiresAt)
	assert.WithinDuration(t, fixed.Add(time.Hour), *row.PendingRecoveryExpiresAt, time.Minute,
		"the expiry must be measured from the service's clock, not the wall clock")
}

// TestSoloRefusesEveryPathThatCanClearAPassword pins the premise
// auth.SoloGate's one-way latch rests on.
//
// The gate caches "the solo account holds a password" and never clears the
// cache, which is sound only while no in-process path can REMOVE that
// password. Exactly one can: account recovery, which replaces a password with
// a passkey by storing PlaceholderHash. Solo mode refuses all three of its
// verbs, so the latch cannot go stale.
//
// If a later change makes recovery reachable in solo, this test fails and
// points at the latch that then needs invalidating -- rather than the hub
// silently demanding a password that no longer exists, which locks the owner
// out of every TCP address at once.
func TestSoloRefusesEveryPathThatCanClearAPassword(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	require.NoError(t, bootstrap.Run(context.Background(), st, true))
	// A LIVE password, because that is what the premise protects. The
	// bootstrap alone leaves the hash empty, and an empty hash differs from
	// PlaceholderHash before a single verb runs -- so the closing assertion
	// held whatever the three verbs did.
	setSoloPasswordForTest(t, st)

	svc := service.NewAuthService(servicetest.AuthServiceDeps(
		st,
		&config.Config{SoloMode: true},
		servicetest.NewSettingsManager(t, st, nil),
		auth.NewCredentialLifecycleEffects(nil, nil, nil),
	))
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"CompleteAccountRecoveryPassword", func() error {
			_, err := svc.CompleteAccountRecoveryPassword(ctx, connect.NewRequest(
				&leapmuxv1.CompleteAccountRecoveryPasswordRequest{Token: "t", NewPassword: "irrelevant-password"}))
			return err
		}},
		{"BeginAccountRecoveryPasskey", func() error {
			_, err := svc.BeginAccountRecoveryPasskey(ctx, connect.NewRequest(
				&leapmuxv1.BeginAccountRecoveryPasskeyRequest{Token: "t"}))
			return err
		}},
		// The one that actually writes PlaceholderHash over a live password.
		{"FinishAccountRecoveryPasskey", func() error {
			_, err := svc.FinishAccountRecoveryPasskey(ctx, connect.NewRequest(
				&leapmuxv1.FinishAccountRecoveryPasskeyRequest{Token: "t"}))
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			require.Error(t, err, "solo must refuse this verb; auth.SoloGate's latch depends on it")
			assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
		})
	}

	// The premise holds only while the account's password survives all three.
	user, err := st.Users().GetByUsername(ctx, usernames.Solo)
	require.NoError(t, err)
	assert.True(t, password.IsUsable(user.PasswordHash),
		"no refused verb may have cleared the password on its way out")
	assert.NotEqual(t, password.PlaceholderHash, user.PasswordHash)
}
