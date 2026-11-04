package service_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// renderedTabCollision seeds the shape that makes the tab lookups' tenancy
// axis observable: two users whose rendered rows carry the SAME
// (tab_type, tab_id), each in a workspace of their own.
//
// That is legal, not contrived: a tab id is unique only within one user. The
// composite workspace key keeps each row inside its own user's workspace, so
// the two rows cannot share one. The stranger's row is inserted FIRST so a
// lookup that resolves by index order rather than by owner reaches it.
//
// Returns each side's workspace and tile id -- the tile is what distinguishes
// the two rows in an assertion.
func renderedTabCollision(t *testing.T, st store.Store) (ownerID, strangerID, ownerWS, strangerWS, ownerTile, strangerTile string) {
	t.Helper()
	ctx := context.Background()

	owner := storetest.SeedUser(t, st, "tabs-owner")
	stranger := storetest.SeedUser(t, st, "tabs-stranger")
	ownerWS = storetest.SeedWorkspace(t, st, owner.ID, "Owned")
	strangerWS = storetest.SeedWorkspace(t, st, stranger.ID, "Stranger's")
	ownerWorker := storetest.SeedWorker(t, st, owner.ID)
	strangerWorker := storetest.SeedWorker(t, st, stranger.ID)

	const tabID = "shared-tab-id"
	for _, row := range []store.UpsertTabParams{
		{
			UserID: userid.MustNew(stranger.ID), WorkspaceID: strangerWS,
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: tabID,
			WorkerID: strangerWorker.ID, TileID: "tile-stranger", Position: "a0",
			Rendered: true,
		},
		{
			UserID: userid.MustNew(owner.ID), WorkspaceID: ownerWS,
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: tabID,
			WorkerID: ownerWorker.ID, TileID: "tile-owner", Position: "a1",
			Rendered: true,
		},
	} {
		require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, row))
	}
	return owner.ID, stranger.ID, ownerWS, strangerWS, "tile-owner", "tile-stranger"
}

// TestWorkspaceService_LocateTab_BindsTheCallerAsOwner pins the service half of
// the rendered-tab tenancy rule. The store query is owner-scoped, so a
// storetest passes no matter WHICH owner the service binds; only a test at this
// layer catches LocateTab binding the wrong id. LocateTab searches by
// (tab_type, tab_id) alone, so the owner binding is the ONLY thing separating
// the two colliding rows here -- and this is the path `leapmux control` uses to
// expand an env-injected tab id.
func TestWorkspaceService_LocateTab_BindsTheCallerAsOwner(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	ownerID, strangerID, ownerWS, strangerWS, ownerTile, strangerTile := renderedTabCollision(t, st)

	svc := service.NewWorkspaceService(st, nil, nil)
	locate := func(userID string) *leapmuxv1.LocateTabResponse {
		t.Helper()
		ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: userid.MustNew(userID)})
		resp, err := svc.LocateTab(ctx, &leapmuxv1.LocateTabRequest{
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
			TabId:   "shared-tab-id",
		})
		require.NoError(t, err)
		return resp
	}

	located := locate(ownerID).GetTab()
	assert.Equal(t, ownerTile, located.GetTileId(), "the owner must locate their own row")
	assert.Equal(t, ownerWS, located.GetWorkspaceId())
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_AGENT, located.GetTab().GetTabType(), "the reply identifies the tab")
	assert.Equal(t, "shared-tab-id", located.GetTab().GetTabId())

	// The control that stops this being a one-sided assertion: the stranger
	// reaches THEIR row, not an error and not the owner's. Both rows are live
	// and share a key, so each side must be resolved by owner alone.
	located = locate(strangerID).GetTab()
	assert.Equal(t, strangerTile, located.GetTileId(), "the stranger must locate their own row, not the owner's")
	assert.Equal(t, strangerWS, located.GetWorkspaceId())
}

// TestWorkspaceService_ListTabs_ListsOnlyTheCallersRows pins the tenancy of the
// listing that replaced GetTab. Both users hold a rendered row under the same
// (tab_type, tab_id), so only the owner binding separates them. A workspace the
// caller does not own is dropped from the request rather than read.
func TestWorkspaceService_ListTabs_ListsOnlyTheCallersRows(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	ownerID, strangerID, ownerWS, strangerWS, ownerTile, strangerTile := renderedTabCollision(t, st)
	svc := service.NewWorkspaceService(st, nil, nil)
	list := func(userID string, workspaceIDs ...string) []*leapmuxv1.WorkspaceTab {
		t.Helper()
		ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: userid.MustNew(userID)})
		resp, err := svc.ListTabs(ctx, &leapmuxv1.ListTabsRequest{WorkspaceIds: workspaceIDs})
		require.NoError(t, err)
		return resp.GetTabs()
	}

	for name, workspaceIDs := range map[string][]string{
		"the owner's workspace":          {ownerWS},
		"every workspace":                nil,
		"the owner's and another user's": {ownerWS, strangerWS},
	} {
		t.Run("the owner lists "+name, func(t *testing.T) {
			tabs := list(ownerID, workspaceIDs...)
			require.Len(t, tabs, 1, "another user's row with the same tab id must not be listed")
			assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_AGENT, tabs[0].GetTab().GetTabType())
			assert.Equal(t, "shared-tab-id", tabs[0].GetTab().GetTabId())
			assert.Equal(t, ownerTile, tabs[0].GetTileId())
			assert.Equal(t, ownerWS, tabs[0].GetWorkspaceId())
		})
	}

	assert.Empty(t, list(strangerID, ownerWS), "a workspace the caller does not own lists nothing")
	strangers := list(strangerID)
	require.Len(t, strangers, 1)
	assert.Equal(t, strangerTile, strangers[0].GetTileId(), "the stranger lists their own row, not the owner's")
}

// A caller with no workspace has no tab to list, and the reply says so with
// an empty list rather than an error.
func TestWorkspaceService_ListTabs_ListsNothingForACallerWithNoWorkspace(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "tabs-nobody")
	svc := service.NewWorkspaceService(st, nil, nil)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: userid.MustNew(user.ID)})

	resp, err := svc.ListTabs(ctx, &leapmuxv1.ListTabsRequest{})
	require.NoError(t, err)
	assert.Empty(t, resp.GetTabs())
}
