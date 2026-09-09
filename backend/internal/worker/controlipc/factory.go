package controlipc

import (
	"context"
	"errors"
	"log/slog"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/crossworker"
	"github.com/leapmux/leapmux/internal/worker/service"
)

// DelegationLifecycle is the worker-side hook the IPC factory uses to
// pin the per-user delegation-token slot for the lifetime of a spawn.
// Implemented by *crossworker.DelegationStore. Splitting it from
// DelegationProvider keeps the per-call mint API independent from the
// spawn-bookkeeping API and lets the factory be wired with a nil lifecycle
// in tests / minimal configurations without forcing
// crossworker.DelegationStore in.
//
// Acquire carries the spawn's tab identity (tabID, tabType) because
// the hub's mint endpoint validates that the calling worker owns the tab
// -- it is the provenance recorded on the token, not a scope. The first
// spawn for a given user supplies it; concurrent spawns share the same
// cached bearer.
type DelegationLifecycle interface {
	Acquire(userID userid.UserID)
	Release(ctx context.Context, userID userid.UserID) error
}

// revokeRevokeTimeout caps the hub call we make from the cleanup
// goroutine. Spawn cleanups need to return promptly; the delegation
// row will expire on its own if the revoke RPC doesn't land in time.
const releaseRevokeTimeout = 5 * time.Second

// Factory implements service.ControlIPCFactory by minting a per-agent
// (or per-terminal) socket + token, wiring it into a Router that knows
// how to talk to the local dispatcher, sibling workers via
// crossworker.Client, and the hub via a hub-bound HTTP client.
type Factory struct {
	WorkerID    string
	Dispatcher  *channel.Dispatcher
	CrossWorker *crossworker.Client
	HubBridge   HubBridge
	HubStreams  HubStreamer
	// Streams is the worker service.Service wearing its ReleaseLocalStream
	// hat. The router uses it to retire the event subscriptions a local-IPC
	// dispatch leaves behind.
	Streams LocalStreams

	// Delegation pins the per-user bearer slot for the lifetime of every
	// spawn so the last referencing close hits the hub revoke endpoint
	// instead of leaving the row to expire. nil is allowed (tests) and
	// disables the revoke-on-close path.
	Delegation DelegationLifecycle
}

// HubBridge is the subset of hub-side calls the IPC router needs.
//
// **Why an adapter instead of extending `internal/worker/hub/client.go`
// directly?** The plan originally proposed teaching the worker's hub
// client to switch between the worker's own AuthToken and a
// per-user delegation bearer per-call. That would have
// pushed delegation-token plumbing into the long-lived registration /
// channel-handler client whose lifetime is process-scoped. Putting it
// behind a narrow interface here keeps:
//
//  1. The user-scoped path (CallHub here, StreamHub in HubStreamer)
//     separate from the worker-self-scoped registration path. Either
//     side can evolve without touching the other.
//  2. The delegation-bearer minting + caching localised to one
//     adapter (`internal/worker/crossworker/delegation.go`) the IPC
//     Factory wires up at spawn time, instead of leaking through
//     hub.Client's API.
//  3. Tests free to stub the bridge without standing up the real
//     worker→hub registration loop.
//
// The trade-off: anyone reading hub/client.go sees only the
// worker-self-scoped paths. The HubBridge adapter that the spawned
// agent actually goes through lives in this package next to the rest
// of the IPC dispatch, with `hubBridgeAdapter` translating
// `HubClient.CallInner(userID, ...)` calls into `CallHub(userID, ...)`
// delegation-bearer-authenticated requests.
type HubBridge interface {
	CallHub(ctx context.Context, userID userid.UserID, method string, payload []byte) ([]byte, error)
}

// spawnCommon carries the union of fields AgentSpawning and
// TerminalSpawning need to build the per-spawn IPC server. The two
// exported entrypoints project their service-package input into this
// shape so the listen/acquire/cleanup wiring is in one place.
type spawnCommon struct {
	UserID   userid.UserID
	WorkerID string
	// SocketID identifies the SOCKET, and it is always the spawned entity's own id.
	// Kept apart from TabID because a quake terminal advertises ANOTHER tab:
	// keyed on that tab the socket path would collide with the tab's own spawn
	// -- with the agent's socket in the best case (different SocketKind, so
	// merely confusing) and with a terminal tab's socket in the worst, where
	// two live shells would fight over one listener.
	SocketID string
	// TabID is the tab the spawn ADVERTISES as ambient, which every
	// `leapmux control` command resolves through the hub's LocateTab. Empty
	// when there is no such tab; EnvVars then omits LEAPMUX_CONTROL_TAB_ID.
	TabID         string
	TabType       leapmuxv1.TabType
	WorkingDir    string
	AgentProvider string
}

func (f *Factory) spawn(socketKind SocketKind, sc spawnCommon) ([]string, func(), error) {
	// The identity is typed all the way from the channel session, so a blank one
	// is already a compile-time impossibility at every call site. This is the
	// residual zero-value guard, and it is FATAL rather than degrading: a spawn
	// that cannot name its user must not start as nobody. See ErrMissingIdentity.
	if sc.UserID.IsZero() {
		return nil, nil, service.ErrMissingIdentity
	}
	socketURL := DefaultSocketPath(sc.WorkerID, socketKind, sc.SocketID)
	token := MintToken()
	tokenInfo := TokenInfo{
		UserID:        sc.UserID,
		WorkerID:      sc.WorkerID,
		TabID:         sc.TabID,
		TabType:       sc.TabType,
		WorkingDir:    sc.WorkingDir,
		AgentProvider: sc.AgentProvider,
	}
	// Derived from the socket KIND rather than carried as another spawnCommon
	// field, because SocketID already IS the spawned entity's own id -- so for
	// a terminal spawn the two cannot disagree, and an agent spawn cannot set
	// it by mistake. See TokenInfo.TerminalID.
	if socketKind == SocketKindTerminal {
		tokenInfo.TerminalID = sc.SocketID
	}
	router := f.newRouter(sc.UserID)
	srv, err := Listen(Options{
		SocketURL: socketURL,
		Token:     token,
		TokenInfo: tokenInfo,
		Router:    router,
	})
	if err != nil {
		return nil, nil, err
	}
	if f.Delegation != nil {
		f.Delegation.Acquire(sc.UserID)
	}
	cleanup := f.makeCleanup(socketKind, sc, srv)
	return EnvVars(socketURL, token, tokenInfo), cleanup, nil
}

// AgentSpawning satisfies service.ControlIPCFactory.
func (f *Factory) AgentSpawning(info service.AgentSpawnInfo) ([]string, func(), error) {
	return f.spawn(SocketKindAgent, spawnCommon{
		UserID:        info.UserID,
		WorkerID:      info.WorkerID,
		SocketID:      info.TabID,
		TabID:         info.TabID,
		TabType:       leapmuxv1.TabType_TAB_TYPE_AGENT,
		WorkingDir:    info.WorkingDir,
		AgentProvider: info.AgentProvider,
	})
}

// TerminalSpawning satisfies service.ControlIPCFactory.
//
// A terminal TAB advertises itself as the ambient tab. A QUAKE terminal
// advertises NO TAB: LEAPMUX_CONTROL_TAB_ID exists to be resolved through the
// hub's LocateTab, and a quake terminal has no CRDT tab for the hub to find --
// so its own id would resolve to `not_found`, and any OTHER tab in its
// directory would be a target the user never chose. Both env vars are simply
// omitted, and a command that acts on a tab asks for --tab-id.
//
// What the panel keeps is its own identity: LEAPMUX_CONTROL_TERMINAL_ID gives
// the shell (see TokenInfo.TerminalID), and WORKER_ID + WORKING_DIR are what
// `terminal quake ...` needs, so those still run with no flags.
//
// The SOCKET stays keyed on the terminal's own id either way -- see
// spawnCommon.SocketID.
func (f *Factory) TerminalSpawning(info service.TerminalSpawnInfo) ([]string, func(), error) {
	tabID, tabType := info.TabID, leapmuxv1.TabType_TAB_TYPE_TERMINAL
	if info.IsQuake {
		tabID, tabType = "", leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED
	}
	return f.spawn(SocketKindTerminal, spawnCommon{
		UserID:     info.UserID,
		WorkerID:   info.WorkerID,
		SocketID:   info.TabID,
		TabID:      tabID,
		TabType:    tabType,
		WorkingDir: info.WorkingDir,
	})
}

// makeCleanup builds the spawn-teardown function: close the local
// listener, then drop the delegation refcount (which revokes the
// hub-side row when this was the last referencing spawn for the user).
//
// The revoke runs synchronously with a short timeout — failures here
// are non-fatal because the row's TTL bounds the worst-case lifetime,
// but logging makes silent revoke leaks observable.
//
// It closes over the whole spawnCommon rather than the two fields it reads:
// they both come from the same spawn, and re-listing them as bare parameters
// invites a caller to pass one spawn's user with another's tab.
// The attribute NAME comes from the socket kind (see SocketKind.logKey), so an
// agent spawn cannot be logged under "terminal_id". Its VALUE is SocketID, the
// spawn's own entity: a quake terminal's advertised TabID identifies a
// different tab, and logging that under "terminal_id" would attribute the
// teardown to a tab that is still running.
func (f *Factory) makeCleanup(socketKind SocketKind, sc spawnCommon, srv *Server) func() {
	return func() {
		if err := srv.Close(); err != nil {
			slog.Warn("remote IPC close failed", socketKind.logKey(), sc.SocketID, "error", err)
		}
		if f.Delegation != nil {
			ctx, cancel := context.WithTimeout(context.Background(), releaseRevokeTimeout)
			defer cancel()
			if err := f.Delegation.Release(ctx, sc.UserID); err != nil {
				slog.Warn("delegation release failed", socketKind.logKey(), sc.SocketID, "user_id", sc.UserID, "error", err)
			}
		}
	}
}

// HoldDelegation takes one reference on userID's delegation and returns the
// release, so a caller that retires a spawn and mints its replacement keeps the
// reference count off zero across the swap.
//
// Without it that count reaches zero for a user whose only live spawn is the one
// being replaced, and Release revokes the token through a blocking call to the
// hub -- inside the per-tab lifecycle lock -- after which the next call from the
// relaunched tab pays a second round trip to mint a fresh one.
func (f *Factory) HoldDelegation(userID userid.UserID) func() {
	if f.Delegation == nil || userID.IsZero() {
		return func() {}
	}
	f.Delegation.Acquire(userID)
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), releaseRevokeTimeout)
		defer cancel()
		if err := f.Delegation.Release(ctx, userID); err != nil {
			slog.Warn("delegation release failed after a spawn swap", "user_id", userID, "error", err)
		}
	}
}

// newRouter builds a router scoped to userID.
func (f *Factory) newRouter(userID userid.UserID) *Router {
	return &Router{
		WorkerID:        f.WorkerID,
		UserID:          userID,
		LocalDispatcher: dispatcherAdapter{f.Dispatcher},
		CrossWorker:     crossWorkerAdapter{f.CrossWorker},
		Hub:             hubBridgeAdapter{f.HubBridge},
		HubStreams:      f.HubStreams,
		Streams:         f.Streams,
	}
}

// dispatcherAdapter satisfies LocalDispatcher.
type dispatcherAdapter struct{ d *channel.Dispatcher }

func (a dispatcherAdapter) DispatchWith(ctx context.Context, caller channel.Caller, req *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
	if a.d == nil {
		_ = w.SendError(2, "no dispatcher")
		return
	}
	a.d.DispatchWith(ctx, caller, req, w)
}

// crossWorkerAdapter satisfies CrossWorkerClient.
type crossWorkerAdapter struct{ c *crossworker.Client }

func (a crossWorkerAdapter) CallInner(ctx context.Context, targetWorkerID string, userID userid.UserID, method string, payload []byte) ([]byte, error) {
	if a.c == nil {
		return nil, errors.New("cross-worker client not configured")
	}
	return a.c.CallInner(ctx, targetWorkerID, userID, method, payload)
}

func (a crossWorkerAdapter) StreamInner(ctx context.Context, targetWorkerID string, userID userid.UserID, method string, payload []byte, onMsg func(*leapmuxv1.InnerStreamMessage), bindCtrl func(channel.StreamController)) error {
	if a.c == nil {
		return errors.New("cross-worker client not configured")
	}
	return a.c.StreamInner(ctx, targetWorkerID, userID, method, payload, onMsg, bindCtrl)
}

// hubBridgeAdapter satisfies HubClient. The user id is forwarded verbatim --
// it is the whole scope the hub validates on /worker/delegation-tokens/mint.
type hubBridgeAdapter struct{ b HubBridge }

func (a hubBridgeAdapter) CallInner(ctx context.Context, userID userid.UserID, method string, payload []byte) ([]byte, error) {
	if a.b == nil {
		return nil, errors.New("hub bridge not configured")
	}
	return a.b.CallHub(ctx, userID, method, payload)
}
