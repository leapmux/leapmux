package cmd

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/remote"
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
func callInnerRPC(ctx context.Context, c *remote.Client, workerID, method string, in proto.Message, out proto.Message) error {
	if err := callInnerRPCBest(ctx, c, workerID, method, in, out); err != nil {
		var coded *codedRPCError
		if errors.As(err, &coded) {
			return remote.EmitErrorWith(coded.Code, coded.Cause)
		}
		return remote.EmitErrorWith("rpc_failed", err)
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
func callInnerRPCBest(ctx context.Context, c *remote.Client, workerID, method string, in proto.Message, out proto.Message) error {
	payload, err := proto.Marshal(in)
	if err != nil {
		return &codedRPCError{Code: "marshal_failed", Cause: err}
	}
	if c.IsLocal() {
		return localIPCCallInnerBest(ctx, c, workerID, "", method, payload, out)
	}
	if workerID == "" {
		return &codedRPCError{Code: "invalid_request", Cause: errors.New("worker_id is required")}
	}
	ch, err := c.OpenE2EEChannel(ctx, ctx, workerID)
	if err != nil {
		return &codedRPCError{Code: "channel_open_failed", Cause: err}
	}
	defer ch.Close()

	return callInnerRPCOnChannel(ctx, ch, c, workerID, method, payload, out)
}

// callInnerRPCOnChannel issues `method` on an already-open E2EE
// channel, bypassing the Noise_NK handshake cost that callInnerRPC
// pays per call. Multi-call sites (e.g. `git status` issuing
// `GetGitInfo` + `GetGitFileStatus`; `openAgentAndAddTab` issuing
// `OpenAgent` + `UpdateAgentSettings` + `SendAgentMessage`) save
// roughly one handshake round-trip per extra call by hoisting one
// `OpenE2EEChannel` over the whole sequence.
//
// On local-IPC clients there is no channel to share, so callers that
// can't tell the transport apart use this helper with `ch == nil` and
// it falls through to the per-call CallInner path. Hub-bound clients
// MUST pass a live channel.
func callInnerRPCOnChannel(ctx context.Context, ch *tunnel.Channel, c *remote.Client, workerID, method string, payload []byte, out proto.Message) error {
	if ch == nil {
		return localIPCCallInnerBest(ctx, c, workerID, "", method, payload, out)
	}
	resp, err := ch.CallRPC(ctx, method, payload)
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

// withWorkerChannel opens one E2EE channel to `workerID` and invokes
// `body` with it (or `nil` on local-IPC clients, where no channel is
// involved). The channel is closed when `body` returns. Multi-call
// sites use this wrapper to amortize the Noise_NK handshake across
// every call in `body` instead of paying it per call.
func withWorkerChannel(ctx context.Context, c *remote.Client, workerID string, body func(ch *tunnel.Channel) error) error {
	if c.IsLocal() {
		return body(nil)
	}
	if workerID == "" {
		return &codedRPCError{Code: "invalid_request", Cause: errors.New("worker_id is required")}
	}
	ch, err := c.OpenE2EEChannel(ctx, ctx, workerID)
	if err != nil {
		return &codedRPCError{Code: "channel_open_failed", Cause: err}
	}
	defer ch.Close()
	return body(ch)
}

// callInnerRPCOnChannelMarshal is the marshal-aware twin of
// callInnerRPCOnChannel: callers pass a proto.Message instead of
// pre-marshaled bytes.
func callInnerRPCOnChannelMarshal(ctx context.Context, ch *tunnel.Channel, c *remote.Client, workerID, method string, in proto.Message, out proto.Message) error {
	payload, err := proto.Marshal(in)
	if err != nil {
		return &codedRPCError{Code: "marshal_failed", Cause: err}
	}
	return callInnerRPCOnChannel(ctx, ch, c, workerID, method, payload, out)
}

func localIPCCallInnerBest(ctx context.Context, c *remote.Client, workerID, workspaceID, method string, payload []byte, out proto.Message) error {
	resp, err := c.RemoteIPCService().CallInner(ctx, connect.NewRequest(&leapmuxv1.CallInnerRequest{
		Method:         "worker." + method,
		Payload:        payload,
		TargetWorkerId: workerID,
		WorkspaceId:    workspaceID,
	}))
	if err != nil {
		return &codedRPCError{Code: "rpc_failed", Cause: err}
	}
	if resp.Msg.GetIsError() {
		return &codedRPCError{Code: "rpc_error", Cause: errors.New(resp.Msg.GetErrorMessage())}
	}
	if out != nil && len(resp.Msg.GetPayload()) > 0 {
		if err := proto.Unmarshal(resp.Msg.GetPayload(), out); err != nil {
			return &codedRPCError{Code: "unmarshal_failed", Cause: err}
		}
	}
	return nil
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
