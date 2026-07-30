package service_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
)

func TestWorkspaceServiceDeleteWorkspaceFansOutToOwnersWorkersOnly(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	ctx := context.Background()
	owner := storetest.SeedUser(t, st, "fanout-owner")
	stranger := storetest.SeedUser(t, st, "fanout-stranger")
	ownerWorker := storetest.SeedWorker(t, st, owner.ID)
	strangerWorker := storetest.SeedWorker(t, st, stranger.ID)
	workspaceID := storetest.SeedWorkspace(t, st, owner.ID, "deleted")

	require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, store.UpsertOwnedTabParams{
		UserID: userid.MustNew(owner.ID), WorkspaceID: workspaceID, WorkerID: ownerWorker.ID,
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "tab-owner", TileID: "tile", Position: "a0",
	}))
	require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, store.UpsertOwnedTabParams{
		UserID: userid.MustNew(stranger.ID), WorkspaceID: workspaceID, WorkerID: strangerWorker.ID,
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "tab-stranger", TileID: "tile", Position: "a1",
	}))

	svc := service.NewWorkspaceService(st, nil)
	authCtx := auth.WithUser(ctx, &auth.UserInfo{ID: userid.MustNew(owner.ID)})
	resp, err := svc.DeleteWorkspace(authCtx, connect.NewRequest(&leapmuxv1.DeleteWorkspaceRequest{WorkspaceId: workspaceID}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetWorkerTabs(), 1,
		"a stranger's worker must not be swept into the owner's workspace deletion")
	assert.Equal(t, ownerWorker.ID, resp.Msg.GetWorkerTabs()[0].GetWorkerId())
	// The tabs travel with the worker, read inside the delete transaction. That
	// is what removed the caller's own pre-delete read -- and with it a race, a
	// swallowed error, and a projection subset that hid tabs entirely.
	assert.NotEmpty(t, resp.Msg.GetWorkerTabs()[0].GetTabs(),
		"the response must name the tabs the worker has to tear down")
}

// A delegation bearer authenticates AS its user, so it lists exactly what that
// user owns -- no more, and no less than a session would. The workspace pin
// that used to narrow this to one id is gone; what still bounds the bearer is
// which WORKER it may reach, which this RPC does not touch.
func TestWorkspaceService_ListWorkspaces_DelegationSeesTheOwnersWorkspaces(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "alice")
	wsA := storetest.SeedWorkspace(t, st, user.ID, "A")
	wsB := storetest.SeedWorkspace(t, st, user.ID, "B")
	// Another user's workspace, to prove the listing is still owner-scoped.
	other := storetest.SeedUser(t, st, "bob")
	storetest.SeedWorkspace(t, st, other.ID, "Other")

	svc := service.NewWorkspaceService(st, nil)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:         userid.MustNew(user.ID),
		Credential: auth.DelegationCredential("test-delegation", "worker-mint"),
	})

	resp, err := svc.ListWorkspaces(ctx, connect.NewRequest(&leapmuxv1.ListWorkspacesRequest{}))
	require.NoError(t, err)
	ids := make([]string, 0, len(resp.Msg.GetWorkspaces()))
	for _, ws := range resp.Msg.GetWorkspaces() {
		ids = append(ids, ws.GetId())
	}
	assert.ElementsMatch(t, []string{wsA, wsB}, ids,
		"a delegation bearer reads its user's workspaces, and only those")
}

func TestWorkspaceService_GetWorkspace_NonOwnerIsDenied(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	owner := storetest.SeedUser(t, st, "owner")
	other := storetest.SeedUser(t, st, "other")
	wsID := storetest.SeedWorkspace(t, st, owner.ID, "Owned")

	svc := service.NewWorkspaceService(st, nil)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: userid.MustNew(other.ID)})

	_, err := svc.GetWorkspace(ctx, connect.NewRequest(&leapmuxv1.GetWorkspaceRequest{
		WorkspaceId: wsID,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err),
		"a non-owner must be denied access to someone else's workspace")

	// The owner still reads it.
	ownerCtx := auth.WithUser(context.Background(), &auth.UserInfo{ID: userid.MustNew(owner.ID)})
	resp, err := svc.GetWorkspace(ownerCtx, connect.NewRequest(&leapmuxv1.GetWorkspaceRequest{
		WorkspaceId: wsID,
	}))
	require.NoError(t, err)
	assert.Equal(t, wsID, resp.Msg.GetWorkspace().GetId())
}

func TestWorkspaceService_LocateTile_FindsByWorkspaceRoot(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "alice")
	ws := storetest.SeedWorkspace(t, st, user.ID, "WS")

	env := setupLocateTileEnv(t, user.ID)
	env.mgr.SeedStateForTest(func(s *leapmuxv1.UserCrdtState) {
		s.Workspaces[ws] = &leapmuxv1.WorkspaceContentsRecord{WorkspaceId: ws, RootNodeId: "root-1"}
		s.Nodes["root-1"] = &leapmuxv1.NodeRecord{NodeId: "root-1"}
	})
	svc := service.NewWorkspaceService(st, env.registry)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: userid.MustNew(user.ID)})

	resp, err := svc.LocateTile(ctx, connect.NewRequest(&leapmuxv1.LocateTileRequest{TileId: "root-1"}))
	require.NoError(t, err)
	assert.Equal(t, ws, resp.Msg.GetWorkspaceId())
}

// TestWorkspaceService_LocateTile_WalksUpToOwningWorkspace pins the
// transitive walk: a tile nested under a workspace's root must
// climb parent_id links until a workspace root matches. Without
// this coverage a regression that only checked direct membership
// would silently return NotFound for every non-root tile, which
// includes every tile in a tiled workspace.
func TestWorkspaceService_LocateTile_WalksUpToOwningWorkspace(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "alice")
	ws := storetest.SeedWorkspace(t, st, user.ID, "WS")

	env := setupLocateTileEnv(t, user.ID)
	env.mgr.SeedStateForTest(func(s *leapmuxv1.UserCrdtState) {
		s.Workspaces[ws] = &leapmuxv1.WorkspaceContentsRecord{WorkspaceId: ws, RootNodeId: "root-1"}
		s.Nodes["root-1"] = &leapmuxv1.NodeRecord{NodeId: "root-1"}
		s.Nodes["mid-1"] = &leapmuxv1.NodeRecord{NodeId: "mid-1", ParentId: "root-1"}
		s.Nodes["leaf-1"] = &leapmuxv1.NodeRecord{NodeId: "leaf-1", ParentId: "mid-1"}
	})
	svc := service.NewWorkspaceService(st, env.registry)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: userid.MustNew(user.ID)})

	resp, err := svc.LocateTile(ctx, connect.NewRequest(&leapmuxv1.LocateTileRequest{TileId: "leaf-1"}))
	require.NoError(t, err)
	assert.Equal(t, ws, resp.Msg.GetWorkspaceId())
}

func TestWorkspaceService_LocateTile_RejectsEmptyTileID(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "alice")
	env := setupLocateTileEnv(t, user.ID)
	svc := service.NewWorkspaceService(st, env.registry)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: userid.MustNew(user.ID)})

	_, err := svc.LocateTile(ctx, connect.NewRequest(&leapmuxv1.LocateTileRequest{TileId: ""}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

// TestWorkspaceService_LocateTile_NotFoundForUnknownTile covers the
// "tile id exists in no workspace" case. The walk terminates at a
// missing parent node and returns "", which the handler surfaces
// as NotFound.
func TestWorkspaceService_LocateTile_NotFoundForUnknownTile(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "alice")
	env := setupLocateTileEnv(t, user.ID)
	svc := service.NewWorkspaceService(st, env.registry)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: userid.MustNew(user.ID)})

	_, err := svc.LocateTile(ctx, connect.NewRequest(&leapmuxv1.LocateTileRequest{TileId: "ghost"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// TestWorkspaceService_LocateTile_TransientManagerErrorIsRetryable pins the
// registry.Get failure arm: when the caller's manager cannot be bootstrapped,
// the caller must get a retryable Internal, not NotFound. NotFound is a
// permanent answer -- the CLI tile resolver stops looking -- so a DB blip
// during manager bootstrap would report a tile that exists as gone. Every
// other LocateTile test goes through setupLocateTileEnv, whose factory always
// succeeds, so without this test folding the Get failure into the same
// `return notFound` as an unresolved tile stays green.
func TestWorkspaceService_LocateTile_TransientManagerErrorIsRetryable(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "alice")

	// The only candidate (the caller's own user) fails to bootstrap transiently.
	registry := crdt.NewRegistry(func(context.Context, userid.UserID) (*crdt.Manager, error) {
		return nil, errors.New("transient bootstrap failure")
	}, nil, crdt.WithManagerIdleTTL(0))
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

	svc := service.NewWorkspaceService(st, registry)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: userid.MustNew(user.ID)})

	_, err := svc.LocateTile(ctx, connect.NewRequest(&leapmuxv1.LocateTileRequest{TileId: "missing"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err),
		"an unresolved tile plus a transient manager failure must be retryable Internal, not NotFound")
}

// locateTileEnv bundles a registry-backed manager so each LocateTile
// test can seed the in-memory state via SeedStateForTest without
// driving real lifecycle events through the journal.
type locateTileEnv struct {
	mgr      *crdt.Manager
	registry *crdt.Registry
}

func setupLocateTileEnv(t *testing.T, userID string) *locateTileEnv {
	t.Helper()
	j := newMemJournal()
	var (
		once sync.Once
		mgr  *crdt.Manager
	)
	registry := crdt.NewRegistry(func(ctx context.Context, want userid.UserID) (*crdt.Manager, error) {
		if want.String() != userID {
			return nil, errors.New("unexpected user")
		}
		once.Do(func() {
			mgr = crdt.NewManager(userid.MustNew(userID), j, allowAllAuth{}, nil, time.Now)
			require.NoError(t, mgr.Bootstrap(ctx))
		})
		return mgr, nil
	}, nil)
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })
	_, err := registry.Get(context.Background(), userID)
	require.NoError(t, err)
	return &locateTileEnv{mgr: mgr, registry: registry}
}
