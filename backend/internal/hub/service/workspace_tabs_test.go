package service_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
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

// renderedTabCollision seeds the one shape that makes the tab lookups'
// tenancy axis observable: two users whose rendered rows carry the SAME
// (workspace_id, tab_type, tab_id).
//
// That is legal, not contrived. workspace_tab_rendered is keyed
// (user_id, tab_id) and its workspace_id is a plain FK, so any user's row may
// name any existing workspace, and a tab id is unique only within one user.
// The stranger's row is inserted FIRST so a lookup that resolves by
// index order rather than by owner reaches it.
//
// Returns the workspace the owner owns, plus each side's tile id -- the tile
// is what distinguishes the two rows in an assertion.
func renderedTabCollision(t *testing.T, st store.Store) (ownerID, strangerID, wsID, ownerTile, strangerTile string) {
	t.Helper()
	ctx := context.Background()

	owner := storetest.SeedUser(t, st, "tabs-owner")
	stranger := storetest.SeedUser(t, st, "tabs-stranger")
	wsID = storetest.SeedWorkspace(t, st, owner.ID, "Owned")
	ownerWorker := storetest.SeedWorker(t, st, owner.ID)
	strangerWorker := storetest.SeedWorker(t, st, stranger.ID)

	const tabID = "shared-tab-id"
	for _, row := range []store.UpsertRenderedTabParams{
		{
			UserID: userid.MustNew(stranger.ID), WorkspaceID: wsID,
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: tabID,
			WorkerID: strangerWorker.ID, TileID: "tile-stranger", Position: "a0",
		},
		{
			UserID: userid.MustNew(owner.ID), WorkspaceID: wsID,
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: tabID,
			WorkerID: ownerWorker.ID, TileID: "tile-owner", Position: "a1",
		},
	} {
		require.NoError(t, st.WorkspaceTabIndex().UpsertRendered(ctx, row))
	}
	return owner.ID, stranger.ID, wsID, "tile-owner", "tile-stranger"
}

// TestWorkspaceService_GetTab_BindsTheCallerAsOwner pins the service half of
// the rendered-tab tenancy fix. The store query is owner-scoped, so a storetest
// passes no matter WHICH owner the service binds; only a test at this layer
// catches GetTab binding the wrong id -- or dropping the binding when the
// params struct next changes shape.
//
// Proving the caller owns the workspace is not the same as proving the ROW
// does, which is exactly why the caller's own id has to reach the query.
func TestWorkspaceService_GetTab_BindsTheCallerAsOwner(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	ownerID, _, wsID, ownerTile, strangerTile := renderedTabCollision(t, st)

	svc := service.NewWorkspaceService(st, nil, noopWorkspaceChannelCloser{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: userid.MustNew(ownerID)})

	resp, err := svc.GetTab(ctx, connect.NewRequest(&leapmuxv1.GetTabRequest{
		WorkspaceId: wsID,
		TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabId:       "shared-tab-id",
	}))
	require.NoError(t, err)
	assert.Equal(t, ownerTile, resp.Msg.GetTab().GetTileId(),
		"GetTab must answer from the caller's own row")
	assert.NotEqual(t, strangerTile, resp.Msg.GetTab().GetTileId(),
		"a row owned by another user must never satisfy this lookup, even in a workspace the caller owns")
}

// TestWorkspaceService_LocateTab_BindsTheCallerAsOwner is the same pin for the
// workspace-less lookup. LocateTab searches by (tab_type, tab_id) alone, so the
// owner binding is the ONLY thing separating the two colliding rows here -- and
// this is the path `leapmux remote` uses to expand an env-injected tab id.
func TestWorkspaceService_LocateTab_BindsTheCallerAsOwner(t *testing.T) {
	ctx := context.Background()
	st := hubtestutil.OpenTestStore(t)

	// Each user owns their own workspace and both hold a row under the SAME tab
	// id -- the collision LocateTab must resolve by owner. They cannot share one
	// workspace here: LocateAccessibleRendered searches only workspaces the
	// caller can access, so a row parked in someone else's workspace is
	// correctly unreachable, and one user cannot hold two rows under one tab id
	// because (user_id, tab_id) is the primary key.
	owner := storetest.SeedUser(t, st, "locate-owner")
	stranger := storetest.SeedUser(t, st, "locate-stranger")
	ownerWS := storetest.SeedWorkspace(t, st, owner.ID, "Owner WS")
	strangerWS := storetest.SeedWorkspace(t, st, stranger.ID, "Stranger WS")
	ownerWorker := storetest.SeedWorker(t, st, owner.ID)
	strangerWorker := storetest.SeedWorker(t, st, stranger.ID)

	ownerID, strangerID := owner.ID, stranger.ID
	ownerTile, strangerTile := "tile-owner", "tile-stranger"
	for _, row := range []store.UpsertRenderedTabParams{
		{
			UserID: userid.MustNew(strangerID), WorkspaceID: strangerWS,
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "shared-tab-id",
			WorkerID: strangerWorker.ID, TileID: strangerTile, Position: "a0",
		},
		{
			UserID: userid.MustNew(ownerID), WorkspaceID: ownerWS,
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "shared-tab-id",
			WorkerID: ownerWorker.ID, TileID: ownerTile, Position: "a1",
		},
	} {
		require.NoError(t, st.WorkspaceTabIndex().UpsertRendered(ctx, row))
	}

	svc := service.NewWorkspaceService(st, nil, noopWorkspaceChannelCloser{})
	locate := func(userID string) *connect.Response[leapmuxv1.LocateTabResponse] {
		t.Helper()
		ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: userid.MustNew(userID)})
		resp, err := svc.LocateTab(ctx, connect.NewRequest(&leapmuxv1.LocateTabRequest{
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
			TabId:   "shared-tab-id",
		}))
		require.NoError(t, err)
		return resp
	}

	assert.Equal(t, ownerTile, locate(ownerID).Msg.GetTab().GetTileId(),
		"the owner must locate their own row")

	// The control that stops this being a one-sided assertion: the stranger
	// reaches THEIR row, not an error and not the owner's. Both rows are live
	// and share a key, so each side must be resolved by owner alone.
	assert.Equal(t, strangerTile, locate(strangerID).Msg.GetTab().GetTileId(),
		"the stranger must locate their own row, not the owner's")
}
