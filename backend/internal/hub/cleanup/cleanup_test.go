package cleanup

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/util/id"
)

func setupTestStore(t *testing.T) store.TestableStore {
	t.Helper()
	st, err := sqlite.OpenTestable(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

type cleanupSpyStore struct {
	store.Store
	cleanup *cleanupSpy
}

func (s cleanupSpyStore) Cleanup() store.CleanupStore {
	return s.cleanup
}

type cleanupSpy struct {
	store.CleanupStore
	called         bool
	compactionRuns int
	results        []int64
	afterRun       func()
}

func (s *cleanupSpy) CompactPublishedRevocationEvents(_ context.Context, _ store.CompactRevocationEventsParams) (int64, error) {
	s.called = true
	s.compactionRuns++
	var result int64
	if s.compactionRuns <= len(s.results) {
		result = s.results[s.compactionRuns-1]
	}
	if s.afterRun != nil {
		s.afterRun()
	}
	return result, nil
}

func TestRun_CleansUpOldRecords(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	// Create a user + org.
	orgID := id.Generate()
	err := st.Orgs().Create(ctx, store.CreateOrgParams{ID: orgID, Name: "testorg"})
	require.NoError(t, err)
	hash, err := password.Hash("TestPassword1!")
	require.NoError(t, err)
	userID := id.Generate()
	err = st.Users().Create(ctx, store.CreateUserParams{
		ID: userID, OrgID: orgID, Username: "testuser",
		PasswordHash: hash, DisplayName: "Test", PasswordSet: true,
	})
	require.NoError(t, err)

	// Soft-delete the user and org, backdate to 8 days ago.
	err = st.Users().Delete(ctx, userID)
	require.NoError(t, err)
	err = st.Orgs().SoftDelete(ctx, orgID)
	require.NoError(t, err)
	past := time.Now().UTC().Add(-8 * 24 * time.Hour)
	err = st.TestHelper().SetDeletedAt(ctx, store.EntityUsers, userID, past)
	require.NoError(t, err)
	err = st.TestHelper().SetDeletedAt(ctx, store.EntityOrgs, orgID, past)
	require.NoError(t, err)

	// Run cleanup.
	run(ctx, st)

	// Verify hard-deleted.
	_, err = st.Users().GetByIDIncludeDeleted(ctx, userID)
	require.ErrorIs(t, err, store.ErrNotFound)

	_, err = st.Orgs().GetByIDIncludeDeleted(ctx, orgID)
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestRun_RetainsRecentlyDeleted(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	// Create and soft-delete a user (recent, within retention).
	orgID := id.Generate()
	err := st.Orgs().Create(ctx, store.CreateOrgParams{ID: orgID, Name: "testorg"})
	require.NoError(t, err)
	hash, err := password.Hash("TestPassword1!")
	require.NoError(t, err)
	userID := id.Generate()
	err = st.Users().Create(ctx, store.CreateUserParams{
		ID: userID, OrgID: orgID, Username: "testuser",
		PasswordHash: hash, DisplayName: "Test", PasswordSet: true,
	})
	require.NoError(t, err)
	err = st.Users().Delete(ctx, userID)
	require.NoError(t, err)

	// Run cleanup.
	run(ctx, st)

	// User should still exist (recently deleted, within 7-day retention).
	user, err := st.Users().GetByIDIncludeDeleted(ctx, userID)
	require.NoError(t, err)
	require.NotNil(t, user.DeletedAt)
}

func TestRun_CompactsPublishedRevocationEvents(t *testing.T) {
	st := setupTestStore(t)
	spy := &cleanupSpy{CleanupStore: st.Cleanup()}

	run(context.Background(), cleanupSpyStore{Store: st, cleanup: spy})

	require.True(t, spy.called)
}

func TestRun_BoundsRevocationCompactionWorkPerPass(t *testing.T) {
	st := setupTestStore(t)
	results := make([]int64, maxRevocationCompactionBatches+1)
	for i := range results {
		results[i] = store.CleanupBatchLimit
	}
	spy := &cleanupSpy{
		CleanupStore: st.Cleanup(),
		results:      results,
	}

	run(context.Background(), cleanupSpyStore{Store: st, cleanup: spy})

	require.Equal(t, maxRevocationCompactionBatches, spy.compactionRuns)
}

func TestRun_DrainsRevocationCompactionUntilEmpty(t *testing.T) {
	// The loop must keep compacting until a batch deletes NOTHING, not stop on a
	// partial batch. This decouples termination from the delete query's internal
	// LIMIT (a separate constant that could drift from CleanupBatchLimit): a batch
	// that deletes fewer than a full page must still be followed by a drain check.
	st := setupTestStore(t)
	spy := &cleanupSpy{
		CleanupStore: st.Cleanup(),
		results:      []int64{store.CleanupBatchLimit, store.CleanupBatchLimit / 2, 0},
	}

	run(context.Background(), cleanupSpyStore{Store: st, cleanup: spy})

	// 1000 -> continue, 500 (partial) -> continue (must NOT stop here), 0 -> stop.
	require.Equal(t, 3, spy.compactionRuns)
}

func TestRun_StopsRevocationCompactionWhenContextIsCanceled(t *testing.T) {
	st := setupTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	spy := &cleanupSpy{
		CleanupStore: st.Cleanup(),
		results:      []int64{store.CleanupBatchLimit, store.CleanupBatchLimit},
		afterRun:     cancel,
	}

	run(ctx, cleanupSpyStore{Store: st, cleanup: spy})

	require.Equal(t, 1, spy.compactionRuns)
}
