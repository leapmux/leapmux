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

func TestGoalUpdateErrorPreservesTheFailureClass(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		err  error
		code codes.Code
		text string
	}{
		{"busy", agent.ErrAgentBusy, codes.FailedPrecondition, "already running a turn"},
		{"uncertain delivery", agent.ErrDeliveryUncertain, codes.FailedPrecondition, "uncertain"},
		{"unsupported", agent.ErrGoalControlUnsupported, codes.FailedPrecondition, "cannot perform"},
		{"command argument", agent.ErrGoalObjectiveIsCommand, codes.InvalidArgument, "command argument"},
		{"missing", agent.ErrAgentNotFound, codes.NotFound, "not found"},
		{"cancelled", context.Canceled, codes.Canceled, "canceled"},
		{"session changed", agent.ErrContextClearCancelled, codes.Canceled, "cancelled"},
		{"deadline", context.DeadlineExceeded, codes.DeadlineExceeded, "deadline"},
		{"provider failure", errors.New("the provider refused the goal"), codes.Internal, "provider refused the goal"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			writer := newTestWriter()
			sendGoalUpdateError(writer, fmt.Errorf("goal update: %w", test.err))
			failures := writer.rejections()
			require.Len(t, failures, 1)
			assert.Equal(t, int32(test.code), failures[0].code)
			assert.Contains(t, failures[0].message, test.text)
		})
	}
}

func TestGoalUpdateErrorIgnoresAnAbsentError(t *testing.T) {
	t.Parallel()
	writer := newTestWriter()
	sendGoalUpdateError(writer, nil)
	assert.Empty(t, writer.rejections())
}
