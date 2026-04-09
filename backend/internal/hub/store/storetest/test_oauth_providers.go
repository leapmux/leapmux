package storetest

import (
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testOAuthProviders(t *testing.T) {
	seedProvider := func(t *testing.T, st store.Store, name string, enabled bool) string {
		t.Helper()
		provID := id.Generate()
		err := st.OAuthProviders().Create(ctx, store.CreateOAuthProviderParams{
			ID:           provID,
			ProviderType: "oidc",
			Name:         name,
			IssuerURL:    "https://issuer.example.com",
			ClientID:     "client-" + name,
			ClientSecret: []byte("secret-" + name),
			Scopes:       "openid profile email",
			TrustEmail:   true,
			Enabled:      enabled,
		})
		require.NoError(t, err)
		return provID
	}

	t.Run("create and get by id", func(t *testing.T) {
		st := s.NewStore(t)
		provID := seedProvider(t, st, "test-prov", true)

		prov, err := st.OAuthProviders().GetByID(ctx, provID)
		require.NoError(t, err)
		assert.Equal(t, provID, prov.ID)
		assert.Equal(t, "oidc", prov.ProviderType)
		assert.Equal(t, "test-prov", prov.Name)
		assert.Equal(t, "https://issuer.example.com", prov.IssuerURL)
		assert.Equal(t, "client-test-prov", prov.ClientID)
		assert.Equal(t, []byte("secret-test-prov"), prov.ClientSecret)
		assert.Equal(t, "openid profile email", prov.Scopes)
		assert.True(t, prov.TrustEmail)
		assert.True(t, prov.Enabled)
		assert.False(t, prov.CreatedAt.IsZero())
	})

	t.Run("get by id not found", func(t *testing.T) {
		st := s.NewStore(t)
		_, err := st.OAuthProviders().GetByID(ctx, "nonexistent")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("list enabled", func(t *testing.T) {
		st := s.NewStore(t)
		seedProvider(t, st, "enabled-prov", true)
		seedProvider(t, st, "disabled-prov", false)

		enabled, err := st.OAuthProviders().ListEnabled(ctx)
		require.NoError(t, err)
		assert.Len(t, enabled, 1)
		assert.Equal(t, "enabled-prov", enabled[0].Name)
	})

	t.Run("list all", func(t *testing.T) {
		st := s.NewStore(t)
		seedProvider(t, st, "prov-a", true)
		seedProvider(t, st, "prov-b", false)

		all, err := st.OAuthProviders().ListAll(ctx)
		require.NoError(t, err)
		assert.Len(t, all, 2)
	})

	t.Run("list all with secrets", func(t *testing.T) {
		st := s.NewStore(t)
		seedProvider(t, st, "secret-prov", true)

		all, err := st.OAuthProviders().ListAllWithSecrets(ctx)
		require.NoError(t, err)
		require.Len(t, all, 1)
		assert.NotEmpty(t, all[0].ClientSecret)
	})

	t.Run("update enabled", func(t *testing.T) {
		st := s.NewStore(t)
		provID := seedProvider(t, st, "toggle-prov", true)

		err := st.OAuthProviders().UpdateEnabled(ctx, store.UpdateOAuthProviderEnabledParams{
			ID:      provID,
			Enabled: false,
		})
		require.NoError(t, err)

		prov, err := st.OAuthProviders().GetByID(ctx, provID)
		require.NoError(t, err)
		assert.False(t, prov.Enabled)
	})

	t.Run("delete", func(t *testing.T) {
		st := s.NewStore(t)
		provID := seedProvider(t, st, "del-prov", true)

		err := st.OAuthProviders().Delete(ctx, provID)
		require.NoError(t, err)

		_, err = st.OAuthProviders().GetByID(ctx, provID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("list enabled empty", func(t *testing.T) {
		st := s.NewStore(t)

		enabled, err := st.OAuthProviders().ListEnabled(ctx)
		require.NoError(t, err)
		require.NotNil(t, enabled)
		assert.Empty(t, enabled)
	})

	t.Run("list all empty", func(t *testing.T) {
		st := s.NewStore(t)

		all, err := st.OAuthProviders().ListAll(ctx)
		require.NoError(t, err)
		require.NotNil(t, all)
		assert.Empty(t, all)
	})

	t.Run("delete then get", func(t *testing.T) {
		st := s.NewStore(t)
		provID := seedProvider(t, st, "del-get-prov", true)

		err := st.OAuthProviders().Delete(ctx, provID)
		require.NoError(t, err)

		_, err = st.OAuthProviders().GetByID(ctx, provID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("delete cascades to tokens and user links", func(t *testing.T) {
		st := s.NewStore(t)
		prov := SeedOAuthProvider(t, st, "cascade-prov")
		orgID := SeedOrg(t, st, "cascade-org", true)
		user := SeedUser(t, st, orgID, "cascade-user")

		err := st.OAuthTokens().Upsert(ctx, store.UpsertOAuthTokensParams{
			UserID:       user.ID,
			ProviderID:   prov.ID,
			AccessToken:  []byte("access"),
			RefreshToken: []byte("refresh"),
			TokenType:    "Bearer",
			ExpiresAt:    time.Now().Add(time.Hour),
			KeyVersion:   1,
		})
		require.NoError(t, err)

		err = st.OAuthUserLinks().Create(ctx, store.CreateOAuthUserLinkParams{
			UserID:          user.ID,
			ProviderID:      prov.ID,
			ProviderSubject: "sub-cascade",
		})
		require.NoError(t, err)

		err = st.OAuthProviders().Delete(ctx, prov.ID)
		require.NoError(t, err)

		// Tokens should be gone.
		_, err = st.OAuthTokens().Get(ctx, store.GetOAuthTokensParams{
			UserID:     user.ID,
			ProviderID: prov.ID,
		})
		assert.ErrorIs(t, err, store.ErrNotFound)

		// User links should be gone.
		links, err := st.OAuthUserLinks().ListByUser(ctx, user.ID)
		require.NoError(t, err)
		assert.Empty(t, links)
	})

	t.Run("deleted provider excluded from list all and list enabled", func(t *testing.T) {
		st := s.NewStore(t)
		alive := SeedOAuthProvider(t, st, "alive-prov")
		dead := SeedOAuthProvider(t, st, "dead-prov")

		err := st.OAuthProviders().Delete(ctx, dead.ID)
		require.NoError(t, err)

		all, err := st.OAuthProviders().ListAll(ctx)
		require.NoError(t, err)
		for _, p := range all {
			assert.NotEqual(t, dead.ID, p.ID, "deleted provider should not appear in list all")
		}

		enabled, err := st.OAuthProviders().ListEnabled(ctx)
		require.NoError(t, err)
		for _, p := range enabled {
			assert.NotEqual(t, dead.ID, p.ID, "deleted provider should not appear in list enabled")
		}

		// Alive provider should still be there.
		assert.True(t, len(all) >= 1)
		found := false
		for _, p := range all {
			if p.ID == alive.ID {
				found = true
			}
		}
		assert.True(t, found, "alive provider should still appear in list all")
	})

	t.Run("update enabled reflected in list enabled", func(t *testing.T) {
		st := s.NewStore(t)
		p1 := SeedOAuthProvider(t, st, "toggle-prov1")
		p2 := SeedOAuthProvider(t, st, "toggle-prov2")

		// Disable p1.
		err := st.OAuthProviders().UpdateEnabled(ctx, store.UpdateOAuthProviderEnabledParams{
			ID: p1.ID, Enabled: false,
		})
		require.NoError(t, err)

		enabled, err := st.OAuthProviders().ListEnabled(ctx)
		require.NoError(t, err)
		for _, p := range enabled {
			assert.NotEqual(t, p1.ID, p.ID, "disabled provider should not appear")
		}
		found := false
		for _, p := range enabled {
			if p.ID == p2.ID {
				found = true
			}
		}
		assert.True(t, found, "enabled provider should still appear")
	})

	t.Run("list all with secrets roundtrips client secret", func(t *testing.T) {
		st := s.NewStore(t)
		prov := SeedOAuthProvider(t, st, "secret-roundtrip-prov")

		all, err := st.OAuthProviders().ListAllWithSecrets(ctx)
		require.NoError(t, err)
		var found *store.OAuthProvider
		for _, p := range all {
			if p.ID == prov.ID {
				found = &p
				break
			}
		}
		require.NotNil(t, found, "provider should appear in list")
		assert.Equal(t, []byte("secret-secret-roundtrip-prov"), found.ClientSecret)
	})

	t.Run("update enabled toggle", func(t *testing.T) {
		st := s.NewStore(t)
		provID := seedProvider(t, st, "toggle2-prov", true)

		// Verify initially enabled.
		prov, err := st.OAuthProviders().GetByID(ctx, provID)
		require.NoError(t, err)
		assert.True(t, prov.Enabled)

		// Disable.
		err = st.OAuthProviders().UpdateEnabled(ctx, store.UpdateOAuthProviderEnabledParams{
			ID: provID, Enabled: false,
		})
		require.NoError(t, err)

		prov, err = st.OAuthProviders().GetByID(ctx, provID)
		require.NoError(t, err)
		assert.False(t, prov.Enabled)

		// Re-enable.
		err = st.OAuthProviders().UpdateEnabled(ctx, store.UpdateOAuthProviderEnabledParams{
			ID: provID, Enabled: true,
		})
		require.NoError(t, err)

		prov, err = st.OAuthProviders().GetByID(ctx, provID)
		require.NoError(t, err)
		assert.True(t, prov.Enabled)
	})
}
