package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/tunnel"
)

// The error preserves its code and cause for callers that combine operation results.
func TestCodedRPCError_PreservesCodeAndCause(t *testing.T) {
	cause := errors.New("network unreachable")
	e := &codedRPCError{Code: "channel_open_failed", Cause: cause}

	assert.Equal(t, cause.Error(), e.Error())
	assert.Equal(t, "channel_open_failed", e.Code)
	assert.Same(t, cause, e.Unwrap())
}

// errors.As finds the coded error through an additional wrapper.
func TestCodedRPCError_ErrorsAsUnwrapsThroughChain(t *testing.T) {
	inner := &codedRPCError{Code: "rpc_failed", Cause: errors.New("boom")}
	wrapped := fmt.Errorf("context: %w", inner)

	var found *codedRPCError
	require.True(t, errors.As(wrapped, &found))
	assert.Equal(t, "rpc_failed", found.Code)
}

// errors.Is finds the original cause through the coded error.
func TestCodedRPCError_ErrorsIsThroughChain(t *testing.T) {
	cause := errors.New("not found")
	e := &codedRPCError{Code: "not_found", Cause: cause}

	assert.True(t, errors.Is(e, cause), "errors.Is should descend into Cause")
}

// Formatting an absent cause must preserve the code without a nil dereference.
func TestCodedRPCError_NilCauseUsesCode(t *testing.T) {
	cases := []struct {
		name string
		code string
	}{
		{name: "code only", code: "rpc_failed"},
		{name: "zero value"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := &codedRPCError{Code: tc.code}
			var message string
			require.NotPanics(t, func() { message = e.Error() })
			assert.Equal(t, tc.code, message)
			assert.Nil(t, e.Unwrap())

			wrapped := fmt.Errorf("context: %w", e)
			assert.Equal(t, "context: "+tc.code, wrapped.Error())
			var found *codedRPCError
			require.True(t, errors.As(wrapped, &found))
			assert.Same(t, e, found)
		})
	}
}

func TestEmitInnerRPCError(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		code    string
		message string
	}{
		{
			name:    "coded cause",
			err:     &codedRPCError{Code: "channel_open_failed", Cause: errors.New("worker unreachable")},
			code:    "channel_open_failed",
			message: "worker unreachable",
		},
		{
			name: "empty cause message",
			err:  &codedRPCError{Code: "channel_open_failed", Cause: errors.New("")},
			code: "channel_open_failed",
		},
		{
			name:    "absent cause",
			err:     &codedRPCError{Code: "channel_open_failed"},
			code:    "channel_open_failed",
			message: "channel_open_failed",
		},
		{
			name:    "wrapped absent cause",
			err:     fmt.Errorf("context: %w", &codedRPCError{Code: "channel_open_failed"}),
			code:    "channel_open_failed",
			message: "channel_open_failed",
		},
		{
			name:    "uncoded cause",
			err:     errors.New("request failed"),
			code:    "rpc_failed",
			message: "request failed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := withCapturedStdout(t, func() {
				require.NotPanics(t, func() {
					err := emitInnerRPCError(tc.err)
					require.Error(t, err)
					assert.True(t, control.IsEmitted(err))
					assert.Equal(t, tc.code+": "+tc.message, err.Error())
				})
			})
			var envelope struct {
				Error map[string]string `json:"error"`
			}
			require.NoError(t, json.Unmarshal(out, &envelope))
			assert.Equal(t, map[string]string{"code": tc.code, "message": tc.message}, envelope.Error)
		})
	}
}

// inBandError reports an in-band error as a Connect error with the worker's
// code: classifyHubError, isWorkerUnreachable and the other predicates test
// connect.CodeOf, and the operator reads the code in the printed message. The
// mapping from a google.rpc.Code to a Connect code is RPCError.ConnectError's,
// which the channelwire tests pin value by value.
func TestInBandError_KeepsTheWorkersCode(t *testing.T) {
	for name, err := range map[string]error{
		"as returned":  channelwire.ErrorOf(&leapmuxv1.RpcError{Code: int32(codes.NotFound), Message: "worker said no"}),
		"when wrapped": fmt.Errorf("call: %w", channelwire.ErrorOf(&leapmuxv1.RpcError{Code: int32(codes.NotFound), Message: "worker said no"})),
	} {
		t.Run(name, func(t *testing.T) {
			reported := inBandError(err)
			var connectErr *connect.Error
			require.ErrorAs(t, reported, &connectErr)
			assert.Equal(t, connect.CodeNotFound, connectErr.Code())
			assert.Equal(t, "worker said no", connectErr.Message())
			assert.Equal(t, "not_found: worker said no", reported.Error(), "the operator reads the worker's code")
			var rpcErr *channelwire.RPCError
			assert.ErrorAs(t, reported, &rpcErr, "the in-band error stays the cause")
		})
	}
}

// An error that holds no in-band error is the call failing, not the worker
// answering, so inBandError leaves it as it is.
func TestInBandError_ReturnsAnyOtherErrorUnchanged(t *testing.T) {
	plain := errors.New("channel closed")
	assert.Same(t, plain, inBandError(plain))
	refused := connect.NewError(connect.CodeUnavailable, "hub unavailable")
	assert.Same(t, refused, inBandError(refused))
}

// A worker refusal reaches the operator with one code, whichever transport
// carried it. The E2EE channel returns the worker's in-band error as a
// *channelwire.RPCError; it must classify as rpc_error with the worker's code,
// as the local-IPC path classifies the same refusal.
func TestChannelRPCError_KeepsTheWorkersRefusal(t *testing.T) {
	err := channelRPCError(fmt.Errorf("call: %w", &channelwire.RPCError{Code: int32(codes.NotFound), Message: "no such agent"}))
	assert.Equal(t, "rpc_error", err.Code)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	assert.Equal(t, "not_found: no such agent", err.Cause.Error())
}

// A response that the tunnel channel on this side rejects reaches CallRPC as the
// same type as a worker's refusal, so it reads as rpc_error too, and the
// operator tells the two apart by the code in the message.
func TestChannelRPCError_ReportsAResponseThatTheChannelRejected(t *testing.T) {
	err := channelRPCError(&channelwire.RPCError{Code: int32(codes.ResourceExhausted), Message: "message too large"})
	assert.Equal(t, "rpc_error", err.Code)
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	assert.Equal(t, "resource_exhausted: message too large", err.Error())
	assert.False(t, isWorkerUnreachable(err), "a rejected response came from a reachable worker")
}

// Any other failure of the call is the call failing, not the worker refusing.
func TestChannelRPCError_ReportsAFailedCall(t *testing.T) {
	cause := errors.New("channel closed")
	err := channelRPCError(cause)
	assert.Equal(t, "rpc_failed", err.Code)
	assert.ErrorIs(t, err, cause)
}

// An in-band error whose code states no failure (OK, 0) is still a failure,
// reported as Unknown. No predicate reads Unknown as a specific failure, so it
// can neither read as `not_found` nor allow the CRDT-only fallback.
func TestInBandError_ZeroCodeIsUnknown(t *testing.T) {
	err := inBandError(channelwire.ErrorOf(&leapmuxv1.RpcError{Message: "worker said no"}))
	assert.Equal(t, connect.CodeUnknown, connect.CodeOf(err))
	assert.Equal(t, "unknown: worker said no", err.Error())
	assert.Equal(t, "rpc_failed", classifyHubError(err))
	assert.False(t, isWorkerUnreachable(err))
}

// The hub's existence answer must reach the operator as `not_found` on the
// local-IPC transport too. Flattening the in-band error to its message made
// that code unreachable there.
func TestInBandError_ClassifiesAsNotFoundForTheHub(t *testing.T) {
	for _, code := range []codes.Code{codes.NotFound, codes.PermissionDenied} {
		err := inBandError(channelwire.ErrorOf(&leapmuxv1.RpcError{Code: int32(code), Message: "no such workspace"}))
		assert.Equalf(t, "not_found", classifyHubError(err), "code %s", code)
	}
	assert.Equal(t, "rpc_failed", classifyHubError(inBandError(channelwire.ErrorOf(&leapmuxv1.RpcError{Code: int32(codes.Internal), Message: "boom"}))))
}

// inBandErrorDispatcher stands in for a worker handler that refuses every
// call in-band, with one code and one message.
type inBandErrorDispatcher struct {
	code    codes.Code
	message string
}

func (d inBandErrorDispatcher) DispatchWith(_ context.Context, _ channel.Caller, _ *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
	_ = w.SendError(int32(d.code), d.message)
}

// A worker handler that refuses a call in-band reaches the caller over the
// real local-IPC router with its code intact, under the `rpc_error` envelope
// code. A caller that combines results can then still classify the refusal.
func TestLocalIPCCall_KeepsTheCodeOfAnInBandRefusal(t *testing.T) {
	startSpawnIPC(t, &recordingHub{}, inBandErrorDispatcher{code: codes.NotFound, message: "no such terminal"})
	c, err := requireClient("")
	require.NoError(t, err)
	require.True(t, c.IsWorkerIPC(), "the spawn harness must select the local-IPC transport")

	var resp leapmuxv1.ListAvailableShellsResponse
	err = callInnerRPCBest(t.Context(), c, "worker-A",
		leapmuxv1connect.WorkerTerminalServiceListAvailableShellsProcedure, &leapmuxv1.ListAvailableShellsRequest{}, &resp)
	require.Error(t, err)

	var coded *codedRPCError
	require.True(t, errors.As(err, &coded), "unexpected error type %T", err)
	assert.Equal(t, "rpc_error", coded.Code)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err), "the in-band code must survive the router")
	assert.Contains(t, err.Error(), "no such terminal")
	assert.False(t, isWorkerUnreachable(err), "a refusal from a reachable worker must never allow the CRDT-only fallback")

	out := withCapturedStdout(t, func() {
		require.True(t, control.IsEmitted(emitInnerRPCError(err)))
	})
	var envelope struct {
		Error map[string]string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &envelope))
	assert.Equal(t, "rpc_error", envelope.Error["code"])
	assert.Contains(t, envelope.Error["message"], "no such terminal")
}

// A worker that refuses a call with NotFound reaches the operator as one
// envelope, whichever transport carried the call: the same `rpc_error` code and
// the same message, over the E2EE channel and over local IPC alike.
//
// The local-IPC half runs through the real control-IPC router. The E2EE half
// starts at the error that tunnel.Channel.CallRPC returns for the worker's
// in-band answer, channelwire.ErrorOf of its RpcError: a CLI test cannot stand
// up a Noise session to a worker, and the tunnel tests pin that CallRPC
// returns exactly that error.
func TestWorkerRefusal_EmitsOneEnvelopeOnBothTransports(t *testing.T) {
	const message = "no such terminal"
	emitted := func(t *testing.T, err error) map[string]string {
		t.Helper()
		out := withCapturedStdout(t, func() {
			require.True(t, control.IsEmitted(emitInnerRPCError(err)))
		})
		var envelope struct {
			Error map[string]string `json:"error"`
		}
		require.NoError(t, json.Unmarshal(out, &envelope))
		return envelope.Error
	}

	// The same refusal, as CallRPC returns it, and through channelRPCError
	// as workerCall.Call reports it.
	overChannel := emitted(t, channelRPCError(channelwire.ErrorOf(&leapmuxv1.RpcError{Code: int32(codes.NotFound), Message: message})))

	startSpawnIPC(t, &recordingHub{}, inBandErrorDispatcher{code: codes.NotFound, message: message})
	c, err := requireClient("")
	require.NoError(t, err)
	require.True(t, c.IsWorkerIPC(), "the spawn harness must select the local-IPC transport")
	var resp leapmuxv1.ListAvailableShellsResponse
	ipcErr := callInnerRPCBest(t.Context(), c, "worker-A",
		leapmuxv1connect.WorkerTerminalServiceListAvailableShellsProcedure, &leapmuxv1.ListAvailableShellsRequest{}, &resp)
	require.Error(t, ipcErr)
	overIPC := emitted(t, ipcErr)

	assert.Equal(t, map[string]string{"code": "rpc_error", "message": "not_found: " + message}, overChannel)
	assert.Equal(t, overChannel, overIPC, "both transports emit one envelope for one refusal")
}

// withWorkerChannel gives its body a channel that lives until the body returns,
// not until ctx ends. A rollback in the body runs after ctx ended, and it needs
// the channel that carried the open. The channel closes when the body returns,
// because nothing else owns it.
func TestWithWorkerChannel_ChannelLivesUntilTheBodyReturns(t *testing.T) {
	disp := &rollbackDispatcher{}
	harness := startHubChannelHarness(t, &recordingHub{}, disp)
	c, err := requireClient(harness.url)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var shared *tunnel.Channel
	err = withWorkerChannel(ctx, c, "worker-A", func(w workerCall) error {
		shared = w.ch
		cancel()
		return w.Call(t.Context(), leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{
			Tab: &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "agent-9"},
		}, nil)
	})
	require.NoError(t, err, "the end of ctx must not end the channel")

	_, closes := disp.recorded()
	require.Len(t, closes, 1, "the call reached the worker after ctx ended")
	assert.Equal(t, "agent-9", closes[0].GetTab().GetTabId())
	require.NotNil(t, shared)
	assert.True(t, shared.Closed(), "the channel closes when the body returns")
}

// The default deadline prevents an unresponsive hub from blocking the CLI indefinitely.
func TestRpcDeadline_HasFiniteDeadline(t *testing.T) {
	ctx, cancel := rpcDeadline(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	require.True(t, ok, "rpcDeadline must attach a deadline")
	assert.WithinDuration(t, time.Now().Add(30*time.Second), deadline, 5*time.Second)
}

// Parent cancellation must stop the child context before the default deadline.
func TestRpcDeadline_HonoursParentCancellation(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	ctx, cancel := rpcDeadline(parent)
	defer cancel()

	cancelParent()
	select {
	case <-ctx.Done():
		// expected
	case <-time.After(time.Second):
		t.Fatal("child context did not honour parent cancellation")
	}
}
