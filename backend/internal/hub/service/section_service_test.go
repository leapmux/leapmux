package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/internal/util/userid"

	"connectrpc.com/connect"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"

	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
)

type sectionTestEnv struct {
	client leapmuxv1connect.SectionServiceClient
	store  store.Store
	token  string
	userID string
}

func setupSectionTest(t *testing.T) *sectionTestEnv {
	t.Helper()

	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	err = st.Migrator().Migrate(context.Background())
	require.NoError(t, err)

	sectionSvc := service.NewSectionService(st)

	mux := http.NewServeMux()
	interceptor, _ := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st})
	opts := connect.WithInterceptors(interceptor)
	path, handler := leapmuxv1connect.NewSectionServiceHandler(sectionSvc, opts)
	mux.Handle(path, handler)

	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewSectionServiceClient(
		server.Client(),
		server.URL,
		connect.WithGRPC(),
	)

	hash, _ := password.Hash("testpass")

	// Route through CreateUser, the production path, because that is what seeds
	// the default sections. ListSections only reads: a user made with a bare
	// Users().Create has no sections at all, and every assertion below would be
	// measuring the wrong thing.
	user, err := service.CreateUser(context.Background(), st, service.CreateUserParams{
		Username:     "testuser",
		PasswordHash: hash,
		DisplayName:  "Test",
		PasswordSet:  true,
		IsAdmin:      true,
	})
	require.NoError(t, err)
	userID := user.ID

	token, _, _, err := auth.Login(context.Background(), st, "testuser", "testpass", auth.DefaultSessionDuration)
	require.NoError(t, err)

	return &sectionTestEnv{
		client: client,
		store:  st,
		token:  token,
		userID: userID,
	}
}

func TestSectionService_ListSections_ReturnsSeededDefaults(t *testing.T) {
	t.Parallel()

	env := setupSectionTest(t)

	resp, err := env.client.ListSections(context.Background(), authedReq(
		&leapmuxv1.ListSectionsRequest{}, env.token))
	require.NoError(t, err)

	// CreateUser seeded all six: In progress, Archived, Workers (left), Files,
	// To-dos, Background tasks (right).
	sections := resp.Msg.GetSections()
	require.Len(t, sections, 6)

	var hasInProgress, hasArchived, hasWorkers, hasFiles, hasTodos bool
	for _, s := range sections {
		switch s.GetSectionType() {
		case leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS:
			hasInProgress = true
			assert.Equal(t, "In progress", s.GetName())
			assert.Equal(t, leapmuxv1.Sidebar_SIDEBAR_LEFT, s.GetSidebar())
		case leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED:
			hasArchived = true
			assert.Equal(t, "Archived", s.GetName())
			assert.Equal(t, leapmuxv1.Sidebar_SIDEBAR_LEFT, s.GetSidebar())
		case leapmuxv1.SectionType_SECTION_TYPE_WORKERS:
			hasWorkers = true
			assert.Equal(t, "Workers", s.GetName())
			assert.Equal(t, leapmuxv1.Sidebar_SIDEBAR_LEFT, s.GetSidebar())
		case leapmuxv1.SectionType_SECTION_TYPE_FILES:
			hasFiles = true
			assert.Equal(t, "Files", s.GetName())
			assert.Equal(t, leapmuxv1.Sidebar_SIDEBAR_RIGHT, s.GetSidebar())
		case leapmuxv1.SectionType_SECTION_TYPE_TODOS:
			hasTodos = true
			assert.Equal(t, "To-dos", s.GetName())
			assert.Equal(t, leapmuxv1.Sidebar_SIDEBAR_RIGHT, s.GetSidebar())
		default:
			// UNSPECIFIED and WORKSPACES_CUSTOM are not seeded defaults, so the
			// per-type assertions above do not apply to them.
		}
	}
	assert.True(t, hasInProgress, "missing in_progress section")
	assert.True(t, hasArchived, "missing archived section")
	assert.True(t, hasWorkers, "missing workers section")
	assert.True(t, hasFiles, "missing files section")
	assert.True(t, hasTodos, "missing todos section")

	// Ordering is the half the checks above cannot see: they confirm each
	// section EXISTS with the right name and sidebar, which stays true even if
	// every position collapsed to the same rank or the two sidebars were ranked
	// against one shared chain. Assert the ranks directly, per sidebar, so the
	// order the user actually sees is pinned rather than inferred.
	positions := map[leapmuxv1.SectionType]string{}
	for _, s := range sections {
		positions[s.GetSectionType()] = s.GetPosition()
	}
	for _, chain := range [][]leapmuxv1.SectionType{
		{
			leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS,
			leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED,
			leapmuxv1.SectionType_SECTION_TYPE_WORKERS,
		},
		{
			leapmuxv1.SectionType_SECTION_TYPE_FILES,
			leapmuxv1.SectionType_SECTION_TYPE_TODOS,
		},
	} {
		for i := 1; i < len(chain); i++ {
			prev, cur := positions[chain[i-1]], positions[chain[i]]
			require.NotEmpty(t, prev)
			require.NotEmpty(t, cur)
			assert.Less(t, prev, cur, "%v must rank before %v within its sidebar", chain[i-1], chain[i])
		}
	}
	// Each sidebar starts its own chain, so the two first entries share a rank.
	// That is the property a single shared chain would break.
	assert.Equal(t,
		positions[leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS],
		positions[leapmuxv1.SectionType_SECTION_TYPE_FILES],
		"each sidebar is ranked independently, so both start at the same first rank")
}

// ListSections must not write. It used to seed the defaults itself with a
// check-then-insert that no transaction and no uniqueness constraint
// serialized, so concurrent callers each saw an empty list and each wrote a
// full set -- and the sidebar rendered two of every section, with the
// duplicates indistinguishable from one another.
//
// The test starts from a user with NO sections, which is the ONLY state the old
// code's `if len(sections) == 0` seeding branch reacted to. Leaving the
// CreateUser-seeded rows in place would make every caller take the non-empty
// path, so the old implementation would pass this test unchanged -- the
// concurrency would prove nothing. Starting empty makes the assertion "no
// section appeared" fail against the old code (which writes 6 to 48 rows here)
// and hold against a pure read.
func TestSectionService_ListSections_NeverWrites(t *testing.T) {
	t.Parallel()

	env := setupSectionTest(t)
	ctx := context.Background()

	// A SECOND user, created straight through the store, so it has no sections.
	// A default section cannot be deleted (the query filters section_type = 1,
	// custom only), so bypassing the seeding is the only way to reach the state
	// the old code's `if len(sections) == 0` branch reacted to.
	hash, err := password.Hash("testpass")
	require.NoError(t, err)
	bareID := id.Generate()
	require.NoError(t, env.store.Users().Create(ctx, store.CreateUserParams{
		ID:           bareID,
		Username:     "unseeded",
		PasswordHash: hash,
		DisplayName:  "Unseeded",
		PasswordSet:  true,
	}))
	bareToken, _, _, err := auth.Login(ctx, env.store, "unseeded", "testpass", auth.DefaultSessionDuration)
	require.NoError(t, err)
	userID := userid.MustNew(bareID)

	require.Empty(t, mustListSections(t, env, userID),
		"the bypassing path must genuinely leave the user unseeded")

	const callers = 8
	start := make(chan struct{})
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, callErr := env.client.ListSections(ctx, authedReq(
				&leapmuxv1.ListSectionsRequest{}, bareToken))
			errs <- callErr
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	// Every call must SUCCEED. Without this, a run in which all eight failed
	// would still satisfy the row-count assertion below.
	for callErr := range errs {
		require.NoError(t, callErr)
	}

	// Read the store directly: the RPC response is derived from it, so a written
	// row is only provable here.
	assert.Empty(t, mustListSections(t, env, userID),
		"ListSections is a pure read; it must create nothing")
}

// mustListSections reads one user's sections straight from the store, which is
// where a stray write is provable (the RPC response is derived from it).
func mustListSections(t *testing.T, env *sectionTestEnv, userID userid.UserID) []store.WorkspaceSection {
	t.Helper()
	sections, err := env.store.WorkspaceSections().ListByUserID(context.Background(), userID)
	require.NoError(t, err)
	return sections
}

// The seeded set is exactly one row per default type. A second write path (or a
// re-run of the seeding) shows up here as a duplicated type rather than only as
// a moved total.
func TestSectionService_CreateUser_SeedsOneSectionPerType(t *testing.T) {
	t.Parallel()

	env := setupSectionTest(t)

	sections := mustListSections(t, env, userid.MustNew(env.userID))
	require.Len(t, sections, 6)

	perType := map[leapmuxv1.SectionType]int{}
	for _, sec := range sections {
		perType[sec.SectionType]++
	}
	assert.Len(t, perType, 6, "six distinct default types")
	for sectionType, count := range perType {
		assert.Equal(t, 1, count, "%v must exist exactly once", sectionType)
	}
}

func TestSectionService_CreateSection(t *testing.T) {
	t.Parallel()

	env := setupSectionTest(t)

	// Load the sections CreateUser seeded.
	_, _ = env.client.ListSections(context.Background(), authedReq(
		&leapmuxv1.ListSectionsRequest{}, env.token))

	// Create a custom section.
	resp, err := env.client.CreateSection(context.Background(), authedReq(
		&leapmuxv1.CreateSectionRequest{Name: "My Custom"}, env.token))
	require.NoError(t, err)

	assert.NotEmpty(t, resp.Msg.GetSectionId())

	// Verify it appears in the list.
	listResp, _ := env.client.ListSections(context.Background(), authedReq(
		&leapmuxv1.ListSectionsRequest{}, env.token))
	require.Len(t, listResp.Msg.GetSections(), 7)
}

func TestSectionService_CreateSection_EmptyName(t *testing.T) {
	t.Parallel()

	env := setupSectionTest(t)

	_, err := env.client.CreateSection(context.Background(), authedReq(
		&leapmuxv1.CreateSectionRequest{Name: ""}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestSectionService_RenameSection(t *testing.T) {
	t.Parallel()

	env := setupSectionTest(t)

	_, _ = env.client.ListSections(context.Background(), authedReq(
		&leapmuxv1.ListSectionsRequest{}, env.token))

	// Create a section first.
	createResp, _ := env.client.CreateSection(context.Background(), authedReq(
		&leapmuxv1.CreateSectionRequest{Name: "Old Name"}, env.token))
	sectionID := createResp.Msg.GetSectionId()

	// Rename it.
	_, err := env.client.RenameSection(context.Background(), authedReq(
		&leapmuxv1.RenameSectionRequest{SectionId: sectionID, Name: "New Name"}, env.token))
	require.NoError(t, err)

	// Verify the name changed.
	listResp, _ := env.client.ListSections(context.Background(), authedReq(
		&leapmuxv1.ListSectionsRequest{}, env.token))
	for _, s := range listResp.Msg.GetSections() {
		if s.GetId() == sectionID {
			assert.Equal(t, "New Name", s.GetName())
			return
		}
	}
	assert.Fail(t, "section not found after rename")
}

func TestSectionService_RenameSection_EmptyName(t *testing.T) {
	t.Parallel()

	env := setupSectionTest(t)

	_, err := env.client.RenameSection(context.Background(), authedReq(
		&leapmuxv1.RenameSectionRequest{SectionId: "whatever", Name: ""}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestSectionService_DeleteSection(t *testing.T) {
	t.Parallel()

	env := setupSectionTest(t)

	_, _ = env.client.ListSections(context.Background(), authedReq(
		&leapmuxv1.ListSectionsRequest{}, env.token))

	// Create a section, then delete it.
	createResp, _ := env.client.CreateSection(context.Background(), authedReq(
		&leapmuxv1.CreateSectionRequest{Name: "Temp Section"}, env.token))
	sectionID := createResp.Msg.GetSectionId()

	_, err := env.client.DeleteSection(context.Background(), authedReq(
		&leapmuxv1.DeleteSectionRequest{SectionId: sectionID}, env.token))
	require.NoError(t, err)

	// Verify it's gone (back to 6 default sections).
	listResp, _ := env.client.ListSections(context.Background(), authedReq(
		&leapmuxv1.ListSectionsRequest{}, env.token))
	require.Len(t, listResp.Msg.GetSections(), 6)
}

func TestSectionService_DeleteSection_WithItems(t *testing.T) {
	t.Parallel()

	env := setupSectionTest(t)
	ctx := context.Background()

	// Create a workspace so the FK in workspace_section_items is satisfied.
	workspaceID := id.Generate()
	err := env.store.Workspaces().Create(ctx, store.CreateWorkspaceParams{
		ID:          workspaceID,
		OwnerUserID: userid.MustNew(env.userID),
		Title:       "ws for delete test",
	})
	require.NoError(t, err)

	// Load the sections CreateUser seeded.
	listResp, err := env.client.ListSections(ctx, authedReq(
		&leapmuxv1.ListSectionsRequest{}, env.token))
	require.NoError(t, err)

	// Create a custom section and assign a workspace to it.
	createResp, err := env.client.CreateSection(ctx, authedReq(
		&leapmuxv1.CreateSectionRequest{Name: "Custom With Items"}, env.token))
	require.NoError(t, err)
	customID := createResp.Msg.GetSectionId()

	_, err = env.client.MoveWorkspace(ctx, authedReq(
		&leapmuxv1.MoveWorkspaceRequest{
			WorkspaceId: workspaceID,
			SectionId:   customID,
			Position:    "a",
		}, env.token))
	require.NoError(t, err)

	// Delete the custom section — items should be moved to "In progress".
	_, err = env.client.DeleteSection(ctx, authedReq(
		&leapmuxv1.DeleteSectionRequest{SectionId: customID}, env.token))
	require.NoError(t, err)

	// Find the "In progress" section ID.
	var inProgressID string
	for _, s := range listResp.Msg.GetSections() {
		if s.GetSectionType() == leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS {
			inProgressID = s.GetId()
			break
		}
	}
	require.NotEmpty(t, inProgressID)

	// Verify the workspace was moved to "In progress".
	listResp2, err := env.client.ListSections(ctx, authedReq(
		&leapmuxv1.ListSectionsRequest{}, env.token))
	require.NoError(t, err)
	var found bool
	for _, item := range listResp2.Msg.GetItems() {
		if item.GetWorkspaceId() == workspaceID {
			assert.Equal(t, inProgressID, item.GetSectionId())
			found = true
			break
		}
	}
	assert.True(t, found, "workspace should be in 'In progress' section after deleting custom section")
}

// TestSectionService_DeleteSection_ReassignsPositionsOnMerge pins the
// position-uniqueness fix: when a custom section is deleted, its items
// must be appended to "In progress" with FRESH lexorank positions that
// don't collide with the items already there. The buggy bulk-move
// preserved the source items' positions, so any source item at
// lexorank.First() ("n") collided with an in-progress item that also
// sat at "n" (the common case for fresh accounts dragging "the first
// item" into each section). On a tie the SQL planner picked an
// order, and the sidebar shuffled across page refreshes.
func TestSectionService_DeleteSection_ReassignsPositionsOnMerge(t *testing.T) {
	t.Parallel()

	env := setupSectionTest(t)
	ctx := context.Background()

	// Create two workspaces — one for In progress, one for the custom
	// section that's about to be merged in.
	wsInProgress := id.Generate()
	require.NoError(t, env.store.Workspaces().Create(ctx, store.CreateWorkspaceParams{
		ID:          wsInProgress,
		OwnerUserID: userid.MustNew(env.userID),
		Title:       "ws in progress",
	}))
	wsCustom := id.Generate()
	require.NoError(t, env.store.Workspaces().Create(ctx, store.CreateWorkspaceParams{
		ID:          wsCustom,
		OwnerUserID: userid.MustNew(env.userID),
		Title:       "ws custom",
	}))

	// Load the seeded sections.
	listResp, err := env.client.ListSections(ctx, authedReq(
		&leapmuxv1.ListSectionsRequest{}, env.token))
	require.NoError(t, err)
	var inProgressID string
	for _, s := range listResp.Msg.GetSections() {
		if s.GetSectionType() == leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS {
			inProgressID = s.GetId()
			break
		}
	}
	require.NotEmpty(t, inProgressID)

	// Drag wsInProgress into the In progress section at lexorank.First().
	_, err = env.client.MoveWorkspace(ctx, authedReq(
		&leapmuxv1.MoveWorkspaceRequest{
			WorkspaceId: wsInProgress,
			SectionId:   inProgressID,
			Position:    "n", // lexorank.First()
		}, env.token))
	require.NoError(t, err)

	// Create a custom section and drop wsCustom into it at "n" too —
	// the colliding position that the bug exploited.
	createResp, err := env.client.CreateSection(ctx, authedReq(
		&leapmuxv1.CreateSectionRequest{Name: "Custom"}, env.token))
	require.NoError(t, err)
	customID := createResp.Msg.GetSectionId()
	_, err = env.client.MoveWorkspace(ctx, authedReq(
		&leapmuxv1.MoveWorkspaceRequest{
			WorkspaceId: wsCustom,
			SectionId:   customID,
			Position:    "n",
		}, env.token))
	require.NoError(t, err)

	// Delete the custom section. wsCustom must land in In progress
	// AND its position must differ from wsInProgress's "n".
	_, err = env.client.DeleteSection(ctx, authedReq(
		&leapmuxv1.DeleteSectionRequest{SectionId: customID}, env.token))
	require.NoError(t, err)

	items, err := env.store.WorkspaceSectionItems().ListByUser(ctx, userid.MustNew(env.userID))
	require.NoError(t, err)
	posByWs := map[string]string{}
	sectionByWs := map[string]string{}
	for _, item := range items {
		posByWs[item.WorkspaceID] = item.Position
		sectionByWs[item.WorkspaceID] = item.SectionID
	}
	assert.Equal(t, inProgressID, sectionByWs[wsInProgress])
	assert.Equal(t, inProgressID, sectionByWs[wsCustom])
	assert.Equal(t, "n", posByWs[wsInProgress], "untouched in-progress item keeps its position")
	assert.NotEqual(t, posByWs[wsInProgress], posByWs[wsCustom],
		"merged item must be reassigned to a unique position")
	assert.Greater(t, posByWs[wsCustom], posByWs[wsInProgress],
		"merged item must sort AFTER existing in-progress items")
}

// TestSectionService_DeleteSection_NotFoundOnBogusID pins the
// NotFound mapping the DeleteSection rewrite preserves: the move loop
// runs inside a transaction whose final `WorkspaceSections().Delete`
// is what decides whether the operation is allowed (custom sections
// only). When the section id doesn't exist (or isn't custom), the
// handler returns CodeNotFound and the entire move loop is rolled
// back atomically — the implementation guarantees this structurally
// via RunInTransaction; this test verifies the user-visible status
// code on the cheapest input that triggers the path (no real items to
// move, but the code path through ListByUserID + the empty move loop +
// the rows=0 Delete still exercises the sentinel-roll-back branch).
func TestSectionService_DeleteSection_NotFoundOnBogusID(t *testing.T) {
	t.Parallel()

	env := setupSectionTest(t)
	ctx := context.Background()
	_, err := env.client.ListSections(ctx, authedReq(
		&leapmuxv1.ListSectionsRequest{}, env.token))
	require.NoError(t, err)

	_, err = env.client.DeleteSection(ctx, authedReq(
		&leapmuxv1.DeleteSectionRequest{SectionId: "section-id-that-does-not-exist"}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err),
		"a delete of a missing section must surface NotFound via the rollback sentinel, not Internal")
}

func TestSectionService_MoveSection(t *testing.T) {
	t.Parallel()

	env := setupSectionTest(t)

	// Load the seeded sections.
	listResp, _ := env.client.ListSections(context.Background(), authedReq(
		&leapmuxv1.ListSectionsRequest{}, env.token))

	// Find the "In progress" section (should be on left sidebar).
	var inProgressID string
	for _, s := range listResp.Msg.GetSections() {
		if s.GetSectionType() == leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS {
			inProgressID = s.GetId()
			assert.Equal(t, leapmuxv1.Sidebar_SIDEBAR_LEFT, s.GetSidebar())
		}
	}
	require.NotEmpty(t, inProgressID)

	// Move it to the right sidebar.
	_, err := env.client.MoveSection(context.Background(), authedReq(
		&leapmuxv1.MoveSectionRequest{
			SectionId: inProgressID,
			Sidebar:   leapmuxv1.Sidebar_SIDEBAR_RIGHT,
			Position:  "z",
		}, env.token))
	require.NoError(t, err)

	// Verify it's now on the right sidebar.
	listResp2, _ := env.client.ListSections(context.Background(), authedReq(
		&leapmuxv1.ListSectionsRequest{}, env.token))
	for _, s := range listResp2.Msg.GetSections() {
		if s.GetId() == inProgressID {
			assert.Equal(t, leapmuxv1.Sidebar_SIDEBAR_RIGHT, s.GetSidebar())
			assert.Equal(t, "z", s.GetPosition())
		}
	}
}

func TestSectionService_MoveWorkspace(t *testing.T) {
	t.Parallel()

	env := setupSectionTest(t)

	// Create a workspace (hub-owned) so that the FK in workspace_section_items is satisfied.
	workspaceID := id.Generate()
	err := env.store.Workspaces().Create(context.Background(), store.CreateWorkspaceParams{
		ID:          workspaceID,
		OwnerUserID: userid.MustNew(env.userID),
		Title:       "test workspace",
	})
	require.NoError(t, err)

	// Load the sections CreateUser seeded.
	listResp, _ := env.client.ListSections(context.Background(), authedReq(
		&leapmuxv1.ListSectionsRequest{}, env.token))

	var archivedID string
	for _, s := range listResp.Msg.GetSections() {
		if s.GetSectionType() == leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED {
			archivedID = s.GetId()
		}
	}

	// Move the workspace to the archived section.
	_, err = env.client.MoveWorkspace(context.Background(), authedReq(
		&leapmuxv1.MoveWorkspaceRequest{
			WorkspaceId: workspaceID,
			SectionId:   archivedID,
			Position:    "n",
		}, env.token))
	require.NoError(t, err)

	// Verify the item appears in the list.
	listResp2, _ := env.client.ListSections(context.Background(), authedReq(
		&leapmuxv1.ListSectionsRequest{}, env.token))
	items := listResp2.Msg.GetItems()
	require.Len(t, items, 1)
	assert.Equal(t, archivedID, items[0].GetSectionId())
	assert.Equal(t, workspaceID, items[0].GetWorkspaceId())
}

func TestSectionService_IsWorkspaceInArchivedSection(t *testing.T) {
	t.Parallel()

	env := setupSectionTest(t)

	workspaceID := id.Generate()
	err := env.store.Workspaces().Create(context.Background(), store.CreateWorkspaceParams{
		ID:          workspaceID,
		OwnerUserID: userid.MustNew(env.userID),
		Title:       "test workspace",
	})
	require.NoError(t, err)

	// Load the seeded sections and find their IDs.
	listResp, _ := env.client.ListSections(context.Background(), authedReq(
		&leapmuxv1.ListSectionsRequest{}, env.token))
	var inProgressID, archivedID string
	for _, s := range listResp.Msg.GetSections() {
		switch s.GetSectionType() {
		case leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS:
			inProgressID = s.GetId()
		case leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED:
			archivedID = s.GetId()
		default:
			// Only the two workspace sections are needed to drive this test.
		}
	}

	// Not archived initially (not in any section).
	archived, err := env.store.WorkspaceSectionItems().IsInArchivedSection(context.Background(), store.IsWorkspaceInArchivedSectionParams{
		UserID:      userid.MustNew(env.userID),
		WorkspaceID: workspaceID,
	})
	require.NoError(t, err)
	assert.False(t, archived)

	// Move to In Progress.
	_, _ = env.client.MoveWorkspace(context.Background(), authedReq(
		&leapmuxv1.MoveWorkspaceRequest{WorkspaceId: workspaceID, SectionId: inProgressID, Position: "a"}, env.token))
	archived, err = env.store.WorkspaceSectionItems().IsInArchivedSection(context.Background(), store.IsWorkspaceInArchivedSectionParams{
		UserID:      userid.MustNew(env.userID),
		WorkspaceID: workspaceID,
	})
	require.NoError(t, err)
	assert.False(t, archived)

	// Move to Archived.
	_, _ = env.client.MoveWorkspace(context.Background(), authedReq(
		&leapmuxv1.MoveWorkspaceRequest{WorkspaceId: workspaceID, SectionId: archivedID, Position: "a"}, env.token))
	archived, err = env.store.WorkspaceSectionItems().IsInArchivedSection(context.Background(), store.IsWorkspaceInArchivedSectionParams{
		UserID:      userid.MustNew(env.userID),
		WorkspaceID: workspaceID,
	})
	require.NoError(t, err)
	assert.True(t, archived)

	// Move back to In Progress.
	_, _ = env.client.MoveWorkspace(context.Background(), authedReq(
		&leapmuxv1.MoveWorkspaceRequest{WorkspaceId: workspaceID, SectionId: inProgressID, Position: "a"}, env.token))
	archived, err = env.store.WorkspaceSectionItems().IsInArchivedSection(context.Background(), store.IsWorkspaceInArchivedSectionParams{
		UserID:      userid.MustNew(env.userID),
		WorkspaceID: workspaceID,
	})
	require.NoError(t, err)
	assert.False(t, archived)
}

func TestSectionService_Unauthenticated(t *testing.T) {
	t.Parallel()

	env := setupSectionTest(t)

	_, err := env.client.ListSections(context.Background(),
		connect.NewRequest(&leapmuxv1.ListSectionsRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// A caller whose identity never got populated must not own someone else's
// section.
//
// requireOwnedSection is the package's OTHER resource-ownership predicate
// (loadOwnedWorkspaceOr403 is the first), and it compared raw strings, so two
// empty ids matched and granted Move/Delete on a blank-owner row. It now
// compares through userid.UserID.Matches, which refuses an empty id on either
// side.
//
// The blank-OWNER row that made the empty-vs-empty pairing observable is gone
// by construction: workspace_sections.user_id is `REFERENCES users(id)` and
// CreateUserParams.Validate refuses the blank parent, so the row cannot exist.
// The zero CALLER stays reachable -- auth.UserInfo.ID is a zero UserID whenever
// it was never minted -- and it is aimed at a REAL owner's section here, which
// is what keeps the assertion non-vacuous: a predicate that stopped comparing
// would move this section.
func TestMoveSectionDeniesZeroCallerOnBlankOwnedSection(t *testing.T) {
	t.Parallel()

	env := setupSectionTest(t)
	ctx := context.Background()

	require.ErrorIs(t, env.store.Users().Create(ctx, store.CreateUserParams{
		ID: "", Username: "blank-id-user", PasswordHash: "h",
		DisplayName: "Blank", PasswordSet: true,
	}), store.ErrInvalidArgument,
		"the parent key a blank-owner section would need must stay unwritable")

	sectionID := id.Generate()
	require.NoError(t, env.store.WorkspaceSections().Create(ctx, store.CreateWorkspaceSectionParams{
		ID: sectionID, UserID: userid.MustNew(env.userID), Name: "real-owned", Position: "n",
		SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM,
		Sidebar:     leapmuxv1.Sidebar_SIDEBAR_LEFT,
	}))

	// A UserInfo whose ID never got minted -- the zero value.
	zeroCaller := auth.WithUser(ctx, &auth.UserInfo{Username: "nobody"})
	_, err := service.NewSectionService(env.store).MoveSection(zeroCaller,
		connect.NewRequest(&leapmuxv1.MoveSectionRequest{
			SectionId: sectionID,
			Position:  "z",
			Sidebar:   leapmuxv1.Sidebar_SIDEBAR_LEFT,
		}))
	require.Error(t, err, "a zero caller id must not own another user's section")
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err),
		"non-owner hits masquerade as NotFound so section ids do not leak")

	// The row is untouched: the deny happened before the update.
	section, getErr := env.store.WorkspaceSections().GetByID(ctx, sectionID)
	require.NoError(t, getErr)
	assert.Equal(t, "n", section.Position, "the denied move must not have written")
}
