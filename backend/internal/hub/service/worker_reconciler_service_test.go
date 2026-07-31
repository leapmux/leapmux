package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
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
// legitimately reference one worker. The response must carry only the
// registrant's rows AND announce that scope: the reconciler on the other end
// reaps every LOCAL row the list omits, so an unannounced narrowing would tell
// it to destroy every other owner's live file tabs.
func TestListOwnedTabsForWorker_ScopedToRegistrant(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	ctx := context.Background()
	userA := storetest.SeedUser(t, st, "reconcile-alice")
	userB := storetest.SeedUser(t, st, "reconcile-bob")
	worker := storetest.SeedWorker(t, st, userA.ID)
	wsA := storetest.SeedWorkspace(t, st, userA.ID, "alice ws")

	// Both owners hold a FILE tab with the SAME client-minted id on alice's
	// worker, in a workspace alice owns. Every part of that is schema-legal.
	const sharedTabID = "file-1700000000000-1"
	require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, store.UpsertOwnedTabParams{
		UserID: userid.MustNew(userB.ID), WorkspaceID: wsA, WorkerID: worker.ID,
		TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabID: sharedTabID, TileID: "tile-b", Position: "b0",
	}))
	require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, store.UpsertOwnedTabParams{
		UserID: userid.MustNew(userA.ID), WorkspaceID: wsA, WorkerID: worker.ID,
		TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabID: sharedTabID, TileID: "tile-a", Position: "a0",
	}))

	req := connect.NewRequest(&leapmuxv1.ListOwnedTabsForWorkerRequest{})
	req.Header().Set("Authorization", "Bearer "+worker.AuthToken)
	resp, err := service.NewWorkerReconcilerService(st).ListOwnedTabsForWorker(ctx, req)
	require.NoError(t, err)

	require.Len(t, resp.Msg.GetTabs(), 1, "a foreign owner's row must not be listed")
	got := resp.Msg.GetTabs()[0]
	assert.Equal(t, userA.ID, got.GetUserId())
	assert.Equal(t, sharedTabID, got.GetTabId())
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_FILE, got.GetTabType(),
		"alice's row, not bob's identically-named one")
	assert.Equal(t, userA.ID, resp.Msg.GetOwnerUserId(),
		"the response must declare the owner it is authoritative for")
}

// TestListOwnedTabsForWorker_RejectsUnauthenticated keeps the bearer gate
// pinned: the owner scope is derived from the resolved worker, so an
// unauthenticated call must not reach the store at all.
func TestListOwnedTabsForWorker_RejectsUnauthenticated(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	req := connect.NewRequest(&leapmuxv1.ListOwnedTabsForWorkerRequest{})
	req.Header().Set("Authorization", "Bearer nope")
	_, err := service.NewWorkerReconcilerService(st).ListOwnedTabsForWorker(context.Background(), req)
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
	require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, store.UpsertOwnedTabParams{
		UserID: userid.MustNew(owner.ID), WorkspaceID: ws, WorkerID: worker.ID,
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "agent-live", TileID: "tile", Position: "a0",
	}))

	interceptor, _ := auth.NewInterceptor(st, nil, false, false)
	mux := http.NewServeMux()
	path, handler := leapmuxv1connect.NewWorkerReconcilerServiceHandler(
		service.NewWorkerReconcilerService(st), connect.WithInterceptors(interceptor))
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewWorkerReconcilerServiceClient(server.Client(), server.URL)
	req := connect.NewRequest(&leapmuxv1.ListOwnedTabsForWorkerRequest{})
	req.Header().Set("Authorization", "Bearer "+worker.AuthToken)

	resp, err := client.ListOwnedTabsForWorker(ctx, req)
	require.NoError(t, err, "a worker's own auth_token must reach the reconciler RPC")
	require.Len(t, resp.Msg.GetTabs(), 1)
	assert.Equal(t, "agent-live", resp.Msg.GetTabs()[0].GetTabId())
	assert.Equal(t, owner.ID, resp.Msg.GetOwnerUserId())
}

// TestListOwnedTabsForWorker_InterceptorStillRejectsAStranger is the deny half
// of the exemption above: publicProcedures means "the handler authenticates
// itself", not "anyone may call it". A caller with no worker bearer must still
// be refused -- by the handler, since the interceptor no longer stands in the
// way.
func TestListOwnedTabsForWorker_InterceptorStillRejectsAStranger(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)

	interceptor, _ := auth.NewInterceptor(st, nil, false, false)
	mux := http.NewServeMux()
	path, handler := leapmuxv1connect.NewWorkerReconcilerServiceHandler(
		service.NewWorkerReconcilerService(st), connect.WithInterceptors(interceptor))
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewWorkerReconcilerServiceClient(server.Client(), server.URL)
	_, err := client.ListOwnedTabsForWorker(context.Background(),
		connect.NewRequest(&leapmuxv1.ListOwnedTabsForWorkerRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}
