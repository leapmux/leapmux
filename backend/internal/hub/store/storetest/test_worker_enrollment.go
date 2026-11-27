package storetest

import (
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

func (s *Suite) testWorkerEnrollmentRevision(t *testing.T) {
	ctx := t.Context()
	st := s.NewStore(t)
	owner := userid.MustNew(SeedUser(t, st, "worker-membership-owner").ID)
	other := userid.MustNew(SeedUser(t, st, "worker-membership-stranger").ID)
	snapshot := func(account userid.UserID) store.WorkerInventorySnapshot {
		snap, err := st.Workers().InventorySnapshot(ctx, account)
		require.NoError(t, err)
		return snap
	}

	_, err := st.Workers().InventorySnapshot(ctx, userid.UserID{})
	require.ErrorIs(t, err, store.ErrNotFound)

	initial := snapshot(owner)
	assert.Zero(t, initial.Revision)
	assert.Empty(t, initial.WorkerIDs)

	worker := SeedWorker(t, st, owner.String())
	enrolled := snapshot(owner)
	assert.Equal(t, int64(1), enrolled.Revision)
	assert.Equal(t, []string{worker.ID}, enrolled.WorkerIDs)
	otherSnap := snapshot(other)
	assert.Zero(t, otherSnap.Revision)
	assert.Empty(t, otherSnap.WorkerIDs)

	require.NoError(t, st.Workers().UpdateLastSeen(ctx, worker.ID))
	afterHeartbeat := snapshot(owner)
	assert.Equal(t, int64(1), afterHeartbeat.Revision)
	assert.Equal(t, []string{worker.ID}, afterHeartbeat.WorkerIDs)

	changed, err := st.Workers().Deregister(ctx, store.DeregisterWorkerParams{ID: worker.ID, RegisteredBy: owner})
	require.NoError(t, err)
	assert.Equal(t, int64(1), changed)
	absent, err := st.Workers().ListByUserID(ctx, store.ListWorkersByUserIDParams{
		RegisteredBy: owner, PageParams: store.PageParams{Limit: 10},
	})
	require.NoError(t, err)
	assert.Empty(t, absent.Rows)
	removed := snapshot(owner)
	assert.Empty(t, removed.WorkerIDs)
	assert.Equal(t, int64(1), removed.Revision, "an empty listing still proves the Worker came and went")

	require.NoError(t, st.Workers().MarkDeleted(ctx, worker.ID))
	deleted := snapshot(owner)
	assert.Equal(t, int64(1), deleted.Revision, "the acknowledgement does not change the active inventory")
	assert.Empty(t, deleted.WorkerIDs)

	var rolledBackID string
	require.ErrorIs(t, st.RunInTransaction(ctx, func(tx store.Store) error {
		rolledBackID = SeedWorker(t, tx, owner.String()).ID
		return assert.AnError
	}), assert.AnError)
	_, err = st.Workers().GetByID(ctx, rolledBackID)
	require.ErrorIs(t, err, store.ErrNotFound)
	afterRollback := snapshot(owner)
	assert.Equal(t, int64(1), afterRollback.Revision)
	assert.Empty(t, afterRollback.WorkerIDs)

	require.Error(t, st.Workers().Create(ctx, store.CreateWorkerParams{
		ID: worker.ID, RegisteredBy: owner, AuthTokenHash: FixtureSecretHash("duplicate"), PublicKeys: WorkerKeys(0x42),
	}))
	afterDup := snapshot(owner)
	assert.Equal(t, int64(1), afterDup.Revision)
	assert.Empty(t, afterDup.WorkerIDs)

	const writers = 4
	var group sync.WaitGroup
	results := make(chan error, writers)
	wantIDs := make([]string, writers)
	for i := range writers {
		workerID := id.Generate()
		wantIDs[i] = workerID
		params := store.CreateWorkerParams{
			ID: workerID, RegisteredBy: owner, AuthTokenHash: FixtureSecretHash(workerID), PublicKeys: WorkerKeys(0x42),
		}
		group.Go(func() { results <- st.Workers().Create(ctx, params) })
	}
	group.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	slices.Sort(wantIDs)
	afterConcurrent := snapshot(owner)
	assert.Equal(t, int64(1+writers), afterConcurrent.Revision)
	assert.Equal(t, wantIDs, afterConcurrent.WorkerIDs)
	otherFinal := snapshot(other)
	assert.Zero(t, otherFinal.Revision)
	assert.Empty(t, otherFinal.WorkerIDs)
}
