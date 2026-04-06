package storetest

import (
	"testing"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testWorkspaceAccess(t *testing.T) {
	t.Run("grant and has access", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wa-org", false)
		owner := SeedUser(t, st, orgID, "wa-owner")
		viewer := SeedUser(t, st, orgID, "wa-viewer")
		wsID := SeedWorkspace(t, st, orgID, owner.ID, "Shared WS")

		err := st.WorkspaceAccess().Grant(ctx, store.GrantWorkspaceAccessParams{
			WorkspaceID: wsID,
			UserID:      viewer.ID,
		})
		require.NoError(t, err)

		has, err := st.WorkspaceAccess().HasAccess(ctx, store.HasWorkspaceAccessParams{
			WorkspaceID: wsID,
			UserID:      viewer.ID,
		})
		require.NoError(t, err)
		assert.True(t, has)
	})

	t.Run("has access false when not granted", func(t *testing.T) {
		st := s.NewStore(t)

		has, err := st.WorkspaceAccess().HasAccess(ctx, store.HasWorkspaceAccessParams{
			WorkspaceID: "no-ws",
			UserID:      "no-user",
		})
		require.NoError(t, err)
		assert.False(t, has)
	})

	t.Run("list by workspace id", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wa-org", false)
		owner := SeedUser(t, st, orgID, "wa-list-owner")
		v1 := SeedUser(t, st, orgID, "wa-v1")
		v2 := SeedUser(t, st, orgID, "wa-v2")
		wsID := SeedWorkspace(t, st, orgID, owner.ID, "List WS")

		for _, uid := range []string{v1.ID, v2.ID} {
			err := st.WorkspaceAccess().Grant(ctx, store.GrantWorkspaceAccessParams{
				WorkspaceID: wsID, UserID: uid,
			})
			require.NoError(t, err)
		}

		acls, err := st.WorkspaceAccess().ListByWorkspaceID(ctx, wsID)
		require.NoError(t, err)
		assert.Len(t, acls, 2)
	})

	t.Run("revoke", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wa-org", false)
		owner := SeedUser(t, st, orgID, "wa-rev-owner")
		viewer := SeedUser(t, st, orgID, "wa-rev-viewer")
		wsID := SeedWorkspace(t, st, orgID, owner.ID, "Revoke WS")

		err := st.WorkspaceAccess().Grant(ctx, store.GrantWorkspaceAccessParams{
			WorkspaceID: wsID, UserID: viewer.ID,
		})
		require.NoError(t, err)

		err = st.WorkspaceAccess().Revoke(ctx, store.RevokeWorkspaceAccessParams{
			WorkspaceID: wsID, UserID: viewer.ID,
		})
		require.NoError(t, err)

		has, err := st.WorkspaceAccess().HasAccess(ctx, store.HasWorkspaceAccessParams{
			WorkspaceID: wsID, UserID: viewer.ID,
		})
		require.NoError(t, err)
		assert.False(t, has)
	})

	t.Run("clear", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wa-org", false)
		owner := SeedUser(t, st, orgID, "wa-clear-owner")
		v1 := SeedUser(t, st, orgID, "wa-clear-v1")
		v2 := SeedUser(t, st, orgID, "wa-clear-v2")
		wsID := SeedWorkspace(t, st, orgID, owner.ID, "Clear WS")

		for _, uid := range []string{v1.ID, v2.ID} {
			err := st.WorkspaceAccess().Grant(ctx, store.GrantWorkspaceAccessParams{
				WorkspaceID: wsID, UserID: uid,
			})
			require.NoError(t, err)
		}

		err := st.WorkspaceAccess().Clear(ctx, wsID)
		require.NoError(t, err)

		acls, err := st.WorkspaceAccess().ListByWorkspaceID(ctx, wsID)
		require.NoError(t, err)
		require.NotNil(t, acls)
		assert.Empty(t, acls)
	})

	t.Run("has access non existent", func(t *testing.T) {
		st := s.NewStore(t)

		has, err := st.WorkspaceAccess().HasAccess(ctx, store.HasWorkspaceAccessParams{
			WorkspaceID: "nonexistent-ws",
			UserID:      "nonexistent-user",
		})
		require.NoError(t, err)
		assert.False(t, has)
	})

	t.Run("list empty", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wa-org", false)
		owner := SeedUser(t, st, orgID, "wa-listempty-owner")
		wsID := SeedWorkspace(t, st, orgID, owner.ID, "Empty ACL WS")

		acls, err := st.WorkspaceAccess().ListByWorkspaceID(ctx, wsID)
		require.NoError(t, err)
		require.NotNil(t, acls)
		assert.Empty(t, acls)
	})

	t.Run("clear empty", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wa-org", false)
		owner := SeedUser(t, st, orgID, "wa-clearempty-owner")
		wsID := SeedWorkspace(t, st, orgID, owner.ID, "No ACL WS")

		err := st.WorkspaceAccess().Clear(ctx, wsID)
		require.NoError(t, err)
	})

	t.Run("grant then revoke then has access", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wa-org", false)
		owner := SeedUser(t, st, orgID, "wa-lifecycle-owner")
		viewer := SeedUser(t, st, orgID, "wa-lifecycle-viewer")
		wsID := SeedWorkspace(t, st, orgID, owner.ID, "Lifecycle WS")

		// Grant access.
		err := st.WorkspaceAccess().Grant(ctx, store.GrantWorkspaceAccessParams{
			WorkspaceID: wsID, UserID: viewer.ID,
		})
		require.NoError(t, err)

		has, err := st.WorkspaceAccess().HasAccess(ctx, store.HasWorkspaceAccessParams{
			WorkspaceID: wsID, UserID: viewer.ID,
		})
		require.NoError(t, err)
		assert.True(t, has)

		// Revoke access.
		err = st.WorkspaceAccess().Revoke(ctx, store.RevokeWorkspaceAccessParams{
			WorkspaceID: wsID, UserID: viewer.ID,
		})
		require.NoError(t, err)

		has, err = st.WorkspaceAccess().HasAccess(ctx, store.HasWorkspaceAccessParams{
			WorkspaceID: wsID, UserID: viewer.ID,
		})
		require.NoError(t, err)
		assert.False(t, has)
	})

	t.Run("bulk grant", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wa-org", false)
		owner := SeedUser(t, st, orgID, "wa-bulk-owner")
		v1 := SeedUser(t, st, orgID, "wa-bulk-v1")
		v2 := SeedUser(t, st, orgID, "wa-bulk-v2")
		v3 := SeedUser(t, st, orgID, "wa-bulk-v3")
		wsID := SeedWorkspace(t, st, orgID, owner.ID, "Bulk Grant WS")

		err := st.WorkspaceAccess().BulkGrant(ctx, []store.GrantWorkspaceAccessParams{
			{WorkspaceID: wsID, UserID: v1.ID},
			{WorkspaceID: wsID, UserID: v2.ID},
			{WorkspaceID: wsID, UserID: v3.ID},
		})
		require.NoError(t, err)

		acls, err := st.WorkspaceAccess().ListByWorkspaceID(ctx, wsID)
		require.NoError(t, err)
		assert.Len(t, acls, 3)

		// Verify each user has access.
		for _, uid := range []string{v1.ID, v2.ID, v3.ID} {
			has, err := st.WorkspaceAccess().HasAccess(ctx, store.HasWorkspaceAccessParams{
				WorkspaceID: wsID, UserID: uid,
			})
			require.NoError(t, err)
			assert.True(t, has)
		}
	})

	t.Run("shared workspace appears in accessible list", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wa-org", false)
		owner := SeedUser(t, st, orgID, "wa-shared-owner")
		viewer := SeedUser(t, st, orgID, "wa-shared-viewer")
		wsID := SeedWorkspace(t, st, orgID, owner.ID, "Shared Visible")

		err := st.WorkspaceAccess().Grant(ctx, store.GrantWorkspaceAccessParams{
			WorkspaceID: wsID, UserID: viewer.ID,
		})
		require.NoError(t, err)

		// The viewer should see the shared workspace in their accessible list.
		workspaces, err := st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: viewer.ID,
			OrgID:  orgID,
		})
		require.NoError(t, err)
		require.Len(t, workspaces, 1)
		assert.Equal(t, wsID, workspaces[0].ID)
	})

	t.Run("duplicate workspace grant is idempotent", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wa-org", false)
		owner := SeedUser(t, st, orgID, "wa-idem-owner")
		viewer := SeedUser(t, st, orgID, "wa-idem-viewer")
		wsID := SeedWorkspace(t, st, orgID, owner.ID, "Idem WS")

		err := st.WorkspaceAccess().Grant(ctx, store.GrantWorkspaceAccessParams{
			WorkspaceID: wsID, UserID: viewer.ID,
		})
		require.NoError(t, err)

		// Second grant should not error.
		err = st.WorkspaceAccess().Grant(ctx, store.GrantWorkspaceAccessParams{
			WorkspaceID: wsID, UserID: viewer.ID,
		})
		require.NoError(t, err)

		// Should still have exactly one entry.
		acls, err := st.WorkspaceAccess().ListByWorkspaceID(ctx, wsID)
		require.NoError(t, err)
		assert.Len(t, acls, 1)
	})

	t.Run("bulk grant empty slice", func(t *testing.T) {
		st := s.NewStore(t)
		err := st.WorkspaceAccess().BulkGrant(ctx, nil)
		require.NoError(t, err)
	})
}
