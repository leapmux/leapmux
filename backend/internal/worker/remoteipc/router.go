package remoteipc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// LocalDispatcher is the subset of channel.Dispatcher the router needs.
// Mirrors the existing dispatcher's DispatchWith signature so the
// router can inject local-IPC ResponseWriters.
type LocalDispatcher interface {
	DispatchWith(userID string, req *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter)
}

// CrossWorkerClient sends a unary inner RPC to a sibling worker via the
// hub's E2EE channel relay. The implementation lives in
// internal/worker/crossworker.
//
// workspaceID is the delegation-bearer scope: it both keys the channel
// pool and feeds the mint request, so the same (target, user) pair on
// a different workspace gets a fresh Noise session.
type CrossWorkerClient interface {
	CallInner(ctx context.Context, targetWorkerID, userID, workspaceID, method string, payload []byte) ([]byte, error)
	StreamInner(ctx context.Context, targetWorkerID, userID, workspaceID, method string, payload []byte, onMsg func(*leapmuxv1.InnerStreamMessage)) error
}

// HubClient is the subset of the worker's hub-bound client the router
// uses. Lets the router make user-scoped calls to hub services on
// behalf of the spawning user (with a delegation token, when minted).
//
// workspaceID is the delegation scope. The router fills it from the
// IPC request's WorkspaceId, falling back to the spawning agent's
// workspace when callers leave it blank — a delegation token's scope
// is `(user_id, workspace_id)` so the bridge always needs a concrete
// workspace to mint against.
type HubClient interface {
	CallInner(ctx context.Context, userID, workspaceID, method string, payload []byte) ([]byte, error)
}

// HubStreamer forwards a server-streaming hub RPC. The implementation
// authenticates with a delegation-token bearer minted for the spawned
// agent's user/workspace pair. payload is a marshalled request proto;
// onPayload receives marshalled response protos.
type HubStreamer interface {
	StreamHub(ctx context.Context, userID, method string, payload []byte, onPayload func([]byte) error) error
}

// LocalAuthorizers is the subset of service.Context the router uses to
// stash a per-stream WorkspaceAuthorizer that worker handlers consult
// instead of the channelmgr lookup.
type LocalAuthorizers interface {
	RegisterLocalAuthorizer(streamID string, workspaceIDs []string)
	UnregisterLocalAuthorizer(streamID string)
}

// Router dispatches local-IPC requests to the appropriate backend.
//
// Method names are namespaced:
//   - "worker.<Name>": the local worker's inner-RPC dispatcher (or a
//     cross-worker channel when target_worker_id ≠ this worker).
//   - "hub.<Service>/<Method>": the hub-bound client.
type Router struct {
	WorkerID        string
	UserID          string
	WorkspaceIDs    []string
	LocalDispatcher LocalDispatcher
	CrossWorker     CrossWorkerClient
	Hub             HubClient
	HubStreams      HubStreamer
	Authorizers     LocalAuthorizers
	WorkspaceFilter func(workspaceID string) bool
	// Now overrides time.Now for tests that want to advance the
	// SweepStaleCancellers clock without sleeping. Defaults to
	// time.Now when nil.
	Now func() time.Time
	// StreamCancellers maps the IPC stream's client_request_id to a
	// streamCancelEntry. Entries are stored on stream registration
	// and deleted via defer on stream exit — but a panicking handler
	// or a partial teardown can leave an entry behind. The Server
	// janitor calls SweepStaleCancellers periodically to bound the
	// worst-case lifetime of an orphaned cancel function.
	StreamCancellers sync.Map // string → streamCancelEntry
}

// streamCancelEntry pairs a stream's cancel function with the time it
// was registered so the defense-in-depth sweep can drop entries left
// behind by abnormal teardowns.
type streamCancelEntry struct {
	cancel       context.CancelFunc
	registeredAt time.Time
}

func (r *Router) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// localStreamIDPrefix mirrors service.LocalIPCStreamPrefix without the
// service-package import (avoids router→service coupling in the hot
// path). Keep these in sync.
const localStreamIDPrefix = "localipc:"

// CallInner executes a unary inner-RPC. workspaceID, when non-empty,
// is checked against the bearer's scope.
func (r *Router) CallInner(ctx context.Context, info TokenInfo, method string, payload []byte, targetWorkerID, workspaceID string) (*leapmuxv1.CallInnerResponse, error) {
	if workspaceID != "" && r.WorkspaceFilter != nil && !r.WorkspaceFilter(workspaceID) {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("workspace not in scope"))
	}
	switch ns := namespaceOf(method); ns {
	case namespaceWorker:
		bare := stripNamespace(method)
		if targetWorkerID == "" || targetWorkerID == r.WorkerID {
			return r.dispatchLocal(info, bare, payload), nil
		}
		if r.CrossWorker == nil {
			return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("cross-worker client not configured"))
		}
		out, err := r.CrossWorker.CallInner(ctx, targetWorkerID, r.UserID, r.scopeWorkspaceID(workspaceID), bare, payload)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		return &leapmuxv1.CallInnerResponse{Payload: out}, nil
	case namespaceHub:
		if r.Hub == nil {
			return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("hub client not configured"))
		}
		out, err := r.Hub.CallInner(ctx, r.UserID, r.scopeWorkspaceID(workspaceID), stripNamespace(method), payload)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		return &leapmuxv1.CallInnerResponse{Payload: out}, nil
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown method namespace: %s", method))
	}
}

// withLocalAuthorizer registers a per-request synthetic stream id with
// the authorizer registry, runs fn, then unregisters. Pairs the
// dispatchLocal / streamLocal lifecycle in one place so both call
// sites can't drift on register/unregister symmetry.
func (r *Router) withLocalAuthorizer(info TokenInfo, fn func(streamID string)) {
	streamID := newLocalStreamID(info)
	if r.Authorizers != nil {
		r.Authorizers.RegisterLocalAuthorizer(streamID, r.WorkspaceIDs)
		defer r.Authorizers.UnregisterLocalAuthorizer(streamID)
	}
	fn(streamID)
}

// dispatchLocal runs a same-worker inner-RPC and synchronously
// collects the response. Streams aren't expected here — StreamInner
// handles that path.
func (r *Router) dispatchLocal(info TokenInfo, method string, payload []byte) *leapmuxv1.CallInnerResponse {
	if r.LocalDispatcher == nil {
		return &leapmuxv1.CallInnerResponse{
			IsError:      true,
			ErrorCode:    int32(codes.Unimplemented),
			ErrorMessage: "local dispatcher not configured",
		}
	}
	collector := &responseCollector{}
	r.withLocalAuthorizer(info, func(streamID string) {
		collector.streamID = streamID
		r.LocalDispatcher.DispatchWith(r.UserID, &leapmuxv1.InnerRpcRequest{Method: method, Payload: payload}, collector)
	})
	return collector.toResponse()
}

// StreamInner runs a server-streaming inner RPC.
func (r *Router) StreamInner(ctx context.Context, info TokenInfo, method string, payload []byte, targetWorkerID, workspaceID, clientReqID string, onMsg func(*leapmuxv1.StreamInnerEnvelope) error) error {
	if workspaceID != "" && r.WorkspaceFilter != nil && !r.WorkspaceFilter(workspaceID) {
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("workspace not in scope"))
	}
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if clientReqID != "" {
		r.StreamCancellers.Store(clientReqID, streamCancelEntry{cancel: cancel, registeredAt: r.now()})
		defer r.StreamCancellers.Delete(clientReqID)
	}

	switch ns := namespaceOf(method); ns {
	case namespaceWorker:
		bare := stripNamespace(method)
		if targetWorkerID == "" || targetWorkerID == r.WorkerID {
			return r.streamLocal(streamCtx, info, bare, payload, onMsg)
		}
		if r.CrossWorker == nil {
			return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("cross-worker client not configured"))
		}
		return r.CrossWorker.StreamInner(streamCtx, targetWorkerID, r.UserID, r.scopeWorkspaceID(workspaceID), bare, payload, func(m *leapmuxv1.InnerStreamMessage) {
			_ = onMsg(&leapmuxv1.StreamInnerEnvelope{
				Payload:      m.GetPayload(),
				End:          m.GetEnd(),
				IsError:      m.GetIsError(),
				ErrorMessage: m.GetErrorMessage(),
				ErrorCode:    m.GetErrorCode(),
			})
		})
	case namespaceHub:
		if r.HubStreams == nil {
			return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("hub streamer not configured"))
		}
		bare := stripNamespace(method)
		return r.HubStreams.StreamHub(streamCtx, r.UserID, bare, payload, func(p []byte) error {
			return onMsg(&leapmuxv1.StreamInnerEnvelope{Payload: p})
		})
	default:
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown method namespace: %s", method))
	}
}

func (r *Router) streamLocal(ctx context.Context, info TokenInfo, method string, payload []byte, onMsg func(*leapmuxv1.StreamInnerEnvelope) error) error {
	if r.LocalDispatcher == nil {
		return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("local dispatcher not configured"))
	}
	var collector *streamCollector
	r.withLocalAuthorizer(info, func(streamID string) {
		collector = newStreamCollector(ctx, streamID, onMsg)
		r.LocalDispatcher.DispatchWith(r.UserID, &leapmuxv1.InnerRpcRequest{Method: method, Payload: payload}, collector)
		collector.wait()
	})
	return collector.err
}

// scopeWorkspaceID picks the delegation-bearer scope for hub-bound
// calls: prefer the IPC request's workspace_id (callers can target a
// sibling workspace they have access to), otherwise fall back to the
// spawning agent's workspace. Empty when neither is available — the
// bridge will surface a clear "missing workspace" error.
func (r *Router) scopeWorkspaceID(requestWorkspaceID string) string {
	if requestWorkspaceID != "" {
		return requestWorkspaceID
	}
	if len(r.WorkspaceIDs) > 0 {
		return r.WorkspaceIDs[0]
	}
	return ""
}

// CancelStream cancels an active stream by client_request_id.
func (r *Router) CancelStream(clientReqID string) {
	if v, ok := r.StreamCancellers.LoadAndDelete(clientReqID); ok {
		if entry, ok := v.(streamCancelEntry); ok {
			entry.cancel()
		}
	}
}

// SweepStaleCancellers drops StreamCancellers entries whose
// registeredAt is before `cutoff`, invoking each cancel function so a
// dangling stream goroutine gets a context-cancellation signal on its
// way out. Defense-in-depth pass: the canonical lifecycle is Store +
// defer Delete inside StreamInner, which under healthy operation keeps
// the map bounded by the number of in-flight streams. The sweep
// catches entries that survived an abnormal teardown.
func (r *Router) SweepStaleCancellers(cutoff time.Time) int {
	dropped := 0
	r.StreamCancellers.Range(func(key, value any) bool {
		entry, ok := value.(streamCancelEntry)
		if !ok {
			return true
		}
		if entry.registeredAt.Before(cutoff) {
			r.StreamCancellers.Delete(key)
			entry.cancel()
			dropped++
		}
		return true
	})
	return dropped
}

// --- ResponseWriter implementations ---

// responseCollector is a one-shot ResponseWriter for unary calls.
type responseCollector struct {
	streamID string
	mu       sync.Mutex
	resp     *leapmuxv1.InnerRpcResponse
	errSent  *struct {
		code int32
		msg  string
	}
}

func (c *responseCollector) SendResponse(resp *leapmuxv1.InnerRpcResponse) error {
	c.mu.Lock()
	c.resp = resp
	c.mu.Unlock()
	return nil
}

func (c *responseCollector) SendError(code int32, msg string) error {
	c.mu.Lock()
	c.errSent = &struct {
		code int32
		msg  string
	}{code: code, msg: msg}
	c.mu.Unlock()
	return nil
}

func (c *responseCollector) SendStream(*leapmuxv1.InnerStreamMessage) error {
	return errors.New("unary call cannot stream")
}

func (c *responseCollector) ChannelID() string { return c.streamID }

func (c *responseCollector) toResponse() *leapmuxv1.CallInnerResponse {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.errSent != nil {
		return &leapmuxv1.CallInnerResponse{
			IsError:      true,
			ErrorCode:    c.errSent.code,
			ErrorMessage: c.errSent.msg,
		}
	}
	if c.resp == nil {
		return &leapmuxv1.CallInnerResponse{}
	}
	return &leapmuxv1.CallInnerResponse{
		Payload:      c.resp.GetPayload(),
		IsError:      c.resp.GetIsError(),
		ErrorCode:    c.resp.GetErrorCode(),
		ErrorMessage: c.resp.GetErrorMessage(),
	}
}

// streamCollector adapts SendStream calls into onMsg invocations and
// blocks until the handler emits a non-streaming terminal response or
// the ctx is cancelled. err writes happen inside finish() (CAS-gated
// to a single writer) and the reader only runs after wait() returns,
// which observes `close(c.done)` — so the close-of-done provides the
// happens-before edge and no mutex is needed.
type streamCollector struct {
	ctx      context.Context
	onMsg    func(*leapmuxv1.StreamInnerEnvelope) error
	streamID string

	finished atomic.Bool
	done     chan struct{}
	err      error
}

func newStreamCollector(ctx context.Context, streamID string, onMsg func(*leapmuxv1.StreamInnerEnvelope) error) *streamCollector {
	return &streamCollector{
		ctx:      ctx,
		streamID: streamID,
		onMsg:    onMsg,
		done:     make(chan struct{}),
	}
}

// finish marks the collector terminal. Returns true when the caller
// is the first writer to reach this state — callers that observe
// false MUST NOT touch c.err (someone else owns it now). Closes done
// so wait() can unblock without waiting for ctx cancellation.
func (c *streamCollector) finish() bool {
	if !c.finished.CompareAndSwap(false, true) {
		return false
	}
	close(c.done)
	return true
}

func (c *streamCollector) SendResponse(resp *leapmuxv1.InnerRpcResponse) error {
	if !c.finish() {
		return nil
	}
	if resp != nil && resp.GetIsError() {
		c.err = fmt.Errorf("rpc error: %s", resp.GetErrorMessage())
	}
	return nil
}

func (c *streamCollector) SendError(code int32, msg string) error {
	if !c.finish() {
		return nil
	}
	c.err = fmt.Errorf("rpc error %d: %s", code, msg)
	return nil
}

func (c *streamCollector) SendStream(m *leapmuxv1.InnerStreamMessage) error {
	if c.ctx.Err() != nil {
		return c.ctx.Err()
	}
	return c.onMsg(&leapmuxv1.StreamInnerEnvelope{
		Payload:      m.GetPayload(),
		End:          m.GetEnd(),
		IsError:      m.GetIsError(),
		ErrorMessage: m.GetErrorMessage(),
		ErrorCode:    m.GetErrorCode(),
	})
}

func (c *streamCollector) ChannelID() string { return c.streamID }

// wait blocks until either the handler signals completion (via
// SendResponse / SendError → finish) or the request context is
// cancelled. Observing finish lets us release the authorizer
// registration as soon as the handler returns, even if the gRPC
// client hasn't closed the stream yet.
func (c *streamCollector) wait() {
	select {
	case <-c.done:
	case <-c.ctx.Done():
	}
}

// --- Method namespacing helpers ---

const (
	namespaceWorker = "worker"
	namespaceHub    = "hub"
)

func namespaceOf(method string) string {
	if i := strings.IndexByte(method, '.'); i > 0 {
		return method[:i]
	}
	return ""
}

func stripNamespace(method string) string {
	if i := strings.IndexByte(method, '.'); i > 0 {
		return method[i+1:]
	}
	return method
}

// newLocalStreamID returns the synthetic stream identity used to key
// per-stream state inside the worker handlers (e.g. the WatchEvents
// watcher cleanup map). The shape is `localipc:<token-id>:<request-id>`
// — token-id is stable for the lifetime of one spawned-process bearer
// (so multiple streams from the same agent share a prefix and log
// correlation works), and request-id is a fresh nanoid per call so
// each stream has its own row in the watcher map.
//
// Plan reference: "Wire the LocalIPCAuthorizer to provide a stable
// synthetic ID for the lifetime of each ConnectRPC server-streaming
// RPC" — using the agent/terminal id satisfies "stable for the
// bearer" while the per-request suffix keeps every WatchEvents
// registration distinct.
func newLocalStreamID(info TokenInfo) string {
	return localStreamIDPrefix + tokenIdentitySegment(info) + ":" + id.Generate()
}

// tokenIdentitySegment derives a stable, non-empty identifier from a
// TokenInfo. The tab id is preferred (one per spawn, prefixed with
// tab type for readability); fallback is the user id; final fallback
// "anon" never trips because the auth layer always sets at least
// UserID, but is defensive.
func tokenIdentitySegment(info TokenInfo) string {
	switch {
	case info.TabID != "" && info.TabType != leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED:
		return tabTypeWireName(info.TabType) + "-" + info.TabID
	case info.TabID != "":
		return "tab-" + info.TabID
	case info.UserID != "":
		return "user-" + info.UserID
	default:
		return "anon"
	}
}
