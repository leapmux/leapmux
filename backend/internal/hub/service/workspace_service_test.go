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
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
)

func TestWorkspaceServiceDeleteWorkspaceFansOutToOwnersWorkersOnly(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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

// --- RenameWorkspace ---
//
// An archived workspace is read-only everywhere in the app: the tab bar's `+`
// disappears, the branch menu is hidden, and `isWorkspaceMutatable` names the
// rule. Hiding the menu item is not enforcement, though -- RenameWorkspace is
// reachable from the CLI and from any client -- so the hub refuses it too.
//
// The user goes through service.CreateUser rather than storetest.SeedUser:
// SeedUser writes the user row alone, and a user with no sections can never
// have an archived workspace, so every assertion below would pass vacuously.
// CreateUser is the production path and seeds the defaults in the same
// transaction.
func seedUserWithSections(t *testing.T, st store.Store, username string) *store.User {
	t.Helper()
	hash, err := password.Hash("testpass")
	require.NoError(t, err)
	user, err := service.CreateUser(context.Background(), st, service.CreateUserParams{
		Username:              username,
		PasswordHash:          hash,
		DisplayName:           username,
		FirstCredentialExempt: true,
	})
	require.NoError(t, err)
	return user
}

// The id of the seeded section of one built-in type, for `userID`.
func sectionIDOfType(t *testing.T, st store.Store, userID string, sectionType leapmuxv1.SectionType) string {
	t.Helper()
	sections, err := st.WorkspaceSections().ListByUserID(context.Background(), userid.MustNew(userID))
	require.NoError(t, err)
	for _, sec := range sections {
		if sec.SectionType == sectionType {
			return sec.ID
		}
	}
	require.FailNowf(t, "no seeded section", "type %v", sectionType)
	return ""
}

func placeWorkspaceInSection(t *testing.T, st store.Store, userID, workspaceID, sectionID string) {
	t.Helper()
	require.NoError(t, st.WorkspaceSectionItems().Set(context.Background(), store.SetWorkspaceSectionItemParams{
		UserID:      userid.MustNew(userID),
		WorkspaceID: workspaceID,
		SectionID:   sectionID,
		Position:    "n",
	}))
}

func TestWorkspaceService_RenameWorkspace_RenamesALiveWorkspace(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	ctx := context.Background()
	user := seedUserWithSections(t, st, "rename-live")
	workspaceID := storetest.SeedWorkspace(t, st, user.ID, "before")
	placeWorkspaceInSection(t, st, user.ID, workspaceID,
		sectionIDOfType(t, st, user.ID, leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS))

	svc := service.NewWorkspaceService(st, nil)
	authCtx := auth.WithUser(ctx, &auth.UserInfo{ID: userid.MustNew(user.ID)})
	_, err := svc.RenameWorkspace(authCtx, connect.NewRequest(&leapmuxv1.RenameWorkspaceRequest{
		WorkspaceId: workspaceID,
		Title:       "after",
	}))
	require.NoError(t, err)

	ws, err := st.Workspaces().GetByID(ctx, workspaceID)
	require.NoError(t, err)
	assert.Equal(t, "after", ws.Title)
}

// An UNPLACED workspace is not archived. A workspace created by the CLI has no
// section item until the client files it, and the sidebar shows it under In
// progress; refusing the rename there would break the ordinary case.
func TestWorkspaceService_RenameWorkspace_RenamesAnUnplacedWorkspace(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	ctx := context.Background()
	user := seedUserWithSections(t, st, "rename-unplaced")
	workspaceID := storetest.SeedWorkspace(t, st, user.ID, "before")

	svc := service.NewWorkspaceService(st, nil)
	authCtx := auth.WithUser(ctx, &auth.UserInfo{ID: userid.MustNew(user.ID)})
	_, err := svc.RenameWorkspace(authCtx, connect.NewRequest(&leapmuxv1.RenameWorkspaceRequest{
		WorkspaceId: workspaceID,
		Title:       "after",
	}))
	require.NoError(t, err)

	ws, err := st.Workspaces().GetByID(ctx, workspaceID)
	require.NoError(t, err)
	assert.Equal(t, "after", ws.Title)
}

func TestWorkspaceService_RenameWorkspace_RefusesArchived(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	ctx := context.Background()
	user := seedUserWithSections(t, st, "rename-archived")
	workspaceID := storetest.SeedWorkspace(t, st, user.ID, "before")
	placeWorkspaceInSection(t, st, user.ID, workspaceID,
		sectionIDOfType(t, st, user.ID, leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED))

	svc := service.NewWorkspaceService(st, nil)
	authCtx := auth.WithUser(ctx, &auth.UserInfo{ID: userid.MustNew(user.ID)})
	_, err := svc.RenameWorkspace(authCtx, connect.NewRequest(&leapmuxv1.RenameWorkspaceRequest{
		WorkspaceId: workspaceID,
		Title:       "after",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err),
		"the workspace is well-formed and owned; only its state forbids the write")
	assert.Contains(t, err.Error(), "archived")

	// The guard aborts the transaction, so the title is untouched.
	ws, getErr := st.Workspaces().GetByID(ctx, workspaceID)
	require.NoError(t, getErr)
	assert.Equal(t, "before", ws.Title)
}

// Unarchiving is a MoveWorkspace, which stays unguarded by design -- archive
// and unarchive are the same call, distinguished only by the section id. A
// workspace moved back out becomes renamable again.
func TestWorkspaceService_RenameWorkspace_AllowedAgainAfterUnarchive(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	ctx := context.Background()
	user := seedUserWithSections(t, st, "rename-unarchived")
	workspaceID := storetest.SeedWorkspace(t, st, user.ID, "before")
	placeWorkspaceInSection(t, st, user.ID, workspaceID,
		sectionIDOfType(t, st, user.ID, leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED))
	placeWorkspaceInSection(t, st, user.ID, workspaceID,
		sectionIDOfType(t, st, user.ID, leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS))

	svc := service.NewWorkspaceService(st, nil)
	authCtx := auth.WithUser(ctx, &auth.UserInfo{ID: userid.MustNew(user.ID)})
	_, err := svc.RenameWorkspace(authCtx, connect.NewRequest(&leapmuxv1.RenameWorkspaceRequest{
		WorkspaceId: workspaceID,
		Title:       "after",
	}))
	require.NoError(t, err)

	ws, err := st.Workspaces().GetByID(ctx, workspaceID)
	require.NoError(t, err)
	assert.Equal(t, "after", ws.Title)
}

// Another user's archived workspace does not make the caller's own workspace
// unrenamable: the query filters on the CALLER's section items.
func TestWorkspaceService_RenameWorkspace_ArchivedForAnotherUserDoesNotBlock(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	ctx := context.Background()
	owner := seedUserWithSections(t, st, "rename-owner")
	other := seedUserWithSections(t, st, "rename-other")
	workspaceID := storetest.SeedWorkspace(t, st, owner.ID, "before")
	placeWorkspaceInSection(t, st, owner.ID, workspaceID,
		sectionIDOfType(t, st, owner.ID, leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS))
	// The OTHER user files the same workspace id into THEIR archive.
	placeWorkspaceInSection(t, st, other.ID, workspaceID,
		sectionIDOfType(t, st, other.ID, leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED))

	svc := service.NewWorkspaceService(st, nil)
	authCtx := auth.WithUser(ctx, &auth.UserInfo{ID: userid.MustNew(owner.ID)})
	_, err := svc.RenameWorkspace(authCtx, connect.NewRequest(&leapmuxv1.RenameWorkspaceRequest{
		WorkspaceId: workspaceID,
		Title:       "after",
	}))
	require.NoError(t, err)

	ws, err := st.Workspaces().GetByID(ctx, workspaceID)
	require.NoError(t, err)
	assert.Equal(t, "after", ws.Title)
}

// Archive then delete is a normal flow, and Delete stays unguarded.
func TestWorkspaceService_DeleteWorkspace_StillWorksOnAnArchivedWorkspace(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	ctx := context.Background()
	user := seedUserWithSections(t, st, "delete-archived")
	workspaceID := storetest.SeedWorkspace(t, st, user.ID, "doomed")
	placeWorkspaceInSection(t, st, user.ID, workspaceID,
		sectionIDOfType(t, st, user.ID, leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED))

	svc := service.NewWorkspaceService(st, nil)
	authCtx := auth.WithUser(ctx, &auth.UserInfo{ID: userid.MustNew(user.ID)})
	_, err := svc.DeleteWorkspace(authCtx, connect.NewRequest(&leapmuxv1.DeleteWorkspaceRequest{
		WorkspaceId: workspaceID,
	}))
	require.NoError(t, err)

	_, getErr := st.Workspaces().GetByID(ctx, workspaceID)
	require.ErrorIs(t, getErr, store.ErrNotFound)
}
