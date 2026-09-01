// Package bootstrap wires a worker's service, channel manager and
// dispatcher into a running whole.
//
// It exists because the worker has two entry points -- `worker.Run` (the
// solo binary and the desktop sidecar) and `runWorker` (the `leapmux
// worker` CLI) -- whose wiring sequences drifted apart while both were
// maintained by hand. The drift was not theoretical: the CLI shipped
// without `svc.ControlIPC`, so `leapmux control` inside an agent it started
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
	"errors"
	"fmt"
	"log/slog"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
	"github.com/leapmux/leapmux/internal/worker/crossworker"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/hub"
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
	MaxMessageSize       int

	WorkerID string
	Name     string
	HomeDir  string
	DataDir  string

	// AuthToken is the worker's own credential, used to mint the per-user
	// delegation bearers that cross-worker calls and spawned-agent hub calls
	// present.
	//
	// The hub ADDRESS is deliberately absent: Wire takes it from
	// Client.Endpoint(). A second field here could be pointed somewhere else,
	// or left unset, and the delegation lane would then either fail or open a
	// second connection pool against the same Hub. Reading it off the Client
	// makes both impossible.
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
	// client is retained solely so Shutdown can flush it. Unexported for the
	// same reason the manager and dispatcher are absent: the entry points
	// already hold the Client they passed in, and a second handle here would
	// only invite a caller to reach past Shutdown.
	client *hub.Client
}

// Shutdown drains the worker and then flushes everything that drain wrote to
// the Hub. Both halves, in this order, are the contract -- and it lives here
// rather than in each entry point because half of it is silent when omitted.
//
// Service.Shutdown broadcasts the terminal "[Worker disconnected - Press Enter
// to restart]" notice, but Client.Send only ENQUEUES onto the outbound queue.
// An entry point that tore the stream down straight afterwards raced the
// drain goroutine and lost under load, and the loss is invisible:
// sendq.Writer.Close discards the queue and logs nothing, so the browser's
// terminal simply stops. FlushSends bounds the teardown behind the drain.
func (w *Wiring) Shutdown() {
	w.Service.Shutdown()
	if w.client == nil {
		return
	}
	if err := w.client.FlushSends(); err != nil {
		slog.Warn("shutdown: outbound queue did not drain", "error", err)
	}
}

// Wire assembles the worker and starts its background loops. The caller
// is responsible for calling Wiring.Shutdown before closing the database
// and tearing down the stream, and for connecting the Client.
//
// Ordering here is load-bearing, and each step says why:
//
//	channel manager -> service -> dispatcher -> remote IPC -> register
//	  -> publish the manager to the connect loop
//
// Nothing the connect loop can reach is published until every handler is
// registered behind it.
func Wire(p Params) *Wiring {
	// Built first because the service needs it for channel-session
	// lookups. Its close callback is attached below, once there is a
	// service for it to reach.
	channelMgr := channel.NewManager(
		p.CompositeKey, p.EncryptionMode, p.Client.Send, p.Client.TrySendOrReset, p.MaxMessageSize, p.MaxIncompleteChunked,
	)

	// Raise producer ceilings with the configured payload budget so a
	// raised max_message_size is not receive-side-only.
	agent.ConfigureMaxMessageSize(p.MaxMessageSize)

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
		MaxMessageSize:      p.MaxMessageSize,
	})
	svc.RestoreState()

	// WatchEvents stream teardown owns UnwatchAll via watchSession.OnCancel
	// (stream cancel / channel releaseAll). Do not also wire UnwatchAll here
	// -- two paths to one effect that can disagree.
	// When a channel opens, record its negotiated payload budget on the
	// agent stdout scanners; release on close so the live ceiling rises
	// back to the worker configured budget once no channels remain (and
	// so Hub reject-after-accept teardown undoes an Observe that raced
	// ahead of registration commit).
	channelMgr.SetOnNegotiatedMaxMessageSize(agent.ObserveNegotiatedMaxMessageSize)
	channelMgr.SetOnReleasedMaxMessageSize(agent.ReleaseNegotiatedMaxMessageSize)
	channelMgr.SetOnReleasedAllMaxMessageSizes(agent.ReleaseAllNegotiatedMaxMessageSizes)

	// Drop pending control_requests on every subprocess exit (graceful
	// stop, crash, worker tear-down) so request_ids bound to the exited
	// subprocess don't reappear stale on resume. This fires on a
	// RELAUNCH's old-process stop too, so it must NOT run the full
	// ClearAgentRuntimeState: that clears the in-memory notification
	// thread (and to-do/span trackers), which must survive a relaunch so
	// two settings-change notifications bracketing a model/effort switch
	// stay in one thread and consolidate. Permanent teardown does the full
	// cleanup via its own ClearAgentRuntimeState call.
	p.Client.AgentManager().SetOnExit(func(agentID string, exitCode int, err error, stopped bool) {
		svc.HandleAgentProcessExit(agentID, exitCode, err, stopped)
	})

	dispatcher := channel.NewDispatcher()
	svc.ControlIPC = newControlIPCFactory(p, svc, dispatcher)

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

	return &Wiring{Service: svc, client: p.Client}
}

// newControlIPCFactory builds the per-agent local-IPC factory backing the
// `leapmux control` CLI.
//
// Cross-worker calls use TOFU pin storage in the worker data dir;
// failures there are non-fatal -- the worker still serves its own agents
// over the existing E2EE channel, so a missing pin store degrades sibling
// dispatch rather than the whole feature.
func newControlIPCFactory(p Params, svc *service.Service, dispatcher *channel.Dispatcher) *controlipc.Factory {
	pins, pinErr := crossworker.NewPinStore(p.DataDir)
	if pinErr != nil {
		slog.Warn("cross-worker pin store unavailable; sibling-worker calls disabled", "error", pinErr)
	}

	var cwClient *crossworker.Client
	var delegation *crossworker.DelegationStore
	if pins != nil {
		delegation = crossworker.NewDelegationStore(p.Client.Endpoint(), p.AuthToken, p.WorkerID)
		// The mint's issued_for_tab_id comes from the worker's OWN open-tab tables,
		// which are the authority on what this machine hosts. Wired as a closure so
		// crossworker keeps no dependency on the worker DB -- the same shape
		// TabSyncProvider below uses, and for the same reason.
		delegation.LiveTab = liveTabForMint(db.New(p.DB))
		// Defense-in-depth: a periodic sweep drops cached delegation rows
		// whose access token has expired AND whose refcount fell to zero
		// through an abnormal Release path. The healthy lifecycle
		// (Acquire -> GetBearer -> Release) already keeps the cache
		// bounded; this catches orphans.
		go delegation.RunJanitor(p.Ctx, time.Hour)
		cwClient = crossworker.New(p.Ctx, p.Client.Endpoint(), pins, delegation)
	}

	var hubStreams controlipc.HubStreamer
	var hubBridge controlipc.HubBridge
	if delegation != nil {
		hubStreams = controlipc.NewHubEventStreamer(p.Client.Endpoint(), delegation)
		// HubBridge mirrors HubStreamer for unary hub-bound RPCs
		// (workspace/tab/tile/layout). Wired with the same delegation
		// store so streaming and unary share a single user -> bearer cache
		// and one revoke path.
		hubBridge = controlipc.NewHubUnaryBridge(p.Client.Endpoint(), delegation)
	}

	return &controlipc.Factory{
		WorkerID:    p.WorkerID,
		Dispatcher:  dispatcher,
		CrossWorker: cwClient,
		HubBridge:   hubBridge,
		HubStreams:  hubStreams,
		Streams:     svc,
		Delegation:  delegation,
	}
}

// liveTabForMint answers "a tab this worker currently hosts" for the delegation
// mint, preferring an agent and falling back to a terminal.
//
// Any open tab will do: the hub validates "does this worker own that tab", which
// holds for every tab it hosts, so the mint only needs ONE. Reading it live is what
// removed the shadow set Acquire/Release used to maintain -- a second source of
// truth for the same fact that could outlive the tab it named and 403 every
// subsequent mint.
//
// A read error answers "none": the caller turns that into a mint failure the
// backoff retries, which is the same shape a not-yet-propagated tab already takes.
func liveTabForMint(queries *db.Queries) crossworker.LiveTabProvider {
	return func() (string, int32, bool) {
		ctx := context.Background()
		// A delegation mint targets a tab this worker owns, and worker-owned
		// tabs are ROOT agents only (child agents are excluded from
		// tab_locations via parent_agent_id IS NULL). List roots so a child id
		// can never surface as ids[0] -- the hub 403s a child id ("tab not
		// owned by calling worker") and the mint backoff would loop to a
		// permanent failure.
		if ids, err := queries.ListAllOpenRootAgentIDs(ctx); err == nil && len(ids) > 0 {
			return ids[0], int32(leapmuxv1.TabType_TAB_TYPE_AGENT), true
		}
		if ids, err := queries.ListAllOpenTerminalIDs(ctx); err == nil && len(ids) > 0 {
			return ids[0], int32(leapmuxv1.TabType_TAB_TYPE_TERMINAL), true
		}
		return "", 0, false
	}
}

// startBackgroundLoops starts every periodic task the worker owns. All of
// them stop with p.Ctx.
func startBackgroundLoops(p Params, svc *service.Service) {
	queries := db.New(p.DB)

	// Provide workspace tab sync data on connect. A nil return means "send
	// nothing", which is the only safe answer to a failed read: the hub
	// tombstones every owned tab a report omits, so shipping a partial
	// inventory would delete the user's tabs. Skipping the exchange costs a
	// reconnect's worth of convergence, and the reconciler's own pass retries.
	p.Client.TabSyncProvider = func() *leapmuxv1.WorkerTabInventory {
		sync, err := BuildTabSync(queries, svc.RegisteredBy())
		if err != nil {
			slog.Warn("tab sync: skipping the report rather than sending a partial inventory", "error", err)
			return nil
		}
		return sync
	}

	// Periodic orphan reconciler: walks worker-local rows against the hub's
	// CRDT-derived workspace_tab_owned view and drops the rows the CRDT no
	// longer agrees with. Runs once at startup and every hour after;
	// cancelled on ctx done.
	reconciler := service.NewOrphanReconciler(
		queries,
		svc.FileTabPaths,
		func(rctx context.Context) (*leapmuxv1.ListOwnedTabsForWorkerResponse, error) {
			return p.Client.ListOwnedTabsForWorker(rctx)
		},
		service.OrphanReconcilerOptions{
			// Reclaim worktrees whose tab links are all startup-race
			// strands (no live tab references them). Backstops the startup
			// link guards so a close that raced startup can't leak the
			// worktree dir.
			ReapWorktree: svc.ReapOrphanWorktree,
			CloseTab:     svc.CloseTabForReconcile,
		},
	)
	go reconciler.Run(p.Ctx)
	// Hand Shutdown a way to stop it. The reconciler's own teardowns are not
	// registered with svc.Cleanup, and the entry points call Shutdown BEFORE
	// cancelling p.Ctx, so without this a nudge-triggered pass could still be
	// writing when the caller closes the DB.
	svc.SetStopBackgroundLoops(reconciler.Stop)

	// The Hub's connect-time WorkerTabInventory reply only signals that the
	// hub has finished its side of the reconciliation; trigger the
	// worker-side reconciler so this worker converges on every reconnect
	// (not just on the hourly tick).
	p.Client.OnTabSyncResponse = func(*leapmuxv1.WorkerTabInventoryResponse) {
		reconciler.Trigger()
	}

	// The Hub also nudges mid-session, when a CRDT batch tombstones a tab this
	// worker hosts. Without it the reconciler was the only route for a close
	// whose E2EE RPC failed, or one performed CRDT-only by a peer client or
	// `leapmux control tab close` -- and it only ran hourly, so the agent
	// subprocess kept running (and burning tokens) until the next tick.
	p.Client.OnReconcileNudge = reconciler.Trigger

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

// BuildTabSync constructs a WorkerTabInventory message from the worker's
// database: every agent, terminal and file tab it hosts.
//
// The report must be COMPLETE or not sent at all, which is why this returns an
// error rather than a best-effort message. The hub treats it as authoritative
// by omission: handleWorkspaceTabsSync deletes the reported (tab_type, tab_id)
// keys from its own owned-tab list and submits a TombstoneTabOp for every
// leftover. So a tab type this function forgets, or a query whose failure it
// swallows, is not a missing entry -- it is an instruction to delete the user's
// tabs. All three types are listed here for that reason, and every read error
// aborts: the caller sends nothing and the next reconnect (or the reconciler's
// own pass) tries again.
func BuildTabSync(queries *db.Queries, owner userid.UserID) (*leapmuxv1.WorkerTabInventory, error) {
	ctx := context.Background()
	// The report carries no user axis on the wire, so the hub attributes ALL of it
	// to the connecting worker's registrant. Refusing an unminted owner is what
	// keeps that attribution honest: without one we cannot tell whose file tabs
	// these are, and reporting them anyway is how a foreign row would suppress a
	// tombstone the real owner's tab was due.
	if owner.IsZero() {
		return nil, errors.New("tab sync: no registered owner yet")
	}

	var tabs []*leapmuxv1.TabRef
	// One default-less switch over every TabType, so `exhaustive` (enabled in
	// .golangci.yml) fails the BUILD when a type is added. That matters more here
	// than almost anywhere: the hub tombstones every owned tab this report omits,
	// so a forgotten arm silently deletes the user's tabs of that type on every
	// reconnect. Three hand-written loops with nothing tying them to the enum is
	// exactly how the FILE arm came to be missing.
	for _, tabType := range []leapmuxv1.TabType{
		leapmuxv1.TabType_TAB_TYPE_AGENT,
		leapmuxv1.TabType_TAB_TYPE_TERMINAL,
		leapmuxv1.TabType_TAB_TYPE_FILE,
		leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED,
	} {
		var ids []string
		var err error
		switch tabType {
		case leapmuxv1.TabType_TAB_TYPE_AGENT:
			// Active and closed alike: a closed row the hub no longer lists is what
			// the reconciler converges, so omitting it would hide the divergence.
			ids, err = queries.ListAllAgentIDs(ctx)
		case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
			ids, err = queries.ListAllTerminalIDs(ctx)
		case leapmuxv1.TabType_TAB_TYPE_FILE:
			// A file tab's CRDT row carries a worker_id (the frontend stamps
			// ctx.workerId and the validator rejects a live tab without one), so it
			// lands in workspace_tab_owned and comes back from ListOwnedByWorker,
			// which applies no tab-type filter. Omitting this arm tombstoned every
			// file tab on the worker on every single reconnect.
			//
			// Scoped to the owner, unlike the two above: worker_file_tabs is PK'd on
			// (user_id, tab_id) because file-tab ids are client-minted and unique
			// only within a user, and a stale second owner's rows can outlive a
			// deregister (ClearState removes state.json, not worker.db).
			ids, err = fileTabIDsForOwner(ctx, queries, owner)
		case leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED:
			// Not a hostable tab type; nothing to report.
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("list %s ids: %w", tabType, err)
		}
		for _, id := range ids {
			tabs = append(tabs, &leapmuxv1.TabRef{TabType: tabType, TabId: id})
		}
	}

	return &leapmuxv1.WorkerTabInventory{Tabs: tabs}, nil
}

// fileTabIDsForOwner returns owner's file-tab ids, dropping any row belonging to
// a different user. See the FILE arm of BuildTabSync for why the filter matters.
func fileTabIDsForOwner(ctx context.Context, queries *db.Queries, owner userid.UserID) ([]string, error) {
	rows, err := queries.ListAllWorkerFileTabs(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, ft := range rows {
		if !owner.Matches(ft.UserID) {
			continue
		}
		ids = append(ids, ft.TabID)
	}
	return ids, nil
}
