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
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
)

// TestWorkspaceService_ListWorkspaces_DefaultsOrgIDToUserHome locks in
// the CLI-friendly default: when the caller doesn't specify an
// org_id, the handler falls back to the authenticated user's home
// org rather than asking the SQL layer to match an empty string
// (which never hits a row). Without this default, `leapmux remote
// workspace list` returns `null` from inside a tab because the
// worker-side delegation flow never threads an org_id into the
// request body.
func TestWorkspaceService_ListWorkspaces_DefaultsOrgIDToUserHome(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	orgID := storetest.SeedOrg(t, st, "primary-org", false)
	user := storetest.SeedUser(t, st, orgID, "alice")
	ws1 := storetest.SeedWorkspace(t, st, orgID, user.ID, "WS1")
	ws2 := storetest.SeedWorkspace(t, st, orgID, user.ID, "WS2")

	svc := service.NewWorkspaceService(st, false, nil)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:    user.ID,
		OrgID: orgID,
	})

	resp, err := svc.ListWorkspaces(ctx, connect.NewRequest(&leapmuxv1.ListWorkspacesRequest{}))
	require.NoError(t, err)
	got := make([]string, 0, len(resp.Msg.GetWorkspaces()))
	for _, w := range resp.Msg.GetWorkspaces() {
		got = append(got, w.GetId())
	}
	assert.ElementsMatch(t, []string{ws1, ws2}, got,
		"empty org_id must default to user.OrgID, not match the literal empty string")
}

// TestWorkspaceService_ListWorkspaces_DelegationPinsToScope encodes
// the documented intent of `auth.UserInfo.DelegationWorkspaceID`: a
// delegation bearer is pinned to one workspace and MUST NOT
// enumerate the user's full grant set. ChannelService already
// enforces this for OpenChannel; ListWorkspaces is the read-side
// twin — a leaked delegation bearer must not be able to discover
// every workspace the underlying user owns.
func TestWorkspaceService_ListWorkspaces_DelegationPinsToScope(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	orgID := storetest.SeedOrg(t, st, "primary-org", false)
	user := storetest.SeedUser(t, st, orgID, "alice")
	pinned := storetest.SeedWorkspace(t, st, orgID, user.ID, "Pinned")
	_ = storetest.SeedWorkspace(t, st, orgID, user.ID, "Sibling")

	svc := service.NewWorkspaceService(st, false, nil)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:                    user.ID,
		OrgID:                 orgID,
		DelegationWorkspaceID: pinned,
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
	orgID := storetest.SeedOrg(t, st, "primary-org", false)
	user := storetest.SeedUser(t, st, orgID, "alice")
	pinned := storetest.SeedWorkspace(t, st, orgID, user.ID, "Pinned")
	// Other user owns a workspace `pinned` doesn't have access to —
	// proves the handler doesn't blindly return whatever id is on
	// the bearer.
	other := storetest.SeedUser(t, st, orgID, "bob")
	otherWS := storetest.SeedWorkspace(t, st, orgID, other.ID, "Other")

	svc := service.NewWorkspaceService(st, false, nil)

	// Sanity: the pinned workspace is returned when accessible.
	resp, err := svc.ListWorkspaces(
		auth.WithUser(context.Background(), &auth.UserInfo{
			ID:                    user.ID,
			OrgID:                 orgID,
			DelegationWorkspaceID: pinned,
		}),
		connect.NewRequest(&leapmuxv1.ListWorkspacesRequest{}),
	)
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetWorkspaces(), 1)

	// A bearer scoped to a workspace owned by someone else must
	// not surface it.
	resp, err = svc.ListWorkspaces(
		auth.WithUser(context.Background(), &auth.UserInfo{
			ID:                    user.ID,
			OrgID:                 orgID,
			DelegationWorkspaceID: otherWS,
		}),
		connect.NewRequest(&leapmuxv1.ListWorkspacesRequest{}),
	)
	require.NoError(t, err)
	assert.Empty(t, resp.Msg.GetWorkspaces(),
		"a delegation bearer pinned to an inaccessible workspace must yield an empty list")
}

// TestWorkspaceService_LocateTile_FindsByWorkspaceRoot exercises the
// simplest happy path: a tile id that *is* a workspace root resolves
// to its workspace + org without walking any parent links. This pins
// the base case of `tileOwningWorkspace` against future regressions
// where a refactor of the walk loop accidentally skips the
// "current node is a root" check.
func TestWorkspaceService_LocateTile_FindsByWorkspaceRoot(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	orgID := storetest.SeedOrg(t, st, "primary-org", false)
	user := storetest.SeedUser(t, st, orgID, "alice")
	ws := storetest.SeedWorkspace(t, st, orgID, user.ID, "WS")

	env := setupLocateTileEnv(t, orgID)
	env.mgr.MutateInternal(func(s *leapmuxv1.OrgCrdtState) {
		s.Workspaces[ws] = &leapmuxv1.WorkspaceContentsRecord{WorkspaceId: ws, RootNodeId: "root-1"}
		s.Nodes["root-1"] = &leapmuxv1.NodeRecord{NodeId: "root-1"}
	})
	svc := service.NewWorkspaceService(st, false, env.registry)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: user.ID, OrgID: orgID})

	resp, err := svc.LocateTile(ctx, connect.NewRequest(&leapmuxv1.LocateTileRequest{TileId: "root-1"}))
	require.NoError(t, err)
	assert.Equal(t, ws, resp.Msg.GetWorkspaceId())
	assert.Equal(t, orgID, resp.Msg.GetOrgId())
}

// TestWorkspaceService_LocateTile_WalksUpToOwningWorkspace pins the
// transitive walk: a tile nested under a workspace's root must
// climb parent_id links until a workspace root matches. Without
// this coverage a regression that only checked direct membership
// would silently return NotFound for every non-root tile, which
// includes every tile in a tiled workspace.
func TestWorkspaceService_LocateTile_WalksUpToOwningWorkspace(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	orgID := storetest.SeedOrg(t, st, "primary-org", false)
	user := storetest.SeedUser(t, st, orgID, "alice")
	ws := storetest.SeedWorkspace(t, st, orgID, user.ID, "WS")

	env := setupLocateTileEnv(t, orgID)
	env.mgr.MutateInternal(func(s *leapmuxv1.OrgCrdtState) {
		s.Workspaces[ws] = &leapmuxv1.WorkspaceContentsRecord{WorkspaceId: ws, RootNodeId: "root-1"}
		s.Nodes["root-1"] = &leapmuxv1.NodeRecord{NodeId: "root-1"}
		s.Nodes["mid-1"] = &leapmuxv1.NodeRecord{NodeId: "mid-1", ParentId: "root-1"}
		s.Nodes["leaf-1"] = &leapmuxv1.NodeRecord{NodeId: "leaf-1", ParentId: "mid-1"}
	})
	svc := service.NewWorkspaceService(st, false, env.registry)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: user.ID, OrgID: orgID})

	resp, err := svc.LocateTile(ctx, connect.NewRequest(&leapmuxv1.LocateTileRequest{TileId: "leaf-1"}))
	require.NoError(t, err)
	assert.Equal(t, ws, resp.Msg.GetWorkspaceId())
}

// TestWorkspaceService_LocateTile_DelegationCollapsesToNotFound is the
// scope-leak guard. A delegated bearer pinned to workspace A must
// not be able to enumerate sibling tiles in workspace B even though
// both belong to the user's org. We deliberately collapse
// PermissionDenied to NotFound to avoid leaking existence to the
// bearer.
func TestWorkspaceService_LocateTile_DelegationCollapsesToNotFound(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	orgID := storetest.SeedOrg(t, st, "primary-org", false)
	user := storetest.SeedUser(t, st, orgID, "alice")
	allowedWS := storetest.SeedWorkspace(t, st, orgID, user.ID, "Allowed")
	forbiddenWS := storetest.SeedWorkspace(t, st, orgID, user.ID, "Forbidden")

	env := setupLocateTileEnv(t, orgID)
	env.mgr.MutateInternal(func(s *leapmuxv1.OrgCrdtState) {
		s.Workspaces[forbiddenWS] = &leapmuxv1.WorkspaceContentsRecord{WorkspaceId: forbiddenWS, RootNodeId: "root-forbidden"}
		s.Nodes["root-forbidden"] = &leapmuxv1.NodeRecord{NodeId: "root-forbidden"}
	})
	svc := service.NewWorkspaceService(st, false, env.registry)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:                    user.ID,
		OrgID:                 orgID,
		DelegationWorkspaceID: allowedWS,
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
	orgID := storetest.SeedOrg(t, st, "primary-org", false)
	user := storetest.SeedUser(t, st, orgID, "alice")
	env := setupLocateTileEnv(t, orgID)
	svc := service.NewWorkspaceService(st, false, env.registry)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: user.ID, OrgID: orgID})

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
	orgID := storetest.SeedOrg(t, st, "primary-org", false)
	user := storetest.SeedUser(t, st, orgID, "alice")
	env := setupLocateTileEnv(t, orgID)
	svc := service.NewWorkspaceService(st, false, env.registry)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: user.ID, OrgID: orgID})

	_, err := svc.LocateTile(ctx, connect.NewRequest(&leapmuxv1.LocateTileRequest{TileId: "ghost"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// locateTileEnv bundles a registry-backed manager so each LocateTile
// test can seed the in-memory state via MutateInternal without
// driving real lifecycle events through the journal.
type locateTileEnv struct {
	mgr      *crdt.Manager
	registry *crdt.Registry
}

func setupLocateTileEnv(t *testing.T, orgID string) *locateTileEnv {
	t.Helper()
	j := newMemJournal()
	var (
		once sync.Once
		mgr  *crdt.Manager
	)
	registry := crdt.NewRegistry(func(ctx context.Context, want string) (*crdt.Manager, error) {
		if want != orgID {
			return nil, errors.New("unexpected org")
		}
		once.Do(func() {
			mgr = crdt.NewManager(orgID, j, allowAllAuth{}, nil, time.Now)
			require.NoError(t, mgr.Bootstrap(ctx))
		})
		return mgr, nil
	}, nil)
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })
	_, err := registry.Get(context.Background(), orgID)
	require.NoError(t, err)
	return &locateTileEnv{mgr: mgr, registry: registry}
}
