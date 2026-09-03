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
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// elevateAPITokenRow stamps the ordinary step-up window on a command-line
// credential, through the REAL store write the /oauth/step-up
// leg performs. The session twin is elevateSessionRow.
//
// Call it BEFORE the first RPC on that credential: there is no cached
// UserInfo entry to evict yet, which is why this takes no registry.
func elevateAPITokenRow(t *testing.T, st store.Store, tokenID, userID string) {
	t.Helper()
	now := time.Now().UTC()
	n, err := st.APITokens().Elevate(context.Background(), store.ElevateAPITokenParams{
		TokenID:            tokenID,
		UserID:             userid.MustNew(userID),
		ElevationProvenAt:  now,
		ElevationExpiresAt: now.Add(auth.ElevationWindow),
	}, now)
	require.NoError(t, err)
	require.EqualValues(t, 1, n, "the credential must exist and be live to elevate")
}

// tokenIDOf reads the api_tokens row id back out of a bearer string, so a
// test can address the row the credential authenticates against.
func tokenIDOf(t *testing.T, bearer string) string {
	t.Helper()
	_, tokenID, _, err := auth.ParseBearer(bearer)
	require.NoError(t, err)
	return tokenID
}

// TestChangePassword_FromAnElevatedCLICredentialKeepsThatCredentialUsable is
// the regression for a credential that destroyed itself by succeeding.
//
// revokeOtherCredentialsPreservingActingCredential preserved the acting
// SESSION and nothing else, and auth.RevokeAllUserCredentials revoked every
// api_tokens row with no exclusion. So a command-line credential that changed
// its owner's password committed the change and then answered Unauthenticated
// on every later call, with the credential file still on disk and no message
// that explains it.
//
// Preserving takes TWO writes, and this fails against either one alone: the
// revoke must skip the row, and the restamp must move it onto the account's
// new auth_generation. Bearer validation refuses a row behind
// users.auth_generation whether or not revoked_at is set.
func TestChangePassword_FromAnElevatedCLICredentialKeepsThatCredentialUsable(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	env := setupUserTest(t)

	bearer := env.bearerFor(t)
	actingTokenID := tokenIDOf(t, bearer)
	elevateAPITokenRow(t, env.store, actingTokenID, env.userID)

	// Everything the change must still take away: another command-line
	// credential, and another browser session.
	otherBearer := env.bearerFor(t)
	otherTokenID := tokenIDOf(t, otherBearer)
	otherSession, _, err := auth.CreateSession(ctx, env.store, userid.MustNew(env.userID), auth.DefaultSessionDuration)
	require.NoError(t, err)

	_, err = env.client.ChangePassword(ctx, bearerReq(&leapmuxv1.ChangePasswordRequest{
		NewPassword: "newpass123",
	}, bearer))
	require.NoError(t, err, "an elevated command-line credential may change its owner's password")

	// The password really changed, so nothing below passes because the
	// mutation quietly did nothing.
	_, _, _, err = auth.Login(ctx, env.store, "testuser", "newpass123", auth.DefaultSessionDuration)
	require.NoError(t, err)

	// THE regression, read from the store where no cache can mask it.
	actingRow, err := env.store.APITokens().GetByID(ctx, actingTokenID)
	require.NoError(t, err)
	assert.Nil(t, actingRow.RevokedAt, "the credential that asked must survive its own success")
	updatedUser, err := env.store.Users().GetByID(ctx, env.userID)
	require.NoError(t, err)
	assert.Equal(t, updatedUser.AuthGeneration, actingRow.AuthGeneration,
		"the kept credential must sit at the account's new epoch, or validation reads it as revoked")

	// And through the whole interceptor, which is what the user meets. The
	// eviction is what the revocation watcher's replay does; doing it here
	// keeps the assertion about the committed rows rather than about a cache
	// entry minted before the change.
	env.contexts.EvictByUserID(env.userID)
	_, err = env.client.GetTimeouts(ctx, bearerReq(&leapmuxv1.GetTimeoutsRequest{}, bearer))
	require.NoError(t, err, "the acting credential must still authenticate after the change it made")

	// The exclusion is NARROW: it spares one row and nothing else.
	otherRow, err := env.store.APITokens().GetByID(ctx, otherTokenID)
	require.NoError(t, err)
	assert.NotNil(t, otherRow.RevokedAt, "every other command-line credential is revoked")
	_, err = env.client.GetTimeouts(ctx, bearerReq(&leapmuxv1.GetTimeoutsRequest{}, otherBearer))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	_, err = auth.ValidateToken(ctx, env.store, otherSession)
	assert.Error(t, err, "a change from the command line signs every browser out")
	_, err = auth.ValidateToken(ctx, env.store, env.token)
	assert.Error(t, err, "the acting credential is not a session, so no session survives")
}

// TestChangePassword_FromAnElevatedSessionStillRevokesEveryCredential is the
// contrast that gives the case above its meaning. The exclusion follows the
// credential that ASKED, so a change made from a browser keeps the session
// and takes every command-line credential away.
func TestChangePassword_FromAnElevatedSessionStillRevokesEveryCredential(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	env := setupUserTest(t)

	bearer := env.bearerFor(t)
	tokenID := tokenIDOf(t, bearer)
	// Elevated, so the case cannot pass because the credential was weak.
	elevateAPITokenRow(t, env.store, tokenID, env.userID)

	env.elevate(t)
	_, err := env.client.ChangePassword(ctx, authedReq(&leapmuxv1.ChangePasswordRequest{
		NewPassword: "newpass123",
	}, env.token))
	require.NoError(t, err)

	row, err := env.store.APITokens().GetByID(ctx, tokenID)
	require.NoError(t, err)
	assert.NotNil(t, row.RevokedAt,
		"a change from a browser revokes every command-line credential, elevated or not")

	_, err = auth.ValidateToken(ctx, env.store, env.token)
	assert.NoError(t, err, "the acting session survives, as it always did")
}

// The OTHER caller of the same revocation. DeletePasskey and
// DeactivatePasskeyAuth reach it through commitPasskeyDeactivation on a
// passkey-only account, where they set the replacement password, so the
// acting credential must survive there for the same reason.
//
// The caller is a SESSION, and it can only be a session. Both verbs are
// ScopeNever: they manage the account's authenticators, which outlive any
// app's connection, so no consent screen offers them and the scope rung
// refuses an app credential before the elevation gate runs. The bearer twin of
// this test therefore cannot exist -- see assertOutOfScope in
// user_service_test.go, which pins that refusal.
func TestDeactivatePasskeyAuth_FromAnElevatedSessionKeepsThatSession(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	// A passwordless account (setupOAuthUserTest) that holds one passkey: the
	// account HAS a factor, so requireElevation is the rule, and
	// commitPasskeyDeactivation takes the passkey-only path that sets the
	// replacement password and revokes.
	env := setupOAuthUserTest(t)
	require.NoError(t, env.store.PasskeyCredentials().Create(ctx, store.CreatePasskeyCredentialParams{
		ID:           id.Generate(),
		UserID:       env.userID,
		CredentialID: []byte("cli-deactivate-credential"),
		PublicKey:    []byte("key"),
		FriendlyName: "Laptop",
	}))

	// An app credential on the account, elevated, so the revocation below
	// cannot pass because the credential was weak.
	bearer := env.bearerFor(t)
	tokenID := tokenIDOf(t, bearer)
	elevateAPITokenRow(t, env.store, tokenID, env.userID)

	elevateSession(t, env.store, env.token, env.userID)
	_, err := env.client.DeactivatePasskeyAuth(ctx, authedReq(&leapmuxv1.DeactivatePasskeyAuthRequest{
		NewPassword: "newpass123",
	}, env.token))
	require.NoError(t, err)

	_, err = auth.ValidateToken(ctx, env.store, env.token)
	assert.NoError(t, err, "the credential that asked must survive its own success")

	row, err := env.store.APITokens().GetByID(ctx, tokenID)
	require.NoError(t, err)
	assert.NotNil(t, row.RevokedAt,
		"every OTHER credential goes, because the account's factors just changed")

	updatedUser, err := env.store.Users().GetByID(ctx, env.userID)
	require.NoError(t, err)
	assert.True(t, updatedUser.FirstCredentialExempt, "the replacement password committed")
}
