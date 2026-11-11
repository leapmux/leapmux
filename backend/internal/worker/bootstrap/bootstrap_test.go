package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/hubtransport"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/util/periodic"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/leapmux/leapmux/internal/worker/channel"
	workerconfig "github.com/leapmux/leapmux/internal/worker/config"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/hub"
	"github.com/leapmux/leapmux/internal/worker/hub/fakehubtest"
	"github.com/leapmux/leapmux/util/drain"

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
	sync, err := BuildTabSync(t.Context(), queries, userid.MustNew("user-1"))
	require.NoError(t, err)

	require.NotNil(t, sync)
	assert.Empty(t, sync.GetTabs())
}

func TestBuildTabSync_AgentsFromDB(t *testing.T) {
	queries := setupTestDB(t)
	ctx := context.Background()

	// Insert agents directly into the DB (simulating persisted state).
	err := queries.CreateAgent(ctx, db.CreateAgentParams{AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		ID:         "agent-1",
		WorkingDir: "/tmp",
	})
	require.NoError(t, err)

	err = queries.CreateAgent(ctx, db.CreateAgentParams{AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		ID:         "agent-2",
		WorkingDir: "/tmp",
	})
	require.NoError(t, err)

	// Close agent-2 to verify closed agents are still included.
	_, err = queries.CloseAgent(ctx, "agent-2")
	require.NoError(t, err)

	sync, err := BuildTabSync(t.Context(), queries, userid.MustNew("user-1"))
	require.NoError(t, err)

	require.NotNil(t, sync)
	assert.Len(t, sync.GetTabs(), 2)

	// Collect tabs into a map for order-independent assertion.
	tabMap := make(map[string]*leapmuxv1.TabIdent)
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

	sync, err := BuildTabSync(t.Context(), queries, userid.MustNew("user-1"))
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
	err := queries.CreateAgent(ctx, db.CreateAgentParams{AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
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

	sync, err := BuildTabSync(t.Context(), queries, userid.MustNew("user-1"))
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
// client-minted id -- worker_tab_payloads is PK'd (user_id, tab_id) precisely because
// those ids are unique only within a user -- would suppress a tombstone the real
// owner's tab was due, resurrecting a file tab they closed elsewhere.
func TestBuildTabSync_ReportsFileTabsScopedToTheOwner(t *testing.T) {
	queries := setupTestDB(t)
	ctx := context.Background()

	require.NoError(t, queries.UpsertWorkerTabPayload(ctx, db.UpsertWorkerTabPayloadParams{
		UserID:     "user-1",
		TabID:      "file-1",
		TabType:    int64(leapmuxv1.TabType_TAB_TYPE_FILE),
		Payload:    mustMarshalFilePayload("/tmp/a.go", ""),
		WorkingDir: "",
	}))
	// A stale second owner's row, which must NOT be attributed to user-1.
	require.NoError(t, queries.UpsertWorkerTabPayload(ctx, db.UpsertWorkerTabPayloadParams{
		UserID:     "user-2",
		TabID:      "file-2",
		TabType:    int64(leapmuxv1.TabType_TAB_TYPE_FILE),
		Payload:    mustMarshalFilePayload("/tmp/b.go", ""),
		WorkingDir: "",
	}))

	sync, err := BuildTabSync(t.Context(), queries, userid.MustNew("user-1"))
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
	_, err := BuildTabSync(t.Context(), queries, userid.UserID{})
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

	require.NoError(t, queries.CreateAgent(ctx, db.CreateAgentParams{AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		ID:         "agent-1",
		WorkingDir: "/tmp",
	}))
	// Drop the terminals table so the middle read fails after the agent read
	// has already succeeded -- the exact shape that produced a truncated,
	// agent-only report.
	_, execErr := sqlDB.Exec(`DROP TABLE terminals`)
	require.NoError(t, execErr)

	sync, err := BuildTabSync(t.Context(), queries, userid.MustNew("user-1"))

	require.Error(t, err, "a failed read must be reported, not swallowed")
	assert.Nil(t, sync, "no partial report may escape")
}

// wireForTest assembles a Wiring against an in-memory DB and an
// unconnected hub client, which is all Wire needs: it registers handlers
// and starts background loops but never dials.
func wireForTest(t *testing.T, mode leapmuxv1.EncryptionMode) (*Wiring, *hub.Client) {
	t.Helper()
	return wireForTestWith(t, mode, func(*Params) {})
}

// wireForTestWith is wireForTest with a hook that adjusts the Params before
// Wire runs, for the tests that care about one specific field.
func wireForTestWith(t *testing.T, mode leapmuxv1.EncryptionMode, adjust func(*Params)) (*Wiring, *hub.Client) {
	t.Helper()
	w, client, _ := wireForTestWithKeypair(t, mode, adjust)
	return w, client
}

// wireForTestWithKeypair is wireForTestWith plus the keypair Wire was given, for
// the tests that compare what the client advertises against its source.
func wireForTestWithKeypair(
	t *testing.T, mode leapmuxv1.EncryptionMode, adjust func(*Params),
) (*Wiring, *hub.Client, *noiseutil.CompositeKeypair) {
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

	params := Params{
		Ctx:            ctx,
		Client:         client,
		DB:             sqlDB,
		CompositeKey:   key,
		EncryptionMode: mode,
		WorkerID:       "worker-1",
		Name:           "test",
		HomeDir:        t.TempDir(),
		DataDir:        t.TempDir(),
	}
	adjust(&params)
	w := Wire(params)
	t.Cleanup(w.Shutdown)
	return w, client, key
}

// TestWire_AdvertisesPostQuantumKeysInEveryMode is the regression for a
// silent identity change.
//
// The keys describe the worker, not the session's cipher: the handshake
// picks its mode from EncryptionMode alone. The Hub refuses a hello with an
// incomplete key set, because it pins the complete set the worker registered
// with, so withholding the post-quantum keys in classic mode would stop the
// worker from connecting at all.
func TestWire_AdvertisesPostQuantumKeysInEveryMode(t *testing.T) {
	for _, mode := range []leapmuxv1.EncryptionMode{
		leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC,
		leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM,
	} {
		t.Run(mode.String(), func(t *testing.T) {
			_, client, key := wireForTestWithKeypair(t, mode, func(*Params) {})
			slhdsa, err := key.SlhdsaPublicKeyBytes()
			require.NoError(t, err)

			require.NotNil(t, client.PublicKeys)
			assert.Equal(t, key.X25519Public, client.PublicKeys.GetX25519())
			assert.Equal(t, key.MlkemPublicKeyBytes(), client.PublicKeys.GetMlkem())
			assert.Equal(t, slhdsa, client.PublicKeys.GetSlhdsa())
			assert.Len(t, client.PublicKeys.GetX25519(), contracts.X25519PublicKeySize)
			assert.Len(t, client.PublicKeys.GetMlkem(), contracts.MlkemPublicKeySize)
			assert.Len(t, client.PublicKeys.GetSlhdsa(), contracts.SlhdsaPublicKeySize)
			assert.NotEmpty(t, client.PublicKeys.GetMlkem(),
				"ML-KEM key must be advertised: the Hub refuses a hello with an incomplete key set")
			assert.NotEmpty(t, client.PublicKeys.GetSlhdsa(),
				"SLH-DSA key must be advertised: the Hub refuses a hello with an incomplete key set")
			assert.Equal(t, mode, client.EncryptionMode,
				"the mode, not key presence, is what selects the handshake")
		})
	}
}

// A keypair Wire cannot describe as a complete key set is a wiring mistake, and
// it fails before PrepareAgentDirs or the control IPC factory start anything.
func TestWire_PanicsOnAKeypairWithoutKeys(t *testing.T) {
	assert.Panics(t, func() {
		wireForTestWith(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM, func(p *Params) {
			p.CompositeKey = &noiseutil.CompositeKeypair{}
		})
	})
}

// TestWire_PerformsEveryStepAWorkerReliesOn pins the wiring steps whose
// omission in one of two old entry points is why this package exists. Each was
// a shipped defect: a missing ControlIPC left `leapmux control` with no
// socket, and an unbound cleanup WaitGroup let Shutdown return while a
// close handler was still writing.
func TestWire_PerformsEveryStepAWorkerReliesOn(t *testing.T) {
	w, client := wireForTest(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM)

	require.NotNil(t, w.Service)
	assert.NotNil(t, w.Service.ControlIPC,
		"ControlIPC must be wired; the CLI once shipped without it")
	assert.NotNil(t, client.OnWorkerIdentity,
		"the Hub delivers the owner on connect and is the authority")
	assert.NotNil(t, client.TabSyncProvider, "tab sync must be published")

	// Construction must not have left the always-non-nil fields nil.
	assert.NotNil(t, w.Service.PrivateEvents)
	assert.NotNil(t, w.Service.TabPayloads)
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
	w, _ := wireForTest(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM)

	handler := w.Service.Agents.ExitHandlerForTest()
	require.NotNil(t, handler, "Wire constructs the manager with no exit handler and must install one")

	// A non-nil handler proves only that Wire installed SOME handler. Run it: only
	// the service-aware one gives a running background-task row a final status.
	bg := context.Background()
	require.NoError(t, w.Service.Queries.CreateAgent(bg, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	sink, sinkErr := w.Service.Output.NewRootSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	require.NoError(t, sinkErr)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "sub", Kind: bgtask.KindSubagent, Title: "review the diff", Status: bgtask.StatusRunning,
	}))

	handler(agent.ProcessExit{AgentID: "agent-1", Err: errors.New("boom"), Stopped: false, Output: sink})

	rows, err := w.Service.Queries.ListAgentBackgroundTasksNewestFirst(bg, db.ListAgentBackgroundTasksNewestFirstParams{
		OwnerAgentID: "agent-1", Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, bgtask.StatusInterrupted, bgtask.Status(rows[0].Status),
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
	require.NoError(t, client.FlushSends(hub.ShutdownFlushTimeout))

	// Shutdown is the single seam; calling it must not require the caller to
	// remember the flush. Idempotent, because every exit path converges here.
	w.Shutdown()
	w.Shutdown()
}

func TestWire_PropagatesMaxMessageSize(t *testing.T) {
	const configured = 2 << 20
	var workerPublicKey []byte
	w, _ := wireForTestWith(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC, func(p *Params) {
		p.MaxMessageSize = configured
		workerPublicKey = p.CompositeKey.X25519Public
	})
	t.Cleanup(func() { agent.ConfigureMaxMessageSize(0) })

	assert.Equal(t, configured, w.Service.MaxMessageSize,
		"Wire must carry MaxMessageSize into the service producer ceiling")

	_, msg1, err := noiseutil.ClassicalInitiatorHandshake1(workerPublicKey)
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
	require.NoError(t, queries.CreateAgent(ctx, db.CreateAgentParams{AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		ID:         "root-1",
		WorkingDir: "/tmp",
	}))
	require.NoError(t, queries.CreateChildAgent(ctx, db.CreateChildAgentParams{
		ID:            "child-1",
		ParentAgentID: sql.NullString{String: "root-1", Valid: true},
		SpawnSpanID:   "span-1",
		WorkingDir:    "/tmp",
		Title:         "child",
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))

	provider := liveTabForMint(queries)
	id, ok := provider()
	require.True(t, ok)
	assert.Equal(t, "root-1", id, "mint must target a root agent, never a child")
}

// TestWire_AppliesTheConfiguredStartupConcurrency pins the hop from the loaded
// config to the ONE place that holds it: the agent manager's background permit
// pool. The resume sweep reads it back from there rather than keeping a second
// copy, so this one assertion covers both consumers.
func TestWire_AppliesTheConfiguredStartupConcurrency(t *testing.T) {
	w, _ := wireForTestWith(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM,
		func(p *Params) { p.AgentStartupConcurrency = 3 })

	assert.Equal(t, 3, w.Service.Agents.StartupConcurrency(),
		"the manager holds the configured cap, and the resume sweep sizes its fan-out from it")
}

// TestWire_UnsetStartupConcurrencyResolvesTheDefault pins the "0 means auto"
// convention at the boundary that worker.Run actually crosses: it passes the
// loaded value through unchanged, and an unset worker.yaml key arrives as 0.
func TestWire_UnsetStartupConcurrencyResolvesTheDefault(t *testing.T) {
	w, _ := wireForTest(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM)

	assert.Equal(t, workerconfig.ResolveStartupConcurrency(0),
		w.Service.Agents.StartupConcurrency())
}

// TestWiring_ShutdownStopsTheResumeSweep pins that the resume sweep is covered
// by the single stopBackgroundLoops slot the reconciler already used.
//
// Both loops share that one field, so wiring a second one is exactly the shape
// in which the first gets overwritten and quietly stops being stopped: a sweep
// left running past Shutdown keeps spawning agent processes and registering
// remote-IPC cleanups while the caller closes the database underneath it.
//
// It asserts on the RESUMER, not merely on Shutdown returning. A stopper that
// dropped the resumer returns just as promptly, so a timing-only check passes
// against the exact regression this test is named for.
func TestWiring_ShutdownStopsTheResumeSweep(t *testing.T) {
	w, _ := wireForTest(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM)

	require.NotNil(t, w.Resumer, "Wire must hand the resume sweep back so Shutdown's reach is observable")
	require.False(t, w.Resumer.StoppedForTest(), "the resumer must be live before Shutdown")

	// Shutdown is idempotent and the fixture's cleanup runs it again; this also
	// asserts that it RETURNS -- a stopper that waited on a loop it never started
	// would block here for ever.
	done := make(chan struct{})
	go func() { defer close(done); w.Shutdown() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Shutdown blocked; the composed background-loop stopper did not return")
	}
	assert.True(t, w.Resumer.StoppedForTest(),
		"the composed stopper must reach the resume sweep, not only the reconciler")
}

// A canceled loop finishes the pass in progress, and the caller closes the database
// that the pass uses. So the stop must wait for that pass, not only cancel it.
func TestStopPeriodicLoops_WaitsForThePassInProgress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		release := make(chan struct{})
		stopped := periodic.Start(ctx, periodic.Schedule{Interval: time.Hour}, func(context.Context) { <-release })
		idle := periodic.Start(ctx, periodic.Schedule{Interval: time.Hour, SkipFirstRun: true}, func(context.Context) {})
		synctest.Wait()

		returned := make(chan struct{})
		go func() {
			defer close(returned)
			stopPeriodicLoops(cancel, []<-chan struct{}{idle, stopped}, nil)
		}()
		synctest.Wait()
		select {
		case <-returned:
			require.FailNow(t, "the stop returned while a pass still ran")
		default:
		}

		close(release)
		synctest.Wait()
		select {
		case <-returned:
		default:
			require.FailNow(t, "the stop must return after the pass returns")
		}
	})
}

// A pass that does not return holds the stop only until the shutdown
// deadline: the stop abandons it then, so the database close starts in time.
func TestStopPeriodicLoops_StopsWaitingAtTheDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const budget = time.Minute
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		release := make(chan struct{})
		defer close(release)
		stopped := periodic.Start(ctx, periodic.Schedule{Interval: time.Hour}, func(context.Context) { <-release })
		synctest.Wait()

		// The real clock reads the bubble's fake time.
		deadline := drain.NewDeadline(quartz.NewReal(), budget)
		deadline.Start()
		returned := make(chan struct{})
		go func() {
			defer close(returned)
			stopPeriodicLoops(cancel, []<-chan struct{}{stopped}, deadline)
		}()
		synctest.Wait()
		select {
		case <-returned:
			require.FailNow(t, "the stop returned before the deadline while a pass still ran")
		default:
		}

		time.Sleep(budget)
		synctest.Wait()
		select {
		case <-returned:
		default:
			require.FailNow(t, "the stop must return at the deadline")
		}
		select {
		case <-stopped:
			require.FailNow(t, "the pass must still run: the stop abandoned it rather than joined it")
		default:
		}
	})
}

// Wire hands its deadline to the service, so Shutdown, the flush and the
// handler join all end at the one deadline that worker.Run starts.
func TestWire_HandsTheShutdownDeadlineToTheService(t *testing.T) {
	deadline := drain.NewDeadline(quartz.NewReal(), time.Minute)
	w, _ := wireForTestWith(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC, func(p *Params) {
		p.ShutdownDeadline = deadline
	})
	assert.Same(t, deadline, w.Service.ShutdownDeadline)
}

// TestWire_WorkerIdentityTriggersAReconcilerPass pins the nudge that keeps the
// resume sweep's retry from taking an hour.
//
// The sweep refuses, without latching, while the worker has no owner. A pass can
// CONVERGE before the owner arrives -- the hub leg needs only the auth token --
// and a converged pass disarms the retry backoff, so the next pass would be the
// hourly tick. Delivering the identity has to wake the reconciler itself.
func TestWire_WorkerIdentityTriggersAReconcilerPass(t *testing.T) {
	w, client := wireForTest(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM)

	require.NotNil(t, client.OnWorkerIdentity,
		"the identity handler must survive the background-loop wiring that wraps it")
	client.OnWorkerIdentity(&leapmuxv1.WorkerIdentity{RegisteredBy: "user-1", RegisteredByUsername: "alice"})

	// The wrapper must still do the delivery it wrapped.
	assert.Eventually(t, func() bool { return w.Service.RegisteredBy().Matches("user-1") },
		5*time.Second, 10*time.Millisecond,
		"the identity nudge dropped the delivery it wraps")
	assert.Equal(t, "alice", w.Service.OwnerUsername(userid.MustNew("user-1")),
		"the delivery carries the owner's username to the service")
}

// The Hub re-delivers the identity on every account change of the owner, such
// as a rename or an elevation, and a re-delivery for the same owner cannot
// unblock the resume sweep. So only a delivery that changes the owner starts a
// reconcile pass; each pass costs a ListOwnedTabsForWorker round trip.
func TestNudgeOnNewOwner_NudgesOnlyWhenTheOwnerChanges(t *testing.T) {
	w, _ := wireForTest(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM)
	var nudges int
	deliver := nudgeOnNewOwner(w.Service, func() { nudges++ })

	assert.True(t, deliver(&leapmuxv1.WorkerIdentity{RegisteredBy: "user-1", RegisteredByUsername: "alice"}))
	assert.Equal(t, 1, nudges, "the first owner unblocks the resume sweep")

	assert.True(t, deliver(&leapmuxv1.WorkerIdentity{RegisteredBy: "user-1", RegisteredByUsername: "alice2"}),
		"a rename is applied")
	assert.Equal(t, "alice2", w.Service.OwnerUsername(userid.MustNew("user-1")), "a rename still reaches the service")
	assert.True(t, deliver(&leapmuxv1.WorkerIdentity{RegisteredBy: "user-1", RegisteredByUsername: "alice2"}),
		"an unchanged identity is applied")
	assert.True(t, deliver(&leapmuxv1.WorkerIdentity{RegisteredByUsername: "nobody"}),
		"a refused empty owner leaves the worker with its owner")
	assert.Equal(t, 1, nudges, "a rename, an unchanged identity and a refused empty owner keep the owner")

	assert.True(t, deliver(&leapmuxv1.WorkerIdentity{RegisteredBy: "user-2", RegisteredByUsername: "bob"}))
	assert.Equal(t, 2, nudges, "a different owner starts a pass")
}

// An identity with no owner is refused, so it changes nothing and starts no
// pass, even on a worker that holds no owner yet. A zero owner matches no
// owner, the zero owner included, so comparing the owner before and after the
// delivery would read the refusal as a change.
func TestNudgeOnNewOwner_SkipsARefusedEmptyOwnerOnAWorkerWithNoOwner(t *testing.T) {
	w, _ := wireForTest(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM)
	var nudges int
	deliver := nudgeOnNewOwner(w.Service, func() { nudges++ })

	assert.False(t, deliver(&leapmuxv1.WorkerIdentity{}), "an empty owner is not applied")
	assert.False(t, deliver(&leapmuxv1.WorkerIdentity{RegisteredByUsername: "nobody"}), "an empty owner is not applied")

	assert.Zero(t, nudges, "a refused identity unblocks nothing")
	assert.True(t, w.Service.RegisteredBy().IsZero(), "a refused identity stores no owner")
}

// A refused identity keeps the owner that the worker holds, and that owner keeps the
// owner gate open. So the handler reports an owner. Otherwise the identity watchdog
// would end the connection after every greeting, and each reconnect would drop every
// browser channel of this worker.
func TestNudgeOnNewOwner_ReportsTheOwnerThatARefusedIdentityLeftInPlace(t *testing.T) {
	refused := &leapmuxv1.WorkerIdentity{RegisteredByUsername: "nobody"}

	t.Run("a seeded owner", func(t *testing.T) {
		w, _ := wireForTestWith(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM, func(p *Params) {
			p.SeedRegisteredBy = "user-1"
		})
		var nudges int
		deliver := nudgeOnNewOwner(w.Service, func() { nudges++ })

		assert.True(t, deliver(refused))
		assert.Zero(t, nudges, "a refused identity unblocks nothing")
		assert.True(t, w.Service.RegisteredBy().Matches("user-1"), "the worker keeps its seeded owner")
	})

	t.Run("an owner from an earlier delivery", func(t *testing.T) {
		w, _ := wireForTest(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM)
		var nudges int
		deliver := nudgeOnNewOwner(w.Service, func() { nudges++ })
		require.True(t, deliver(&leapmuxv1.WorkerIdentity{RegisteredBy: "user-1", RegisteredByUsername: "alice"}))

		assert.True(t, deliver(refused))
		assert.Equal(t, 1, nudges, "a refused identity starts no pass of its own")
		assert.True(t, w.Service.RegisteredBy().Matches("user-1"), "the worker keeps the delivered owner")
		assert.Equal(t, "alice", w.Service.OwnerUsername(userid.MustNew("user-1")),
			"the refused frame's username belongs to no owner")
	})
}

// A worker seeded with its owner already passes the sweep's owner check, so the
// first greeting, which carries the same owner, starts no pass of its own. The
// connect-time WorkerTabInventory reply still starts one on every connect.
func TestNudgeOnNewOwner_SkipsAGreetingForTheSeededOwner(t *testing.T) {
	w, _ := wireForTestWith(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM, func(p *Params) {
		p.SeedRegisteredBy = "user-1"
	})
	var nudges int
	deliver := nudgeOnNewOwner(w.Service, func() { nudges++ })

	assert.True(t, deliver(&leapmuxv1.WorkerIdentity{RegisteredBy: "user-1", RegisteredByUsername: "alice"}),
		"the greeting is applied, so the identity watchdog stops")

	assert.Zero(t, nudges)
	assert.Equal(t, "alice", w.Service.OwnerUsername(userid.MustNew("user-1")))
}

// The control IPC factory answers Whoami's username from the live service, so
// each spawned process reports the owner the Hub delivered last, and only to
// the owner.
func TestWire_ControlIPCReadsTheOwnerUsernameFromTheService(t *testing.T) {
	w, client := wireForTest(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM)

	factory, ok := w.Service.ControlIPC.(*controlipc.Factory)
	require.True(t, ok, "Wire installs the control IPC factory, got %T", w.Service.ControlIPC)
	require.NotNil(t, factory.OwnerUsername, "without the lookup, Whoami reports no username")
	owner := userid.MustNew("user-1")
	assert.Empty(t, factory.OwnerUsername(owner), "no username before the Hub delivers the identity")

	client.OnWorkerIdentity(&leapmuxv1.WorkerIdentity{RegisteredBy: "user-1", RegisteredByUsername: "alice"})
	assert.Equal(t, "alice", factory.OwnerUsername(owner))
	assert.Empty(t, factory.OwnerUsername(userid.MustNew("user-2")), "another user is never told the owner's name")

	client.OnWorkerIdentity(&leapmuxv1.WorkerIdentity{RegisteredBy: "user-1", RegisteredByUsername: "alice2"})
	assert.Equal(t, "alice2", factory.OwnerUsername(owner), "a re-delivered identity reaches the factory")
}

// C-M5: the worker sweeps the agent directories that ended workers left when it
// starts, before any agent of their provider starts. A crash then leaves no
// secret, and no daemon, behind until that provider starts again.
func TestWire_SweepsTheAgentDirectoriesOfEndedWorkers(t *testing.T) {
	// The default bases take $XDG_RUNTIME_DIR and the system's temporary
	// directory. Both point into the test, so the sweep finds nothing of the
	// user's own. t.Setenv keeps the test serial.
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	parent := "leapmux-agents"
	if runtime.GOOS == "windows" {
		temp := t.TempDir()
		t.Setenv("TMP", temp)
		t.Setenv("TEMP", temp)
	} else {
		t.Setenv("TMPDIR", t.TempDir())
		parent += "-" + strconv.Itoa(os.Getuid())
	}
	dataDir := t.TempDir()
	stale := filepath.Join(dataDir, "run", parent, "amp-1-1")
	require.NoError(t, os.MkdirAll(stale, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(stale, ".owner.lock"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(stale, "helper.json"), []byte(`{"secret":"s"}`), 0o600))

	wireForTestWith(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM, func(p *Params) { p.DataDir = dataDir })
	assert.Eventually(t, func() bool {
		_, err := os.Stat(stale)
		return errors.Is(err, fs.ErrNotExist)
	}, 30*time.Second, 10*time.Millisecond, "the sweep removed the directory, although no agent started")
}

// --- shutdown admission and the handler join ---

// wiringStubWriter is the smallest ResponseWriter the admission tests need.
type wiringStubWriter struct{}

func (*wiringStubWriter) SendResponse(*leapmuxv1.InnerRpcResponse) error { return nil }
func (*wiringStubWriter) SendError(int32, string) error                  { return nil }
func (*wiringStubWriter) SendStream(*leapmuxv1.InnerStreamMessage) error { return nil }
func (*wiringStubWriter) ChannelID() string                              { return "" }
func (*wiringStubWriter) MaxPayloadBudget() int                          { return 0 }
func (*wiringStubWriter) BindStream(channel.StreamController) (func(), bool) {
	return func() {}, false
}

// A dispatcher admission that is still open after Shutdown returned would let
// a handler write into the closed database.
func TestWiringShutdownClosesDispatchAdmission(t *testing.T) {
	w, _ := wireForTest(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM)

	w.Shutdown()

	ran := false
	w.dispatcher.Handle("leapmux.v1.WorkerAgentService/Mutate", channel.Registration{}, func(context.Context, channel.Caller, *leapmuxv1.InnerRpcRequest, channel.ResponseWriter) {
		ran = true
	})
	w.dispatcher.DispatchWith(context.Background(), channel.LocalAgentCaller(userid.MustNew("u")),
		&leapmuxv1.InnerRpcRequest{Method: "leapmux.v1.WorkerAgentService/Mutate"}, &wiringStubWriter{})

	assert.False(t, ran, "a handler dispatched after Shutdown must not run")
}

// WaitHandlers joins an untracked handler: the dispatch the old Cleanup.Wait
// never covered is exactly the one this join exists for.
func TestWiringWaitHandlersJoinsAnUntrackedHandler(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, _ := wireForTest(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM)
		release := make(chan struct{})
		entered := make(chan struct{})
		w.dispatcher.Handle("plain", channel.Registration{}, func(context.Context, channel.Caller, *leapmuxv1.InnerRpcRequest, channel.ResponseWriter) {
			close(entered)
			<-release
		})

		w.dispatcher.DispatchAsync(context.Background(), channel.LocalAgentCaller(userid.MustNew("u")),
			&leapmuxv1.InnerRpcRequest{Method: "plain"}, &wiringStubWriter{})

		waited := make(chan struct{}, 1)
		go func() {
			w.WaitHandlers()
			close(waited)
		}()
		<-entered
		synctest.Wait()
		select {
		case <-waited:
			t.Fatal("WaitHandlers returned while an untracked handler was parked")
		default:
		}

		close(release)
		select {
		case <-waited:
		case <-time.After(time.Second):
			t.Fatal("WaitHandlers did not return after the release")
		}
	})
}

// A handler that never returns is abandoned at the handler budget, with one
// Warn that reports it.
func TestWiringWaitHandlersWarnsAtTheDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, _ := wireForTest(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM)
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })
		entered := make(chan struct{})
		w.dispatcher.Handle("stuck", channel.Registration{}, func(context.Context, channel.Caller, *leapmuxv1.InnerRpcRequest, channel.ResponseWriter) {
			close(entered)
			<-release
		})
		w.dispatcher.DispatchAsync(context.Background(), channel.LocalAgentCaller(userid.MustNew("u")),
			&leapmuxv1.InnerRpcRequest{Method: "stuck"}, &wiringStubWriter{})
		<-entered

		previousLogger := slog.Default()
		var warns atomic.Int32
		slog.SetDefault(slog.New(warnCountingHandler{next: slog.NewTextHandler(io.Discard, nil), warns: &warns}))
		t.Cleanup(func() { slog.SetDefault(previousLogger) })

		done := make(chan struct{})
		go func() {
			w.WaitHandlers()
			close(done)
		}()

		// The budget is fake time in the bubble, so the deadline arrives
		// without spending real seconds.
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatal("WaitHandlers did not return at the deadline")
		}
		assert.Equal(t, int32(1), warns.Load(), "the expiry must surface as exactly one Warn")
	})
}

// warnCountingHandler counts Warn records, so a test can assert the expiry
// logged once.
type warnCountingHandler struct {
	next  slog.Handler
	warns *atomic.Int32
}

func (h warnCountingHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelWarn
}
func (h warnCountingHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Level == slog.LevelWarn {
		h.warns.Add(1)
	}
	return nil
}
func (h warnCountingHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return h }
func (h warnCountingHandler) WithGroup(name string) slog.Handler       { return h }

func TestBuildTabSync_UsesCallerCancellation(t *testing.T) {
	queries := setupTestDB(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	inventory, err := BuildTabSync(ctx, queries, userid.MustNew("user-1"))
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, inventory)
}

func TestWire_FirstIdentityBuildsInventoryWithoutSeed(t *testing.T) {
	reports := make(chan *leapmuxv1.WorkerTabInventory, 1)
	url := fakehubtest.Start(t, func(ctx context.Context, stream *fakehubtest.Stream) {
		if _, err := stream.Receive(ctx); !assert.NoError(t, err) {
			return
		}
		if err := stream.Greet(ctx, "user-1"); !assert.NoError(t, err) {
			return
		}
		report, err := stream.Receive(ctx)
		if !assert.NoError(t, err) {
			return
		}
		reports <- report.GetWorkerTabInventory()
		_ = stream.ReadUntilClosed(ctx)
	})
	var client *hub.Client
	wiring, _ := wireForTestWith(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC, func(p *Params) {
		client = newTestHubClient(t, url)
		p.Client = client
	})
	require.True(t, wiring.Service.RegisteredBy().IsZero())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- client.Connect(ctx, "token") }()
	select {
	case report := <-reports:
		require.NotNil(t, report)
		assert.True(t, report.GetComplete())
	case <-time.After(30 * time.Second):
		t.Fatal("first identity produced no inventory")
	}
	assert.Equal(t, "user-1", wiring.Service.RegisteredBy().String())
	wiring.Shutdown()
	cancel()
	require.ErrorIs(t, <-result, context.Canceled)
}

func TestWire_InventoryTransactionAcquisitionIsCancellable(t *testing.T) {
	w, client := wireForTest(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC)
	require.True(t, client.OnWorkerIdentity(&leapmuxv1.WorkerIdentity{RegisteredBy: "user-1"}))
	w.Service.DB.SetMaxOpenConns(1)
	connection, err := w.Service.DB.Conn(t.Context())
	require.NoError(t, err)
	defer func() { _ = connection.Close() }()
	waits := w.Service.DB.Stats().WaitCount
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	type result struct {
		inventory *leapmuxv1.WorkerTabInventory
		err       error
	}
	done := make(chan result, 1)
	go func() { inventory, err := client.TabSyncProvider(ctx); done <- result{inventory, err} }()
	testutil.AssertEventually(t, func() bool { return w.Service.DB.Stats().WaitCount > waits })
	cancel()
	select {
	case got := <-done:
		require.ErrorIs(t, got.err, context.Canceled)
		assert.Nil(t, got.inventory)
	case <-time.After(30 * time.Second):
		t.Fatal("inventory transaction acquisition ignored cancellation")
	}
}

func TestWire_ShutdownJoinsInventoryBeforeFlush(t *testing.T) {
	url := fakehubtest.Start(t, func(ctx context.Context, stream *fakehubtest.Stream) {
		if _, err := stream.Receive(ctx); err != nil {
			return
		}
		if err := stream.Greet(ctx, "user-1"); err != nil {
			return
		}
		_ = stream.ReadUntilClosed(ctx)
	})
	var client *hub.Client
	w, _ := wireForTestWith(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC, func(p *Params) { client = newTestHubClient(t, url); p.Client = client })
	t.Cleanup(client.Stop)
	started, joined := make(chan struct{}), make(chan struct{})
	client.TabSyncProvider = func(ctx context.Context) (*leapmuxv1.WorkerTabInventory, error) {
		close(started)
		<-ctx.Done()
		close(joined)
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- client.Connect(ctx, "token") }()
	select {
	case <-started:
	case <-time.After(30 * time.Second):
		t.Fatal("inventory producer did not start")
	}
	w.Shutdown()
	select {
	case <-joined:
	default:
		t.Error("shutdown returned before the inventory producer stopped")
	}
	cancel()
	require.ErrorIs(t, <-result, context.Canceled)
}
