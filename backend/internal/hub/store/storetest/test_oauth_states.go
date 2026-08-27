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
			NonceHash:    "6b86b273ff34fce19d6b804eff5a3f5747ada4eaa22f1d49c01e52ddb7875b4b",
			RedirectURI:  "https://example.com/callback",
			Purpose:      store.OAuthStatePurposeLogin,
			ExpiresAt:    time.Now().Add(10 * time.Minute),
		})
		require.NoError(t, err)

		found, err := st.OAuthStates().Get(ctx, state)
		require.NoError(t, err)
		assert.Equal(t, state, found.State)
		assert.Equal(t, prov.ID, found.ProviderID)
		assert.Equal(t, "verifier-abc", found.PkceVerifier)
		assert.Equal(t, "https://example.com/callback", found.RedirectURI)
		// The browser-binding hash must round-trip on every dialect: the
		// callback refuses a row whose hash it cannot match, so a column
		// that silently drops the value would refuse every OAuth login.
		assert.Equal(t, "6b86b273ff34fce19d6b804eff5a3f5747ada4eaa22f1d49c01e52ddb7875b4b", found.NonceHash)
		assert.False(t, found.CreatedAt.IsZero())
	})

	// A reauth row must round-trip both fields it is made of. The callback
	// branches on purpose and elevates session_id, so a dialect that dropped
	// either would turn a re-authentication into a fresh sign-in.
	t.Run("a reauth row round-trips its purpose and session", func(t *testing.T) {
		st := s.NewStore(t)
		state := id.Generate()
		prov := SeedOAuthProvider(t, st, "state-reauth-prov")
		user := SeedUser(t, st, "state-reauth-user")
		sess := SeedSession(t, st, user.ID)

		require.NoError(t, st.OAuthStates().Create(ctx, store.CreateOAuthStateParams{
			State:        state,
			ProviderID:   prov.ID,
			PkceVerifier: "verifier",
			RedirectURI:  "https://example.com/cb",
			Purpose:      store.OAuthStatePurposeReauth,
			SessionID:    sess.ID,
			ExpiresAt:    time.Now().Add(10 * time.Minute),
		}))

		found, err := st.OAuthStates().Get(ctx, state)
		require.NoError(t, err)
		assert.Equal(t, store.OAuthStatePurposeReauth, found.Purpose)
		assert.Equal(t, sess.ID, found.SessionID)
	})

	// purpose is an enumerated column, and the schema is what enforces the
	// enumeration. Go's zero value is "", never "login", so an explicit insert
	// never reaches the column DEFAULT -- and the callback reads every value
	// that is not "reauth" as a login, which may create a session or link an
	// identity. The CHECK refuses the row instead.
	t.Run("an unknown purpose is refused by the schema", func(t *testing.T) {
		st := s.NewStore(t)
		prov := SeedOAuthProvider(t, st, "state-badpurpose-prov")

		for _, purpose := range []string{"", "LOGIN", "elevate"} {
			err := st.OAuthStates().Create(ctx, store.CreateOAuthStateParams{
				State:        id.Generate(),
				ProviderID:   prov.ID,
				PkceVerifier: "v",
				RedirectURI:  "https://example.com/cb",
				Purpose:      purpose,
				ExpiresAt:    time.Now().Add(10 * time.Minute),
			})
			assert.Error(t, err, "purpose %q must not be storable", purpose)
		}
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
			Purpose:      store.OAuthStatePurposeLogin,
			ExpiresAt:    time.Now().Add(10 * time.Minute),
		})
		require.NoError(t, err)

		deleted, err := st.OAuthStates().Delete(ctx, state)
		require.NoError(t, err)
		assert.EqualValues(t, 1, deleted, "the delete reports the row it consumed")

		_, err = st.OAuthStates().Get(ctx, state)
		assert.ErrorIs(t, err, store.ErrNotFound)

		// The SECOND delete of the same state reports zero. That count is
		// what makes the single use of a flow the hub's own property; the
		// callback refuses on it.
		again, err := st.OAuthStates().Delete(ctx, state)
		require.NoError(t, err)
		assert.EqualValues(t, 0, again, "a state row is consumable once")
	})

	t.Run("delete non existent", func(t *testing.T) {
		st := s.NewStore(t)

		deleted, err := st.OAuthStates().Delete(ctx, "nonexistent-state")
		require.NoError(t, err)
		assert.EqualValues(t, 0, deleted)
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
			Purpose:      store.OAuthStatePurposeLogin,
			ExpiresAt:    time.Now().Add(-1 * time.Hour),
		})
		require.NoError(t, err)

		// Run cleanup via the Cleanup store.
		_, err = st.Cleanup().DeleteExpiredOAuthStates(ctx, time.Now().UTC())
		require.NoError(t, err)

		// The expired state should be gone.
		_, err = st.OAuthStates().Get(ctx, state)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})
}
