package storetest

import (
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testCaptchaConfig(t *testing.T) {
	t.Run("absent row is not found", func(t *testing.T) {
		st := s.NewStore(t)
		_, err := st.CaptchaConfig().Get(ctx)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("insert, get, update", func(t *testing.T) {
		st := s.NewStore(t)
		err := st.CaptchaConfig().Insert(ctx, store.InsertCaptchaConfigParams{
			Enabled:                true,
			Algorithm:              "PBKDF2/SHA-256",
			Cost:                   10000,
			MemoryCost:             0,
			Parallelism:            0,
			ChallengeExpirySeconds: 1200,
			Secret:                 []byte("encrypted-secret"),
		})
		require.NoError(t, err)

		got, err := st.CaptchaConfig().Get(ctx)
		require.NoError(t, err)
		assert.True(t, got.Enabled)
		assert.Equal(t, "PBKDF2/SHA-256", got.Algorithm)
		assert.Equal(t, int64(10000), got.Cost)
		assert.EqualValues(t, 0, got.MemoryCost)
		assert.EqualValues(t, 0, got.Parallelism)
		assert.EqualValues(t, 1200, got.ChallengeExpirySeconds)
		assert.Equal(t, []byte("encrypted-secret"), got.Secret)
		assert.False(t, got.UpdatedAt.IsZero())

		// The update carries no secret: provisioning is the secret's only
		// writer, so a configuration change must preserve it untouched.
		err = st.CaptchaConfig().Update(ctx, store.UpdateCaptchaConfigParams{
			Enabled:                false,
			Algorithm:              "SHA-256",
			Cost:                   50000,
			MemoryCost:             1,
			Parallelism:            2,
			ChallengeExpirySeconds: 600,
		})
		require.NoError(t, err)

		got, err = st.CaptchaConfig().Get(ctx)
		require.NoError(t, err)
		assert.False(t, got.Enabled)
		assert.Equal(t, "SHA-256", got.Algorithm)
		assert.Equal(t, int64(50000), got.Cost)
		assert.EqualValues(t, 1, got.MemoryCost)
		assert.EqualValues(t, 2, got.Parallelism)
		assert.EqualValues(t, 600, got.ChallengeExpirySeconds)
		assert.Equal(t, []byte("encrypted-secret"), got.Secret)
	})

	t.Run("insert is a no-op when the row exists", func(t *testing.T) {
		st := s.NewStore(t)
		first := store.InsertCaptchaConfigParams{
			Enabled:                true,
			Algorithm:              "PBKDF2/SHA-256",
			Cost:                   10000,
			ChallengeExpirySeconds: 1200,
			Secret:                 []byte("first-secret"),
		}
		require.NoError(t, st.CaptchaConfig().Insert(ctx, first))
		// A racing provisioning attempt must not clobber the winner.
		first.Secret = []byte("second-secret")
		require.NoError(t, st.CaptchaConfig().Insert(ctx, first))

		got, err := st.CaptchaConfig().Get(ctx)
		require.NoError(t, err)
		assert.Equal(t, []byte("first-secret"), got.Secret)
	})

	t.Run("delete restores the absent-row state", func(t *testing.T) {
		st := s.NewStore(t)
		require.NoError(t, st.CaptchaConfig().Insert(ctx, store.InsertCaptchaConfigParams{
			Enabled:                true,
			Algorithm:              "PBKDF2/SHA-256",
			Cost:                   10000,
			ChallengeExpirySeconds: 1200,
			Secret:                 []byte("secret"),
		}))
		require.NoError(t, st.CaptchaConfig().Delete(ctx))
		_, err := st.CaptchaConfig().Get(ctx)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("consume salt is single-use until its expiry", func(t *testing.T) {
		st := s.NewStore(t)
		future := time.Now().Add(time.Hour)

		rows, err := st.CaptchaConfig().ConsumeCaptchaSalt(ctx, store.ConsumeCaptchaSaltParams{Salt: "salt-1", ExpiresAt: future})
		require.NoError(t, err)
		assert.EqualValues(t, 1, rows, "first use consumes the salt")

		rows, err = st.CaptchaConfig().ConsumeCaptchaSalt(ctx, store.ConsumeCaptchaSaltParams{Salt: "salt-1", ExpiresAt: future})
		require.NoError(t, err)
		assert.EqualValues(t, 0, rows, "a replay finds the row and consumes nothing")

		// A distinct salt is independent.
		rows, err = st.CaptchaConfig().ConsumeCaptchaSalt(ctx, store.ConsumeCaptchaSaltParams{Salt: "salt-2", ExpiresAt: future})
		require.NoError(t, err)
		assert.EqualValues(t, 1, rows)

		// The cleanup sweep drops only expired rows, freeing their salts.
		past := time.Now().Add(-time.Minute)
		_, err = st.CaptchaConfig().ConsumeCaptchaSalt(ctx, store.ConsumeCaptchaSaltParams{Salt: "salt-old", ExpiresAt: past})
		require.NoError(t, err)
		purged, err := st.Cleanup().DeleteExpiredCaptchaSalts(ctx)
		require.NoError(t, err)
		assert.EqualValues(t, 1, purged, "only the expired salt row is dropped")

		// The expired salt is usable again after the purge: single-use is
		// bounded by expiry, which Verify enforces before this point.
		rows, err = st.CaptchaConfig().ConsumeCaptchaSalt(ctx, store.ConsumeCaptchaSaltParams{Salt: "salt-old", ExpiresAt: future})
		require.NoError(t, err)
		assert.EqualValues(t, 1, rows)

		// Live salts survive the sweep.
		rows, err = st.CaptchaConfig().ConsumeCaptchaSalt(ctx, store.ConsumeCaptchaSaltParams{Salt: "salt-1", ExpiresAt: future})
		require.NoError(t, err)
		assert.EqualValues(t, 0, rows)
	})
}

func (s *Suite) testRateLimitConfig(t *testing.T) {
	t.Run("absent row is not found", func(t *testing.T) {
		st := s.NewStore(t)
		_, err := st.RateLimitConfig().Get(ctx, "change-password")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("upsert, get, list", func(t *testing.T) {
		st := s.NewStore(t)
		require.NoError(t, st.RateLimitConfig().Upsert(ctx, store.UpsertRateLimitConfigParams{
			Operation:     "change-password",
			Enabled:       true,
			MaxAttempts:   5,
			WindowSeconds: 900,
		}))

		got, err := st.RateLimitConfig().Get(ctx, "change-password")
		require.NoError(t, err)
		assert.True(t, got.Enabled)
		assert.EqualValues(t, 5, got.MaxAttempts)
		assert.EqualValues(t, 900, got.WindowSeconds)
		assert.False(t, got.UpdatedAt.IsZero())

		// Upsert overwrites in place.
		require.NoError(t, st.RateLimitConfig().Upsert(ctx, store.UpsertRateLimitConfigParams{
			Operation:     "change-password",
			Enabled:       false,
			MaxAttempts:   10,
			WindowSeconds: 60,
		}))
		got, err = st.RateLimitConfig().Get(ctx, "change-password")
		require.NoError(t, err)
		assert.False(t, got.Enabled)
		assert.EqualValues(t, 10, got.MaxAttempts)
		assert.EqualValues(t, 60, got.WindowSeconds)

		// A second operation coexists.
		require.NoError(t, st.RateLimitConfig().Upsert(ctx, store.UpsertRateLimitConfigParams{
			Operation:     "another_op",
			Enabled:       true,
			MaxAttempts:   3,
			WindowSeconds: 120,
		}))
		rows, err := st.RateLimitConfig().List(ctx)
		require.NoError(t, err)
		assert.Len(t, rows, 2)
	})

	t.Run("delete removes only the named operation", func(t *testing.T) {
		st := s.NewStore(t)
		for _, op := range []string{"change-password", "another_op"} {
			require.NoError(t, st.RateLimitConfig().Upsert(ctx, store.UpsertRateLimitConfigParams{
				Operation:     op,
				Enabled:       true,
				MaxAttempts:   5,
				WindowSeconds: 900,
			}))
		}
		require.NoError(t, st.RateLimitConfig().Delete(ctx, "change-password"))

		_, err := st.RateLimitConfig().Get(ctx, "change-password")
		assert.ErrorIs(t, err, store.ErrNotFound)
		_, err = st.RateLimitConfig().Get(ctx, "another_op")
		assert.NoError(t, err)
	})
}
