package storetest

import (
	"testing"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testWorkspaces(t *testing.T) {
	t.Run("create and get by id", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-org", false)
		user := SeedUser(t, st, orgID, "ws-user")
		wsID := SeedWorkspace(t, st, orgID, user.ID, "My Workspace")

		ws, err := st.Workspaces().GetByID(ctx, wsID)
		require.NoError(t, err)
		assert.Equal(t, wsID, ws.ID)
		assert.Equal(t, orgID, ws.OrgID)
		assert.Equal(t, user.ID, ws.OwnerUserID)
		assert.Equal(t, "My Workspace", ws.Title)
		assert.False(t, ws.IsDeleted)
		assert.False(t, ws.CreatedAt.IsZero())
		assert.Nil(t, ws.DeletedAt)
	})

	t.Run("get by id not found", func(t *testing.T) {
		st := s.NewStore(t)
		_, err := st.Workspaces().GetByID(ctx, "nonexistent")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("list accessible", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-org", false)
		user := SeedUser(t, st, orgID, "ws-list-user")
		SeedWorkspace(t, st, orgID, user.ID, "WS 1")
		SeedWorkspace(t, st, orgID, user.ID, "WS 2")

		workspaces, err := st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: user.ID,
			OrgID:  orgID,
		})
		require.NoError(t, err)
		assert.Len(t, workspaces, 2)
	})

	t.Run("rename", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-org", false)
		user := SeedUser(t, st, orgID, "ws-rename-user")
		wsID := SeedWorkspace(t, st, orgID, user.ID, "Old Title")

		n, err := st.Workspaces().Rename(ctx, store.RenameWorkspaceParams{
			ID:          wsID,
			OwnerUserID: user.ID,
			Title:       "New Title",
		})
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		ws, err := st.Workspaces().GetByID(ctx, wsID)
		require.NoError(t, err)
		assert.Equal(t, "New Title", ws.Title)
	})

	t.Run("rename wrong owner", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-org", false)
		user := SeedUser(t, st, orgID, "ws-rename-wrong")
		wsID := SeedWorkspace(t, st, orgID, user.ID, "Title")

		n, err := st.Workspaces().Rename(ctx, store.RenameWorkspaceParams{
			ID:          wsID,
			OwnerUserID: "other-user",
			Title:       "Hacked",
		})
		require.NoError(t, err)
		assert.Equal(t, int64(0), n)
	})

	t.Run("soft delete", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-org", false)
		user := SeedUser(t, st, orgID, "ws-del-user")
		wsID := SeedWorkspace(t, st, orgID, user.ID, "Delete Me")

		n, err := st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{
			ID:          wsID,
			OwnerUserID: user.ID,
		})
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		// GetByID should not return soft-deleted workspaces.
		_, err = st.Workspaces().GetByID(ctx, wsID)
		assert.ErrorIs(t, err, store.ErrNotFound)

		// GetByIDIncludeDeleted should still return it.
		ws, err := st.Workspaces().GetByIDIncludeDeleted(ctx, wsID)
		require.NoError(t, err)
		assert.True(t, ws.IsDeleted)
		assert.NotNil(t, ws.DeletedAt)
	})

	t.Run("soft delete all by user", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-org", false)
		user := SeedUser(t, st, orgID, "ws-delall-user")
		ws1 := SeedWorkspace(t, st, orgID, user.ID, "WS A")
		ws2 := SeedWorkspace(t, st, orgID, user.ID, "WS B")

		err := st.Workspaces().SoftDeleteAllByUser(ctx, user.ID)
		require.NoError(t, err)

		for _, wsID := range []string{ws1, ws2} {
			ws, err := st.Workspaces().GetByIDIncludeDeleted(ctx, wsID)
			require.NoError(t, err)
			assert.True(t, ws.IsDeleted)
		}
	})

	t.Run("shared workspace appears in accessible list", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-org", false)
		owner := SeedUser(t, st, orgID, "ws-share-owner")
		viewer := SeedUser(t, st, orgID, "ws-share-viewer")
		wsID := SeedWorkspace(t, st, orgID, owner.ID, "Shared WS")

		// Viewer cannot see it yet.
		workspaces, err := st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: viewer.ID,
			OrgID:  orgID,
		})
		require.NoError(t, err)
		assert.Empty(t, workspaces)

		// Grant access.
		err = st.WorkspaceAccess().Grant(ctx, store.GrantWorkspaceAccessParams{
			WorkspaceID: wsID,
			UserID:      viewer.ID,
		})
		require.NoError(t, err)

		// Now viewer can see the shared workspace.
		workspaces, err = st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: viewer.ID,
			OrgID:  orgID,
		})
		require.NoError(t, err)
		require.Len(t, workspaces, 1)
		assert.Equal(t, wsID, workspaces[0].ID)
	})

	t.Run("soft-deleted shared workspace excluded from accessible list", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-org", false)
		owner := SeedUser(t, st, orgID, "ws-sddel-owner")
		viewer := SeedUser(t, st, orgID, "ws-sddel-viewer")
		wsID := SeedWorkspace(t, st, orgID, owner.ID, "To Be Deleted")

		// Grant access and verify visible.
		err := st.WorkspaceAccess().Grant(ctx, store.GrantWorkspaceAccessParams{
			WorkspaceID: wsID,
			UserID:      viewer.ID,
		})
		require.NoError(t, err)

		workspaces, err := st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: viewer.ID,
			OrgID:  orgID,
		})
		require.NoError(t, err)
		require.Len(t, workspaces, 1)

		// Soft-delete the workspace.
		_, err = st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{
			ID:          wsID,
			OwnerUserID: owner.ID,
		})
		require.NoError(t, err)

		// Viewer should no longer see it.
		workspaces, err = st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: viewer.ID,
			OrgID:  orgID,
		})
		require.NoError(t, err)
		assert.Empty(t, workspaces)
	})

	t.Run("soft deleted not in accessible list", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-org", false)
		user := SeedUser(t, st, orgID, "ws-acclist-user")
		SeedWorkspace(t, st, orgID, user.ID, "Alive")
		delID := SeedWorkspace(t, st, orgID, user.ID, "Dead")

		_, err := st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{
			ID:          delID,
			OwnerUserID: user.ID,
		})
		require.NoError(t, err)

		workspaces, err := st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: user.ID,
			OrgID:  orgID,
		})
		require.NoError(t, err)
		assert.Len(t, workspaces, 1)
		assert.Equal(t, "Alive", workspaces[0].Title)
	})

	t.Run("list accessible empty", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-org", false)
		user := SeedUser(t, st, orgID, "ws-empty-user")

		workspaces, err := st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: user.ID,
			OrgID:  orgID,
		})
		require.NoError(t, err)
		require.NotNil(t, workspaces)
		assert.Empty(t, workspaces)
	})

	t.Run("rename non-existent", func(t *testing.T) {
		st := s.NewStore(t)

		n, err := st.Workspaces().Rename(ctx, store.RenameWorkspaceParams{
			ID:          "nonexistent",
			OwnerUserID: "nonexistent",
			Title:       "New",
		})
		require.NoError(t, err)
		assert.Equal(t, int64(0), n)
	})

	t.Run("soft delete already deleted", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-org", false)
		user := SeedUser(t, st, orgID, "ws-deldel-user")
		wsID := SeedWorkspace(t, st, orgID, user.ID, "Double Delete")

		n, err := st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{
			ID:          wsID,
			OwnerUserID: user.ID,
		})
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		// Second soft-delete is idempotent (may return 0 or 1 depending on backend).
		_, err = st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{
			ID:          wsID,
			OwnerUserID: user.ID,
		})
		require.NoError(t, err)

		// The workspace should still be soft-deleted.
		_, err = st.Workspaces().GetByID(ctx, wsID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("soft delete all by user empty", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-org", false)
		user := SeedUser(t, st, orgID, "ws-delall-empty-user")

		// Should be a no-op when user has no workspaces.
		err := st.Workspaces().SoftDeleteAllByUser(ctx, user.ID)
		require.NoError(t, err)
	})

	t.Run("get by id include deleted returns non-deleted workspace", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-org", false)
		user := SeedUser(t, st, orgID, "ws-incl-nondel-user")
		wsID := SeedWorkspace(t, st, orgID, user.ID, "Live WS")

		ws, err := st.Workspaces().GetByIDIncludeDeleted(ctx, wsID)
		require.NoError(t, err)
		assert.Equal(t, wsID, ws.ID)
		assert.False(t, ws.IsDeleted)
		assert.Nil(t, ws.DeletedAt)
	})

	t.Run("soft delete all by user does not affect other users", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "ws-org", false)
		userA := SeedUser(t, st, orgID, "ws-sdabu-userA")
		userB := SeedUser(t, st, orgID, "ws-sdabu-userB")
		SeedWorkspace(t, st, orgID, userA.ID, "A's WS")
		bWS := SeedWorkspace(t, st, orgID, userB.ID, "B's WS")

		err := st.Workspaces().SoftDeleteAllByUser(ctx, userA.ID)
		require.NoError(t, err)

		// B's workspace should be untouched.
		ws, err := st.Workspaces().GetByID(ctx, bWS)
		require.NoError(t, err)
		assert.False(t, ws.IsDeleted)
	})

	t.Run("list accessible isolates by org", func(t *testing.T) {
		st := s.NewStore(t)
		orgA := SeedOrg(t, st, "iso-orgA", false)
		orgB := SeedOrg(t, st, "iso-orgB", false)
		owner := SeedUser(t, st, orgA, "iso-owner")
		viewer := SeedUser(t, st, orgA, "iso-viewer")
		wsA := SeedWorkspace(t, st, orgA, owner.ID, "WS in A")
		wsB := SeedWorkspace(t, st, orgB, owner.ID, "WS in B")

		// Grant viewer access to both workspaces.
		for _, wsID := range []string{wsA, wsB} {
			err := st.WorkspaceAccess().Grant(ctx, store.GrantWorkspaceAccessParams{
				WorkspaceID: wsID, UserID: viewer.ID,
			})
			require.NoError(t, err)
		}

		// ListAccessible for orgA should only return wsA.
		workspaces, err := st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: viewer.ID, OrgID: orgA,
		})
		require.NoError(t, err)
		require.Len(t, workspaces, 1)
		assert.Equal(t, wsA, workspaces[0].ID)
	})
}
