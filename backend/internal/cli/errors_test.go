package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExitCode(t *testing.T) {
	cases := map[Code]int{
		CodeUsage:             ExitUsage,
		CodeInvalidArgument:   ExitFailure,
		CodeNotLoggedIn:       ExitAuth,
		CodePermissionDenied:  ExitAuth,
		CodeNotFound:          ExitNotFound,
		CodeAmbiguousTarget:   ExitNotFound,
		CodeConflict:          ExitFailure,
		CodeSelfTargetRefused: ExitRefused,
		CodeTabBusyRefused:    ExitRefused,
		CodeAgentWaiting:      ExitRefused,
		CodeTimeout:           ExitWaitEnded,
		CodeWorkerOffline:     ExitUnavailable,
		CodeUnavailable:       ExitUnavailable,
		CodeVersionMismatch:   ExitFailure,
		CodeInternal:          ExitFailure,
		Code("unknown"):       ExitFailure,
	}
	for code, want := range cases {
		assert.Equal(t, want, ExitCode(code), code)
	}
}

func TestErrorWithExitCodeReplacesTheCodesStatus(t *testing.T) {
	e := Newf(CodeTimeout, "the agent stopped at a prompt").WithExitCode(ExitRefused)
	assert.Equal(t, ExitRefused, e.ExitCode())
	assert.Equal(t, ExitWaitEnded, Newf(CodeTimeout, "x").ExitCode())
}

func TestErrorRetryableOnlyForTransientCodes(t *testing.T) {
	assert.True(t, Newf(CodeUnavailable, "x").Retryable())
	assert.True(t, Newf(CodeWorkerOffline, "x").Retryable())
	assert.False(t, Newf(CodeNotFound, "x").Retryable())
	assert.False(t, Usagef("x").Retryable())
}

func TestFromError(t *testing.T) {
	t.Run("keeps an Error as it is", func(t *testing.T) {
		e := Newf(CodeTabBusyRefused, "busy").WithHint("pass --allow-busy")
		got := FromError(fmt.Errorf("wrapped: %w", e))
		assert.Same(t, e, got)
	})
	t.Run("returns nil for nil", func(t *testing.T) {
		assert.Nil(t, FromError(nil))
	})
	t.Run("maps each connect code", func(t *testing.T) {
		cases := map[connect.Code]Code{
			connect.CodeNotFound:           CodeNotFound,
			connect.CodePermissionDenied:   CodePermissionDenied,
			connect.CodeUnauthenticated:    CodeNotLoggedIn,
			connect.CodeInvalidArgument:    CodeInvalidArgument,
			connect.CodeOutOfRange:         CodeInvalidArgument,
			connect.CodeAlreadyExists:      CodeConflict,
			connect.CodeAborted:            CodeConflict,
			connect.CodeFailedPrecondition: CodeConflict,
			connect.CodeUnavailable:        CodeUnavailable,
			connect.CodeResourceExhausted:  CodeUnavailable,
			connect.CodeDeadlineExceeded:   CodeUnavailable,
			connect.CodeUnimplemented:      CodeVersionMismatch,
			connect.CodeInternal:           CodeInternal,
			connect.CodeUnknown:            CodeInternal,
			connect.CodeDataLoss:           CodeInternal,
		}
		for in, want := range cases {
			got := FromError(fmt.Errorf("get workspace: %w", connect.NewError(in, "boom")))
			assert.Equal(t, want, got.Code, in.String())
			assert.Equal(t, "get workspace: boom", got.Message,
				"the message keeps the context that callers wrapped, and the envelope states the code once")
		}
	})
	t.Run("keeps the code of a Connect error with no message", func(t *testing.T) {
		got := FromError(connect.NewError(connect.CodeUnavailable, ""))
		assert.Equal(t, CodeUnavailable, got.Code)
		assert.NotEmpty(t, got.Message, "a message must never be empty")
	})
	t.Run("hints the login for an unauthenticated call", func(t *testing.T) {
		got := FromError(connect.NewError(connect.CodeUnauthenticated, "expired"))
		require.NotEmpty(t, got.Hints)
		assert.Contains(t, got.Hints[0], "auth login")
	})
	t.Run("hints the version comparison for an unimplemented call", func(t *testing.T) {
		got := FromError(connect.NewError(connect.CodeUnimplemented, "no such method"))
		require.NotEmpty(t, got.Hints)
		assert.Contains(t, got.Hints[0], "version")
	})
	t.Run("classifies a not-logged-in sentinel", func(t *testing.T) {
		got := FromError(fmt.Errorf("%w to this hub", ErrNotLoggedIn))
		assert.Equal(t, CodeNotLoggedIn, got.Code)
		assert.NotEmpty(t, got.Hints)
	})
	t.Run("asks a classifier", func(t *testing.T) {
		got := FromError(fmt.Errorf("outer: %w", classified{}))
		assert.Equal(t, CodeWorkerOffline, got.Code)
	})
	t.Run("ignores a classifier that returns nil", func(t *testing.T) {
		got := FromError(nilClassifier{})
		assert.Equal(t, CodeInternal, got.Code)
	})
	t.Run("maps a refused dial to unavailable", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		addr := ln.Addr().String()
		require.NoError(t, ln.Close())
		_, dialErr := http.Get("http://" + addr + "/")
		require.Error(t, dialErr)

		got := FromError(fmt.Errorf("post the form: %w", dialErr))
		assert.Equal(t, CodeUnavailable, got.Code)
		assert.True(t, got.Retryable(), "a peer that is down now may be up on the next try")
		assert.Contains(t, got.Message, "post the form", "the message keeps the context of the caller")
	})
	t.Run("maps a network timeout to unavailable", func(t *testing.T) {
		got := FromError(&url.Error{Op: "Post", URL: "http://hub", Err: timeoutError{}})
		assert.Equal(t, CodeUnavailable, got.Code)
	})
	t.Run("keeps a request that never reached the network internal", func(t *testing.T) {
		_, err := http.Get("ftp://hub.example/")
		require.Error(t, err)
		var netErr net.Error
		require.ErrorAs(t, err, &netErr, "the case under test is a net.Error that is not a network failure")
		assert.Equal(t, CodeInternal, FromError(err).Code)
	})
	t.Run("maps a context deadline to unavailable", func(t *testing.T) {
		got := FromError(fmt.Errorf("call: %w", context.DeadlineExceeded))
		assert.Equal(t, CodeUnavailable, got.Code)
		assert.True(t, got.Retryable())
	})
	t.Run("maps a canceled context to internal", func(t *testing.T) {
		assert.Equal(t, CodeInternal, FromError(context.Canceled).Code)
	})
	t.Run("maps anything else to internal and keeps the cause", func(t *testing.T) {
		cause := errors.New("disk full")
		got := FromError(cause)
		assert.Equal(t, CodeInternal, got.Code)
		assert.ErrorIs(t, got, cause)
	})
}

// classified is an error that states its own envelope.
type classified struct{}

func (classified) Error() string    { return "classified" }
func (classified) CLIError() *Error { return Newf(CodeWorkerOffline, "offline") }

// nilClassifier is a classifier that declines to classify.
type nilClassifier struct{}

func (nilClassifier) Error() string    { return "nil classifier" }
func (nilClassifier) CLIError() *Error { return nil }

func TestWrapKeepsTheCauseAndPrefixesTheMessage(t *testing.T) {
	cause := errors.New("boom")
	got := Wrap(CodeConflict, cause, "submit batch")
	assert.Equal(t, "submit batch: boom", got.Message)
	assert.ErrorIs(t, got, cause)
	assert.Equal(t, "boom", Wrap(CodeConflict, cause, "").Message)
}

// timeoutError is a net.Error that reports a timeout.
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }
