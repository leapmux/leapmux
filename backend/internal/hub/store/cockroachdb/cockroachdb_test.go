//go:build integration

package cockroachdb_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/docker/go-connections/nat"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/postgres"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

func TestCockroachDBStore(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	testutil.ConfigureDockerHost(t)

	ctx := context.Background()

	// Start a CockroachDB single-node container.
	req := testcontainers.ContainerRequest{
		Image:        "cockroachdb/cockroach:latest-v24.3",
		ExposedPorts: []string{"26257/tcp"},
		Cmd:          []string{"start-single-node", "--insecure"},
		WaitingFor: wait.ForSQL("26257/tcp", "pgx", func(host string, port nat.Port) string {
			return fmt.Sprintf("postgresql://root@%s:%s/defaultdb?sslmode=disable", host, port.Port())
		}),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "26257")
	require.NoError(t, err)

	connStr := fmt.Sprintf("postgresql://root@%s:%s/defaultdb?sslmode=disable", host, port.Port())

	// Create the test database.
	setupPool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	_, err = setupPool.Exec(ctx, "CREATE DATABASE IF NOT EXISTS leapmux_test")
	require.NoError(t, err)
	setupPool.Close()

	connStr = fmt.Sprintf("postgresql://root@%s:%s/leapmux_test?sslmode=disable", host, port.Port())

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
