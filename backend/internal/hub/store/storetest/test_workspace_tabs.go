package storetest

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testWorkspaceTabs(t *testing.T) {
	t.Run("upsert and list by workspace", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wt-org", false)
		user := SeedUser(t, st, orgID, "wt-user")
		worker := SeedWorker(t, st, user.ID)
		wsID := SeedWorkspace(t, st, orgID, user.ID, "Tab WS")

		tabID := id.Generate()
		err := st.WorkspaceTabs().Upsert(ctx, store.UpsertWorkspaceTabParams{
			WorkspaceID: wsID,
			WorkerID:    worker.ID,
			TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
			TabID:       tabID,
			Position:    "a0",
			TileID:      "tile-1",
		})
		require.NoError(t, err)

		tabs, err := st.WorkspaceTabs().ListByWorkspace(ctx, wsID)
		require.NoError(t, err)
		require.Len(t, tabs, 1)
		assert.Equal(t, wsID, tabs[0].WorkspaceID)
		assert.Equal(t, worker.ID, tabs[0].WorkerID)
		assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_AGENT, tabs[0].TabType)
		assert.Equal(t, tabID, tabs[0].TabID)
		assert.Equal(t, "a0", tabs[0].Position)
		assert.Equal(t, "tile-1", tabs[0].TileID)
	})

	t.Run("upsert overwrites existing", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wt-org", false)
		user := SeedUser(t, st, orgID, "wt-upsert-user")
		worker := SeedWorker(t, st, user.ID)
		wsID := SeedWorkspace(t, st, orgID, user.ID, "Upsert WS")

		tabID := id.Generate()
		for _, pos := range []string{"a0", "b0"} {
			err := st.WorkspaceTabs().Upsert(ctx, store.UpsertWorkspaceTabParams{
				WorkspaceID: wsID,
				WorkerID:    worker.ID,
				TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
				TabID:       tabID,
				Position:    pos,
				TileID:      "tile-1",
			})
			require.NoError(t, err)
		}

		tabs, err := st.WorkspaceTabs().ListByWorkspace(ctx, wsID)
		require.NoError(t, err)
		require.Len(t, tabs, 1)
		assert.Equal(t, "b0", tabs[0].Position)
	})

	t.Run("delete", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wt-org", false)
		user := SeedUser(t, st, orgID, "wt-del-user")
		worker := SeedWorker(t, st, user.ID)
		wsID := SeedWorkspace(t, st, orgID, user.ID, "Del Tab WS")

		tabID := id.Generate()
		err := st.WorkspaceTabs().Upsert(ctx, store.UpsertWorkspaceTabParams{
			WorkspaceID: wsID,
			WorkerID:    worker.ID,
			TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
			TabID:       tabID,
			Position:    "a0",
			TileID:      "tile-1",
		})
		require.NoError(t, err)

		err = st.WorkspaceTabs().Delete(ctx, store.DeleteWorkspaceTabParams{
			WorkspaceID: wsID,
			TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
			TabID:       tabID,
		})
		require.NoError(t, err)

		tabs, err := st.WorkspaceTabs().ListByWorkspace(ctx, wsID)
		require.NoError(t, err)
		require.NotNil(t, tabs)
		assert.Empty(t, tabs)
	})

	t.Run("delete by worker", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wt-org", false)
		user := SeedUser(t, st, orgID, "wt-dbw-user")
		worker := SeedWorker(t, st, user.ID)
		wsID := SeedWorkspace(t, st, orgID, user.ID, "DBW WS")

		for i := 0; i < 2; i++ {
			err := st.WorkspaceTabs().Upsert(ctx, store.UpsertWorkspaceTabParams{
				WorkspaceID: wsID,
				WorkerID:    worker.ID,
				TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
				TabID:       id.Generate(),
				Position:    "a0",
				TileID:      "tile-1",
			})
			require.NoError(t, err)
		}

		err := st.WorkspaceTabs().DeleteByWorker(ctx, worker.ID)
		require.NoError(t, err)

		tabs, err := st.WorkspaceTabs().ListByWorker(ctx, worker.ID)
		require.NoError(t, err)
		require.NotNil(t, tabs)
		assert.Empty(t, tabs)
	})

	t.Run("delete by workspace", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wt-org", false)
		user := SeedUser(t, st, orgID, "wt-dws-user")
		worker := SeedWorker(t, st, user.ID)
		wsID := SeedWorkspace(t, st, orgID, user.ID, "DWS WS")

		err := st.WorkspaceTabs().Upsert(ctx, store.UpsertWorkspaceTabParams{
			WorkspaceID: wsID,
			WorkerID:    worker.ID,
			TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
			TabID:       id.Generate(),
			Position:    "a0",
			TileID:      "tile-1",
		})
		require.NoError(t, err)

		err = st.WorkspaceTabs().DeleteByWorkspace(ctx, wsID)
		require.NoError(t, err)

		tabs, err := st.WorkspaceTabs().ListByWorkspace(ctx, wsID)
		require.NoError(t, err)
		require.NotNil(t, tabs)
		assert.Empty(t, tabs)
	})

	t.Run("delete worker tabs for workspace", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wt-org", false)
		user := SeedUser(t, st, orgID, "wt-dwtw-user")
		w1 := SeedWorker(t, st, user.ID)
		w2 := SeedWorker(t, st, user.ID)
		wsID := SeedWorkspace(t, st, orgID, user.ID, "DWTW WS")

		// Create tabs for two different workers in the same workspace.
		for _, wID := range []string{w1.ID, w2.ID} {
			err := st.WorkspaceTabs().Upsert(ctx, store.UpsertWorkspaceTabParams{
				WorkspaceID: wsID,
				WorkerID:    wID,
				TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
				TabID:       id.Generate(),
				Position:    "a0",
				TileID:      "tile-1",
			})
			require.NoError(t, err)
		}

		// Delete only w1's tabs for this workspace.
		err := st.WorkspaceTabs().DeleteWorkerTabsForWorkspace(ctx, store.DeleteWorkerTabsForWorkspaceParams{
			WorkerID:    w1.ID,
			WorkspaceID: wsID,
		})
		require.NoError(t, err)

		tabs, err := st.WorkspaceTabs().ListByWorkspace(ctx, wsID)
		require.NoError(t, err)
		require.Len(t, tabs, 1)
		assert.Equal(t, w2.ID, tabs[0].WorkerID)
	})

	t.Run("list by worker", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wt-org", false)
		user := SeedUser(t, st, orgID, "wt-lbw-user")
		worker := SeedWorker(t, st, user.ID)
		ws1 := SeedWorkspace(t, st, orgID, user.ID, "LBW WS 1")
		ws2 := SeedWorkspace(t, st, orgID, user.ID, "LBW WS 2")

		for _, wsID := range []string{ws1, ws2} {
			err := st.WorkspaceTabs().Upsert(ctx, store.UpsertWorkspaceTabParams{
				WorkspaceID: wsID,
				WorkerID:    worker.ID,
				TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
				TabID:       id.Generate(),
				Position:    "a0",
				TileID:      "tile-1",
			})
			require.NoError(t, err)
		}

		tabs, err := st.WorkspaceTabs().ListByWorker(ctx, worker.ID)
		require.NoError(t, err)
		assert.Len(t, tabs, 2)
	})

	t.Run("list distinct workers by workspace", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wt-org", false)
		user := SeedUser(t, st, orgID, "wt-ldw-user")
		w1 := SeedWorker(t, st, user.ID)
		w2 := SeedWorker(t, st, user.ID)
		wsID := SeedWorkspace(t, st, orgID, user.ID, "LDW WS")

		for _, wID := range []string{w1.ID, w2.ID} {
			err := st.WorkspaceTabs().Upsert(ctx, store.UpsertWorkspaceTabParams{
				WorkspaceID: wsID,
				WorkerID:    wID,
				TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
				TabID:       id.Generate(),
				Position:    "a0",
				TileID:      "tile-1",
			})
			require.NoError(t, err)
		}

		workerIDs, err := st.WorkspaceTabs().ListDistinctWorkersByWorkspace(ctx, wsID)
		require.NoError(t, err)
		assert.Len(t, workerIDs, 2)
		assert.Contains(t, workerIDs, w1.ID)
		assert.Contains(t, workerIDs, w2.ID)
	})

	t.Run("get max position", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wt-org", false)
		user := SeedUser(t, st, orgID, "wt-pos-user")
		worker := SeedWorker(t, st, user.ID)
		wsID := SeedWorkspace(t, st, orgID, user.ID, "Pos WS")

		// Empty workspace should return empty string.
		maxPos, err := st.WorkspaceTabs().GetMaxPosition(ctx, wsID)
		require.NoError(t, err)
		assert.Empty(t, maxPos)

		err = st.WorkspaceTabs().Upsert(ctx, store.UpsertWorkspaceTabParams{
			WorkspaceID: wsID,
			WorkerID:    worker.ID,
			TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
			TabID:       id.Generate(),
			Position:    "a0",
			TileID:      "tile-1",
		})
		require.NoError(t, err)

		err = st.WorkspaceTabs().Upsert(ctx, store.UpsertWorkspaceTabParams{
			WorkspaceID: wsID,
			WorkerID:    worker.ID,
			TabType:     leapmuxv1.TabType_TAB_TYPE_TERMINAL,
			TabID:       id.Generate(),
			Position:    "b0",
			TileID:      "tile-2",
		})
		require.NoError(t, err)

		maxPos, err = st.WorkspaceTabs().GetMaxPosition(ctx, wsID)
		require.NoError(t, err)
		assert.Equal(t, "b0", maxPos)
	})

	t.Run("list by workspace empty", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wt-org", false)
		user := SeedUser(t, st, orgID, "wt-listempty-user")
		wsID := SeedWorkspace(t, st, orgID, user.ID, "Empty Tabs WS")

		tabs, err := st.WorkspaceTabs().ListByWorkspace(ctx, wsID)
		require.NoError(t, err)
		require.NotNil(t, tabs)
		assert.Empty(t, tabs)
	})

	t.Run("list by worker empty", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wt-org", false)
		user := SeedUser(t, st, orgID, "wt-lbwempty-user")
		worker := SeedWorker(t, st, user.ID)

		tabs, err := st.WorkspaceTabs().ListByWorker(ctx, worker.ID)
		require.NoError(t, err)
		require.NotNil(t, tabs)
		assert.Empty(t, tabs)
	})

	t.Run("get max position empty", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wt-org", false)
		user := SeedUser(t, st, orgID, "wt-posempty-user")
		wsID := SeedWorkspace(t, st, orgID, user.ID, "Empty Pos WS")

		maxPos, err := st.WorkspaceTabs().GetMaxPosition(ctx, wsID)
		require.NoError(t, err)
		assert.Empty(t, maxPos)
	})

	t.Run("delete non existent", func(t *testing.T) {
		st := s.NewStore(t)

		err := st.WorkspaceTabs().Delete(ctx, store.DeleteWorkspaceTabParams{
			WorkspaceID: "nonexistent-ws",
			TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
			TabID:       "nonexistent-tab",
		})
		require.NoError(t, err)
	})

	t.Run("list distinct workers empty", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wt-org", false)
		user := SeedUser(t, st, orgID, "wt-ldwempty-user")
		wsID := SeedWorkspace(t, st, orgID, user.ID, "No Workers WS")

		workerIDs, err := st.WorkspaceTabs().ListDistinctWorkersByWorkspace(ctx, wsID)
		require.NoError(t, err)
		require.NotNil(t, workerIDs)
		assert.Empty(t, workerIDs)
	})

	t.Run("bulk upsert", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wt-org", false)
		user := SeedUser(t, st, orgID, "wt-bulk-user")
		worker := SeedWorker(t, st, user.ID)
		wsID := SeedWorkspace(t, st, orgID, user.ID, "Bulk Upsert WS")

		tab1 := id.Generate()
		tab2 := id.Generate()
		tab3 := id.Generate()

		err := st.WorkspaceTabs().BulkUpsert(ctx, []store.UpsertWorkspaceTabParams{
			{WorkspaceID: wsID, WorkerID: worker.ID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: tab1, Position: "a0", TileID: "tile-1"},
			{WorkspaceID: wsID, WorkerID: worker.ID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: tab2, Position: "b0", TileID: "tile-2"},
			{WorkspaceID: wsID, WorkerID: worker.ID, TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabID: tab3, Position: "c0", TileID: "tile-3"},
		})
		require.NoError(t, err)

		tabs, err := st.WorkspaceTabs().ListByWorkspace(ctx, wsID)
		require.NoError(t, err)
		assert.Len(t, tabs, 3)
	})

	t.Run("multiple tab types", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "wt-org", false)
		user := SeedUser(t, st, orgID, "wt-types-user")
		worker := SeedWorker(t, st, user.ID)
		wsID := SeedWorkspace(t, st, orgID, user.ID, "Types WS")

		agentTabID := id.Generate()
		termTabID := id.Generate()

		err := st.WorkspaceTabs().Upsert(ctx, store.UpsertWorkspaceTabParams{
			WorkspaceID: wsID,
			WorkerID:    worker.ID,
			TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
			TabID:       agentTabID,
			Position:    "a0",
			TileID:      "tile-1",
		})
		require.NoError(t, err)

		err = st.WorkspaceTabs().Upsert(ctx, store.UpsertWorkspaceTabParams{
			WorkspaceID: wsID,
			WorkerID:    worker.ID,
			TabType:     leapmuxv1.TabType_TAB_TYPE_TERMINAL,
			TabID:       termTabID,
			Position:    "b0",
			TileID:      "tile-2",
		})
		require.NoError(t, err)

		tabs, err := st.WorkspaceTabs().ListByWorkspace(ctx, wsID)
		require.NoError(t, err)
		assert.Len(t, tabs, 2)

		// Verify both tab types are present.
		tabTypes := make(map[leapmuxv1.TabType]bool)
		for _, tab := range tabs {
			tabTypes[tab.TabType] = true
		}
		assert.True(t, tabTypes[leapmuxv1.TabType_TAB_TYPE_AGENT])
		assert.True(t, tabTypes[leapmuxv1.TabType_TAB_TYPE_TERMINAL])
	})

	t.Run("bulk upsert empty slice", func(t *testing.T) {
		st := s.NewStore(t)
		err := st.WorkspaceTabs().BulkUpsert(ctx, nil)
		require.NoError(t, err)
	})
}
