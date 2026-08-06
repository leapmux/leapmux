//go:build integration

package cockroachdb_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/postgres"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
)

func TestCockroachDBStore(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()

	// Start a CockroachDB single-node container. The readiness probe dials
	// defaultdb; leapmux_test does not exist until the block below creates it.
	rootDSN := func(host, port, database string) string {
		return fmt.Sprintf("postgresql://root@%s:%s/%s?sslmode=disable", host, port, database)
	}
	host, port := storetest.SQLContainer{
		Image:    "cockroachdb/cockroach:latest-v24.3",
		Port:     26257,
		Driver:   "pgx",
		Cmd:      []string{"start-single-node", "--insecure"},
		ReadyDSN: func(host, port string) string { return rootDSN(host, port, "defaultdb") },
	}.Start(t)

	// Create the test database.
	setupPool, err := pgxpool.New(ctx, rootDSN(host, port, "defaultdb"))
	require.NoError(t, err)
	_, err = setupPool.Exec(ctx, "CREATE DATABASE IF NOT EXISTS leapmux_test")
	require.NoError(t, err)
	setupPool.Close()

	connStr := rootDSN(host, port, "leapmux_test")

	// Create ONE pool and store, run migrations once.
	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	sqlDB := stdlib.OpenDBFromPool(pool)
	st, err := postgres.NewTestableFromPool(pool, sqlDB)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	err = st.Migrator().Migrate(ctx)
	require.NoError(t, err)

	suite := &storetest.Suite{
		NewStore: func(t *testing.T) store.TestableStore {
			t.Helper()
			// Re-migrate first in case a migrator test rolled back the schema.
			err := st.Migrator().Migrate(context.Background())
			require.NoError(t, err)
			err = st.TestHelper().TruncateAll(context.Background())
			require.NoError(t, err)
			return st
		},
	}
	suite.Run(t)
}
