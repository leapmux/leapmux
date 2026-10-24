package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

func TestInterruptErrorPreservesTheFailureClass(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
		code codes.Code
		text string
	}{
		{"missing", agent.ErrAgentNotFound, codes.NotFound, "agent not found"},
		{"unsupported child", agent.ErrChildOperationUnsupported, codes.FailedPrecondition, "this subagent cannot be interrupted"},
		{"unready child", agent.ErrChildRouteNotReady, codes.Unavailable, "subagent route is not ready"},
		{"native restriction", &agent.InterruptUnavailableError{Message: "the native operation cannot be interrupted"}, codes.FailedPrecondition, "the native operation cannot be interrupted"},
		{"empty restriction", &agent.InterruptUnavailableError{}, codes.FailedPrecondition, "the current operation cannot be interrupted"},
		{"cancellation", context.Canceled, codes.Canceled, "context canceled"},
		{"deadline", context.DeadlineExceeded, codes.DeadlineExceeded, "deadline exceeded"},
		{"transport failure", errors.New("native stop transport failed"), codes.Internal, "native stop transport failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			writer := newTestWriter()
			sendInterruptError(writer, fmt.Errorf("provider stop: %w", tc.err))
			failures := writer.rejections()
			require.Len(t, failures, 1)
			assert.Equal(t, int32(tc.code), failures[0].code)
			assert.Contains(t, failures[0].message, tc.text)
		})
	}
}

func TestInterruptErrorIgnoresAnAbsentError(t *testing.T) {
	t.Parallel()
	writer := newTestWriter()
	sendInterruptError(writer, nil)
	assert.Empty(t, writer.rejections())
}
