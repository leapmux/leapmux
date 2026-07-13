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
	orgID := storetest.SeedOrg(t, st, "delete-org")
	owner := storetest.SeedUser(t, st, orgID, "owner")
	workspaceID := storetest.SeedWorkspace(t, st, orgID, owner.ID, "deleted")
	closer := &recordingWorkspaceChannelCloser{}
	svc := service.NewWorkspaceService(st, nil, closer)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: owner.ID, OrgID: orgID})

	_, err := svc.DeleteWorkspace(ctx, connect.NewRequest(&leapmuxv1.DeleteWorkspaceRequest{WorkspaceId: workspaceID}))
	require.NoError(t, err)
	assert.Equal(t, []string{workspaceID}, closer.closedWorkspaceIDs)
	assert.ElementsMatch(t, []string{owner.ID}, closer.closedUserIDs)
}

// TestWorkspaceService_CreateWorkspace_RejectsNonMemberOrg pins the C2 authz
// gate: a caller may only home a new workspace in an org they belong to. A
// caller-supplied org_id for an org the user is not a member of must fail
// closed with NotFound (mirroring ResolveOrgID) and create nothing in that
// org's namespace, while an empty org_id falls back to the caller's home org.
func TestWorkspaceService_CreateWorkspace_RejectsNonMemberOrg(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	homeOrg := storetest.SeedOrg(t, st, "home-org")
	otherOrg := storetest.SeedOrg(t, st, "other-org")
	user := storetest.SeedUser(t, st, homeOrg, "alice") // member of homeOrg only

	svc := service.NewWorkspaceService(st, nil, noopWorkspaceChannelCloser{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: user.ID, OrgID: homeOrg})

	// A non-member org is rejected with NotFound and creates nothing.
	_, err := svc.CreateWorkspace(ctx, connect.NewRequest(&leapmuxv1.CreateWorkspaceRequest{
		OrgId: otherOrg,
		Title: "intruder",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	inOther, err := st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
		UserID: user.ID, OrgID: otherOrg,
	})
	require.NoError(t, err)
	assert.Empty(t, inOther, "a rejected create must not leave a phantom workspace in the non-member org")

	// An empty org_id succeeds and homes the workspace in the caller's org.
	resp, err := svc.CreateWorkspace(ctx, connect.NewRequest(&leapmuxv1.CreateWorkspaceRequest{
		Title: "mine",
	}))
	require.NoError(t, err)
	created, err := st.Workspaces().GetByID(ctx, resp.Msg.GetWorkspaceId())
	require.NoError(t, err)
	assert.Equal(t, homeOrg, created.OrgID, "empty org_id must home the workspace in the caller's org")
}

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
	orgID := storetest.SeedOrg(t, st, "primary-org")
	user := storetest.SeedUser(t, st, orgID, "alice")
	ws1 := storetest.SeedWorkspace(t, st, orgID, user.ID, "WS1")
	ws2 := storetest.SeedWorkspace(t, st, orgID, user.ID, "WS2")

	svc := service.NewWorkspaceService(st, nil, noopWorkspaceChannelCloser{})
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
// the documented intent of `auth.UserInfo.Credential.WorkspaceScopeID()`: a
// delegation bearer is pinned to one workspace and MUST NOT
// enumerate the user's full grant set. ChannelService already
// enforces this for OpenChannel; ListWorkspaces is the read-side
// twin — a leaked delegation bearer must not be able to discover
// every workspace the underlying user owns.
func TestWorkspaceService_ListWorkspaces_DelegationPinsToScope(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	orgID := storetest.SeedOrg(t, st, "primary-org")
	user := storetest.SeedUser(t, st, orgID, "alice")
	pinned := storetest.SeedWorkspace(t, st, orgID, user.ID, "Pinned")
	_ = storetest.SeedWorkspace(t, st, orgID, user.ID, "Sibling")

	svc := service.NewWorkspaceService(st, nil, noopWorkspaceChannelCloser{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:         user.ID,
		OrgID:      orgID,
		Credential: auth.DelegationCredential("test-delegation", pinned, "worker-mint"),
	})

	resp, err := svc.ListWorkspaces(ctx, connect.NewRequest(&leapmuxv1.ListWorkspacesRequest{}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetWorkspaces(), 1,
		"delegation bearer must surface only its pinned workspace, not every accessible one")
	assert.Equal(t, pinned, resp.Msg.GetWorkspaces()[0].GetId())

	resp, err = svc.ListWorkspaces(ctx, connect.NewRequest(&leapmuxv1.ListWorkspacesRequest{OrgId: "different-org"}))
	require.NoError(t, err)
	assert.Empty(t, resp.Msg.GetWorkspaces(),
		"delegated ListWorkspaces must still honor an explicit org_id mismatch")
}

// TestWorkspaceService_ListWorkspaces_ForeignOrgReturnsNothing locks in that
// an explicit --org-id for another user's org never surfaces that org's
// workspaces: access is owner-only, and the caller owns nothing there.
func TestWorkspaceService_ListWorkspaces_ForeignOrgReturnsNothing(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	homeOrg := storetest.SeedOrg(t, st, "home-org")
	otherOrg := storetest.SeedOrg(t, st, "other-org")
	viewer := storetest.SeedUser(t, st, homeOrg, "viewer")
	ownerB := storetest.SeedUser(t, st, otherOrg, "ownerB")
	storetest.SeedWorkspace(t, st, otherOrg, ownerB.ID, "not mine")

	svc := service.NewWorkspaceService(st, nil, noopWorkspaceChannelCloser{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: viewer.ID, OrgID: homeOrg})

	// Explicitly target the foreign org: nothing may surface.
	resp, err := svc.ListWorkspaces(ctx, connect.NewRequest(&leapmuxv1.ListWorkspacesRequest{OrgId: otherOrg}))
	require.NoError(t, err)
	assert.Empty(t, resp.Msg.GetWorkspaces(),
		"another user's org must never surface workspaces the caller does not own")
}

// TestWorkspaceService_ListWorkspaces_DelegationVerifiesAccess
// catches the "workspace deleted but bearer still alive" edge: a
// delegation token outlives its workspace when the workspace is
// soft-deleted while the bearer is still in its TTL. ListWorkspaces
// must surface this as an empty list rather than returning a
// tombstoned row.
func TestWorkspaceService_ListWorkspaces_DelegationVerifiesAccess(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	orgID := storetest.SeedOrg(t, st, "primary-org")
	user := storetest.SeedUser(t, st, orgID, "alice")
	pinned := storetest.SeedWorkspace(t, st, orgID, user.ID, "Pinned")
	// Other user owns a workspace `pinned` doesn't have access to —
	// proves the handler doesn't blindly return whatever id is on
	// the bearer.
	other := storetest.SeedUser(t, st, orgID, "bob")
	otherWS := storetest.SeedWorkspace(t, st, orgID, other.ID, "Other")

	svc := service.NewWorkspaceService(st, nil, noopWorkspaceChannelCloser{})

	// Sanity: the pinned workspace is returned when accessible.
	resp, err := svc.ListWorkspaces(
		auth.WithUser(context.Background(), &auth.UserInfo{
			ID:         user.ID,
			OrgID:      orgID,
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
			ID:         user.ID,
			OrgID:      orgID,
			Credential: auth.DelegationCredential("test-delegation", otherWS, "worker-mint"),
		}),
		connect.NewRequest(&leapmuxv1.ListWorkspacesRequest{}),
	)
	require.NoError(t, err)
	assert.Empty(t, resp.Msg.GetWorkspaces(),
		"a delegation bearer pinned to an inaccessible workspace must yield an empty list")
}

// TestWorkspaceService_GetWorkspace_NonOwnerIsDenied pins the owner-only
// access rule at the RPC surface: another user -- even in the same org --
// must get PermissionDenied for a workspace they do not own.
func TestWorkspaceService_GetWorkspace_NonOwnerIsDenied(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	orgID := storetest.SeedOrg(t, st, "primary-org")
	owner := storetest.SeedUser(t, st, orgID, "owner")
	other := storetest.SeedUser(t, st, orgID, "other")
	wsID := storetest.SeedWorkspace(t, st, orgID, owner.ID, "Owned")

	svc := service.NewWorkspaceService(st, nil, noopWorkspaceChannelCloser{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: other.ID, OrgID: orgID})

	_, err := svc.GetWorkspace(ctx, connect.NewRequest(&leapmuxv1.GetWorkspaceRequest{
		WorkspaceId: wsID,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err),
		"a non-owner must be denied access to someone else's workspace")

	// The owner still reads it.
	ownerCtx := auth.WithUser(context.Background(), &auth.UserInfo{ID: owner.ID, OrgID: orgID})
	resp, err := svc.GetWorkspace(ownerCtx, connect.NewRequest(&leapmuxv1.GetWorkspaceRequest{
		WorkspaceId: wsID,
	}))
	require.NoError(t, err)
	assert.Equal(t, wsID, resp.Msg.GetWorkspace().GetId())
}

func TestWorkspaceService_GetWorkspace_DelegationCollapsesSiblingToNotFound(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	orgID := storetest.SeedOrg(t, st, "primary-org")
	user := storetest.SeedUser(t, st, orgID, "alice")
	pinned := storetest.SeedWorkspace(t, st, orgID, user.ID, "Pinned")
	sibling := storetest.SeedWorkspace(t, st, orgID, user.ID, "Sibling")

	svc := service.NewWorkspaceService(st, nil, noopWorkspaceChannelCloser{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:         user.ID,
		OrgID:      orgID,
		Credential: auth.DelegationCredential("test-delegation", pinned, "worker-mint"),
	})

	_, err := svc.GetWorkspace(ctx, connect.NewRequest(&leapmuxv1.GetWorkspaceRequest{
		OrgId:       orgID,
		WorkspaceId: sibling,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err),
		"a delegated lookup outside its pinned workspace must not confirm the sibling workspace exists")
}

func TestWorkspaceService_TabReads_DelegationPinsToScope(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	orgID := storetest.SeedOrg(t, st, "primary-org")
	user := storetest.SeedUser(t, st, orgID, "alice")
	pinned := storetest.SeedWorkspace(t, st, orgID, user.ID, "Pinned")
	sibling := storetest.SeedWorkspace(t, st, orgID, user.ID, "Sibling")
	seedRenderedTab(t, st, orgID, pinned, "tab-pinned")
	seedRenderedTab(t, st, orgID, sibling, "tab-sibling")

	svc := service.NewWorkspaceService(st, nil, noopWorkspaceChannelCloser{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:         user.ID,
		OrgID:      orgID,
		Credential: auth.DelegationCredential("test-delegation", pinned, "worker-mint"),
	})

	listResp, err := svc.ListTabs(ctx, connect.NewRequest(&leapmuxv1.ListTabsRequest{OrgId: orgID}))
	require.NoError(t, err)
	require.Len(t, listResp.Msg.GetTabs(), 1)
	assert.Equal(t, pinned, listResp.Msg.GetTabs()[0].GetWorkspaceId(),
		"an empty delegated tab list must expand to the pinned workspace only")

	_, err = svc.ListTabs(ctx, connect.NewRequest(&leapmuxv1.ListTabsRequest{
		OrgId:        orgID,
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

func TestWorkspaceService_TabReads_DelegationUsesPinnedWorkspaceOrg(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	homeOrgID := storetest.SeedOrg(t, st, "home-org")
	agentOrgID := storetest.SeedOrg(t, st, "agent-org")
	user := storetest.SeedUser(t, st, homeOrgID, "alice")
	homeWS := storetest.SeedWorkspace(t, st, homeOrgID, user.ID, "Home")
	pinned := storetest.SeedWorkspace(t, st, agentOrgID, user.ID, "Pinned")
	seedRenderedTab(t, st, homeOrgID, homeWS, "shared-tab")
	seedRenderedTab(t, st, agentOrgID, pinned, "shared-tab")

	svc := service.NewWorkspaceService(st, nil, noopWorkspaceChannelCloser{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:         user.ID,
		OrgID:      homeOrgID,
		Credential: auth.DelegationCredential("test-delegation", pinned, "worker-mint"),
	})

	listResp, err := svc.ListTabs(ctx, connect.NewRequest(&leapmuxv1.ListTabsRequest{}))
	require.NoError(t, err)
	require.Len(t, listResp.Msg.GetTabs(), 1)
	assert.Equal(t, pinned, listResp.Msg.GetTabs()[0].GetWorkspaceId(),
		"delegated ListTabs without org_id must use the pinned workspace org, not the user's home org")

	locateResp, err := svc.LocateTab(ctx, connect.NewRequest(&leapmuxv1.LocateTabRequest{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabId:   "shared-tab",
	}))
	require.NoError(t, err)
	assert.Equal(t, pinned, locateResp.Msg.GetTab().GetWorkspaceId(),
		"delegated LocateTab must resolve inside the pinned workspace before considering broader user access")
}

// TestWorkspaceService_LocateTile_FindsByWorkspaceRoot exercises the
// simplest happy path: a tile id that *is* a workspace root resolves
// to its workspace + org without walking any parent links. This pins
// the base case of `tileOwningWorkspace` against future regressions
// where a refactor of the walk loop accidentally skips the
// "current node is a root" check.
func TestWorkspaceService_LocateTile_FindsByWorkspaceRoot(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	orgID := storetest.SeedOrg(t, st, "primary-org")
	user := storetest.SeedUser(t, st, orgID, "alice")
	ws := storetest.SeedWorkspace(t, st, orgID, user.ID, "WS")

	env := setupLocateTileEnv(t, orgID)
	env.mgr.MutateInternal(func(s *leapmuxv1.OrgCrdtState) {
		s.Workspaces[ws] = &leapmuxv1.WorkspaceContentsRecord{WorkspaceId: ws, RootNodeId: "root-1"}
		s.Nodes["root-1"] = &leapmuxv1.NodeRecord{NodeId: "root-1"}
	})
	svc := service.NewWorkspaceService(st, env.registry, noopWorkspaceChannelCloser{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: user.ID, OrgID: orgID})

	resp, err := svc.LocateTile(ctx, connect.NewRequest(&leapmuxv1.LocateTileRequest{TileId: "root-1"}))
	require.NoError(t, err)
	assert.Equal(t, ws, resp.Msg.GetWorkspaceId())
	assert.Equal(t, orgID, resp.Msg.GetOrgId())
}

func seedRenderedTab(t *testing.T, st store.Store, orgID, workspaceID, tabID string) {
	t.Helper()
	require.NoError(t, st.WorkspaceTabIndex().UpsertRendered(context.Background(), store.UpsertRenderedTabParams{
		OrgID:       orgID,
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
	orgID := storetest.SeedOrg(t, st, "primary-org")
	user := storetest.SeedUser(t, st, orgID, "alice")
	ws := storetest.SeedWorkspace(t, st, orgID, user.ID, "WS")

	env := setupLocateTileEnv(t, orgID)
	env.mgr.MutateInternal(func(s *leapmuxv1.OrgCrdtState) {
		s.Workspaces[ws] = &leapmuxv1.WorkspaceContentsRecord{WorkspaceId: ws, RootNodeId: "root-1"}
		s.Nodes["root-1"] = &leapmuxv1.NodeRecord{NodeId: "root-1"}
		s.Nodes["mid-1"] = &leapmuxv1.NodeRecord{NodeId: "mid-1", ParentId: "root-1"}
		s.Nodes["leaf-1"] = &leapmuxv1.NodeRecord{NodeId: "leaf-1", ParentId: "mid-1"}
	})
	svc := service.NewWorkspaceService(st, env.registry, noopWorkspaceChannelCloser{})
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
	orgID := storetest.SeedOrg(t, st, "primary-org")
	user := storetest.SeedUser(t, st, orgID, "alice")
	allowedWS := storetest.SeedWorkspace(t, st, orgID, user.ID, "Allowed")
	forbiddenWS := storetest.SeedWorkspace(t, st, orgID, user.ID, "Forbidden")

	env := setupLocateTileEnv(t, orgID)
	env.mgr.MutateInternal(func(s *leapmuxv1.OrgCrdtState) {
		s.Workspaces[forbiddenWS] = &leapmuxv1.WorkspaceContentsRecord{WorkspaceId: forbiddenWS, RootNodeId: "root-forbidden"}
		s.Nodes["root-forbidden"] = &leapmuxv1.NodeRecord{NodeId: "root-forbidden"}
	})
	svc := service.NewWorkspaceService(st, env.registry, noopWorkspaceChannelCloser{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:         user.ID,
		OrgID:      orgID,
		Credential: auth.DelegationCredential("test-delegation", allowedWS, "worker-mint"),
	})

	_, err := svc.LocateTile(ctx, connect.NewRequest(&leapmuxv1.LocateTileRequest{TileId: "root-forbidden"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err),
		"a tile outside the delegation scope must surface as NotFound, not PermissionDenied (existence leak)")
}

func TestWorkspaceService_LocateTile_DelegationUsesPinnedWorkspaceOrg(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	homeOrgID := storetest.SeedOrg(t, st, "home-org")
	agentOrgID := storetest.SeedOrg(t, st, "agent-org")
	user := storetest.SeedUser(t, st, homeOrgID, "alice")
	pinned := storetest.SeedWorkspace(t, st, agentOrgID, user.ID, "Pinned")

	j := newMemJournal()
	var (
		once sync.Once
		mgr  *crdt.Manager
	)
	registry := crdt.NewRegistry(func(ctx context.Context, want string) (*crdt.Manager, error) {
		if want != agentOrgID {
			return nil, errors.New("unexpected org")
		}
		once.Do(func() {
			mgr = crdt.NewManager(agentOrgID, j, allowAllAuth{}, nil, time.Now)
			require.NoError(t, mgr.Bootstrap(ctx))
		})
		return mgr, nil
	}, nil)
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })
	_, err := registry.Get(context.Background(), agentOrgID)
	require.NoError(t, err)
	mgr.MutateInternal(func(s *leapmuxv1.OrgCrdtState) {
		s.Workspaces[pinned] = &leapmuxv1.WorkspaceContentsRecord{WorkspaceId: pinned, RootNodeId: "root-pinned"}
		s.Nodes["root-pinned"] = &leapmuxv1.NodeRecord{NodeId: "root-pinned"}
	})

	svc := service.NewWorkspaceService(st, registry, noopWorkspaceChannelCloser{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:         user.ID,
		OrgID:      homeOrgID,
		Credential: auth.DelegationCredential("test-delegation", pinned, "worker-mint"),
	})
	resp, err := svc.LocateTile(ctx, connect.NewRequest(&leapmuxv1.LocateTileRequest{TileId: "root-pinned"}))
	require.NoError(t, err)
	assert.Equal(t, pinned, resp.Msg.GetWorkspaceId())
	assert.Equal(t, agentOrgID, resp.Msg.GetOrgId(),
		"delegated LocateTile must use the pinned workspace org, not the user's home org")
}

// TestWorkspaceService_LocateTile_ForeignWorkspaceTileIsNotFound is the leak
// guard: a tile that exists only in ANOTHER user's workspace (in that user's
// org) must collapse to NotFound for the caller -- their personal org is the
// only one searched, and the final loadWorkspaceForRead check gates the rest.
func TestWorkspaceService_LocateTile_ForeignWorkspaceTileIsNotFound(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	homeOrg := storetest.SeedOrg(t, st, "viewer-home-org")
	ownerOrg := storetest.SeedOrg(t, st, "owner-org")
	viewer := storetest.SeedUser(t, st, homeOrg, "viewer")
	owner := storetest.SeedUser(t, st, ownerOrg, "owner")
	secretWS := storetest.SeedWorkspace(t, st, ownerOrg, owner.ID, "Secret")

	registry, managers := newMultiOrgRegistry(t, homeOrg, ownerOrg)
	managers[ownerOrg].MutateInternal(func(s *leapmuxv1.OrgCrdtState) {
		s.Workspaces[secretWS] = &leapmuxv1.WorkspaceContentsRecord{WorkspaceId: secretWS, RootNodeId: "root-secret"}
		s.Nodes["root-secret"] = &leapmuxv1.NodeRecord{NodeId: "root-secret"}
	})

	svc := service.NewWorkspaceService(st, registry, noopWorkspaceChannelCloser{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: viewer.ID, OrgID: homeOrg})

	_, err := svc.LocateTile(ctx, connect.NewRequest(&leapmuxv1.LocateTileRequest{TileId: "root-secret"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err),
		"a tile in another user's workspace must be NotFound, not leaked")
}

// TestWorkspaceService_LocateTile_RejectsEmptyTileID covers the
// invalid-args branch. Empty tile_id hard-fails before any auth or
// CRDT lookup so the error envelope is unambiguous.
func TestWorkspaceService_LocateTile_RejectsEmptyTileID(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	orgID := storetest.SeedOrg(t, st, "primary-org")
	user := storetest.SeedUser(t, st, orgID, "alice")
	env := setupLocateTileEnv(t, orgID)
	svc := service.NewWorkspaceService(st, env.registry, noopWorkspaceChannelCloser{})
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
	orgID := storetest.SeedOrg(t, st, "primary-org")
	user := storetest.SeedUser(t, st, orgID, "alice")
	env := setupLocateTileEnv(t, orgID)
	svc := service.NewWorkspaceService(st, env.registry, noopWorkspaceChannelCloser{})
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

// TestWorkspaceService_LocateTile_TransientOrgErrorWithNoMatchIsRetryable verifies
// the complement: when no org resolves the tile AND at least one org's Get failed
// transiently, the caller gets a retryable Internal (so it retries) rather than a
// false NotFound (which would tell it to stop looking for a tile that may exist).
func TestWorkspaceService_LocateTile_TransientOrgErrorWithNoMatchIsRetryable(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	homeOrg := storetest.SeedOrg(t, st, "viewer-home-org")
	viewer := storetest.SeedUser(t, st, homeOrg, "viewer")

	// The only candidate org (the home org) fails to bootstrap transiently.
	registry := crdt.NewRegistry(func(_ context.Context, _ string) (*crdt.Manager, error) {
		return nil, errors.New("transient bootstrap failure")
	}, nil, crdt.WithManagerIdleTTL(0))
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

	svc := service.NewWorkspaceService(st, registry, noopWorkspaceChannelCloser{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: viewer.ID, OrgID: homeOrg})
	_, err := svc.LocateTile(ctx, connect.NewRequest(&leapmuxv1.LocateTileRequest{TileId: "missing"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err),
		"an unresolved tile plus a transient org failure must be retryable Internal, not NotFound")
}

// newMultiOrgRegistry builds a CRDT registry that lazily serves an independent
// (memory-journal, allow-all-auth) manager per allowed org, and eagerly creates
// each so tests can MutateInternal state before the RPC. Returns the registry and
// the orgID -> Manager map. Any org not in orgIDs is rejected by the factory, so a
// test that walks an unexpected org fails loudly rather than silently.
func newMultiOrgRegistry(t *testing.T, orgIDs ...string) (*crdt.Registry, map[string]*crdt.Manager) {
	t.Helper()
	allowed := make(map[string]struct{}, len(orgIDs))
	for _, o := range orgIDs {
		allowed[o] = struct{}{}
	}
	var mu sync.Mutex
	managers := make(map[string]*crdt.Manager, len(orgIDs))
	registry := crdt.NewRegistry(func(ctx context.Context, want string) (*crdt.Manager, error) {
		if _, ok := allowed[want]; !ok {
			return nil, errors.New("unexpected org: " + want)
		}
		mu.Lock()
		defer mu.Unlock()
		if m, ok := managers[want]; ok {
			return m, nil
		}
		m := crdt.NewManager(want, newMemJournal(), allowAllAuth{}, nil, time.Now)
		if err := m.Bootstrap(ctx); err != nil {
			return nil, err
		}
		managers[want] = m
		return m, nil
	}, nil)
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })
	for _, o := range orgIDs {
		_, err := registry.Get(context.Background(), o)
		require.NoError(t, err)
	}
	return registry, managers
}
