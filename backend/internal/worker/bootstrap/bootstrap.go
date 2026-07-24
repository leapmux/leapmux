// Package bootstrap wires a worker's service, channel manager and
// dispatcher into a running whole.
//
// It exists because the worker has two entry points -- `worker.Run` (the
// solo binary and the desktop sidecar) and `runWorker` (the `leapmux
// worker` CLI) -- whose wiring sequences drifted apart while both were
// maintained by hand. The drift was not theoretical: the CLI shipped
// without `svc.RemoteIPC`, so `leapmux remote` inside an agent it started
// found no socket even though the docs promise one, and `worker.Run`
// shipped without the dispatcher's cleanup binding, so its Shutdown
// waited on an always-zero WaitGroup. Both are the same defect -- a step
// one entry point performs and the other silently omits -- and neither is
// catchable by a compiler.
//
// So the sequence lives here once. What remains in each entry point is
// only what genuinely differs: how it obtains its keypair and token, and
// how it decides to stop.
package bootstrap

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/crossworker"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/hub"
	"github.com/leapmux/leapmux/internal/worker/remoteipc"
	"github.com/leapmux/leapmux/internal/worker/service"
	"github.com/leapmux/leapmux/internal/worker/wakelock"
)

// Params is everything Wire needs that an entry point already knows.
//
// Every field is supplied by both entry points except SeedRegisteredBy,
// which only the in-process launchers can read from their own DB.
type Params struct {
	// Ctx bounds every background loop Wire starts.
	Ctx    context.Context
	Client *hub.Client
	DB     *sql.DB

	CompositeKey         *noiseutil.CompositeKeypair
	EncryptionMode       leapmuxv1.EncryptionMode
	MaxIncompleteChunked int

	WorkerID string
	Name     string
	HomeDir  string
	DataDir  string

	// HubURL and AuthToken are used for cross-worker delegation, not for
	// the worker's own Hub connection (the Client already holds that).
	HubURL    string
	AuthToken string

	// SeedRegisteredBy is a DB-sourced guess at the worker owner. The Hub
	// overrides it on every connect and is the authority; leaving it empty
	// is correct for any entry point that has no local copy.
	SeedRegisteredBy string

	AgentStartupTimeout time.Duration
	APITimeout          time.Duration
	UseLoginShell       bool
	WakeLock            *wakelock.ActivityTracker
}

// Wiring is the assembled worker. Callers own the lifecycle: nothing here
// is started or stopped by Wire beyond the background loops it documents.
//
// Only the Service is exposed. The channel manager and dispatcher are
// fully wired into the Client before Wire returns, so handing them back
// would offer callers a handle on objects whose safe write window has
// already closed -- and neither entry point ever wanted one.
type Wiring struct {
	Service *service.Service
}

// Wire assembles the worker and starts its background loops. The caller
// is responsible for calling Wiring.Service.Shutdown before closing the
// database, and for connecting the Client.
//
// Ordering here is load-bearing, and each step says why:
//
//	channel manager -> service -> dispatcher -> remote IPC -> register
//	  -> publish the manager to the connect loop
//
// Nothing the connect loop can reach is published until every handler is
// registered behind it.
func Wire(p Params) *Wiring {
	// Built first because the service needs it for workspace access
	// lookups. Its close callback is attached below, once there is a
	// service for it to reach.
	channelMgr := channel.NewManager(
		p.CompositeKey, p.EncryptionMode, p.Client.Send, p.Client.TrySend, p.MaxIncompleteChunked,
	)

	svc := service.New(service.Config{
		Channels:            channelMgr,
		Send:                p.Client.Send,
		DB:                  p.DB,
		Agents:              p.Client.AgentManager(),
		Terminals:           p.Client.TerminalManager(),
		HomeDir:             p.HomeDir,
		DataDir:             p.DataDir,
		WorkerID:            p.WorkerID,
		Name:                p.Name,
		SeedRegisteredBy:    p.SeedRegisteredBy,
		AgentStartupTimeout: p.AgentStartupTimeout,
		APITimeout:          p.APITimeout,
		UseLoginShell:       p.UseLoginShell,
		WakeLock:            p.WakeLock,
	})
	svc.RestoreState()

	// Retire a channel's event subscriptions when it closes. Set now
	// rather than passed to NewManager because it needs the service the
	// manager itself is a dependency of; SetDispatcher below has the same
	// shape for the same reason.
	channelMgr.SetOnChannelClose(func(channelID string) {
		svc.Watchers.UnwatchAll(channelID)
	})

	// Drop pending control_requests on every subprocess exit (graceful
	// stop, crash, worker tear-down) so request_ids bound to the exited
	// subprocess don't reappear stale on resume. This fires on a
	// RELAUNCH's old-process stop too, so it must NOT run the full
	// ClearAgentRuntimeState: that clears the in-memory notification
	// thread (and to-do/span trackers), which must survive a relaunch so
	// two settings-change notifications bracketing a model/effort switch
	// stay in one thread and consolidate. Permanent teardown does the full
	// cleanup via its own ClearAgentRuntimeState call.
	p.Client.AgentManager().SetOnExit(func(agentID string, _ int, _ error) {
		svc.Output.ClearPendingControlRequests(agentID)
	})

	dispatcher := channel.NewDispatcher()
	svc.RemoteIPC = newRemoteIPCFactory(p, svc, dispatcher)

	// Binds svc.Cleanup as the tracked-dispatch drain as well as
	// registering the handlers -- see service.RegisterAll.
	service.RegisterAll(dispatcher, svc)
	channelMgr.SetDispatcher(dispatcher)

	// Only now is the manager reachable from the connect loop.
	p.Client.SetChannelMgr(channelMgr)
	p.Client.EncryptionMode = p.EncryptionMode
	p.Client.PublicKey = p.CompositeKey.X25519Public
	// All three keys go out regardless of mode, because they are the
	// worker's IDENTITY, not this session's cipher choice. What selects
	// the handshake is EncryptionMode above, read on its own by
	// newInitiatorHandshaker (backend/tunnel/channel.go), so a classic
	// worker advertising them still gets the classical handshake.
	//
	// Gating them on mode -- which the CLI did, and which unifying the
	// two entry points spread to solo/desktop -- makes a classic
	// heartbeat overwrite the stored PQ keys with empty blobs
	// (UpdatePublicKey writes all three columns unconditionally). Every
	// client that had pinned that worker then fails TOFU verification in
	// pinStore.Verify and cannot open a channel until the pin is cleared
	// by hand. Registration already sends them unconditionally, so
	// gating here also made the two disagree.
	p.Client.MlkemPublicKey = p.CompositeKey.MlkemPublicKeyBytes()
	slhdsaPub, _ := p.CompositeKey.SlhdsaPublicKeyBytes()
	p.Client.SlhdsaPublicKey = slhdsaPub

	// The Hub owns workers.registered_by and re-delivers it on every
	// connect, so the worker never caches it. It arrives before any
	// ChannelOpen on the same stream, hence before any handler the owner
	// gates can run. UpdateRegisteredBy is the service's own method rather
	// than a closure over SetRegisteredBy, so the drift warning and the
	// empty-owner refusal are shared by both entry points.
	p.Client.OnWorkerIdentity = svc.UpdateRegisteredBy

	startBackgroundLoops(p, svc)

	return &Wiring{Service: svc}
}

// newRemoteIPCFactory builds the per-agent local-IPC factory backing the
// `leapmux remote` CLI.
//
// Cross-worker calls use TOFU pin storage in the worker data dir;
// failures there are non-fatal -- the worker still serves its own agents
// over the existing E2EE channel, so a missing pin store degrades sibling
// dispatch rather than the whole feature.
func newRemoteIPCFactory(p Params, svc *service.Service, dispatcher *channel.Dispatcher) *remoteipc.Factory {
	pins, pinErr := crossworker.NewPinStore(p.DataDir)
	if pinErr != nil {
		slog.Warn("cross-worker pin store unavailable; sibling-worker calls disabled", "error", pinErr)
	}

	var cwClient *crossworker.Client
	var delegation *crossworker.DelegationStore
	if pins != nil {
		delegation = crossworker.NewDelegationStore(p.HubURL, p.AuthToken, p.WorkerID)
		// Defense-in-depth: a periodic sweep drops cached delegation rows
		// whose access token has expired AND whose refcount fell to zero
		// through an abnormal Release path. The healthy lifecycle
		// (Acquire -> GetBearer -> Release) already keeps the cache
		// bounded; this catches orphans.
		go delegation.RunJanitor(p.Ctx, time.Hour)
		cwClient = crossworker.New(p.Ctx, p.HubURL, pins, delegation)
	}

	var hubStreams remoteipc.HubStreamer
	var hubBridge remoteipc.HubBridge
	if delegation != nil {
		hubStreams = remoteipc.NewHubWorkspaceStreamer(p.HubURL, delegation)
		// HubBridge mirrors HubStreamer for unary hub-bound RPCs
		// (workspace/tab/tile/layout). Wired with the same delegation
		// store so streaming and unary share a single (user, workspace) ->
		// bearer cache and one revoke path.
		hubBridge = remoteipc.NewHubWorkspaceBridge(p.HubURL, delegation)
	}

	return &remoteipc.Factory{
		WorkerID:    p.WorkerID,
		Dispatcher:  dispatcher,
		CrossWorker: cwClient,
		HubBridge:   hubBridge,
		HubStreams:  hubStreams,
		Authorizers: svc,
		Delegation:  delegation,
	}
}

// startBackgroundLoops starts every periodic task the worker owns. All of
// them stop with p.Ctx.
func startBackgroundLoops(p Params, svc *service.Service) {
	queries := db.New(p.DB)

	// Provide workspace tab sync data on connect.
	p.Client.TabSyncProvider = func() *leapmuxv1.WorkspaceTabsSync {
		return BuildTabSync(queries)
	}

	// Periodic orphan reconciler: walks worker-local file-tab rows against
	// the hub's CRDT-derived workspace_tab_owned view and drops /
	// relocates rows the CRDT no longer agrees with. Runs once at startup
	// and every hour after; cancelled on ctx done.
	reconciler := service.NewOrphanReconciler(
		queries,
		svc.FileTabPaths,
		func(rctx context.Context) ([]*leapmuxv1.OwnedTab, error) {
			return p.Client.ListOwnedTabsForWorker(rctx)
		},
		service.OrphanReconcilerOptions{
			// Stop the in-memory exec.Cmd / PTY alongside the DB closed_at
			// write. Without these, an orphan reconcile only stops future
			// respawns; the live subprocess keeps running until the worker
			// itself exits.
			Agents:    svc.Agents,
			Terminals: svc.Terminals,
			// Reclaim worktrees whose tab links are all startup-race
			// strands (no live tab references them). Backstops the startup
			// link guards so a close that raced startup can't leak the
			// worktree dir.
			ReapWorktree: svc.ReapOrphanWorktree,
		},
	)
	go reconciler.Run(p.Ctx)

	// The Hub's connect-time WorkspaceTabsSync reply only signals that the
	// hub has finished its side of the reconciliation; trigger the
	// worker-side reconciler so this worker converges on every reconnect
	// (not just on the hourly tick).
	p.Client.OnTabSyncResponse = func(*leapmuxv1.WorkspaceTabsSyncResponse) {
		reconciler.Trigger()
	}

	// Periodically reclaim in-memory agent tracker state orphaned by a
	// closed/deleted agent that never routed through a cleanup path (the
	// per-exit handler keeps the state for a possible relaunch).
	svc.StartOrphanSweepLoop(p.Ctx)

	StartRetentionLoops(p.Ctx, p.DB, p.DataDir)
}

// StartRetentionLoops starts the two data-retention loops. It is exported
// separately because worker.Run runs them even on its degenerate
// no-composite-key path, where there is no service to wire at all.
func StartRetentionLoops(ctx context.Context, sqlDB *sql.DB, dataDir string) {
	// Hard-delete agents and terminals closed for longer than the
	// retention period.
	service.StartCleanupLoop(ctx, db.New(sqlDB))

	// Roll up old plan year directories (`<data_dir>/plans/<YYYY>/`) into
	// per-year zip files.
	service.StartPlanArchiveLoop(ctx, dataDir, db.New(sqlDB))
}

// BuildTabSync constructs a WorkspaceTabsSync message from the worker's
// database: all agents and all terminals.
func BuildTabSync(queries *db.Queries) *leapmuxv1.WorkspaceTabsSync {
	ctx := context.Background()
	var tabs []*leapmuxv1.WorkspaceTabEntry

	// Add agent tabs from DB (includes both active and closed agents).
	agents, err := queries.ListAllAgentIDsAndWorkspaces(ctx)
	if err == nil {
		for _, agent := range agents {
			tabs = append(tabs, &leapmuxv1.WorkspaceTabEntry{
				WorkspaceId: agent.WorkspaceID,
				TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
				TabId:       agent.ID,
			})
		}
	}

	// Add terminal tabs from DB.
	terminals, err := queries.ListAllTerminals(ctx)
	if err == nil {
		for _, t := range terminals {
			tabs = append(tabs, &leapmuxv1.WorkspaceTabEntry{
				WorkspaceId: t.WorkspaceID,
				TabType:     leapmuxv1.TabType_TAB_TYPE_TERMINAL,
				TabId:       t.ID,
			})
		}
	}

	return &leapmuxv1.WorkspaceTabsSync{Tabs: tabs}
}
