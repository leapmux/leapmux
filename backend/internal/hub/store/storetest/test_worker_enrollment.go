package storetest

import (
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
	revision := func(account userid.UserID) int64 {
		position, err := st.Workers().InventoryState(ctx, account)
		require.NoError(t, err)
		return position.Revision
	}
	assert.Zero(t, revision(owner))
	worker := SeedWorker(t, st, owner.String())
	assert.Equal(t, int64(1), revision(owner))
	assert.Zero(t, revision(other))
	require.NoError(t, st.Workers().UpdateLastSeen(ctx, worker.ID))
	assert.Equal(t, int64(1), revision(owner))
	changed, err := st.Workers().Deregister(ctx, store.DeregisterWorkerParams{ID: worker.ID, RegisteredBy: owner})
	require.NoError(t, err)
	assert.Equal(t, int64(1), changed)
	absent, err := st.Workers().ListByUserID(ctx, store.ListWorkersByUserIDParams{
		RegisteredBy: owner, PageParams: store.PageParams{Limit: 10},
	})
	require.NoError(t, err)
	assert.Empty(t, absent.Rows)
	removed, err := st.Workers().InventoryState(ctx, owner)
	require.NoError(t, err)
	assert.Zero(t, removed.ActiveWorkers)
	assert.Equal(t, int64(1), revision(owner), "an empty listing still proves the Worker came and went")
	require.NoError(t, st.Workers().MarkDeleted(ctx, worker.ID))
	assert.Equal(t, int64(1), revision(owner), "the acknowledgement does not change the active inventory")

	var rolledBackID string
	require.ErrorIs(t, st.RunInTransaction(ctx, func(tx store.Store) error {
		rolledBackID = SeedWorker(t, tx, owner.String()).ID
		return assert.AnError
	}), assert.AnError)
	_, err = st.Workers().GetByID(ctx, rolledBackID)
	require.ErrorIs(t, err, store.ErrNotFound)
	assert.Equal(t, int64(1), revision(owner))
	require.Error(t, st.Workers().Create(ctx, store.CreateWorkerParams{
		ID: worker.ID, RegisteredBy: owner, AuthTokenHash: FixtureSecretHash("duplicate"), PublicKeys: WorkerKeys(0x42),
	}))
	assert.Equal(t, int64(1), revision(owner))

	const writers = 4
	var group sync.WaitGroup
	results := make(chan error, writers)
	for range writers {
		workerID := id.Generate()
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
	assert.Equal(t, int64(1+writers), revision(owner))
	assert.Zero(t, revision(other))
}
