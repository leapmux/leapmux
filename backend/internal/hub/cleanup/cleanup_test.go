package cleanup

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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

	// Create a user.
	userID := id.Generate()
	hash, err := password.Hash("TestPassword1!")
	require.NoError(t, err)
	err = st.Users().Create(ctx, store.CreateUserParams{
		ID: userID, Username: "testuser",
		PasswordHash: hash, DisplayName: "Test", PasswordSet: true,
	})
	require.NoError(t, err)

	// Soft-delete the user and backdate to 8 days ago.
	err = st.Users().Delete(ctx, userID)
	require.NoError(t, err)
	past := time.Now().UTC().Add(-8 * 24 * time.Hour)
	err = st.TestHelper().SetDeletedAt(ctx, store.EntityUsers, userID, past)
	require.NoError(t, err)

	// Run cleanup.
	run(ctx, st)

	// Verify hard-deleted.
	_, err = st.Users().GetByIDIncludeDeleted(ctx, userID)
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestRun_RetainsRecentlyDeleted(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	// Create and soft-delete a user (recent, within retention).
	userID := id.Generate()
	hash, err := password.Hash("TestPassword1!")
	require.NoError(t, err)
	err = st.Users().Create(ctx, store.CreateUserParams{
		ID: userID, Username: "testuser",
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

// DrainUntilEmpty is the shared loop behind both paged sweeps, so its contract
// is pinned directly rather than only through the two callers: terminate on a
// pass that deletes nothing, stop on the first error, respect the runaway bound,
// and report a cancelled context as a PAUSE (total, no error) rather than a
// failure -- the rows already deleted are committed and the next scheduled
// sweep picks up the rest.
func TestDrainUntilEmpty(t *testing.T) {
	t.Run("drains until a pass deletes nothing", func(t *testing.T) {
		pages := []int64{1000, 1000, 250, 0}
		var calls int
		total, err := DrainUntilEmpty(context.Background(), 10, func() (int64, error) {
			n := pages[calls]
			calls++
			return n, nil
		})
		require.NoError(t, err)
		assert.Equal(t, int64(2250), total)
		assert.Equal(t, 4, calls, "must stop on the empty pass, not keep going to maxPasses")
	})

	t.Run("stops on the first error and keeps what was already deleted", func(t *testing.T) {
		var calls int
		total, err := DrainUntilEmpty(context.Background(), 10, func() (int64, error) {
			calls++
			if calls == 2 {
				return 7, assert.AnError
			}
			return 100, nil
		})
		require.ErrorIs(t, err, assert.AnError)
		assert.Equal(t, int64(107), total, "the failing pass's own deletions still count")
		assert.Equal(t, 2, calls)
	})

	t.Run("honours the runaway bound", func(t *testing.T) {
		var calls int
		total, err := DrainUntilEmpty(context.Background(), 3, func() (int64, error) {
			calls++
			return 1000, nil
		})
		require.NoError(t, err)
		assert.Equal(t, 3, calls)
		assert.Equal(t, int64(3000), total)
	})

	t.Run("treats a cancelled context as a pause, not a failure", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		var calls int
		total, err := DrainUntilEmpty(ctx, 10, func() (int64, error) {
			calls++
			cancel()
			return 100, nil
		})
		require.NoError(t, err, "shutdown mid-drain must not log as a cleanup failure")
		assert.Equal(t, int64(100), total)
		assert.Equal(t, 1, calls, "the cancellation is observed before the next pass")
	})

	t.Run("never calls the pass when already cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var calls int
		total, err := DrainUntilEmpty(ctx, 10, func() (int64, error) {
			calls++
			return 100, nil
		})
		require.NoError(t, err)
		assert.Zero(t, total)
		assert.Zero(t, calls)
	})
}

func TestRun_DeletesExpiredWebAuthnSessions(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	userID := id.Generate()
	hash, err := password.Hash("TestPassword1!")
	require.NoError(t, err)
	require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
		ID: userID, Username: "wauser",
		PasswordHash: hash, DisplayName: "WA", PasswordSet: true,
	}))

	expiredID := id.Generate()
	liveID := id.Generate()
	now := time.Now().UTC()
	require.NoError(t, st.WebAuthnSessions().Create(ctx, store.CreateWebAuthnSessionParams{
		ID: expiredID, Kind: "login", UserID: userID, PayloadJSON: "{}",
		SessionData: []byte("x"), ExpiresAt: now.Add(-time.Hour), CreatedAt: now.Add(-2 * time.Hour),
	}))
	require.NoError(t, st.WebAuthnSessions().Create(ctx, store.CreateWebAuthnSessionParams{
		ID: liveID, Kind: "login", UserID: userID, PayloadJSON: "{}",
		SessionData: []byte("y"), ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}))

	run(ctx, st)

	_, err = st.WebAuthnSessions().Get(ctx, expiredID)
	require.ErrorIs(t, err, store.ErrNotFound)
	_, err = st.WebAuthnSessions().Get(ctx, liveID)
	require.NoError(t, err)
}
