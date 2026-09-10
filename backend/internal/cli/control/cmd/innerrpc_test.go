package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/cli/control"
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
