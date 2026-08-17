package storetest

import (
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/internal/util/userid"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testOAuthUserLinks(t *testing.T) {
	t.Run("create and get", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "oul-user")
		prov := SeedOAuthProvider(t, st, "oul-prov")
		provID := prov.ID

		err := st.OAuthUserLinks().Create(ctx, store.CreateOAuthUserLinkParams{
			UserID:          userid.MustNew(user.ID),
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
		user := SeedUser(t, st, "oul-list-user")

		for i := 0; i < 2; i++ {
			prov := SeedOAuthProvider(t, st, fmt.Sprintf("oul-list-prov-%d", i))
			err := st.OAuthUserLinks().Create(ctx, store.CreateOAuthUserLinkParams{
				UserID:          userid.MustNew(user.ID),
				ProviderID:      prov.ID,
				ProviderSubject: "sub-" + id.Generate(),
			})
			require.NoError(t, err)
		}

		links, err := st.OAuthUserLinks().ListByUser(ctx, userid.MustNew(user.ID))
		require.NoError(t, err)
		assert.Len(t, links, 2)
	})

	t.Run("delete", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "oul-del-user")
		prov := SeedOAuthProvider(t, st, "oul-del-prov")
		provID := prov.ID

		err := st.OAuthUserLinks().Create(ctx, store.CreateOAuthUserLinkParams{
			UserID:          userid.MustNew(user.ID),
			ProviderID:      provID,
			ProviderSubject: "sub-del",
		})
		require.NoError(t, err)

		err = st.OAuthUserLinks().Delete(ctx, store.DeleteOAuthUserLinkParams{
			UserID:     userid.MustNew(user.ID),
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
		user := SeedUser(t, st, "oul-dbp-user")
		prov := SeedOAuthProvider(t, st, "oul-dbp-prov")
		provID := prov.ID

		err := st.OAuthUserLinks().Create(ctx, store.CreateOAuthUserLinkParams{
			UserID:          userid.MustNew(user.ID),
			ProviderID:      provID,
			ProviderSubject: "sub-dbp",
		})
		require.NoError(t, err)

		err = st.OAuthUserLinks().DeleteByProvider(ctx, provID)
		require.NoError(t, err)

		links, err := st.OAuthUserLinks().ListByUser(ctx, userid.MustNew(user.ID))
		require.NoError(t, err)
		require.NotNil(t, links)
		assert.Empty(t, links)
	})

	t.Run("delete by provider preserves other providers", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "oul-dbppres-user")
		prov1 := SeedOAuthProvider(t, st, "oul-dbppres-prov1")
		prov2 := SeedOAuthProvider(t, st, "oul-dbppres-prov2")

		for _, prov := range []*store.OAuthProvider{prov1, prov2} {
			err := st.OAuthUserLinks().Create(ctx, store.CreateOAuthUserLinkParams{
				UserID:          userid.MustNew(user.ID),
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
		user := SeedUser(t, st, "oul-listempty-user")

		links, err := st.OAuthUserLinks().ListByUser(ctx, userid.MustNew(user.ID))
		require.NoError(t, err)
		require.NotNil(t, links)
		assert.Empty(t, links)
	})

	t.Run("delete non existent", func(t *testing.T) {
		st := s.NewStore(t)

		err := st.OAuthUserLinks().Delete(ctx, store.DeleteOAuthUserLinkParams{
			UserID:     userid.MustNew("nonexistent-user"),
			ProviderID: "nonexistent-prov",
		})
		require.NoError(t, err)
	})

	// CountUsersOrphanedByProvider guards the admin verb that REMOVES a
	// provider: the delete cascades every link away, so an account with no
	// password and no other link loses its last way in. Each dialect spells
	// the false literal and the parameter differently, so all three run it.
	t.Run("counts only the accounts a provider removal would lock out", func(t *testing.T) {
		st := s.NewStore(t)
		target := SeedOAuthProvider(t, st, "oul-orphan-target")
		other := SeedOAuthProvider(t, st, "oul-orphan-other")

		link := func(user *store.User, provs ...*store.OAuthProvider) {
			for _, prov := range provs {
				require.NoError(t, st.OAuthUserLinks().Create(ctx, store.CreateOAuthUserLinkParams{
					UserID:          userid.MustNew(user.ID),
					ProviderID:      prov.ID,
					ProviderSubject: user.Username + "-" + prov.Name,
				}))
			}
		}
		count := func() int64 {
			n, err := st.OAuthUserLinks().CountUsersOrphanedByProvider(ctx, target.ID)
			require.NoError(t, err)
			return n
		}
		assert.Zero(t, count(), "a provider nobody uses orphans nobody")

		// At risk: no password, and this provider is the only link.
		orphaned := SeedPasswordlessUser(t, st, "oul-orphan-sso-only")
		link(orphaned, target)
		assert.Equal(t, int64(1), count())

		// Not at risk: a password, or a second provider to fall back to.
		link(SeedUser(t, st, "oul-orphan-has-password"), target)
		twoLinks := SeedPasswordlessUser(t, st, "oul-orphan-two-links")
		link(twoLinks, target, other)
		assert.Equal(t, int64(1), count(), "only the password-less single-link account counts")

		// A link to the OTHER provider only is outside this count.
		linkedElsewhere := SeedPasswordlessUser(t, st, "oul-orphan-elsewhere")
		link(linkedElsewhere, other)
		assert.Equal(t, int64(1), count())

		// A soft-deleted account cannot sign in either way.
		require.NoError(t, st.Users().Delete(ctx, orphaned.ID))
		assert.Zero(t, count(), "a deleted account is not locked out by the removal")
	})
}
