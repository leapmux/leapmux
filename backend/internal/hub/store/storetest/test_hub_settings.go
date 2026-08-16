package storetest

import (
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testHubSettings(t *testing.T) {
	t.Run("absent key is not found; GetAll omits it", func(t *testing.T) {
		st := s.NewStore(t)
		_, err := st.Settings().Get(ctx, "signup_enabled")
		assert.ErrorIs(t, err, store.ErrNotFound)
		rows, err := st.Settings().GetAll(ctx)
		require.NoError(t, err)
		assert.Empty(t, rows)
	})

	t.Run("upsert writes both halves; delete removes the row", func(t *testing.T) {
		st := s.NewStore(t)
		ss := st.Settings()

		value := `true`
		require.NoError(t, ss.Upsert(ctx, store.UpsertSettingParams{
			Key: "signup_enabled", Value: &value,
		}))
		got, err := ss.Get(ctx, "signup_enabled")
		require.NoError(t, err)
		require.NotNil(t, got.Value)
		assert.Equal(t, "true", *got.Value)
		assert.Nil(t, got.Secret)
		assert.False(t, got.UpdatedAt.IsZero())

		// A secret-bearing row carries both halves; the public half can
		// be nil only when the secret half exists.
		secretDoc := `{"password":"hunter2"}`
		require.NoError(t, ss.Upsert(ctx, store.UpsertSettingParams{
			Key: "smtp", Value: &value, Secret: []byte(secretDoc),
		}))
		got, err = ss.Get(ctx, "smtp")
		require.NoError(t, err)
		assert.Equal(t, []byte(secretDoc), got.Secret)

		// Upsert overwrites both halves in place.
		next := `{"host":"smtp.example.com","port":465}`
		require.NoError(t, ss.Upsert(ctx, store.UpsertSettingParams{
			Key: "smtp", Value: &next, Secret: []byte(`{"password":"rotated"}`),
		}))
		got, err = ss.Get(ctx, "smtp")
		require.NoError(t, err)
		assert.Equal(t, next, *got.Value)
		assert.Equal(t, []byte(`{"password":"rotated"}`), got.Secret)

		// GetAll returns every row ordered by key.
		rows, err := ss.GetAll(ctx)
		require.NoError(t, err)
		require.Len(t, rows, 2)
		assert.Equal(t, "signup_enabled", rows[0].Key)
		assert.Equal(t, "smtp", rows[1].Key)

		// Delete removes only the specified key.
		require.NoError(t, ss.Delete(ctx, "smtp"))
		_, err = ss.Get(ctx, "smtp")
		assert.ErrorIs(t, err, store.ErrNotFound)
		_, err = ss.Get(ctx, "signup_enabled")
		assert.NoError(t, err)
	})

	t.Run("InsertIfAbsent keeps the first writer's row", func(t *testing.T) {
		st := s.NewStore(t)
		ss := st.Settings()

		first := `{"host":"first.example.com"}`
		inserted, err := ss.InsertIfAbsent(ctx, store.UpsertSettingParams{
			Key: "smtp", Value: &first,
		})
		require.NoError(t, err)
		assert.True(t, inserted, "the first insert lands the row")

		second := `{"host":"second.example.com"}`
		inserted, err = ss.InsertIfAbsent(ctx, store.UpsertSettingParams{
			Key: "smtp", Value: &second,
		})
		require.NoError(t, err)
		assert.False(t, inserted, "a racing provisioner does not win the row")
		got, err := ss.Get(ctx, "smtp")
		require.NoError(t, err)
		assert.Equal(t, first, *got.Value, "the winner's value is the row that stays")
	})

	t.Run("GetForUpdate reads the row like Get", func(t *testing.T) {
		st := s.NewStore(t)
		ss := st.Settings()

		_, err := ss.GetForUpdate(ctx, "smtp")
		assert.ErrorIs(t, err, store.ErrNotFound, "no row means absent, not a fault")

		value := `{"host":"smtp.example.com"}`
		require.NoError(t, ss.Upsert(ctx, store.UpsertSettingParams{Key: "smtp", Value: &value}))
		got, err := ss.GetForUpdate(ctx, "smtp")
		require.NoError(t, err)
		assert.Equal(t, value, *got.Value)
	})

	t.Run("a row unknown to the catalog round-trips untouched", func(t *testing.T) {
		// The whole point of the KV shape: a row written for a key this
		// binary does not know (older or newer hub) must survive verbatim.
		st := s.NewStore(t)
		ss := st.Settings()
		value := `{"future":"field"}`
		require.NoError(t, ss.Upsert(ctx, store.UpsertSettingParams{
			Key: "some.future.key", Value: &value,
		}))
		got, err := ss.Get(ctx, "some.future.key")
		require.NoError(t, err)
		assert.Equal(t, value, *got.Value)
	})

	t.Run("consume salt is single-use until its expiry", func(t *testing.T) {
		st := s.NewStore(t)
		future := time.Now().Add(time.Hour)

		used, err := st.AltchaSalts().HasAltchaSalt(ctx, "salt-1")
		require.NoError(t, err)
		assert.False(t, used, "an unknown salt reports unused")

		rows, err := st.AltchaSalts().ConsumeAltchaSalt(ctx, store.ConsumeAltchaSaltParams{Salt: "salt-1", ExpiresAt: future})
		require.NoError(t, err)
		assert.EqualValues(t, 1, rows, "first use consumes the salt")

		used, err = st.AltchaSalts().HasAltchaSalt(ctx, "salt-1")
		require.NoError(t, err)
		assert.True(t, used, "a consumed salt reports used -- the read-only replay pre-check's answer")

		rows, err = st.AltchaSalts().ConsumeAltchaSalt(ctx, store.ConsumeAltchaSaltParams{Salt: "salt-1", ExpiresAt: future})
		require.NoError(t, err)
		assert.EqualValues(t, 0, rows, "a replay finds the row and consumes nothing")

		// A distinct salt is independent.
		rows, err = st.AltchaSalts().ConsumeAltchaSalt(ctx, store.ConsumeAltchaSaltParams{Salt: "salt-2", ExpiresAt: future})
		require.NoError(t, err)
		assert.EqualValues(t, 1, rows)

		// The cleanup sweep drops only expired rows, freeing their salts.
		past := time.Now().Add(-time.Minute)
		_, err = st.AltchaSalts().ConsumeAltchaSalt(ctx, store.ConsumeAltchaSaltParams{Salt: "salt-old", ExpiresAt: past})
		require.NoError(t, err)
		purged, err := st.Cleanup().DeleteExpiredAltchaSalts(ctx)
		require.NoError(t, err)
		assert.EqualValues(t, 1, purged, "only the expired salt row is dropped")

		// The expired salt is usable again after the purge: single-use is
		// limited by expiry, which Verify enforces before this point.
		rows, err = st.AltchaSalts().ConsumeAltchaSalt(ctx, store.ConsumeAltchaSaltParams{Salt: "salt-old", ExpiresAt: future})
		require.NoError(t, err)
		assert.EqualValues(t, 1, rows)

		// Live salts survive the sweep.
		rows, err = st.AltchaSalts().ConsumeAltchaSalt(ctx, store.ConsumeAltchaSaltParams{Salt: "salt-1", ExpiresAt: future})
		require.NoError(t, err)
		assert.EqualValues(t, 0, rows)
	})
}
