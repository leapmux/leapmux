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
	// The next step after a refused call depends on the command, so the
	// classifier adds none; the tree of the command does.
	t.Run("adds no hint to a Connect error", func(t *testing.T) {
		for _, code := range []connect.Code{connect.CodeUnauthenticated, connect.CodeUnimplemented, connect.CodePermissionDenied} {
			got := FromError(connect.NewError(code, "refused"))
			assert.Empty(t, got.Hints, code.String())
		}
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
	t.Run("outer classifier wins over wrapped Error", func(t *testing.T) {
		inner := Newf(CodeInternal, "inner failure")
		got := FromError(wrappingClassifier{err: inner})
		assert.Equal(t, CodeWorkerOffline, got.Code)
		assert.Equal(t, "outer offline", got.Message)
	})
	t.Run("outer Error wins over wrapped classifier", func(t *testing.T) {
		outer := Wrap(CodeConflict, classified{}, "outer conflict")
		got := FromError(outer)
		assert.Same(t, outer, got)
		assert.Equal(t, CodeConflict, got.Code)
	})
	t.Run("nil classifier falls through to wrapped Error", func(t *testing.T) {
		inner := Newf(CodeNotFound, "missing")
		got := FromError(wrappingNilClassifier{err: inner})
		assert.Same(t, inner, got)
	})
	t.Run("HintProvider attaches hints without dropping outer context", func(t *testing.T) {
		rpcErr := connect.NewError(connect.CodeUnauthenticated, "token expired")
		wrapped := fmt.Errorf("locate tab t1: %w", stubHintProvider{err: rpcErr, hints: []string{"re-authenticate"}})
		got := FromError(wrapped)
		assert.Equal(t, CodeNotLoggedIn, got.Code)
		assert.Equal(t, "locate tab t1: token expired", got.Message)
		assert.Equal(t, []string{"re-authenticate"}, got.Hints)
		assert.ErrorIs(t, got, rpcErr)
	})
	t.Run("HintProvider collects multiple hints across nested and joined errors", func(t *testing.T) {
		inner := stubHintProvider{err: errors.New("boom"), hints: []string{"inner hint", "shared hint"}}
		outer := stubHintProvider{err: inner, hints: []string{"outer hint", "shared hint"}}
		joined := errors.Join(outer, stubHintProvider{err: errors.New("other"), hints: []string{"joined hint"}})
		got := FromError(joined)
		assert.Equal(t, CodeInternal, got.Code)
		assert.Equal(t, []string{"outer hint", "shared hint", "inner hint", "shared hint", "joined hint"}, got.Hints)
	})
	t.Run("HintProvider clones shared Error without mutating original", func(t *testing.T) {
		shared := Newf(CodeConflict, "stale state")
		shared.Hints = make([]string, 1, 4)
		shared.Hints[0] = "original hint"

		wrapped := stubHintProvider{err: shared, hints: []string{"extra hint"}}
		got1 := FromError(wrapped)
		got2 := FromError(wrapped)

		assert.NotSame(t, shared, got1)
		assert.Equal(t, []string{"original hint"}, shared.Hints, "shared Error must not be mutated")
		assert.Equal(t, []string{"original hint", "extra hint"}, got1.Hints)
		assert.Equal(t, []string{"original hint", "extra hint"}, got2.Hints)

		emptyProvider := stubHintProvider{err: shared, hints: nil}
		assert.Same(t, shared, FromError(emptyProvider), "empty HintProvider returns the original Error")
	})
	t.Run("extractEnvelope handles errors.Join and custom As methods", func(t *testing.T) {
		joinedErr := errors.Join(errors.New("plain"), Newf(CodeNotFound, "missing in second branch"))
		gotJoined := FromError(joinedErr)
		assert.Equal(t, CodeNotFound, gotJoined.Code)
		assert.Equal(t, "missing in second branch", gotJoined.Message)

		asErr := customAsError{targetErr: Newf(CodeConflict, "from As Error")}
		assert.Equal(t, CodeConflict, FromError(asErr).Code)

		asClassifier := customAsError{targetClassifier: classified{}}
		assert.Equal(t, CodeWorkerOffline, FromError(asClassifier).Code)

		asNilClassifier := customAsError{targetClassifier: nilClassifier{}}
		assert.Equal(t, CodeInternal, FromError(asNilClassifier).Code)
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

type wrappingClassifier struct{ err error }

func (w wrappingClassifier) Error() string    { return "wrapping classifier: " + w.err.Error() }
func (w wrappingClassifier) Unwrap() error    { return w.err }
func (w wrappingClassifier) CLIError() *Error { return Newf(CodeWorkerOffline, "outer offline") }

type wrappingNilClassifier struct{ err error }

func (w wrappingNilClassifier) Error() string    { return "wrapping nil classifier: " + w.err.Error() }
func (w wrappingNilClassifier) Unwrap() error    { return w.err }
func (w wrappingNilClassifier) CLIError() *Error { return nil }

type stubHintProvider struct {
	err   error
	hints []string
}

func (s stubHintProvider) Error() string      { return s.err.Error() }
func (s stubHintProvider) Unwrap() error      { return s.err }
func (s stubHintProvider) CLIHints() []string { return s.hints }

type customAsError struct {
	targetErr        *Error
	targetClassifier Classifier
}

func (c customAsError) Error() string { return "custom as error" }

func (c customAsError) As(target any) bool {
	if c.targetErr != nil {
		if dst, ok := target.(**Error); ok {
			*dst = c.targetErr
			return true
		}
	}
	if c.targetClassifier != nil {
		if dst, ok := target.(*Classifier); ok {
			*dst = c.targetClassifier
			return true
		}
	}
	return false
}

func TestError_StringAndWithReceipt(t *testing.T) {
	receipt := &RetryReceipt{
		RequestID:    "req-1",
		Disposition:  "uncertain",
		QueryCommand: "leapmux tab list",
		RetryCommand: "leapmux tab open --request-id req-1",
	}
	e := Newf(CodeTimeout, "wait timed out").WithReceipt(receipt)
	assert.Equal(t, "timeout: wait timed out", e.Error())
	assert.Same(t, receipt, e.Receipt)
}

func TestError_WithCausePreservesMessageAndMatchesTarget(t *testing.T) {
	cause := &url.Error{Op: "Get", URL: "https://hub.example", Err: errors.New("connection refused")}
	e := Newf(CodeUnavailable, "the Hub rejected the connection").WithCause(cause)
	assert.Equal(t, "the Hub rejected the connection", e.Message)
	assert.Equal(t, "unavailable: the Hub rejected the connection", e.Error())
	assert.ErrorIs(t, e, cause)
	var target *url.Error
	require.ErrorAs(t, e, &target)
	assert.Same(t, cause, target)
}

func TestWrapKeepsTheCauseAndPrefixesTheMessage(t *testing.T) {
	cause := errors.New("boom")
	got := Wrap(CodeConflict, cause, "submit batch")
	assert.Equal(t, "submit batch: boom", got.Message)
	assert.ErrorIs(t, got, cause)
	assert.Equal(t, "boom", Wrap(CodeConflict, cause, "").Message)

	nilWrapped := Wrap(CodeConflict, nil, "no cause")
	assert.Equal(t, CodeConflict, nilWrapped.Code)
	assert.Equal(t, "no cause", nilWrapped.Message)
	assert.Nil(t, nilWrapped.Unwrap())
}

// timeoutError is a net.Error that reports a timeout.
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }
