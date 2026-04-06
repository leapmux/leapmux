package storetest

import (
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testOAuthTokens(t *testing.T) {
	t.Run("upsert and get", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ot-org", true)
		user := SeedUser(t, st, orgID, "ot-user")
		prov := SeedOAuthProvider(t, st, "ot-prov")
		provID := prov.ID

		err := st.OAuthTokens().Upsert(ctx, store.UpsertOAuthTokensParams{
			UserID:       user.ID,
			ProviderID:   provID,
			AccessToken:  []byte("access"),
			RefreshToken: []byte("refresh"),
			TokenType:    "Bearer",
			ExpiresAt:    time.Now().Add(1 * time.Hour),
			KeyVersion:   1,
		})
		require.NoError(t, err)

		token, err := st.OAuthTokens().Get(ctx, store.GetOAuthTokensParams{
			UserID:     user.ID,
			ProviderID: provID,
		})
		require.NoError(t, err)
		assert.Equal(t, user.ID, token.UserID)
		assert.Equal(t, provID, token.ProviderID)
		assert.Equal(t, []byte("access"), token.AccessToken)
		assert.Equal(t, []byte("refresh"), token.RefreshToken)
		assert.Equal(t, "Bearer", token.TokenType)
		assert.Equal(t, int64(1), token.KeyVersion)
	})

	t.Run("get not found", func(t *testing.T) {
		st := s.NewStore(t)
		_, err := st.OAuthTokens().Get(ctx, store.GetOAuthTokensParams{
			UserID:     "no-user",
			ProviderID: "no-prov",
		})
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("upsert overwrites", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ot-org", true)
		user := SeedUser(t, st, orgID, "ot-overwrite-user")
		prov := SeedOAuthProvider(t, st, "ot-overwrite-prov")
		provID := prov.ID

		for _, v := range []int64{1, 2} {
			err := st.OAuthTokens().Upsert(ctx, store.UpsertOAuthTokensParams{
				UserID:       user.ID,
				ProviderID:   provID,
				AccessToken:  []byte("access"),
				RefreshToken: []byte("refresh"),
				TokenType:    "Bearer",
				ExpiresAt:    time.Now().Add(1 * time.Hour),
				KeyVersion:   v,
			})
			require.NoError(t, err)
		}

		token, err := st.OAuthTokens().Get(ctx, store.GetOAuthTokensParams{
			UserID:     user.ID,
			ProviderID: provID,
		})
		require.NoError(t, err)
		assert.Equal(t, int64(2), token.KeyVersion)
	})

	t.Run("list expiring", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ot-org", true)
		user := SeedUser(t, st, orgID, "ot-expiring-user")
		prov := SeedOAuthProvider(t, st, "ot-expiring-prov")
		provID := prov.ID

		err := st.OAuthTokens().Upsert(ctx, store.UpsertOAuthTokensParams{
			UserID:       user.ID,
			ProviderID:   provID,
			AccessToken:  []byte("access"),
			RefreshToken: []byte("refresh"),
			TokenType:    "Bearer",
			ExpiresAt:    time.Now().Add(2 * time.Minute),
			KeyVersion:   1,
		})
		require.NoError(t, err)

		tokens, err := st.OAuthTokens().ListExpiring(ctx)
		require.NoError(t, err)
		assert.NotEmpty(t, tokens)
	})

	t.Run("list and count by key version", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ot-org", true)
		user := SeedUser(t, st, orgID, "ot-kv-user")
		prov := SeedOAuthProvider(t, st, "ot-kv-prov")
		provID := prov.ID

		err := st.OAuthTokens().Upsert(ctx, store.UpsertOAuthTokensParams{
			UserID:       user.ID,
			ProviderID:   provID,
			AccessToken:  []byte("access"),
			RefreshToken: []byte("refresh"),
			TokenType:    "Bearer",
			ExpiresAt:    time.Now().Add(1 * time.Hour),
			KeyVersion:   42,
		})
		require.NoError(t, err)

		tokens, err := st.OAuthTokens().ListByKeyVersion(ctx, 42)
		require.NoError(t, err)
		assert.Len(t, tokens, 1)

		count, err := st.OAuthTokens().CountByKeyVersion(ctx, 42)
		require.NoError(t, err)
		assert.Equal(t, int64(1), count)

		count, err = st.OAuthTokens().CountByKeyVersion(ctx, 99)
		require.NoError(t, err)
		assert.Equal(t, int64(0), count)
	})

	t.Run("delete by provider", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ot-org", true)
		user := SeedUser(t, st, orgID, "ot-dbp-user")
		prov := SeedOAuthProvider(t, st, "ot-dbp-prov")
		provID := prov.ID

		err := st.OAuthTokens().Upsert(ctx, store.UpsertOAuthTokensParams{
			UserID:       user.ID,
			ProviderID:   provID,
			AccessToken:  []byte("a"),
			RefreshToken: []byte("r"),
			TokenType:    "Bearer",
			ExpiresAt:    time.Now().Add(1 * time.Hour),
			KeyVersion:   1,
		})
		require.NoError(t, err)

		err = st.OAuthTokens().DeleteByProvider(ctx, provID)
		require.NoError(t, err)

		_, err = st.OAuthTokens().Get(ctx, store.GetOAuthTokensParams{
			UserID:     user.ID,
			ProviderID: provID,
		})
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("delete by user", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ot-org", true)
		user := SeedUser(t, st, orgID, "ot-dbu-user")
		prov := SeedOAuthProvider(t, st, "ot-dbu-prov")
		provID := prov.ID

		err := st.OAuthTokens().Upsert(ctx, store.UpsertOAuthTokensParams{
			UserID:       user.ID,
			ProviderID:   provID,
			AccessToken:  []byte("a"),
			RefreshToken: []byte("r"),
			TokenType:    "Bearer",
			ExpiresAt:    time.Now().Add(1 * time.Hour),
			KeyVersion:   1,
		})
		require.NoError(t, err)

		err = st.OAuthTokens().DeleteByUser(ctx, user.ID)
		require.NoError(t, err)

		_, err = st.OAuthTokens().Get(ctx, store.GetOAuthTokensParams{
			UserID:     user.ID,
			ProviderID: provID,
		})
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("delete by user and provider", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ot-org", true)
		user := SeedUser(t, st, orgID, "ot-dbup-user")
		p1 := SeedOAuthProvider(t, st, "ot-dbup-prov1")
		p2 := SeedOAuthProvider(t, st, "ot-dbup-prov2")
		prov1 := p1.ID
		prov2 := p2.ID

		for _, provID := range []string{prov1, prov2} {
			err := st.OAuthTokens().Upsert(ctx, store.UpsertOAuthTokensParams{
				UserID:       user.ID,
				ProviderID:   provID,
				AccessToken:  []byte("a"),
				RefreshToken: []byte("r"),
				TokenType:    "Bearer",
				ExpiresAt:    time.Now().Add(1 * time.Hour),
				KeyVersion:   1,
			})
			require.NoError(t, err)
		}

		err := st.OAuthTokens().DeleteByUserAndProvider(ctx, store.DeleteOAuthTokensByUserAndProviderParams{
			UserID:     user.ID,
			ProviderID: prov1,
		})
		require.NoError(t, err)

		_, err = st.OAuthTokens().Get(ctx, store.GetOAuthTokensParams{
			UserID: user.ID, ProviderID: prov1,
		})
		assert.ErrorIs(t, err, store.ErrNotFound)

		// prov2 should still exist.
		_, err = st.OAuthTokens().Get(ctx, store.GetOAuthTokensParams{
			UserID: user.ID, ProviderID: prov2,
		})
		require.NoError(t, err)
	})

	t.Run("delete by provider preserves other providers", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ot-org", true)
		user := SeedUser(t, st, orgID, "ot-dbppres-user")
		prov1 := SeedOAuthProvider(t, st, "ot-dbppres-prov1")
		prov2 := SeedOAuthProvider(t, st, "ot-dbppres-prov2")

		for _, prov := range []*store.OAuthProvider{prov1, prov2} {
			err := st.OAuthTokens().Upsert(ctx, store.UpsertOAuthTokensParams{
				UserID:       user.ID,
				ProviderID:   prov.ID,
				AccessToken:  []byte("access"),
				RefreshToken: []byte("refresh"),
				TokenType:    "Bearer",
				ExpiresAt:    time.Now().Add(1 * time.Hour),
				KeyVersion:   1,
			})
			require.NoError(t, err)
		}

		err := st.OAuthTokens().DeleteByProvider(ctx, prov1.ID)
		require.NoError(t, err)

		// prov1 tokens should be gone.
		_, err = st.OAuthTokens().Get(ctx, store.GetOAuthTokensParams{
			UserID: user.ID, ProviderID: prov1.ID,
		})
		assert.ErrorIs(t, err, store.ErrNotFound)

		// prov2 tokens should survive.
		_, err = st.OAuthTokens().Get(ctx, store.GetOAuthTokensParams{
			UserID: user.ID, ProviderID: prov2.ID,
		})
		require.NoError(t, err)
	})

	t.Run("list expiring empty", func(t *testing.T) {
		st := s.NewStore(t)

		tokens, err := st.OAuthTokens().ListExpiring(ctx)
		require.NoError(t, err)
		require.NotNil(t, tokens)
		assert.Empty(t, tokens)
	})

	t.Run("list by key version empty", func(t *testing.T) {
		st := s.NewStore(t)

		tokens, err := st.OAuthTokens().ListByKeyVersion(ctx, 999)
		require.NoError(t, err)
		require.NotNil(t, tokens)
		assert.Empty(t, tokens)
	})

	t.Run("count by key version zero", func(t *testing.T) {
		st := s.NewStore(t)

		count, err := st.OAuthTokens().CountByKeyVersion(ctx, 999)
		require.NoError(t, err)
		assert.Equal(t, int64(0), count)
	})
}
