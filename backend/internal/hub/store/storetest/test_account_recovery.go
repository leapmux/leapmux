package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store"
)

func hashFor(s string) string { return "hash-" + s }

// testAccountRecoveryStore pins the account-recovery user-store queries across
// every dialect: the token-keyed consume (attempts increment, force-expiry
// past the budget, ErrNotFound for a cleared or unknown token), the
// conditional mint's cooldown gate, the completion clear, and the manual
// clear.
func (s *Suite) testAccountRecoveryStore(t *testing.T) {
	ctx := context.Background()

	t.Run("consume by token charges attempts and force-expires", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "reset-consume-user")
		token := "tok-" + user.ID

		// Seed the expiry far in the future: the consume's liveness
		// predicate compares against the DATABASE clock, and a seed near
		// "now" can lose to container clock skew.
		minted, err := st.Users().SetPendingRecovery(ctx, store.SetPendingRecoveryParams{
			ID:                       user.ID,
			PendingRecoveryToken:     token,
			PendingRecoveryExpiresAt: time.Now().Add(24 * time.Hour).UTC(),
			CooldownCutoff:           store.UnconditionalMintCutoff(),
		})
		require.NoError(t, err)
		require.True(t, minted)

		for i := 1; i <= 6; i++ {
			charged, err := st.Users().ConsumeRecoveryAttemptByToken(ctx, token, time.Now().UTC(), 5)
			require.NoErrorf(t, err, "consume iteration %d failed", i)
			assert.Equal(t, user.ID, charged.ID)
			assert.Equal(t, int64(i), charged.PendingRecoveryAttempts)
		}

		// The 6th charge force-expired the row in SQL (attempts + 1 > 5
		// sets expires_at = now), so the next consume finds no live row.
		_, err = st.Users().ConsumeRecoveryAttemptByToken(ctx, token, time.Now().UTC(), 5)
		require.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("consume by token rejects cleared and unknown tokens", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "reset-miss-user")

		_, err := st.Users().ConsumeRecoveryAttemptByToken(ctx, "tok-unknown", time.Now().UTC(), 5)
		require.ErrorIs(t, err, store.ErrNotFound)

		minted, err := st.Users().SetPendingRecovery(ctx, store.SetPendingRecoveryParams{
			ID:                       user.ID,
			PendingRecoveryToken:     "tok-live",
			PendingRecoveryExpiresAt: time.Now().Add(time.Hour).UTC(),
			CooldownCutoff:           store.UnconditionalMintCutoff(),
		})
		require.NoError(t, err)
		require.True(t, minted)
		require.NoError(t, st.Users().ClearPendingRecovery(ctx, user.ID))
		_, err = st.Users().ConsumeRecoveryAttemptByToken(ctx, "tok-live", time.Now().UTC(), 5)
		require.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("attempt budget comes from the caller, not the SQL text", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "reset-budget-user")

		minted, err := st.Users().SetPendingRecovery(ctx, store.SetPendingRecoveryParams{
			ID:                       user.ID,
			PendingRecoveryToken:     "tok-budget",
			PendingRecoveryExpiresAt: time.Now().Add(24 * time.Hour).UTC(),
			CooldownCutoff:           store.UnconditionalMintCutoff(),
		})
		require.NoError(t, err)
		require.True(t, minted)

		// A budget of 2 force-expires at the third charge (2 + 1 > 2 in
		// the SQL CASE, whose threshold rides the bound max_attempts
		// argument instead of a literal), and the row's liveness WHERE
		// sees the forced expiry on the NEXT charge: under the default
		// budget of 5 neither would happen for another two charges.
		for i := 1; i <= 3; i++ {
			charged, err := st.Users().ConsumeRecoveryAttemptByToken(ctx, "tok-budget", time.Now().UTC(), 2)
			require.NoErrorf(t, err, "charge %d failed", i)
			assert.Equal(t, int64(i), charged.PendingRecoveryAttempts)
		}
		_, err = st.Users().ConsumeRecoveryAttemptByToken(ctx, "tok-budget", time.Now().UTC(), 2)
		require.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("conditional mint refuses a token inside the cooldown window", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "reset-mint-user")

		issued := time.Now().UTC()
		// First mint: no previous token exists, so any cutoff passes.
		minted, err := st.Users().SetPendingRecovery(ctx, store.SetPendingRecoveryParams{
			ID:                       user.ID,
			PendingRecoveryToken:     "tok-first",
			PendingRecoveryExpiresAt: issued.Add(time.Hour),
			PendingRecoveryIssuedAt:  &issued,
			CooldownCutoff:           issued,
		})
		require.NoError(t, err)
		require.True(t, minted)

		// The previous token was issued now; a cutoff before that issue
		// means the cooldown has not elapsed, so the mint must refuse and
		// the FIRST token must survive.
		minted, err = st.Users().SetPendingRecovery(ctx, store.SetPendingRecoveryParams{
			ID:                       user.ID,
			PendingRecoveryToken:     "tok-second",
			PendingRecoveryExpiresAt: issued.Add(2 * time.Hour),
			CooldownCutoff:           issued.Add(-time.Second),
		})
		require.NoError(t, err)
		require.False(t, minted)

		after, err := st.Users().GetByID(ctx, user.ID)
		require.NoError(t, err)
		assert.Equal(t, "tok-first", after.PendingRecoveryToken)

		// Once the previous issue is at or before the cutoff (the cooldown
		// elapsed), the mint lands and replaces the token.
		minted, err = st.Users().SetPendingRecovery(ctx, store.SetPendingRecoveryParams{
			ID:                       user.ID,
			PendingRecoveryToken:     "tok-third",
			PendingRecoveryExpiresAt: issued.Add(2 * time.Hour),
			CooldownCutoff:           issued.Add(time.Second),
		})
		require.NoError(t, err)
		require.True(t, minted)

		after, err = st.Users().GetByID(ctx, user.ID)
		require.NoError(t, err)
		assert.Equal(t, "tok-third", after.PendingRecoveryToken)
	})

	t.Run("complete rotates password and clears the pending row", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "reset-complete-user")
		token := "tok-complete"
		minted, err := st.Users().SetPendingRecovery(ctx, store.SetPendingRecoveryParams{
			ID:                       user.ID,
			PendingRecoveryToken:     token,
			PendingRecoveryExpiresAt: time.Now().Add(time.Hour).UTC(),
			CooldownCutoff:           store.UnconditionalMintCutoff(),
		})
		require.NoError(t, err)
		require.True(t, minted)

		revoked, err := st.Users().CompleteRecovery(ctx, store.CompleteRecoveryParams{
			ID:                   user.ID,
			PasswordHash:         hashFor("newpass"),
			PendingRecoveryToken: token,
		})
		require.NoError(t, err)
		assert.NotZero(t, revoked.AuthGeneration)

		after, err := st.Users().GetByID(ctx, user.ID)
		require.NoError(t, err)
		assert.Equal(t, hashFor("newpass"), after.PasswordHash)
		assert.True(t, after.PasswordSet)
		assert.Empty(t, after.PendingRecoveryToken)
		assert.Nil(t, after.PendingRecoveryExpiresAt)
		assert.Zero(t, after.PendingRecoveryAttempts)

		// A replayed completion matches no row.
		_, err = st.Users().CompleteRecovery(ctx, store.CompleteRecoveryParams{
			ID:                   user.ID,
			PasswordHash:         hashFor("replay"),
			PendingRecoveryToken: token,
		})
		require.ErrorIs(t, err, store.ErrNotFound)
	})
}
