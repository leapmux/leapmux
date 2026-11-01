package service_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
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
	// The stranger's tab sits in the stranger's own workspace: the composite
	// workspace key refuses a row whose user does not own its workspace.
	strangerWorkspaceID := storetest.SeedWorkspace(t, st, stranger.ID, "stranger's")

	require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, store.UpsertTabParams{
		UserID: userid.MustNew(owner.ID), WorkspaceID: workspaceID, WorkerID: ownerWorker.ID,
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "tab-owner", TileID: "tile", Position: "a0",
		Rendered: false,
	}))
	require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, store.UpsertTabParams{
		UserID: userid.MustNew(stranger.ID), WorkspaceID: strangerWorkspaceID, WorkerID: strangerWorker.ID,
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "tab-stranger", TileID: "tile", Position: "a1",
		Rendered: false,
	}))

	svc := service.NewWorkspaceService(st, nil, nil)
	authCtx := auth.WithUser(ctx, &auth.UserInfo{ID: userid.MustNew(owner.ID)})
	resp, err := svc.DeleteWorkspace(authCtx, &leapmuxv1.DeleteWorkspaceRequest{WorkspaceId: workspaceID})
	require.NoError(t, err)
	require.Len(t, resp.GetWorkerTabs(), 1,
		"a stranger's worker must not be swept into the owner's workspace deletion")
	assert.Equal(t, ownerWorker.ID, resp.GetWorkerTabs()[0].GetWorkerId())
	// The tabs travel with the worker, read inside the delete transaction. That
	// is what removed the caller's own pre-delete read -- and with it a race, a
	// swallowed error, and a projection subset that hid tabs entirely.
	tabs := resp.GetWorkerTabs()[0].GetTabs()
	require.Len(t, tabs, 1, "the response must name the tabs the worker has to tear down, and only those")
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_AGENT, tabs[0].GetTabType())
	assert.Equal(t, "tab-owner", tabs[0].GetTabId(), "the stranger's tab is not the owner's worker's to tear down")
}

// A workspace reports its creation time as an instant, read from the stored
// row, so a client never parses a formatted string.
func TestWorkspaceService_GetWorkspace_ReportsCreatedAtAsAnInstant(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	owner := storetest.SeedUser(t, st, "created-at-owner")
	wsID := storetest.SeedWorkspace(t, st, owner.ID, "Dated")
	row, err := st.Workspaces().GetByID(context.Background(), wsID)
	require.NoError(t, err)

	svc := service.NewWorkspaceService(st, nil, nil)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: userid.MustNew(owner.ID)})
	got, err := svc.GetWorkspace(ctx, &leapmuxv1.GetWorkspaceRequest{WorkspaceId: wsID})
	require.NoError(t, err)
	require.NotNil(t, got.GetWorkspace().GetCreatedAt())
	assert.True(t, got.GetWorkspace().GetCreatedAt().AsTime().Equal(row.CreatedAt),
		"created_at is the stored creation time, with no precision lost to formatting")

	listed, err := svc.ListWorkspaces(ctx, &leapmuxv1.ListWorkspacesRequest{})
	require.NoError(t, err)
	require.Len(t, listed.GetWorkspaces(), 1)
	assert.True(t, listed.GetWorkspaces()[0].GetCreatedAt().AsTime().Equal(row.CreatedAt),
		"the listing reports the same instant as the single read")
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

	resp, err := svc.ListWorkspaces(ctx, &leapmuxv1.ListWorkspacesRequest{})
	require.NoError(t, err)
	ids := make([]string, 0, len(resp.GetWorkspaces()))
	for _, ws := range resp.GetWorkspaces() {
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

	_, err := svc.GetWorkspace(ctx, &leapmuxv1.GetWorkspaceRequest{
		WorkspaceId: wsID,
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err),
		"a non-owner must be denied access to someone else's workspace")

	// The owner still reads it.
	ownerCtx := auth.WithUser(context.Background(), &auth.UserInfo{ID: userid.MustNew(owner.ID)})
	resp, err := svc.GetWorkspace(ownerCtx, &leapmuxv1.GetWorkspaceRequest{
		WorkspaceId: wsID,
	})
	require.NoError(t, err)
	assert.Equal(t, wsID, resp.GetWorkspace().GetId())
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

	resp, err := svc.LocateTile(ctx, &leapmuxv1.LocateTileRequest{TileId: "root-1"})
	require.NoError(t, err)
	assert.Equal(t, ws, resp.GetWorkspaceId())
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

	resp, err := svc.LocateTile(ctx, &leapmuxv1.LocateTileRequest{TileId: "leaf-1"})
	require.NoError(t, err)
	assert.Equal(t, ws, resp.GetWorkspaceId())
}

func TestWorkspaceService_LocateTile_RejectsEmptyTileID(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "alice")
	env := setupLocateTileEnv(t, user.ID)
	svc := service.NewWorkspaceService(st, env.registry, nil)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: userid.MustNew(user.ID)})

	_, err := svc.LocateTile(ctx, &leapmuxv1.LocateTileRequest{TileId: ""})
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

	_, err := svc.LocateTile(ctx, &leapmuxv1.LocateTileRequest{TileId: "ghost"})
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

	_, err := svc.LocateTile(ctx, &leapmuxv1.LocateTileRequest{TileId: "missing"})
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

// moveWorkspaceEnv is one user with the seeded sections, a custom section, and
// one workspace in In progress that hosts tabs on two workers.
type moveWorkspaceEnv struct {
	st           store.Store
	owner        userid.UserID
	workspaceID  string
	inProgressID string
	archivedID   string
	customID     string
	workerA      string
	workerB      string
	nudges       *nudgeRecorder
	svc          *service.WorkspaceService
}

func newMoveWorkspaceEnv(t *testing.T, username string) moveWorkspaceEnv {
	t.Helper()
	ctx := context.Background()
	st := hubtestutil.OpenTestStore(t)
	user := seedUserWithSections(t, st, username)
	owner := userid.MustNew(user.ID)
	env := moveWorkspaceEnv{
		st:           st,
		owner:        owner,
		workspaceID:  storetest.SeedWorkspace(t, st, user.ID, "target"),
		inProgressID: sectionIDOfType(t, st, user.ID, leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS),
		archivedID:   sectionIDOfType(t, st, user.ID, leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED),
		customID:     id.Generate(),
		nudges:       &nudgeRecorder{},
	}
	require.NoError(t, st.WorkspaceSections().Create(ctx, store.CreateWorkspaceSectionParams{
		ID: env.customID, UserID: owner, Name: "Later", Position: "z",
		SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM,
		Sidebar:     leapmuxv1.Sidebar_SIDEBAR_LEFT,
	}))
	placeWorkspaceInSection(t, st, user.ID, env.workspaceID, env.inProgressID)

	workerA := storetest.SeedWorker(t, st, user.ID)
	workerB := storetest.SeedWorker(t, st, user.ID)
	env.workerA, env.workerB = workerA.ID, workerB.ID
	for _, tab := range []store.UpsertTabParams{
		{UserID: owner, WorkspaceID: env.workspaceID, WorkerID: workerA.ID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "agent-a", TileID: "tile-a", Position: "a", Rendered: false},
		{UserID: owner, WorkspaceID: env.workspaceID, WorkerID: workerA.ID, TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabID: "file-a", TileID: "tile-a", Position: "b", Rendered: false},
		{UserID: owner, WorkspaceID: env.workspaceID, WorkerID: workerB.ID, TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabID: "terminal-b", TileID: "tile-b", Position: "c", Rendered: false},
	} {
		require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, tab))
	}
	env.svc = service.NewWorkspaceService(st, nil, env.nudges)
	return env
}

func (e moveWorkspaceEnv) move(ctx context.Context, sectionID, position string) error {
	_, err := e.svc.MoveWorkspace(ctx, &leapmuxv1.MoveWorkspaceRequest{
		WorkspaceId: e.workspaceID, SectionId: sectionID, Position: position,
	})
	return err
}

func (e moveWorkspaceEnv) item(t *testing.T) *store.WorkspaceSectionItem {
	t.Helper()
	item, err := e.st.WorkspaceSectionItems().Get(context.Background(), store.GetWorkspaceSectionItemParams{UserID: e.owner, WorkspaceID: e.workspaceID})
	require.NoError(t, err)
	return item
}

func (e moveWorkspaceEnv) ownerCtx() context.Context {
	return auth.WithUser(context.Background(), &auth.UserInfo{ID: e.owner})
}

func (e moveWorkspaceEnv) bearerCtx() context.Context {
	return auth.WithUser(context.Background(), &auth.UserInfo{ID: e.owner, Credential: auth.DelegationCredential("d1", "worker-mint")})
}

// The destination section decides the archive state, and the Hub owns the
// Worker delivery, so the nudge set IS the observable: every worker hosting one
// of the workspace's tabs is told once per move, including the one that hosts
// only a payload-backed tab.
func TestWorkspaceService_MoveWorkspace_ArchivesAndUnarchivesByTheSectionTypeAndNudges(t *testing.T) {
	t.Parallel()
	env := newMoveWorkspaceEnv(t, "move-archive")
	ctx := context.Background()
	existingArchived := storetest.SeedWorkspace(t, env.st, env.owner.String(), "existing archived")
	require.NoError(t, env.st.WorkspaceSectionItems().Set(ctx, store.SetWorkspaceSectionItemParams{
		UserID: env.owner, WorkspaceID: existingArchived, SectionID: env.archivedID, Position: "x",
	}))
	existingActive := storetest.SeedWorkspace(t, env.st, env.owner.String(), "existing active")
	require.NoError(t, env.st.WorkspaceSectionItems().Set(ctx, store.SetWorkspaceSectionItemParams{
		UserID: env.owner, WorkspaceID: existingActive, SectionID: env.inProgressID, Position: "y",
	}))

	require.NoError(t, env.move(env.ownerCtx(), env.archivedID, ""))
	archived := env.item(t)
	assert.Equal(t, env.archivedID, archived.SectionID, "into Archived archives the workspace")
	// The rank sorts after the destination's tail; its exact spelling carries a
	// per-workspace tie-break, so assert the ORDER rather than the literal.
	assert.Greater(t, archived.Position, "x", "an empty position appends after the destination tail")
	assert.ElementsMatch(t, []string{env.workerA, env.workerB}, env.nudges.snapshot())

	// A repeat is the caller's remedy after a Worker missed its nudge, so it
	// must nudge every one again, and must not move the workspace again, or
	// every retry would reorder the sidebar.
	require.NoError(t, env.move(env.ownerCtx(), env.archivedID, ""))
	assert.Len(t, env.nudges.snapshot(), 4, "a repeat nudges every affected Worker again")
	assert.Equal(t, archived.Position, env.item(t).Position, "a repeat keeps the workspace's rank")

	require.NoError(t, env.move(env.ownerCtx(), env.inProgressID, ""))
	assert.Len(t, env.nudges.snapshot(), 6, "out of Archived nudges every affected Worker too")
	unarchived := env.item(t)
	assert.Equal(t, env.inProgressID, unarchived.SectionID, "out of Archived unarchives the workspace")
	assert.Greater(t, unarchived.Position, "y")
}

// A move that stays on one side of the archive boundary still places the
// workspace exactly where the caller asked, and still nudges each hosting
// worker once: the nudge carries the archive state, and a repeat is the
// recovery path.
func TestWorkspaceService_MoveWorkspace_SameSideSetsThePositionAndNudgesOnce(t *testing.T) {
	t.Parallel()
	env := newMoveWorkspaceEnv(t, "move-same-side")

	require.NoError(t, env.move(env.ownerCtx(), env.customID, "q"))
	item := env.item(t)
	assert.Equal(t, env.customID, item.SectionID)
	assert.Equal(t, "q", item.Position, "an explicit rank is used verbatim, so a drop keeps its place")
	assert.ElementsMatch(t, []string{env.workerA, env.workerB}, env.nudges.snapshot())
}

// "Move to <custom section>" on an archived workspace is ONE transaction: it
// unarchives AND lands where the user dropped it, so the workspace never rests
// in In progress because a second call failed.
func TestWorkspaceService_MoveWorkspace_UnarchivesStraightIntoACustomSection(t *testing.T) {
	t.Parallel()
	env := newMoveWorkspaceEnv(t, "move-unarchive-custom")
	placeWorkspaceInSection(t, env.st, env.owner.String(), env.workspaceID, env.archivedID)

	require.NoError(t, env.move(env.ownerCtx(), env.customID, "q"))
	item := env.item(t)
	assert.Equal(t, env.customID, item.SectionID, "the workspace lands where the caller asked, not in In progress")
	assert.Equal(t, "q", item.Position)
}

// A prompt-injectable agent holds a delegation bearer. It may reorder the
// sidebar, but crossing the archive boundary stops or resumes every process of
// the workspace, so it may not do that.
func TestWorkspaceService_MoveWorkspace_RefusesADelegationBearerOnlyAcrossTheArchiveBoundary(t *testing.T) {
	t.Parallel()
	env := newMoveWorkspaceEnv(t, "move-bearer")

	require.NoError(t, env.move(env.bearerCtx(), env.customID, "q"), "a same-side move is allowed")
	assert.Equal(t, env.customID, env.item(t).SectionID)

	err := env.move(env.bearerCtx(), env.archivedID, "")
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Equal(t, env.customID, env.item(t).SectionID, "a refused move moves nothing")
	assert.Len(t, env.nudges.snapshot(), 2, "a refused move nudges nobody")
}

func TestWorkspaceService_MoveWorkspace_RefusesABadDestination(t *testing.T) {
	t.Parallel()
	env := newMoveWorkspaceEnv(t, "move-bad-destination")
	stranger := seedUserWithSections(t, env.st, "move-bad-destination-stranger")
	strangerSection := sectionIDOfType(t, env.st, stranger.ID, leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS)
	filesID := sectionIDOfType(t, env.st, env.owner.String(), leapmuxv1.SectionType_SECTION_TYPE_FILES)

	for _, tc := range []struct {
		name      string
		sectionID string
		want      connect.Code
	}{
		{"an empty section id", "", connect.CodeInvalidArgument},
		{"a section that cannot hold a workspace", filesID, connect.CodeInvalidArgument},
		// NotFound, so the parameter cannot probe which section ids exist.
		{"another user's section", strangerSection, connect.CodeNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := env.move(env.ownerCtx(), tc.sectionID, "")
			require.Error(t, err)
			assert.Equal(t, tc.want, connect.CodeOf(err))
			assert.Equal(t, env.inProgressID, env.item(t).SectionID, "a refused destination moves nothing")
		})
	}
	assert.Empty(t, env.nudges.snapshot(), "a refused move nudges nobody")
}

// TestWorkspaceService_MoveWorkspace_ConcurrentMovesGetDistinctRanks pins the
// rank tie-break. Two workspaces that move against the same destination tail
// must not land on the same lexorank: two items in a tie come back in
// planner-defined order, which the user sees as the sidebar reshuffling the
// whole set on every refresh. Sequencing the calls client-side does not fix it,
// because a second browser or a second device defeats that.
func TestWorkspaceService_MoveWorkspace_ConcurrentMovesGetDistinctRanks(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := hubtestutil.OpenTestStore(t)
	user := seedUserWithSections(t, st, "archive-rank-owner")
	owner := userid.MustNew(user.ID)
	inProgressID := sectionIDOfType(t, st, user.ID, leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS)
	archivedID := sectionIDOfType(t, st, user.ID, leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED)

	svc := service.NewWorkspaceService(st, nil, &nudgeRecorder{})
	authCtx := auth.WithUser(ctx, &auth.UserInfo{ID: owner})

	// Every workspace here moves into an EMPTY destination, so all three read
	// the same tail ("") and compute their rank from it -- which is what two
	// overlapping transactions observe at READ COMMITTED, without needing real
	// concurrency to reproduce it.
	positions := make(map[string]string)
	for _, name := range []string{"ws-one", "ws-two", "ws-three"} {
		workspaceID := storetest.SeedWorkspace(t, st, user.ID, name)
		require.NoError(t, st.WorkspaceSectionItems().Set(ctx, store.SetWorkspaceSectionItemParams{
			UserID: owner, WorkspaceID: workspaceID, SectionID: inProgressID, Position: "n",
		}))
		_, err := svc.MoveWorkspace(authCtx, &leapmuxv1.MoveWorkspaceRequest{
			WorkspaceId: workspaceID,
			SectionId:   archivedID,
		})
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

func TestWorkspaceService_MoveWorkspace_RefusesForeignWorkspace(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	owner := seedUserWithSections(t, st, "archive-owner")
	stranger := seedUserWithSections(t, st, "archive-stranger")
	workspaceID := storetest.SeedWorkspace(t, st, owner.ID, "owned")
	svc := service.NewWorkspaceService(st, nil, nil)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: userid.MustNew(stranger.ID)})

	_, err := svc.MoveWorkspace(ctx, &leapmuxv1.MoveWorkspaceRequest{
		WorkspaceId: workspaceID,
		SectionId:   sectionIDOfType(t, st, stranger.ID, leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED),
	})
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

func TestWorkspaceService_MoveWorkspace_RollsBackTransactionFailures(t *testing.T) {
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

			_, err := svc.MoveWorkspace(ctx, &leapmuxv1.MoveWorkspaceRequest{
				WorkspaceId: workspaceID,
				SectionId:   sectionIDOfType(t, base, user.ID, leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED),
			})
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
	_, err := svc.RenameWorkspace(authCtx, &leapmuxv1.RenameWorkspaceRequest{
		WorkspaceId: workspaceID,
		Title:       "after",
	})
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
	_, err := svc.RenameWorkspace(authCtx, &leapmuxv1.RenameWorkspaceRequest{
		WorkspaceId: workspaceID,
		Title:       "after",
	})
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
	_, err := svc.RenameWorkspace(authCtx, &leapmuxv1.RenameWorkspaceRequest{
		WorkspaceId: workspaceID,
		Title:       "after",
	})
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
	_, err := svc.RenameWorkspace(authCtx, &leapmuxv1.RenameWorkspaceRequest{
		WorkspaceId: workspaceID,
		Title:       "after",
	})
	require.NoError(t, err)

	ws, err := st.Workspaces().GetByID(ctx, workspaceID)
	require.NoError(t, err)
	assert.Equal(t, "after", ws.Title)
}

// Another user cannot archive the caller's workspace, so the caller's own
// workspace stays renamable: the composite placement key refuses a second
// user's placement of it outright.
func TestWorkspaceService_RenameWorkspace_AnotherUserCannotArchiveTheWorkspace(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	ctx := context.Background()
	owner := seedUserWithSections(t, st, "rename-owner")
	other := seedUserWithSections(t, st, "rename-other")
	workspaceID := storetest.SeedWorkspace(t, st, owner.ID, "before")
	placeWorkspaceInSection(t, st, owner.ID, workspaceID,
		sectionIDOfType(t, st, owner.ID, leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS))
	// The OTHER user tries to file the same workspace id into THEIR archive.
	require.Error(t, st.WorkspaceSectionItems().Set(ctx, store.SetWorkspaceSectionItemParams{
		UserID:      userid.MustNew(other.ID),
		WorkspaceID: workspaceID,
		SectionID:   sectionIDOfType(t, st, other.ID, leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED),
		Position:    "n",
	}), "a placement of another user's workspace must not be storable")

	svc := service.NewWorkspaceService(st, nil, nil)
	authCtx := auth.WithUser(ctx, &auth.UserInfo{ID: userid.MustNew(owner.ID)})
	_, err := svc.RenameWorkspace(authCtx, &leapmuxv1.RenameWorkspaceRequest{
		WorkspaceId: workspaceID,
		Title:       "after",
	})
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
	_, err := svc.DeleteWorkspace(authCtx, &leapmuxv1.DeleteWorkspaceRequest{
		WorkspaceId: workspaceID,
	})
	require.NoError(t, err)

	_, getErr := st.Workspaces().GetByID(ctx, workspaceID)
	require.ErrorIs(t, getErr, store.ErrNotFound)

	// The placement goes with it. A section item states where a workspace sits,
	// so one for a workspace that no longer exists states nothing. The delete is
	// a SOFT delete, so the ON DELETE CASCADE on workspace_id never fires and
	// the row would otherwise stay for the life of the account.
	_, itemErr := st.WorkspaceSectionItems().Get(ctx, store.GetWorkspaceSectionItemParams{
		UserID:      userid.MustNew(user.ID),
		WorkspaceID: workspaceID,
	})
	require.ErrorIs(t, itemErr, store.ErrNotFound)
}
