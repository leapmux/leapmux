package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"

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
	orgID  string
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
	interceptor, _ := auth.NewInterceptor(st, nil, false, false)
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

	orgID := id.Generate()
	userID := id.Generate()
	hash, _ := password.Hash("testpass")

	_ = st.Orgs().Create(context.Background(), store.CreateOrgParams{ID: orgID, Name: "test-org"})
	_ = st.Users().Create(context.Background(), store.CreateUserParams{
		ID:           userID,
		OrgID:        orgID,
		Username:     "testuser",
		PasswordHash: hash,
		DisplayName:  "Test",
		PasswordSet:  true,
		IsAdmin:      true,
	})

	token, _, _, err := auth.Login(context.Background(), st, "testuser", "testpass")
	require.NoError(t, err)

	return &sectionTestEnv{
		client: client,
		store:  st,
		token:  token,
		orgID:  orgID,
		userID: userID,
	}
}

func TestSectionService_ListSections_AutoInitializes(t *testing.T) {
	env := setupSectionTest(t)

	resp, err := env.client.ListSections(context.Background(), authedReq(
		&leapmuxv1.ListSectionsRequest{OrgId: env.orgID}, env.token))
	require.NoError(t, err)

	// Should auto-create all default sections: In progress, Shared, Archived, Workers (left), Files, To-dos (right).
	sections := resp.Msg.GetSections()
	require.Len(t, sections, 6)

	var hasInProgress, hasShared, hasArchived, hasWorkers, hasFiles, hasTodos bool
	for _, s := range sections {
		switch s.GetSectionType() {
		case leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS:
			hasInProgress = true
			assert.Equal(t, "In progress", s.GetName())
			assert.Equal(t, leapmuxv1.Sidebar_SIDEBAR_LEFT, s.GetSidebar())
		case leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_SHARED:
			hasShared = true
			assert.Equal(t, "Shared", s.GetName())
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
		}
	}
	assert.True(t, hasInProgress, "missing in_progress section")
	assert.True(t, hasShared, "missing shared section")
	assert.True(t, hasArchived, "missing archived section")
	assert.True(t, hasWorkers, "missing workers section")
	assert.True(t, hasFiles, "missing files section")
	assert.True(t, hasTodos, "missing todos section")
}

func TestSectionService_CreateSection(t *testing.T) {
	env := setupSectionTest(t)

	// Trigger auto-init of default sections.
	_, _ = env.client.ListSections(context.Background(), authedReq(
		&leapmuxv1.ListSectionsRequest{OrgId: env.orgID}, env.token))

	// Create a custom section.
	resp, err := env.client.CreateSection(context.Background(), authedReq(
		&leapmuxv1.CreateSectionRequest{Name: "My Custom"}, env.token))
	require.NoError(t, err)

	assert.NotEmpty(t, resp.Msg.GetSectionId())

	// Verify it appears in the list.
	listResp, _ := env.client.ListSections(context.Background(), authedReq(
		&leapmuxv1.ListSectionsRequest{OrgId: env.orgID}, env.token))
	require.Len(t, listResp.Msg.GetSections(), 7)
}

func TestSectionService_CreateSection_EmptyName(t *testing.T) {
	env := setupSectionTest(t)

	_, err := env.client.CreateSection(context.Background(), authedReq(
		&leapmuxv1.CreateSectionRequest{Name: ""}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestSectionService_RenameSection(t *testing.T) {
	env := setupSectionTest(t)

	_, _ = env.client.ListSections(context.Background(), authedReq(
		&leapmuxv1.ListSectionsRequest{OrgId: env.orgID}, env.token))

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
		&leapmuxv1.ListSectionsRequest{OrgId: env.orgID}, env.token))
	for _, s := range listResp.Msg.GetSections() {
		if s.GetId() == sectionID {
			assert.Equal(t, "New Name", s.GetName())
			return
		}
	}
	assert.Fail(t, "section not found after rename")
}

func TestSectionService_RenameSection_EmptyName(t *testing.T) {
	env := setupSectionTest(t)

	_, err := env.client.RenameSection(context.Background(), authedReq(
		&leapmuxv1.RenameSectionRequest{SectionId: "whatever", Name: ""}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestSectionService_DeleteSection(t *testing.T) {
	env := setupSectionTest(t)

	_, _ = env.client.ListSections(context.Background(), authedReq(
		&leapmuxv1.ListSectionsRequest{OrgId: env.orgID}, env.token))

	// Create a section, then delete it.
	createResp, _ := env.client.CreateSection(context.Background(), authedReq(
		&leapmuxv1.CreateSectionRequest{Name: "Temp Section"}, env.token))
	sectionID := createResp.Msg.GetSectionId()

	_, err := env.client.DeleteSection(context.Background(), authedReq(
		&leapmuxv1.DeleteSectionRequest{SectionId: sectionID}, env.token))
	require.NoError(t, err)

	// Verify it's gone (back to 6 default sections).
	listResp, _ := env.client.ListSections(context.Background(), authedReq(
		&leapmuxv1.ListSectionsRequest{OrgId: env.orgID}, env.token))
	require.Len(t, listResp.Msg.GetSections(), 6)
}

func TestSectionService_DeleteSection_WithItems(t *testing.T) {
	env := setupSectionTest(t)
	ctx := context.Background()

	// Create a workspace so the FK in workspace_section_items is satisfied.
	workspaceID := id.Generate()
	err := env.store.Workspaces().Create(ctx, store.CreateWorkspaceParams{
		ID:          workspaceID,
		OrgID:       env.orgID,
		OwnerUserID: env.userID,
		Title:       "ws for delete test",
	})
	require.NoError(t, err)

	// Trigger auto-init of sections.
	listResp, err := env.client.ListSections(ctx, authedReq(
		&leapmuxv1.ListSectionsRequest{OrgId: env.orgID}, env.token))
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
		&leapmuxv1.ListSectionsRequest{OrgId: env.orgID}, env.token))
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

func TestSectionService_MoveSection(t *testing.T) {
	env := setupSectionTest(t)

	// Trigger auto-init.
	listResp, _ := env.client.ListSections(context.Background(), authedReq(
		&leapmuxv1.ListSectionsRequest{OrgId: env.orgID}, env.token))

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
		&leapmuxv1.ListSectionsRequest{OrgId: env.orgID}, env.token))
	for _, s := range listResp2.Msg.GetSections() {
		if s.GetId() == inProgressID {
			assert.Equal(t, leapmuxv1.Sidebar_SIDEBAR_RIGHT, s.GetSidebar())
			assert.Equal(t, "z", s.GetPosition())
		}
	}
}

func TestSectionService_MoveWorkspace(t *testing.T) {
	env := setupSectionTest(t)

	// Create a workspace (hub-owned) so that the FK in workspace_section_items is satisfied.
	workspaceID := id.Generate()
	err := env.store.Workspaces().Create(context.Background(), store.CreateWorkspaceParams{
		ID:          workspaceID,
		OrgID:       env.orgID,
		OwnerUserID: env.userID,
		Title:       "test workspace",
	})
	require.NoError(t, err)

	// Trigger auto-init of sections.
	listResp, _ := env.client.ListSections(context.Background(), authedReq(
		&leapmuxv1.ListSectionsRequest{OrgId: env.orgID}, env.token))

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
		&leapmuxv1.ListSectionsRequest{OrgId: env.orgID}, env.token))
	items := listResp2.Msg.GetItems()
	require.Len(t, items, 1)
	assert.Equal(t, archivedID, items[0].GetSectionId())
	assert.Equal(t, workspaceID, items[0].GetWorkspaceId())
}

func TestSectionService_IsWorkspaceInArchivedSection(t *testing.T) {
	env := setupSectionTest(t)

	workspaceID := id.Generate()
	err := env.store.Workspaces().Create(context.Background(), store.CreateWorkspaceParams{
		ID:          workspaceID,
		OrgID:       env.orgID,
		OwnerUserID: env.userID,
		Title:       "test workspace",
	})
	require.NoError(t, err)

	// Trigger auto-init and find section IDs.
	listResp, _ := env.client.ListSections(context.Background(), authedReq(
		&leapmuxv1.ListSectionsRequest{OrgId: env.orgID}, env.token))
	var inProgressID, archivedID string
	for _, s := range listResp.Msg.GetSections() {
		switch s.GetSectionType() {
		case leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS:
			inProgressID = s.GetId()
		case leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED:
			archivedID = s.GetId()
		}
	}

	// Not archived initially (not in any section).
	archived, err := env.store.WorkspaceSectionItems().IsInArchivedSection(context.Background(), store.IsWorkspaceInArchivedSectionParams{
		UserID:      env.userID,
		WorkspaceID: workspaceID,
	})
	require.NoError(t, err)
	assert.False(t, archived)

	// Move to In Progress.
	_, _ = env.client.MoveWorkspace(context.Background(), authedReq(
		&leapmuxv1.MoveWorkspaceRequest{WorkspaceId: workspaceID, SectionId: inProgressID, Position: "a"}, env.token))
	archived, err = env.store.WorkspaceSectionItems().IsInArchivedSection(context.Background(), store.IsWorkspaceInArchivedSectionParams{
		UserID:      env.userID,
		WorkspaceID: workspaceID,
	})
	require.NoError(t, err)
	assert.False(t, archived)

	// Move to Archived.
	_, _ = env.client.MoveWorkspace(context.Background(), authedReq(
		&leapmuxv1.MoveWorkspaceRequest{WorkspaceId: workspaceID, SectionId: archivedID, Position: "a"}, env.token))
	archived, err = env.store.WorkspaceSectionItems().IsInArchivedSection(context.Background(), store.IsWorkspaceInArchivedSectionParams{
		UserID:      env.userID,
		WorkspaceID: workspaceID,
	})
	require.NoError(t, err)
	assert.True(t, archived)

	// Move back to In Progress.
	_, _ = env.client.MoveWorkspace(context.Background(), authedReq(
		&leapmuxv1.MoveWorkspaceRequest{WorkspaceId: workspaceID, SectionId: inProgressID, Position: "a"}, env.token))
	archived, err = env.store.WorkspaceSectionItems().IsInArchivedSection(context.Background(), store.IsWorkspaceInArchivedSectionParams{
		UserID:      env.userID,
		WorkspaceID: workspaceID,
	})
	require.NoError(t, err)
	assert.False(t, archived)
}

func TestSectionService_Unauthenticated(t *testing.T) {
	env := setupSectionTest(t)

	_, err := env.client.ListSections(context.Background(),
		connect.NewRequest(&leapmuxv1.ListSectionsRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}
