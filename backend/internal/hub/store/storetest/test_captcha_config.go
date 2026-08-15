package storetest

import (
	"fmt"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/hub/captcha"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testCaptchaConfig(t *testing.T) {
	t.Run("absent selection is not found", func(t *testing.T) {
		st := s.NewStore(t)
		_, err := st.CaptchaConfig().GetSelected(ctx)
		assert.ErrorIs(t, err, store.ErrNotFound)
		_, err = st.CaptchaConfig().Get(ctx, captcha.ProviderAltcha)
		assert.ErrorIs(t, err, store.ErrNotFound)

		// Activating a provider that has no row selects nothing; the
		// caller's provisioning self-heals on the next read.
		require.NoError(t, st.CaptchaConfig().Activate(ctx, captcha.ProviderRecaptchaV3))
		_, err = st.CaptchaConfig().GetSelected(ctx)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("provision, upsert, activate, enable", func(t *testing.T) {
		st := s.NewStore(t)
		cs := st.CaptchaConfig()

		// Provisioning inserts unselected; activation is explicit.
		require.NoError(t, cs.InsertIfAbsent(ctx, store.InsertCaptchaConfigIfAbsentParams{
			Provider: captcha.ProviderAltcha,
			Secret:   []byte("altcha-secret"),
			Settings: `{"algorithm":"PBKDF2/SHA-256","cost":10000}`,
		}))
		_, err := cs.GetSelected(ctx)
		assert.ErrorIs(t, err, store.ErrNotFound, "a provisioned row starts unselected")
		require.NoError(t, cs.Activate(ctx, captcha.ProviderAltcha))

		got, err := cs.GetSelected(ctx)
		require.NoError(t, err)
		assert.Equal(t, captcha.ProviderAltcha, got.Provider)
		assert.True(t, got.Selected)
		assert.True(t, got.Enabled, "activation also enables: choosing a provider means running it")
		assert.Equal(t, `{"algorithm":"PBKDF2/SHA-256","cost":10000}`, got.Settings)
		assert.Equal(t, []byte("altcha-secret"), got.Secret)
		assert.False(t, got.UpdatedAt.IsZero())

		// Disable keeps the selection so a later enable restores the same
		// provider.
		require.NoError(t, cs.SetEnabled(ctx, false))
		got, err = cs.GetSelected(ctx)
		require.NoError(t, err)
		assert.False(t, got.Enabled)
		assert.True(t, got.Selected)
		require.NoError(t, cs.SetEnabled(ctx, true))
		got, err = cs.GetSelected(ctx)
		require.NoError(t, err)
		assert.True(t, got.Enabled)

		// Upserting a second provider and activating it moves the
		// selection in one step and re-enables.
		require.NoError(t, cs.Upsert(ctx, store.UpsertCaptchaConfigParams{
			Provider: captcha.ProviderTurnstile,
			Secret:   []byte("turnstile-secret"),
			Settings: `{"site_key":"1x00AA"}`,
		}))
		require.NoError(t, cs.Activate(ctx, captcha.ProviderTurnstile))

		got, err = cs.GetSelected(ctx)
		require.NoError(t, err)
		assert.Equal(t, captcha.ProviderTurnstile, got.Provider)
		assert.True(t, got.Enabled)
		old, err := cs.Get(ctx, captcha.ProviderAltcha)
		require.NoError(t, err)
		assert.False(t, old.Selected, "the previously selected provider is deselected")
		assert.False(t, old.Enabled)

		// SetEnabled touches only the selected row.
		require.NoError(t, cs.SetEnabled(ctx, false))
		old, err = cs.Get(ctx, captcha.ProviderAltcha)
		require.NoError(t, err)
		assert.False(t, old.Selected)

		rows, err := cs.List(ctx)
		require.NoError(t, err)
		assert.Len(t, rows, 2)
	})

	t.Run("conditional activation never overrides an existing selection", func(t *testing.T) {
		st := s.NewStore(t)
		cs := st.CaptchaConfig()
		require.NoError(t, cs.Upsert(ctx, store.UpsertCaptchaConfigParams{
			Provider: captcha.ProviderAltcha,
			Secret:   []byte("altcha-secret"),
			Settings: `{"algorithm":"PBKDF2/SHA-256","cost":10000}`,
		}))
		require.NoError(t, cs.Upsert(ctx, store.UpsertCaptchaConfigParams{
			Provider: captcha.ProviderTurnstile,
			Secret:   []byte("turnstile-secret"),
			Settings: `{"site_key":"1x00AA"}`,
		}))

		// Nothing selected yet: the self-heal activates altcha.
		require.NoError(t, cs.ActivateIfNoneSelected(ctx, captcha.ProviderAltcha))
		got, err := cs.GetSelected(ctx)
		require.NoError(t, err)
		assert.Equal(t, captcha.ProviderAltcha, got.Provider)

		// An admin CLI switch wins; a late self-heal read that started
		// before the switch must not flip the selection back.
		require.NoError(t, cs.Activate(ctx, captcha.ProviderTurnstile))
		require.NoError(t, cs.ActivateIfNoneSelected(ctx, captcha.ProviderAltcha))
		got, err = cs.GetSelected(ctx)
		require.NoError(t, err)
		assert.Equal(t, captcha.ProviderTurnstile, got.Provider, "the self-heal must never override an admin selection")
	})

	t.Run("settings update keeps the stored secret; upsert replaces it", func(t *testing.T) {
		st := s.NewStore(t)
		cs := st.CaptchaConfig()
		require.NoError(t, cs.Upsert(ctx, store.UpsertCaptchaConfigParams{
			Provider: captcha.ProviderRecaptchaV3,
			Secret:   []byte("provider-secret"),
			Settings: `{"site_key":"site"}`,
		}))

		// A settings-only write must never lose the key.
		require.NoError(t, cs.UpdateSettings(ctx, captcha.ProviderRecaptchaV3, `{"site_key":"site-2","min_score":0.7}`))
		got, err := cs.Get(ctx, captcha.ProviderRecaptchaV3)
		require.NoError(t, err)
		assert.Equal(t, `{"site_key":"site-2","min_score":0.7}`, got.Settings)
		assert.Equal(t, []byte("provider-secret"), got.Secret)

		// An upsert carries a secret by definition (operator rotation).
		require.NoError(t, cs.Upsert(ctx, store.UpsertCaptchaConfigParams{
			Provider: captcha.ProviderRecaptchaV3,
			Secret:   []byte("rotated"),
			Settings: `{"site_key":"site-2","min_score":0.7}`,
		}))
		got, err = cs.Get(ctx, captcha.ProviderRecaptchaV3)
		require.NoError(t, err)
		assert.Equal(t, []byte("rotated"), got.Secret)
	})

	t.Run("insert-if-absent is a no-op when the row exists", func(t *testing.T) {
		st := s.NewStore(t)
		cs := st.CaptchaConfig()
		require.NoError(t, cs.InsertIfAbsent(ctx, store.InsertCaptchaConfigIfAbsentParams{
			Provider: captcha.ProviderAltcha,
			Secret:   []byte("first-secret"),
			Settings: `{"algorithm":"SHA-256","cost":2000}`,
		}))
		// A racing provisioning attempt must not clobber the winner.
		require.NoError(t, cs.InsertIfAbsent(ctx, store.InsertCaptchaConfigIfAbsentParams{
			Provider: captcha.ProviderAltcha,
			Secret:   []byte("second-secret"),
			Settings: `{}`,
		}))

		got, err := cs.Get(ctx, captcha.ProviderAltcha)
		require.NoError(t, err)
		assert.Equal(t, []byte("first-secret"), got.Secret)
		assert.Equal(t, `{"algorithm":"SHA-256","cost":2000}`, got.Settings)
	})

	t.Run("delete removes one provider or all", func(t *testing.T) {
		st := s.NewStore(t)
		cs := st.CaptchaConfig()
		for _, p := range []captcha.Provider{captcha.ProviderAltcha, captcha.ProviderTurnstile} {
			require.NoError(t, cs.Upsert(ctx, store.UpsertCaptchaConfigParams{
				Provider: p,
				Secret:   []byte(fmt.Sprintf("secret-%d", int32(p))),
				Settings: `{}`,
			}))
		}
		require.NoError(t, cs.Activate(ctx, captcha.ProviderAltcha))

		require.NoError(t, cs.DeleteProvider(ctx, captcha.ProviderTurnstile))
		_, err := cs.Get(ctx, captcha.ProviderTurnstile)
		assert.ErrorIs(t, err, store.ErrNotFound)
		got, err := cs.Get(ctx, captcha.ProviderAltcha)
		require.NoError(t, err)
		assert.True(t, got.Selected, "deleting an unselected provider leaves the selection alone")

		require.NoError(t, cs.Delete(ctx))
		_, err = cs.GetSelected(ctx)
		assert.ErrorIs(t, err, store.ErrNotFound, "deleting the selected provider leaves nothing selected")
	})

	t.Run("consume salt is single-use until its expiry", func(t *testing.T) {
		st := s.NewStore(t)
		future := time.Now().Add(time.Hour)

		rows, err := st.CaptchaConfig().ConsumeAltchaSalt(ctx, store.ConsumeAltchaSaltParams{Salt: "salt-1", ExpiresAt: future})
		require.NoError(t, err)
		assert.EqualValues(t, 1, rows, "first use consumes the salt")

		rows, err = st.CaptchaConfig().ConsumeAltchaSalt(ctx, store.ConsumeAltchaSaltParams{Salt: "salt-1", ExpiresAt: future})
		require.NoError(t, err)
		assert.EqualValues(t, 0, rows, "a replay finds the row and consumes nothing")

		// A distinct salt is independent.
		rows, err = st.CaptchaConfig().ConsumeAltchaSalt(ctx, store.ConsumeAltchaSaltParams{Salt: "salt-2", ExpiresAt: future})
		require.NoError(t, err)
		assert.EqualValues(t, 1, rows)

		// The cleanup sweep drops only expired rows, freeing their salts.
		past := time.Now().Add(-time.Minute)
		_, err = st.CaptchaConfig().ConsumeAltchaSalt(ctx, store.ConsumeAltchaSaltParams{Salt: "salt-old", ExpiresAt: past})
		require.NoError(t, err)
		purged, err := st.Cleanup().DeleteExpiredAltchaSalts(ctx)
		require.NoError(t, err)
		assert.EqualValues(t, 1, purged, "only the expired salt row is dropped")

		// The expired salt is usable again after the purge: single-use is
		// bounded by expiry, which Verify enforces before this point.
		rows, err = st.CaptchaConfig().ConsumeAltchaSalt(ctx, store.ConsumeAltchaSaltParams{Salt: "salt-old", ExpiresAt: future})
		require.NoError(t, err)
		assert.EqualValues(t, 1, rows)

		// Live salts survive the sweep.
		rows, err = st.CaptchaConfig().ConsumeAltchaSalt(ctx, store.ConsumeAltchaSaltParams{Salt: "salt-1", ExpiresAt: future})
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
