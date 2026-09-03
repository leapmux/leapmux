package storetest_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

func newDoubleRunStore(t *testing.T) store.Store {
	t.Helper()
	st, err := sqlite.OpenTestable(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return storetest.NewDoubleRunStore(st)
}

// The decorator's whole value is that an ACCUMULATING callback becomes
// visible. This asserts the mechanism itself, not a caller that happens to be
// correct -- otherwise the decorator could be a no-op and every suite would
// still pass.
func TestDoubleRunStoreExposesAnAccumulatingCallback(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newDoubleRunStore(t)

	var appended []string
	require.NoError(t, st.RunInTransaction(ctx, func(store.Store) error {
		appended = append(appended, "effect")
		return nil
	}))
	assert.Len(t, appended, 2,
		"a callback that accumulates must double, which is what makes the contract testable")

	// The correct shape -- ASSIGN, never accumulate -- is unaffected, because
	// a re-run overwrites what the rehearsal wrote.
	var assigned string
	require.NoError(t, st.RunInTransaction(ctx, func(store.Store) error {
		assigned = "result"
		return nil
	}))
	assert.Equal(t, "result", assigned)
}

// The DATABASE sees one attempt. A rehearsal that left rows behind would make
// every store-backed test lie about what its callback wrote.
func TestDoubleRunStoreCommitsExactlyOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newDoubleRunStore(t)

	userID := id.Generate()
	require.NoError(t, st.RunInTransaction(ctx, func(tx store.Store) error {
		return tx.Users().Create(ctx, store.CreateUserParams{
			ID: userID, Username: "doubled", PasswordHash: "hash",
			DisplayName: "Doubled", FirstCredentialExempt: true,
		})
	}))

	page, err := st.Users().ListAll(ctx, store.ListAllUsersParams{PageParams: store.PageParams{Limit: 10}})
	require.NoError(t, err)
	assert.Len(t, page.Rows, 1, "the rehearsal must roll back, so the row lands once")
}

// The REAL attempt's error is what the caller gets. A rehearsal that failed
// for a reason the real attempt does not repeat must not become the answer.
func TestDoubleRunStoreReportsTheRealAttempt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newDoubleRunStore(t)

	attempts := 0
	err := st.RunInTransaction(ctx, func(store.Store) error {
		attempts++
		if attempts == 1 {
			return assert.AnError
		}
		return nil
	})
	require.NoError(t, err, "the rehearsal's failure is not the caller's answer")
	assert.Equal(t, 2, attempts)
}

// The user-auth variant takes the identical shape, and it is the one the
// step-up mutations use.
func TestDoubleRunStoreCoversTheUserAuthTransaction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newDoubleRunStore(t)

	userID := id.Generate()
	require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
		ID: userID, Username: "locked", PasswordHash: "hash",
		DisplayName: "Locked", FirstCredentialExempt: true,
	}))

	runs := 0
	require.NoError(t, st.RunInUserAuthTransaction(ctx, userid.MustNew(userID), func(store.Store) error {
		runs++
		return nil
	}))
	assert.Equal(t, 2, runs)
}
