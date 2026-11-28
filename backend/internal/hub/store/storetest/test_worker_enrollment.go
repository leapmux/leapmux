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

	unknown := snapshot(userid.MustNew(id.Generate()))
	assert.Zero(t, unknown.Revision)
	assert.Empty(t, unknown.WorkerIDs)

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
	var inRollbackTx store.WorkerInventorySnapshot
	require.ErrorIs(t, st.RunInTransaction(ctx, func(tx store.Store) error {
		beforeWrite, txErr := tx.Workers().InventorySnapshot(ctx, owner)
		if txErr != nil {
			return txErr
		}
		assert.Equal(t, int64(1), beforeWrite.Revision)
		assert.Empty(t, beforeWrite.WorkerIDs)

		rolledBackID = SeedWorker(t, tx, owner.String()).ID
		inRollbackTx, txErr = tx.Workers().InventorySnapshot(ctx, owner)
		if txErr != nil {
			return txErr
		}
		return assert.AnError
	}), assert.AnError)
	assert.Equal(t, int64(2), inRollbackTx.Revision)
	assert.Equal(t, []string{rolledBackID}, inRollbackTx.WorkerIDs)
	_, err = st.Workers().GetByID(ctx, rolledBackID)
	require.ErrorIs(t, err, store.ErrNotFound)
	afterRollback := snapshot(owner)
	assert.Equal(t, int64(1), afterRollback.Revision)
	assert.Empty(t, afterRollback.WorkerIDs)

	txOwner := userid.MustNew(SeedUser(t, st, "worker-membership-tx").ID)
	suffix := id.Generate()
	workerZ := "worker-sort-z-" + suffix
	workerA := "worker-sort-a-" + suffix
	workerM := "worker-sort-m-" + suffix
	var inCommitTx store.WorkerInventorySnapshot
	require.NoError(t, st.RunInTransaction(ctx, func(tx store.Store) error {
		emptyTxSnap, txErr := tx.Workers().InventorySnapshot(ctx, txOwner)
		if txErr != nil {
			return txErr
		}
		assert.Zero(t, emptyTxSnap.Revision)
		assert.Empty(t, emptyTxSnap.WorkerIDs)

		for _, workerID := range []string{workerZ, workerA, workerM} {
			if err := tx.Workers().Create(ctx, store.CreateWorkerParams{
				ID:            workerID,
				RegisteredBy:  txOwner,
				AuthTokenHash: FixtureSecretHash(workerID),
				PublicKeys:    WorkerKeys(0x42),
			}); err != nil {
				return err
			}
		}
		n, deregErr := tx.Workers().Deregister(ctx, store.DeregisterWorkerParams{
			ID:           workerM,
			RegisteredBy: txOwner,
		})
		if deregErr != nil {
			return deregErr
		}
		assert.Equal(t, int64(1), n)

		inCommitTx, txErr = tx.Workers().InventorySnapshot(ctx, txOwner)
		return txErr
	}))
	wantSorted := []string{workerA, workerZ}
	assert.Equal(t, int64(3), inCommitTx.Revision)
	assert.Equal(t, wantSorted, inCommitTx.WorkerIDs)
	afterCommit := snapshot(txOwner)
	assert.Equal(t, int64(3), afterCommit.Revision)
	assert.Equal(t, wantSorted, afterCommit.WorkerIDs)

	for _, workerID := range wantSorted {
		n, deregErr := st.Workers().Deregister(ctx, store.DeregisterWorkerParams{ID: workerID, RegisteredBy: txOwner})
		require.NoError(t, deregErr)
		assert.Equal(t, int64(1), n)
	}
	afterAllDereg := snapshot(txOwner)
	assert.Equal(t, int64(3), afterAllDereg.Revision)
	assert.Empty(t, afterAllDereg.WorkerIDs)

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

	t.Run("direct_mark_deleted_omits_worker_and_keeps_revision", func(t *testing.T) {
		directOwner := userid.MustNew(SeedUser(t, st, "worker-membership-direct-delete").ID)
		liveWorker := SeedWorker(t, st, directOwner.String())
		keepWorker := SeedWorker(t, st, directOwner.String())
		before := snapshot(directOwner)
		assert.Equal(t, int64(2), before.Revision)
		wantBefore := []string{liveWorker.ID, keepWorker.ID}
		slices.Sort(wantBefore)
		assert.Equal(t, wantBefore, before.WorkerIDs)

		// MarkDeleted sets deleted_at directly; InventorySnapshot must still
		// omit the worker and keep Revision unchanged.
		require.NoError(t, st.Workers().MarkDeleted(ctx, liveWorker.ID))
		afterOne := snapshot(directOwner)
		assert.Equal(t, int64(2), afterOne.Revision)
		assert.Equal(t, []string{keepWorker.ID}, afterOne.WorkerIDs)

		require.NoError(t, st.Workers().MarkDeleted(ctx, keepWorker.ID))
		afterAll := snapshot(directOwner)
		assert.Equal(t, int64(2), afterAll.Revision)
		assert.Empty(t, afterAll.WorkerIDs)
	})
}
