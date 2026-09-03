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
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// requestRecoveryLinkFor drives RequestAccountRecovery for an identifier on
// a passkey-capable server and returns the token from the sent mail.
func requestRecoveryLinkFor(t *testing.T, client passkeyAuthTestEnv, sender *mailSenderDouble, identifier string) string {
	t.Helper()
	_, err := client.client.RequestAccountRecovery(context.Background(), connect.NewRequest(&leapmuxv1.RequestAccountRecoveryRequest{
		Identifier: identifier,
	}))
	require.NoError(t, err)
	last := sender.last()
	require.NotNil(t, last)
	token := extractAccountRecoveryToken(last.Body)
	require.NotEmpty(t, token)
	return token
}

// beginRecoveryPasskey starts the recovery passkey ceremony with the
// browser origin every other ceremony test uses.
func beginRecoveryPasskey(t *testing.T, env passkeyAuthTestEnv, token string) *leapmuxv1.BeginAccountRecoveryPasskeyResponse {
	t.Helper()
	req := connect.NewRequest(&leapmuxv1.BeginAccountRecoveryPasskeyRequest{Token: token})
	req.Header().Set("Origin", passkeyTestOrigin)
	resp, err := env.client.BeginAccountRecoveryPasskey(context.Background(), req)
	require.NoError(t, err)
	return resp.Msg
}

// TestFinishAccountRecoveryPasskey_ReplacesEverythingWithTheNewPasskey pins
// the passkey path's whole contract: the old passkeys are revoked, the
// password is cleared (not replaced), the sessions die, and the account
// signs in with the credential this flow enrolled. Linked providers stay.
func TestFinishAccountRecoveryPasskey_ReplacesEverythingWithTheNewPasskey(t *testing.T) {
	t.Parallel()

	sender := &mailSenderDouble{}
	env := setupPasskeyAuthTestServer(t, nil, sender)

	userID := id.Generate()
	hash, err := password.Hash("oldpass123")
	require.NoError(t, err)
	require.NoError(t, env.store.Users().Create(context.Background(), store.CreateUserParams{
		ID:                    userID,
		Username:              "pkrecover",
		PasswordHash:          hash,
		DisplayName:           "PK Recover",
		Email:                 "pkrecover@example.com",
		EmailVerified:         true,
		FirstCredentialExempt: true,
	}))
	uid := userid.MustNew(userID)
	oldSession, _, err := auth.CreateSession(context.Background(), env.store, uid, auth.DefaultSessionDuration)
	require.NoError(t, err)

	// seedPasskeyCredential plants a row whose public key no keystore could
	// have produced. Recovery Begin loads no existing credentials -- those
	// rows are revoked at completion -- so this test enrolls a REAL
	// passkey: the second half recovers again and proves the first
	// recovery's passkey was revoked, which is the property that matters.
	token := requestRecoveryLinkFor(t, env, sender, "pkrecover")
	begin := beginRecoveryPasskey(t, env, token)
	ceremony := newPasskeyCeremony()
	credentialJSON, err := ceremony.registrationResponse(begin.GetOptionsJson())
	require.NoError(t, err)

	_, err = env.client.FinishAccountRecoveryPasskey(context.Background(), connect.NewRequest(&leapmuxv1.FinishAccountRecoveryPasskeyRequest{
		Token:          token,
		SessionId:      begin.GetSessionId(),
		CredentialJson: credentialJSON,
	}))
	require.NoError(t, err)

	// Passkey-only now: one credential (the new one), no password, and no
	// surviving session.
	count, err := env.store.PasskeyCredentials().CountByUser(context.Background(), userID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, count)
	row, err := env.store.Users().GetByID(context.Background(), userID)
	require.NoError(t, err)
	assert.False(t, row.FirstCredentialExempt)
	assert.Equal(t, password.PlaceholderHash, row.PasswordHash)
	_, _, _, err = auth.Login(context.Background(), env.store, "pkrecover", "oldpass123", auth.DefaultSessionDuration)
	assert.Error(t, err, "the old password must not sign in after a passkey recovery")
	_, err = env.store.Sessions().GetByID(context.Background(), oldSession, time.Now().UTC())
	assert.ErrorIs(t, err, store.ErrNotFound)

	// The enrolled credential signs in: same ceremony, assertion half.
	loginReq := connect.NewRequest(&leapmuxv1.BeginPasskeyLoginRequest{Username: "pkrecover"})
	loginReq.Header().Set("Origin", passkeyTestOrigin)
	loginBegin, err := env.client.BeginPasskeyLogin(context.Background(), loginReq)
	require.NoError(t, err)
	assertionJSON, err := ceremony.assertionResponse(loginBegin.Msg.GetOptionsJson())
	require.NoError(t, err)
	_, err = env.client.FinishPasskeyLogin(context.Background(), connect.NewRequest(&leapmuxv1.FinishPasskeyLoginRequest{
		SessionId:      loginBegin.Msg.GetSessionId(),
		CredentialJson: assertionJSON,
	}))
	require.NoError(t, err)

	// Recovering AGAIN revokes the first flow's passkey: a fresh ceremony
	// leaves the account holding exactly the new credential, and the old
	// authenticator no longer answers login.
	token2 := requestRecoveryLinkFor(t, env, sender, "pkrecover")
	begin2 := beginRecoveryPasskey(t, env, token2)
	ceremony2 := newPasskeyCeremony()
	credentialJSON2, err := ceremony2.registrationResponse(begin2.GetOptionsJson())
	require.NoError(t, err)
	_, err = env.client.FinishAccountRecoveryPasskey(context.Background(), connect.NewRequest(&leapmuxv1.FinishAccountRecoveryPasskeyRequest{
		Token:          token2,
		SessionId:      begin2.GetSessionId(),
		CredentialJson: credentialJSON2,
	}))
	require.NoError(t, err)
	count, err = env.store.PasskeyCredentials().CountByUser(context.Background(), userID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, count, "the previous recovery's passkey must be revoked")

	reloginReq := connect.NewRequest(&leapmuxv1.BeginPasskeyLoginRequest{Username: "pkrecover"})
	reloginReq.Header().Set("Origin", passkeyTestOrigin)
	reloginBegin, err := env.client.BeginPasskeyLogin(context.Background(), reloginReq)
	require.NoError(t, err)
	oldAssertion, err := ceremony.assertionResponse(reloginBegin.Msg.GetOptionsJson())
	require.NoError(t, err)
	_, err = env.client.FinishPasskeyLogin(context.Background(), connect.NewRequest(&leapmuxv1.FinishPasskeyLoginRequest{
		SessionId:      reloginBegin.Msg.GetSessionId(),
		CredentialJson: oldAssertion,
	}))
	assert.Error(t, err, "the revoked first-recovery passkey must not sign in")
	newAssertion, err := ceremony2.assertionResponse(reloginBegin.Msg.GetOptionsJson())
	require.NoError(t, err)
	_, err = env.client.FinishPasskeyLogin(context.Background(), connect.NewRequest(&leapmuxv1.FinishPasskeyLoginRequest{
		SessionId:      reloginBegin.Msg.GetSessionId(),
		CredentialJson: newAssertion,
	}))
	assert.Error(t, err, "a ceremony session is single-use; the second Finish must refuse")
}

// TestFinishAccountRecoveryPasskey_TokenSpentByPassword fails the
// ceremony's Finish when the link was spent on the password path between
// Begin and Finish: Finish re-checks the live token before it consumes
// the ceremony, so a half-spent flow cannot leave a passkey behind a
// password the user never chose.
func TestFinishAccountRecoveryPasskey_TokenSpentByPassword(t *testing.T) {
	t.Parallel()

	sender := &mailSenderDouble{}
	env := setupPasskeyAuthTestServer(t, nil, sender)

	userID := id.Generate()
	hash, err := password.Hash("oldpass123")
	require.NoError(t, err)
	require.NoError(t, env.store.Users().Create(context.Background(), store.CreateUserParams{
		ID:                    userID,
		Username:              "racespend",
		PasswordHash:          hash,
		DisplayName:           "Race Spend",
		Email:                 "racespend@example.com",
		EmailVerified:         true,
		FirstCredentialExempt: true,
	}))

	token := requestRecoveryLinkFor(t, env, sender, "racespend")
	begin := beginRecoveryPasskey(t, env, token)
	ceremony := newPasskeyCeremony()
	credentialJSON, err := ceremony.registrationResponse(begin.GetOptionsJson())
	require.NoError(t, err)

	_, err = env.client.CompleteAccountRecoveryPassword(context.Background(), connect.NewRequest(&leapmuxv1.CompleteAccountRecoveryPasswordRequest{
		Token:       token,
		NewPassword: "newpass123",
	}))
	require.NoError(t, err)

	_, err = env.client.FinishAccountRecoveryPasskey(context.Background(), connect.NewRequest(&leapmuxv1.FinishAccountRecoveryPasskeyRequest{
		Token:          token,
		SessionId:      begin.GetSessionId(),
		CredentialJson: credentialJSON,
	}))
	// The password path spent the token, so Finish refuses -- never a
	// success, never a half-spent write.
	require.Error(t, err)
	code := connect.CodeOf(err)
	assert.Contains(t, []connect.Code{connect.CodeNotFound, connect.CodeUnauthenticated}, code)

	// The password path's outcome stands: no passkey was written.
	count, err := env.store.PasskeyCredentials().CountByUser(context.Background(), userID)
	require.NoError(t, err)
	assert.EqualValues(t, 0, count)
	_, _, _, err = auth.Login(context.Background(), env.store, "racespend", "newpass123", auth.DefaultSessionDuration)
	require.NoError(t, err)
}

// TestBeginAccountRecoveryPasskey_AfterPasswordComplete refuses Begin
// once CompleteAccountRecoveryPassword has spent the token. The inverse
// of TokenSpentByPassword (Begin, then Complete, then Finish): a user
// who already recovered with a password cannot start a passkey ceremony
// on the same link.
func TestBeginAccountRecoveryPasskey_AfterPasswordComplete(t *testing.T) {
	t.Parallel()

	sender := &mailSenderDouble{}
	env := setupPasskeyAuthTestServer(t, nil, sender)

	require.NoError(t, env.store.Users().Create(context.Background(), store.CreateUserParams{
		ID:            id.Generate(),
		Username:      "spentbegin",
		PasswordHash:  password.PlaceholderHash,
		DisplayName:   "Spent Begin",
		Email:         "spentbegin@example.com",
		EmailVerified: true,
	}))

	token := requestRecoveryLinkFor(t, env, sender, "spentbegin")
	_, err := env.client.CompleteAccountRecoveryPassword(context.Background(), connect.NewRequest(&leapmuxv1.CompleteAccountRecoveryPasswordRequest{
		Token:       token,
		NewPassword: "newpass123",
	}))
	require.NoError(t, err)

	req := connect.NewRequest(&leapmuxv1.BeginAccountRecoveryPasskeyRequest{Token: token})
	req.Header().Set("Origin", passkeyTestOrigin)
	_, err = env.client.BeginAccountRecoveryPasskey(context.Background(), req)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// TestBeginAccountRecoveryPasskey_InvalidToken pins that a wrong token
// draws the same NotFound the password path answers with. Consume matches
// no row, so this path does not charge the attempt budget.
func TestBeginAccountRecoveryPasskey_InvalidToken(t *testing.T) {
	t.Parallel()

	sender := &mailSenderDouble{}
	env := setupPasskeyAuthTestServer(t, nil, sender)

	req := connect.NewRequest(&leapmuxv1.BeginAccountRecoveryPasskeyRequest{Token: "not-a-real-token"})
	req.Header().Set("Origin", passkeyTestOrigin)
	_, err := env.client.BeginAccountRecoveryPasskey(context.Background(), req)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// TestBeginAccountRecoveryPasskey_ChargesTheSharedBudget proves the two
// paths draw on ONE attempt budget: burning the cap through Begin refuses
// the password path too.
func TestBeginAccountRecoveryPasskey_ChargesTheSharedBudget(t *testing.T) {
	t.Parallel()

	sender := &mailSenderDouble{}
	env := setupPasskeyAuthTestServer(t, nil, sender)

	require.NoError(t, env.store.Users().Create(context.Background(), store.CreateUserParams{
		ID:            id.Generate(),
		Username:      "budgetburn",
		PasswordHash:  password.PlaceholderHash,
		DisplayName:   "Budget Burn",
		Email:         "budgetburn@example.com",
		EmailVerified: true,
	}))

	token := requestRecoveryLinkFor(t, env, sender, "budgetburn")
	for range 5 {
		req := connect.NewRequest(&leapmuxv1.BeginAccountRecoveryPasskeyRequest{Token: token})
		req.Header().Set("Origin", passkeyTestOrigin)
		_, err := env.client.BeginAccountRecoveryPasskey(context.Background(), req)
		require.NoError(t, err)
	}
	req := connect.NewRequest(&leapmuxv1.BeginAccountRecoveryPasskeyRequest{Token: token})
	req.Header().Set("Origin", passkeyTestOrigin)
	_, err := env.client.BeginAccountRecoveryPasskey(context.Background(), req)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	_, err = env.client.CompleteAccountRecoveryPassword(context.Background(), connect.NewRequest(&leapmuxv1.CompleteAccountRecoveryPasswordRequest{
		Token:       token,
		NewPassword: "newpass123",
	}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// TestAccountRecovery_InterleavedPasskeyBeginThenPasswordComplete proves
// the shared budget is a running counter across both paths: four cancelled
// passkey Begins leave one charge, and CompleteAccountRecoveryPassword
// still spends the token.
func TestAccountRecovery_InterleavedPasskeyBeginThenPasswordComplete(t *testing.T) {
	t.Parallel()

	sender := &mailSenderDouble{}
	env := setupPasskeyAuthTestServer(t, nil, sender)

	require.NoError(t, env.store.Users().Create(context.Background(), store.CreateUserParams{
		ID:            id.Generate(),
		Username:      "interleave",
		PasswordHash:  password.PlaceholderHash,
		DisplayName:   "Interleave",
		Email:         "interleave@example.com",
		EmailVerified: true,
	}))

	token := requestRecoveryLinkFor(t, env, sender, "interleave")
	for range 4 {
		req := connect.NewRequest(&leapmuxv1.BeginAccountRecoveryPasskeyRequest{Token: token})
		req.Header().Set("Origin", passkeyTestOrigin)
		_, err := env.client.BeginAccountRecoveryPasskey(context.Background(), req)
		require.NoError(t, err)
	}
	_, err := env.client.CompleteAccountRecoveryPassword(context.Background(), connect.NewRequest(&leapmuxv1.CompleteAccountRecoveryPasswordRequest{
		Token:       token,
		NewPassword: "newpass123",
	}))
	require.NoError(t, err)
	_, _, _, err = auth.Login(context.Background(), env.store, "interleave", "newpass123", auth.DefaultSessionDuration)
	require.NoError(t, err)

	req := connect.NewRequest(&leapmuxv1.BeginAccountRecoveryPasskeyRequest{Token: token})
	req.Header().Set("Origin", passkeyTestOrigin)
	_, err = env.client.BeginAccountRecoveryPasskey(context.Background(), req)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err), "a spent token must refuse both paths")
}

// TestBeginAccountRecoveryPasskey_UnservedOrigin pins the precondition
// answer every passkey surface gives: a hub that runs no ceremony at the
// caller's origin refuses the Begin before it charges anything about the
// account, and the recovery page hides the option for the same reason.
func TestBeginAccountRecoveryPasskey_UnservedOrigin(t *testing.T) {
	t.Parallel()

	sender := &mailSenderDouble{}
	env := setupPasskeyAuthTestServer(t, nil, sender)

	userID := id.Generate()
	require.NoError(t, env.store.Users().Create(context.Background(), store.CreateUserParams{
		ID:            userID,
		Username:      "wrongorigin",
		PasswordHash:  password.PlaceholderHash,
		DisplayName:   "Wrong Origin",
		Email:         "wrongorigin@example.com",
		EmailVerified: true,
	}))
	token := requestRecoveryLinkFor(t, env, sender, "wrongorigin")

	req := connect.NewRequest(&leapmuxv1.BeginAccountRecoveryPasskeyRequest{Token: token})
	req.Header().Set("Origin", "https://elsewhere.example.com")
	_, err := env.client.BeginAccountRecoveryPasskey(context.Background(), req)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	row, err := env.store.Users().GetByID(context.Background(), userID)
	require.NoError(t, err)
	assert.Zero(t, row.PendingRecoveryAttempts, "an unserved origin must not charge the recovery budget")
}

// TestBeginAccountRecoveryPasskey_UndecryptableExistingPasskey pins that
// recovery Begin loads no existing credentials: a row whose ciphertext no
// keystore can decrypt must not block the flow that is about to revoke it.
func TestBeginAccountRecoveryPasskey_UndecryptableExistingPasskey(t *testing.T) {
	t.Parallel()

	sender := &mailSenderDouble{}
	env := setupPasskeyAuthTestServer(t, nil, sender)

	userID := id.Generate()
	require.NoError(t, env.store.Users().Create(context.Background(), store.CreateUserParams{
		ID:            userID,
		Username:      "badcipher",
		PasswordHash:  password.PlaceholderHash,
		DisplayName:   "Bad Cipher",
		Email:         "badcipher@example.com",
		EmailVerified: true,
	}))
	seedPasskeyCredential(t, env.store, userID, "Lost")

	token := requestRecoveryLinkFor(t, env, sender, "badcipher")
	begin := beginRecoveryPasskey(t, env, token)
	require.NotEmpty(t, begin.GetSessionId())
	require.NotEmpty(t, begin.GetOptionsJson())

	row, err := env.store.Users().GetByID(context.Background(), userID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, row.PendingRecoveryAttempts)
}

// TestFinishAccountRecoveryPasskey_RefusesAfterForceExpire pins that a
// sixth Begin force-expires the token, so Finish of the fifth ceremony
// cannot spend it, and neither can the password path.
func TestFinishAccountRecoveryPasskey_RefusesAfterForceExpire(t *testing.T) {
	t.Parallel()

	sender := &mailSenderDouble{}
	env := setupPasskeyAuthTestServer(t, nil, sender)

	userID := id.Generate()
	require.NoError(t, env.store.Users().Create(context.Background(), store.CreateUserParams{
		ID:            userID,
		Username:      "forceexpire",
		PasswordHash:  password.PlaceholderHash,
		DisplayName:   "Force Expire",
		Email:         "forceexpire@example.com",
		EmailVerified: true,
	}))

	token := requestRecoveryLinkFor(t, env, sender, "forceexpire")
	var last *leapmuxv1.BeginAccountRecoveryPasskeyResponse
	for range 5 {
		last = beginRecoveryPasskey(t, env, token)
	}
	req := connect.NewRequest(&leapmuxv1.BeginAccountRecoveryPasskeyRequest{Token: token})
	req.Header().Set("Origin", passkeyTestOrigin)
	_, err := env.client.BeginAccountRecoveryPasskey(context.Background(), req)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	ceremony := newPasskeyCeremony()
	credentialJSON, err := ceremony.registrationResponse(last.GetOptionsJson())
	require.NoError(t, err)
	_, err = env.client.FinishAccountRecoveryPasskey(context.Background(), connect.NewRequest(&leapmuxv1.FinishAccountRecoveryPasskeyRequest{
		Token:          token,
		SessionId:      last.GetSessionId(),
		CredentialJson: credentialJSON,
	}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	count, err := env.store.PasskeyCredentials().CountByUser(context.Background(), userID)
	require.NoError(t, err)
	assert.EqualValues(t, 0, count)

	_, err = env.client.CompleteAccountRecoveryPassword(context.Background(), connect.NewRequest(&leapmuxv1.CompleteAccountRecoveryPasswordRequest{
		Token:       token,
		NewPassword: "newpass123",
	}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// TestAccountRecoveryPasskey_RequiredFields pins the argument validation
// both paths run before any store write: an empty token, session, or
// credential answers InvalidArgument without charging the attempt budget
// or consuming a ceremony session.
func TestAccountRecoveryPasskey_RequiredFields(t *testing.T) {
	t.Parallel()

	sender := &mailSenderDouble{}
	env := setupPasskeyAuthTestServer(t, nil, sender)

	_, err := env.client.BeginAccountRecoveryPasskey(context.Background(),
		connect.NewRequest(&leapmuxv1.BeginAccountRecoveryPasskeyRequest{}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = env.client.FinishAccountRecoveryPasskey(context.Background(),
		connect.NewRequest(&leapmuxv1.FinishAccountRecoveryPasskeyRequest{Token: "tok"}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}
