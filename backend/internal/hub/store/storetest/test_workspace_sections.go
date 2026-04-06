package storetest

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testWorkspaceSections(t *testing.T) {
	t.Run("create and get by id", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wsec-org", true)
		user := SeedUser(t, st, orgID, "wsec-user")

		secID := id.Generate()
		err := st.WorkspaceSections().Create(ctx, store.CreateWorkspaceSectionParams{
			ID:          secID,
			UserID:      user.ID,
			Name:        "In Progress",
			Position:    "a0",
			SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS,
			Sidebar:     leapmuxv1.Sidebar_SIDEBAR_LEFT,
		})
		require.NoError(t, err)

		sec, err := st.WorkspaceSections().GetByID(ctx, secID)
		require.NoError(t, err)
		assert.Equal(t, secID, sec.ID)
		assert.Equal(t, user.ID, sec.UserID)
		assert.Equal(t, "In Progress", sec.Name)
		assert.Equal(t, "a0", sec.Position)
		assert.Equal(t, leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS, sec.SectionType)
		assert.Equal(t, leapmuxv1.Sidebar_SIDEBAR_LEFT, sec.Sidebar)
		assert.False(t, sec.CreatedAt.IsZero())
	})

	t.Run("get by id not found", func(t *testing.T) {
		st := s.NewStore(t)
		_, err := st.WorkspaceSections().GetByID(ctx, "nonexistent")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("list by user id", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wsec-org", true)
		user := SeedUser(t, st, orgID, "wsec-list-user")

		for i, name := range []string{"Sec A", "Sec B"} {
			err := st.WorkspaceSections().Create(ctx, store.CreateWorkspaceSectionParams{
				ID:          id.Generate(),
				UserID:      user.ID,
				Name:        name,
				Position:    string(rune('a'+i)) + "0",
				SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM,
				Sidebar:     leapmuxv1.Sidebar_SIDEBAR_LEFT,
			})
			require.NoError(t, err)
		}

		sections, err := st.WorkspaceSections().ListByUserID(ctx, user.ID)
		require.NoError(t, err)
		assert.Len(t, sections, 2)
	})

	t.Run("rename", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wsec-org", true)
		user := SeedUser(t, st, orgID, "wsec-rename-user")

		secID := id.Generate()
		err := st.WorkspaceSections().Create(ctx, store.CreateWorkspaceSectionParams{
			ID:          secID,
			UserID:      user.ID,
			Name:        "Old Name",
			Position:    "a0",
			SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM,
			Sidebar:     leapmuxv1.Sidebar_SIDEBAR_LEFT,
		})
		require.NoError(t, err)

		n, err := st.WorkspaceSections().Rename(ctx, store.RenameWorkspaceSectionParams{
			ID:     secID,
			UserID: user.ID,
			Name:   "New Name",
		})
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		sec, err := st.WorkspaceSections().GetByID(ctx, secID)
		require.NoError(t, err)
		assert.Equal(t, "New Name", sec.Name)
	})

	t.Run("rename wrong user", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wsec-org", true)
		user := SeedUser(t, st, orgID, "wsec-rename-wrong")

		secID := id.Generate()
		err := st.WorkspaceSections().Create(ctx, store.CreateWorkspaceSectionParams{
			ID:          secID,
			UserID:      user.ID,
			Name:        "Name",
			Position:    "a0",
			SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM,
			Sidebar:     leapmuxv1.Sidebar_SIDEBAR_LEFT,
		})
		require.NoError(t, err)

		n, err := st.WorkspaceSections().Rename(ctx, store.RenameWorkspaceSectionParams{
			ID:     secID,
			UserID: "other-user",
			Name:   "Hacked",
		})
		require.NoError(t, err)
		assert.Equal(t, int64(0), n)
	})

	t.Run("update position", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wsec-org", true)
		user := SeedUser(t, st, orgID, "wsec-pos-user")

		secID := id.Generate()
		err := st.WorkspaceSections().Create(ctx, store.CreateWorkspaceSectionParams{
			ID:          secID,
			UserID:      user.ID,
			Name:        "Movable",
			Position:    "a0",
			SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM,
			Sidebar:     leapmuxv1.Sidebar_SIDEBAR_LEFT,
		})
		require.NoError(t, err)

		err = st.WorkspaceSections().UpdatePosition(ctx, store.UpdateWorkspaceSectionPositionParams{
			ID:       secID,
			UserID:   user.ID,
			Position: "z0",
		})
		require.NoError(t, err)

		sec, err := st.WorkspaceSections().GetByID(ctx, secID)
		require.NoError(t, err)
		assert.Equal(t, "z0", sec.Position)
	})

	t.Run("update sidebar position", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wsec-org", true)
		user := SeedUser(t, st, orgID, "wsec-sidebar-user")

		secID := id.Generate()
		err := st.WorkspaceSections().Create(ctx, store.CreateWorkspaceSectionParams{
			ID:          secID,
			UserID:      user.ID,
			Name:        "Sidebar Move",
			Position:    "a0",
			SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM,
			Sidebar:     leapmuxv1.Sidebar_SIDEBAR_LEFT,
		})
		require.NoError(t, err)

		err = st.WorkspaceSections().UpdateSidebarPosition(ctx, store.UpdateWorkspaceSectionSidebarPositionParams{
			ID:       secID,
			UserID:   user.ID,
			Sidebar:  leapmuxv1.Sidebar_SIDEBAR_RIGHT,
			Position: "m0",
		})
		require.NoError(t, err)

		sec, err := st.WorkspaceSections().GetByID(ctx, secID)
		require.NoError(t, err)
		assert.Equal(t, leapmuxv1.Sidebar_SIDEBAR_RIGHT, sec.Sidebar)
		assert.Equal(t, "m0", sec.Position)
	})

	t.Run("delete", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wsec-org", true)
		user := SeedUser(t, st, orgID, "wsec-del-user")

		secID := id.Generate()
		err := st.WorkspaceSections().Create(ctx, store.CreateWorkspaceSectionParams{
			ID:          secID,
			UserID:      user.ID,
			Name:        "Delete Me",
			Position:    "a0",
			SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM,
			Sidebar:     leapmuxv1.Sidebar_SIDEBAR_LEFT,
		})
		require.NoError(t, err)

		n, err := st.WorkspaceSections().Delete(ctx, store.DeleteWorkspaceSectionParams{
			ID:     secID,
			UserID: user.ID,
		})
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		_, err = st.WorkspaceSections().GetByID(ctx, secID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("delete section with items succeeds at store level", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wsec-org", true)
		user := SeedUser(t, st, orgID, "wsec-notempty-user")
		wsID := SeedWorkspace(t, st, orgID, user.ID, "ws-notempty")

		secID := id.Generate()
		err := st.WorkspaceSections().Create(ctx, store.CreateWorkspaceSectionParams{
			ID:          secID,
			UserID:      user.ID,
			Name:        "Has Items",
			Position:    "a0",
			SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM,
			Sidebar:     leapmuxv1.Sidebar_SIDEBAR_LEFT,
		})
		require.NoError(t, err)

		err = st.WorkspaceSectionItems().Set(ctx, store.SetWorkspaceSectionItemParams{
			UserID:      user.ID,
			WorkspaceID: wsID,
			SectionID:   secID,
			Position:    "a0",
		})
		require.NoError(t, err)

		// Store-level delete succeeds even with items (business rule is
		// enforced at the service layer, not the store layer).
		n, err := st.WorkspaceSections().Delete(ctx, store.DeleteWorkspaceSectionParams{
			ID:     secID,
			UserID: user.ID,
		})
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		_, err = st.WorkspaceSections().GetByID(ctx, secID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("has default for user", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wsec-org", true)
		user := SeedUser(t, st, orgID, "wsec-count-user")

		// Create an in-progress section (default type).
		err := st.WorkspaceSections().Create(ctx, store.CreateWorkspaceSectionParams{
			ID:          id.Generate(),
			UserID:      user.ID,
			Name:        "In Progress",
			Position:    "a0",
			SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS,
			Sidebar:     leapmuxv1.Sidebar_SIDEBAR_LEFT,
		})
		require.NoError(t, err)

		// Create an archived section (default type).
		err = st.WorkspaceSections().Create(ctx, store.CreateWorkspaceSectionParams{
			ID:          id.Generate(),
			UserID:      user.ID,
			Name:        "Archived",
			Position:    "b0",
			SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED,
			Sidebar:     leapmuxv1.Sidebar_SIDEBAR_LEFT,
		})
		require.NoError(t, err)

		// Create a custom section (not default).
		err = st.WorkspaceSections().Create(ctx, store.CreateWorkspaceSectionParams{
			ID:          id.Generate(),
			UserID:      user.ID,
			Name:        "Custom",
			Position:    "c0",
			SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM,
			Sidebar:     leapmuxv1.Sidebar_SIDEBAR_LEFT,
		})
		require.NoError(t, err)

		has, err := st.WorkspaceSections().HasDefaultForUser(ctx, user.ID)
		require.NoError(t, err)
		assert.True(t, has)
	})

	t.Run("list by user id empty", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wsec-org", true)
		user := SeedUser(t, st, orgID, "wsec-listempty-user")

		sections, err := st.WorkspaceSections().ListByUserID(ctx, user.ID)
		require.NoError(t, err)
		require.NotNil(t, sections)
		assert.Empty(t, sections)
	})

	t.Run("update sidebar position wrong user", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wsec-org", true)
		user := SeedUser(t, st, orgID, "wsec-sbwrong-user")

		secID := id.Generate()
		err := st.WorkspaceSections().Create(ctx, store.CreateWorkspaceSectionParams{
			ID:          secID,
			UserID:      user.ID,
			Name:        "SB Wrong",
			Position:    "a0",
			SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM,
			Sidebar:     leapmuxv1.Sidebar_SIDEBAR_LEFT,
		})
		require.NoError(t, err)

		// UpdateSidebarPosition with wrong user should be a no-op.
		err = st.WorkspaceSections().UpdateSidebarPosition(ctx, store.UpdateWorkspaceSectionSidebarPositionParams{
			ID:       secID,
			UserID:   "other-user",
			Sidebar:  leapmuxv1.Sidebar_SIDEBAR_RIGHT,
			Position: "z0",
		})
		require.NoError(t, err)

		// Verify it didn't change.
		sec, err := st.WorkspaceSections().GetByID(ctx, secID)
		require.NoError(t, err)
		assert.Equal(t, leapmuxv1.Sidebar_SIDEBAR_LEFT, sec.Sidebar)
		assert.Equal(t, "a0", sec.Position)
	})

	t.Run("delete non custom type fails", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wsec-org", true)
		user := SeedUser(t, st, orgID, "wsec-delnc-user")

		secID := id.Generate()
		err := st.WorkspaceSections().Create(ctx, store.CreateWorkspaceSectionParams{
			ID:          secID,
			UserID:      user.ID,
			Name:        "In Progress",
			Position:    "a0",
			SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS,
			Sidebar:     leapmuxv1.Sidebar_SIDEBAR_LEFT,
		})
		require.NoError(t, err)

		// Deleting a non-custom section should return 0 rows affected (no-op).
		n, err := st.WorkspaceSections().Delete(ctx, store.DeleteWorkspaceSectionParams{
			ID:     secID,
			UserID: user.ID,
		})
		require.NoError(t, err)
		assert.Equal(t, int64(0), n)

		// Section should still exist.
		_, err = st.WorkspaceSections().GetByID(ctx, secID)
		require.NoError(t, err)
	})

	t.Run("has default for user false", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wsec-org", true)
		user := SeedUser(t, st, orgID, "wsec-countzero-user")

		has, err := st.WorkspaceSections().HasDefaultForUser(ctx, user.ID)
		require.NoError(t, err)
		assert.False(t, has)
	})
}
