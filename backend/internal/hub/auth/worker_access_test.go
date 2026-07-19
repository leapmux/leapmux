package auth_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// WorkerUsableNow is the one predicate both the channel-service entrypoints and
// the CRDT auth checker apply after WorkerCanUse, so the ACTIVE bar a
// deregistering worker fails cannot drift between them. Pin it directly: an
// ACTIVE worker is usable, a deregistering one is not, a nil record (the shape
// WorkerCanUse returns for a missing worker) is not, and the soft-deleted state
// never reaches here because GetByID filters it.
func TestWorkerUsableNow(t *testing.T) {
	assert.True(t, auth.WorkerUsableNow(&store.Worker{
		Status: leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE,
	}), "an ACTIVE worker is usable now")

	for _, status := range []leapmuxv1.WorkerStatus{
		leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING,
		leapmuxv1.WorkerStatus_WORKER_STATUS_UNSPECIFIED,
	} {
		assert.False(t, auth.WorkerUsableNow(&store.Worker{Status: status}),
			"a non-ACTIVE worker (status=%v) must not be usable", status)
	}

	assert.False(t, auth.WorkerUsableNow(nil),
		"a nil record (WorkerCanUse's missing-worker shape) must not be usable")
}
