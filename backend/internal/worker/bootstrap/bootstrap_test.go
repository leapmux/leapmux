package bootstrap

import (
	"context"
	"database/sql"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/hubtransport"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/agent"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/hub"

	"github.com/leapmux/leapmux/internal/authscope"
)

// newTestHubClient builds a hub.Client for url. These tests care about the
// wiring around the client, not about its transport, so they state a URL and
// let hubtransport build the endpoint (which opens no connection).
func newTestHubClient(t *testing.T, url string) *hub.Client {
	t.Helper()
	endpoint, err := hubtransport.New(url)
	require.NoError(t, err, "hubtransport.New(%q)", url)
	return hub.New(endpoint)
}

// testChannelGrant is the grant a channel-open fixture announces.
//
// The Hub sends the opening credential's scopes on the wire, and a worker that
// receives NONE refuses the handshake -- the zero grant reaches nothing, so an
// omitted field denies rather than admits. Every fixture here is about what
// happens AFTER the channel opens, so each announces the ordinary
// non-administrative grant. A test that means to exercise a narrower one says
// so at its own call site.
var testChannelGrant = authscope.ScopesToWire(authscope.NonAdminGrant())

func setupTestDB(t *testing.T) *db.Queries {
	t.Helper()
	queries, _ := setupTestDBWithHandle(t)
	return queries
}

// setupTestDBWithHandle also hands back the raw *sql.DB, for the few tests
// that need to break the schema to exercise a read-failure path.
func setupTestDBWithHandle(t *testing.T) (*db.Queries, *sql.DB) {
	t.Helper()
	sqlDB, err := workerdb.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	err = workerdb.Migrate(context.Background(), sqlDB)
	require.NoError(t, err)

	return db.New(sqlDB), sqlDB
}

func TestBuildTabSync_Empty(t *testing.T) {
	queries := setupTestDB(t)
	sync, err := BuildTabSync(queries, userid.MustNew("user-1"))
	require.NoError(t, err)

	require.NotNil(t, sync)
	assert.Empty(t, sync.GetTabs())
}

func TestBuildTabSync_AgentsFromDB(t *testing.T) {
	queries := setupTestDB(t)
	ctx := context.Background()

	// Insert agents directly into the DB (simulating persisted state).
	err := queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:         "agent-1",
		WorkingDir: "/tmp",
	})
	require.NoError(t, err)

	err = queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:         "agent-2",
		WorkingDir: "/tmp",
	})
	require.NoError(t, err)

	// Close agent-2 to verify closed agents are still included.
	_, err = queries.CloseAgent(ctx, "agent-2")
	require.NoError(t, err)

	sync, err := BuildTabSync(queries, userid.MustNew("user-1"))
	require.NoError(t, err)

	require.NotNil(t, sync)
	assert.Len(t, sync.GetTabs(), 2)

	// Collect tabs into a map for order-independent assertion.
	tabMap := make(map[string]*leapmuxv1.TabRef)
	for _, tab := range sync.GetTabs() {
		tabMap[tab.GetTabId()] = tab
	}

	agent1Tab := tabMap["agent-1"]
	require.NotNil(t, agent1Tab)
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_AGENT, agent1Tab.GetTabType())

	agent2Tab := tabMap["agent-2"]
	require.NotNil(t, agent2Tab)
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_AGENT, agent2Tab.GetTabType())
}

func TestBuildTabSync_TerminalsFromDB(t *testing.T) {
	queries := setupTestDB(t)
	ctx := context.Background()

	err := queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID:     "term-1",
		Cols:   80,
		Rows:   24,
		Screen: []byte("screen data"),
	})
	require.NoError(t, err)

	sync, err := BuildTabSync(queries, userid.MustNew("user-1"))
	require.NoError(t, err)

	require.NotNil(t, sync)
	assert.Len(t, sync.GetTabs(), 1)

	tab := sync.GetTabs()[0]
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_TERMINAL, tab.GetTabType())
	assert.Equal(t, "term-1", tab.GetTabId())
}

func TestBuildTabSync_MixedAgentsAndTerminals(t *testing.T) {
	queries := setupTestDB(t)
	ctx := context.Background()

	// Add an agent and a terminal.
	err := queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:         "agent-1",
		WorkingDir: "/tmp",
	})
	require.NoError(t, err)

	err = queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID:     "term-1",
		Cols:   80,
		Rows:   24,
		Screen: []byte("data"),
	})
	require.NoError(t, err)

	sync, err := BuildTabSync(queries, userid.MustNew("user-1"))
	require.NoError(t, err)

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

// TestBuildTabSync_ReportsFileTabsScopedToTheOwner pins the completeness of the
// report, not merely that file tabs "appear". The hub tombstones every owned tab
// the report omits, and a file tab's CRDT row carries a worker_id, so it IS in the
// hub's owned list -- omitting the FILE arm deleted the user's open file tabs from
// every session on every single reconnect.
//
// It also pins the OWNER scoping, which is the other half of being correct here.
// This test previously asserted the opposite -- that both owners' rows are
// reported, on the reasoning that "the hub asks a worker for everything it hosts".
// The wire disagrees: the report carries no user axis, so the hub attributes all
// of it to the connecting registrant. A row belonging to a stale second owner
// (ClearState removes state.json, not worker.db, and workers.registered_by is
// never UPDATEd) would therefore be read as the registrant's, and a colliding
// client-minted id -- worker_file_tabs is PK'd (user_id, tab_id) precisely because
// those ids are unique only within a user -- would suppress a tombstone the real
// owner's tab was due, resurrecting a file tab they closed elsewhere.
func TestBuildTabSync_ReportsFileTabsScopedToTheOwner(t *testing.T) {
	queries := setupTestDB(t)
	ctx := context.Background()

	require.NoError(t, queries.UpsertWorkerFileTab(ctx, db.UpsertWorkerFileTabParams{
		UserID:   "user-1",
		TabID:    "file-1",
		FilePath: "/tmp/a.go",
	}))
	// A stale second owner's row, which must NOT be attributed to user-1.
	require.NoError(t, queries.UpsertWorkerFileTab(ctx, db.UpsertWorkerFileTabParams{
		UserID:   "user-2",
		TabID:    "file-2",
		FilePath: "/tmp/b.go",
	}))

	sync, err := BuildTabSync(queries, userid.MustNew("user-1"))
	require.NoError(t, err)

	reported := make(map[string]leapmuxv1.TabType)
	for _, tab := range sync.GetTabs() {
		reported[tab.GetTabId()] = tab.GetTabType()
	}
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_FILE, reported["file-1"],
		"the connecting owner's file tab must be reported")
	assert.NotContains(t, reported, "file-2",
		"another owner's file tab must not be reported as this registrant's, or it suppresses a tombstone that owner's tab was due")
}

// TestBuildTabSync_RefusesWithoutARegisteredOwner pins the fail-closed half: with
// no owner there is no one to attribute the report to, and sending it anyway is
// how foreign rows would be claimed by whoever connects next.
func TestBuildTabSync_RefusesWithoutARegisteredOwner(t *testing.T) {
	queries := setupTestDB(t)
	_, err := BuildTabSync(queries, userid.UserID{})
	require.Error(t, err, "an unminted owner must refuse rather than report every row on the machine")
}

// TestBuildTabSync_ReadFailureIsAnErrorNotAnEmptyReport pins the other half of
// the completeness contract. An empty report is indistinguishable from "this
// worker hosts nothing", which the hub answers by tombstoning everything it
// believes the worker owns -- so a transient read failure must surface as an
// error and suppress the send, never degrade into a partial inventory.
func TestBuildTabSync_ReadFailureIsAnErrorNotAnEmptyReport(t *testing.T) {
	queries, sqlDB := setupTestDBWithHandle(t)
	ctx := context.Background()

	require.NoError(t, queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:         "agent-1",
		WorkingDir: "/tmp",
	}))
	// Drop the terminals table so the middle read fails after the agent read
	// has already succeeded -- the exact shape that produced a truncated,
	// agent-only report.
	_, execErr := sqlDB.Exec(`DROP TABLE terminals`)
	require.NoError(t, execErr)

	sync, err := BuildTabSync(queries, userid.MustNew("user-1"))

	require.Error(t, err, "a failed read must be reported, not swallowed")
	assert.Nil(t, sync, "no partial report may escape")
}

// wireForTest assembles a Wiring against an in-memory DB and an
// unconnected hub client, which is all Wire needs: it registers handlers
// and starts background loops but never dials.
func wireForTest(t *testing.T, mode leapmuxv1.EncryptionMode) (*Wiring, *hub.Client) {
	t.Helper()

	sqlDB, err := workerdb.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, workerdb.Migrate(context.Background(), sqlDB))

	key, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)

	client := newTestHubClient(t, "http://127.0.0.1:0")
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
	t.Cleanup(w.Shutdown)
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
// a shipped defect: a missing ControlIPC left `leapmux control` with no
// socket, and an unbound cleanup WaitGroup let Shutdown return while a
// close handler was still writing.
func TestWire_PerformsEveryStepBothEntryPointsRelyOn(t *testing.T) {
	w, client := wireForTest(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM)

	require.NotNil(t, w.Service)
	assert.NotNil(t, w.Service.ControlIPC,
		"ControlIPC must be wired; the CLI once shipped without it")
	assert.NotNil(t, client.OnWorkerIdentity,
		"the Hub delivers the owner on connect and is the authority")
	assert.NotNil(t, client.TabSyncProvider, "tab sync must be published")

	// Construction must not have left the always-non-nil fields nil.
	assert.NotNil(t, w.Service.PrivateEvents)
	assert.NotNil(t, w.Service.FileTabPaths)
}

// TestWire_InstallsTheAgentExitHandler pins the one call that gives a dead
// process's background-task rows a final status.
//
// It fails silently if dropped, which is why it needs a guard of its own rather
// than a test that calls HandleAgentProcessExit directly: without the handler,
// every in-flight subagent and shell row stays 'running' after a crash, the
// sidebar keeps showing work that is not happening, and the parent tab keeps a
// thinking indicator an active row pins -- with no error anywhere to trace back
// to the missing wiring.
func TestWire_InstallsTheAgentExitHandler(t *testing.T) {
	sqlDB, err := workerdb.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, workerdb.Migrate(context.Background(), sqlDB))

	key, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)

	client := newTestHubClient(t, "http://127.0.0.1:0")
	t.Cleanup(client.Stop)

	// A non-nil check would prove nothing: the manager is constructed carrying a
	// logging placeholder, so the field is never nil. Only its REPLACEMENT says
	// the service-aware handler was installed.
	placeholder := reflect.ValueOf(client.AgentManager().ExitHandlerForTest()).Pointer()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w := Wire(Params{
		Ctx:            ctx,
		Client:         client,
		DB:             sqlDB,
		CompositeKey:   key,
		EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM,
		WorkerID:       "worker-1",
		Name:           "test",
		HomeDir:        t.TempDir(),
		DataDir:        t.TempDir(),
	})
	t.Cleanup(w.Shutdown)

	assert.NotEqual(t, placeholder,
		reflect.ValueOf(client.AgentManager().ExitHandlerForTest()).Pointer(),
		"a dead agent must be able to give its background-task rows a final status")
}

// TestWiring_ShutdownFlushesTheOutboundQueue pins the pairing that used to be
// stated twice, in prose, in two entry points.
//
// Service.Shutdown broadcasts the terminal disconnect notice, but Client.Send
// only ENQUEUES it: an entry point that tore the stream down straight afterwards
// raced the drain and lost under load, and the loss is silent -- the writer
// discards its queue and logs nothing, so the browser's terminal just stops.
// One entry point cross-referenced the other as documentation, which is exactly
// how a third would ship half of it. Wiring.Shutdown owns both halves now.
func TestWiring_ShutdownFlushesTheOutboundQueue(t *testing.T) {
	w, client := wireForTest(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM)

	// A never-connected client has no writer, so FlushSends is a no-op that
	// must still be REACHED -- and must not panic or error on the nil writer,
	// since a worker can be shut down before it ever connects.
	require.NoError(t, client.FlushSends())

	// Shutdown is the single seam; calling it must not require the caller to
	// remember the flush. Idempotent, because every exit path converges here.
	w.Shutdown()
	w.Shutdown()
}

func TestWire_PropagatesMaxMessageSize(t *testing.T) {
	sqlDB, err := workerdb.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, workerdb.Migrate(context.Background(), sqlDB))

	key, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)

	client := newTestHubClient(t, "http://127.0.0.1:0")
	t.Cleanup(client.Stop)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	const configured = 2 << 20
	w := Wire(Params{
		Ctx:            ctx,
		Client:         client,
		DB:             sqlDB,
		CompositeKey:   key,
		EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC,
		WorkerID:       "worker-1",
		Name:           "test",
		HomeDir:        t.TempDir(),
		DataDir:        t.TempDir(),
		MaxMessageSize: configured,
	})
	t.Cleanup(w.Service.Shutdown)
	t.Cleanup(func() { agent.ConfigureMaxMessageSize(0) })

	assert.Equal(t, configured, w.Service.MaxMessageSize,
		"Wire must carry MaxMessageSize into the service producer ceiling")

	_, msg1, err := noiseutil.ClassicalInitiatorHandshake1(key.X25519Public)
	require.NoError(t, err)
	resp := w.Service.Channels.HandleOpen(&leapmuxv1.ChannelOpenRequest{
		ChannelId:        "ch-wired-max",
		UserId:           "user-1",
		HandshakePayload: msg1,
		MaxMessageSize:   uint64(4 << 20), // hub larger than worker → worker wins
		GrantedScopes:    testChannelGrant,
	})
	require.Empty(t, resp.GetError())
	assert.Equal(t, uint64(configured), resp.GetMaxMessageSize(),
		"channel manager must negotiate with the wired worker budget")
}

// TestLiveTabForMint_SkipsChildAgents verifies the delegation-mint tab picker
// never returns a CHILD agent id: a child is excluded from tab_locations
// (parent_agent_id IS NULL), so the hub 403s it ("tab not owned by calling
// worker") and the mint backoff loops to permanent failure. Only roots are
// worker-owned tabs.
func TestLiveTabForMint_SkipsChildAgents(t *testing.T) {
	queries := setupTestDB(t)
	ctx := context.Background()

	// Root must exist before the child (foreign key on parent_agent_id).
	require.NoError(t, queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:         "root-1",
		WorkingDir: "/tmp",
	}))
	require.NoError(t, queries.CreateChildAgent(ctx, db.CreateChildAgentParams{
		ID:            "child-1",
		ParentAgentID: sql.NullString{String: "root-1", Valid: true},
		SpawnSpanID:   "span-1",
		WorkingDir:    "/tmp",
		HomeDir:       "/tmp",
		Title:         "child",
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))

	provider := liveTabForMint(queries)
	id, tabType, ok := provider()
	require.True(t, ok)
	assert.Equal(t, "root-1", id, "mint must target a root agent, never a child")
	assert.Equal(t, int32(leapmuxv1.TabType_TAB_TYPE_AGENT), tabType)
}
