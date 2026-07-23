package remoteipc

import (
	"context"
	"errors"
	"log/slog"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/crossworker"
	"github.com/leapmux/leapmux/internal/worker/service"
)

// DelegationLifecycle is the worker-side hook the IPC factory uses to
// pin the (user, workspace) delegation-token slot for the lifetime of
// a spawn. Implemented by *crossworker.DelegationStore. Splitting it
// from DelegationProvider keeps the per-call mint API independent
// from the spawn-bookkeeping API and lets the factory be wired with a
// nil lifecycle in tests / minimal configurations without forcing
// crossworker.DelegationStore in.
//
// Acquire carries the spawn's tab identity (tabID, tabType) because
// the hub's mint endpoint validates that the calling worker owns
// (workspace_id, tab_id). The first spawn for a given (user,
// workspace) supplies the provenance tab; concurrent spawns share
// the same cached bearer.
type DelegationLifecycle interface {
	Acquire(userID, workspaceID, tabID string, tabType int32)
	Release(ctx context.Context, userID, workspaceID string) error
}

// revokeRevokeTimeout caps the hub call we make from the cleanup
// goroutine. Spawn cleanups need to return promptly; the delegation
// row will expire on its own if the revoke RPC doesn't land in time.
const releaseRevokeTimeout = 5 * time.Second

// Factory implements service.RemoteIPCFactory by minting a per-agent
// (or per-terminal) socket + token, wiring it into a Router that knows
// how to talk to the local dispatcher, sibling workers via
// crossworker.Client, and the hub via a hub-bound HTTP client.
type Factory struct {
	WorkerID    string
	Dispatcher  *channel.Dispatcher
	CrossWorker *crossworker.Client
	HubBridge   HubBridge
	HubStreams  HubStreamer
	// Authorizers is the worker service.Service wearing its
	// RegisterLocalAuthorizer / ReleaseLocalStream hat. The
	// router uses this to expose the bearer's scope to handlers.
	Authorizers LocalAuthorizers

	// Delegation pins (user, workspace) bearer slots for the lifetime
	// of every spawn so the last referencing close hits the hub
	// revoke endpoint instead of leaving the row to expire. nil is
	// allowed (tests) and disables the revoke-on-close path.
	Delegation DelegationLifecycle
}

// HubBridge is the subset of hub-side calls the IPC router needs.
//
// **Why an adapter instead of extending `internal/worker/hub/client.go`
// directly?** The plan originally proposed teaching the worker's hub
// client to switch between the worker's own AuthToken and a
// per-(user, workspace) delegation bearer per-call. That would have
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
// `HubClient.CallInner(userID, workspaceID, ...)` calls into
// `CallHub(userID, workspaceID, ...)` delegation-bearer-authenticated
// requests.
//
// workspaceID is the delegation scope: tokens are minted for
// (userID, workspaceID), so the router populates it from the IPC
// request's WorkspaceId field, falling back to the spawning agent's
// workspace when callers omit it.
type HubBridge interface {
	CallHub(ctx context.Context, userID, workspaceID, method string, payload []byte) ([]byte, error)
}

// spawnCommon carries the union of fields AgentSpawning and
// TerminalSpawning need to build the per-spawn IPC server. The two
// exported entrypoints project their service-package input into this
// shape so the listen/acquire/cleanup wiring is in one place.
type spawnCommon struct {
	UserID        string
	OrgID         string
	WorkspaceID   string
	WorkerID      string
	TabID         string
	TabType       leapmuxv1.TabType
	WorkingDir    string
	AgentProvider string
}

func (f *Factory) spawn(socketKind SocketKind, spawnKey string, sc spawnCommon) ([]string, func(), error) {
	socketURL := DefaultSocketPath(sc.WorkerID, socketKind, sc.TabID)
	token := MintToken()
	tokenInfo := TokenInfo{
		UserID:        sc.UserID,
		OrgID:         sc.OrgID,
		WorkspaceID:   sc.WorkspaceID,
		WorkerID:      sc.WorkerID,
		TabID:         sc.TabID,
		TabType:       sc.TabType,
		WorkingDir:    sc.WorkingDir,
		AgentProvider: sc.AgentProvider,
	}
	router := f.newRouter(sc.UserID, sc.WorkspaceID)
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
		f.Delegation.Acquire(sc.UserID, sc.WorkspaceID, sc.TabID, int32(sc.TabType))
	}
	cleanup := f.makeCleanup(spawnKey, sc.TabID, sc.UserID, sc.WorkspaceID, srv)
	return EnvVars(socketURL, token, tokenInfo), cleanup, nil
}

// AgentSpawning satisfies service.RemoteIPCFactory.
func (f *Factory) AgentSpawning(info service.AgentSpawnInfo) ([]string, func(), error) {
	return f.spawn(SocketKindAgent, "agent_id", spawnCommon{
		UserID:        info.UserID,
		OrgID:         info.OrgID,
		WorkspaceID:   info.WorkspaceID,
		WorkerID:      info.WorkerID,
		TabID:         info.TabID,
		TabType:       leapmuxv1.TabType_TAB_TYPE_AGENT,
		WorkingDir:    info.WorkingDir,
		AgentProvider: info.AgentProvider,
	})
}

// TerminalSpawning satisfies service.RemoteIPCFactory.
func (f *Factory) TerminalSpawning(info service.TerminalSpawnInfo) ([]string, func(), error) {
	return f.spawn(SocketKindTerminal, "terminal_id", spawnCommon{
		UserID:      info.UserID,
		OrgID:       info.OrgID,
		WorkspaceID: info.WorkspaceID,
		WorkerID:    info.WorkerID,
		TabID:       info.TabID,
		TabType:     leapmuxv1.TabType_TAB_TYPE_TERMINAL,
		WorkingDir:  info.WorkingDir,
	})
}

// makeCleanup builds the spawn-teardown function: close the local
// listener, then drop the delegation refcount (which revokes the
// hub-side row when this was the last referencing spawn for the
// (user, workspace) pair).
//
// The revoke runs synchronously with a short timeout — failures here
// are non-fatal because the row's TTL bounds the worst-case lifetime,
// but logging makes silent revoke leaks observable.
func (f *Factory) makeCleanup(spawnKey, spawnID, userID, workspaceID string, srv *Server) func() {
	return func() {
		if err := srv.Close(); err != nil {
			slog.Warn("remote IPC close failed", spawnKey, spawnID, "error", err)
		}
		if f.Delegation != nil {
			ctx, cancel := context.WithTimeout(context.Background(), releaseRevokeTimeout)
			defer cancel()
			if err := f.Delegation.Release(ctx, userID, workspaceID); err != nil {
				slog.Warn("delegation release failed", spawnKey, spawnID, "user_id", userID, "workspace_id", workspaceID, "error", err)
			}
		}
	}
}

// newRouter builds a router scoped to (userID, workspaceID).
func (f *Factory) newRouter(userID, workspaceID string) *Router {
	return &Router{
		WorkerID:        f.WorkerID,
		UserID:          userID,
		WorkspaceIDs:    []string{workspaceID},
		LocalDispatcher: dispatcherAdapter{f.Dispatcher},
		CrossWorker:     crossWorkerAdapter{f.CrossWorker},
		Hub:             hubBridgeAdapter{f.HubBridge},
		HubStreams:      f.HubStreams,
		Authorizers:     f.Authorizers,
		WorkspaceFilter: func(id string) bool { return id == "" || id == workspaceID },
	}
}

// dispatcherAdapter satisfies LocalDispatcher.
type dispatcherAdapter struct{ d *channel.Dispatcher }

func (a dispatcherAdapter) DispatchWith(ctx context.Context, userID string, req *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
	if a.d == nil {
		_ = w.SendError(2, "no dispatcher")
		return
	}
	a.d.DispatchWith(ctx, userID, req, w)
}

// crossWorkerAdapter satisfies CrossWorkerClient.
type crossWorkerAdapter struct{ c *crossworker.Client }

func (a crossWorkerAdapter) CallInner(ctx context.Context, targetWorkerID, userID, workspaceID, method string, payload []byte) ([]byte, error) {
	if a.c == nil {
		return nil, errors.New("cross-worker client not configured")
	}
	return a.c.CallInner(ctx, targetWorkerID, userID, workspaceID, method, payload)
}

func (a crossWorkerAdapter) StreamInner(ctx context.Context, targetWorkerID, userID, workspaceID, method string, payload []byte, onMsg func(*leapmuxv1.InnerStreamMessage)) error {
	if a.c == nil {
		return errors.New("cross-worker client not configured")
	}
	return a.c.StreamInner(ctx, targetWorkerID, userID, workspaceID, method, payload, onMsg)
}

// hubBridgeAdapter satisfies HubClient. The (userID, workspaceID)
// pair is forwarded verbatim — the bridge needs both to mint the
// correct delegation-token bearer (`(user_id, workspace_id)` is the
// scope the hub validates on /worker/delegation-tokens/mint).
type hubBridgeAdapter struct{ b HubBridge }

func (a hubBridgeAdapter) CallInner(ctx context.Context, userID, workspaceID, method string, payload []byte) ([]byte, error) {
	if a.b == nil {
		return nil, errors.New("hub bridge not configured")
	}
	return a.b.CallHub(ctx, userID, workspaceID, method, payload)
}
