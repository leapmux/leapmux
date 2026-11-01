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

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/worker/channel"
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

// inBandError rebuilds the error that a local handler answered in-band. The
// code must survive as a Connect code, because classifyHubError,
// isWorkerUnreachable and the other predicates test connect.CodeOf. A code
// that no decision reads becomes Unknown, which no predicate matches.
func TestInBandError_KeepsTheCodeThatADecisionReads(t *testing.T) {
	cases := []struct {
		name string
		code codes.Code
		want connect.Code
	}{
		{"not found", codes.NotFound, connect.CodeNotFound},
		{"permission denied", codes.PermissionDenied, connect.CodePermissionDenied},
		{"unauthenticated", codes.Unauthenticated, connect.CodeUnauthenticated},
		{"unavailable", codes.Unavailable, connect.CodeUnavailable},
		{"invalid argument", codes.InvalidArgument, connect.CodeInvalidArgument},
		{"unimplemented", codes.Unimplemented, connect.CodeUnimplemented},
		{"a code no decision reads", codes.Internal, connect.CodeUnknown},
		{"a code outside the enum", codes.Code(99), connect.CodeUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := inBandError(&leapmuxv1.RpcError{Code: int32(tc.code), Message: "worker said no"})
			require.Error(t, err)
			var connectErr *connect.Error
			require.True(t, errors.As(err, &connectErr), "a non-zero code builds a Connect error")
			assert.Equal(t, tc.want, connectErr.Code())
			assert.Equal(t, "worker said no", connectErr.Message())
		})
	}
}

// A zero code carries no classification, so the error is the message alone.
// It must not turn into a Connect error that a predicate could misread.
func TestInBandError_ZeroCodeKeepsOnlyTheMessage(t *testing.T) {
	err := inBandError(&leapmuxv1.RpcError{Message: "worker said no"})
	require.Error(t, err)
	assert.Equal(t, "worker said no", err.Error())
	var connectErr *connect.Error
	assert.False(t, errors.As(err, &connectErr), "a zero code must not build a Connect error")
}

// The hub's existence answer must reach the operator as `not_found` on the
// local-IPC transport too. Flattening the in-band error to its message made
// that code unreachable there.
func TestInBandError_ClassifiesAsNotFoundForTheHub(t *testing.T) {
	for _, code := range []codes.Code{codes.NotFound, codes.PermissionDenied} {
		err := inBandError(&leapmuxv1.RpcError{Code: int32(code), Message: "no such workspace"})
		assert.Equalf(t, "not_found", classifyHubError(err), "code %s", code)
	}
	assert.Equal(t, "rpc_failed", classifyHubError(inBandError(&leapmuxv1.RpcError{Code: int32(codes.Internal), Message: "boom"})))
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
