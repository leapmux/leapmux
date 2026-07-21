package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
)

func TestSQLiteStore(t *testing.T) {
	st, err := sqlite.OpenTestable(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	suite := &storetest.Suite{
		NewStore: func(t *testing.T) store.TestableStore {
			t.Helper()
			// Re-migrate first in case a migrator test rolled back the schema.
			err := st.Migrator().Migrate(context.Background())
			require.NoError(t, err)
			err = st.TestHelper().TruncateAll(context.Background())
			require.NoError(t, err)
			// After the subtest's writes (cleanup runs before the next
			// NewStore truncates), walk every DATETIME column of every table
			// and assert the canonical on-disk layout -- so ANY store write
			// path the suite exercises that forgets its strftime wrap fails
			// here, not as a silent row drop in production.
			t.Cleanup(func() {
				require.NoError(t, sqlite.CheckCanonicalTimestamps(context.Background(), st))
			})
			return st
		},
	}
	suite.Run(t)
}
