package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// TestCodex_SendChildInputUnknownThreadReturnsRetryable verifies that when the
// owner process is running but its in-memory collab index does not know the
// thread (empty after a worker restart), SendChildInput wraps the failure in
// ErrChildNotSteerableYet. The service maps that to UNAVAILABLE so the client
// re-queues, instead of persisting a permanent delivery error.
func TestCodex_SendChildInputUnknownThreadReturnsRetryable(t *testing.T) {
	t.Parallel()
	// A fresh CodexAgent has an empty collabThreadSpans (the post-restart
	// state until the live spawn re-fires).
	a := &CodexAgent{}
	err := a.SendChildInput("unknown-thread", "hello", []*leapmuxv1.Attachment{})
	assert.ErrorIs(t, err, ErrChildNotSteerableYet,
		"an unknown thread on a running owner must be retryable, not a hard failure")
}

// TestCodex_InterruptChildUnknownThreadReturnsRetryable mirrors the send path
// for interrupt: a NotFound misclassification would make the frontend drop the
// child tab as unavailable even though the root process is running.
func TestCodex_InterruptChildUnknownThreadReturnsRetryable(t *testing.T) {
	t.Parallel()
	a := &CodexAgent{}
	err := a.InterruptChild("unknown-thread")
	assert.ErrorIs(t, err, ErrChildNotSteerableYet)
}
