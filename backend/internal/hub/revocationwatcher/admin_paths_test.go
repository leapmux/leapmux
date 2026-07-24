package revocationwatcher_test

// Integration tests that pin the cross-process contract: each admin
// CLI revocation path records durable revocation events, and the watcher
// in turn drives EvictBearer / RevokeUserAuthContextAtGeneration +
// CloseChannelsByBearer / CloseChannelsByUserRevocation. The admin commands
// themselves run in a separate process from the hub in production;
// these tests exercise the database side they share.

import (
	"context"
	"testing"

	"github.com/leapmux/leapmux/internal/util/userid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
)

// TestAdminPath_APITokenRevoke_ClosesBearerChannels mirrors what
// `leapmux admin api-token revoke --id <id>` does (Revoke a single
// row). The watcher must pick that up and close any bearer-keyed
// channel that token authenticated.
func TestAdminPath_APITokenRevoke_ClosesBearerChannels(t *testing.T) {
	env := setup(t)

	apiTokenID := id.Generate()
	require.NoError(t, env.st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID: apiTokenID, UserID: userid.MustNew(env.userID), ClientType: "cli", ClientName: "test",
		SecretHash: []byte("hash"), Scope: "remote:*",
	}))

	// Admin CLI revoke shape: the row gets revoked_at; cache /
	// channel teardown is the watcher's job.
	_, err := env.st.APITokens().Revoke(context.Background(), apiTokenID)
	require.NoError(t, err)

	require.NoError(t, env.watcher.RunOnce(context.Background()))
	assert.Equal(t, []string{apiTokenID}, env.closer.bearerSnapshot())
}

// TestAdminPath_UserDelete_TearsDownEverything emulates the
// `leapmux admin user delete` admin transaction: every credential
// the user had — sessions (separate concern), api_tokens,
// delegation_tokens — gets revoked, and `users.tokens_revoked_at`
// is bumped. The generation-bearing user event closes every channel;
// individual bearer events would be redundant for this atomic path.
func TestAdminPath_UserDelete_TearsDownEverything(t *testing.T) {
	env := setup(t)

	apiTok := id.Generate()
	require.NoError(t, env.st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID: apiTok, UserID: userid.MustNew(env.userID), ClientType: "cli", ClientName: "test",
		SecretHash: []byte("hash"), Scope: "remote:*",
	}))
	env.seedDelegationToken(t)

	// Mimic the admin transaction sequence.
	require.NoError(t, env.st.RunInTransaction(context.Background(), func(tx store.Store) error {
		if _, err := tx.APITokens().RevokeByUser(context.Background(), userid.MustNew(env.userID)); err != nil {
			return err
		}
		if _, err := tx.DelegationTokens().RevokeByUser(context.Background(), userid.MustNew(env.userID)); err != nil {
			return err
		}
		_, err := tx.Users().RevokeUserTokens(context.Background(), userid.MustNew(env.userID))
		return err
	}))

	require.NoError(t, env.watcher.RunOnce(context.Background()))

	assert.Empty(t, env.closer.bearerSnapshot())
	assert.Equal(t, []string{env.userID}, env.closer.userSnapshot())
}

// TestAdminPath_ResetPassword_RevokesTokensAndChannels mirrors
// `leapmux admin user reset-password`. The shape is the same as
// user delete except the user row stays and sessions are deleted
// instead of soft-revoked; from the watcher's perspective only
// the (api_tokens revoke + delegation revoke + tokens_revoked_at
// bump) tuple matters.
func TestAdminPath_ResetPassword_RevokesTokensAndChannels(t *testing.T) {
	env := setup(t)
	apiTok := id.Generate()
	require.NoError(t, env.st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID: apiTok, UserID: userid.MustNew(env.userID), ClientType: "cli", ClientName: "test",
		SecretHash: []byte("hash"), Scope: "remote:*",
	}))
	env.seedDelegationToken(t)

	require.NoError(t, env.st.RunInTransaction(context.Background(), func(tx store.Store) error {
		if _, err := tx.APITokens().RevokeByUser(context.Background(), userid.MustNew(env.userID)); err != nil {
			return err
		}
		if _, err := tx.DelegationTokens().RevokeByUser(context.Background(), userid.MustNew(env.userID)); err != nil {
			return err
		}
		_, err := tx.Users().RevokeUserTokens(context.Background(), userid.MustNew(env.userID))
		return err
	}))

	require.NoError(t, env.watcher.RunOnce(context.Background()))
	assert.Empty(t, env.closer.bearerSnapshot())
	assert.Equal(t, []string{env.userID}, env.closer.userSnapshot())
}

// TestAdminPath_SessionRevokeUser_TearsDownAllChannels mirrors
// `leapmux admin session revoke-user`. Sessions are deleted (the
// watcher doesn't see those — they're hard-deleted, not flagged),
// so the user-wide tokens_revoked_at bump carries the signal for cookie
// channels; api / delegation tokens are revoked in the same
// transaction.
func TestAdminPath_SessionRevokeUser_TearsDownAllChannels(t *testing.T) {
	env := setup(t)
	apiTok := id.Generate()
	require.NoError(t, env.st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID: apiTok, UserID: userid.MustNew(env.userID), ClientType: "cli", ClientName: "test",
		SecretHash: []byte("hash"), Scope: "remote:*",
	}))
	env.seedDelegationToken(t)

	require.NoError(t, env.st.RunInTransaction(context.Background(), func(tx store.Store) error {
		// sessions are hard-deleted in production; from the
		// watcher's point of view this is a no-op.
		if _, err := tx.APITokens().RevokeByUser(context.Background(), userid.MustNew(env.userID)); err != nil {
			return err
		}
		if _, err := tx.DelegationTokens().RevokeByUser(context.Background(), userid.MustNew(env.userID)); err != nil {
			return err
		}
		_, err := tx.Users().RevokeUserTokens(context.Background(), userid.MustNew(env.userID))
		return err
	}))

	require.NoError(t, env.watcher.RunOnce(context.Background()))
	assert.Empty(t, env.closer.bearerSnapshot())
	assert.Equal(t, []string{env.userID}, env.closer.userSnapshot())
}

// TestAdminPath_RevokeAdmin_FencesUserCredentials mirrors
// `leapmux admin user revoke-admin`. A grant stays on the soft user_info
// path (no channel teardown); a demotion emits a generation-bearing
// user_tokens event that CloseChannelsByUserRevocation fences.
func TestAdminPath_RevokeAdmin_FencesUserCredentials(t *testing.T) {
	env := setup(t)
	ctx := context.Background()
	// Use a non-admin so UpdateAdmin(true) is a real grant, not a no-op on
	// the already-admin fixture.
	targetID := hubtestutil.CreateTestUser(t, env.st, "gate-user", "password-gate-user-123")

	require.NoError(t, env.st.Users().UpdateAdmin(ctx, store.UpdateUserAdminParams{
		ID: targetID, IsAdmin: true,
	}))
	require.NoError(t, env.watcher.RunOnce(ctx))
	assert.Empty(t, env.closer.userSnapshot(), "grant must not fence live streams")

	require.NoError(t, env.st.Users().UpdateAdmin(ctx, store.UpdateUserAdminParams{
		ID: targetID, IsAdmin: false,
	}))
	require.NoError(t, env.watcher.RunOnce(ctx))
	assert.Equal(t, []string{targetID}, env.closer.userSnapshot())
}

// TestAdminPath_UnverifyEmail_FencesUserCredentials mirrors an admin
// `user update --email-verified=false`. Verify (grant) stays soft; un-verify
// fences via the same user_tokens watcher path as revoke-admin.
func TestAdminPath_UnverifyEmail_FencesUserCredentials(t *testing.T) {
	env := setup(t)
	ctx := context.Background()

	require.NoError(t, env.st.Users().UpdateEmailVerified(ctx, store.UpdateUserEmailVerifiedParams{
		ID: env.userID, EmailVerified: true,
	}))
	require.NoError(t, env.watcher.RunOnce(ctx))
	assert.Empty(t, env.closer.userSnapshot(), "verify grant must not fence live streams")

	require.NoError(t, env.st.Users().UpdateEmailVerified(ctx, store.UpdateUserEmailVerifiedParams{
		ID: env.userID, EmailVerified: false,
	}))
	require.NoError(t, env.watcher.RunOnce(ctx))
	assert.Equal(t, []string{env.userID}, env.closer.userSnapshot())
}
