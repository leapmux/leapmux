package auth_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
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

// TestWorkerCanUse_ZeroUserIDDenies pins the typed-identity fail-close: a zero
// UserID must never match a registrant, even when the worker row exists.
func TestWorkerCanUse_ZeroUserIDDenies(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	orgID := id.Generate()
	require.NoError(t, st.Orgs().Create(ctx, store.CreateOrgParams{ID: orgID, Name: "org"}))
	userID := id.Generate()
	require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
		ID: userID, OrgID: orgID, Username: "owner", PasswordHash: "h", DisplayName: "O", PasswordSet: true,
	}))
	worker := storetest.SeedWorker(t, st, userID)

	w, ok, err := auth.WorkerCanUse(ctx, st, worker.ID, userid.UserID{})
	require.NoError(t, err)
	assert.Nil(t, w)
	assert.False(t, ok, "zero UserID must fail closed without a store match")

	w, ok, err = auth.WorkerCanUse(ctx, st, worker.ID, userid.MustNew(userID))
	require.NoError(t, err)
	require.NotNil(t, w)
	assert.True(t, ok, "the registrant may use the worker")

	w, ok, err = auth.WorkerCanUse(ctx, st, worker.ID, userid.MustNew(id.Generate()))
	require.NoError(t, err)
	require.NotNil(t, w)
	assert.False(t, ok, "a non-registrant is denied")

	w, ok, err = auth.WorkerCanUse(ctx, st, "", userid.MustNew(userID))
	require.NoError(t, err)
	assert.Nil(t, w)
	assert.False(t, ok, "empty workerID fails closed")
}
