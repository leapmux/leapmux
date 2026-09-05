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
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type nudgeRecorder struct {
	mu  sync.Mutex
	ids []string
}

func (r *nudgeRecorder) NudgeReconcile(workerID string) {
	r.mu.Lock()
	r.ids = append(r.ids, workerID)
	r.mu.Unlock()
}

func (r *nudgeRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ids...)
}

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

	svc := service.NewWorkspaceService(st, nil, nil)
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

	svc := service.NewWorkspaceService(st, nil, nil)
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

	svc := service.NewWorkspaceService(st, nil, nil)
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
	svc := service.NewWorkspaceService(st, env.registry, nil)
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
	svc := service.NewWorkspaceService(st, env.registry, nil)
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
	svc := service.NewWorkspaceService(st, env.registry, nil)
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
	svc := service.NewWorkspaceService(st, env.registry, nil)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: userid.MustNew(user.ID)})

	_, err := svc.LocateTile(ctx, connect.NewRequest(&leapmuxv1.LocateTileRequest{TileId: "ghost"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// TestWorkspaceService_LocateTile_TransientManagerErrorIsRetryable pins the
// registry.Get failure case: when the caller's manager cannot be bootstrapped,
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

	svc := service.NewWorkspaceService(st, registry, nil)
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
// disappears, the branch menu is hidden, and `isWorkspaceMutatable` states the
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
	// A literal, not `password.Hash`: nothing here verifies a password, and
	// deriving one costs an Argon2id pass over 19 MiB per call. `storetest`
	// seeds the same way.
	user, err := service.CreateUser(context.Background(), st, service.CreateUserParams{
		Username:              username,
		PasswordHash:          "hash-" + username,
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

func TestWorkspaceService_SetWorkspaceArchiveState_TransitionsAndGroupsWorkers(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	ctx := context.Background()
	user := seedUserWithSections(t, st, "archive-lifecycle")
	owner := userid.MustNew(user.ID)
	workspaceID := storetest.SeedWorkspace(t, st, user.ID, "target")
	inProgressID := sectionIDOfType(t, st, user.ID, leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS)
	archivedID := sectionIDOfType(t, st, user.ID, leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED)
	placeWorkspaceInSection(t, st, user.ID, workspaceID, inProgressID)

	existingArchived := storetest.SeedWorkspace(t, st, user.ID, "existing archived")
	require.NoError(t, st.WorkspaceSectionItems().Set(ctx, store.SetWorkspaceSectionItemParams{
		UserID: owner, WorkspaceID: existingArchived, SectionID: archivedID, Position: "x",
	}))
	existingActive := storetest.SeedWorkspace(t, st, user.ID, "existing active")
	require.NoError(t, st.WorkspaceSectionItems().Set(ctx, store.SetWorkspaceSectionItemParams{
		UserID: owner, WorkspaceID: existingActive, SectionID: inProgressID, Position: "y",
	}))

	workerA := storetest.SeedWorker(t, st, user.ID)
	workerB := storetest.SeedWorker(t, st, user.ID)
	for _, tab := range []store.UpsertOwnedTabParams{
		{UserID: owner, WorkspaceID: workspaceID, WorkerID: workerA.ID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "agent-a", TileID: "tile-a", Position: "a"},
		{UserID: owner, WorkspaceID: workspaceID, WorkerID: workerA.ID, TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabID: "file-a", TileID: "tile-a", Position: "b"},
		{UserID: owner, WorkspaceID: workspaceID, WorkerID: workerB.ID, TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabID: "terminal-b", TileID: "tile-b", Position: "c"},
	} {
		require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, tab))
	}

	nudges := &nudgeRecorder{}
	svc := service.NewWorkspaceService(st, nil, nudges)
	authCtx := auth.WithUser(ctx, &auth.UserInfo{ID: owner})
	archive := func(state leapmuxv1.WorkspaceArchiveState) {
		_, err := svc.SetWorkspaceArchiveState(authCtx, connect.NewRequest(&leapmuxv1.SetWorkspaceArchiveStateRequest{
			WorkspaceId: workspaceID, ArchiveState: state,
		}))
		require.NoError(t, err)
	}

	archive(leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED)
	// The Hub owns the Worker delivery, so the nudge set IS the observable.
	// Every worker hosting one of this workspace's tabs must be told once,
	// including the one that hosts only a payload-backed tab.
	item, err := st.WorkspaceSectionItems().Get(ctx, store.GetWorkspaceSectionItemParams{UserID: owner, WorkspaceID: workspaceID})
	require.NoError(t, err)
	assert.Equal(t, archivedID, item.SectionID)
	// The rank sorts after the destination's tail; its exact spelling carries a
	// per-workspace tie-break, so assert the ORDER rather than the literal.
	archivedPosition := item.Position
	assert.Greater(t, archivedPosition, "x", "the Hub appends after the current destination tail")
	assert.ElementsMatch(t, []string{workerA.ID, workerB.ID}, nudges.snapshot())

	// A repeat is the caller's remedy after a Worker missed its nudge, so it
	// must nudge every one again. Only the section move is skipped.
	archive(leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED)
	assert.Len(t, nudges.snapshot(), 4, "a repeat nudges every affected Worker again")
	item, err = st.WorkspaceSectionItems().Get(ctx, store.GetWorkspaceSectionItemParams{UserID: owner, WorkspaceID: workspaceID})
	require.NoError(t, err)
	assert.Equal(t, archivedPosition, item.Position,
		"a repeat must not move the workspace again, or every retry would reorder the sidebar")

	archive(leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ACTIVE)
	assert.Len(t, nudges.snapshot(), 6, "the unarchive nudges every affected Worker too")
	item, err = st.WorkspaceSectionItems().Get(ctx, store.GetWorkspaceSectionItemParams{UserID: owner, WorkspaceID: workspaceID})
	require.NoError(t, err)
	assert.Equal(t, inProgressID, item.SectionID)
	assert.Greater(t, item.Position, "y")
}

// TestWorkspaceService_SetWorkspaceArchiveState_HonorsAnExplicitDestination
// pins the parameter that made a boundary-crossing move ONE transaction.
//
// "Move to <custom section>" on an archived workspace used to unarchive into In
// progress and then issue a second MoveWorkspace. When the second call failed
// the workspace came to rest in a section the user never chose, and the drag
// position was thrown away because the lifecycle call always appended.
//
// The guard matters as much as the feature: a destination on the wrong side of
// the boundary must be REFUSED, or this parameter becomes a second way to
// archive a workspace, bypassing everything the lifecycle call does.
func TestWorkspaceService_SetWorkspaceArchiveState_HonorsAnExplicitDestination(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := hubtestutil.OpenTestStore(t)
	user := seedUserWithSections(t, st, "archive-destination")
	owner := userid.MustNew(user.ID)
	inProgressID := sectionIDOfType(t, st, user.ID, leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS)
	archivedID := sectionIDOfType(t, st, user.ID, leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED)
	customID := id.Generate()
	require.NoError(t, st.WorkspaceSections().Create(ctx, store.CreateWorkspaceSectionParams{
		ID: customID, UserID: owner, Name: "Later", Position: "z",
		SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM,
		Sidebar:     leapmuxv1.Sidebar_SIDEBAR_LEFT,
	}))

	workspaceID := storetest.SeedWorkspace(t, st, user.ID, "target")
	placeWorkspaceInSection(t, st, user.ID, workspaceID, archivedID)

	svc := service.NewWorkspaceService(st, nil, &nudgeRecorder{})
	authCtx := auth.WithUser(ctx, &auth.UserInfo{ID: owner})

	// Unarchive straight into the custom section, at a chosen rank.
	_, err := svc.SetWorkspaceArchiveState(authCtx, connect.NewRequest(&leapmuxv1.SetWorkspaceArchiveStateRequest{
		WorkspaceId:          workspaceID,
		ArchiveState:         leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ACTIVE,
		DestinationSectionId: customID,
		Position:             "q",
	}))
	require.NoError(t, err)
	item, err := st.WorkspaceSectionItems().Get(ctx, store.GetWorkspaceSectionItemParams{UserID: owner, WorkspaceID: workspaceID})
	require.NoError(t, err)
	assert.Equal(t, customID, item.SectionID, "the workspace lands where the caller asked, not in In progress")
	assert.Equal(t, "q", item.Position, "an explicit rank is used verbatim, so a drop keeps its place")

	// A destination on the WRONG side is refused, and nothing moves.
	_, err = svc.SetWorkspaceArchiveState(authCtx, connect.NewRequest(&leapmuxv1.SetWorkspaceArchiveStateRequest{
		WorkspaceId:          workspaceID,
		ArchiveState:         leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ACTIVE,
		DestinationSectionId: archivedID,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	item, err = st.WorkspaceSectionItems().Get(ctx, store.GetWorkspaceSectionItemParams{UserID: owner, WorkspaceID: workspaceID})
	require.NoError(t, err)
	assert.Equal(t, customID, item.SectionID, "a refused destination moves nothing")

	// A destination that CANNOT hold a workspace is refused. The archive move
	// and the sidebar move share requireWorkspaceSection, so neither can start
	// accepting a section type the other refuses.
	filesID := sectionIDOfType(t, st, user.ID, leapmuxv1.SectionType_SECTION_TYPE_FILES)
	_, err = svc.SetWorkspaceArchiveState(authCtx, connect.NewRequest(&leapmuxv1.SetWorkspaceArchiveStateRequest{
		WorkspaceId:          workspaceID,
		ArchiveState:         leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ACTIVE,
		DestinationSectionId: filesID,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	// A destination belonging to ANOTHER user is refused, and as NotFound, so
	// the parameter cannot be used to probe which section ids exist.
	stranger := seedUserWithSections(t, st, "archive-destination-stranger")
	strangerSection := sectionIDOfType(t, st, stranger.ID, leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS)
	_, err = svc.SetWorkspaceArchiveState(authCtx, connect.NewRequest(&leapmuxv1.SetWorkspaceArchiveStateRequest{
		WorkspaceId:          workspaceID,
		ArchiveState:         leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ACTIVE,
		DestinationSectionId: strangerSection,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	item, err = st.WorkspaceSectionItems().Get(ctx, store.GetWorkspaceSectionItemParams{UserID: owner, WorkspaceID: workspaceID})
	require.NoError(t, err)
	assert.Equal(t, customID, item.SectionID, "no refused destination moves the workspace")

	// No destination keeps the built-in behaviour.
	_, err = svc.SetWorkspaceArchiveState(authCtx, connect.NewRequest(&leapmuxv1.SetWorkspaceArchiveStateRequest{
		WorkspaceId:  workspaceID,
		ArchiveState: leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED,
	}))
	require.NoError(t, err)
	item, err = st.WorkspaceSectionItems().Get(ctx, store.GetWorkspaceSectionItemParams{UserID: owner, WorkspaceID: workspaceID})
	require.NoError(t, err)
	assert.Equal(t, archivedID, item.SectionID)
	assert.NotEqual(t, inProgressID, item.SectionID)
}

// TestWorkspaceService_SetWorkspaceArchiveState_ConcurrentMovesGetDistinctRanks
// pins the rank tie-break. Two workspaces that cross the boundary against the
// same destination tail must not land on the same lexorank: two items in a tie
// come back in planner-defined order, which the user sees as the sidebar
// reshuffling the whole set on every refresh. Sequencing the calls client-side
// does not fix it, because a second browser or a second device defeats that.
func TestWorkspaceService_SetWorkspaceArchiveState_ConcurrentMovesGetDistinctRanks(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := hubtestutil.OpenTestStore(t)
	user := seedUserWithSections(t, st, "archive-rank-owner")
	owner := userid.MustNew(user.ID)
	inProgressID := sectionIDOfType(t, st, user.ID, leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS)

	svc := service.NewWorkspaceService(st, nil, &nudgeRecorder{})
	authCtx := auth.WithUser(ctx, &auth.UserInfo{ID: owner})

	// Every workspace here archives out of an EMPTY destination, so all three
	// read the same tail ("") and compute their rank from it -- which is what
	// two overlapping transactions observe at READ COMMITTED, without needing
	// real concurrency to reproduce it.
	positions := make(map[string]string)
	for _, name := range []string{"ws-one", "ws-two", "ws-three"} {
		workspaceID := storetest.SeedWorkspace(t, st, user.ID, name)
		require.NoError(t, st.WorkspaceSectionItems().Set(ctx, store.SetWorkspaceSectionItemParams{
			UserID: owner, WorkspaceID: workspaceID, SectionID: inProgressID, Position: "n",
		}))
		_, err := svc.SetWorkspaceArchiveState(authCtx, connect.NewRequest(&leapmuxv1.SetWorkspaceArchiveStateRequest{
			WorkspaceId:  workspaceID,
			ArchiveState: leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED,
		}))
		require.NoError(t, err)
		item, err := st.WorkspaceSectionItems().Get(ctx, store.GetWorkspaceSectionItemParams{
			UserID: owner, WorkspaceID: workspaceID,
		})
		require.NoError(t, err)
		// Delete the row again, so the next workspace reads the same empty
		// tail this one did instead of appending after it.
		require.NoError(t, st.WorkspaceSectionItems().Delete(ctx, store.DeleteWorkspaceSectionItemParams{
			UserID: owner, WorkspaceID: workspaceID,
		}))
		for other, position := range positions {
			assert.NotEqual(t, position, item.Position,
				"%s and %s must not share a rank in the destination section", other, name)
		}
		positions[name] = item.Position
	}
	assert.Len(t, positions, 3)
}

func TestWorkspaceService_SetWorkspaceArchiveState_RefusesForeignWorkspace(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	owner := seedUserWithSections(t, st, "archive-owner")
	stranger := seedUserWithSections(t, st, "archive-stranger")
	workspaceID := storetest.SeedWorkspace(t, st, owner.ID, "owned")
	svc := service.NewWorkspaceService(st, nil, nil)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: userid.MustNew(stranger.ID)})

	_, err := svc.SetWorkspaceArchiveState(ctx, connect.NewRequest(&leapmuxv1.SetWorkspaceArchiveStateRequest{
		WorkspaceId:  workspaceID,
		ArchiveState: leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

type archiveFailureStore struct {
	store.Store
	failMove bool
	failTabs bool
}

func (s archiveFailureStore) RunInTransaction(ctx context.Context, fn func(store.Store) error) error {
	return s.Store.RunInTransaction(ctx, func(tx store.Store) error {
		return fn(archiveFailureStore{Store: tx, failMove: s.failMove, failTabs: s.failTabs})
	})
}

func (s archiveFailureStore) WorkspaceSectionItems() store.WorkspaceSectionItemStore {
	return archiveFailureSectionItems{WorkspaceSectionItemStore: s.Store.WorkspaceSectionItems(), fail: s.failMove}
}

func (s archiveFailureStore) WorkspaceTabIndex() store.WorkspaceTabIndexStore {
	return archiveFailureTabIndex{WorkspaceTabIndexStore: s.Store.WorkspaceTabIndex(), fail: s.failTabs}
}

type archiveFailureSectionItems struct {
	store.WorkspaceSectionItemStore
	fail bool
}

func (s archiveFailureSectionItems) Set(ctx context.Context, p store.SetWorkspaceSectionItemParams) error {
	if s.fail {
		return errors.New("forced section move failure")
	}
	return s.WorkspaceSectionItemStore.Set(ctx, p)
}

type archiveFailureTabIndex struct {
	store.WorkspaceTabIndexStore
	fail bool
}

func (s archiveFailureTabIndex) ListOwnedTabsByWorkspace(ctx context.Context, p store.ListOwnedTabsByWorkspaceParams) ([]store.OwnedTabRef, error) {
	if s.fail {
		return nil, errors.New("forced tab read failure")
	}
	return s.WorkspaceTabIndexStore.ListOwnedTabsByWorkspace(ctx, p)
}

func TestWorkspaceService_SetWorkspaceArchiveState_RollsBackTransactionFailures(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name     string
		username string
		failMove bool
		failTabs bool
	}{
		{name: "section move", username: "section-move", failMove: true},
		{name: "tab read", username: "tab-read", failTabs: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			base := hubtestutil.OpenTestStore(t)
			user := seedUserWithSections(t, base, "archive-failure-"+testCase.username)
			owner := userid.MustNew(user.ID)
			workspaceID := storetest.SeedWorkspace(t, base, user.ID, "target")
			inProgressID := sectionIDOfType(t, base, user.ID, leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS)
			placeWorkspaceInSection(t, base, user.ID, workspaceID, inProgressID)
			nudges := &nudgeRecorder{}
			svc := service.NewWorkspaceService(archiveFailureStore{
				Store: base, failMove: testCase.failMove, failTabs: testCase.failTabs,
			}, nil, nudges)
			ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: owner})

			_, err := svc.SetWorkspaceArchiveState(ctx, connect.NewRequest(&leapmuxv1.SetWorkspaceArchiveStateRequest{
				WorkspaceId:  workspaceID,
				ArchiveState: leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED,
			}))
			require.Error(t, err)
			assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
			item, getErr := base.WorkspaceSectionItems().Get(context.Background(), store.GetWorkspaceSectionItemParams{
				UserID: owner, WorkspaceID: workspaceID,
			})
			require.NoError(t, getErr)
			assert.Equal(t, inProgressID, item.SectionID)
			assert.Empty(t, nudges.snapshot())
		})
	}
}

func TestWorkspaceService_RenameWorkspace_RenamesALiveWorkspace(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	ctx := context.Background()
	user := seedUserWithSections(t, st, "rename-live")
	workspaceID := storetest.SeedWorkspace(t, st, user.ID, "before")
	placeWorkspaceInSection(t, st, user.ID, workspaceID,
		sectionIDOfType(t, st, user.ID, leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS))

	svc := service.NewWorkspaceService(st, nil, nil)
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

	svc := service.NewWorkspaceService(st, nil, nil)
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

	svc := service.NewWorkspaceService(st, nil, nil)
	authCtx := auth.WithUser(ctx, &auth.UserInfo{ID: userid.MustNew(user.ID)})
	_, err := svc.RenameWorkspace(authCtx, connect.NewRequest(&leapmuxv1.RenameWorkspaceRequest{
		WorkspaceId: workspaceID,
		Title:       "after",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err),
		"the workspace is well-formed and owned; only its state forbids the write")
	// The VERB, not just "archived": the guard takes it as a parameter so the
	// next mutation that adopts it cannot report a rename that never ran.
	assert.Contains(t, err.Error(), "cannot rename an archived workspace")

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

	svc := service.NewWorkspaceService(st, nil, nil)
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

	svc := service.NewWorkspaceService(st, nil, nil)
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

	svc := service.NewWorkspaceService(st, nil, nil)
	authCtx := auth.WithUser(ctx, &auth.UserInfo{ID: userid.MustNew(user.ID)})
	_, err := svc.DeleteWorkspace(authCtx, connect.NewRequest(&leapmuxv1.DeleteWorkspaceRequest{
		WorkspaceId: workspaceID,
	}))
	require.NoError(t, err)

	_, getErr := st.Workspaces().GetByID(ctx, workspaceID)
	require.ErrorIs(t, getErr, store.ErrNotFound)
}
