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

// farFutureCutoff lets a seeded mint always land: the conditional write
// compares the previous expiry against this cutoff.
func farFutureCutoff() time.Time { return time.Now().Add(48 * time.Hour).UTC() }

// testPasswordResetStore pins the password-reset user-store queries across
// every dialect: the token-keyed consume (attempts increment, force-expiry
// past the budget, ErrNotFound for a cleared or unknown token), the
// conditional mint's cooldown gate, the completion clear, and the manual
// clear.
func (s *Suite) testPasswordResetStore(t *testing.T) {
	ctx := context.Background()

	t.Run("consume by token charges attempts and force-expires", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "reset-consume-user")
		token := "tok-" + user.ID

		// Seed the expiry far in the future: the consume's liveness
		// predicate compares against the DATABASE clock, and a seed near
		// "now" can lose to container clock skew.
		minted, err := st.Users().SetPendingPasswordReset(ctx, store.SetPendingPasswordResetParams{
			ID:                            user.ID,
			PendingPasswordResetToken:     token,
			PendingPasswordResetExpiresAt: time.Now().Add(24 * time.Hour).UTC(),
			CooldownCutoff:                farFutureCutoff(),
		})
		require.NoError(t, err)
		require.True(t, minted)

		for i := 1; i <= 6; i++ {
			charged, err := st.Users().ConsumePasswordResetAttemptByToken(ctx, token, time.Now().UTC(), 5)
			require.NoErrorf(t, err, "consume iteration %d failed", i)
			assert.Equal(t, user.ID, charged.ID)
			assert.Equal(t, int64(i), charged.PendingPasswordResetAttempts)
		}

		// The 6th charge force-expired the row in SQL (attempts + 1 > 5
		// sets expires_at = now), so the next consume finds no live row.
		_, err = st.Users().ConsumePasswordResetAttemptByToken(ctx, token, time.Now().UTC(), 5)
		require.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("consume by token rejects cleared and unknown tokens", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "reset-miss-user")

		_, err := st.Users().ConsumePasswordResetAttemptByToken(ctx, "tok-unknown", time.Now().UTC(), 5)
		require.ErrorIs(t, err, store.ErrNotFound)

		minted, err := st.Users().SetPendingPasswordReset(ctx, store.SetPendingPasswordResetParams{
			ID:                            user.ID,
			PendingPasswordResetToken:     "tok-live",
			PendingPasswordResetExpiresAt: time.Now().Add(time.Hour).UTC(),
			CooldownCutoff:                farFutureCutoff(),
		})
		require.NoError(t, err)
		require.True(t, minted)
		require.NoError(t, st.Users().ClearPendingPasswordReset(ctx, user.ID))
		_, err = st.Users().ConsumePasswordResetAttemptByToken(ctx, "tok-live", time.Now().UTC(), 5)
		require.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("attempt budget comes from the caller, not the SQL text", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "reset-budget-user")

		minted, err := st.Users().SetPendingPasswordReset(ctx, store.SetPendingPasswordResetParams{
			ID:                            user.ID,
			PendingPasswordResetToken:     "tok-budget",
			PendingPasswordResetExpiresAt: time.Now().Add(24 * time.Hour).UTC(),
			CooldownCutoff:                farFutureCutoff(),
		})
		require.NoError(t, err)
		require.True(t, minted)

		// A budget of 2 force-expires at the third charge (2 + 1 > 2 in
		// the SQL CASE, whose threshold rides the bound max_attempts
		// argument instead of a literal), and the row's liveness WHERE
		// sees the forced expiry on the NEXT charge: under the default
		// budget of 5 neither would happen for another two charges.
		for i := 1; i <= 3; i++ {
			charged, err := st.Users().ConsumePasswordResetAttemptByToken(ctx, "tok-budget", time.Now().UTC(), 2)
			require.NoErrorf(t, err, "charge %d failed", i)
			assert.Equal(t, int64(i), charged.PendingPasswordResetAttempts)
		}
		_, err = st.Users().ConsumePasswordResetAttemptByToken(ctx, "tok-budget", time.Now().UTC(), 2)
		require.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("conditional mint refuses a token inside the cooldown window", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "reset-mint-user")

		// First mint: no previous token exists, so any cutoff passes.
		minted, err := st.Users().SetPendingPasswordReset(ctx, store.SetPendingPasswordResetParams{
			ID:                            user.ID,
			PendingPasswordResetToken:     "tok-first",
			PendingPasswordResetExpiresAt: time.Now().Add(time.Hour).UTC(),
			CooldownCutoff:                time.Now().UTC(),
		})
		require.NoError(t, err)
		require.True(t, minted)

		// The previous token expires in one hour; a cutoff before that
		// expiry means the cooldown has not elapsed, so the mint must
		// refuse and the FIRST token must survive.
		minted, err = st.Users().SetPendingPasswordReset(ctx, store.SetPendingPasswordResetParams{
			ID:                            user.ID,
			PendingPasswordResetToken:     "tok-second",
			PendingPasswordResetExpiresAt: time.Now().Add(2 * time.Hour).UTC(),
			CooldownCutoff:                time.Now().UTC(),
		})
		require.NoError(t, err)
		require.False(t, minted)

		after, err := st.Users().GetByID(ctx, user.ID)
		require.NoError(t, err)
		assert.Equal(t, "tok-first", after.PendingPasswordResetToken)

		// Once the previous expiry is at or before the cutoff (the cooldown
		// elapsed), the mint lands and replaces the token.
		minted, err = st.Users().SetPendingPasswordReset(ctx, store.SetPendingPasswordResetParams{
			ID:                            user.ID,
			PendingPasswordResetToken:     "tok-third",
			PendingPasswordResetExpiresAt: time.Now().Add(2 * time.Hour).UTC(),
			CooldownCutoff:                time.Now().Add(time.Hour).UTC(),
		})
		require.NoError(t, err)
		require.True(t, minted)

		after, err = st.Users().GetByID(ctx, user.ID)
		require.NoError(t, err)
		assert.Equal(t, "tok-third", after.PendingPasswordResetToken)
	})

	t.Run("complete rotates password and clears the pending row", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "reset-complete-user")
		token := "tok-complete"
		minted, err := st.Users().SetPendingPasswordReset(ctx, store.SetPendingPasswordResetParams{
			ID:                            user.ID,
			PendingPasswordResetToken:     token,
			PendingPasswordResetExpiresAt: time.Now().Add(time.Hour).UTC(),
			CooldownCutoff:                farFutureCutoff(),
		})
		require.NoError(t, err)
		require.True(t, minted)

		revoked, err := st.Users().CompletePasswordReset(ctx, store.CompletePasswordResetParams{
			ID:                        user.ID,
			PasswordHash:              hashFor("newpass"),
			PendingPasswordResetToken: token,
		})
		require.NoError(t, err)
		assert.NotZero(t, revoked.AuthGeneration)

		after, err := st.Users().GetByID(ctx, user.ID)
		require.NoError(t, err)
		assert.Equal(t, hashFor("newpass"), after.PasswordHash)
		assert.True(t, after.PasswordSet)
		assert.Empty(t, after.PendingPasswordResetToken)
		assert.Nil(t, after.PendingPasswordResetExpiresAt)
		assert.Zero(t, after.PendingPasswordResetAttempts)

		// A replayed completion matches no row.
		_, err = st.Users().CompletePasswordReset(ctx, store.CompletePasswordResetParams{
			ID:                        user.ID,
			PasswordHash:              hashFor("replay"),
			PendingPasswordResetToken: token,
		})
		require.ErrorIs(t, err, store.ErrNotFound)
	})
}
