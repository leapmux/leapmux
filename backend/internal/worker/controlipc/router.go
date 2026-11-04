package controlipc

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"

	"connectrpc.com/connect/v2"
	"google.golang.org/grpc/codes"

	"github.com/leapmux/leapmux/channelwire"
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
// CallInner returns an error that the sibling worker's handler answers in-band as a *channelwire.RPCError, which keeps its code.
// StreamInner supplies a controller for revisions and cancellation on the dedicated stream channel.
type CrossWorkerClient interface {
	CallInner(ctx context.Context, targetWorkerID string, userID userid.UserID, method string, payload []byte) ([]byte, error)
	StreamInner(ctx context.Context, targetWorkerID string, userID userid.UserID, method string, payload []byte, onMsg func(*leapmuxv1.InnerStreamMessage), bindCtrl func(channel.StreamController)) error
}

// HubClient sends hub requests for the spawning user with a delegation token.
// procedure is the full procedure path of the hub method.
// The hub enforces the token's grant through auth.CeilingFor. A channel.Caller grant here would add an unenforced second authority.
type HubClient interface {
	CallInner(ctx context.Context, userID userid.UserID, procedure string, payload []byte) ([]byte, error)
}

// HubStreamer forwards a server-streaming hub RPC with the spawning user's delegation token.
// procedure is the stream's address, hubrpc.WatchUserStream for the one stream that the hub serves.
// payload contains the serialized request protobuf. onPayload receives serialized response protobufs.
type HubStreamer interface {
	StreamHub(ctx context.Context, userID userid.UserID, procedure string, payload []byte, onPayload func([]byte) error) error
}

// LocalStreams releases subscriptions for a synthetic local stream ID.
// Local stream IDs do not reach the channel manager's close callback. ReleaseLocalStream supplies their cleanup.
type LocalStreams interface {
	ReleaseLocalStream(streamID string)
}

// Router dispatches local-IPC requests to the appropriate backend.
//
// Method names are namespaced, and both namespaces address a method by its
// full procedure path:
//   - "worker.<procedure>", such as
//     "worker./leapmux.v1.WorkerAgentService/OpenAgent": the local worker's
//     inner-RPC dispatcher when target_worker_id is empty or this worker's id,
//     and a cross-worker channel for any other id.
//   - "hub.<procedure>", such as "hub./leapmux.v1.WorkspaceService/LocateTab":
//     the hub-bound client, which resolves the procedure through
//     internal/hubrpc. The streamed "hub./ws/userevents"
//     (hubrpc.WatchUserStream) goes through HubStreams instead.
//
// The router never reads a payload to choose the destination.
type Router struct {
	WorkerID        string
	UserID          userid.UserID
	LocalDispatcher LocalDispatcher
	CrossWorker     CrossWorkerClient
	Hub             HubClient
	HubStreams      HubStreamer
	Streams         LocalStreams
	// StreamCancellers maps the IPC stream's client_request_id to a
	// streamCancelEntry. StreamInner stores the entry when its stream starts
	// and removes it in a defer when the call returns, on every path, a
	// panicking handler included. The map therefore holds exactly the live
	// streams, and no entry needs a sweep: an old entry is a long stream.
	StreamCancellers sync.Map // string → *streamCancelEntry
}

// streamCancelEntry retains frames before binding and cancels its stream context on retirement.
type streamCancelEntry struct {
	cancel context.CancelFunc
	inbox  channel.StreamInbox
}

// CallInner executes a unary inner-RPC.
func (r *Router) CallInner(ctx context.Context, info TokenInfo, method string, payload []byte, targetWorkerID string) (*leapmuxv1.InnerRpcResponse, error) {
	switch ns := namespaceOf(method); ns {
	case namespaceWorker:
		bare := stripNamespace(method)
		if targetWorkerID == "" || targetWorkerID == r.WorkerID {
			return r.dispatchLocal(ctx, info, bare, payload), nil
		}
		if r.CrossWorker == nil {
			return nil, connect.Errorf(connect.CodeUnimplemented, "cross-worker client not configured")
		}
		out, err := r.CrossWorker.CallInner(ctx, targetWorkerID, r.UserID, bare, payload)
		// The sibling's handler answered the call with an error. Keep it
		// in-band with its code, as dispatchLocal keeps a local handler's: the
		// caller reads a Connect error as a failure to reach the worker.
		var siblingErr *channelwire.RPCError
		if errors.As(err, &siblingErr) {
			return channelwire.NewErrorResponse(siblingErr.Code, siblingErr.Message), nil
		}
		if err != nil {
			return nil, relayError(err)
		}
		return channelwire.NewPayloadResponse(out), nil
	case namespaceHub:
		if r.Hub == nil {
			return nil, connect.Errorf(connect.CodeUnimplemented, "hub client not configured")
		}
		out, err := r.Hub.CallInner(ctx, r.UserID, stripNamespace(method), payload)
		if err != nil {
			return nil, relayError(err)
		}
		return channelwire.NewPayloadResponse(out), nil
	default:
		return nil, connect.Errorf(connect.CodeInvalidArgument, "unknown method namespace: %s", method)
	}
}

// relayError preserves the Connect error code of a failure to reach the hub or a
// sibling worker, such as a channel that cannot open or a hub that refuses the
// call. Callers use these codes to distinguish missing resources from
// unavailable workers. These failures carry a nil response. An error that a
// worker's handler answers stays in-band instead, as dispatchLocal's does.
func relayError(err error) error {
	// Preserve upstream codes. A plain error has CodeUnknown and maps to CodeInternal.
	code := connect.CodeOf(err)
	if code == connect.CodeUnknown {
		code = connect.CodeInternal
	}
	return connect.NewError(code, relayMessage(err)).WithCause(err)
}

// relayMessage returns the text of err for a new Connect error. The text of a
// Connect error starts with its code, and connect.NewError prints the code in
// front of its message, so the relay carries the Connect error's own message
// instead. The text that a wrapper added stays, because it says which step of
// the relay failed.
func relayMessage(err error) string {
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		return err.Error()
	}
	return strings.Replace(err.Error(), connectErr.Error(), connectErr.Message(), 1)
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
func (r *Router) dispatchLocal(ctx context.Context, info TokenInfo, method string, payload []byte) *leapmuxv1.InnerRpcResponse {
	if r.LocalDispatcher == nil {
		return channelwire.NewErrorResponse(int32(codes.Unimplemented), "local dispatcher not configured")
	}
	collector := &responseCollector{}
	r.withLocalStream(info, func(streamID string) {
		collector.streamID = streamID
		r.LocalDispatcher.DispatchWith(ctx, r.caller(), &leapmuxv1.InnerRpcRequest{Method: method, Payload: payload}, collector)
	})
	return collector.toResponse()
}

// StreamInner runs a server-streaming inner RPC. A stream that ends with an
// error that a worker's handler answered in-band returns it as a Connect error
// with the same code, so the caller reads the code that CallInner keeps
// in-band for a unary call.
func (r *Router) StreamInner(ctx context.Context, info TokenInfo, method string, payload []byte, targetWorkerID, clientReqID string, onMsg func(*leapmuxv1.InnerStreamMessage) error) error {
	err := r.streamInner(ctx, info, method, payload, targetWorkerID, clientReqID, onMsg)
	var refusal *channelwire.RPCError
	if errors.As(err, &refusal) {
		return refusal.ConnectError()
	}
	return err
}

func (r *Router) streamInner(ctx context.Context, info TokenInfo, method string, payload []byte, targetWorkerID, clientReqID string, onMsg func(*leapmuxv1.InnerStreamMessage) error) error {
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var entry *streamCancelEntry
	if clientReqID != "" {
		entry = &streamCancelEntry{cancel: cancel}
		if previous, loaded := r.StreamCancellers.Swap(clientReqID, entry); loaded {
			if old, ok := previous.(*streamCancelEntry); ok {
				old.retire()
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
			return connect.Errorf(connect.CodeUnimplemented, "cross-worker client not configured")
		}
		return r.CrossWorker.StreamInner(streamCtx, targetWorkerID, r.UserID, bare, payload, func(m *leapmuxv1.InnerStreamMessage) {
			_ = onMsg(m)
		}, func(ctrl channel.StreamController) {
			if !entry.bind(ctrl) {
				ctrl.OnCancel()
			}
		})
	case namespaceHub:
		if r.HubStreams == nil {
			return connect.Errorf(connect.CodeUnimplemented, "hub streamer not configured")
		}
		bare := stripNamespace(method)
		return r.HubStreams.StreamHub(streamCtx, r.UserID, bare, payload, func(p []byte) error {
			return onMsg(&leapmuxv1.InnerStreamMessage{Payload: p})
		})
	default:
		return connect.Errorf(connect.CodeInvalidArgument, "unknown method namespace: %s", method)
	}
}

func (r *Router) streamLocal(ctx context.Context, info TokenInfo, method string, payload []byte, entry *streamCancelEntry, onMsg func(*leapmuxv1.InnerStreamMessage) error) error {
	if r.LocalDispatcher == nil {
		return connect.Errorf(connect.CodeUnimplemented, "local dispatcher not configured")
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

// UpdateStream applies the action of request to the stream that its
// client_request_id identifies: a revision goes to the stream, and a cancel
// retires it. An unknown or empty id is a no-op: the stream already ended, and
// its entry went with it. An update that selects no action fails with
// InvalidArgument, because it states nothing that the router could apply.
//
// A revision waits in the stream's inbox until its controller is ready. An
// inbox that overflows CANCELS the stream, and UpdateStream fails with
// ResourceExhausted. Dropping the frame instead would leave the client believing
// its revision landed, and reading the wrong events for as long as the stream
// lives. The cancel is what makes the loss visible: the client's handle closes,
// and its next update opens a fresh stream that carries the current interest.
func (r *Router) UpdateStream(request *leapmuxv1.UpdateStreamRequest) error {
	clientReqID := request.GetClientRequestId()
	switch action := request.GetAction().(type) {
	case *leapmuxv1.UpdateStreamRequest_Cancel:
		if value, ok := r.StreamCancellers.LoadAndDelete(clientReqID); ok {
			if entry, ok := value.(*streamCancelEntry); ok {
				entry.retire()
			}
		}
		return nil
	case *leapmuxv1.UpdateStreamRequest_Payload:
		value, ok := r.StreamCancellers.Load(clientReqID)
		if !ok {
			return nil
		}
		entry, ok := value.(*streamCancelEntry)
		if !ok {
			return nil
		}
		if err := entry.inbox.Deliver(action.Payload); err != nil {
			entry.retire()
			return connect.NewError(connect.CodeResourceExhausted, err.Error()).WithCause(err)
		}
		return nil
	default:
		return connect.NewError(connect.CodeInvalidArgument, "the update selects neither a revision nor a cancel")
	}
}

// retire ends the stream that entry belongs to. It cancels the inbox, which
// calls OnCancel on a bound controller and refuses a later binding, and then
// cancels the stream's context. The order does not matter for correctness; one
// method keeps the three retirement sites alike.
func (entry *streamCancelEntry) retire() {
	entry.inbox.Cancel()
	entry.cancel()
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

func (c *responseCollector) toResponse() *leapmuxv1.InnerRpcResponse {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.errSent != nil {
		return channelwire.NewErrorResponse(c.errSent.code, c.errSent.msg)
	}
	if c.resp == nil {
		return &leapmuxv1.InnerRpcResponse{}
	}
	return c.resp
}

// streamCollector forwards stream frames and waits for completion or context cancellation.
// claim gives one caller ownership of the result. settle records that result before it releases wait.
// The result mutex also protects readers that return through context cancellation before settle finishes.
type streamCollector struct {
	ctx      context.Context
	onMsg    func(*leapmuxv1.InnerStreamMessage) error
	streamID string
	// Retain the original entry even if another stream reuses its request ID.
	entry *streamCancelEntry

	finished atomic.Bool
	done     chan struct{}

	mu  sync.Mutex
	err error
}

func newStreamCollector(ctx context.Context, streamID string, onMsg func(*leapmuxv1.InnerStreamMessage) error) *streamCollector {
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
	if err := channelwire.ErrorOf(resp.GetError()); err != nil {
		c.settle(err)
		return nil
	}
	// SendResponse can complete a stream with a final payload. Forward that payload before completion.
	// An empty response carries only the completion signal.
	var outcome error
	if len(resp.GetPayload()) > 0 {
		outcome = c.onMsg(&leapmuxv1.InnerStreamMessage{
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
	c.settle(&channelwire.RPCError{Code: code, Message: msg})
	return nil
}

func (c *streamCollector) SendStream(m *leapmuxv1.InnerStreamMessage) error {
	if c.ctx.Err() != nil {
		return c.ctx.Err()
	}
	err := c.onMsg(m)

	// End and error frames complete the collector, as SendResponse and SendError do.
	streamErr := channelwire.ErrorOf(m.GetError())
	if m.GetEnd() || streamErr != nil {
		if c.claim() {
			// Preserve delivery failures on a clean End. An explicit provider error takes precedence.
			outcome := err
			if streamErr != nil {
				outcome = streamErr
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
