package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
	tunnelpkg "github.com/leapmux/leapmux/tunnel"
	"github.com/leapmux/leapmux/util/ctxutil"
	"github.com/leapmux/leapmux/util/errwrap"
	"github.com/leapmux/leapmux/util/version"
)

// operationDrainTimeout bounds Shutdown's wait for admitted operations after
// a.ctx is cancelled. Network/lifecycle operations honor a.ctx and unwind
// promptly; the filesystem/exec operations (OpenInEditor, ListEditors refresh,
// OpenFullDiskAccessSettings, CliInstallSymlink) ignore it, so an unbounded
// wait would let one stuck launch hang shutdown -- and the process -- forever.
// Mirrors the bounded RPCSession.drainHandlers and drainRelay drains. A
// var so tests can shorten it.
var operationDrainTimeout time.Duration = 5 * time.Second

// App is the desktop sidecar state managed over stdio RPC by the Tauri shell.
type App struct {
	ctx          context.Context
	cancel       context.CancelFunc
	shutdownOnce sync.Once
	// ops admits and drains side-effecting operations, and is closed (with a.cancel
	// run inside its critical section) by Shutdown.
	ops operationGate
	// transitionMu is the transition gate; see beginTransition. A context-bounded
	// ctxutil.Mutex rather than a sync.Mutex because Shutdown must be able to GIVE
	// UP on acquiring it. Its zero value is a usable unlocked mutex, so a
	// bare-struct App keeps working.
	transitionMu ctxutil.Mutex
	lifecycleMu  sync.RWMutex
	shutdownErr  error
	configMu     sync.RWMutex
	config       *DesktopConfig
	connection   *desktopConnection
	tunnels      *TunnelManager
	editors      *EditorRegistry
	// startSolo launches the in-process Hub+Worker. A function field (mirroring
	// TunnelManager.openCh/dial) so tests can block startup and assert
	// lifecycleMu is released across it rather than held for the whole boot.
	startSolo  func(context.Context) (*soloRuntime, error)
	binaryHash string
	// eventSinkMu/eventSink are a self-contained pub-sub bolted onto App;
	// extracting them into their own type to shrink App's mutex surface is
	// tracked in https://github.com/leapmux/leapmux/issues/295.
	eventSinkMu sync.RWMutex
	eventSink   func(*desktoppb.Event)
	// eventSinkForRelay is the relay-aware variant of eventSink: it carries the
	// owning wrapper id of the relay that emitted the event, so a frame that
	// cannot be delivered (failFrameForRelay) can close ONLY that relay rather than
	// whichever relay happens to be installed by the time the close goroutine
	// runs. Set alongside eventSink by the RPCSession. nil outside a session,
	// in which case EmitRelayEvent falls back to the generic (non-relay) sink.
	eventSinkForRelay func(owner uint64, event *desktoppb.Event)
}

type desktopConnection struct {
	ctx    context.Context
	stop   context.CancelFunc
	proxy  *HubProxy
	solo   *soloRuntime
	relay  *ChannelRelay
	hubURL string
	// Both relays carry their own owner id (see wsRelay.owner), so ownership dies
	// with the relay it named rather than lingering on the connection.
	orgEventsRelay *OrgEventsRelay
}

const protocolVersion = "1"

func NewApp(binaryHash string) *App {
	ctx, cancel := context.WithCancel(context.Background())
	app := &App{
		ctx:        ctx,
		cancel:     cancel,
		tunnels:    NewTunnelManager(),
		editors:    defaultEditorRegistry(),
		binaryHash: binaryHash,
	}
	app.startSolo = app.defaultStartSolo
	app.startup()
	return app
}

func (a *App) startup() {
	cfg, err := LoadConfig()
	if err != nil {
		cfg = &DesktopConfig{}
	}
	a.config = cfg
}

func (a *App) Shutdown() error {
	a.shutdownOnce.Do(func() {
		// Shut the gate and cancel under one lock, so no operation can be admitted
		// after cancellation; admitted network operations then unwind on a.ctx.
		a.ops.close(a.cancel)
		// ONE deadline spans the drain AND the transition-gate acquisition below.
		// They wait on the same straggler -- a transition's beginOperation done()
		// runs after its endTransition (defers unwind LIFO), so the drain can only
		// time out while the gate is still held -- and budgeting them separately
		// would double the worst case before the teardown runs.
		deadline := time.Now().Add(operationDrainTimeout)
		// Drain every admitted side effect before shared lifecycle state is destroyed.
		a.drainOperations(time.Until(deadline))
		// Tear down with or without the gate. Proceeding without it is safe:
		// lifecycleMu is never held across blocking work, and a straggling
		// ConnectSolo/ConnectDistributed that later reaches its commit hits
		// rejectIfShuttingDown() under the write lock and rolls itself back. Waiting
		// for it unboundedly is NOT safe: the gate holder's blocking step
		// (hub.NewServer -- net.Listen, store open, migrations) takes no context, so
		// a flocked SQLite file or a stuck migration would wedge Shutdown, and with
		// it main's deferred exit, forever.
		if a.tryBeginTransition(time.Until(deadline)) {
			defer a.endTransition()
		} else {
			slog.Warn("desktop sidecar: transition gate not acquired during shutdown; tearing down without it")
		}
		// disconnectLocked tears down relays/tunnels/proxy under the lifecycle
		// write lock and hands back the solo runtime; stopSolo runs OUTSIDE the
		// lock so the Hub's full graceful shutdown does not wedge every
		// SidecarInfo/ProxyHTTP reader behind a write lock.
		a.lifecycleMu.Lock()
		soloRuntime := a.disconnectLocked()
		a.lifecycleMu.Unlock()
		if soloRuntime != nil {
			a.shutdownErr = errors.Join(a.shutdownErr, stopSolo(soloRuntime))
		}
	})
	return a.shutdownErr
}

// drainOperations waits (bounded by timeout) for admitted operations to finish so
// their side effects complete before shared lifecycle state is torn down. A
// straggler that ignores a.ctx (a non-cancellable editor launch or filesystem
// scan) is abandoned after the timeout; those operations touch only their own
// subsystem (the editor registry, OS settings), never the connection state
// disconnectLocked destroys, so proceeding is safe.
func (a *App) drainOperations(timeout time.Duration) {
	a.ops.drain(timeout,
		"desktop sidecar: operation drain timed out during shutdown; abandoning in-flight operations")
}

// beginTransition takes the transition gate, blocking until it is free. The gate
// (transitionMu) serializes the mode transitions (SwitchMode, ConnectSolo,
// ConnectDistributed) against each other and against Shutdown's teardown.
//
// It is a context-bounded ctxutil.Mutex rather than a sync.Mutex because Shutdown
// must be able to GIVE UP on it (see tryBeginTransition). Shutdown bounds its
// operation drain precisely because a straggler may ignore cancellation -- and a
// straggling transition is exactly what holds this gate, since its blocking step
// (hub.NewServer: net.Listen, store open, migrations) takes no context and so
// cannot observe the cancelled connection context at all. A sync.Mutex.Lock after
// that bounded drain would silently give the wait back its unbounded shape and
// hang the process exit main defers Shutdown from; a context-bounded acquire can
// give up.
func (a *App) beginTransition() {
	// context.Background never cancels, so this unbounded acquire can never fail;
	// the error is safe to ignore.
	_ = a.transitionMu.Lock(context.Background())
}

// endTransition releases the transition gate. Only the holder may call it.
func (a *App) endTransition() {
	a.transitionMu.Unlock()
}

// tryBeginTransition takes the transition gate, giving up after timeout, and
// reports whether it took it. The caller must endTransition only if it did.
//
// A free gate wins outright even on an exhausted (<= 0) budget, because
// Mutex.Lock's fast path takes a free lock before consulting the context:
// Shutdown shares one deadline across its drain and this acquisition, so timeout
// is routinely <= 0 here, and a plain select would otherwise pick at random
// between two ready cases and abandon a gate that was in fact free. The parent is
// context.Background, NOT a.ctx: Shutdown cancels a.ctx before it reaches here, so
// parenting on it would collapse the wait to the fast path alone.
func (a *App) tryBeginTransition(timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return a.transitionMu.Lock(ctx) == nil
}

func (a *App) beginOperation() (func(), error) {
	done, ok := a.ops.begin()
	if !ok {
		return nil, fmt.Errorf("desktop sidecar is shutting down")
	}
	// A bare-struct App (focused tests) has no context; a real one may have been
	// cancelled by something other than Shutdown.
	if a.ctx != nil && a.ctx.Err() != nil {
		done()
		return nil, fmt.Errorf("desktop sidecar is shutting down")
	}
	return done, nil
}

func (a *App) rejectIfShuttingDown() error {
	if a.ctx != nil && a.ctx.Err() != nil {
		return fmt.Errorf("desktop sidecar is shutting down")
	}
	return nil
}

// acquireLifecycleLock takes the write side of lifecycleMu and rejects the
// operation if the sidecar is shutting down, returning the func that releases
// the lock for the caller to defer or call explicitly. Collapsing the
// lock→reject→early-unlock ladder into one helper puts the "is the lock held on
// this return path?" question in the SIGNATURE (the returned unlock func) rather
// than restating it at each early-return site -- the asymmetric postcondition
// the six sites that used to spell it by hand could each get wrong. The caller
// that releases mid-function (e.g. openRelay, which drops the lock across the
// dial) calls unlock() explicitly; the caller that holds for the whole function
// defers it.
func (a *App) acquireLifecycleLock() (func(), error) {
	a.lifecycleMu.Lock()
	if err := a.rejectIfShuttingDown(); err != nil {
		a.lifecycleMu.Unlock()
		return nil, err
	}
	return a.lifecycleMu.Unlock, nil
}

// acquireLifecycleRLock is the read-side counterpart, for handlers that only
// read shared state (the currently-installed relay or connection) rather than
// mutating it.
func (a *App) acquireLifecycleRLock() (func(), error) {
	a.lifecycleMu.RLock()
	if err := a.rejectIfShuttingDown(); err != nil {
		a.lifecycleMu.RUnlock()
		return nil, err
	}
	return a.lifecycleMu.RUnlock, nil
}

// CloseRelayForUndeliverableEvent tears down the relay whose ordered stream just
// lost a frame, and reports the close to the frontend so it reconnects.
//
// A relay stream tolerates no gap (Noise ciphertext advances a per-message nonce;
// CRDT ops have no gap detection), so a frame the sidecar could not deliver
// leaves the stream permanently desynced while still reporting healthy. Closing
// the relay converts that into the one failure the frontend already recovers from
// -- it reconnects, re-handshakes and re-bootstraps -- and scopes the damage to
// the affected relay instead of the whole sidecar session.
//
// emitterOwner is the owning wrapper id of the relay whose read loop emitted the
// undeliverable frame (threaded through emitForOwner -> EmitRelayEvent ->
// failFrameForRelay). The close gates on it the same way CloseChannelRelay does: if a
// successor wrapper's open has superseded this relay by the time this goroutine
// acquires lifecycleMu, the successor's relay is left alone -- the emitter's read
// loop is already being torn down by the successor's install, and closing the
// successor's relay for a fault that belonged to the emitter would force the
// successor to reconnect for nothing.
//
// The close event must be emitted explicitly: tearing a relay down cancels its
// context first, so its read loop exits WITHOUT emitting the close it would send
// on a network error, and the frontend would otherwise just go quiet. The event is
// tiny, so it fits the frame budget even when the data frame that failed did not.
// An undeliverable close event is dropped rather than retried -- only *Message
// payloads reach this path -- so this can never recurse.
//
// Must not run on the failing relay's own read-loop goroutine: the teardown joins
// that loop (see RPCSession.failFrameForRelay, which dispatches it asynchronously).
func (a *App) CloseRelayForUndeliverableEvent(emitterOwner uint64, event *desktoppb.Event) {
	switch event.GetPayload().(type) {
	case *desktoppb.Event_ChannelMessage:
		a.closeUndeliverableRelay(
			func(c *desktopConnection) *wsRelay {
				if c.relay == nil {
					return nil
				}
				return &c.relay.wsRelay
			},
			emitterOwner,
			a.closeChannelRelay,
			"channel relay frame was undeliverable; closing the relay so the frontend re-handshakes",
			&desktoppb.Event{
				Payload: &desktoppb.Event_ChannelClose{
					ChannelClose: &desktoppb.ChannelCloseEvent{
						Code:     uint32(websocket.StatusInternalError),
						Reason:   "sidecar could not deliver a channel frame",
						WasClean: false,
					},
				},
			})
	case *desktoppb.Event_OrgEventsMessage:
		a.closeUndeliverableRelay(
			func(c *desktopConnection) *wsRelay {
				if c.orgEventsRelay == nil {
					return nil
				}
				return &c.orgEventsRelay.wsRelay
			},
			emitterOwner,
			a.closeOrgEventsRelay,
			"org-events relay frame was undeliverable; closing the relay so the frontend re-bootstraps",
			&desktoppb.Event{
				Payload: &desktoppb.Event_OrgEventsClose{
					OrgEventsClose: &desktoppb.OrgEventsCloseEvent{
						Code:     uint32(websocket.StatusInternalError),
						Reason:   "sidecar could not deliver an org-events frame",
						WasClean: false,
					},
				},
			})
	}
}

// closeUndeliverableRelay is the shared lock/gate/close/drain/log/emit
// choreography behind CloseRelayForUndeliverableEvent's two arms; the arms
// differ only in which relay slot they name, the owning wrapper id, what the
// log says, and which close event the frontend receives, so those ride as
// parameters the way wsRelay.emitClose takes its event builder.
//
// The ownership gate (getRelay(...).owner == emitterOwner) is the point of the
// emitterOwner parameter: without it, a close spawned by emitter A's read loop
// could execute after emitter B's open superseded A and tear down B's relay,
// forcing B to reconnect for A's fault. The gate makes the close a no-op when
// the installed relay is no longer the emitter's, matching closeRelayIfOwner.
// The close event is emitted ONLY when a relay was actually closed, so a
// superseded emitter does not push a spurious close into a successor's stream.
func (a *App) closeUndeliverableRelay(getRelay func(*desktopConnection) *wsRelay, emitterOwner uint64, closeRelay func() <-chan struct{}, logMsg string, closeEvent *desktoppb.Event) {
	a.lifecycleMu.Lock()
	var drain <-chan struct{}
	if a.connection != nil {
		if relay := getRelay(a.connection); relay != nil && relay.owner == emitterOwner {
			drain = closeRelay()
		}
	}
	a.lifecycleMu.Unlock()
	if drain == nil {
		// The emitter's relay is no longer installed (superseded) or never was;
		// nothing to close, and a close event would land on a successor that
		// never saw the failing frame.
		slog.Error(logMsg + " (relay superseded or gone; no close emitted)")
		return
	}
	drainRelay(drain)
	slog.Error(logMsg)
	a.EmitEvent(closeEvent)
}

func (a *App) SetEventSink(sink func(*desktoppb.Event)) {
	a.eventSinkMu.Lock()
	defer a.eventSinkMu.Unlock()
	a.eventSink = sink
}

// SetEventSinkForRelay installs the relay-aware sink the RPCSession provides
// alongside the generic sink. The relay read loops route their emits through it
// (via EmitRelayEvent) so a frame that cannot be delivered carries the emitting
// relay's owner id forward to the close path.
func (a *App) SetEventSinkForRelay(sink func(owner uint64, event *desktoppb.Event)) {
	a.eventSinkMu.Lock()
	defer a.eventSinkMu.Unlock()
	a.eventSinkForRelay = sink
}

// emitForOwner returns a func the relay read loops call in place of a bare
// EmitEvent, capturing the emitting relay so the owner id at emit time rides
// alongside the event. The closure reads relay.owner at CALL time rather than
// capturing its value, because owner is stamped AFTER newWSRelay constructs the
// relay -- but it is stable for the relay's lifetime once stamped (a supersede
// replaces connection.relay, not this relay's owner field), so the read loop's
// emit always reports the id this relay was installed under.
func (a *App) emitForOwner(relay *wsRelay) func(*desktoppb.Event) {
	return func(event *desktoppb.Event) { a.EmitRelayEvent(relay.owner, event) }
}

// EmitRelayEvent routes a relay-sourced event through the relay-aware sink when
// one is installed, falling back to the generic EmitEvent otherwise (e.g. when
// the relay emits before the RPCSession has wired its sink, or in a focused
// test that constructs a bare App). The fallback loses the owner id, but a
// session without a sink has no shell pipe to deliver an undeliverable frame
// over in the first place.
func (a *App) EmitRelayEvent(owner uint64, event *desktoppb.Event) {
	a.eventSinkMu.RLock()
	sink := a.eventSinkForRelay
	a.eventSinkMu.RUnlock()
	if sink != nil {
		sink(owner, event)
		return
	}
	a.EmitEvent(event)
}

func (a *App) EmitEvent(event *desktoppb.Event) {
	a.eventSinkMu.RLock()
	sink := a.eventSink
	a.eventSinkMu.RUnlock()
	if sink != nil {
		sink(event)
	}
}

func (a *App) SidecarInfo() *desktoppb.SidecarInfo {
	a.lifecycleMu.RLock()
	defer a.lifecycleMu.RUnlock()
	mode := desktoppb.SidecarShellMode_SIDECAR_SHELL_MODE_LAUNCHER
	connection := a.connection
	connected := connection != nil
	if connected {
		if connection.solo != nil {
			mode = desktoppb.SidecarShellMode_SIDECAR_SHELL_MODE_SOLO
		} else {
			mode = desktoppb.SidecarShellMode_SIDECAR_SHELL_MODE_DISTRIBUTED
		}
	}

	return &desktoppb.SidecarInfo{
		ProtocolVersion: protocolVersion,
		BinaryHash:      a.binaryHash,
		Pid:             int64(os.Getpid()),
		ShellMode:       mode,
		Connected:       connected,
		HubUrl:          connectionHubURL(connection),
	}
}

func (a *App) GetConfig() *DesktopConfig {
	a.configMu.RLock()
	defer a.configMu.RUnlock()
	config := *a.config
	return &config
}

// SetWindowSize persists the window geometry and display mode. width/height of
// 0 leave the stored windowed size untouched -- the frontend sends 0 while
// maximized or fullscreen so the last windowed dimensions survive.
func (a *App) SetWindowSize(width, height int, mode string) error {
	done, err := a.beginOperation()
	if err != nil {
		return err
	}
	defer done()
	return a.updateConfig(func(config *DesktopConfig) {
		if width > 0 {
			config.WindowWidth = width
		}
		if height > 0 {
			config.WindowHeight = height
		}
		config.WindowMode = mode
	})
}

func (a *App) updateConfig(update func(*DesktopConfig)) error {
	a.configMu.Lock()
	defer a.configMu.Unlock()
	next := *a.config
	update(&next)
	if err := SaveConfig(&next); err != nil {
		return err
	}
	a.config = &next
	return nil
}

// clearDesktopMode resets the persisted desktop mode/HubURL back to launcher.
// Shared by the SwitchMode transition and the solo/distributed rollback paths
// so the three cannot drift apart.
func clearDesktopMode(config *DesktopConfig) {
	config.Mode = ""
	config.HubURL = ""
}

func (a *App) requireLauncher() error {
	if a.connection != nil {
		return fmt.Errorf("already connected; switch to launcher mode first")
	}
	return nil
}

// rejectIfCannotConnect reports why a fresh connection cannot begin -- the
// sidecar is shutting down, or it is already connected -- so the solo and
// distributed connect paths cannot drift on which precondition they check,
// or in what order. Caller holds lifecycleMu.
func (a *App) rejectIfCannotConnect() error {
	if err := a.rejectIfShuttingDown(); err != nil {
		return err
	}
	return a.requireLauncher()
}

// reacquireConnectionForInstall re-takes the lifecycle write lock after an
// unlocked dial and confirms the app has not shut down or swapped out the
// connection snapshotted before the dial. On success it returns the unlock the
// caller now owes (defer it); on failure unlock is nil and the lock is already
// released. Returning the unlock -- rather than leaving the lock silently held
// on one return path -- puts the asymmetric postcondition in the signature, so
// a caller cannot compile without deciding what to do with the lock it may
// hold. Shared by the channel and org-events relay opens so the post-dial
// install guard cannot drift between them.
func (a *App) reacquireConnectionForInstall(connection *desktopConnection) (unlock func(), err error) {
	unlock, err = a.acquireLifecycleLock()
	if err != nil {
		return nil, err
	}
	if a.connection != connection {
		unlock()
		return nil, fmt.Errorf("connection changed during relay setup")
	}
	return unlock, nil
}

type BuildInfo struct {
	Version    string `json:"version"`
	CommitHash string `json:"commit_hash"`
	CommitTime string `json:"commit_time"`
	BuildTime  string `json:"build_time"`
	Branch     string `json:"branch"`
}

func (a *App) GetBuildInfo() BuildInfo {
	return BuildInfo{
		Version:    version.Value,
		CommitHash: version.CommitHash,
		CommitTime: version.CommitTime,
		BuildTime:  version.BuildTime,
		Branch:     version.Branch,
	}
}

func (a *App) CheckFullDiskAccess() bool {
	return checkFullDiskAccess()
}

func (a *App) OpenFullDiskAccessSettings() error {
	done, err := a.beginOperation()
	if err != nil {
		return err
	}
	defer done()
	return openFullDiskAccessSettings()
}

type lifecycleOutcome struct {
	cleanupErrors []error
}

func (a *App) SwitchMode() (lifecycleOutcome, error) {
	done, err := a.beginOperation()
	if err != nil {
		return lifecycleOutcome{}, err
	}
	defer done()
	a.beginTransition()
	defer a.endTransition()
	if err := a.rejectIfShuttingDown(); err != nil {
		return lifecycleOutcome{}, err
	}
	if err := a.updateConfig(clearDesktopMode); err != nil {
		return lifecycleOutcome{}, fmt.Errorf("save cleared desktop mode: %w", err)
	}
	a.lifecycleMu.RLock()
	connection := a.connection
	a.lifecycleMu.RUnlock()
	if connection != nil {
		connection.stop()
	}
	a.lifecycleMu.Lock()
	soloRuntime := a.disconnectLocked()
	a.lifecycleMu.Unlock()
	stopErr := stopSolo(soloRuntime)
	if stopErr != nil {
		return lifecycleOutcome{cleanupErrors: []error{fmt.Errorf("stop solo mode: %w", stopErr)}}, nil
	}
	return lifecycleOutcome{}, nil
}

// disconnectLocked tears down the current connection's relays, tunnels, and
// proxy under the lifecycle write lock and returns the solo runtime so the
// caller can stop it OUTSIDE the lock. stopSolo blocks for the Hub's full
// graceful shutdown; holding lifecycleMu across it would wedge every
// SidecarInfo/ProxyHTTP/SendChannelMessage reader for that window -- the same
// wedge the start path already releases the lock to avoid. Caller holds
// lifecycleMu for writing; it is still held on return.
func (a *App) disconnectLocked() *soloRuntime {
	connection := a.connection
	if connection != nil {
		connection.stop()
	}
	// Detach both relays without draining under the lock: this is a full teardown,
	// so no successor relay is installed and the detached read-loop goroutines
	// self-clean once their force-closed sockets error out. Draining here would
	// freeze every lifecycle reader for relayDrainTimeout (see wsRelay.detach).
	_ = a.closeChannelRelay()
	_ = a.closeOrgEventsRelay()
	a.tunnels.CloseAll()
	// Drop idle proxy connections before stopping the Hub so they don't pin
	// named-pipe handles open across solo.Stop() and block the next ListenPipe.
	if connection != nil && connection.proxy != nil {
		connection.proxy.client.CloseIdleConnections()
		if connection.proxy.wsClient != nil {
			connection.proxy.wsClient.CloseIdleConnections()
		}
	}
	a.connection = nil
	if connection == nil {
		return nil
	}
	return connection.solo
}

func connectionHubURL(connection *desktopConnection) string {
	if connection == nil {
		return ""
	}
	return connection.hubURL
}

func (a *App) ConnectSolo(ctx context.Context) error {
	done, err := a.beginOperation()
	if err != nil {
		return err
	}
	defer done()
	if ctx == nil {
		ctx = context.Background()
	}
	a.beginTransition()
	defer a.endTransition()

	// Validate and create the connection lifetime under the write lock, then
	// RELEASE it across solo.Start (DB open, migrations, bootstrap, listener
	// bind, worker bring-up) so a cold Hub does not block every
	// SidecarInfo/ProxyHTTP/SendChannelMessage reader behind a write lock. The
	// sibling relay/distributed paths release lifecycleMu across their blocking
	// I/O for the same reason; the transition gate still serializes this
	// transition, and a concurrent Shutdown cancels a.ctx and is caught by the
	// post-start re-validation below.
	a.lifecycleMu.Lock()
	if err := a.rejectIfCannotConnect(); err != nil {
		a.lifecycleMu.Unlock()
		return err
	}
	connectionCtx, connectionStop := context.WithCancel(a.ctx)
	stopRequestCancellation := context.AfterFunc(ctx, connectionStop)
	defer stopRequestCancellation()
	a.lifecycleMu.Unlock()

	soloRuntime, err := a.startSolo(connectionCtx)
	if err != nil {
		connectionStop()
		return err
	}

	// solo.Start already blocked until the Hub's local listener was reachable,
	// so no extra polling is needed before building the proxy.
	proxy, err := newLocalProxy(soloRuntime.instance.LocalListenURL())
	if err != nil {
		return a.rollbackSolo(fmt.Errorf("build local proxy: %w", err), connectionStop, soloRuntime, false)
	}
	if ctx.Err() != nil || !stopRequestCancellation() {
		return a.rollbackSolo(ctx.Err(), connectionStop, soloRuntime, false)
	}

	// Re-acquire the write lock to commit. A Shutdown that fired during
	// solo.Start cancelled a.ctx; reject it and roll back the started Hub.
	// commitSoloConnection does the validation + commit under the lock and
	// returns a rollback to perform OUTSIDE it, so rollbackSolo's stopSolo does
	// not wedge lifecycle readers during the Hub's graceful shutdown.
	a.lifecycleMu.Lock()
	rb := a.commitSoloConnection(connectionCtx, connectionStop, soloRuntime, proxy)
	a.lifecycleMu.Unlock()
	if rb != nil {
		return a.rollbackSolo(rb.primary, rb.stop, soloRuntime, rb.clearConfig)
	}
	return nil
}

// soloRollback describes how to undo a partially-committed solo connection.
// commitSoloConnection returns one (instead of rolling back inline) so the
// caller can run rollbackSolo -- and its blocking stopSolo -- OUTSIDE the
// lifecycle write lock.
type soloRollback struct {
	primary     error
	stop        context.CancelFunc
	clearConfig bool
}

// commitSoloConnection validates the post-start state and commits the solo
// connection under the lifecycle write lock. Returns nil once committed, or a
// soloRollback describing how to undo it. Caller holds lifecycleMu for writing.
func (a *App) commitSoloConnection(connectionCtx context.Context, connectionStop context.CancelFunc, runtime *soloRuntime, proxy *HubProxy) *soloRollback {
	if err := a.rejectIfShuttingDown(); err != nil {
		return &soloRollback{primary: err, stop: connectionStop}
	}
	if err := a.updateConfig(func(config *DesktopConfig) {
		config.Mode = "solo"
		config.HubURL = ""
	}); err != nil {
		return &soloRollback{primary: fmt.Errorf("save config: %w", err), stop: connectionStop}
	}
	if err := connectionCtx.Err(); err != nil {
		return &soloRollback{primary: err, clearConfig: true}
	}
	a.applyProxyToTunnels(proxy)
	a.connection = &desktopConnection{
		ctx:   connectionCtx,
		stop:  connectionStop,
		proxy: proxy,
		solo:  runtime,
	}
	return nil
}

// rollbackSolo undoes a partially-committed solo connection, joining the primary
// error with any cleanup failures. stop cancels the connection's lifetime
// context (nil when it is already cancelled). clearConfig reverts the desktop
// mode/HubURL that was already saved to disk.
func (a *App) rollbackSolo(primary error, stop context.CancelFunc, runtime *soloRuntime, clearConfig bool) error {
	if stop != nil {
		stop()
	}
	var cleanup []error
	if clearConfig {
		cleanup = append(cleanup, errwrap.Wrap(
			a.updateConfig(clearDesktopMode), "rollback solo config"))
	}
	cleanup = append(cleanup, errwrap.Wrap(stopSolo(runtime), "rollback solo mode"))
	return errors.Join(append([]error{primary}, cleanup...)...)
}

// rollbackDistributed undoes a partially-committed distributed connection whose
// config was already saved: cancels the connection context and reverts the
// desktop mode/HubURL.
func (a *App) rollbackDistributed(primary error, stop context.CancelFunc) error {
	if stop != nil {
		stop()
	}
	rollbackErr := a.updateConfig(clearDesktopMode)
	return errors.Join(primary, errwrap.Wrap(rollbackErr, "rollback distributed config"))
}

func (a *App) ConnectDistributed(ctx context.Context, hubURL string) error {
	done, err := a.beginOperation()
	if err != nil {
		return err
	}
	defer done()
	a.beginTransition()
	defer a.endTransition()
	a.lifecycleMu.Lock()
	if err := a.rejectIfCannotConnect(); err != nil {
		a.lifecycleMu.Unlock()
		return err
	}
	a.lifecycleMu.Unlock()
	hubURL = strings.TrimRight(strings.TrimSpace(hubURL), "/")
	if hubURL == "" {
		return fmt.Errorf("hub URL is required")
	}
	if !strings.HasPrefix(hubURL, "http://") && !strings.HasPrefix(hubURL, "https://") {
		hubURL = "https://" + hubURL
	}

	probeCtx, cancelProbe := ctxutil.WithLinkedCancel(a.ctx, ctx)
	defer cancelProbe()
	if err := probeHub(probeCtx, hubURL); err != nil {
		return fmt.Errorf("cannot reach Hub at %s: %w", hubURL, err)
	}
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	if err := a.rejectIfCannotConnect(); err != nil {
		return err
	}
	if err := probeCtx.Err(); err != nil {
		return err
	}
	// The successful probe is the transition's admission point. Stop observing
	// the short-lived RPC context before the durable config commit.
	cancelProbe()
	if err := a.updateConfig(func(config *DesktopConfig) {
		config.Mode = "distributed"
		config.HubURL = hubURL
	}); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	connectionCtx, connectionStop := context.WithCancel(a.ctx)
	if err := connectionCtx.Err(); err != nil {
		return a.rollbackDistributed(err, connectionStop)
	}
	proxy := newHTTPProxy(hubURL)
	a.applyProxyToTunnels(proxy)
	a.connection = &desktopConnection{
		ctx:    connectionCtx,
		stop:   connectionStop,
		proxy:  proxy,
		hubURL: hubURL,
	}
	return nil
}

func (a *App) CreateTunnel(requestCtx context.Context, config TunnelConfig) (*TunnelInfo, error) {
	done, err := a.beginOperation()
	if err != nil {
		return nil, err
	}
	defer done()
	// Snapshot the connection under the read lock, then RELEASE it across the
	// blocking E2EE channel open + TCP listener bind (mirroring the relay and
	// connect paths) so a cold handshake cannot wedge SwitchMode/Shutdown behind a
	// write lock. The tunnel binds to connection.ctx as its lifetime, and
	// TunnelManager.CreateTunnel rejects a cancelled lifetime before registering
	// the tunnel, so a connection torn down during the open is handled without
	// holding lifecycleMu.
	unlock, err := a.acquireLifecycleRLock()
	if err != nil {
		return nil, err
	}
	connection := a.connection
	if connection == nil {
		unlock()
		return nil, fmt.Errorf("not connected")
	}
	config.HubURL = connection.proxy.baseURL
	lifetimeCtx := connection.ctx
	a.lifecycleMu.RUnlock()

	operationCtx, cancelOperation := ctxutil.WithLinkedCancel(lifetimeCtx, requestCtx)
	defer cancelOperation()
	return a.tunnels.CreateTunnel(operationCtx, lifetimeCtx, config)
}

// applyProxyToTunnels wires the current proxy's HTTP clients into the tunnel
// manager so subsequent CreateTunnel calls dial through them.
func (a *App) applyProxyToTunnels(proxy *HubProxy) {
	if proxy == nil {
		return
	}
	a.tunnels.SetChannelOptions(&tunnelpkg.OpenChannelOptions{
		HTTPClient:          proxy.client,
		WebSocketHTTPClient: proxy.wsClient,
	})
}

func (a *App) DeleteTunnel(tunnelID string) error {
	done, err := a.beginOperation()
	if err != nil {
		return err
	}
	defer done()
	return a.tunnels.DeleteTunnel(tunnelID)
}

func (a *App) ListTunnels() []TunnelInfo {
	return a.tunnels.ListTunnels()
}

// ResetTunnels tears down every tunnel and invalidates the pooled E2EE channels
// without dropping the sidecar<->Hub connection, so the proxy stays usable for
// the next login. The frontend fires it on a logout / auth change: the sidecar
// authenticates to the Hub purely by the proxy's cookie jar and has no user
// identity of its own, so a distributed-mode user switch would otherwise leave
// the previous user's tunnels relaying (and their cached channels reusable)
// under the old identity until the Hub revokes the session. CloseAll is
// idempotent -- a no-op when there are no tunnels (e.g. solo mode, where logout
// never happens) -- so this needs no connection guard beyond the shutdown gate.
func (a *App) ResetTunnels() error {
	done, err := a.beginOperation()
	if err != nil {
		return err
	}
	defer done()
	a.tunnels.CloseAll()
	return nil
}

func (a *App) ListEditors(refresh bool) ([]DetectedEditor, error) {
	if refresh {
		done, err := a.beginOperation()
		if err != nil {
			return nil, err
		}
		defer done()
		return a.editors.Refresh(), nil
	}
	return a.editors.List(), nil
}

func (a *App) OpenInEditor(editorID, path string) error {
	done, err := a.beginOperation()
	if err != nil {
		return err
	}
	defer done()
	return a.editors.Open(editorID, path)
}

// CliPathStatus reports whether the bundled `leapmux` CLI is discoverable on
// the current user's PATH. macOS only — other platforms always report
// STATE_UNAVAILABLE because PATH integration is handled by the installer.
func (a *App) CliPathStatus() *desktoppb.CliPathStatusResponse {
	return cliPathStatusFromSidecar()
}

// CliInstallSymlink attempts to create the on-PATH symlink pointing at the
// bundled `leapmux`. macOS only. `force` lets the caller overwrite a real
// (non-symlink) file at the destination — the UI sets it on the user's
// "Replace" confirmation after a prior call reported
// RESULT_ALREADY_EXISTS_REAL_FILE.
func (a *App) CliInstallSymlink(force bool) (*desktoppb.CliInstallSymlinkResponse, error) {
	done, err := a.beginOperation()
	if err != nil {
		return nil, err
	}
	defer done()
	return cliInstallSymlinkFromSidecar(force), nil
}
