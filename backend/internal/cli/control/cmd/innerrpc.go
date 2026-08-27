package cmd

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/tunnel"
)

// callInnerRPC dispatches an inner-RPC method to the appropriate
// transport: hub-bound clients open a fresh E2EE channel to the
// resolved worker; local-IPC clients route through CallInner on the
// per-agent socket.
//
// For hub-bound clients, workerID is required (use resolveWorker to
// derive it from --workspace-id + tab id when needed). Errors are
// returned via the JSON error envelope so they appear on stdout.
func callInnerRPC(ctx context.Context, c *control.Client, workerID, method string, in proto.Message, out proto.Message) error {
	if err := callInnerRPCBest(ctx, c, workerID, method, in, out); err != nil {
		return emitInnerRPCError(err)
	}
	return nil
}

// codedRPCError lets callInnerRPCBest stream a stable error code through
// the inner-RPC call site so callers that aggregate multiple invocations
// (e.g. workspace delete fan-out) can emit one envelope at the end while
// preserving the per-call code.
type codedRPCError struct {
	Code  string
	Cause error
}

func (e *codedRPCError) Error() string { return e.Cause.Error() }
func (e *codedRPCError) Unwrap() error { return e.Cause }

// callInnerRPCBest is the same dispatch as callInnerRPC but returns
// raw errors instead of emitting them. Used by orchestration commands
// (workspace delete, agent open rollback) that need to aggregate or
// react to per-call failures before producing a single result envelope.
func callInnerRPCBest(ctx context.Context, c *control.Client, workerID, method string, in proto.Message, out proto.Message) error {
	// Marshal BEFORE opening anything: a malformed request should not pay a
	// Noise_NK handshake to find that out.
	if _, err := proto.Marshal(in); err != nil {
		return &codedRPCError{Code: "marshal_failed", Cause: err}
	}
	return withWorkerChannel(ctx, c, workerID, func(w workerCall) error {
		return w.Call(ctx, method, in, out)
	})
}

// workerCall is a bound "issue an inner-RPC against this worker" value: it
// carries the client, the worker, and the transport to use, so a call site
// names only the method and its messages.
//
// It replaces a matrix of free functions that had grown one name per
// combination of three orthogonal axes -- channel-owned vs hoisted, bytes vs
// proto, raw error vs emitted envelope. Besides the naming, that shape had a
// real footgun: the hoisted helpers took a per-call workerID that the hub-bound
// path silently ignored (the channel is already bound to one worker), so a
// caller reusing a channel across two workers would have been routed to the
// first one with no error. Binding the worker into the value makes that
// unspellable.
//
// ctx stays a per-call ARGUMENT rather than a field: tab_spawn issues OpenAgent
// on an errgroup's derived context while its rollback and follow-up calls use
// the outer one, and a ctx-bound value would break that short-circuiting.
type workerCall struct {
	c        *control.Client
	workerID string
	// ch is the shared channel, when there is one. nil means "no shared
	// channel": either a local-IPC client (routed over the agent socket), or
	// the best-effort hub-bound case below, which opens one per call.
	ch *tunnel.Channel
	// perCallChannel marks a hub-bound caller that could not open a shared
	// channel and should fall back to opening one per call, rather than
	// misreading nil as local IPC.
	perCallChannel bool
}

// Call issues method and returns raw errors, preserving the stable code in a
// codedRPCError for callers that aggregate several invocations.
func (w workerCall) Call(ctx context.Context, method string, in, out proto.Message) error {
	if w.ch == nil && w.perCallChannel {
		return callInnerRPCBest(ctx, w.c, w.workerID, method, in, out)
	}
	payload, err := proto.Marshal(in)
	if err != nil {
		return &codedRPCError{Code: "marshal_failed", Cause: err}
	}
	if w.ch == nil {
		return localIPCCallInnerBest(ctx, w.c, w.workerID, method, payload, out)
	}
	resp, err := w.ch.CallRPC(ctx, method, payload)
	if err != nil {
		return &codedRPCError{Code: "rpc_failed", Cause: err}
	}
	if out != nil && len(resp.GetPayload()) > 0 {
		if err := proto.Unmarshal(resp.GetPayload(), out); err != nil {
			return &codedRPCError{Code: "unmarshal_failed", Cause: err}
		}
	}
	return nil
}

// CallEmit is Call with callInnerRPC's error handling: a failure becomes the
// JSON error envelope on stdout rather than a raw error, so hoisting a channel
// over a sequence that used to call callInnerRPC keeps the emitted codes
// identical.
func (w workerCall) CallEmit(ctx context.Context, method string, in, out proto.Message) error {
	if err := w.Call(ctx, method, in, out); err != nil {
		return emitInnerRPCError(err)
	}
	return nil
}

// withWorkerChannel opens ONE E2EE channel to workerID and invokes body with a
// workerCall bound to it, closing the channel when body returns. Multi-call
// sites use it to amortize the Noise_NK handshake across every call in body
// instead of paying it per call. On local-IPC clients there is no channel to
// share and body gets a socket-routed workerCall.
func withWorkerChannel(ctx context.Context, c *control.Client, workerID string, body func(w workerCall) error) error {
	if c.IsWorkerIPC() {
		return body(workerCall{c: c, workerID: workerID})
	}
	if workerID == "" {
		return &codedRPCError{Code: "invalid_request", Cause: errors.New("worker_id is required")}
	}
	ch, err := c.OpenE2EEChannel(ctx, ctx, workerID)
	if err != nil {
		return &codedRPCError{Code: "channel_open_failed", Cause: err}
	}
	defer ch.Close()
	return body(workerCall{c: c, workerID: workerID, ch: ch})
}

// withBestEffortWorkerChannel is withWorkerChannel for sequences that must run
// even when the worker is unreachable.
//
// `tab close` is the case: it has to be able to tombstone a tab whose worker is
// gone, so a failed channel open must not abort the command. body still gets a
// usable workerCall -- one that opens a channel per call and therefore
// surfaces the SAME channel_open_failed code isWorkerUnreachable keys on, which
// is what lets the CRDT-only fallback fire.
func withBestEffortWorkerChannel(ctx context.Context, c *control.Client, workerID string, body func(w workerCall) error) error {
	if c.IsWorkerIPC() || workerID == "" {
		return body(workerCall{c: c, workerID: workerID})
	}
	ch, err := c.OpenE2EEChannel(ctx, ctx, workerID)
	if err != nil {
		return body(workerCall{c: c, workerID: workerID, perCallChannel: true})
	}
	defer ch.Close()
	return body(workerCall{c: c, workerID: workerID, ch: ch})
}

// emitInnerRPCError converts an inner-RPC error into the JSON error
// envelope, preserving a codedRPCError's stable code. This is the
// error tail callInnerRPC and every hoisted-channel call site share.
func emitInnerRPCError(err error) error {
	var coded *codedRPCError
	if errors.As(err, &coded) {
		return control.EmitErrorWith(coded.Code, coded.Cause)
	}
	return control.EmitErrorWith("rpc_failed", err)
}

// localIPCCallInnerBest routes a worker-namespace call over the per-agent
// socket.
//
// WorkspaceId is deliberately left unset. The worker's router applies no
// workspace or bearer-scope check of its own (controlipc.Router.CallInner
// dispatches on the method name alone): the scope rung for a sibling dispatch
// is the DELEGATION bearer the cross-worker path mints, which the sibling
// worker's own gate enforces, and the local dispatch needs no rung because
// the caller already holds the agent's socket. The parameter used to exist
// and was passed "" at both call sites, which read as a live scoping
// decision that could never be made.
func localIPCCallInnerBest(ctx context.Context, c *control.Client, workerID, method string, payload []byte, out proto.Message) error {
	ipc, err := c.ControlIPCService()
	if err != nil {
		return &codedRPCError{Code: "invalid_request", Cause: err}
	}
	resp, err := ipc.CallInner(ctx, connect.NewRequest(&leapmuxv1.CallInnerRequest{
		Method:         "worker." + method,
		Payload:        payload,
		TargetWorkerId: workerID,
	}))
	if err != nil {
		// An existence/auth-class code means the target could not be reached at
		// all, which is the same condition the hub transport reports as
		// channel_open_failed. Tagging it identically is what lets
		// isWorkerUnreachable fire on BOTH transports: the CRDT-only tombstone
		// fallback in `tab close` was unreachable from inside a worker-spawned
		// agent for as long as this path could only answer rpc_failed. Requires
		// controlipc.relayError to have preserved the code -- a flat
		// CodeInternal wrap makes this branch dead again.
		if classifyConnectCode(err) {
			return &codedRPCError{Code: "channel_open_failed", Cause: err}
		}
		return &codedRPCError{Code: "rpc_failed", Cause: err}
	}
	if resp.Msg.GetIsError() {
		// dispatchLocal is the one path that fills the in-band envelope, and it
		// carries a grpc code in ErrorCode. Rebuild a coded error from it rather
		// than discarding it into a bare message string.
		return &codedRPCError{Code: "rpc_error", Cause: inBandError(resp.Msg)}
	}
	if out != nil && len(resp.Msg.GetPayload()) > 0 {
		if err := proto.Unmarshal(resp.Msg.GetPayload(), out); err != nil {
			return &codedRPCError{Code: "unmarshal_failed", Cause: err}
		}
	}
	return nil
}

// inBandError rebuilds an error from the CallInnerResponse envelope that
// dispatchLocal populates for a LOCAL handler failure.
//
// ErrorCode is a google.golang.org/grpc code (that is what dispatchLocal
// writes), NOT a connect.Code -- the two enums differ, so it is mapped rather
// than cast. A zero/unset code degrades to the bare message, which is what this
// used to do unconditionally.
func inBandError(msg *leapmuxv1.CallInnerResponse) error {
	cause := errors.New(msg.GetErrorMessage())
	if code := codes.Code(msg.GetErrorCode()); code != codes.OK {
		return connect.NewError(grpcToConnectCode(code), cause)
	}
	return cause
}

// grpcToConnectCode maps the grpc codes dispatchLocal emits onto the connect
// codes the CLI's predicates test. Only the codes that drive a decision are
// mapped; anything else stays Unknown, which no predicate matches.
func grpcToConnectCode(c codes.Code) connect.Code {
	switch c {
	case codes.NotFound:
		return connect.CodeNotFound
	case codes.PermissionDenied:
		return connect.CodePermissionDenied
	case codes.Unauthenticated:
		return connect.CodeUnauthenticated
	case codes.Unavailable:
		return connect.CodeUnavailable
	case codes.InvalidArgument:
		return connect.CodeInvalidArgument
	case codes.Unimplemented:
		return connect.CodeUnimplemented
	default:
		return connect.CodeUnknown
	}
}

// defaultInnerRPCTimeout caps a single inner-RPC dispatch (E2EE round
// trip or local-IPC CallInner). 30s is well above any expected
// worker-side latency for the RPCs the CLI uses today (workspace
// list, terminal open, file get) and well below CLI-level
// cancellation timeouts.
const defaultInnerRPCTimeout = 30 * time.Second

// rpcDeadline returns a context.Context with a default timeout
// unless cmd-level cancellation overrides.
func rpcDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, defaultInnerRPCTimeout)
}
