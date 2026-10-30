package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// TestListOwnedTabsForWorker_ScopedToRegistrant pins both halves of the
// worker-reconciliation contract.
//
// workspace_tab_owned is keyed by (user_id, tab_id) and nothing ties a row's
// owner to the registrant of the worker it names, so two owners' rows can
// legitimately reference one worker (each row sits in a workspace its own
// user owns, which the composite workspace key requires). The response must
// carry only the registrant's rows AND announce that scope: the reconciler on
// the other end reaps every LOCAL row the list omits, so an unannounced
// narrowing would tell it to destroy every other owner's live file tabs.
func TestListOwnedTabsForWorker_ScopedToRegistrant(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	ctx := context.Background()
	userA := storetest.SeedUser(t, st, "reconcile-alice")
	userB := storetest.SeedUser(t, st, "reconcile-bob")
	worker := storetest.SeedWorker(t, st, userA.ID)
	wsA := storetest.SeedWorkspace(t, st, userA.ID, "alice ws")
	wsB := storetest.SeedWorkspace(t, st, userB.ID, "bob ws")

	// Both owners hold a FILE tab with the SAME client-minted id on alice's
	// worker, each in a workspace of their own. Every part of that is
	// schema-legal.
	const sharedTabID = "file-1700000000000-1"
	require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, store.UpsertTabParams{
		UserID: userid.MustNew(userB.ID), WorkspaceID: wsB, WorkerID: worker.ID,
		TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabID: sharedTabID, TileID: "tile-b", Position: "b0",
		Rendered: false,
	}))
	require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, store.UpsertTabParams{
		UserID: userid.MustNew(userA.ID), WorkspaceID: wsA, WorkerID: worker.ID,
		TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabID: sharedTabID, TileID: "tile-a", Position: "a0",
		Rendered: false,
	}))

	req := &leapmuxv1.ListOwnedTabsForWorkerRequest{}
	// The bearer rides the request header on the server CallInfo, so the call
	// runs through callUnderServerInfo with the header a transport would
	// deliver.
	var resp *leapmuxv1.ListOwnedTabsForWorkerResponse
	_, callErr := callUnderServerInfo(ctx, func(callCtx context.Context) error {
		info, _ := connect.CallInfoForServerContext(callCtx)
		info.RequestHeader().Set("Authorization", "Bearer "+worker.AuthToken)
		var err error
		resp, err = service.NewWorkerReconcilerService(st).ListOwnedTabsForWorker(callCtx, req)
		return err
	})
	require.NoError(t, callErr)

	require.Len(t, resp.GetTabStates(), 1, "a foreign owner's row must not be listed")
	got := resp.GetTabStates()[0]
	assert.Equal(t, userA.ID, got.GetUserId())
	assert.Equal(t, sharedTabID, got.GetTabId())
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_FILE, got.GetTabType(),
		"alice's row, not bob's identically-named one")
	assert.Equal(t, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ACTIVE, got.GetArchiveState())
	assert.Equal(t, userA.ID, resp.GetOwnerUserId(),
		"the response must declare the owner it is authoritative for")
}

// TestListOwnedTabsForWorker_ReportsADeletedWorkspaceArchived pins the window
// between DeleteWorkspace and the lifecycle drain. The delete soft-deletes the
// workspace and drops its placement in one transaction, while the tab rows
// stay until the drain tombstones the tabs. A reconcile pass in that window
// must read ARCHIVED: ACTIVE cleared the archive flag of the workspace's tabs
// and resumed their agents just before the tabs closed.
func TestListOwnedTabsForWorker_ReportsADeletedWorkspaceArchived(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	ctx := context.Background()
	owner := storetest.SeedUser(t, st, "reconcile-deleted-owner")
	worker := storetest.SeedWorker(t, st, owner.ID)
	workspaceID := storetest.SeedWorkspace(t, st, owner.ID, "deleted")
	ownerID := userid.MustNew(owner.ID)
	require.NoError(t, st.WorkspaceSections().Create(ctx, store.CreateWorkspaceSectionParams{
		ID: "archived-section", UserID: ownerID, Name: "Archived", Position: "z",
		SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED,
		Sidebar:     leapmuxv1.Sidebar_SIDEBAR_LEFT,
	}))
	require.NoError(t, st.WorkspaceSectionItems().Set(ctx, store.SetWorkspaceSectionItemParams{
		UserID: ownerID, WorkspaceID: workspaceID, SectionID: "archived-section", Position: "a",
	}))
	require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, store.UpsertTabParams{
		UserID: ownerID, WorkspaceID: workspaceID, WorkerID: worker.ID,
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "deleted-agent", TileID: "tile", Position: "a",
		Rendered: false,
	}))
	// The store half of DeleteWorkspace's transaction, with no lifecycle drain.
	require.NoError(t, st.RunInTransaction(ctx, func(tx store.Store) error {
		deleted, err := tx.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{ID: workspaceID, OwnerUserID: ownerID})
		if err != nil {
			return err
		}
		require.Equal(t, int64(1), deleted)
		return tx.WorkspaceSectionItems().Delete(ctx, store.DeleteWorkspaceSectionItemParams{
			UserID: ownerID, WorkspaceID: workspaceID,
		})
	}))

	req := &leapmuxv1.ListOwnedTabsForWorkerRequest{}
	var resp *leapmuxv1.ListOwnedTabsForWorkerResponse
	_, callErr := callUnderServerInfo(ctx, func(callCtx context.Context) error {
		info, _ := connect.CallInfoForServerContext(callCtx)
		info.RequestHeader().Set("Authorization", "Bearer "+worker.AuthToken)
		var err error
		resp, err = service.NewWorkerReconcilerService(st).ListOwnedTabsForWorker(callCtx, req)
		return err
	})
	require.NoError(t, callErr)
	require.Len(t, resp.GetTabStates(), 1, "the tab row stays until the lifecycle drain tombstones it")
	assert.Equal(t, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED,
		resp.GetTabStates()[0].GetArchiveState())
}

func TestListOwnedTabsForWorker_ReturnsArchivedState(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	ctx := context.Background()
	owner := storetest.SeedUser(t, st, "reconcile-archived-owner")
	worker := storetest.SeedWorker(t, st, owner.ID)
	workspaceID := storetest.SeedWorkspace(t, st, owner.ID, "archived")
	ownerID := userid.MustNew(owner.ID)
	require.NoError(t, st.WorkspaceSections().Create(ctx, store.CreateWorkspaceSectionParams{
		ID: "archived-section", UserID: ownerID, Name: "Archived", Position: "z",
		SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED,
		Sidebar:     leapmuxv1.Sidebar_SIDEBAR_LEFT,
	}))
	require.NoError(t, st.WorkspaceSectionItems().Set(ctx, store.SetWorkspaceSectionItemParams{
		UserID: ownerID, WorkspaceID: workspaceID, SectionID: "archived-section", Position: "a",
	}))
	require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, store.UpsertTabParams{
		UserID: ownerID, WorkspaceID: workspaceID, WorkerID: worker.ID,
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "archived-agent", TileID: "tile", Position: "a",
		Rendered: false,
	}))

	req := &leapmuxv1.ListOwnedTabsForWorkerRequest{}
	var resp *leapmuxv1.ListOwnedTabsForWorkerResponse
	_, callErr := callUnderServerInfo(ctx, func(callCtx context.Context) error {
		info, _ := connect.CallInfoForServerContext(callCtx)
		info.RequestHeader().Set("Authorization", "Bearer "+worker.AuthToken)
		var err error
		resp, err = service.NewWorkerReconcilerService(st).ListOwnedTabsForWorker(callCtx, req)
		return err
	})
	require.NoError(t, callErr)
	require.Len(t, resp.GetTabStates(), 1)
	assert.Equal(t, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED,
		resp.GetTabStates()[0].GetArchiveState())
}

// TestListOwnedTabsForWorker_RejectsUnauthenticated keeps the bearer gate
// pinned: the owner scope is derived from the resolved worker, so an
// unauthenticated call must not reach the store at all.
func TestListOwnedTabsForWorker_RejectsUnauthenticated(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	// The bearer rides the request header on the server CallInfo, so the call
	// runs through callUnderServerInfo with the header a transport would
	// deliver: a bare method call carries no CallInfo, and reading one panics.
	_, err := callUnderServerInfo(context.Background(), func(ctx context.Context) error {
		info, _ := connect.CallInfoForServerContext(ctx)
		info.RequestHeader().Set("Authorization", "Bearer nope")
		_, callErr := service.NewWorkerReconcilerService(st).ListOwnedTabsForWorker(ctx, &leapmuxv1.ListOwnedTabsForWorkerRequest{})
		return callErr
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// TestListOwnedTabsForWorker_ReachableThroughTheAuthInterceptor is the test the
// two above could not be: they call the handler directly, so they proved the
// bearer gate works while the RPC was unreachable in production.
//
// A worker's auth_token is not a session cookie and not an `lmx_` API token,
// so the hub's auth interceptor rejects it -- unless the procedure is in
// publicProcedures, the same exemption WorkerConnectorService.Register and
// .Connect need for exactly this reason. Without the exemption every
// reconciler pass failed with `unauthenticated`, and the orphan reconciler --
// the sole mechanism that converges a worker after an offline close -- never
// ran at all.
//
// This mounts the real handler behind the real interceptor so the wiring, not
// just the handler, is under test.
func TestListOwnedTabsForWorker_ReachableThroughTheAuthInterceptor(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	ctx := context.Background()
	owner := storetest.SeedUser(t, st, "interceptor-owner")
	worker := storetest.SeedWorker(t, st, owner.ID)
	ws := storetest.SeedWorkspace(t, st, owner.ID, "owned")
	require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, store.UpsertTabParams{
		UserID: userid.MustNew(owner.ID), WorkspaceID: ws, WorkerID: worker.ID,
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "agent-live", TileID: "tile", Position: "a0",
		Rendered: false,
	}))

	interceptor, _ := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st})
	mux := http.NewServeMux()
	connectServer := connect.NewServer(interceptor)
	leapmuxv1connect.RegisterWorkerReconcilerServiceHandler(connectServer,
		service.NewWorkerReconcilerService(st))
	connecthttp.Mount(mux, connectServer, connecthttp.WithReadMaxBytes(0))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewWorkerReconcilerServiceClient(connect.NewClient(connecthttp.NewTransport(server.Client(), server.URL, connecthttp.WithReadMaxBytes(0))))
	req := &leapmuxv1.ListOwnedTabsForWorkerRequest{}
	ctx, info := connect.NewClientContext(ctx)
	info.RequestHeader().Set("Authorization", "Bearer "+worker.AuthToken)

	resp, err := client.ListOwnedTabsForWorker(ctx, req)
	require.NoError(t, err, "a worker's own auth_token must reach the reconciler RPC")
	require.Len(t, resp.GetTabStates(), 1)
	assert.Equal(t, "agent-live", resp.GetTabStates()[0].GetTabId())
	assert.Equal(t, owner.ID, resp.GetOwnerUserId())
}

// TestListOwnedTabsForWorker_InterceptorStillRejectsAStranger is the deny half
// of the exemption above: publicProcedures means "the handler authenticates
// itself", not "anyone may call it". A caller with no worker bearer must still
// be refused -- by the handler, since the interceptor no longer stands in the
// way.
func TestListOwnedTabsForWorker_InterceptorStillRejectsAStranger(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)

	interceptor, _ := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st})
	mux := http.NewServeMux()
	connectServer := connect.NewServer(interceptor)
	leapmuxv1connect.RegisterWorkerReconcilerServiceHandler(connectServer,
		service.NewWorkerReconcilerService(st))
	connecthttp.Mount(mux, connectServer, connecthttp.WithReadMaxBytes(0))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewWorkerReconcilerServiceClient(connect.NewClient(connecthttp.NewTransport(server.Client(), server.URL, connecthttp.WithReadMaxBytes(0))))
	_, err := client.ListOwnedTabsForWorker(context.Background(),
		&leapmuxv1.ListOwnedTabsForWorkerRequest{})
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}
