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
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/util/id"
)

func leafNode(id string) *leapmuxv1.LayoutNode {
	return &leapmuxv1.LayoutNode{
		Node: &leapmuxv1.LayoutNode_Leaf{Leaf: &leapmuxv1.LayoutLeaf{Id: id}},
	}
}

type workspaceTestEnv struct {
	client  leapmuxv1connect.WorkspaceServiceClient
	queries *gendb.Queries
	token   string
	orgID   string
	userID  string
}

func setupWorkspaceTest(t *testing.T) *workspaceTestEnv {
	t.Helper()

	sqlDB, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	err = db.Migrate(sqlDB)
	require.NoError(t, err)

	queries := gendb.New(sqlDB)
	workspaceSvc := service.NewWorkspaceService(sqlDB, queries, false)

	mux := http.NewServeMux()
	opts := connect.WithInterceptors(auth.NewInterceptor(queries, false))
	path, handler := leapmuxv1connect.NewWorkspaceServiceHandler(workspaceSvc, opts)
	mux.Handle(path, handler)

	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewWorkspaceServiceClient(
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

	_ = queries.CreateWorker(context.Background(), gendb.CreateWorkerParams{
		ID:           "w1",
		OrgID:        orgID,
		AuthToken:    "tok",
		RegisteredBy: userID,
		PublicKey:    []byte("key"),
	})

	token, _, err := auth.Login(context.Background(), queries, "testuser", "pass")
	require.NoError(t, err)

	return &workspaceTestEnv{
		client:  client,
		queries: queries,
		token:   token,
		orgID:   orgID,
		userID:  userID,
	}
}

func (env *workspaceTestEnv) createWorkspace(t *testing.T) string {
	t.Helper()
	wsID := id.Generate()
	err := env.queries.CreateWorkspace(context.Background(), gendb.CreateWorkspaceParams{
		ID:          wsID,
		OrgID:       env.orgID,
		OwnerUserID: env.userID,
		Title:       "Test Workspace",
	})
	require.NoError(t, err)
	return wsID
}

func TestSaveMultiLayout_EmptyEntries(t *testing.T) {
	env := setupWorkspaceTest(t)

	resp, err := env.client.SaveMultiLayout(context.Background(), authedReq(
		&leapmuxv1.SaveMultiLayoutRequest{
			OrgId:   env.orgID,
			Entries: []*leapmuxv1.WorkspaceLayoutEntry{},
		}, env.token))
	require.NoError(t, err)
	assert.NotNil(t, resp)
}

func TestSaveMultiLayout_SingleWorkspace(t *testing.T) {
	env := setupWorkspaceTest(t)
	wsID := env.createWorkspace(t)

	resp, err := env.client.SaveMultiLayout(context.Background(), authedReq(
		&leapmuxv1.SaveMultiLayoutRequest{
			OrgId: env.orgID,
			Entries: []*leapmuxv1.WorkspaceLayoutEntry{
				{
					WorkspaceId: wsID,
					Layout:      leafNode("tile-1"),
					Tabs: []*leapmuxv1.WorkspaceTab{
						{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "agent-1", Position: "a", TileId: "tile-1", WorkerId: "w1"},
					},
				},
			},
		}, env.token))
	require.NoError(t, err)
	assert.NotNil(t, resp)

	// Verify the tab was saved by loading it back.
	tabs, err := env.queries.ListWorkspaceTabsByWorkspace(context.Background(), wsID)
	require.NoError(t, err)
	require.Len(t, tabs, 1)
	assert.Equal(t, "agent-1", tabs[0].TabID)
}

func TestSaveMultiLayout_TwoWorkspacesAtomic(t *testing.T) {
	env := setupWorkspaceTest(t)
	ws1 := env.createWorkspace(t)
	ws2 := env.createWorkspace(t)

	resp, err := env.client.SaveMultiLayout(context.Background(), authedReq(
		&leapmuxv1.SaveMultiLayoutRequest{
			OrgId: env.orgID,
			Entries: []*leapmuxv1.WorkspaceLayoutEntry{
				{
					WorkspaceId: ws1,
					Layout:      leafNode("tile-1"),
					Tabs: []*leapmuxv1.WorkspaceTab{
						{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "agent-1", Position: "a", TileId: "tile-1", WorkerId: "w1"},
					},
				},
				{
					WorkspaceId: ws2,
					Layout:      leafNode("tile-2"),
					Tabs: []*leapmuxv1.WorkspaceTab{
						{TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: "term-1", Position: "a", TileId: "tile-2", WorkerId: "w1"},
						{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "agent-2", Position: "b", TileId: "tile-2", WorkerId: "w1"},
					},
				},
			},
		}, env.token))
	require.NoError(t, err)
	assert.NotNil(t, resp)

	// Verify tabs for ws1.
	tabs1, err := env.queries.ListWorkspaceTabsByWorkspace(context.Background(), ws1)
	require.NoError(t, err)
	require.Len(t, tabs1, 1)
	assert.Equal(t, "agent-1", tabs1[0].TabID)

	// Verify tabs for ws2.
	tabs2, err := env.queries.ListWorkspaceTabsByWorkspace(context.Background(), ws2)
	require.NoError(t, err)
	require.Len(t, tabs2, 2)
}

func TestSaveMultiLayout_NotOwnedWorkspaceRejected(t *testing.T) {
	env := setupWorkspaceTest(t)
	ownedWs := env.createWorkspace(t)

	// Create a workspace owned by another user.
	otherUserID := id.Generate()
	hash, _ := bcrypt.GenerateFromPassword([]byte("pass"), bcrypt.MinCost)
	_ = env.queries.CreateUser(context.Background(), gendb.CreateUserParams{
		ID:           otherUserID,
		OrgID:        env.orgID,
		Username:     "other",
		PasswordHash: string(hash),
		DisplayName:  "Other",
		IsAdmin:      0,
	})
	otherWsID := id.Generate()
	_ = env.queries.CreateWorkspace(context.Background(), gendb.CreateWorkspaceParams{
		ID:          otherWsID,
		OrgID:       env.orgID,
		OwnerUserID: otherUserID,
		Title:       "Other's Workspace",
	})

	// Attempt to save both — should fail because the second workspace is not owned.
	_, err := env.client.SaveMultiLayout(context.Background(), authedReq(
		&leapmuxv1.SaveMultiLayoutRequest{
			OrgId: env.orgID,
			Entries: []*leapmuxv1.WorkspaceLayoutEntry{
				{
					WorkspaceId: ownedWs,
					Layout:      leafNode("tile-1"),
					Tabs:        []*leapmuxv1.WorkspaceTab{},
				},
				{
					WorkspaceId: otherWsID,
					Layout:      leafNode("tile-1"),
					Tabs:        []*leapmuxv1.WorkspaceTab{},
				},
			},
		}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	// Verify that the owned workspace's tabs were NOT saved (transaction rolled back).
	tabs, err := env.queries.ListWorkspaceTabsByWorkspace(context.Background(), ownedWs)
	require.NoError(t, err)
	assert.Empty(t, tabs)
}

func TestSaveMultiLayout_TabsReplacedOnUpdate(t *testing.T) {
	env := setupWorkspaceTest(t)
	wsID := env.createWorkspace(t)

	// First save: 2 tabs.
	_, err := env.client.SaveMultiLayout(context.Background(), authedReq(
		&leapmuxv1.SaveMultiLayoutRequest{
			OrgId: env.orgID,
			Entries: []*leapmuxv1.WorkspaceLayoutEntry{
				{
					WorkspaceId: wsID,
					Layout:      leafNode("tile-1"),
					Tabs: []*leapmuxv1.WorkspaceTab{
						{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "agent-1", Position: "a", TileId: "tile-1", WorkerId: "w1"},
						{TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: "term-1", Position: "b", TileId: "tile-1", WorkerId: "w1"},
					},
				},
			},
		}, env.token))
	require.NoError(t, err)

	tabs, err := env.queries.ListWorkspaceTabsByWorkspace(context.Background(), wsID)
	require.NoError(t, err)
	require.Len(t, tabs, 2)

	// Second save: 1 tab (agent-1 moved out, only term-1 remains).
	_, err = env.client.SaveMultiLayout(context.Background(), authedReq(
		&leapmuxv1.SaveMultiLayoutRequest{
			OrgId: env.orgID,
			Entries: []*leapmuxv1.WorkspaceLayoutEntry{
				{
					WorkspaceId: wsID,
					Layout:      leafNode("tile-1"),
					Tabs: []*leapmuxv1.WorkspaceTab{
						{TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: "term-1", Position: "a", TileId: "tile-1", WorkerId: "w1"},
					},
				},
			},
		}, env.token))
	require.NoError(t, err)

	tabs, err = env.queries.ListWorkspaceTabsByWorkspace(context.Background(), wsID)
	require.NoError(t, err)
	require.Len(t, tabs, 1)
	assert.Equal(t, "term-1", tabs[0].TabID)
}
