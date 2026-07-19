package storetest

import (
	"testing"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testOrgs(t *testing.T) {
	t.Run("create and get by id", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := id.Generate()
		err := st.Orgs().Create(ctx, store.CreateOrgParams{
			ID:   orgID,
			Name: "test-org",
		})
		require.NoError(t, err)

		org, err := st.Orgs().GetByID(ctx, orgID)
		require.NoError(t, err)
		assert.Equal(t, orgID, org.ID)
		assert.Equal(t, "test-org", org.Name)
		assert.False(t, org.CreatedAt.IsZero())
		assert.Nil(t, org.DeletedAt)
	})

	t.Run("create normalizes the org name to mirror the normalized username", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := id.Generate()
		err := st.Orgs().Create(ctx, store.CreateOrgParams{
			ID:   orgID,
			Name: "MixedCaseName",
		})
		require.NoError(t, err)

		org, err := st.Orgs().GetByID(ctx, orgID)
		require.NoError(t, err)
		// userStore.Create lowercases the username; the org name mirrors it, so
		// it must be stored with the same normalization -- otherwise a caller
		// passing a non-lowercase username leaves orgs.name and users.username
		// disagreeing in case.
		assert.Equal(t, store.NormalizeUsername("MixedCaseName"), org.Name)
	})

	t.Run("get by id not found", func(t *testing.T) {
		st := s.NewStore(t)
		_, err := st.Orgs().GetByID(ctx, "nonexistent")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("soft delete", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "to-delete")

		err := st.Orgs().SoftDelete(ctx, orgID)
		require.NoError(t, err)

		// GetByID should return ErrNotFound for soft-deleted orgs.
		_, err = st.Orgs().GetByID(ctx, orgID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("get by id include deleted", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "deleted-but-visible")

		err := st.Orgs().SoftDelete(ctx, orgID)
		require.NoError(t, err)

		org, err := st.Orgs().GetByIDIncludeDeleted(ctx, orgID)
		require.NoError(t, err)
		assert.Equal(t, orgID, org.ID)
		assert.NotNil(t, org.DeletedAt)
	})

	t.Run("duplicate id returns conflict", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "first")

		err := st.Orgs().Create(ctx, store.CreateOrgParams{
			ID:   orgID,
			Name: "second",
		})
		assert.ErrorIs(t, err, store.ErrConflict)
	})

	t.Run("duplicate name returns conflict", func(t *testing.T) {
		st := s.NewStore(t)
		SeedOrg(t, st, "taken-name")

		err := st.Orgs().Create(ctx, store.CreateOrgParams{
			ID:   id.Generate(),
			Name: "taken-name",
		})
		assert.ErrorIs(t, err, store.ErrConflict)
	})

	t.Run("reuse org name after soft delete", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "reuse-org-name")

		err := st.Orgs().SoftDelete(ctx, orgID)
		require.NoError(t, err)

		// Creating a new org with the same name should succeed.
		err = st.Orgs().Create(ctx, store.CreateOrgParams{
			ID: id.Generate(), Name: "reuse-org-name",
		})
		require.NoError(t, err)
	})
}
