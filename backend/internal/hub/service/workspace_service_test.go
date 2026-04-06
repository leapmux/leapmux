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

func leafNode(id string) *leapmuxv1.LayoutNode {
	return &leapmuxv1.LayoutNode{
		Node: &leapmuxv1.LayoutNode_Leaf{Leaf: &leapmuxv1.LayoutLeaf{Id: id}},
	}
}

type workspaceTestEnv struct {
	client leapmuxv1connect.WorkspaceServiceClient
	store  store.Store
	token  string
	orgID  string
	userID string
}

func setupWorkspaceTest(t *testing.T) *workspaceTestEnv {
	t.Helper()

	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	err = st.Migrator().Migrate(context.Background())
	require.NoError(t, err)

	workspaceSvc := service.NewWorkspaceService(st, false)

	mux := http.NewServeMux()
	interceptor, _ := auth.NewInterceptor(st, false, false, false)
	opts := connect.WithInterceptors(interceptor)
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

	_ = st.Workers().Create(context.Background(), store.CreateWorkerParams{
		ID:              "w1",
		AuthToken:       "tok",
		RegisteredBy:    userID,
		PublicKey:       []byte("key"),
		MlkemPublicKey:  []byte{},
		SlhdsaPublicKey: []byte{},
	})

	token, _, _, err := auth.Login(context.Background(), st, "testuser", "testpass")
	require.NoError(t, err)

	return &workspaceTestEnv{
		client: client,
		store:  st,
		token:  token,
		orgID:  orgID,
		userID: userID,
	}
}

func (env *workspaceTestEnv) createWorkspace(t *testing.T) string {
	t.Helper()
	wsID := id.Generate()
	err := env.store.Workspaces().Create(context.Background(), store.CreateWorkspaceParams{
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
	tabs, err := env.store.WorkspaceTabs().ListByWorkspace(context.Background(), wsID)
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
	tabs1, err := env.store.WorkspaceTabs().ListByWorkspace(context.Background(), ws1)
	require.NoError(t, err)
	require.Len(t, tabs1, 1)
	assert.Equal(t, "agent-1", tabs1[0].TabID)

	// Verify tabs for ws2.
	tabs2, err := env.store.WorkspaceTabs().ListByWorkspace(context.Background(), ws2)
	require.NoError(t, err)
	require.Len(t, tabs2, 2)
}

func TestSaveMultiLayout_NotOwnedWorkspaceRejected(t *testing.T) {
	env := setupWorkspaceTest(t)
	ownedWs := env.createWorkspace(t)

	// Create a workspace owned by another user.
	otherUserID := id.Generate()
	hash, _ := password.Hash("testpass")
	_ = env.store.Users().Create(context.Background(), store.CreateUserParams{
		ID:           otherUserID,
		OrgID:        env.orgID,
		Username:     "other",
		PasswordHash: hash,
		DisplayName:  "Other",
		PasswordSet:  true,
		IsAdmin:      false,
	})
	otherWsID := id.Generate()
	_ = env.store.Workspaces().Create(context.Background(), store.CreateWorkspaceParams{
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
	tabs, err := env.store.WorkspaceTabs().ListByWorkspace(context.Background(), ownedWs)
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

	tabs, err := env.store.WorkspaceTabs().ListByWorkspace(context.Background(), wsID)
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

	tabs, err = env.store.WorkspaceTabs().ListByWorkspace(context.Background(), wsID)
	require.NoError(t, err)
	require.Len(t, tabs, 1)
	assert.Equal(t, "term-1", tabs[0].TabID)
}
