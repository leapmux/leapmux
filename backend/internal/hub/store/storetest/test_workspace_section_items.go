package storetest

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testWorkspaceSectionItems(t *testing.T) {
	// Helper to seed a section.
	seedSection := func(t *testing.T, st store.Store, userID, name string, secType leapmuxv1.SectionType) string {
		t.Helper()
		secID := id.Generate()
		err := st.WorkspaceSections().Create(ctx, store.CreateWorkspaceSectionParams{
			ID:          secID,
			UserID:      userID,
			Name:        name,
			Position:    "a0",
			SectionType: secType,
			Sidebar:     leapmuxv1.Sidebar_SIDEBAR_LEFT,
		})
		require.NoError(t, err)
		return secID
	}

	t.Run("set and get", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wsi-org", false)
		user := SeedUser(t, st, orgID, "wsi-user")
		secID := seedSection(t, st, user.ID, "Section", leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS)
		wsID := SeedWorkspace(t, st, orgID, user.ID, "WS")

		err := st.WorkspaceSectionItems().Set(ctx, store.SetWorkspaceSectionItemParams{
			UserID:      user.ID,
			WorkspaceID: wsID,
			SectionID:   secID,
			Position:    "a0",
		})
		require.NoError(t, err)

		item, err := st.WorkspaceSectionItems().Get(ctx, store.GetWorkspaceSectionItemParams{
			UserID:      user.ID,
			WorkspaceID: wsID,
		})
		require.NoError(t, err)
		assert.Equal(t, user.ID, item.UserID)
		assert.Equal(t, wsID, item.WorkspaceID)
		assert.Equal(t, secID, item.SectionID)
		assert.Equal(t, "a0", item.Position)
	})

	t.Run("get not found", func(t *testing.T) {
		st := s.NewStore(t)
		_, err := st.WorkspaceSectionItems().Get(ctx, store.GetWorkspaceSectionItemParams{
			UserID:      "no-user",
			WorkspaceID: "no-ws",
		})
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("list by user", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wsi-org", false)
		user := SeedUser(t, st, orgID, "wsi-list-user")
		secID := seedSection(t, st, user.ID, "Section", leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS)
		ws1 := SeedWorkspace(t, st, orgID, user.ID, "WS 1")
		ws2 := SeedWorkspace(t, st, orgID, user.ID, "WS 2")

		for i, wsID := range []string{ws1, ws2} {
			err := st.WorkspaceSectionItems().Set(ctx, store.SetWorkspaceSectionItemParams{
				UserID:      user.ID,
				WorkspaceID: wsID,
				SectionID:   secID,
				Position:    string(rune('a'+i)) + "0",
			})
			require.NoError(t, err)
		}

		items, err := st.WorkspaceSectionItems().ListByUser(ctx, user.ID)
		require.NoError(t, err)
		assert.Len(t, items, 2)
	})

	t.Run("delete", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wsi-org", false)
		user := SeedUser(t, st, orgID, "wsi-del-user")
		secID := seedSection(t, st, user.ID, "Section", leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS)
		wsID := SeedWorkspace(t, st, orgID, user.ID, "WS")

		err := st.WorkspaceSectionItems().Set(ctx, store.SetWorkspaceSectionItemParams{
			UserID:      user.ID,
			WorkspaceID: wsID,
			SectionID:   secID,
			Position:    "a0",
		})
		require.NoError(t, err)

		err = st.WorkspaceSectionItems().Delete(ctx, store.DeleteWorkspaceSectionItemParams{
			UserID:      user.ID,
			WorkspaceID: wsID,
		})
		require.NoError(t, err)

		_, err = st.WorkspaceSectionItems().Get(ctx, store.GetWorkspaceSectionItemParams{
			UserID:      user.ID,
			WorkspaceID: wsID,
		})
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("delete by section", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wsi-org", false)
		user := SeedUser(t, st, orgID, "wsi-dbs-user")
		secID := seedSection(t, st, user.ID, "Section", leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS)
		ws1 := SeedWorkspace(t, st, orgID, user.ID, "WS 1")
		ws2 := SeedWorkspace(t, st, orgID, user.ID, "WS 2")

		for _, wsID := range []string{ws1, ws2} {
			err := st.WorkspaceSectionItems().Set(ctx, store.SetWorkspaceSectionItemParams{
				UserID:      user.ID,
				WorkspaceID: wsID,
				SectionID:   secID,
				Position:    "a0",
			})
			require.NoError(t, err)
		}

		err := st.WorkspaceSectionItems().DeleteBySection(ctx, secID)
		require.NoError(t, err)

		items, err := st.WorkspaceSectionItems().ListByUser(ctx, user.ID)
		require.NoError(t, err)
		require.NotNil(t, items)
		assert.Empty(t, items)
	})

	t.Run("move to section", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wsi-org", false)
		user := SeedUser(t, st, orgID, "wsi-move-user")
		fromSec := seedSection(t, st, user.ID, "From", leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS)
		toSec := seedSection(t, st, user.ID, "To", leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED)
		wsID := SeedWorkspace(t, st, orgID, user.ID, "WS")

		err := st.WorkspaceSectionItems().Set(ctx, store.SetWorkspaceSectionItemParams{
			UserID:      user.ID,
			WorkspaceID: wsID,
			SectionID:   fromSec,
			Position:    "a0",
		})
		require.NoError(t, err)

		err = st.WorkspaceSectionItems().MoveToSection(ctx, store.MoveWorkspaceSectionItemsToSectionParams{
			FromSectionID: fromSec,
			ToSectionID:   toSec,
		})
		require.NoError(t, err)

		item, err := st.WorkspaceSectionItems().Get(ctx, store.GetWorkspaceSectionItemParams{
			UserID:      user.ID,
			WorkspaceID: wsID,
		})
		require.NoError(t, err)
		assert.Equal(t, toSec, item.SectionID)
	})

	t.Run("is in archived section", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wsi-org", false)
		user := SeedUser(t, st, orgID, "wsi-arch-user")
		archSec := seedSection(t, st, user.ID, "Archive", leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED)
		ipSec := seedSection(t, st, user.ID, "In Progress", leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS)
		wsArch := SeedWorkspace(t, st, orgID, user.ID, "Archived WS")
		wsIP := SeedWorkspace(t, st, orgID, user.ID, "Active WS")

		err := st.WorkspaceSectionItems().Set(ctx, store.SetWorkspaceSectionItemParams{
			UserID: user.ID, WorkspaceID: wsArch, SectionID: archSec, Position: "a0",
		})
		require.NoError(t, err)
		err = st.WorkspaceSectionItems().Set(ctx, store.SetWorkspaceSectionItemParams{
			UserID: user.ID, WorkspaceID: wsIP, SectionID: ipSec, Position: "a0",
		})
		require.NoError(t, err)

		isArchived, err := st.WorkspaceSectionItems().IsInArchivedSection(ctx, store.IsWorkspaceInArchivedSectionParams{
			UserID:      user.ID,
			WorkspaceID: wsArch,
		})
		require.NoError(t, err)
		assert.True(t, isArchived)

		isArchived, err = st.WorkspaceSectionItems().IsInArchivedSection(ctx, store.IsWorkspaceInArchivedSectionParams{
			UserID:      user.ID,
			WorkspaceID: wsIP,
		})
		require.NoError(t, err)
		assert.False(t, isArchived)
	})

	t.Run("get not found after delete", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wsi-org", false)
		user := SeedUser(t, st, orgID, "wsi-getnf-user")
		secID := seedSection(t, st, user.ID, "Section", leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS)
		wsID := SeedWorkspace(t, st, orgID, user.ID, "WS")

		err := st.WorkspaceSectionItems().Set(ctx, store.SetWorkspaceSectionItemParams{
			UserID: user.ID, WorkspaceID: wsID, SectionID: secID, Position: "a0",
		})
		require.NoError(t, err)

		err = st.WorkspaceSectionItems().Delete(ctx, store.DeleteWorkspaceSectionItemParams{
			UserID: user.ID, WorkspaceID: wsID,
		})
		require.NoError(t, err)

		_, err = st.WorkspaceSectionItems().Get(ctx, store.GetWorkspaceSectionItemParams{
			UserID: user.ID, WorkspaceID: wsID,
		})
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("list by user empty", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wsi-org", false)
		user := SeedUser(t, st, orgID, "wsi-listempty-user")

		items, err := st.WorkspaceSectionItems().ListByUser(ctx, user.ID)
		require.NoError(t, err)
		require.NotNil(t, items)
		assert.Empty(t, items)
	})

	t.Run("move to section empty source", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wsi-org", false)
		user := SeedUser(t, st, orgID, "wsi-moveempty-user")
		fromSec := seedSection(t, st, user.ID, "From", leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS)
		toSec := seedSection(t, st, user.ID, "To", leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED)

		// Move from an empty section -- should be a no-op.
		err := st.WorkspaceSectionItems().MoveToSection(ctx, store.MoveWorkspaceSectionItemsToSectionParams{
			FromSectionID: fromSec,
			ToSectionID:   toSec,
		})
		require.NoError(t, err)
	})

	t.Run("set overwrites existing section assignment", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wsi-org", false)
		user := SeedUser(t, st, orgID, "wsi-overwrite-user")
		wsID := SeedWorkspace(t, st, orgID, user.ID, "Overwrite WS")
		sec1 := seedSection(t, st, user.ID, "Section A", leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS)
		sec2 := seedSection(t, st, user.ID, "Section B", leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS)

		// Assign workspace to section A.
		err := st.WorkspaceSectionItems().Set(ctx, store.SetWorkspaceSectionItemParams{
			UserID: user.ID, WorkspaceID: wsID, SectionID: sec1, Position: "a0",
		})
		require.NoError(t, err)

		// Reassign to section B.
		err = st.WorkspaceSectionItems().Set(ctx, store.SetWorkspaceSectionItemParams{
			UserID: user.ID, WorkspaceID: wsID, SectionID: sec2, Position: "b0",
		})
		require.NoError(t, err)

		// Should be in section B now.
		item, err := st.WorkspaceSectionItems().Get(ctx, store.GetWorkspaceSectionItemParams{
			UserID: user.ID, WorkspaceID: wsID,
		})
		require.NoError(t, err)
		assert.Equal(t, sec2, item.SectionID)
		assert.Equal(t, "b0", item.Position)

		// Should only have one item total.
		items, err := st.WorkspaceSectionItems().ListByUser(ctx, user.ID)
		require.NoError(t, err)
		assert.Len(t, items, 1)
	})

	t.Run("position-only update preserves section lookup", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wsi-org", false)
		user := SeedUser(t, st, orgID, "wsi-posonly-user")
		secID := seedSection(t, st, user.ID, "Section", leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS)
		wsID := SeedWorkspace(t, st, orgID, user.ID, "WS")

		// Initial assignment.
		err := st.WorkspaceSectionItems().Set(ctx, store.SetWorkspaceSectionItemParams{
			UserID: user.ID, WorkspaceID: wsID, SectionID: secID, Position: "a0",
		})
		require.NoError(t, err)

		// Update position only (same section).
		err = st.WorkspaceSectionItems().Set(ctx, store.SetWorkspaceSectionItemParams{
			UserID: user.ID, WorkspaceID: wsID, SectionID: secID, Position: "b0",
		})
		require.NoError(t, err)

		// Verify the item has the new position.
		item, err := st.WorkspaceSectionItems().Get(ctx, store.GetWorkspaceSectionItemParams{
			UserID: user.ID, WorkspaceID: wsID,
		})
		require.NoError(t, err)
		assert.Equal(t, "b0", item.Position)
		assert.Equal(t, secID, item.SectionID)

		// DeleteBySection should still find and delete the item
		// (proves section lookup table is intact after position-only update).
		err = st.WorkspaceSectionItems().DeleteBySection(ctx, secID)
		require.NoError(t, err)

		_, err = st.WorkspaceSectionItems().Get(ctx, store.GetWorkspaceSectionItemParams{
			UserID: user.ID, WorkspaceID: wsID,
		})
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("is in archived section not found", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wsi-org", false)
		user := SeedUser(t, st, orgID, "wsi-archnf-user")

		isArchived, err := st.WorkspaceSectionItems().IsInArchivedSection(ctx, store.IsWorkspaceInArchivedSectionParams{
			UserID:      user.ID,
			WorkspaceID: "nonexistent-ws",
		})
		require.NoError(t, err)
		assert.False(t, isArchived)
	})
}
