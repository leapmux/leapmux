package service_test

import (
	"context"
	"errors"
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

func (env *workspaceTestEnv) createUserAndToken(t *testing.T, username string) (string, string) {
	t.Helper()

	userID := id.Generate()
	hash, err := password.Hash("testpass")
	require.NoError(t, err)

	err = env.store.Users().Create(context.Background(), store.CreateUserParams{
		ID:           userID,
		OrgID:        env.orgID,
		Username:     username,
		PasswordHash: hash,
		DisplayName:  username,
		PasswordSet:  true,
		IsAdmin:      false,
	})
	require.NoError(t, err)

	token, _, _, err := auth.Login(context.Background(), env.store, username, "testpass")
	require.NoError(t, err)
	return userID, token
}

type failingWorkspaceTabStore struct {
	store.WorkspaceTabStore
	bulkUpsertErr        error
	deleteByWorkspaceErr error
}

func (s failingWorkspaceTabStore) BulkUpsert(ctx context.Context, params []store.UpsertWorkspaceTabParams) error {
	if s.bulkUpsertErr != nil {
		return s.bulkUpsertErr
	}
	return s.WorkspaceTabStore.BulkUpsert(ctx, params)
}

func (s failingWorkspaceTabStore) DeleteByWorkspace(ctx context.Context, workspaceID string) error {
	if s.deleteByWorkspaceErr != nil {
		return s.deleteByWorkspaceErr
	}
	return s.WorkspaceTabStore.DeleteByWorkspace(ctx, workspaceID)
}

type failingWorkspaceLayoutStore struct {
	store.WorkspaceLayoutStore
	deleteErr error
}

func (s failingWorkspaceLayoutStore) Delete(ctx context.Context, workspaceID string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	return s.WorkspaceLayoutStore.Delete(ctx, workspaceID)
}

type wrappedStore struct {
	store.Store
	wrapTabs    func(store.WorkspaceTabStore) store.WorkspaceTabStore
	wrapLayouts func(store.WorkspaceLayoutStore) store.WorkspaceLayoutStore
}

func (s wrappedStore) WorkspaceTabs() store.WorkspaceTabStore {
	if s.wrapTabs != nil {
		return s.wrapTabs(s.Store.WorkspaceTabs())
	}
	return s.Store.WorkspaceTabs()
}

func (s wrappedStore) WorkspaceLayouts() store.WorkspaceLayoutStore {
	if s.wrapLayouts != nil {
		return s.wrapLayouts(s.Store.WorkspaceLayouts())
	}
	return s.Store.WorkspaceLayouts()
}

func (s wrappedStore) RunInTransaction(ctx context.Context, fn func(tx store.Store) error) error {
	return s.Store.RunInTransaction(ctx, func(tx store.Store) error {
		return fn(wrappedStore{
			Store:       tx,
			wrapTabs:    s.wrapTabs,
			wrapLayouts: s.wrapLayouts,
		})
	})
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

func TestWorkspaceReadAccess_SharedMemberReadOnly(t *testing.T) {
	env := setupWorkspaceTest(t)
	wsID := env.createWorkspace(t)
	viewerID, viewerToken := env.createUserAndToken(t, "viewer")

	require.NoError(t, env.store.WorkspaceTabs().Upsert(context.Background(), store.UpsertWorkspaceTabParams{
		WorkspaceID: wsID,
		WorkerID:    "w1",
		TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabID:       "agent-1",
		Position:    "a",
		TileID:      "tile-1",
	}))
	require.NoError(t, env.store.WorkspaceLayouts().Upsert(context.Background(), store.UpsertWorkspaceLayoutParams{
		WorkspaceID: wsID,
		LayoutJSON:  `{"layout":{"leaf":{"id":"tile-1"}}}`,
	}))
	require.NoError(t, env.store.WorkspaceAccess().Grant(context.Background(), store.GrantWorkspaceAccessParams{
		WorkspaceID: wsID,
		UserID:      viewerID,
	}))

	_, err := env.client.GetWorkspace(context.Background(), authedReq(&leapmuxv1.GetWorkspaceRequest{
		OrgId:       env.orgID,
		WorkspaceId: wsID,
	}, viewerToken))
	require.NoError(t, err)

	tabsResp, err := env.client.ListTabs(context.Background(), authedReq(&leapmuxv1.ListTabsRequest{
		WorkspaceId: wsID,
	}, viewerToken))
	require.NoError(t, err)
	require.Len(t, tabsResp.Msg.GetTabs(), 1)

	layoutResp, err := env.client.GetLayout(context.Background(), authedReq(&leapmuxv1.GetLayoutRequest{
		WorkspaceId: wsID,
	}, viewerToken))
	require.NoError(t, err)
	require.NotNil(t, layoutResp.Msg.GetLayout())

	_, err = env.client.AddTab(context.Background(), authedReq(&leapmuxv1.AddTabRequest{
		WorkspaceId: wsID,
		Tab: &leapmuxv1.WorkspaceTab{
			TabType:  leapmuxv1.TabType_TAB_TYPE_AGENT,
			TabId:    "agent-2",
			Position: "b",
			TileId:   "tile-1",
			WorkerId: "w1",
		},
	}, viewerToken))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	_, err = env.client.RemoveTab(context.Background(), authedReq(&leapmuxv1.RemoveTabRequest{
		WorkspaceId: wsID,
		TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabId:       "agent-1",
	}, viewerToken))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	_, err = env.client.SaveLayout(context.Background(), authedReq(&leapmuxv1.SaveLayoutRequest{
		OrgId:       env.orgID,
		WorkspaceId: wsID,
		Layout:      leafNode("tile-1"),
	}, viewerToken))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	_, err = env.client.UpdateWorkspaceSharing(context.Background(), authedReq(&leapmuxv1.UpdateWorkspaceSharingRequest{
		WorkspaceId: wsID,
		ShareMode:   leapmuxv1.ShareMode_SHARE_MODE_PRIVATE,
	}, viewerToken))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

func TestWorkspaceReadAccess_UnsharedUserDenied(t *testing.T) {
	env := setupWorkspaceTest(t)
	wsID := env.createWorkspace(t)
	_, outsiderToken := env.createUserAndToken(t, "outsider")

	_, err := env.client.GetWorkspace(context.Background(), authedReq(&leapmuxv1.GetWorkspaceRequest{
		OrgId:       env.orgID,
		WorkspaceId: wsID,
	}, outsiderToken))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	_, err = env.client.ListTabs(context.Background(), authedReq(&leapmuxv1.ListTabsRequest{
		WorkspaceId: wsID,
	}, outsiderToken))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	_, err = env.client.GetLayout(context.Background(), authedReq(&leapmuxv1.GetLayoutRequest{
		WorkspaceId: wsID,
	}, outsiderToken))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

func TestSaveLayout_RollsBackOnTabFailure(t *testing.T) {
	env := setupWorkspaceTest(t)
	wsID := env.createWorkspace(t)

	require.NoError(t, env.store.WorkspaceLayouts().Upsert(context.Background(), store.UpsertWorkspaceLayoutParams{
		WorkspaceID: wsID,
		LayoutJSON:  `{"layout":{"leaf":{"id":"old-tile"}}}`,
	}))
	require.NoError(t, env.store.WorkspaceTabs().Upsert(context.Background(), store.UpsertWorkspaceTabParams{
		WorkspaceID: wsID,
		WorkerID:    "w1",
		TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabID:       "old-tab",
		Position:    "a",
		TileID:      "old-tile",
	}))

	svc := service.NewWorkspaceService(wrappedStore{
		Store: env.store,
		wrapTabs: func(base store.WorkspaceTabStore) store.WorkspaceTabStore {
			return failingWorkspaceTabStore{WorkspaceTabStore: base, bulkUpsertErr: errors.New("boom")}
		},
	}, false)

	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: env.userID, OrgID: env.orgID, Username: "testuser", IsAdmin: true})
	_, err := svc.SaveLayout(ctx, connect.NewRequest(&leapmuxv1.SaveLayoutRequest{
		OrgId:       env.orgID,
		WorkspaceId: wsID,
		Layout:      leafNode("new-tile"),
		Tabs: []*leapmuxv1.WorkspaceTab{
			{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "new-tab", Position: "b", TileId: "new-tile", WorkerId: "w1"},
		},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))

	layout, err := env.store.WorkspaceLayouts().Get(context.Background(), wsID)
	require.NoError(t, err)
	assert.Equal(t, `{"layout":{"leaf":{"id":"old-tile"}}}`, layout.LayoutJSON)

	tabs, err := env.store.WorkspaceTabs().ListByWorkspace(context.Background(), wsID)
	require.NoError(t, err)
	require.Len(t, tabs, 1)
	assert.Equal(t, "old-tab", tabs[0].TabID)
}

func TestUpdateWorkspaceSharing_InvalidUserLeavesACLsIntact(t *testing.T) {
	env := setupWorkspaceTest(t)
	wsID := env.createWorkspace(t)
	viewerID, _ := env.createUserAndToken(t, "existing-viewer")

	require.NoError(t, env.store.WorkspaceAccess().Grant(context.Background(), store.GrantWorkspaceAccessParams{
		WorkspaceID: wsID,
		UserID:      viewerID,
	}))

	svc := service.NewWorkspaceService(env.store, false)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: env.userID, OrgID: env.orgID, Username: "testuser", IsAdmin: true})
	_, err := svc.UpdateWorkspaceSharing(ctx, connect.NewRequest(&leapmuxv1.UpdateWorkspaceSharingRequest{
		WorkspaceId: wsID,
		ShareMode:   leapmuxv1.ShareMode_SHARE_MODE_MEMBERS,
		UserIds:     []string{viewerID, "missing-user"},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	entries, err := env.store.WorkspaceAccess().ListByWorkspaceID(context.Background(), wsID)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, viewerID, entries[0].UserID)
}

func TestDeleteWorkspace_RollsBackOnCleanupFailure(t *testing.T) {
	env := setupWorkspaceTest(t)
	wsID := env.createWorkspace(t)

	require.NoError(t, env.store.WorkspaceTabs().Upsert(context.Background(), store.UpsertWorkspaceTabParams{
		WorkspaceID: wsID,
		WorkerID:    "w1",
		TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabID:       "tab-1",
		Position:    "a",
		TileID:      "tile-1",
	}))
	require.NoError(t, env.store.WorkspaceLayouts().Upsert(context.Background(), store.UpsertWorkspaceLayoutParams{
		WorkspaceID: wsID,
		LayoutJSON:  `{"layout":{"leaf":{"id":"tile-1"}}}`,
	}))

	svc := service.NewWorkspaceService(wrappedStore{
		Store: env.store,
		wrapLayouts: func(base store.WorkspaceLayoutStore) store.WorkspaceLayoutStore {
			return failingWorkspaceLayoutStore{WorkspaceLayoutStore: base, deleteErr: errors.New("layout delete failed")}
		},
	}, false)

	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: env.userID, OrgID: env.orgID, Username: "testuser", IsAdmin: true})
	_, err := svc.DeleteWorkspace(ctx, connect.NewRequest(&leapmuxv1.DeleteWorkspaceRequest{
		WorkspaceId: wsID,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))

	ws, err := env.store.Workspaces().GetByID(context.Background(), wsID)
	require.NoError(t, err)
	assert.Equal(t, wsID, ws.ID)

	tabs, err := env.store.WorkspaceTabs().ListByWorkspace(context.Background(), wsID)
	require.NoError(t, err)
	require.Len(t, tabs, 1)

	layout, err := env.store.WorkspaceLayouts().Get(context.Background(), wsID)
	require.NoError(t, err)
	assert.Equal(t, `{"layout":{"leaf":{"id":"tile-1"}}}`, layout.LayoutJSON)
}
