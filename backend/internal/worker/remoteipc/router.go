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
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/service"
)

// LocalDispatcher is the subset of channel.Dispatcher the router needs.
// Mirrors the existing dispatcher's DispatchWith signature so the
// router can inject local-IPC ResponseWriters.
type LocalDispatcher interface {
	DispatchWith(ctx context.Context, userID userid.UserID, req *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter)
}

// CrossWorkerClient sends a unary inner RPC to a sibling worker via the
// hub's E2EE channel relay. The implementation lives in
// internal/worker/crossworker.
//
// The delegation bearer it authenticates with is scoped to (user, minting
// worker), so the channel pool keys on (target, user) alone.
type CrossWorkerClient interface {
	CallInner(ctx context.Context, targetWorkerID string, userID userid.UserID, method string, payload []byte) ([]byte, error)
	StreamInner(ctx context.Context, targetWorkerID string, userID userid.UserID, method string, payload []byte, onMsg func(*leapmuxv1.InnerStreamMessage)) error
}

// HubClient is the subset of the worker's hub-bound client the router
// uses. Lets the router make user-scoped calls to hub services on
// behalf of the spawning user (with a delegation token, when minted).
type HubClient interface {
	CallInner(ctx context.Context, userID userid.UserID, method string, payload []byte) ([]byte, error)
}

// HubStreamer forwards a server-streaming hub RPC. The implementation
// authenticates with a delegation-token bearer minted for the spawned
// agent's user. payload is a marshalled request proto;
// onPayload receives marshalled response protos.
type HubStreamer interface {
	StreamHub(ctx context.Context, userID userid.UserID, method string, payload []byte, onPayload func([]byte) error) error
}

// LocalStreams is the subset of service.Service the router uses to retire
// the per-stream state a local-IPC dispatch leaves behind (today, the event
// subscriptions keyed by the synthetic stream id). Local-IPC ids never reach
// the channel manager's close callback, so nothing else would ever sweep them
// -- see service.ReleaseLocalStream.
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

// relayError preserves the originating connect code when relaying a hub or
// cross-worker failure back to the caller.
//
// It used to be a flat connect.CodeInternal wrap, and that silently disabled
// every code-sensitive decision the CLI makes on this transport. The code is
// the only machine-readable part of the answer -- the message survives, but
// nothing may parse it -- so flattening turned "no such workspace"
// (CodeNotFound) and "worker is offline" (CodeUnavailable) into an
// indistinguishable internal error. Downstream that made
// cmd.isNotFoundOrForbidden and cmd.isWorkerUnreachable unable to match at all
// for a worker-spawned agent, so `tab close` on an offline sibling worker
// reported inspect_failed instead of falling back to a CRDT-only tombstone --
// while the identical command over the hub transport, and the frontend's
// mirrored predicate, both fell back correctly.
//
// Note this is NOT the in-band CallInnerResponse.ErrorCode path: hub and
// cross-worker failures return a nil response and a non-nil error, so they
// never reach the IsError branch that dispatchLocal populates. The two also
// carry different enums (ErrorCode is a grpc code, this is a connect.Code);
// they must not be conflated.
func relayError(err error) error {
	// CodeOf answers CodeUnknown for a plain error. Only a real upstream code is
	// worth preserving; an uncoded failure keeps the old CodeInternal, which is
	// what distinguishes "the relay broke" from "no hub configured"
	// (Unimplemented) and "workspace out of scope" (PermissionDenied).
	if code := connect.CodeOf(err); code != connect.CodeUnknown {
		return connect.NewError(code, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

// withLocalStream mints a synthetic stream id for one local-IPC dispatch,
// runs fn under it, then retires whatever the dispatch registered against it.
// Pairs the dispatchLocal / streamLocal lifecycle in one place so both call
// sites can't drift on mint/release symmetry.
func (r *Router) withLocalStream(info TokenInfo, fn func(streamID string)) {
	streamID := newLocalStreamID(info)
	if r.Streams != nil {
		defer r.Streams.ReleaseLocalStream(streamID)
	}
	fn(streamID)
}

// dispatchLocal runs a same-worker inner-RPC and synchronously
// collects the response. Streams aren't expected here — StreamInner
// handles that path. The caller's ctx propagates to the handler so a
// cancelled connect-RPC tears down `exec.CommandContext` subprocesses
// the handler started.
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
		r.LocalDispatcher.DispatchWith(ctx, r.UserID, &leapmuxv1.InnerRpcRequest{Method: method, Payload: payload}, collector)
	})
	return collector.toResponse()
}

// StreamInner runs a server-streaming inner RPC.
func (r *Router) StreamInner(ctx context.Context, info TokenInfo, method string, payload []byte, targetWorkerID, clientReqID string, onMsg func(*leapmuxv1.StreamInnerEnvelope) error) error {
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
		return r.CrossWorker.StreamInner(streamCtx, targetWorkerID, r.UserID, bare, payload, func(m *leapmuxv1.InnerStreamMessage) {
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
	r.withLocalStream(info, func(streamID string) {
		collector = newStreamCollector(ctx, streamID, onMsg)
		r.LocalDispatcher.DispatchWith(ctx, r.UserID, &leapmuxv1.InnerRpcRequest{Method: method, Payload: payload}, collector)
		collector.wait()
	})
	return collector.outcome()
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

func (c *responseCollector) ChannelID() string   { return c.streamID }
func (*responseCollector) MaxPayloadBudget() int { return 0 }

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
// the ctx is cancelled.
//
// Terminating is two steps, and the ORDER is the whole contract: claim() takes
// sole ownership of err via CAS, then settle(err) records it and only then
// closes done. Doing it the other way -- close first, assign after -- released
// wait() BEFORE the write, so the reader raced the writer and a stream error
// could be reported to the caller as success. That is reachable whenever a
// terminal frame arrives from a goroutine other than the handler's own (a
// WatchEvents broadcast, a rejected streaming request), which this commit made
// commonplace by answering streaming denials as stream frames.
//
// err is additionally mutex-guarded so the read in streamLocal is ordered
// against the write even when wait() returns via ctx cancellation rather than
// via done.
type streamCollector struct {
	ctx      context.Context
	onMsg    func(*leapmuxv1.StreamInnerEnvelope) error
	streamID string

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

// claim marks the collector terminal and returns true when the caller is the
// first to reach this state. Only that caller may call settle; anyone observing
// false MUST NOT touch err (someone else owns it now).
//
// claim does NOT release wait() -- settle does. Every claim must be followed by
// exactly one settle, or a caller blocks until its own context expires.
func (c *streamCollector) claim() bool {
	return c.finished.CompareAndSwap(false, true)
}

// settle records the terminal error (nil for success) and releases wait().
// Called exactly once, by whoever won claim.
func (c *streamCollector) settle(err error) {
	c.mu.Lock()
	c.err = err
	c.mu.Unlock()
	close(c.done)
}

// outcome reports the terminal error, or nil when the stream ended cleanly or
// the context was cancelled before any terminal frame arrived.
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
	// A streaming handler that signals completion via SendResponse may
	// still carry a final payload (a fast-path that produced a single
	// terminal frame, a unary-shaped result over a streaming
	// ResponseWriter, or a handler reusing the same ResponseWriter for
	// both unary and stream surfaces). Forwarding the payload through
	// onMsg keeps that frame observable to the IPC caller; dropping it
	// silently corrupted the stream end. Skip the forward only when
	// there is no payload — an empty SendResponse is the documented
	// "done, nothing more" signal.
	var terminal error
	if len(resp.GetPayload()) > 0 {
		terminal = c.onMsg(&leapmuxv1.StreamInnerEnvelope{
			Payload: resp.GetPayload(),
			End:     true,
		})
	}
	c.settle(terminal)
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

	// A terminal frame finishes the collector, exactly as SendResponse and
	// SendError do.
	//
	// Only those two used to, which was enough while every handler answered
	// unary. Now that a streaming handler reports its failures as stream
	// frames -- a rejected request, a panic -- a caller would sit in wait()
	// until its own context expired, because the frame that WAS the ending
	// did not look like one to this type.
	if m.GetEnd() || m.GetIsError() {
		if c.claim() {
			// `terminal` starts as onMsg's error, so a delivery failure on a CLEAN
			// End frame is reported rather than settled as success. SendResponse
			// already assigns onMsg's result straight to its terminal outcome; this
			// path used to look only at IsError, so the outcome a caller saw
			// depended on which of two equivalent reply shapes the handler chose.
			terminal := err
			if m.GetIsError() {
				// An explicit error frame names the failure better than a local
				// send error would, so it wins.
				terminal = fmt.Errorf("rpc error %d: %s", m.GetErrorCode(), m.GetErrorMessage())
			}
			c.settle(terminal)
		}
	}
	return err
}

func (c *streamCollector) ChannelID() string   { return c.streamID }
func (*streamCollector) MaxPayloadBudget() int { return 0 }

// wait blocks until either the handler signals completion or the request
// context is cancelled.
//
// Three paths settle the collector, each through claim/settle so only the first
// wins: SendResponse, SendError, and a terminal SendStream frame (an End or an
// IsError). Observing that lets withLocalStream release the stream's registered
// watchers and event subscriptions as soon as the handler returns, rather than
// waiting for the connect-rpc client to close the stream.
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
