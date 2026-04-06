package storetest

import (
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testPendingOAuthSignups(t *testing.T) {
	t.Run("create and get", func(t *testing.T) {
		st := s.NewStore(t)
		token := id.Generate()
		prov := SeedOAuthProvider(t, st, "signup-prov")
		provID := prov.ID

		err := st.PendingOAuthSignups().Create(ctx, store.CreatePendingOAuthSignupParams{
			Token:           token,
			ProviderID:      provID,
			ProviderSubject: "sub-signup",
			Email:           "signup@example.com",
			DisplayName:     "Signup User",
			AccessToken:     []byte("at"),
			RefreshToken:    []byte("rt"),
			TokenType:       "Bearer",
			TokenExpiresAt:  time.Now().Add(1 * time.Hour),
			KeyVersion:      1,
			RedirectURI:     "https://example.com/cb",
			ExpiresAt:       time.Now().Add(15 * time.Minute),
		})
		require.NoError(t, err)

		found, err := st.PendingOAuthSignups().Get(ctx, token)
		require.NoError(t, err)
		assert.Equal(t, token, found.Token)
		assert.Equal(t, provID, found.ProviderID)
		assert.Equal(t, "sub-signup", found.ProviderSubject)
		assert.Equal(t, "signup@example.com", found.Email)
		assert.Equal(t, "Signup User", found.DisplayName)
		assert.Equal(t, []byte("at"), found.AccessToken)
		assert.Equal(t, []byte("rt"), found.RefreshToken)
		assert.Equal(t, "Bearer", found.TokenType)
		assert.Equal(t, int64(1), found.KeyVersion)
		assert.Equal(t, "https://example.com/cb", found.RedirectURI)
		assert.False(t, found.CreatedAt.IsZero())
	})

	t.Run("get not found", func(t *testing.T) {
		st := s.NewStore(t)
		_, err := st.PendingOAuthSignups().Get(ctx, "nonexistent")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("delete", func(t *testing.T) {
		st := s.NewStore(t)
		token := id.Generate()
		prov := SeedOAuthProvider(t, st, "signup-del-prov")

		err := st.PendingOAuthSignups().Create(ctx, store.CreatePendingOAuthSignupParams{
			Token:           token,
			ProviderID:      prov.ID,
			ProviderSubject: "sub",
			Email:           "del@example.com",
			DisplayName:     "Del",
			AccessToken:     []byte("a"),
			RefreshToken:    []byte("r"),
			TokenType:       "Bearer",
			TokenExpiresAt:  time.Now().Add(1 * time.Hour),
			KeyVersion:      1,
			RedirectURI:     "https://example.com/cb",
			ExpiresAt:       time.Now().Add(15 * time.Minute),
		})
		require.NoError(t, err)

		err = st.PendingOAuthSignups().Delete(ctx, token)
		require.NoError(t, err)

		_, err = st.PendingOAuthSignups().Get(ctx, token)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("delete non existent", func(t *testing.T) {
		st := s.NewStore(t)

		err := st.PendingOAuthSignups().Delete(ctx, "nonexistent-token")
		require.NoError(t, err)
	})

	t.Run("create expired then cleanup", func(t *testing.T) {
		st := s.NewStore(t)
		token := id.Generate()
		prov := SeedOAuthProvider(t, st, "signup-cleanup-prov")

		// Create a signup that is already expired.
		err := st.PendingOAuthSignups().Create(ctx, store.CreatePendingOAuthSignupParams{
			Token:           token,
			ProviderID:      prov.ID,
			ProviderSubject: "sub-exp",
			Email:           "expired@example.com",
			DisplayName:     "Expired",
			AccessToken:     []byte("a"),
			RefreshToken:    []byte("r"),
			TokenType:       "Bearer",
			TokenExpiresAt:  time.Now().Add(1 * time.Hour),
			KeyVersion:      1,
			RedirectURI:     "https://example.com/cb",
			ExpiresAt:       time.Now().Add(-1 * time.Hour),
		})
		require.NoError(t, err)

		// Run cleanup.
		_, err = st.Cleanup().DeleteExpiredPendingOAuthSignups(ctx)
		require.NoError(t, err)

		// The expired signup should be gone.
		_, err = st.PendingOAuthSignups().Get(ctx, token)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})
}
