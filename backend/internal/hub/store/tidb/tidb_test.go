//go:build integration

package tidb_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/store"
	mysqlstore "github.com/leapmux/leapmux/internal/hub/store/mysql"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

func TestTiDBStore(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	testutil.ConfigureDockerHost(t)

	ctx := context.Background()

	// Start a TiDB container.
	req := testcontainers.ContainerRequest{
		Image:        "pingcap/tidb:v8.1.0",
		ExposedPorts: []string{"4000/tcp"},
		WaitingFor: wait.ForSQL("4000/tcp", "mysql", func(host string, port string) string {
			return fmt.Sprintf("root@tcp(%s:%s)/?parseTime=true", host, testutil.PortNumber(port))
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
	port, err := container.MappedPort(ctx, "4000")
	require.NoError(t, err)

	// TiDB's default root user has no password. Create test database and enable FKs.
	rootDSN := fmt.Sprintf("root@tcp(%s:%s)/?parseTime=true", host, port.Port())
	rootDB, err := sql.Open("mysql", rootDSN)
	require.NoError(t, err)
	defer func() { _ = rootDB.Close() }()

	_, err = rootDB.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS leapmux_test")
	require.NoError(t, err)
	_, err = rootDB.ExecContext(ctx, "SET GLOBAL tidb_enable_foreign_key = ON")
	require.NoError(t, err)
	// TiDB defaults this OFF, which makes every CHECK in the shared mysql
	// schema a no-op. Set it before migrating so the suite exercises the same
	// constraints the other five backends enforce.
	_, err = rootDB.ExecContext(ctx, "SET GLOBAL tidb_enable_check_constraint = ON")
	require.NoError(t, err)

	// Assert it actually took. The variable is silently accepted on versions
	// that do not support it, so without this the storetest suite would run
	// green against a backend enforcing none of the schema's CHECK constraints
	// -- which is exactly the state this whole block exists to leave behind.
	// A blank users.id, a non-positive op_count, and the singleton-row guards
	// all rest on it.
	var checkEnabled string
	require.NoError(t, rootDB.QueryRowContext(ctx, "SELECT @@GLOBAL.tidb_enable_check_constraint").Scan(&checkEnabled))
	require.Contains(t, []string{"ON", "1"}, checkEnabled,
		"CHECK constraints must be enforced on TiDB, or the shared mysql schema's invariants hold only by application discipline here")

	dsn := fmt.Sprintf("root@tcp(%s:%s)/leapmux_test?parseTime=true", host, port.Port())

	// Create ONE store and run migrations once.
	st, err := mysqlstore.OpenTestable(config.MySQLConfig{DSN: dsn})
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
