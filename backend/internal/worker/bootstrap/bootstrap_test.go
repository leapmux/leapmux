package bootstrap

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/hub"
)

func setupTestDB(t *testing.T) *db.Queries {
	t.Helper()
	sqlDB, err := workerdb.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	err = workerdb.Migrate(sqlDB)
	require.NoError(t, err)

	return db.New(sqlDB)
}

func TestBuildTabSync_Empty(t *testing.T) {
	queries := setupTestDB(t)
	sync := BuildTabSync(queries)

	require.NotNil(t, sync)
	assert.Empty(t, sync.GetTabs())
}

func TestBuildTabSync_AgentsFromDB(t *testing.T) {
	queries := setupTestDB(t)
	ctx := context.Background()

	// Insert agents directly into the DB (simulating persisted state).
	err := queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-1",
		WorkspaceID: "ws-1",
		WorkingDir:  "/tmp",
	})
	require.NoError(t, err)

	err = queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-2",
		WorkspaceID: "ws-2",
		WorkingDir:  "/tmp",
	})
	require.NoError(t, err)

	// Close agent-2 to verify closed agents are still included.
	err = queries.CloseAgent(ctx, "agent-2")
	require.NoError(t, err)

	sync := BuildTabSync(queries)

	require.NotNil(t, sync)
	assert.Len(t, sync.GetTabs(), 2)

	// Collect tabs into a map for order-independent assertion.
	tabMap := make(map[string]*leapmuxv1.WorkspaceTabEntry)
	for _, tab := range sync.GetTabs() {
		tabMap[tab.GetTabId()] = tab
	}

	agent1Tab := tabMap["agent-1"]
	require.NotNil(t, agent1Tab)
	assert.Equal(t, "ws-1", agent1Tab.GetWorkspaceId())
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_AGENT, agent1Tab.GetTabType())

	agent2Tab := tabMap["agent-2"]
	require.NotNil(t, agent2Tab)
	assert.Equal(t, "ws-2", agent2Tab.GetWorkspaceId())
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_AGENT, agent2Tab.GetTabType())
}

func TestBuildTabSync_TerminalsFromDB(t *testing.T) {
	queries := setupTestDB(t)
	ctx := context.Background()

	err := queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID:          "term-1",
		WorkspaceID: "ws-1",
		Cols:        80,
		Rows:        24,
		Screen:      []byte("screen data"),
	})
	require.NoError(t, err)

	sync := BuildTabSync(queries)

	require.NotNil(t, sync)
	assert.Len(t, sync.GetTabs(), 1)

	tab := sync.GetTabs()[0]
	assert.Equal(t, "ws-1", tab.GetWorkspaceId())
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_TERMINAL, tab.GetTabType())
	assert.Equal(t, "term-1", tab.GetTabId())
}

func TestBuildTabSync_MixedAgentsAndTerminals(t *testing.T) {
	queries := setupTestDB(t)
	ctx := context.Background()

	// Add an agent and a terminal.
	err := queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-1",
		WorkspaceID: "ws-1",
		WorkingDir:  "/tmp",
	})
	require.NoError(t, err)

	err = queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID:          "term-1",
		WorkspaceID: "ws-1",
		Cols:        80,
		Rows:        24,
		Screen:      []byte("data"),
	})
	require.NoError(t, err)

	sync := BuildTabSync(queries)

	require.NotNil(t, sync)
	assert.Len(t, sync.GetTabs(), 2)

	// Verify both types are present.
	types := make(map[leapmuxv1.TabType]int)
	for _, tab := range sync.GetTabs() {
		types[tab.GetTabType()]++
	}
	assert.Equal(t, 1, types[leapmuxv1.TabType_TAB_TYPE_AGENT])
	assert.Equal(t, 1, types[leapmuxv1.TabType_TAB_TYPE_TERMINAL])
}

// wireForTest assembles a Wiring against an in-memory DB and an
// unconnected hub client, which is all Wire needs: it registers handlers
// and starts background loops but never dials.
func wireForTest(t *testing.T, mode leapmuxv1.EncryptionMode) (*Wiring, *hub.Client) {
	t.Helper()

	sqlDB, err := workerdb.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, workerdb.Migrate(sqlDB))

	key, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)

	client := hub.New("http://127.0.0.1:0")
	t.Cleanup(client.Stop)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	w := Wire(Params{
		Ctx:            ctx,
		Client:         client,
		DB:             sqlDB,
		CompositeKey:   key,
		EncryptionMode: mode,
		WorkerID:       "worker-1",
		Name:           "test",
		HomeDir:        t.TempDir(),
		DataDir:        t.TempDir(),
	})
	t.Cleanup(w.Service.Shutdown)
	return w, client
}

// TestWire_AdvertisesPostQuantumKeysInEveryMode is the regression for a
// silent identity change.
//
// The keys describe the worker, not the session's cipher: the handshake
// picks its mode from EncryptionMode alone. Withholding them in classic
// mode still overwrites the hub's stored columns with empty blobs, and
// every client that had pinned the worker then fails TOFU verification
// with a key mismatch it cannot clear without manual intervention.
func TestWire_AdvertisesPostQuantumKeysInEveryMode(t *testing.T) {
	for _, mode := range []leapmuxv1.EncryptionMode{
		leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC,
		leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM,
	} {
		t.Run(mode.String(), func(t *testing.T) {
			_, client := wireForTest(t, mode)

			assert.NotEmpty(t, client.PublicKey, "X25519 key must be advertised")
			assert.NotEmpty(t, client.MlkemPublicKey,
				"ML-KEM key must be advertised so a heartbeat cannot blank the stored pin")
			assert.NotEmpty(t, client.SlhdsaPublicKey,
				"SLH-DSA key must be advertised so a heartbeat cannot blank the stored pin")
			assert.Equal(t, mode, client.EncryptionMode,
				"the mode, not key presence, is what selects the handshake")
		})
	}
}

// TestWire_PerformsEveryStepBothEntryPointsRelyOn pins the wiring steps
// whose omission in one entry point is why this package exists. Each was
// a shipped defect: a missing RemoteIPC left `leapmux remote` with no
// socket, and an unbound cleanup WaitGroup let Shutdown return while a
// close handler was still writing.
func TestWire_PerformsEveryStepBothEntryPointsRelyOn(t *testing.T) {
	w, client := wireForTest(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM)

	require.NotNil(t, w.Service)
	assert.NotNil(t, w.Service.RemoteIPC,
		"RemoteIPC must be wired; the CLI once shipped without it")
	assert.NotNil(t, client.OnWorkerIdentity,
		"the Hub delivers the owner on connect and is the authority")
	assert.NotNil(t, client.TabSyncProvider, "tab sync must be published")

	// Construction must not have left the always-non-nil fields nil.
	assert.NotNil(t, w.Service.PrivateEvents)
	assert.NotNil(t, w.Service.FileTabPaths)
}
