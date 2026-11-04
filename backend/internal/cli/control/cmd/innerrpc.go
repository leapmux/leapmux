package cmd

import (
	"context"
	"errors"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/tunnel"
)

// callInnerRPC selects the transport for an inner remote procedure call (RPC).
// Hub clients open a new channel with end-to-end encryption (E2EE) to the resolved worker.
// Clients that use local interprocess communication (IPC) call CallInner on the agent's socket.
//
// Hub clients require workerID. resolveWorker derives it from --workspace-id and the tab ID when necessary.
// The JSON error envelope sends failures to stdout.
func callInnerRPC(ctx context.Context, c *control.Client, workerID, method string, in proto.Message, out proto.Message) error {
	if err := callInnerRPCBest(ctx, c, workerID, method, in, out); err != nil {
		return emitInnerRPCError(err)
	}
	return nil
}

// codedRPCError preserves the error code for each call.
// Commands that combine operation results can emit one envelope with the original error code.
// Error returns the code when the cause is nil.
type codedRPCError struct {
	Code  string
	Cause error
}

func (e *codedRPCError) Error() string {
	if e.Cause == nil {
		return e.Code
	}
	return e.Cause.Error()
}

func (e *codedRPCError) Unwrap() error { return e.Cause }

// callInnerRPCBest selects the same transport as callInnerRPC and returns errors without emitting them.
// Commands such as workspace deletion and agent rollback combine failures before producing one result envelope.
func callInnerRPCBest(ctx context.Context, c *control.Client, workerID, method string, in proto.Message, out proto.Message) error {
	// Validate serialization before a malformed request can start a Noise_NK handshake.
	if _, err := proto.Marshal(in); err != nil {
		return &codedRPCError{Code: "marshal_failed", Cause: err}
	}
	return withWorkerChannel(ctx, c, workerID, func(w workerCall) error {
		return w.Call(ctx, method, in, out)
	})
}

// workerCall binds the client and transport to one worker.
// Callers supply only the method and its messages.
//
// A shared channel already belongs to one worker. A per-call workerID could incorrectly suggest that the channel can switch workers.
// This type prevents callers from supplying a different workerID for a shared channel.
//
// Each call takes its own context. tab_spawn uses the operation's context for OpenAgent and a detached context for its rollback.
// Storing one context would prevent these calls from using the required cancellation behavior.
type workerCall struct {
	c        *control.Client
	workerID string
	// ch holds the shared channel. A nil channel selects local IPC or the per-call fallback for a hub client.
	ch *tunnel.Channel
	// perCallChannel selects a new channel for each call after a hub client fails to open a shared channel.
	// It prevents a missing shared channel from selecting local IPC incorrectly.
	perCallChannel bool
}

// Call invokes method and returns errors with their original codes for callers that combine operation results.
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
		return channelRPCError(err)
	}
	if out != nil && len(resp.GetPayload()) > 0 {
		if err := proto.Unmarshal(resp.GetPayload(), out); err != nil {
			return &codedRPCError{Code: "unmarshal_failed", Cause: err}
		}
	}
	return nil
}

// channelRPCError classifies an error of a call over the E2EE channel. A
// *channelwire.RPCError keeps its code and reads as rpc_error, exactly as
// localIPCCallInnerBest reports a refusal, so the operator and every predicate
// that reads the code see one answer whichever transport carried the call. Any
// other error is the call failing.
//
// That type has two sources. One is a refusal that the worker answered in-band.
// The other is a response that the tunnel channel on this side rejected, such
// as one over the negotiated message size, where the worker refused nothing.
// CallRPC returns both as one type, and the worker answers the same size
// conditions with the same ResourceExhausted code, so this function cannot
// separate them. RPCHandlers.StreamRejected marks a local rejection for a stream
// handler only, and CallRPC registers none.
func channelRPCError(err error) *codedRPCError {
	if errors.As(err, new(*channelwire.RPCError)) {
		return &codedRPCError{Code: "rpc_error", Cause: inBandError(err)}
	}
	return &codedRPCError{Code: "rpc_failed", Cause: err}
}

// CallEmit invokes Call and emits the same JSON error envelope as callInnerRPC.
// Sharing a channel across multiple calls preserves the emitted error codes.
func (w workerCall) CallEmit(ctx context.Context, method string, in, out proto.Message) error {
	if err := w.Call(ctx, method, in, out); err != nil {
		return emitInnerRPCError(err)
	}
	return nil
}

// withWorkerChannel opens one E2EE channel to workerID and gives body a workerCall bound to that channel.
// It closes the channel when body returns. Calls within body share one Noise_NK handshake.
// Local IPC clients use the agent's socket without opening a channel.
//
// The channel lives until body returns, not until ctx ends. Each call in body takes its own context.
// A rollback in body (closeOrphanTab) runs after ctx ended and needs the channel to still be open.
func withWorkerChannel(ctx context.Context, c *control.Client, workerID string, body func(w workerCall) error) error {
	if c.IsWorkerIPC() {
		return body(workerCall{c: c, workerID: workerID})
	}
	if workerID == "" {
		return &codedRPCError{Code: "invalid_request", Cause: errors.New("worker_id is required")}
	}
	ch, err := c.OpenE2EEChannel(ctx, context.WithoutCancel(ctx), workerID)
	if err != nil {
		return &codedRPCError{Code: "channel_open_failed", Cause: err}
	}
	defer ch.Close()
	return body(workerCall{c: c, workerID: workerID, ch: ch})
}

// withBestEffortWorkerChannel is withWorkerChannel for sequences that must run
// even when the worker is unreachable.
//
// The tab close command must delete a tab even when its worker is unreachable.
// A failed shared channel therefore selects a new channel for each call.
// This preserves the channel_open_failed code that isWorkerUnreachable checks.
// That code permits the fallback that deletes the tab through the conflict-free replicated data type (CRDT) only.
//
// The channel closes when body returns, so every call of the sequence must run inside body.
// Unlike withWorkerChannel, the end of ctx also ends the channel, because no caller runs a rollback after ctx ended.
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

// emitInnerRPCError converts an inner RPC error into the JSON error envelope and preserves its code.
// callInnerRPC and callers that share a channel use this error handler.
func emitInnerRPCError(err error) error {
	var coded *codedRPCError
	if errors.As(err, &coded) {
		return control.EmitErrorWith(coded.Code, coded)
	}
	return control.EmitErrorWith("rpc_failed", err)
}

// localIPCCallInnerBest routes a call in the worker namespace through the agent's socket.
//
// The request carries no workspace, because controlipc.Router.CallInner selects the handler by method alone.
// Calls to another worker use a DELEGATION bearer token, which that worker validates.
// Local calls require no additional token because the caller already holds the agent's socket.
func localIPCCallInnerBest(ctx context.Context, c *control.Client, workerID, method string, payload []byte, out proto.Message) error {
	ipc, err := c.ControlIPCService()
	if err != nil {
		return &codedRPCError{Code: "invalid_request", Cause: err}
	}
	resp, err := ipc.CallInner(ctx, &leapmuxv1.CallInnerRequest{
		Method:         "worker." + method,
		Payload:        payload,
		TargetWorkerId: workerID,
	})
	if err != nil {
		// These connection or authorization codes mean that the call could not reach the target.
		// Match the hub transport's channel_open_failed code so isWorkerUnreachable permits the CRDT-only fallback on both transports.
		// controlipc.relayError must preserve the original code. Replacing it with CodeInternal prevents this fallback.
		if classifyConnectCode(err) {
			return &codedRPCError{Code: "channel_open_failed", Cause: err}
		}
		return &codedRPCError{Code: "rpc_failed", Cause: err}
	}
	if err := channelwire.ErrorOf(resp.GetError()); err != nil {
		// The worker answers a refusal in-band, with its code: the local
		// worker through dispatchLocal, and a sibling worker through
		// Router.CallInner, which keeps the sibling's in-band error. Report
		// the error with that code so callers can classify it.
		return &codedRPCError{Code: "rpc_error", Cause: inBandError(err)}
	}
	if out != nil && len(resp.GetPayload()) > 0 {
		if err := proto.Unmarshal(resp.GetPayload(), out); err != nil {
			return &codedRPCError{Code: "unmarshal_failed", Cause: err}
		}
	}
	return nil
}

// inBandError returns err as the error that the CLI reports when err holds a
// *channelwire.RPCError, the error that a worker or the hub answered in-band:
// a Connect error with the same code and message (RPCError.ConnectError).
// classifyHubError, isWorkerUnreachable and the other predicates read
// connect.CodeOf, so they then read one code whichever transport carried the
// call. Any other err returns unchanged.
func inBandError(err error) error {
	var refusal *channelwire.RPCError
	if errors.As(err, &refusal) {
		return refusal.ConnectError()
	}
	return err
}

// defaultInnerRPCTimeout limits one inner RPC through an E2EE channel or local IPC.
// The limit exceeds expected worker latency and remains below the CLI cancellation timeouts.
const defaultInnerRPCTimeout = 30 * time.Second

// rpcDeadline returns a context with the default timeout. Parent cancellation can stop it sooner.
func rpcDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, defaultInnerRPCTimeout)
}
