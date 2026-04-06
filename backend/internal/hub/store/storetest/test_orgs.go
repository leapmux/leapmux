package storetest

import (
	"fmt"
	"testing"
	"time"

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
			ID:         orgID,
			Name:       "test-org",
			IsPersonal: false,
		})
		require.NoError(t, err)

		org, err := st.Orgs().GetByID(ctx, orgID)
		require.NoError(t, err)
		assert.Equal(t, orgID, org.ID)
		assert.Equal(t, "test-org", org.Name)
		assert.False(t, org.IsPersonal)
		assert.False(t, org.CreatedAt.IsZero())
		assert.Nil(t, org.DeletedAt)
	})

	t.Run("create personal org", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := id.Generate()
		err := st.Orgs().Create(ctx, store.CreateOrgParams{
			ID:         orgID,
			Name:       "personal-org",
			IsPersonal: true,
		})
		require.NoError(t, err)

		org, err := st.Orgs().GetByID(ctx, orgID)
		require.NoError(t, err)
		assert.True(t, org.IsPersonal)
	})

	t.Run("get by name", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "findme-org", false)

		org, err := st.Orgs().GetByName(ctx, "findme-org")
		require.NoError(t, err)
		assert.Equal(t, orgID, org.ID)
	})

	t.Run("get by id not found", func(t *testing.T) {
		st := s.NewStore(t)
		_, err := st.Orgs().GetByID(ctx, "nonexistent")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("get by name not found", func(t *testing.T) {
		st := s.NewStore(t)
		_, err := st.Orgs().GetByName(ctx, "nonexistent")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("has any", func(t *testing.T) {
		st := s.NewStore(t)

		has, err := st.Orgs().HasAny(ctx)
		require.NoError(t, err)
		assert.False(t, has)

		SeedOrg(t, st, "org-a", false)

		has, err = st.Orgs().HasAny(ctx)
		require.NoError(t, err)
		assert.True(t, has)
	})

	t.Run("update name", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "old-name", false)

		err := st.Orgs().UpdateName(ctx, store.UpdateOrgNameParams{
			ID:   orgID,
			Name: "new-name",
		})
		require.NoError(t, err)

		org, err := st.Orgs().GetByID(ctx, orgID)
		require.NoError(t, err)
		assert.Equal(t, "new-name", org.Name)
	})

	t.Run("soft delete", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "to-delete", false)

		err := st.Orgs().SoftDelete(ctx, orgID)
		require.NoError(t, err)

		// GetByID should return ErrNotFound for soft-deleted orgs.
		_, err = st.Orgs().GetByID(ctx, orgID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("get by id include deleted", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "deleted-but-visible", false)

		err := st.Orgs().SoftDelete(ctx, orgID)
		require.NoError(t, err)

		org, err := st.Orgs().GetByIDIncludeDeleted(ctx, orgID)
		require.NoError(t, err)
		assert.Equal(t, orgID, org.ID)
		assert.NotNil(t, org.DeletedAt)
	})

	t.Run("has any after soft delete", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "only-org", false)

		err := st.Orgs().SoftDelete(ctx, orgID)
		require.NoError(t, err)

		// HasAny should return false when all orgs are soft-deleted.
		has, err := st.Orgs().HasAny(ctx)
		require.NoError(t, err)
		assert.False(t, has)
	})

	t.Run("soft delete non-personal", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "team-org", false)

		err := st.Orgs().SoftDeleteNonPersonal(ctx, orgID)
		require.NoError(t, err)

		_, err = st.Orgs().GetByID(ctx, orgID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("soft delete non-personal skips personal", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "my-personal", true)

		// SoftDeleteNonPersonal should not delete personal orgs.
		err := st.Orgs().SoftDeleteNonPersonal(ctx, orgID)
		require.NoError(t, err)

		// Org should still be accessible.
		org, err := st.Orgs().GetByID(ctx, orgID)
		require.NoError(t, err)
		assert.Equal(t, orgID, org.ID)
	})

	t.Run("duplicate id returns conflict", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "first", false)

		err := st.Orgs().Create(ctx, store.CreateOrgParams{
			ID:         orgID,
			Name:       "second",
			IsPersonal: false,
		})
		assert.ErrorIs(t, err, store.ErrConflict)
	})

	t.Run("update name non-existent", func(t *testing.T) {
		st := s.NewStore(t)

		// UpdateName on a non-existent org should not error (0 rows affected).
		err := st.Orgs().UpdateName(ctx, store.UpdateOrgNameParams{
			ID:   "nonexistent",
			Name: "new-name",
		})
		require.NoError(t, err)
	})

	t.Run("soft deleted excluded from get by name", func(t *testing.T) {
		st := s.NewStore(t)
		SeedOrg(t, st, "ghost-org", false)

		// Verify it exists.
		org, err := st.Orgs().GetByName(ctx, "ghost-org")
		require.NoError(t, err)

		err = st.Orgs().SoftDelete(ctx, org.ID)
		require.NoError(t, err)

		// GetByName should return ErrNotFound after soft-delete.
		_, err = st.Orgs().GetByName(ctx, "ghost-org")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("duplicate name returns conflict", func(t *testing.T) {
		st := s.NewStore(t)
		SeedOrg(t, st, "taken-name", false)

		err := st.Orgs().Create(ctx, store.CreateOrgParams{
			ID:         id.Generate(),
			Name:       "taken-name",
			IsPersonal: false,
		})
		assert.ErrorIs(t, err, store.ErrConflict)
	})

	t.Run("list all", func(t *testing.T) {
		st := s.NewStore(t)
		SeedOrg(t, st, "list-org-a", false)
		time.Sleep(5 * time.Millisecond)
		SeedOrg(t, st, "list-org-b", false)
		time.Sleep(5 * time.Millisecond)
		SeedOrg(t, st, "list-org-c", false)

		orgs, err := st.Orgs().ListAll(ctx, store.ListAllOrgsParams{Limit: 2})
		require.NoError(t, err)
		assert.Len(t, orgs, 2)
	})

	t.Run("list all empty", func(t *testing.T) {
		st := s.NewStore(t)
		orgs, err := st.Orgs().ListAll(ctx, store.ListAllOrgsParams{Limit: 100})
		require.NoError(t, err)
		require.NotNil(t, orgs)
		assert.Empty(t, orgs)
	})

	t.Run("list all with cursor returns next page", func(t *testing.T) {
		st := s.NewStore(t)
		SeedOrg(t, st, "cur-org-a", false)
		time.Sleep(5 * time.Millisecond)
		SeedOrg(t, st, "cur-org-b", false)
		time.Sleep(5 * time.Millisecond)
		SeedOrg(t, st, "cur-org-c", false)

		page1, err := st.Orgs().ListAll(ctx, store.ListAllOrgsParams{Limit: 2})
		require.NoError(t, err)
		require.Len(t, page1, 2)

		cursor := page1[len(page1)-1].CreatedAt.UTC().Format(time.RFC3339Nano)
		page2, err := st.Orgs().ListAll(ctx, store.ListAllOrgsParams{Cursor: cursor, Limit: 2})
		require.NoError(t, err)
		require.Len(t, page2, 1)

		// No overlap.
		page1IDs := map[string]bool{page1[0].ID: true, page1[1].ID: true}
		assert.False(t, page1IDs[page2[0].ID])
	})

	t.Run("list all excludes deleted", func(t *testing.T) {
		st := s.NewStore(t)
		SeedOrg(t, st, "alive-org", false)
		deadID := SeedOrg(t, st, "dead-org", false)

		err := st.Orgs().SoftDelete(ctx, deadID)
		require.NoError(t, err)

		orgs, err := st.Orgs().ListAll(ctx, store.ListAllOrgsParams{Limit: 100})
		require.NoError(t, err)
		for _, o := range orgs {
			assert.NotEqual(t, deadID, o.ID)
		}
	})

	t.Run("search by name prefix", func(t *testing.T) {
		st := s.NewStore(t)
		SeedOrg(t, st, "team-alpha", false)
		SeedOrg(t, st, "team-beta", false)
		SeedOrg(t, st, "other-org", false)

		q := "team"
		orgs, err := st.Orgs().Search(ctx, store.SearchOrgsParams{Query: &q, Limit: 100})
		require.NoError(t, err)
		assert.Len(t, orgs, 2)
	})

	t.Run("search prefix is case insensitive", func(t *testing.T) {
		st := s.NewStore(t)
		SeedOrg(t, st, "team-gamma", false)

		q := "TEAM"
		orgs, err := st.Orgs().Search(ctx, store.SearchOrgsParams{Query: &q, Limit: 100})
		require.NoError(t, err)
		assert.Len(t, orgs, 1)
	})

	t.Run("search with cursor", func(t *testing.T) {
		st := s.NewStore(t)
		SeedOrg(t, st, "search-org-a", false)
		time.Sleep(5 * time.Millisecond)
		SeedOrg(t, st, "search-org-b", false)
		time.Sleep(5 * time.Millisecond)
		SeedOrg(t, st, "search-org-c", false)

		q := "search-org"
		page1, err := st.Orgs().Search(ctx, store.SearchOrgsParams{Query: &q, Limit: 2})
		require.NoError(t, err)
		require.Len(t, page1, 2)

		cursor := page1[len(page1)-1].CreatedAt.UTC().Format(time.RFC3339Nano)
		page2, err := st.Orgs().Search(ctx, store.SearchOrgsParams{Query: &q, Cursor: cursor, Limit: 2})
		require.NoError(t, err)
		require.Len(t, page2, 1)
	})

	t.Run("search excludes deleted orgs without panic", func(t *testing.T) {
		st := s.NewStore(t)

		// Create and immediately soft-delete orgs so the bucket table has
		// entries but the hydrated result set is empty.
		for i := 0; i < 3; i++ {
			orgID := SeedOrg(t, st, fmt.Sprintf("del-search-org-%d", i), false)
			err := st.Orgs().SoftDelete(ctx, orgID)
			require.NoError(t, err)
		}

		q := "del-search-org"
		orgs, err := st.Orgs().Search(ctx, store.SearchOrgsParams{
			Query: &q, Limit: 10,
		})
		require.NoError(t, err)
		assert.Empty(t, orgs)
	})

	t.Run("reuse org name after soft delete", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "reuse-org-name", false)

		err := st.Orgs().SoftDelete(ctx, orgID)
		require.NoError(t, err)

		// Creating a new org with the same name should succeed.
		err = st.Orgs().Create(ctx, store.CreateOrgParams{
			ID: id.Generate(), Name: "reuse-org-name", IsPersonal: false,
		})
		require.NoError(t, err)
	})
}
