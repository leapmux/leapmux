package service

import (
	"errors"
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

func TestTurnRetryErrorPreservesTheFailureClass(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		err  error
		code codes.Code
		text string
	}{
		{"pending input", agent.ErrAgentBusy, codes.FailedPrecondition, "pending input delivery"},
		{"offer withdrawn", agent.ErrTurnRetryUnavailable, codes.FailedPrecondition, "no longer offers this retry"},
		{"missing", agent.ErrAgentNotFound, codes.NotFound, "not found"},
		{"unexpected failure", errors.New("the provider refused the retry"), codes.Internal, "provider refused the retry"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			writer := newTestWriter()
			sendTurnRetryError(writer, fmt.Errorf("turn retry: %w", test.err))
			failures := writer.rejections()
			require.Len(t, failures, 1)
			assert.Equal(t, int32(test.code), failures[0].code)
			assert.Contains(t, failures[0].message, test.text)
		})
	}
}

func TestTurnRetryErrorIgnoresAnAbsentError(t *testing.T) {
	t.Parallel()
	writer := newTestWriter()
	sendTurnRetryError(writer, nil)
	assert.Empty(t, writer.rejections())
}
