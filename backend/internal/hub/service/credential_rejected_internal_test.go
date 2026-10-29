package service

import (
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// connect.NewError takes a string and serializes only that. The helper's whole
// job is to send exactly what v1 sent while keeping the cause attached, so
// errors.Is and errors.As keep answering the way v1's
// `connect.NewError(code, err)` did.
func TestWrappedConnectError_SendsTheCauseTextAndKeepsTheCause(t *testing.T) {
	sentinel := errors.New("boom")
	cause := fmt.Errorf("count workers: %w", sentinel)

	cerr := wrappedConnectError(connect.CodeInternal, cause)

	assert.Equal(t, connect.CodeInternal, cerr.Code())
	assert.Equal(t, "count workers: boom", cerr.Message(),
		"the caller must read the same bytes v1 sent")
	require.ErrorIs(t, cerr, sentinel,
		"the wrapped cause must stay reachable, or every errors.Is consumer breaks")
	require.ErrorIs(t, cerr, cause)
}

// An error that wraps nothing is the common case, and it must survive the
// same way.
func TestWrappedConnectError_KeepsAnUnwrappedCauseReachable(t *testing.T) {
	cause := errors.New("boom")

	cerr := wrappedConnectError(connect.CodeUnauthenticated, cause)

	assert.Equal(t, "boom", cerr.Message())
	require.ErrorIs(t, cerr, cause)
}

// A caller switches on a TYPED cause. Losing it turns a specific refusal into
// a generic one with nothing to tell the two apart, which is how a counted
// admission refusal goes uncounted.
type testLimitError struct{ limit int64 }

func (e *testLimitError) Error() string { return fmt.Sprintf("worker cap %d", e.limit) }

func TestWrappedConnectError_KeepsATypedCauseReachable(t *testing.T) {
	cause := &testLimitError{limit: 3}

	cerr := wrappedConnectError(connect.CodeResourceExhausted, cause)

	assert.Equal(t, "worker cap 3", cerr.Message())
	var target *testLimitError
	require.ErrorAs(t, cerr, &target, "errors.As must reach the typed cause")
	assert.Equal(t, int64(3), target.limit)
}
