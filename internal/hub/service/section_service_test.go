package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/db"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/id"
	"github.com/leapmux/leapmux/internal/hub/service"
)

type sectionTestEnv struct {
	client  leapmuxv1connect.SectionServiceClient
	queries *gendb.Queries
	token   string
	orgID   string
	userID  string
}

func setupSectionTest(t *testing.T) *sectionTestEnv {
	t.Helper()

	sqlDB, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	err = db.Migrate(sqlDB)
	require.NoError(t, err)

	queries := gendb.New(sqlDB)
	sectionSvc := service.NewSectionService(queries)

	mux := http.NewServeMux()
	opts := connect.WithInterceptors(auth.NewInterceptor(queries))
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
	hash, _ := bcrypt.GenerateFromPassword([]byte("pass"), bcrypt.MinCost)

	_ = queries.CreateOrg(context.Background(), gendb.CreateOrgParams{ID: orgID, Name: "test-org"})
	_ = queries.CreateUser(context.Background(), gendb.CreateUserParams{
		ID:           userID,
		OrgID:        orgID,
		Username:     "testuser",
		PasswordHash: string(hash),
		DisplayName:  "Test",
		IsAdmin:      1,
	})

	token, _, err := auth.Login(context.Background(), queries, "testuser", "pass")
	require.NoError(t, err)

	return &sectionTestEnv{
		client:  client,
		queries: queries,
		token:   token,
		orgID:   orgID,
		userID:  userID,
	}
}

func TestSectionService_ListSections_AutoInitializes(t *testing.T) {
	env := setupSectionTest(t)

	resp, err := env.client.ListSections(context.Background(), authedReq(
		&leapmuxv1.ListSectionsRequest{OrgId: env.orgID}, env.token))
	require.NoError(t, err)

	// Should auto-create "In progress" and "Archived" sections.
	sections := resp.Msg.GetSections()
	require.Len(t, sections, 2)

	var hasInProgress, hasArchived bool
	for _, s := range sections {
		switch s.GetSectionType() {
		case leapmuxv1.SectionType_SECTION_TYPE_IN_PROGRESS:
			hasInProgress = true
			assert.Equal(t, "In progress", s.GetName())
		case leapmuxv1.SectionType_SECTION_TYPE_ARCHIVED:
			hasArchived = true
			assert.Equal(t, "Archived", s.GetName())
		}
	}
	assert.True(t, hasInProgress, "missing in_progress section")
	assert.True(t, hasArchived, "missing archived section")
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

	sec := resp.Msg.GetSection()
	assert.Equal(t, "My Custom", sec.GetName())
	assert.Equal(t, leapmuxv1.SectionType_SECTION_TYPE_CUSTOM, sec.GetSectionType())
	assert.NotEmpty(t, sec.GetId())

	// Verify it appears in the list.
	listResp, _ := env.client.ListSections(context.Background(), authedReq(
		&leapmuxv1.ListSectionsRequest{OrgId: env.orgID}, env.token))
	require.Len(t, listResp.Msg.GetSections(), 3)
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
	sectionID := createResp.Msg.GetSection().GetId()

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
	sectionID := createResp.Msg.GetSection().GetId()

	_, err := env.client.DeleteSection(context.Background(), authedReq(
		&leapmuxv1.DeleteSectionRequest{SectionId: sectionID}, env.token))
	require.NoError(t, err)

	// Verify it's gone (back to 2 default sections).
	listResp, _ := env.client.ListSections(context.Background(), authedReq(
		&leapmuxv1.ListSectionsRequest{OrgId: env.orgID}, env.token))
	require.Len(t, listResp.Msg.GetSections(), 2)
}

func TestSectionService_MoveWorkspace(t *testing.T) {
	env := setupSectionTest(t)

	// Create a workspace directly in DB.
	workerID := id.Generate()
	_ = env.queries.CreateWorker(context.Background(), gendb.CreateWorkerParams{
		ID:           workerID,
		OrgID:        env.orgID,
		Name:         "test-worker",
		Hostname:     "localhost",
		AuthToken:    id.Generate(),
		RegisteredBy: env.userID,
	})
	workspaceID := id.Generate()
	_ = env.queries.CreateWorkspace(context.Background(), gendb.CreateWorkspaceParams{
		ID:        workspaceID,
		OrgID:     env.orgID,
		CreatedBy: env.userID,
		Title:     "Test Workspace",
	})

	// Trigger auto-init of sections.
	listResp, _ := env.client.ListSections(context.Background(), authedReq(
		&leapmuxv1.ListSectionsRequest{OrgId: env.orgID}, env.token))

	var archivedID string
	for _, s := range listResp.Msg.GetSections() {
		if s.GetSectionType() == leapmuxv1.SectionType_SECTION_TYPE_ARCHIVED {
			archivedID = s.GetId()
		}
	}

	// Move the workspace to the archived section.
	_, err := env.client.MoveWorkspace(context.Background(), authedReq(
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

func TestSectionService_Unauthenticated(t *testing.T) {
	env := setupSectionTest(t)

	_, err := env.client.ListSections(context.Background(),
		connect.NewRequest(&leapmuxv1.ListSectionsRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}
