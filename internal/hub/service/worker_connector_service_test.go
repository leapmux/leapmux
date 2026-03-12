package service

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	hubdb "github.com/leapmux/leapmux/internal/hub/db"
	db "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
)

// setupSyncTest creates a WorkerConnectorService with an in-memory hub DB
// and inserts parent records needed by FK constraints.
func setupSyncTest(t *testing.T, workerIDs []string, workspaceIDs []string) (*WorkerConnectorService, *db.Queries) {
	t.Helper()
	sqlDB, err := hubdb.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, hubdb.Migrate(sqlDB))

	// Insert minimal parent records required by FK constraints.
	seedParents(t, sqlDB, workerIDs, workspaceIDs)

	q := db.New(sqlDB)
	svc := &WorkerConnectorService{queries: q}
	return svc, q
}

func seedParents(t *testing.T, sqlDB *sql.DB, workerIDs []string, workspaceIDs []string) {
	t.Helper()
	_, err := sqlDB.Exec(`INSERT INTO orgs (id, name) VALUES ('org-1', 'test-org')`)
	require.NoError(t, err)
	_, err = sqlDB.Exec(`INSERT INTO users (id, org_id, username, password_hash) VALUES ('user-1', 'org-1', 'testuser', 'hash')`)
	require.NoError(t, err)
	for _, wid := range workerIDs {
		_, err = sqlDB.Exec(`INSERT INTO workers (id, org_id, auth_token, registered_by) VALUES (?, 'org-1', ?, 'user-1')`, wid, "token-"+wid)
		require.NoError(t, err)
	}
	for _, wsid := range workspaceIDs {
		_, err = sqlDB.Exec(`INSERT INTO workspaces (id, org_id, owner_user_id, title) VALUES (?, 'org-1', 'user-1', ?)`, wsid, "ws-"+wsid)
		require.NoError(t, err)
	}
}

func TestHandleWorkspaceTabsSync_WorkspaceMismatchHealed(t *testing.T) {
	ctx := context.Background()
	svc, q := setupSyncTest(t, []string{"w1"}, []string{"ws-A", "ws-B"})

	// Hub has tab in ws-A.
	require.NoError(t, q.UpsertWorkspaceTab(ctx, db.UpsertWorkspaceTabParams{
		WorkspaceID: "ws-A",
		WorkerID:    "w1",
		TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabID:       "a1",
		Position:    "0001",
		TileID:      "tile-1",
	}))

	// Worker reports tab in ws-B (moved on worker side).
	svc.handleWorkspaceTabsSync(ctx, "w1", &leapmuxv1.WorkspaceTabsSync{
		Tabs: []*leapmuxv1.WorkspaceTabEntry{
			{WorkspaceId: "ws-B", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "a1"},
		},
	})

	// Verify hub entry was moved to ws-B, preserving position and tile_id.
	tabs, err := q.ListWorkspaceTabsByWorker(ctx, "w1")
	require.NoError(t, err)
	require.Len(t, tabs, 1)
	assert.Equal(t, "ws-B", tabs[0].WorkspaceID)
	assert.Equal(t, "a1", tabs[0].TabID)
	assert.Equal(t, "0001", tabs[0].Position)
	assert.Equal(t, "tile-1", tabs[0].TileID)
}

func TestHandleWorkspaceTabsSync_MultipleMixedStates(t *testing.T) {
	ctx := context.Background()
	svc, q := setupSyncTest(t, []string{"w1"}, []string{"ws-A", "ws-B"})

	// Hub: agent a1 in ws-A, terminal t1 in ws-A.
	require.NoError(t, q.UpsertWorkspaceTab(ctx, db.UpsertWorkspaceTabParams{
		WorkspaceID: "ws-A", WorkerID: "w1",
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "a1",
		Position: "0001", TileID: "tile-1",
	}))
	require.NoError(t, q.UpsertWorkspaceTab(ctx, db.UpsertWorkspaceTabParams{
		WorkspaceID: "ws-A", WorkerID: "w1",
		TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabID: "t1",
		Position: "0002", TileID: "tile-2",
	}))

	// Worker: agent a1 moved to ws-B, terminal t1 still in ws-A.
	svc.handleWorkspaceTabsSync(ctx, "w1", &leapmuxv1.WorkspaceTabsSync{
		Tabs: []*leapmuxv1.WorkspaceTabEntry{
			{WorkspaceId: "ws-B", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "a1"},
			{WorkspaceId: "ws-A", TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: "t1"},
		},
	})

	tabs, err := q.ListWorkspaceTabsByWorker(ctx, "w1")
	require.NoError(t, err)
	require.Len(t, tabs, 2)

	tabMap := make(map[string]db.WorkspaceTab)
	for _, tab := range tabs {
		tabMap[tab.TabID] = tab
	}
	assert.Equal(t, "ws-B", tabMap["a1"].WorkspaceID, "agent should be moved to ws-B")
	assert.Equal(t, "ws-A", tabMap["t1"].WorkspaceID, "terminal should stay in ws-A")
}

func TestHandleWorkspaceTabsSync_StaleTabDeleted(t *testing.T) {
	ctx := context.Background()
	svc, q := setupSyncTest(t, []string{"w1"}, []string{"ws-A"})

	// Hub has tab that worker no longer has.
	require.NoError(t, q.UpsertWorkspaceTab(ctx, db.UpsertWorkspaceTabParams{
		WorkspaceID: "ws-A", WorkerID: "w1",
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "a2",
		Position: "0001",
	}))

	// Worker reports no tabs.
	svc.handleWorkspaceTabsSync(ctx, "w1", &leapmuxv1.WorkspaceTabsSync{})

	tabs, err := q.ListWorkspaceTabsByWorker(ctx, "w1")
	require.NoError(t, err)
	assert.Empty(t, tabs)
}

func TestHandleWorkspaceTabsSync_MatchingTabsUntouched(t *testing.T) {
	ctx := context.Background()
	svc, q := setupSyncTest(t, []string{"w1"}, []string{"ws-A"})

	require.NoError(t, q.UpsertWorkspaceTab(ctx, db.UpsertWorkspaceTabParams{
		WorkspaceID: "ws-A", WorkerID: "w1",
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "a1",
		Position: "0001", TileID: "tile-1",
	}))

	// Worker reports same tab in same workspace — no changes.
	svc.handleWorkspaceTabsSync(ctx, "w1", &leapmuxv1.WorkspaceTabsSync{
		Tabs: []*leapmuxv1.WorkspaceTabEntry{
			{WorkspaceId: "ws-A", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "a1"},
		},
	})

	tabs, err := q.ListWorkspaceTabsByWorker(ctx, "w1")
	require.NoError(t, err)
	require.Len(t, tabs, 1)
	assert.Equal(t, "ws-A", tabs[0].WorkspaceID)
	assert.Equal(t, "0001", tabs[0].Position)
	assert.Equal(t, "tile-1", tabs[0].TileID)
}

func TestHandleWorkspaceTabsSync_WorkerOnlyTabIgnored(t *testing.T) {
	ctx := context.Background()
	svc, q := setupSyncTest(t, []string{"w1"}, []string{"ws-C"})

	// Hub has no tabs for this worker.
	// Worker reports a tab — should NOT be added (hub is source of truth for visibility).
	svc.handleWorkspaceTabsSync(ctx, "w1", &leapmuxv1.WorkspaceTabsSync{
		Tabs: []*leapmuxv1.WorkspaceTabEntry{
			{WorkspaceId: "ws-C", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "a3"},
		},
	})

	tabs, err := q.ListWorkspaceTabsByWorker(ctx, "w1")
	require.NoError(t, err)
	assert.Empty(t, tabs, "worker-only tab should not be added to hub")
}

func TestHandleWorkspaceTabsSync_EmptyWorkerSync(t *testing.T) {
	ctx := context.Background()
	svc, q := setupSyncTest(t, []string{"w1"}, []string{"ws-A"})

	// Hub has tabs.
	require.NoError(t, q.UpsertWorkspaceTab(ctx, db.UpsertWorkspaceTabParams{
		WorkspaceID: "ws-A", WorkerID: "w1",
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "a1",
		Position: "0001",
	}))
	require.NoError(t, q.UpsertWorkspaceTab(ctx, db.UpsertWorkspaceTabParams{
		WorkspaceID: "ws-A", WorkerID: "w1",
		TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabID: "t1",
		Position: "0002",
	}))

	// Worker reports no tabs — all hub tabs deleted.
	svc.handleWorkspaceTabsSync(ctx, "w1", &leapmuxv1.WorkspaceTabsSync{})

	tabs, err := q.ListWorkspaceTabsByWorker(ctx, "w1")
	require.NoError(t, err)
	assert.Empty(t, tabs)
}

func TestHandleWorkspaceTabsSync_EmptyHubState(t *testing.T) {
	ctx := context.Background()
	svc, q := setupSyncTest(t, []string{"w1"}, []string{"ws-A", "ws-B"})

	// Hub has no tabs, worker reports tabs — no changes (no adds).
	svc.handleWorkspaceTabsSync(ctx, "w1", &leapmuxv1.WorkspaceTabsSync{
		Tabs: []*leapmuxv1.WorkspaceTabEntry{
			{WorkspaceId: "ws-A", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "a1"},
			{WorkspaceId: "ws-B", TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: "t1"},
		},
	})

	tabs, err := q.ListWorkspaceTabsByWorker(ctx, "w1")
	require.NoError(t, err)
	assert.Empty(t, tabs, "hub should not gain tabs from worker-only entries")
}

// --- Encryption mode validation tests ---

func setupEncryptionModeTest(t *testing.T, soloMode bool) (*WorkerConnectorService, *workermgr.Conn) {
	t.Helper()
	svc, _ := setupSyncTest(t, []string{"w1"}, nil)
	svc.soloMode = soloMode
	conn := &workermgr.Conn{WorkerID: "w1", OrgID: "org-1"}
	return svc, conn
}

func TestProcessWorkerMessage_ClassicModeAccepted(t *testing.T) {
	svc, conn := setupEncryptionModeTest(t, false)
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
	svc, conn := setupEncryptionModeTest(t, false)
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
	svc, conn := setupEncryptionModeTest(t, true)
	ctx := context.Background()

	// Simulate a heartbeat with X25519 public key but nil ML-KEM key,
	// which used to trigger a NOT NULL constraint failure on workers.mlkem_public_key.
	err := svc.processWorkerMessage(ctx, conn, "w1", &leapmuxv1.ConnectRequest{
		Payload: &leapmuxv1.ConnectRequest_Heartbeat{
			Heartbeat: &leapmuxv1.Heartbeat{
				PublicKey:      []byte("fake-x25519-public-key"),
				EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM,
			},
		},
	})
	require.NoError(t, err)

	// Verify the public key was persisted without constraint errors.
	pk, dbErr := svc.queries.GetWorkerPublicKey(ctx, "w1")
	require.NoError(t, dbErr)
	assert.Equal(t, []byte("fake-x25519-public-key"), pk.PublicKey)
}

func TestProcessWorkerMessage_UnspecifiedHeartbeatRejectedAfterModeSet(t *testing.T) {
	svc, conn := setupEncryptionModeTest(t, true)
	ctx := context.Background()

	// Initial heartbeat sets POST_QUANTUM mode.
	err := svc.processWorkerMessage(ctx, conn, "w1", &leapmuxv1.ConnectRequest{
		Payload: &leapmuxv1.ConnectRequest_Heartbeat{
			Heartbeat: &leapmuxv1.Heartbeat{
				EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM,
			},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM, conn.EncryptionMode)

	// Subsequent heartbeat without encryption_mode set (UNSPECIFIED)
	// should be rejected — a worker must always include its encryption mode.
	err = svc.processWorkerMessage(ctx, conn, "w1", &leapmuxv1.ConnectRequest{
		Payload: &leapmuxv1.ConnectRequest_Heartbeat{
			Heartbeat: &leapmuxv1.Heartbeat{},
		},
	})
	require.Error(t, err, "subsequent heartbeat with UNSPECIFIED encryption mode should be rejected")
}

func TestProcessWorkerMessage_UnspecifiedDefaultsToPostQuantum(t *testing.T) {
	svc, conn := setupEncryptionModeTest(t, false)
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
