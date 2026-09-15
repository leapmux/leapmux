package controlipc

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
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/service"
)

// LocalDispatcher exposes the dispatcher operation that accepts a local inter-process communication (IPC) response writer.
type LocalDispatcher interface {
	DispatchWith(ctx context.Context, caller channel.Caller, req *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter)
}

// CrossWorkerClient sends remote procedure calls (RPCs) through the hub's end-to-end encrypted channel relay.
// The delegation bearer applies to one user and minting worker. The channel pool key contains the target and user.
// StreamInner supplies a controller for revisions and cancellation on the dedicated stream channel.
type CrossWorkerClient interface {
	CallInner(ctx context.Context, targetWorkerID string, userID userid.UserID, method string, payload []byte) ([]byte, error)
	StreamInner(ctx context.Context, targetWorkerID string, userID userid.UserID, method string, payload []byte, onMsg func(*leapmuxv1.InnerStreamMessage), bindCtrl func(channel.StreamController)) error
}

// HubClient sends hub requests for the spawning user with a delegation token.
// The hub enforces the token's grant through auth.CeilingFor. A channel.Caller grant here would add an unenforced second authority.
type HubClient interface {
	CallInner(ctx context.Context, userID userid.UserID, method string, payload []byte) ([]byte, error)
}

// HubStreamer forwards a server-streaming hub RPC with the spawning user's delegation token.
// payload contains the serialized request protobuf. onPayload receives serialized response protobufs.
type HubStreamer interface {
	StreamHub(ctx context.Context, userID userid.UserID, method string, payload []byte, onPayload func([]byte) error) error
}

// LocalStreams releases subscriptions for a synthetic local stream ID.
// Local stream IDs do not reach the channel manager's close callback. ReleaseLocalStream supplies their cleanup.
type LocalStreams interface {
	ReleaseLocalStream(streamID string)
}

// Router dispatches local-IPC requests to the appropriate backend.
//
// Method names are namespaced:
//   - "worker.<Name>": the local worker's inner-RPC dispatcher (or a
//     cross-worker channel when target_worker_id ≠ this worker).
//   - "hub.<Service>/<Method>": the hub-bound client.
type Router struct {
	WorkerID        string
	UserID          userid.UserID
	LocalDispatcher LocalDispatcher
	CrossWorker     CrossWorkerClient
	Hub             HubClient
	HubStreams      HubStreamer
	Streams         LocalStreams
	// Now overrides time.Now for tests that want to advance the
	// SweepStaleCancellers clock without sleeping. Defaults to
	// time.Now when nil.
	Now func() time.Time
	// StreamCancellers maps the IPC stream's client_request_id to a
	// streamCancelEntry. Entries are stored on stream registration
	// and deleted via defer on stream exit — but a panicking handler
	// or a partial teardown can leave an entry behind. The Server
	// janitor calls SweepStaleCancellers periodically to limit the
	// worst-case lifetime of an orphaned cancel function.
	StreamCancellers sync.Map // string → *streamCancelEntry
}

// streamCancelEntry retains frames before binding and cancels its stream context on retirement.
type streamCancelEntry struct {
	cancel       context.CancelFunc
	registeredAt time.Time
	inbox        channel.StreamInbox
}

func (r *Router) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// CallInner executes a unary inner-RPC.
func (r *Router) CallInner(ctx context.Context, info TokenInfo, method string, payload []byte, targetWorkerID string) (*leapmuxv1.CallInnerResponse, error) {
	switch ns := namespaceOf(method); ns {
	case namespaceWorker:
		bare := stripNamespace(method)
		if targetWorkerID == "" || targetWorkerID == r.WorkerID {
			return r.dispatchLocal(ctx, info, bare, payload), nil
		}
		if r.CrossWorker == nil {
			return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("cross-worker client not configured"))
		}
		out, err := r.CrossWorker.CallInner(ctx, targetWorkerID, r.UserID, bare, payload)
		if err != nil {
			return nil, relayError(err)
		}
		return &leapmuxv1.CallInnerResponse{Payload: out}, nil
	case namespaceHub:
		if r.Hub == nil {
			return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("hub client not configured"))
		}
		out, err := r.Hub.CallInner(ctx, r.UserID, stripNamespace(method), payload)
		if err != nil {
			return nil, relayError(err)
		}
		return &leapmuxv1.CallInnerResponse{Payload: out}, nil
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown method namespace: %s", method))
	}
}

// relayError preserves Connect error codes from the hub or a sibling worker.
// Callers use these codes to distinguish missing resources from unavailable workers.
// These failures carry a nil response. They do not use dispatchLocal's gRPC ErrorCode field.
func relayError(err error) error {
	// Preserve upstream codes. A plain error has CodeUnknown and maps to CodeInternal.
	if code := connect.CodeOf(err); code != connect.CodeUnknown {
		return connect.NewError(code, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

// withLocalStream creates one synthetic stream ID and releases its subscriptions after dispatch.
// Unary and streaming local calls share this lifecycle.
func (r *Router) withLocalStream(info TokenInfo, fn func(streamID string)) {
	streamID := newLocalStreamID(info)
	if r.Streams != nil {
		defer r.Streams.ReleaseLocalStream(streamID)
	}
	fn(streamID)
}

// dispatchLocal collects a same-worker unary response synchronously.
// The handler receives the caller's context, so cancellation stops subprocesses that use exec.CommandContext.
func (r *Router) dispatchLocal(ctx context.Context, info TokenInfo, method string, payload []byte) *leapmuxv1.CallInnerResponse {
	if r.LocalDispatcher == nil {
		return &leapmuxv1.CallInnerResponse{
			IsError:      true,
			ErrorCode:    int32(codes.Unimplemented),
			ErrorMessage: "local dispatcher not configured",
		}
	}
	collector := &responseCollector{}
	r.withLocalStream(info, func(streamID string) {
		collector.streamID = streamID
		r.LocalDispatcher.DispatchWith(ctx, r.caller(), &leapmuxv1.InnerRpcRequest{Method: method, Payload: payload}, collector)
	})
	return collector.toResponse()
}

// StreamInner runs a server-streaming inner RPC.
func (r *Router) StreamInner(ctx context.Context, info TokenInfo, method string, payload []byte, targetWorkerID, clientReqID string, onMsg func(*leapmuxv1.StreamInnerEnvelope) error) error {
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var entry *streamCancelEntry
	if clientReqID != "" {
		entry = &streamCancelEntry{cancel: cancel, registeredAt: r.now()}
		if previous, loaded := r.StreamCancellers.Swap(clientReqID, entry); loaded {
			if old, ok := previous.(*streamCancelEntry); ok {
				old.inbox.Cancel()
				old.cancel()
			}
		}
		defer func() {
			r.StreamCancellers.CompareAndDelete(clientReqID, entry)
			entry.inbox.Cancel()
		}()
	}

	switch ns := namespaceOf(method); ns {
	case namespaceWorker:
		bare := stripNamespace(method)
		if targetWorkerID == "" || targetWorkerID == r.WorkerID {
			return r.streamLocal(streamCtx, info, bare, payload, entry, onMsg)
		}
		if r.CrossWorker == nil {
			return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("cross-worker client not configured"))
		}
		return r.CrossWorker.StreamInner(streamCtx, targetWorkerID, r.UserID, bare, payload, func(m *leapmuxv1.InnerStreamMessage) {
			_ = onMsg(&leapmuxv1.StreamInnerEnvelope{
				Payload:      m.GetPayload(),
				End:          m.GetEnd(),
				IsError:      m.GetIsError(),
				ErrorMessage: m.GetErrorMessage(),
				ErrorCode:    m.GetErrorCode(),
			})
		}, func(ctrl channel.StreamController) {
			if !entry.bind(ctrl) {
				ctrl.OnCancel()
			}
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

func (r *Router) streamLocal(ctx context.Context, info TokenInfo, method string, payload []byte, entry *streamCancelEntry, onMsg func(*leapmuxv1.StreamInnerEnvelope) error) error {
	if r.LocalDispatcher == nil {
		return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("local dispatcher not configured"))
	}
	var collector *streamCollector
	r.withLocalStream(info, func(streamID string) {
		collector = newStreamCollector(ctx, streamID, onMsg)
		collector.entry = entry
		r.LocalDispatcher.DispatchWith(ctx, r.caller(), &leapmuxv1.InnerRpcRequest{Method: method, Payload: payload}, collector)
		collector.wait()
	})
	return collector.outcome()
}

// bind uses the entry that this stream created. A replacement owns a different inbox.
// StreamInbox serializes binding and cancellation. A refused controller needs its own cleanup.
func (entry *streamCancelEntry) bind(ctrl channel.StreamController) bool {
	return entry != nil && entry.inbox.Bind(ctrl)
}

// UpdateStream retains a revision until its controller is ready.
//
// An inbox that overflows CANCELS the stream, and the caller reports
// ResourceExhausted. Dropping the frame instead would leave the client believing
// its revision landed, and reading the wrong events for as long as the stream
// lives. The cancel is what makes the loss visible: the client's handle closes,
// and its next update opens a fresh stream that carries the current interest.
func (r *Router) UpdateStream(clientReqID string, payload []byte) error {
	if clientReqID == "" {
		return nil
	}
	if value, ok := r.StreamCancellers.Load(clientReqID); ok {
		if entry, ok := value.(*streamCancelEntry); ok {
			if err := entry.inbox.Deliver(payload); err != nil {
				entry.cancel()
				entry.inbox.Cancel()
				return err
			}
		}
	}
	return nil
}

// CancelStream cancels an active stream by client_request_id.
func (r *Router) CancelStream(clientReqID string) {
	if v, ok := r.StreamCancellers.LoadAndDelete(clientReqID); ok {
		if entry, ok := v.(*streamCancelEntry); ok {
			entry.inbox.Cancel()
			entry.cancel()
		}
	}
}

// SweepStaleCancellers cancels entries that predate cutoff and still occupy the same map slot.
// StreamInner uses Swap for registration and CompareAndDelete for cleanup. The sweep removes entries that survive abnormal cleanup.
func (r *Router) SweepStaleCancellers(cutoff time.Time) int {
	dropped := 0
	r.StreamCancellers.Range(func(key, value any) bool {
		entry, ok := value.(*streamCancelEntry)
		if !ok {
			return true
		}
		if entry.registeredAt.Before(cutoff) && r.StreamCancellers.CompareAndDelete(key, entry) {
			entry.inbox.Cancel()
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

func (c *responseCollector) ChannelID() string   { return c.streamID }
func (*responseCollector) MaxPayloadBudget() int { return 0 }
func (*responseCollector) BindStream(channel.StreamController) (func(), bool) {
	return func() {}, false
}

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

// streamCollector forwards stream frames and waits for completion or context cancellation.
// claim gives one caller ownership of the result. settle records that result before it releases wait.
// The result mutex also protects readers that return through context cancellation before settle finishes.
type streamCollector struct {
	ctx      context.Context
	onMsg    func(*leapmuxv1.StreamInnerEnvelope) error
	streamID string
	// Retain the original entry even if another stream reuses its request ID.
	entry *streamCancelEntry

	finished atomic.Bool
	done     chan struct{}

	mu  sync.Mutex
	err error
}

func newStreamCollector(ctx context.Context, streamID string, onMsg func(*leapmuxv1.StreamInnerEnvelope) error) *streamCollector {
	return &streamCollector{
		ctx:      ctx,
		streamID: streamID,
		onMsg:    onMsg,
		done:     make(chan struct{}),
	}
}

// claim gives the first caller sole ownership of the result.
// That caller must call settle exactly once. claim alone does not release wait.
func (c *streamCollector) claim() bool {
	return c.finished.CompareAndSwap(false, true)
}

// settle records the final error, or nil for success, and releases wait.
// Only the caller that succeeds at claim can call settle.
func (c *streamCollector) settle(err error) {
	c.mu.Lock()
	c.err = err
	c.mu.Unlock()
	close(c.done)
}

// outcome returns the recorded error. It returns nil if cancellation precedes a final result.
func (c *streamCollector) outcome() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *streamCollector) SendResponse(resp *leapmuxv1.InnerRpcResponse) error {
	if !c.claim() {
		return nil
	}
	if resp == nil {
		c.settle(nil)
		return nil
	}
	if resp.GetIsError() {
		c.settle(fmt.Errorf("rpc error: %s", resp.GetErrorMessage()))
		return nil
	}
	// SendResponse can complete a stream with a final payload. Forward that payload before completion.
	// An empty response carries only the completion signal.
	var outcome error
	if len(resp.GetPayload()) > 0 {
		outcome = c.onMsg(&leapmuxv1.StreamInnerEnvelope{
			Payload: resp.GetPayload(),
			End:     true,
		})
	}
	c.settle(outcome)
	return nil
}

func (c *streamCollector) SendError(code int32, msg string) error {
	if !c.claim() {
		return nil
	}
	c.settle(fmt.Errorf("rpc error %d: %s", code, msg))
	return nil
}

func (c *streamCollector) SendStream(m *leapmuxv1.InnerStreamMessage) error {
	if c.ctx.Err() != nil {
		return c.ctx.Err()
	}
	err := c.onMsg(&leapmuxv1.StreamInnerEnvelope{
		Payload:      m.GetPayload(),
		End:          m.GetEnd(),
		IsError:      m.GetIsError(),
		ErrorMessage: m.GetErrorMessage(),
		ErrorCode:    m.GetErrorCode(),
	})

	// End and error frames complete the collector, as SendResponse and SendError do.
	if m.GetEnd() || m.GetIsError() {
		if c.claim() {
			// Preserve delivery failures on a clean End. An explicit provider error takes precedence.
			outcome := err
			if m.GetIsError() {
				outcome = fmt.Errorf("rpc error %d: %s", m.GetErrorCode(), m.GetErrorMessage())
			}
			c.settle(outcome)
		}
	}
	return err
}

func (c *streamCollector) ChannelID() string   { return c.streamID }
func (*streamCollector) MaxPayloadBudget() int { return 0 }

// BindStream installs the controller on this stream's original entry.
// If binding fails, the handler must cancel its context and release its ownership.
func (c *streamCollector) BindStream(ctrl channel.StreamController) (release func(), ok bool) {
	if !c.entry.bind(ctrl) {
		return func() {}, false
	}
	// Capture this inbox so an old release cannot alter a replacement stream.
	return c.entry.inbox.Release, true
}

// wait blocks until the handler settles its result or the request context ends.
// withLocalStream then releases the subscriptions without waiting for the client to close the connection.
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
// The id has to be stable for the lifetime of one server-streaming RPC and
// distinct per call: the agent/terminal id gives the first, and the
// per-request suffix keeps every WatchEvents registration its own row.
func newLocalStreamID(info TokenInfo) string {
	return service.LocalIPCStreamPrefix + tokenIdentitySegment(info) + ":" + id.Generate()
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
	case !info.UserID.IsZero():
		return "user-" + info.UserID.String()
	default:
		return "anon"
	}
}

// caller is the identity AND authority every LOCAL dispatch runs under.
//
// It is UNSCOPED, and this is the one site that legitimately is: the socket is
// reachable by a process on this machine running as the worker's own user,
// which is already the authority every scope subdivides. Saying so explicitly
// rather than leaving the zero value is what keeps every other construction
// fail-closed -- see channel.LocalAgentCaller.
func (r *Router) caller() channel.Caller {
	return channel.LocalAgentCaller(r.UserID)
}
