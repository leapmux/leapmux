package cmd

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCodedRPCError_PreservesCodeAndCause covers the basic struct
// contract: the Code travels with the error so aggregating callers
// (workspace delete, agent open rollback) can branch on it.
func TestCodedRPCError_PreservesCodeAndCause(t *testing.T) {
	cause := errors.New("network unreachable")
	e := &codedRPCError{Code: "channel_open_failed", Cause: cause}

	assert.Equal(t, cause.Error(), e.Error())
	assert.Equal(t, "channel_open_failed", e.Code)
	assert.Same(t, cause, e.Unwrap())
}

// TestCodedRPCError_ErrorsAsUnwrapsThroughChain pins errors.As
// behaviour so callers can fish a `*codedRPCError` out of a wrapped
// chain. fmt.Errorf("…: %w", err) is the typical wrap site.
func TestCodedRPCError_ErrorsAsUnwrapsThroughChain(t *testing.T) {
	inner := &codedRPCError{Code: "rpc_failed", Cause: errors.New("boom")}
	wrapped := fmt.Errorf("context: %w", inner)

	var found *codedRPCError
	require.True(t, errors.As(wrapped, &found))
	assert.Equal(t, "rpc_failed", found.Code)
}

// TestCodedRPCError_ErrorsIsThroughChain pins errors.Is for the same
// wrapped-error case but matched against the raw cause.
func TestCodedRPCError_ErrorsIsThroughChain(t *testing.T) {
	cause := errors.New("not found")
	e := &codedRPCError{Code: "not_found", Cause: cause}

	assert.True(t, errors.Is(e, cause), "errors.Is should descend into Cause")
}

// TestCodedRPCError_NilCausePanicsOnError documents what happens
// with a nil Cause: Error() panics. Treated as a programmer error —
// every caller already has a non-nil error to wrap.
func TestCodedRPCError_NilCausePanicsOnError(t *testing.T) {
	e := &codedRPCError{Code: "some_code", Cause: nil}
	assert.Panics(t, func() { _ = e.Error() })
}

// TestRpcDeadline_HasFiniteDeadline pins the bounded-context shape
// of rpcDeadline. Without a deadline a hub that never responds would
// hang the CLI indefinitely.
func TestRpcDeadline_HasFiniteDeadline(t *testing.T) {
	ctx, cancel := rpcDeadline(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	require.True(t, ok, "rpcDeadline must attach a deadline")
	assert.WithinDuration(t, time.Now().Add(30*time.Second), deadline, 5*time.Second)
}

// TestRpcDeadline_HonoursParentCancellation guards the conventional
// child-context inheritance: a Ctrl-C between the dispatcher and the
// RPC must propagate through.
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
