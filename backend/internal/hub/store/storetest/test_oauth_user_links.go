package storetest

import (
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testOAuthUserLinks(t *testing.T) {
	t.Run("create and get", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "oul-org", true)
		user := SeedUser(t, st, orgID, "oul-user")
		prov := SeedOAuthProvider(t, st, "oul-prov")
		provID := prov.ID

		err := st.OAuthUserLinks().Create(ctx, store.CreateOAuthUserLinkParams{
			UserID:          user.ID,
			ProviderID:      provID,
			ProviderSubject: "sub-123",
		})
		require.NoError(t, err)

		link, err := st.OAuthUserLinks().Get(ctx, store.GetOAuthUserLinkParams{
			ProviderID:      provID,
			ProviderSubject: "sub-123",
		})
		require.NoError(t, err)
		assert.Equal(t, user.ID, link.UserID)
		assert.Equal(t, provID, link.ProviderID)
		assert.Equal(t, "sub-123", link.ProviderSubject)
		assert.False(t, link.CreatedAt.IsZero())
	})

	t.Run("get not found", func(t *testing.T) {
		st := s.NewStore(t)
		_, err := st.OAuthUserLinks().Get(ctx, store.GetOAuthUserLinkParams{
			ProviderID:      "no-prov",
			ProviderSubject: "no-sub",
		})
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("list by user", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "oul-org", true)
		user := SeedUser(t, st, orgID, "oul-list-user")

		for i := 0; i < 2; i++ {
			prov := SeedOAuthProvider(t, st, fmt.Sprintf("oul-list-prov-%d", i))
			err := st.OAuthUserLinks().Create(ctx, store.CreateOAuthUserLinkParams{
				UserID:          user.ID,
				ProviderID:      prov.ID,
				ProviderSubject: "sub-" + id.Generate(),
			})
			require.NoError(t, err)
		}

		links, err := st.OAuthUserLinks().ListByUser(ctx, user.ID)
		require.NoError(t, err)
		assert.Len(t, links, 2)
	})

	t.Run("delete", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "oul-org", true)
		user := SeedUser(t, st, orgID, "oul-del-user")
		prov := SeedOAuthProvider(t, st, "oul-del-prov")
		provID := prov.ID

		err := st.OAuthUserLinks().Create(ctx, store.CreateOAuthUserLinkParams{
			UserID:          user.ID,
			ProviderID:      provID,
			ProviderSubject: "sub-del",
		})
		require.NoError(t, err)

		err = st.OAuthUserLinks().Delete(ctx, store.DeleteOAuthUserLinkParams{
			UserID:     user.ID,
			ProviderID: provID,
		})
		require.NoError(t, err)

		_, err = st.OAuthUserLinks().Get(ctx, store.GetOAuthUserLinkParams{
			ProviderID:      provID,
			ProviderSubject: "sub-del",
		})
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("delete by provider", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "oul-org", true)
		user := SeedUser(t, st, orgID, "oul-dbp-user")
		prov := SeedOAuthProvider(t, st, "oul-dbp-prov")
		provID := prov.ID

		err := st.OAuthUserLinks().Create(ctx, store.CreateOAuthUserLinkParams{
			UserID:          user.ID,
			ProviderID:      provID,
			ProviderSubject: "sub-dbp",
		})
		require.NoError(t, err)

		err = st.OAuthUserLinks().DeleteByProvider(ctx, provID)
		require.NoError(t, err)

		links, err := st.OAuthUserLinks().ListByUser(ctx, user.ID)
		require.NoError(t, err)
		require.NotNil(t, links)
		assert.Empty(t, links)
	})

	t.Run("delete by provider preserves other providers", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "oul-org", true)
		user := SeedUser(t, st, orgID, "oul-dbppres-user")
		prov1 := SeedOAuthProvider(t, st, "oul-dbppres-prov1")
		prov2 := SeedOAuthProvider(t, st, "oul-dbppres-prov2")

		for _, prov := range []*store.OAuthProvider{prov1, prov2} {
			err := st.OAuthUserLinks().Create(ctx, store.CreateOAuthUserLinkParams{
				UserID:          user.ID,
				ProviderID:      prov.ID,
				ProviderSubject: "sub-" + prov.Name,
			})
			require.NoError(t, err)
		}

		err := st.OAuthUserLinks().DeleteByProvider(ctx, prov1.ID)
		require.NoError(t, err)

		// prov1 link should be gone.
		_, err = st.OAuthUserLinks().Get(ctx, store.GetOAuthUserLinkParams{
			ProviderID: prov1.ID, ProviderSubject: "sub-" + prov1.Name,
		})
		assert.ErrorIs(t, err, store.ErrNotFound)

		// prov2 link should survive.
		link, err := st.OAuthUserLinks().Get(ctx, store.GetOAuthUserLinkParams{
			ProviderID: prov2.ID, ProviderSubject: "sub-" + prov2.Name,
		})
		require.NoError(t, err)
		assert.Equal(t, user.ID, link.UserID)
	})

	t.Run("list by user empty", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "oul-org", true)
		user := SeedUser(t, st, orgID, "oul-listempty-user")

		links, err := st.OAuthUserLinks().ListByUser(ctx, user.ID)
		require.NoError(t, err)
		require.NotNil(t, links)
		assert.Empty(t, links)
	})

	t.Run("delete non existent", func(t *testing.T) {
		st := s.NewStore(t)

		err := st.OAuthUserLinks().Delete(ctx, store.DeleteOAuthUserLinkParams{
			UserID:     "nonexistent-user",
			ProviderID: "nonexistent-prov",
		})
		require.NoError(t, err)
	})
}
