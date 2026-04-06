package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
)

// setupSyncTest creates a WorkerConnectorService with an in-memory hub DB
// and inserts parent records needed by FK constraints.
func setupSyncTest(t *testing.T, workerIDs []string, workspaceIDs []string) (*WorkerConnectorService, store.Store) {
	t.Helper()
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Migrator().Migrate(context.Background()))

	// Insert minimal parent records required by FK constraints.
	seedParents(t, st, workerIDs, workspaceIDs)

	svc := &WorkerConnectorService{store: st}
	return svc, st
}

func seedParents(t *testing.T, st store.Store, workerIDs []string, workspaceIDs []string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, st.Orgs().Create(ctx, store.CreateOrgParams{ID: "org-1", Name: "test-org"}))
	require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{ID: "user-1", OrgID: "org-1", Username: "testuser", PasswordHash: "hash"}))
	for _, wid := range workerIDs {
		require.NoError(t, st.Workers().Create(ctx, store.CreateWorkerParams{
			ID: wid, AuthToken: "token-" + wid, RegisteredBy: "user-1",
			PublicKey: []byte{}, MlkemPublicKey: []byte{}, SlhdsaPublicKey: []byte{},
		}))
	}
	for _, wsid := range workspaceIDs {
		require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{
			ID: wsid, OrgID: "org-1", OwnerUserID: "user-1", Title: "ws-" + wsid,
		}))
	}
}

func TestHandleWorkspaceTabsSync_WorkspaceMismatchHealed(t *testing.T) {
	ctx := context.Background()
	svc, st := setupSyncTest(t, []string{"w1"}, []string{"ws-A", "ws-B"})

	require.NoError(t, st.WorkspaceTabs().Upsert(ctx, store.UpsertWorkspaceTabParams{
		WorkspaceID: "ws-A", WorkerID: "w1",
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "a1",
		Position: "0001", TileID: "tile-1",
	}))

	svc.handleWorkspaceTabsSync(ctx, "w1", &leapmuxv1.WorkspaceTabsSync{
		Tabs: []*leapmuxv1.WorkspaceTabEntry{
			{WorkspaceId: "ws-B", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "a1"},
		},
	})

	tabs, err := st.WorkspaceTabs().ListByWorker(ctx, "w1")
	require.NoError(t, err)
	require.Len(t, tabs, 1)
	assert.Equal(t, "ws-B", tabs[0].WorkspaceID)
	assert.Equal(t, "a1", tabs[0].TabID)
	assert.Equal(t, "0001", tabs[0].Position)
	assert.Equal(t, "tile-1", tabs[0].TileID)
}

func TestHandleWorkspaceTabsSync_MultipleMixedStates(t *testing.T) {
	ctx := context.Background()
	svc, st := setupSyncTest(t, []string{"w1"}, []string{"ws-A", "ws-B"})

	require.NoError(t, st.WorkspaceTabs().Upsert(ctx, store.UpsertWorkspaceTabParams{
		WorkspaceID: "ws-A", WorkerID: "w1",
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "a1",
		Position: "0001", TileID: "tile-1",
	}))
	require.NoError(t, st.WorkspaceTabs().Upsert(ctx, store.UpsertWorkspaceTabParams{
		WorkspaceID: "ws-A", WorkerID: "w1",
		TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabID: "t1",
		Position: "0002", TileID: "tile-2",
	}))

	svc.handleWorkspaceTabsSync(ctx, "w1", &leapmuxv1.WorkspaceTabsSync{
		Tabs: []*leapmuxv1.WorkspaceTabEntry{
			{WorkspaceId: "ws-B", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "a1"},
			{WorkspaceId: "ws-A", TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: "t1"},
		},
	})

	tabs, err := st.WorkspaceTabs().ListByWorker(ctx, "w1")
	require.NoError(t, err)
	require.Len(t, tabs, 2)

	tabMap := make(map[string]store.WorkspaceTab)
	for _, tab := range tabs {
		tabMap[tab.TabID] = tab
	}
	assert.Equal(t, "ws-B", tabMap["a1"].WorkspaceID)
	assert.Equal(t, "ws-A", tabMap["t1"].WorkspaceID)
}

func TestHandleWorkspaceTabsSync_StaleTabDeleted(t *testing.T) {
	ctx := context.Background()
	svc, st := setupSyncTest(t, []string{"w1"}, []string{"ws-A"})

	require.NoError(t, st.WorkspaceTabs().Upsert(ctx, store.UpsertWorkspaceTabParams{
		WorkspaceID: "ws-A", WorkerID: "w1",
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "a2",
		Position: "0001",
	}))

	svc.handleWorkspaceTabsSync(ctx, "w1", &leapmuxv1.WorkspaceTabsSync{})

	tabs, err := st.WorkspaceTabs().ListByWorker(ctx, "w1")
	require.NoError(t, err)
	assert.Empty(t, tabs)
}

func TestHandleWorkspaceTabsSync_MatchingTabsUntouched(t *testing.T) {
	ctx := context.Background()
	svc, st := setupSyncTest(t, []string{"w1"}, []string{"ws-A"})

	require.NoError(t, st.WorkspaceTabs().Upsert(ctx, store.UpsertWorkspaceTabParams{
		WorkspaceID: "ws-A", WorkerID: "w1",
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "a1",
		Position: "0001", TileID: "tile-1",
	}))

	svc.handleWorkspaceTabsSync(ctx, "w1", &leapmuxv1.WorkspaceTabsSync{
		Tabs: []*leapmuxv1.WorkspaceTabEntry{
			{WorkspaceId: "ws-A", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "a1"},
		},
	})

	tabs, err := st.WorkspaceTabs().ListByWorker(ctx, "w1")
	require.NoError(t, err)
	require.Len(t, tabs, 1)
	assert.Equal(t, "ws-A", tabs[0].WorkspaceID)
	assert.Equal(t, "0001", tabs[0].Position)
	assert.Equal(t, "tile-1", tabs[0].TileID)
}

func TestHandleWorkspaceTabsSync_WorkerOnlyTabIgnored(t *testing.T) {
	ctx := context.Background()
	svc, st := setupSyncTest(t, []string{"w1"}, []string{"ws-C"})

	svc.handleWorkspaceTabsSync(ctx, "w1", &leapmuxv1.WorkspaceTabsSync{
		Tabs: []*leapmuxv1.WorkspaceTabEntry{
			{WorkspaceId: "ws-C", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "a3"},
		},
	})

	tabs, err := st.WorkspaceTabs().ListByWorker(ctx, "w1")
	require.NoError(t, err)
	assert.Empty(t, tabs)
}

func TestHandleWorkspaceTabsSync_EmptyWorkerSync(t *testing.T) {
	ctx := context.Background()
	svc, st := setupSyncTest(t, []string{"w1"}, []string{"ws-A"})

	require.NoError(t, st.WorkspaceTabs().Upsert(ctx, store.UpsertWorkspaceTabParams{
		WorkspaceID: "ws-A", WorkerID: "w1",
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "a1",
		Position: "0001",
	}))
	require.NoError(t, st.WorkspaceTabs().Upsert(ctx, store.UpsertWorkspaceTabParams{
		WorkspaceID: "ws-A", WorkerID: "w1",
		TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabID: "t1",
		Position: "0002",
	}))

	svc.handleWorkspaceTabsSync(ctx, "w1", &leapmuxv1.WorkspaceTabsSync{})

	tabs, err := st.WorkspaceTabs().ListByWorker(ctx, "w1")
	require.NoError(t, err)
	assert.Empty(t, tabs)
}

func TestHandleWorkspaceTabsSync_EmptyHubState(t *testing.T) {
	ctx := context.Background()
	svc, st := setupSyncTest(t, []string{"w1"}, []string{"ws-A", "ws-B"})

	svc.handleWorkspaceTabsSync(ctx, "w1", &leapmuxv1.WorkspaceTabsSync{
		Tabs: []*leapmuxv1.WorkspaceTabEntry{
			{WorkspaceId: "ws-A", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "a1"},
			{WorkspaceId: "ws-B", TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: "t1"},
		},
	})

	tabs, err := st.WorkspaceTabs().ListByWorker(ctx, "w1")
	require.NoError(t, err)
	assert.Empty(t, tabs)
}

// --- Encryption mode validation tests ---

func setupEncryptionModeTest(t *testing.T) (*WorkerConnectorService, *workermgr.Conn) {
	t.Helper()
	svc, _ := setupSyncTest(t, []string{"w1"}, nil)
	conn := &workermgr.Conn{WorkerID: "w1"}
	return svc, conn
}

func TestProcessWorkerMessage_ClassicModeAccepted(t *testing.T) {
	svc, conn := setupEncryptionModeTest(t)
	ctx := context.Background()

	err := svc.processWorkerMessage(ctx, conn, "w1", &leapmuxv1.ConnectRequest{
		Payload: &leapmuxv1.ConnectRequest_Heartbeat{
			Heartbeat: &leapmuxv1.Heartbeat{
				EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC,
			},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC, conn.EncryptionMode)
}

func TestProcessWorkerMessage_PostQuantumModeAccepted(t *testing.T) {
	svc, conn := setupEncryptionModeTest(t)
	ctx := context.Background()

	err := svc.processWorkerMessage(ctx, conn, "w1", &leapmuxv1.ConnectRequest{
		Payload: &leapmuxv1.ConnectRequest_Heartbeat{
			Heartbeat: &leapmuxv1.Heartbeat{
				EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM,
			},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM, conn.EncryptionMode)
}

func TestProcessWorkerMessage_PublicKeyUpdateWithNilMlkem(t *testing.T) {
	svc, conn := setupEncryptionModeTest(t)
	ctx := context.Background()

	err := svc.processWorkerMessage(ctx, conn, "w1", &leapmuxv1.ConnectRequest{
		Payload: &leapmuxv1.ConnectRequest_Heartbeat{
			Heartbeat: &leapmuxv1.Heartbeat{
				PublicKey:      []byte("fake-x25519-public-key"),
				EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM,
			},
		},
	})
	require.NoError(t, err)

	pk, dbErr := svc.store.Workers().GetPublicKey(ctx, "w1")
	require.NoError(t, dbErr)
	assert.Equal(t, []byte("fake-x25519-public-key"), pk.PublicKey)
}

func TestProcessWorkerMessage_UnspecifiedHeartbeatRejectedAfterModeSet(t *testing.T) {
	svc, conn := setupEncryptionModeTest(t)
	ctx := context.Background()

	err := svc.processWorkerMessage(ctx, conn, "w1", &leapmuxv1.ConnectRequest{
		Payload: &leapmuxv1.ConnectRequest_Heartbeat{
			Heartbeat: &leapmuxv1.Heartbeat{
				EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM,
			},
		},
	})
	require.NoError(t, err)

	err = svc.processWorkerMessage(ctx, conn, "w1", &leapmuxv1.ConnectRequest{
		Payload: &leapmuxv1.ConnectRequest_Heartbeat{
			Heartbeat: &leapmuxv1.Heartbeat{},
		},
	})
	require.Error(t, err)
}

func TestProcessWorkerMessage_UnspecifiedDefaultsToPostQuantum(t *testing.T) {
	svc, conn := setupEncryptionModeTest(t)
	ctx := context.Background()

	err := svc.processWorkerMessage(ctx, conn, "w1", &leapmuxv1.ConnectRequest{
		Payload: &leapmuxv1.ConnectRequest_Heartbeat{
			Heartbeat: &leapmuxv1.Heartbeat{
				EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_UNSPECIFIED,
			},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM, conn.EncryptionMode)
}
