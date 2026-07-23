package channel

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"google.golang.org/grpc/codes"
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
}

// HandlerFunc is the signature for an inner RPC method handler. `ctx`
// is bound to the inbound request's lifecycle (per-session for E2EE
// channel handlers, per-call for cleartext local IPC) and is cancelled
// when the originating connection / call ends — handlers should pass
// it to long-running subprocesses and DB queries so a dropped client
// stops the work instead of letting it run to completion. Fire-and-
// forget post-response work that must outlive the request should keep
// using `bgCtx()`.
//
// `sender` is the dispatcher's writer, handed over by identity rather
// than wrapped. A handler MAY retain it past return -- WatchEvents does
// exactly that, parking it in the watcher registry to carry live events
// for the rest of the channel's life. That is only sound because every
// transport mints a FRESH writer per inbound request (see the
// boundSender built in HandleMessage, and remoteipc's per-call
// collectors): the writer carries the request's correlation id, so a
// retained one keeps addressing the stream it was created for. A
// transport that pooled or reused writers would silently misroute every
// retained stream, with nothing here to catch it.
type HandlerFunc func(ctx context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender ResponseWriter)

// registered captures a handler plus whether its in-flight invocations
// must gate Shutdown. tracked=true methods drive cleanup.Add(1) /
// .Done() around every dispatch via the WaitGroup bound through
// BindCleanup.
type registered struct {
	fn      HandlerFunc
	tracked bool
	// streaming records that this method answers with InnerStreamMessage
	// frames rather than a single InnerRpcResponse. invoke needs it to
	// report a panic in a shape the caller is actually listening for: a
	// streaming call's correlation id is registered as a stream on the
	// client, and an InnerRpcResponse on a stream id is dropped with no
	// pending request to receive it.
	streaming bool
}

// Dispatcher routes inner RPC method calls to registered handlers.
type Dispatcher struct {
	handlers map[string]registered
	// cleanup, when non-nil, is incremented before every tracked
	// dispatch and decremented when the handler returns. Callers
	// (typically the worker service.Service) Wait on it from Shutdown
	// so destructive mutations finish before DB / data-dir teardown.
	cleanup *sync.WaitGroup
}

// NewDispatcher creates a new inner RPC Dispatcher.
func NewDispatcher() *Dispatcher {
	return &Dispatcher{
		handlers: make(map[string]registered),
	}
}

// Register adds a handler for the given method name. The method runs
// fire-and-forget on Shutdown: in-flight calls are NOT awaited. Use
// this for read-only probes whose results are safe to drop on
// teardown (GetGitInfo, ListWorkers, dialog-open probes, etc.).
func (d *Dispatcher) Register(method string, handler HandlerFunc) {
	d.handlers[method] = registered{fn: handler}
}

// RegisterTracked adds a handler whose in-flight invocations gate
// Shutdown via the WaitGroup bound by BindCleanup. Use it for any
// destructive mutation whose half-applied state would leak if Shutdown
// returned mid-flight — e.g. a multi-step git mutation that has run the
// first command but not the second, or a worktree teardown that has
// removed the directory but not yet written the DB soft-delete. (Grep
// for RegisterTracked to see the current set rather than maintaining a
// list here, which drifts as handlers are added or removed.)
//
// The Add(1) for tracked methods happens SYNCHRONOUSLY in DispatchAsync
// (before the goroutine launches) and at the top of DispatchWith (the
// synchronous local-IPC path), so Shutdown.Wait observes the
// increment regardless of how the dispatch was initiated. Putting the
// Add(1) inside the handler body — as an earlier revision did — races
// `go DispatchWith(...)` against Shutdown.Wait, which can return
// before the goroutine even reaches Add(1).
func (d *Dispatcher) RegisterTracked(method string, handler HandlerFunc) {
	d.handlers[method] = registered{fn: handler, tracked: true}
}

// RegisterStream adds a handler for a server-streaming method, whose
// replies are InnerStreamMessage frames rather than one
// InnerRpcResponse. Registering with Register instead still works for
// the happy path -- the handler chooses its own reply shape -- but a
// panic would then be reported as an InnerRpcResponse the client drops,
// leaving the tab waiting with no events and no error.
func (d *Dispatcher) RegisterStream(method string, handler HandlerFunc) {
	d.handlers[method] = registered{fn: handler, streaming: true}
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

// BindCleanup wires the WaitGroup that tracked dispatches Add(1) on.
// Pass nil to disable cleanup tracking (e.g. in tests that don't care
// about Shutdown drain semantics). Must be called before the first
// dispatch; concurrent BindCleanup + Dispatch is a programming error.
//
// Callers registering the worker service's handlers do NOT call this:
// service.RegisterAll binds svc.Cleanup itself, so the two can't drift.
// It stays exported for the channel-package tests and for any registrar
// that wires a dispatcher without a Service behind it.
func (d *Dispatcher) BindCleanup(wg *sync.WaitGroup) {
	d.cleanup = wg
}

// Dispatch routes an InnerRpcRequest to the appropriate handler using
// the encrypted channel sender. requestID is the correlation ID from
// the outer ChannelMessage, used to match responses on the frontend.
// ctx is the session-scoped context — cancelled when the channel closes
// — so handlers can abort gracefully on client disconnect.
//
// Synchronous. Callers that want fire-and-forget behaviour (the
// session receive loop) should use DispatchAsync, which manages the
// Add(1)/Done() pairing across the goroutine boundary.
func (d *Dispatcher) Dispatch(ctx context.Context, userID string, req *leapmuxv1.InnerRpcRequest, requestID uint64, inner *channelSender) {
	d.DispatchWith(ctx, userID, req, &boundSender{sender: inner, requestID: requestID, method: req.GetMethod()})
}

// DispatchWith routes an InnerRpcRequest to the appropriate handler
// using a custom ResponseWriter. Synchronous; the call returns after
// the handler completes. Used by the local-IPC router (which serves
// the `leapmux remote` CLI on a synchronous request/response loop).
//
// For tracked methods, Add(1)/Done() bracket the handler call so a
// SIGTERM mid-request waits for the response to flush before Shutdown
// tears down the DB.
func (d *Dispatcher) DispatchWith(ctx context.Context, userID string, req *leapmuxv1.InnerRpcRequest, w ResponseWriter) {
	h, ok := d.handlers[req.GetMethod()]
	if !ok {
		slog.Warn("unknown inner RPC method", "method", req.GetMethod())
		_ = w.SendError(int32(codes.Unimplemented), "unknown method: "+req.GetMethod())
		return
	}
	if h.tracked && d.cleanup != nil {
		d.cleanup.Add(1)
		defer d.cleanup.Done()
	}
	d.invoke(ctx, userID, req, w, h)
}

// DispatchAsync launches the handler on a new goroutine. For tracked
// methods the WaitGroup Add(1) happens SYNCHRONOUSLY before the
// goroutine launches, so Shutdown.Wait observes the increment
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
func (d *Dispatcher) DispatchAsync(ctx context.Context, userID string, req *leapmuxv1.InnerRpcRequest, w ResponseWriter) {
	h, ok := d.handlers[req.GetMethod()]
	if !ok {
		// Send the error INLINE rather than spawning a goroutine. Unlike a real
		// handler (which DispatchAsync exists to offload -- WatchEvents, agent
		// calls can block for seconds), an unknown-method response is just a
		// marshal+encrypt+send, so spawning a goroutine per frame would let a peer
		// flooding unknown-method frames pin one goroutine (and one sender.mu
		// waiter) per frame; sending inline bounds the cost to the receive loop's
		// own decryption rate.
		//
		// This is NOT fully safe on the receive goroutine the way the comment used
		// to claim: SendError takes sender.mu and writes on the Connect stream,
		// which can block on the stream's HTTP/2 send window under Hub backpressure
		// -- the same wedge the per-session bounded error-send queue exists to
		// close (session.go, tracked in
		// https://github.com/leapmux/leapmux/issues/293). Unifying this path behind
		// that bounded writer would close it; until then an unknown-method storm
		// under backpressure can stall the receive loop, a tradeoff chosen here
		// over the goroutine-per-frame flood it prevents.
		slog.Warn("unknown inner RPC method", "method", req.GetMethod())
		_ = w.SendError(int32(codes.Unimplemented), "unknown method: "+req.GetMethod())
		return
	}
	if h.tracked && d.cleanup != nil {
		d.cleanup.Add(1)
	}
	go func() {
		if h.tracked && d.cleanup != nil {
			defer d.cleanup.Done()
		}
		d.invoke(ctx, userID, req, w, h)
	}()
}

// invoke runs a resolved handler with panic recovery. Centralised so
// DispatchWith and DispatchAsync stay in sync on the wrapping
// semantics — adding a wrap (tracing, metrics) once lands at both
// entry points. This is the real decorator choke point for handler
// invocations.
func (d *Dispatcher) invoke(ctx context.Context, userID string, req *leapmuxv1.InnerRpcRequest, w ResponseWriter, h registered) {
	// Recover from handler panics so the goroutine doesn't die silently,
	// which would leave the frontend waiting until its 30s timeout fires.
	defer func() {
		if r := recover(); r != nil {
			slog.Error("handler panicked",
				"method", req.GetMethod(),
				"panic", r,
				"stack", string(debug.Stack()),
			)
			if h.streaming {
				// The caller registered this correlation id as a stream, so
				// it has no pending request an InnerRpcResponse could
				// resolve; report in-band instead.
				_ = w.SendStream(&leapmuxv1.InnerStreamMessage{
					IsError:      true,
					ErrorCode:    int32(codes.Internal),
					ErrorMessage: "internal error",
				})
				return
			}
			_ = w.SendError(int32(codes.Internal), "internal error")
		}
	}()
	h.fn(ctx, userID, req, w)
}
