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
			ID:                         user.ID,
			PendingRecoveryToken:       token,
			PendingRecoveryExpiresAt:   time.Now().Add(24 * time.Hour).UTC(),
			PendingRecoveryUnblockedAt: time.Now().UTC().Add(time.Minute),
			Now:                        time.Now().UTC(),
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
		user := SeedUser(t, st, "reset-consume-user")

		_, err := st.Users().ConsumeRecoveryAttemptByToken(ctx, "tok-unknown", time.Now().UTC(), 5)
		require.ErrorIs(t, err, store.ErrNotFound)

		base := time.Now().UTC()
		minted, err := st.Users().SetPendingRecovery(ctx, store.SetPendingRecoveryParams{
			ID:                         user.ID,
			PendingRecoveryToken:       "tok-live",
			PendingRecoveryExpiresAt:   base.Add(time.Hour),
			PendingRecoveryUnblockedAt: base.Add(time.Minute),
			Now:                        base,
		})
		require.NoError(t, err)
		require.True(t, minted)
		// A failed-send clear at `base` with a 10s failure window: the
		// deadline it arms is 10s ahead, not a full cooldown.
		require.NoError(t, st.Users().ClearPendingRecovery(ctx, store.ClearPendingRecoveryParams{
			ID:          user.ID,
			UnblockedAt: base.Add(10 * time.Second),
		}))
		_, err = st.Users().ConsumeRecoveryAttemptByToken(ctx, "tok-live", base, 5)
		require.ErrorIs(t, err, store.ErrNotFound)

		// Inside the failure window the retry is refused: a retry that no
		// window holds back is a mint-send-clear loop that costs the relay
		// one SMTP transaction per request.
		insideWindow := base.Add(5 * time.Second)
		minted, err = st.Users().SetPendingRecovery(ctx, store.SetPendingRecoveryParams{
			ID:                         user.ID,
			PendingRecoveryToken:       "tok-retry",
			PendingRecoveryExpiresAt:   insideWindow.Add(time.Hour),
			PendingRecoveryUnblockedAt: insideWindow.Add(time.Minute),
			Now:                        insideWindow,
		})
		require.NoError(t, err)
		assert.False(t, minted, "the failure window must refuse the retry a failed send invites")

		// Once the window elapses the retry lands -- the person whose relay
		// hiccuped waits seconds, not a minute.
		pastWindow := base.Add(11 * time.Second)
		minted, err = st.Users().SetPendingRecovery(ctx, store.SetPendingRecoveryParams{
			ID:                         user.ID,
			PendingRecoveryToken:       "tok-retry2",
			PendingRecoveryExpiresAt:   pastWindow.Add(time.Hour),
			PendingRecoveryUnblockedAt: pastWindow.Add(time.Minute),
			Now:                        pastWindow,
		})
		require.NoError(t, err)
		assert.True(t, minted, "a cleared recovery row must not block the retry past the failure window")
	})

	t.Run("attempt budget comes from the caller, not the SQL text", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "reset-budget-user")

		minted, err := st.Users().SetPendingRecovery(ctx, store.SetPendingRecoveryParams{
			ID:                         user.ID,
			PendingRecoveryToken:       "tok-budget",
			PendingRecoveryExpiresAt:   time.Now().Add(24 * time.Hour).UTC(),
			PendingRecoveryUnblockedAt: time.Now().UTC().Add(time.Minute),
			Now:                        time.Now().UTC(),
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
		// First mint: a fresh row holds no blockade, so it lands.
		minted, err := st.Users().SetPendingRecovery(ctx, store.SetPendingRecoveryParams{
			ID:                         user.ID,
			PendingRecoveryToken:       "tok-first",
			PendingRecoveryExpiresAt:   issued.Add(time.Hour),
			PendingRecoveryUnblockedAt: issued.Add(time.Minute),
			Now:                        issued,
		})
		require.NoError(t, err)
		require.True(t, minted)

		// The first mint armed unblocked_at = issued+60s; a mint whose Now
		// is still inside that window must refuse, and the FIRST token
		// must survive.
		minted, err = st.Users().SetPendingRecovery(ctx, store.SetPendingRecoveryParams{
			ID:                         user.ID,
			PendingRecoveryToken:       "tok-second",
			PendingRecoveryExpiresAt:   issued.Add(2 * time.Hour),
			PendingRecoveryUnblockedAt: issued.Add(2 * time.Minute),
			Now:                        issued,
		})
		require.NoError(t, err)
		assert.False(t, minted)

		after, err := st.Users().GetByID(ctx, user.ID)
		require.NoError(t, err)
		assert.Equal(t, "tok-first", after.PendingRecoveryToken)

		// One cooldown after the mint, the blockade elapsed and the mint
		// lands and replaces the token.
		later := issued.Add(time.Minute)
		minted, err = st.Users().SetPendingRecovery(ctx, store.SetPendingRecoveryParams{
			ID:                         user.ID,
			PendingRecoveryToken:       "tok-third",
			PendingRecoveryExpiresAt:   later.Add(2 * time.Hour),
			PendingRecoveryUnblockedAt: later.Add(time.Minute),
			Now:                        later,
		})
		require.NoError(t, err)
		assert.True(t, minted)

		after, err = st.Users().GetByID(ctx, user.ID)
		require.NoError(t, err)
		assert.Equal(t, "tok-third", after.PendingRecoveryToken)
	})

	t.Run("complete rotates password and clears the pending row", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "reset-complete-user")
		token := "tok-complete"
		minted, err := st.Users().SetPendingRecovery(ctx, store.SetPendingRecoveryParams{
			ID:                         user.ID,
			PendingRecoveryToken:       token,
			PendingRecoveryExpiresAt:   time.Now().Add(time.Hour).UTC(),
			PendingRecoveryUnblockedAt: time.Now().UTC().Add(time.Minute),
			Now:                        time.Now().UTC(),
		})
		require.NoError(t, err)
		require.True(t, minted)

		revoked, err := st.Users().CompleteRecovery(ctx, store.CompleteRecoveryParams{
			ID:                   user.ID,
			PasswordHash:         hashFor("newpass"),
			PendingRecoveryToken: token,
		})
		require.NoError(t, err)
		// Exactly one bump: the post-commit lifecycle eviction targets this
		// generation, so a second bump inside the completion would evict at
		// an epoch this transaction never produced.
		assert.Equal(t, user.AuthGeneration+1, revoked.AuthGeneration)

		after, err := st.Users().GetByID(ctx, user.ID)
		require.NoError(t, err)
		assert.Equal(t, hashFor("newpass"), after.PasswordHash)
		assert.True(t, after.PasswordSet)
		assert.Empty(t, after.PendingRecoveryToken)
		assert.Nil(t, after.PendingRecoveryExpiresAt)
		assert.Nil(t, after.PendingRecoveryUnblockedAt,
			"a completed row carries no leftover recovery state to report or gate on")
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
