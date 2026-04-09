package storetest

import (
	"testing"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testWorkspaceLayouts(t *testing.T) {
	t.Run("upsert and get", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wl-org", false)
		user := SeedUser(t, st, orgID, "wl-user")
		wsID := SeedWorkspace(t, st, orgID, user.ID, "Layout WS")

		err := st.WorkspaceLayouts().Upsert(ctx, store.UpsertWorkspaceLayoutParams{
			WorkspaceID: wsID,
			LayoutJSON:  `{"type":"horizontal","children":[]}`,
		})
		require.NoError(t, err)

		layout, err := st.WorkspaceLayouts().Get(ctx, wsID)
		require.NoError(t, err)
		assert.Equal(t, wsID, layout.WorkspaceID)
		assert.Equal(t, `{"type":"horizontal","children":[]}`, layout.LayoutJSON)
		assert.False(t, layout.UpdatedAt.IsZero())
	})

	t.Run("get not found", func(t *testing.T) {
		st := s.NewStore(t)
		_, err := st.WorkspaceLayouts().Get(ctx, "nonexistent")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("upsert overwrites", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wl-org", false)
		user := SeedUser(t, st, orgID, "wl-overwrite-user")
		wsID := SeedWorkspace(t, st, orgID, user.ID, "Overwrite WS")

		err := st.WorkspaceLayouts().Upsert(ctx, store.UpsertWorkspaceLayoutParams{
			WorkspaceID: wsID,
			LayoutJSON:  `{"v":1}`,
		})
		require.NoError(t, err)

		err = st.WorkspaceLayouts().Upsert(ctx, store.UpsertWorkspaceLayoutParams{
			WorkspaceID: wsID,
			LayoutJSON:  `{"v":2}`,
		})
		require.NoError(t, err)

		layout, err := st.WorkspaceLayouts().Get(ctx, wsID)
		require.NoError(t, err)
		assert.Equal(t, `{"v":2}`, layout.LayoutJSON)
	})

	t.Run("delete", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wl-org", false)
		user := SeedUser(t, st, orgID, "wl-del-user")
		wsID := SeedWorkspace(t, st, orgID, user.ID, "Del Layout WS")

		err := st.WorkspaceLayouts().Upsert(ctx, store.UpsertWorkspaceLayoutParams{
			WorkspaceID: wsID,
			LayoutJSON:  `{}`,
		})
		require.NoError(t, err)

		err = st.WorkspaceLayouts().Delete(ctx, wsID)
		require.NoError(t, err)

		_, err = st.WorkspaceLayouts().Get(ctx, wsID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("upsert empty json", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wl-org", false)
		user := SeedUser(t, st, orgID, "wl-empty-user")
		wsID := SeedWorkspace(t, st, orgID, user.ID, "Empty JSON WS")

		err := st.WorkspaceLayouts().Upsert(ctx, store.UpsertWorkspaceLayoutParams{
			WorkspaceID: wsID,
			LayoutJSON:  "",
		})
		require.NoError(t, err)

		layout, err := st.WorkspaceLayouts().Get(ctx, wsID)
		require.NoError(t, err)
		assert.Equal(t, "", layout.LayoutJSON)
	})

	t.Run("delete non existent", func(t *testing.T) {
		st := s.NewStore(t)

		err := st.WorkspaceLayouts().Delete(ctx, "nonexistent-ws")
		require.NoError(t, err)
	})
}
