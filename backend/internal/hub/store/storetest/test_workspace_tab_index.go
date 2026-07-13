package storetest

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testWorkspaceTabIndex covers the bulk read and write paths of
// WorkspaceTabIndexStore. The per-row write side is also exercised
// indirectly by the manager-integration suite; here we focus on the
// bulk read (cross-workspace list) and bulk write (upsert / delete)
// surfaces.
func (s *Suite) testWorkspaceTabIndex(t *testing.T) {
	t.Run("bulk upsert and delete owned and rendered", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "bulk-tabidx-org")
		user := SeedUser(t, st, orgID, "bulk-tabidx-user")
		worker := SeedWorker(t, st, user.ID)
		wsA := SeedWorkspace(t, st, orgID, user.ID, "A")
		wsB := SeedWorkspace(t, st, orgID, user.ID, "B")

		ownedRows := []store.UpsertOwnedTabParams{
			{OrgID: orgID, WorkspaceID: wsA, WorkerID: worker.ID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "o1", TileID: "tile-a", Position: "a0"},
			{OrgID: orgID, WorkspaceID: wsA, WorkerID: worker.ID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "o2", TileID: "tile-a", Position: "a1"},
			{OrgID: orgID, WorkspaceID: wsB, WorkerID: worker.ID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "o3", TileID: "tile-b", Position: "b0"},
		}
		require.NoError(t, st.WorkspaceTabIndex().BulkUpsertOwned(ctx, ownedRows))

		// Verify all three rows landed.
		got, err := st.WorkspaceTabIndex().ListOwnedByWorkspace(ctx, wsA)
		require.NoError(t, err)
		assert.Len(t, got, 2)
		got, err = st.WorkspaceTabIndex().ListOwnedByWorkspace(ctx, wsB)
		require.NoError(t, err)
		assert.Len(t, got, 1)

		// Bulk upsert again with one row's position changed: the
		// conflict path must fire and update in place rather than
		// erroring on the duplicate primary key.
		ownedRows[0].Position = "a-updated"
		require.NoError(t, st.WorkspaceTabIndex().BulkUpsertOwned(ctx, ownedRows))
		row, err := st.WorkspaceTabIndex().GetOwned(ctx, store.GetOwnedTabParams{WorkspaceID: wsA, TabID: "o1"})
		require.NoError(t, err)
		assert.Equal(t, "a-updated", row.Position)

		// Same set, but for the rendered view.
		renderedRows := []store.UpsertRenderedTabParams{
			{OrgID: orgID, WorkspaceID: wsA, WorkerID: worker.ID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "r1", TileID: "tile-a", Position: "a0"},
			{OrgID: orgID, WorkspaceID: wsB, WorkerID: worker.ID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "r2", TileID: "tile-b", Position: "b0"},
		}
		require.NoError(t, st.WorkspaceTabIndex().BulkUpsertRendered(ctx, renderedRows))
		gotRendered, err := st.WorkspaceTabIndex().ListRenderedByWorkspaceIDs(ctx, []string{wsA, wsB})
		require.NoError(t, err)
		assert.Len(t, gotRendered, 2)

		// Bulk delete a subset of owned rows: only o1 and o3 should
		// remain after the call (o2 is deleted).
		require.NoError(t, st.WorkspaceTabIndex().BulkDeleteOwned(ctx, []store.TabIndexKey{
			{OrgID: orgID, TabID: "o2"},
		}))
		got, err = st.WorkspaceTabIndex().ListOwnedByWorkspace(ctx, wsA)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "o1", got[0].TabID)

		// Bulk delete every rendered row in one call: both r1 and
		// r2 should be gone.
		require.NoError(t, st.WorkspaceTabIndex().BulkDeleteRendered(ctx, []store.TabIndexKey{
			{OrgID: orgID, TabID: "r1"},
			{OrgID: orgID, TabID: "r2"},
		}))
		gotRendered, err = st.WorkspaceTabIndex().ListRenderedByWorkspaceIDs(ctx, []string{wsA, wsB})
		require.NoError(t, err)
		assert.Empty(t, gotRendered)

		// Empty inputs must be no-ops, not errors.
		assert.NoError(t, st.WorkspaceTabIndex().BulkUpsertOwned(ctx, nil))
		assert.NoError(t, st.WorkspaceTabIndex().BulkUpsertRendered(ctx, nil))
		assert.NoError(t, st.WorkspaceTabIndex().BulkDeleteOwned(ctx, nil))
		assert.NoError(t, st.WorkspaceTabIndex().BulkDeleteRendered(ctx, nil))
	})

	t.Run("list rendered by workspace ids", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "tabidx-org")
		user := SeedUser(t, st, orgID, "tabidx-user")
		worker := SeedWorker(t, st, user.ID)
		wsA := SeedWorkspace(t, st, orgID, user.ID, "A")
		wsB := SeedWorkspace(t, st, orgID, user.ID, "B")
		wsUnreferenced := SeedWorkspace(t, st, orgID, user.ID, "Unreferenced")

		// Two tabs in A, one in B.
		require.NoError(t, st.WorkspaceTabIndex().UpsertRendered(ctx, store.UpsertRenderedTabParams{
			OrgID: orgID, WorkspaceID: wsA, WorkerID: worker.ID,
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "a1",
			TileID: "tile-a", Position: "a0",
		}))
		require.NoError(t, st.WorkspaceTabIndex().UpsertRendered(ctx, store.UpsertRenderedTabParams{
			OrgID: orgID, WorkspaceID: wsA, WorkerID: worker.ID,
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "a2",
			TileID: "tile-a", Position: "a1",
		}))
		require.NoError(t, st.WorkspaceTabIndex().UpsertRendered(ctx, store.UpsertRenderedTabParams{
			OrgID: orgID, WorkspaceID: wsB, WorkerID: worker.ID,
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "b1",
			TileID: "tile-b", Position: "b0",
		}))

		// Empty input: nil with no DB call.
		got, err := st.WorkspaceTabIndex().ListRenderedByWorkspaceIDs(ctx, nil)
		require.NoError(t, err)
		assert.Empty(t, got)

		// Single id behaves like the per-workspace variant.
		got, err = st.WorkspaceTabIndex().ListRenderedByWorkspaceIDs(ctx, []string{wsA})
		require.NoError(t, err)
		assert.Len(t, got, 2)
		for _, row := range got {
			assert.Equal(t, wsA, row.WorkspaceID)
		}

		// Multi-id: result groups by workspace_id then position. Missing
		// ids are silently dropped; empty workspaces return nothing.
		got, err = st.WorkspaceTabIndex().ListRenderedByWorkspaceIDs(ctx, []string{wsA, wsB, wsUnreferenced, "missing"})
		require.NoError(t, err)
		require.Len(t, got, 3)
		byWS := map[string][]string{}
		for _, row := range got {
			byWS[row.WorkspaceID] = append(byWS[row.WorkspaceID], row.TabID)
		}
		assert.ElementsMatch(t, []string{"a1", "a2"}, byWS[wsA])
		assert.ElementsMatch(t, []string{"b1"}, byWS[wsB])
		assert.Empty(t, byWS[wsUnreferenced])
		assert.Empty(t, byWS["missing"])
	})

	t.Run("locate accessible rendered is owner-only", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "locate-tabidx-org")
		owner := SeedUser(t, st, orgID, "locate-owner")
		other := SeedUser(t, st, orgID, "locate-other")
		worker := SeedWorker(t, st, owner.ID)
		wsID := SeedWorkspace(t, st, orgID, owner.ID, "Locate WS")
		require.NoError(t, st.WorkspaceTabIndex().UpsertRendered(ctx, store.UpsertRenderedTabParams{
			OrgID: orgID, WorkspaceID: wsID, WorkerID: worker.ID,
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "loc1", TileID: "tile", Position: "a0",
		}))

		// The owner locates the tab.
		row, err := st.WorkspaceTabIndex().LocateAccessibleRendered(ctx, store.LocateAccessibleRenderedTabParams{
			TabID: "loc1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, UserID: owner.ID,
		})
		require.NoError(t, err)
		assert.Equal(t, wsID, row.WorkspaceID)

		// A non-owner -- even in the same org -- gets ErrNotFound.
		_, err = st.WorkspaceTabIndex().LocateAccessibleRendered(ctx, store.LocateAccessibleRenderedTabParams{
			TabID: "loc1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, UserID: other.ID,
		})
		assert.ErrorIs(t, err, store.ErrNotFound, "locate must be owner-only")

		// The owner cannot locate a tab in a soft-deleted workspace.
		_, err = st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{ID: wsID, OwnerUserID: owner.ID})
		require.NoError(t, err)
		_, err = st.WorkspaceTabIndex().LocateAccessibleRendered(ctx, store.LocateAccessibleRenderedTabParams{
			TabID: "loc1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, UserID: owner.ID,
		})
		assert.ErrorIs(t, err, store.ErrNotFound, "a soft-deleted workspace's tabs are unreachable")
	})
}
