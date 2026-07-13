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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store"
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
		ID: apiTokenID, UserID: env.userID, ClientType: "cli", ClientName: "test",
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
		ID: apiTok, UserID: env.userID, ClientType: "cli", ClientName: "test",
		SecretHash: []byte("hash"), Scope: "remote:*",
	}))
	env.seedDelegationToken(t)

	// Mimic the admin transaction sequence.
	require.NoError(t, env.st.RunInTransaction(context.Background(), func(tx store.Store) error {
		if _, err := tx.APITokens().RevokeByUser(context.Background(), env.userID); err != nil {
			return err
		}
		if _, err := tx.DelegationTokens().RevokeByUser(context.Background(), env.userID); err != nil {
			return err
		}
		_, err := tx.Users().RevokeUserTokens(context.Background(), env.userID)
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
		ID: apiTok, UserID: env.userID, ClientType: "cli", ClientName: "test",
		SecretHash: []byte("hash"), Scope: "remote:*",
	}))
	env.seedDelegationToken(t)

	require.NoError(t, env.st.RunInTransaction(context.Background(), func(tx store.Store) error {
		if _, err := tx.APITokens().RevokeByUser(context.Background(), env.userID); err != nil {
			return err
		}
		if _, err := tx.DelegationTokens().RevokeByUser(context.Background(), env.userID); err != nil {
			return err
		}
		_, err := tx.Users().RevokeUserTokens(context.Background(), env.userID)
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
		ID: apiTok, UserID: env.userID, ClientType: "cli", ClientName: "test",
		SecretHash: []byte("hash"), Scope: "remote:*",
	}))
	env.seedDelegationToken(t)

	require.NoError(t, env.st.RunInTransaction(context.Background(), func(tx store.Store) error {
		// sessions are hard-deleted in production; from the
		// watcher's point of view this is a no-op.
		if _, err := tx.APITokens().RevokeByUser(context.Background(), env.userID); err != nil {
			return err
		}
		if _, err := tx.DelegationTokens().RevokeByUser(context.Background(), env.userID); err != nil {
			return err
		}
		_, err := tx.Users().RevokeUserTokens(context.Background(), env.userID)
		return err
	}))

	require.NoError(t, env.watcher.RunOnce(context.Background()))
	assert.Empty(t, env.closer.bearerSnapshot())
	assert.Equal(t, []string{env.userID}, env.closer.userSnapshot())
}
