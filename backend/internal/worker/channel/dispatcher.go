package channel

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"

	"google.golang.org/grpc/codes"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/util/drain"
)

// ResponseWriter sends inner RPC responses back through the transport layer.
// Implementations include the encrypted channel sender and the cleartext
// RPC-forward writer.
type ResponseWriter interface {
	SendResponse(resp *leapmuxv1.InnerRpcResponse) error
	SendError(code int32, message string) error
	SendStream(msg *leapmuxv1.InnerStreamMessage) error
	// ChannelID returns the E2EE channel ID for this writer.
	// Returns "" for non-channel writers (e.g. cleartext RPC-forward).
	ChannelID() string
	// MaxPayloadBudget returns the negotiated application payload budget
	// for this channel (min(hub, worker) at open), or 0 when the writer is
	// not channel-backed (cleartext IPC / test stubs).
	MaxPayloadBudget() int
	// BindStream installs ctrl as the receiver of client frames on this
	// writer's correlation id and returns a release func to call when the
	// stream ends. ok is false for writers whose transport carries no client
	// frames, so a handler can degrade instead of assuming.
	BindStream(ctrl StreamController) (release func(), ok bool)
}

// errorQueuer is implemented by response writers that can hand an error to a
// drainer instead of sending it inline. DispatchAsync's own refusals (an unknown
// method, a stream it cannot prepare) run on the worker's shared Connect receive
// goroutine, where an inline send takes the session's encryption gate and can
// park behind a handler that is itself parked on the connection writer's byte
// budget -- wedging the ONE receive loop that serves every channel on this
// worker. Queuing keeps that loop free; writers that are not on the shared
// receive path (the local-IPC router) keep the inline send (see queueRefusal).
type errorQueuer interface {
	// QueueError queues a unary error, an InnerRpcResponse.
	QueueError(code int32, message string)
	// QueueStreamError queues a stream error frame, an InnerStreamMessage that
	// carries the error and ends the stream.
	QueueStreamError(code int32, message string)
}

// Prepare the stream before dispatch can race a client update or cancellation.
type streamPreparer interface {
	prepareStream() (releaseUnbound func(), ok bool)
}

// HandlerFunc is the signature for an inner RPC method handler.
//
// `caller` carries the identity the Hub established AND the grant that identity
// was authorized with, together. It replaced a bare userid.UserID, and the
// pairing is the point: the Worker restricted each method on identity alone --
// "is this the registrant", one bit -- so a credential that could open a channel
// reached ReadFile, SendInput, PushBranch and OpenTunnelConn alike. A signature
// carrying BOTH facts makes forgetting the second impossible rather than merely
// unlikely, because the compiler refuses every unconverted call site.
//
// `ctx`
// is bound to the inbound request's lifecycle (per-session for E2EE
// channel handlers, per-call for cleartext local IPC) and is cancelled
// when the originating connection / call ends — handlers should pass
// it to long-running subprocesses and DB queries so a dropped client
// stops the work instead of letting it run to completion. Fire-and-
// forget post-response work that must outlive the request should keep
// using `bgCtx()`.
//
// `sender` is the transport's writer for this request. A unary method receives
// it by identity. A streaming method receives it inside shapedWriter, which adds
// the declared reply shape and no other state. A handler MAY retain it past
// return -- WatchEvents does exactly that, parking it in the watcher registry to
// carry live events for the rest of the channel's life. That is only sound
// because every transport mints a FRESH writer per inbound request (see the
// boundSender built in HandleMessage, and controlipc's per-call
// collectors): the writer carries the request's correlation id, so a
// retained one keeps addressing the stream it was created for. A
// transport that pooled or reused writers would silently misroute every
// retained stream, with nothing here to catch it.
type HandlerFunc func(ctx context.Context, caller Caller, req *leapmuxv1.InnerRpcRequest, sender ResponseWriter)

// registered captures a handler plus whether its in-flight invocations
// must gate Shutdown. tracked=true methods drive cleanup.Add() /
// .Done() around every dispatch via the counter bound through
// BindCleanup.
type registered struct {
	fn      HandlerFunc
	tracked bool
	// streaming records that this method answers with InnerStreamMessage
	// frames rather than a single InnerRpcResponse. Every error reply takes this
	// shape, whoever sends it (see shapedWriter).
	streaming bool
}

// Dispatcher routes inner RPC method calls to registered handlers.
//
// Handlers are keyed by the method string a request carries. The worker's
// registrar registers each method under its full Connect procedure path
// (/leapmux.v1.<Service>/<Method>), taken from the generated
// leapmuxv1connect constants.
type Dispatcher struct {
	handlers map[string]registered
	// cleanup, when non-nil, is incremented before every tracked
	// dispatch and decremented when the handler returns. Callers
	// (typically the worker service.Service) wait on it from Shutdown
	// so destructive mutations finish before DB / data-dir teardown.
	cleanup *drain.Counter
	// gate admits handlers while the worker is live and joins them, up to a
	// limit, at shutdown. Every handler -- tracked or not -- counts: an untracked
	// handler such as UpdateAgentSettings or SendControlResponse can reach
	// the closed database, and the Cleanup wait in Shutdown ran once, so a
	// later tracked Add was neither joined nor safe. CloseAdmission flips the gate, and the
	// refusal of a closed gate states the shutdown instead of racing it.
	gate drain.Gate
}

// NewDispatcher creates a new inner RPC Dispatcher.
func NewDispatcher() *Dispatcher {
	return &Dispatcher{
		handlers: make(map[string]registered),
	}
}

// Registration states how the dispatcher runs one method.
type Registration struct {
	// Tracked makes Shutdown wait for in-flight invocations, through the
	// counter that BindCleanup binds. Use it for any destructive mutation whose
	// half-applied state would leak if Shutdown returned mid-flight -- e.g. a
	// multi-step git mutation that has run the first command but not the
	// second, or a worktree teardown that has removed the directory but not
	// yet written the DB soft-delete. (Grep for Tracked registrations to see
	// the current set rather than maintaining a list here, which drifts as
	// handlers are added or removed.)
	//
	// The Add() for tracked methods happens SYNCHRONOUSLY in DispatchAsync
	// (before the goroutine launches) and at the top of DispatchWith (the
	// synchronous local-IPC path), so the wait in Shutdown observes the
	// increment regardless of how the dispatch was initiated. Putting the
	// Add() inside the handler body -- as an earlier revision did -- races
	// `go DispatchWith(...)` against that wait, which can return
	// before the goroutine even reaches Add().
	Tracked bool
	// Streaming marks a server-streaming method, whose replies are
	// InnerStreamMessage frames rather than one InnerRpcResponse. The
	// dispatcher prepares the stream before the handler runs, and every error
	// reply of the method is a stream error frame: the dispatcher's own
	// refusals (a stream it cannot prepare, a handler panic), and each SendError
	// of the handler (see shapedWriter).
	Streaming bool
}

// Handle adds a handler for the given method. It panics when the method
// already has one: a silent overwrite would replace a handler with no sign
// that anything happened.
func (d *Dispatcher) Handle(method string, reg Registration, handler HandlerFunc) {
	if _, exists := d.handlers[method]; exists {
		panic("duplicate inner RPC handler: " + method)
	}
	d.handlers[method] = registered{fn: handler, tracked: reg.Tracked, streaming: reg.Streaming}
}

// Methods returns every registered method name, unordered.
//
// It exists so a test can assert a property of EVERY handler a registrar installed
// without hand-maintaining a list of them -- a list that silently stops covering
// each method added after it was written, which is the same "a line each author must
// remember" failure the registrars exist to remove. Not for dispatch: routing goes
// through Dispatch/DispatchWith/DispatchAsync, which look the method up directly.
func (d *Dispatcher) Methods() []string {
	methods := make([]string, 0, len(d.handlers))
	for method := range d.handlers {
		methods = append(methods, method)
	}
	return methods
}

// BindCleanup wires the counter that tracked dispatches Add() on.
// Pass nil to disable cleanup tracking (e.g. in tests that don't care
// about Shutdown drain semantics). Must be called before the first
// dispatch; concurrent BindCleanup + Dispatch is a programming error.
//
// Callers registering the worker service's handlers do NOT call this:
// service.RegisterAll binds svc.Cleanup itself, so the two can't drift.
// It stays exported for the channel-package tests and for any registrar
// that wires a dispatcher without a Service behind it.
func (d *Dispatcher) BindCleanup(cleanup *drain.Counter) {
	d.cleanup = cleanup
}

// Dispatch routes an InnerRpcRequest to the appropriate handler using
// the encrypted channel sender. requestID is the correlation ID from
// the outer ChannelMessage, used to match responses on the frontend.
// ctx is the session-scoped context — cancelled when the channel closes
// — so handlers can abort gracefully on client disconnect.
//
// Synchronous. Callers that want fire-and-forget behaviour (the
// session receive loop) should use DispatchAsync, which manages the
// Add()/Done() pairing across the goroutine boundary.
func (d *Dispatcher) Dispatch(ctx context.Context, caller Caller, req *leapmuxv1.InnerRpcRequest, requestID uint64, inner *channelSender) {
	d.DispatchWith(ctx, caller, req, &boundSender{sender: inner, requestID: requestID, method: req.GetMethod()})
}

// DispatchWith routes an InnerRpcRequest to the appropriate handler
// using a custom ResponseWriter. Synchronous; the call returns after
// the handler completes. Used by the local-IPC router (which serves
// the `leapmux control` CLI on a synchronous request/response loop).
//
// For tracked methods, Add()/Done() bracket the handler call so a
// SIGTERM mid-request waits for the response to flush before Shutdown
// tears down the DB.
func (d *Dispatcher) DispatchWith(ctx context.Context, caller Caller, req *leapmuxv1.InnerRpcRequest, w ResponseWriter) {
	h, ok := d.handlers[req.GetMethod()]
	if !ok {
		slog.Warn("unknown inner RPC method", "method", req.GetMethod())
		// An unknown method has no registration to state a reply shape, so its
		// refusal is unary.
		_ = w.SendError(int32(codes.Unimplemented), "unknown method: "+req.GetMethod())
		return
	}
	done, admitted := d.beginInvocation(h)
	if !admitted {
		queueRefusal(w, h.streaming, codes.FailedPrecondition, workerShuttingDownRefusal)
		return
	}
	defer done()
	d.invoke(ctx, caller, req, w, h)
}

// DispatchAsync launches the handler on a new goroutine. For tracked
// methods the counter's Add() happens SYNCHRONOUSLY before the
// goroutine launches, so the wait in Shutdown observes the increment
// regardless of scheduler timing. The defer'd Done() runs inside the
// goroutine after the handler returns.
//
// Used by the encrypted channel receive loop, which dispatches
// requests fire-and-forget so a slow handler (e.g. WatchEvents) can't
// stall the decrypt-and-route loop. The previous pattern of
// `go d.DispatchWith(...)` plus per-handler Cleanup.Add(1) had a TOCTOU
// race: Shutdown.Wait could return before the new goroutine reached
// the Add(1), letting a destructive mutation run against a tearing-
// down service.
func (d *Dispatcher) DispatchAsync(ctx context.Context, caller Caller, req *leapmuxv1.InnerRpcRequest, w ResponseWriter) {
	h, ok := d.handlers[req.GetMethod()]
	if !ok {
		// Prefer the bounded error queue when the writer offers one: an
		// unknown-method response is just a marshal+encrypt+send, but on the
		// encrypted channel path that still takes the session's encryption
		// gate -- and a peer flooding unknown-method frames must not pin one
		// goroutine (or one gate waiter) per frame, nor park the shared
		// receive loop behind a handler that is itself parked on the
		// connection writer's byte budget. Writers without a queue (local
		// IPC) keep the inline path, which is correct there: they are not on
		// the shared receive goroutine.
		slog.Warn("unknown inner RPC method", "method", req.GetMethod())
		// An unknown method has no registration to state a reply shape, so its
		// refusal is unary.
		queueRefusal(w, false, codes.Unimplemented, "unknown method: "+req.GetMethod())
		return
	}
	var releaseUnbound func()
	if h.streaming {
		if preparer, supports := w.(streamPreparer); supports {
			var prepared bool
			releaseUnbound, prepared = preparer.prepareStream()
			if !prepared {
				queueRefusal(w, h.streaming, codes.FailedPrecondition, "the stream request is closed or its ID is already in use")
				return
			}
		}
	}
	done, admitted := d.beginInvocation(h)
	if !admitted {
		// The stream above is unbound: release it before the refusal, so a
		// closed gate cannot strand a prepared stream registration.
		if releaseUnbound != nil {
			releaseUnbound()
		}
		queueRefusal(w, h.streaming, codes.FailedPrecondition, workerShuttingDownRefusal)
		return
	}
	go func() {
		if releaseUnbound != nil {
			defer releaseUnbound()
		}
		defer done()
		d.invoke(ctx, caller, req, w, h)
	}()
}

// beginInvocation admits and counts a handler before either dispatch path runs it.
func (d *Dispatcher) beginInvocation(h registered) (func(), bool) {
	tracked := h.tracked && d.cleanup != nil
	finish, admitted := d.gate.Begin(func() {
		if tracked {
			d.cleanup.Add()
		}
	})
	if !admitted {
		return nil, false
	}
	return func() {
		// A joined invocation must also have left the service cleanup counter.
		if tracked {
			d.cleanup.Done()
		}
		finish()
	}, true
}

// workerShuttingDownRefusal is the closed-gate refusal's message. The wording
// matches refuseIfShuttingDown in the worker service, whose latch is set
// after CloseAdmission runs, so a client sees one answer whichever guard
// refused it.
const workerShuttingDownRefusal = "worker is shutting down"

// CloseAdmission refuses every later dispatch. Wiring.Shutdown calls it
// before the drains, so WaitHandlers joins exactly the handlers that were in
// flight when the gate closed. A refused dispatch answers FailedPrecondition
// with the shutdown, in the reply shape of its method.
func (d *Dispatcher) CloseAdmission() {
	d.gate.Close(nil)
}

// WaitHandlers joins every admitted handler, limited by timeout, and reports
// whether they all finished. A streaming handler ends only when its session
// context ends, so the caller must close the channels first (channel.Manager
// CloseAll). WatchEvents remains in its invocation until replay, final frames,
// and stream release finish.
func (d *Dispatcher) WaitHandlers(timeout time.Duration) bool {
	return d.gate.Drain(timeout, "worker: handler drain timed out during shutdown; abandoning in-flight handlers")
}

// invoke runs a resolved handler with panic recovery. Centralised so
// DispatchWith and DispatchAsync stay in sync on the wrapping
// semantics — adding a wrap (tracing, metrics) once lands at both
// entry points. This is the real decorator choke point for handler
// invocations.
func (d *Dispatcher) invoke(ctx context.Context, caller Caller, req *leapmuxv1.InnerRpcRequest, w ResponseWriter, h registered) {
	// Before the deferred recover, so that the panic refusal takes the shape of
	// every other error of this method.
	w = withReplyShape(w, h.streaming)
	// Recover from handler panics so the goroutine doesn't die silently,
	// which would leave the frontend waiting until its 30s timeout fires.
	defer func() {
		if r := recover(); r != nil {
			slog.Error("handler panicked",
				"method", req.GetMethod(),
				"panic", r,
				"stack", string(debug.Stack()),
			)
			_ = w.SendError(int32(codes.Internal), "internal error")
		}
	}()
	h.fn(ctx, caller, req, w)
}

// shapedWriter makes the reply shape of a server-streaming method a property of
// its registration. SendError answers with one stream error frame that also ends
// the stream, so no handler or helper can choose the wrong shape.
//
// A unary error is the wrong shape for a stream. The browser holds a streaming
// call's correlation id in streamListeners. deliverResponse hands a unary ERROR
// on that id to the listener's onError, but the frame carries no End flag, so a
// receiver cannot end the stream on it generically. A unary non-error payload on
// that id is dropped. The browser checks the error before End (see
// deliverStream), so a frame with both reports the error rather than a clean
// close.
type shapedWriter struct {
	ResponseWriter
}

// SendError sends the error as a stream frame instead of a unary response.
func (w shapedWriter) SendError(code int32, message string) error {
	return w.SendStream(channelwire.NewStreamError(code, message))
}

// withReplyShape returns the writer that answers in a method's declared shape.
// A unary method keeps the transport's writer, which already answers unary.
func withReplyShape(w ResponseWriter, streaming bool) ResponseWriter {
	if streaming {
		return shapedWriter{ResponseWriter: w}
	}
	return w
}

// queueRefusal sends a refusal decided on the shared receive goroutine, in the
// reply shape of the method. It hands the refusal to the writer's error queue
// when the writer offers one (see errorQueuer), and sends it inline otherwise.
func queueRefusal(w ResponseWriter, streaming bool, code codes.Code, message string) {
	queue, ok := w.(errorQueuer)
	switch {
	case !ok:
		_ = withReplyShape(w, streaming).SendError(int32(code), message)
	case streaming:
		queue.QueueStreamError(int32(code), message)
	default:
		queue.QueueError(int32(code), message)
	}
}
