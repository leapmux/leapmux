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

// Sender provides methods for sending responses back to the caller.
type Sender struct {
	inner ResponseWriter
}

// NewSender creates a Sender from a ResponseWriter.
func NewSender(w ResponseWriter) *Sender {
	return &Sender{inner: w}
}

// SendResponse sends an InnerRpcResponse.
func (s *Sender) SendResponse(resp *leapmuxv1.InnerRpcResponse) error {
	return s.inner.SendResponse(resp)
}

// SendError sends an error response.
func (s *Sender) SendError(code int32, message string) error {
	return s.inner.SendError(code, message)
}

// SendStream sends an InnerStreamMessage.
func (s *Sender) SendStream(msg *leapmuxv1.InnerStreamMessage) error {
	return s.inner.SendStream(msg)
}

// ChannelID returns the E2EE channel ID for this sender.
func (s *Sender) ChannelID() string {
	return s.inner.ChannelID()
}

// HandlerFunc is the signature for an inner RPC method handler. `ctx`
// is bound to the inbound request's lifecycle (per-session for E2EE
// channel handlers, per-call for cleartext local IPC) and is cancelled
// when the originating connection / call ends — handlers should pass
// it to long-running subprocesses and DB queries so a dropped client
// stops the work instead of letting it run to completion. Fire-and-
// forget post-response work that must outlive the request should keep
// using `bgCtx()`.
type HandlerFunc func(ctx context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender *Sender)

// registered captures a handler plus whether its in-flight invocations
// must gate Shutdown. tracked=true methods drive cleanup.Add(1) /
// .Done() around every dispatch via the WaitGroup bound through
// BindCleanup.
type registered struct {
	fn      HandlerFunc
	tracked bool
}

// Dispatcher routes inner RPC method calls to registered handlers.
type Dispatcher struct {
	handlers map[string]registered
	// cleanup, when non-nil, is incremented before every tracked
	// dispatch and decremented when the handler returns. Callers
	// (typically the worker service.Context) Wait on it from Shutdown
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
// Shutdown via the WaitGroup bound by BindCleanup. Use for destructive
// mutations whose half-applied state would leak (CheckoutBranch,
// CreateBranch, DeleteBranch, PushBranch, ForceRemoveWorktree,
// KeepWorktree, closeTabCommon — anything where Shutdown cannot
// safely abandon the in-flight work).
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

// BindCleanup wires the WaitGroup that tracked dispatches Add(1) on.
// Pass nil to disable cleanup tracking (e.g. in tests that don't care
// about Shutdown drain semantics). Must be called before the first
// dispatch; concurrent BindCleanup + Dispatch is a programming error.
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
func (d *Dispatcher) Dispatch(ctx context.Context, userID string, req *leapmuxv1.InnerRpcRequest, requestID uint32, inner *channelSender) {
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
		go func() {
			slog.Warn("unknown inner RPC method", "method", req.GetMethod())
			_ = w.SendError(int32(codes.Unimplemented), "unknown method: "+req.GetMethod())
		}()
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
// entry points.
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
			_ = w.SendError(int32(codes.Internal), "internal error")
		}
	}()
	sender := &Sender{inner: w}
	h.fn(ctx, userID, req, sender)
}
