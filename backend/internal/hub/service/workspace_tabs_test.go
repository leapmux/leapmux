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

// TestWorkspaceService_GetTab_BindsTheCallerAsOwner pins the service half of
// the rendered-tab tenancy rule. The store query is owner-scoped, so a
// storetest passes no matter WHICH owner the service binds; only a test at
// this layer catches GetTab binding the wrong id -- or dropping the binding
// when the params struct next changes shape.
func TestWorkspaceService_GetTab_BindsTheCallerAsOwner(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	ownerID, strangerID, ownerWS, strangerWS, ownerTile, strangerTile := renderedTabCollision(t, st)

	svc := service.NewWorkspaceService(st, nil, nil)
	getTab := func(userID, workspaceID string) (*leapmuxv1.GetTabResponse, error) {
		ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: userid.MustNew(userID)})
		return svc.GetTab(ctx, &leapmuxv1.GetTabRequest{
			WorkspaceId: workspaceID,
			TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
			TabId:       "shared-tab-id",
		})
	}

	resp, err := getTab(ownerID, ownerWS)
	require.NoError(t, err)
	assert.Equal(t, ownerTile, resp.GetTab().GetTileId(),
		"GetTab must answer from the caller's own row")

	resp, err = getTab(strangerID, strangerWS)
	require.NoError(t, err)
	assert.Equal(t, strangerTile, resp.GetTab().GetTileId(),
		"control: the stranger reaches their own row in their own workspace")

	_, err = getTab(strangerID, ownerWS)
	require.Error(t, err, "a caller who does not own the workspace must not read its tabs")
}

// TestWorkspaceService_LocateTab_BindsTheCallerAsOwner is the same pin for the
// workspace-less lookup. LocateTab searches by (tab_type, tab_id) alone, so the
// owner binding is the ONLY thing separating the two colliding rows here -- and
// this is the path `leapmux control` uses to expand an env-injected tab id.
func TestWorkspaceService_LocateTab_BindsTheCallerAsOwner(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := hubtestutil.OpenTestStore(t)

	// Each user owns their own workspace and both hold a row under the SAME tab
	// id -- the collision LocateTab must resolve by owner. They cannot share one
	// workspace: the composite workspace key ties each row to a workspace its
	// own user owns, and one user cannot hold two rows under one tab id because
	// (user_id, tab_id) is the primary key.
	owner := storetest.SeedUser(t, st, "locate-owner")
	stranger := storetest.SeedUser(t, st, "locate-stranger")
	ownerWS := storetest.SeedWorkspace(t, st, owner.ID, "Owner WS")
	strangerWS := storetest.SeedWorkspace(t, st, stranger.ID, "Stranger WS")
	ownerWorker := storetest.SeedWorker(t, st, owner.ID)
	strangerWorker := storetest.SeedWorker(t, st, stranger.ID)

	ownerID, strangerID := owner.ID, stranger.ID
	ownerTile, strangerTile := "tile-owner", "tile-stranger"
	for _, row := range []store.UpsertTabParams{
		{
			UserID: userid.MustNew(strangerID), WorkspaceID: strangerWS,
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "shared-tab-id",
			WorkerID: strangerWorker.ID, TileID: strangerTile, Position: "a0",
			Rendered: true,
		},
		{
			UserID: userid.MustNew(ownerID), WorkspaceID: ownerWS,
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "shared-tab-id",
			WorkerID: ownerWorker.ID, TileID: ownerTile, Position: "a1",
			Rendered: true,
		},
	} {
		require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, row))
	}

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

	assert.Equal(t, ownerTile, locate(ownerID).GetTab().GetTileId(),
		"the owner must locate their own row")

	// The control that stops this being a one-sided assertion: the stranger
	// reaches THEIR row, not an error and not the owner's. Both rows are live
	// and share a key, so each side must be resolved by owner alone.
	assert.Equal(t, strangerTile, locate(strangerID).GetTab().GetTileId(),
		"the stranger must locate their own row, not the owner's")
}
