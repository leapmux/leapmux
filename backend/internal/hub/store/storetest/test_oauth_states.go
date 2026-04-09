package storetest

import (
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testOAuthStates(t *testing.T) {
	t.Run("create and get", func(t *testing.T) {
		st := s.NewStore(t)
		state := id.Generate()
		prov := SeedOAuthProvider(t, st, "state-prov")

		err := st.OAuthStates().Create(ctx, store.CreateOAuthStateParams{
			State:        state,
			ProviderID:   prov.ID,
			PkceVerifier: "verifier-abc",
			RedirectURI:  "https://example.com/callback",
			ExpiresAt:    time.Now().Add(10 * time.Minute),
		})
		require.NoError(t, err)

		found, err := st.OAuthStates().Get(ctx, state)
		require.NoError(t, err)
		assert.Equal(t, state, found.State)
		assert.Equal(t, prov.ID, found.ProviderID)
		assert.Equal(t, "verifier-abc", found.PkceVerifier)
		assert.Equal(t, "https://example.com/callback", found.RedirectURI)
		assert.False(t, found.CreatedAt.IsZero())
	})

	t.Run("get not found", func(t *testing.T) {
		st := s.NewStore(t)
		_, err := st.OAuthStates().Get(ctx, "nonexistent")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("delete", func(t *testing.T) {
		st := s.NewStore(t)
		state := id.Generate()
		prov := SeedOAuthProvider(t, st, "state-del-prov")

		err := st.OAuthStates().Create(ctx, store.CreateOAuthStateParams{
			State:        state,
			ProviderID:   prov.ID,
			PkceVerifier: "v",
			RedirectURI:  "https://example.com/cb",
			ExpiresAt:    time.Now().Add(10 * time.Minute),
		})
		require.NoError(t, err)

		err = st.OAuthStates().Delete(ctx, state)
		require.NoError(t, err)

		_, err = st.OAuthStates().Get(ctx, state)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("delete non existent", func(t *testing.T) {
		st := s.NewStore(t)

		err := st.OAuthStates().Delete(ctx, "nonexistent-state")
		require.NoError(t, err)
	})

	t.Run("create expired then cleanup", func(t *testing.T) {
		st := s.NewStore(t)
		state := id.Generate()
		prov := SeedOAuthProvider(t, st, "state-cleanup-prov")

		// Create a state that is already expired.
		err := st.OAuthStates().Create(ctx, store.CreateOAuthStateParams{
			State:        state,
			ProviderID:   prov.ID,
			PkceVerifier: "v",
			RedirectURI:  "https://example.com/cb",
			ExpiresAt:    time.Now().Add(-1 * time.Hour),
		})
		require.NoError(t, err)

		// Run cleanup via the Cleanup store.
		_, err = st.Cleanup().DeleteExpiredOAuthStates(ctx)
		require.NoError(t, err)

		// The expired state should be gone.
		_, err = st.OAuthStates().Get(ctx, state)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})
}
