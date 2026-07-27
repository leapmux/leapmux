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

type noopWorkspaceChannelCloser struct{}

func (noopWorkspaceChannelCloser) CloseChannelsByUsersForWorkspace(string, []string) int { return 0 }

type recordingWorkspaceChannelCloser struct {
	closedWorkspaceIDs []string
	closedUserIDs      []string
}

func (c *recordingWorkspaceChannelCloser) CloseChannelsByUsersForWorkspace(workspaceID string, userIDs []string) int {
	c.closedWorkspaceIDs = append(c.closedWorkspaceIDs, workspaceID)
	c.closedUserIDs = append(c.closedUserIDs, userIDs...)
	return 1
}

func TestNewWorkspaceService_RequiresChannelCloser(t *testing.T) {
	require.Panics(t, func() {
		service.NewWorkspaceService(nil, nil, nil)
	})
	var typedNil *noopWorkspaceChannelCloser
	require.Panics(t, func() {
		service.NewWorkspaceService(nil, nil, typedNil)
	})
}

func TestWorkspaceServiceDeleteWorkspaceClosesChannelsWithWorkspaceSnapshots(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	owner := storetest.SeedUser(t, st, "owner")
	workspaceID := storetest.SeedWorkspace(t, st, owner.ID, "deleted")
	closer := &recordingWorkspaceChannelCloser{}
	svc := service.NewWorkspaceService(st, nil, closer)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: userid.MustNew(owner.ID)})

	_, err := svc.DeleteWorkspace(ctx, connect.NewRequest(&leapmuxv1.DeleteWorkspaceRequest{WorkspaceId: workspaceID}))
	require.NoError(t, err)
	assert.Equal(t, []string{workspaceID}, closer.closedWorkspaceIDs)
	assert.ElementsMatch(t, []string{owner.ID}, closer.closedUserIDs)
}

// TestWorkspaceServiceDeleteWorkspaceFansOutToOwnersWorkersOnly pins the
// tenancy of the delete fan-out. The returned worker ids tell the caller which
// workers to invalidate, and workspace_tab_owned is keyed by (user_id, tab_id)
// with workspace_id only a plain FK -- so another user's row may name this
// workspace and drag ITS worker into the fan-out. Unlike the row-returning
// reads there is no owner column left in a DISTINCT worker_id projection for
// the service to filter on afterwards, so the predicate has to be in the query.
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

	svc := service.NewWorkspaceService(st, nil, &recordingWorkspaceChannelCloser{})
	authCtx := auth.WithUser(ctx, &auth.UserInfo{ID: userid.MustNew(owner.ID)})
	resp, err := svc.DeleteWorkspace(authCtx, connect.NewRequest(&leapmuxv1.DeleteWorkspaceRequest{WorkspaceId: workspaceID}))
	require.NoError(t, err)
	assert.Equal(t, []string{ownerWorker.ID}, resp.Msg.GetWorkerIds(),
		"a stranger's worker must not be swept into the owner's workspace deletion")
}

// TestWorkspaceService_ListWorkspaces_DelegationPinsToScope encodes
// the documented intent of `auth.UserInfo.Credential.WorkspaceScopeID()`: a
// delegation bearer is pinned to one workspace and MUST NOT
// enumerate the user's full grant set. ChannelService already
// enforces this for OpenChannel; ListWorkspaces is the read-side
// twin — a leaked delegation bearer must not be able to discover
// every workspace the underlying user owns.
func TestWorkspaceService_ListWorkspaces_DelegationPinsToScope(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "alice")
	pinned := storetest.SeedWorkspace(t, st, user.ID, "Pinned")
	_ = storetest.SeedWorkspace(t, st, user.ID, "Sibling")

	svc := service.NewWorkspaceService(st, nil, noopWorkspaceChannelCloser{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:         userid.MustNew(user.ID),
		Credential: auth.DelegationCredential("test-delegation", pinned, "worker-mint"),
	})

	resp, err := svc.ListWorkspaces(ctx, connect.NewRequest(&leapmuxv1.ListWorkspacesRequest{}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetWorkspaces(), 1,
		"delegation bearer must surface only its pinned workspace, not every accessible one")
	assert.Equal(t, pinned, resp.Msg.GetWorkspaces()[0].GetId())
}

// TestWorkspaceService_ListWorkspaces_DelegationVerifiesAccess
// catches the "workspace deleted but bearer still alive" edge: a
// delegation token outlives its workspace when the workspace is
// soft-deleted while the bearer is still in its TTL. ListWorkspaces
// must surface this as an empty list rather than returning a
// tombstoned row.
func TestWorkspaceService_ListWorkspaces_DelegationVerifiesAccess(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "alice")
	pinned := storetest.SeedWorkspace(t, st, user.ID, "Pinned")
	// Other user owns a workspace `pinned` doesn't have access to —
	// proves the handler doesn't blindly return whatever id is on
	// the bearer.
	other := storetest.SeedUser(t, st, "bob")
	otherWS := storetest.SeedWorkspace(t, st, other.ID, "Other")

	svc := service.NewWorkspaceService(st, nil, noopWorkspaceChannelCloser{})

	// Sanity: the pinned workspace is returned when accessible.
	resp, err := svc.ListWorkspaces(
		auth.WithUser(context.Background(), &auth.UserInfo{
			ID:         userid.MustNew(user.ID),
			Credential: auth.DelegationCredential("test-delegation", pinned, "worker-mint"),
		}),
		connect.NewRequest(&leapmuxv1.ListWorkspacesRequest{}),
	)
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetWorkspaces(), 1)

	// A bearer scoped to a workspace owned by someone else must
	// not surface it.
	resp, err = svc.ListWorkspaces(
		auth.WithUser(context.Background(), &auth.UserInfo{
			ID:         userid.MustNew(user.ID),
			Credential: auth.DelegationCredential("test-delegation", otherWS, "worker-mint"),
		}),
		connect.NewRequest(&leapmuxv1.ListWorkspacesRequest{}),
	)
	require.NoError(t, err)
	assert.Empty(t, resp.Msg.GetWorkspaces(),
		"a delegation bearer pinned to an inaccessible workspace must yield an empty list")
}

// TestWorkspaceService_GetWorkspace_NonOwnerIsDenied pins the owner-only
// access rule at the RPC surface: another user must get PermissionDenied
// for a workspace they do not own.
func TestWorkspaceService_GetWorkspace_NonOwnerIsDenied(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	owner := storetest.SeedUser(t, st, "owner")
	other := storetest.SeedUser(t, st, "other")
	wsID := storetest.SeedWorkspace(t, st, owner.ID, "Owned")

	svc := service.NewWorkspaceService(st, nil, noopWorkspaceChannelCloser{})
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

func TestWorkspaceService_GetWorkspace_DelegationCollapsesSiblingToNotFound(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "alice")
	pinned := storetest.SeedWorkspace(t, st, user.ID, "Pinned")
	sibling := storetest.SeedWorkspace(t, st, user.ID, "Sibling")

	svc := service.NewWorkspaceService(st, nil, noopWorkspaceChannelCloser{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:         userid.MustNew(user.ID),
		Credential: auth.DelegationCredential("test-delegation", pinned, "worker-mint"),
	})

	_, err := svc.GetWorkspace(ctx, connect.NewRequest(&leapmuxv1.GetWorkspaceRequest{
		WorkspaceId: sibling,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err),
		"a delegated lookup outside its pinned workspace must not confirm the sibling workspace exists")
}

func TestWorkspaceService_TabReads_DelegationPinsToScope(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "alice")
	pinned := storetest.SeedWorkspace(t, st, user.ID, "Pinned")
	sibling := storetest.SeedWorkspace(t, st, user.ID, "Sibling")
	seedRenderedTab(t, st, user.ID, pinned, "tab-pinned")
	seedRenderedTab(t, st, user.ID, sibling, "tab-sibling")

	svc := service.NewWorkspaceService(st, nil, noopWorkspaceChannelCloser{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:         userid.MustNew(user.ID),
		Credential: auth.DelegationCredential("test-delegation", pinned, "worker-mint"),
	})

	listResp, err := svc.ListTabs(ctx, connect.NewRequest(&leapmuxv1.ListTabsRequest{}))
	require.NoError(t, err)
	require.Len(t, listResp.Msg.GetTabs(), 1)
	assert.Equal(t, pinned, listResp.Msg.GetTabs()[0].GetWorkspaceId(),
		"an empty delegated tab list must expand to the pinned workspace only")

	_, err = svc.ListTabs(ctx, connect.NewRequest(&leapmuxv1.ListTabsRequest{
		WorkspaceIds: []string{sibling},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	_, err = svc.GetTab(ctx, connect.NewRequest(&leapmuxv1.GetTabRequest{
		WorkspaceId: sibling,
		TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabId:       "tab-sibling",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err),
		"direct tab lookup outside the delegation scope must collapse to NotFound")

	_, err = svc.LocateTab(ctx, connect.NewRequest(&leapmuxv1.LocateTabRequest{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabId:   "tab-sibling",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err),
		"cross-workspace tab locate must not leak a sibling tab through the user's broader ACL")
}

// TestWorkspaceService_LocateTile_FindsByWorkspaceRoot exercises the
// simplest happy path: a tile id that *is* a workspace root resolves
// to its workspace without walking any parent links. This pins
// the base case of `tileOwningWorkspace` against future regressions
// where a refactor of the walk loop accidentally skips the
// "current node is a root" check.
func TestWorkspaceService_LocateTile_FindsByWorkspaceRoot(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "alice")
	ws := storetest.SeedWorkspace(t, st, user.ID, "WS")

	env := setupLocateTileEnv(t, user.ID)
	env.mgr.SeedStateForTest(func(s *leapmuxv1.UserCrdtState) {
		s.Workspaces[ws] = &leapmuxv1.WorkspaceContentsRecord{WorkspaceId: ws, RootNodeId: "root-1"}
		s.Nodes["root-1"] = &leapmuxv1.NodeRecord{NodeId: "root-1"}
	})
	svc := service.NewWorkspaceService(st, env.registry, noopWorkspaceChannelCloser{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: userid.MustNew(user.ID)})

	resp, err := svc.LocateTile(ctx, connect.NewRequest(&leapmuxv1.LocateTileRequest{TileId: "root-1"}))
	require.NoError(t, err)
	assert.Equal(t, ws, resp.Msg.GetWorkspaceId())
}

func seedRenderedTab(t *testing.T, st store.Store, userID, workspaceID, tabID string) {
	t.Helper()
	require.NoError(t, st.WorkspaceTabIndex().UpsertRendered(context.Background(), store.UpsertRenderedTabParams{
		UserID:      userid.MustNew(userID),
		WorkspaceID: workspaceID,
		TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabID:       tabID,
		WorkerID:    "worker-" + tabID,
		TileID:      "tile-" + tabID,
		Position:    "pos-" + tabID,
	}))
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
	svc := service.NewWorkspaceService(st, env.registry, noopWorkspaceChannelCloser{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: userid.MustNew(user.ID)})

	resp, err := svc.LocateTile(ctx, connect.NewRequest(&leapmuxv1.LocateTileRequest{TileId: "leaf-1"}))
	require.NoError(t, err)
	assert.Equal(t, ws, resp.Msg.GetWorkspaceId())
}

// TestWorkspaceService_LocateTile_DelegationCollapsesToNotFound is the
// scope-leak guard. A delegated bearer pinned to workspace A must
// not be able to enumerate sibling tiles in workspace B even though
// both belong to the same user. We deliberately collapse
// PermissionDenied to NotFound to avoid leaking existence to the
// bearer.
func TestWorkspaceService_LocateTile_DelegationCollapsesToNotFound(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "alice")
	allowedWS := storetest.SeedWorkspace(t, st, user.ID, "Allowed")
	forbiddenWS := storetest.SeedWorkspace(t, st, user.ID, "Forbidden")

	env := setupLocateTileEnv(t, user.ID)
	env.mgr.SeedStateForTest(func(s *leapmuxv1.UserCrdtState) {
		s.Workspaces[forbiddenWS] = &leapmuxv1.WorkspaceContentsRecord{WorkspaceId: forbiddenWS, RootNodeId: "root-forbidden"}
		s.Nodes["root-forbidden"] = &leapmuxv1.NodeRecord{NodeId: "root-forbidden"}
	})
	svc := service.NewWorkspaceService(st, env.registry, noopWorkspaceChannelCloser{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:         userid.MustNew(user.ID),
		Credential: auth.DelegationCredential("test-delegation", allowedWS, "worker-mint"),
	})

	_, err := svc.LocateTile(ctx, connect.NewRequest(&leapmuxv1.LocateTileRequest{TileId: "root-forbidden"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err),
		"a tile outside the delegation scope must surface as NotFound, not PermissionDenied (existence leak)")
}

// TestWorkspaceService_LocateTile_RejectsEmptyTileID covers the
// invalid-args branch. Empty tile_id hard-fails before any auth or
// CRDT lookup so the error envelope is unambiguous.
func TestWorkspaceService_LocateTile_RejectsEmptyTileID(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "alice")
	env := setupLocateTileEnv(t, user.ID)
	svc := service.NewWorkspaceService(st, env.registry, noopWorkspaceChannelCloser{})
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
	svc := service.NewWorkspaceService(st, env.registry, noopWorkspaceChannelCloser{})
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

	svc := service.NewWorkspaceService(st, registry, noopWorkspaceChannelCloser{})
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
