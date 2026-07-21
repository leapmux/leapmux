//go:build integration

package mysql_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/store"
	mysqlstore "github.com/leapmux/leapmux/internal/hub/store/mysql"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

// startMySQLContainer boots a disposable MySQL 8 container and returns its
// DSN. Shared by the store conformance suite and the live schema assertions.
func startMySQLContainer(t *testing.T) string {
	t.Helper()
	testutil.ConfigureDockerHost(t)

	ctx := context.Background()
	req := testcontainers.ContainerRequest{
		Image:        "mysql:8",
		ExposedPorts: []string{"3306/tcp"},
		Env: map[string]string{
			"MYSQL_ROOT_PASSWORD": "test",
			"MYSQL_DATABASE":      "leapmux_test",
			"MYSQL_USER":          "test",
			"MYSQL_PASSWORD":      "test",
		},
		WaitingFor: wait.ForSQL("3306/tcp", "mysql", func(host string, port string) string {
			return fmt.Sprintf("test:test@tcp(%s:%s)/leapmux_test?parseTime=true", host, testutil.PortNumber(port))
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
	port, err := container.MappedPort(ctx, "3306")
	require.NoError(t, err)

	return fmt.Sprintf("test:test@tcp(%s:%s)/leapmux_test?parseTime=true", host, port.Port())
}

func TestMySQLStore(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	dsn := startMySQLContainer(t)

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

// TestMySQLBinaryCollationLive pins the binary-collation invariant against the
// ACTUAL migrated schema: every application table must carry the utf8mb4_bin
// collation so id/FK columns collate byte-wise (case-sensitive) and the
// composite cursor's `id < ?` tiebreak stays deterministic for mixed-case ids.
// This is the live twin of the static source scan in schema_internal_test.go
// (TestEveryCreateTableDeclaresBinaryCollation): the static scan runs without
// Docker but reads only 00001_initial.sql, while this one is
// migration-count-agnostic and also catches a column-level collation override
// the source scan cannot see.
func TestMySQLBinaryCollationLive(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	dsn := startMySQLContainer(t)

	st, err := mysqlstore.OpenTestable(config.MySQLConfig{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Migrator().Migrate(ctx))

	db, err := sql.Open("mysql", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	rows, err := db.QueryContext(ctx, `SHOW TABLES`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var tables []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		tables = append(tables, name)
	}
	require.NoError(t, rows.Err())
	require.Greater(t, len(tables), 20, "expected the migrated schema to carry many tables; got %v", tables)

	for _, table := range tables {
		if table == "goose_db_version" {
			// The migration tool's bookkeeping table is not ours; it inherits
			// the database default collation and takes part in no cursor.
			continue
		}
		var name, ddl string
		require.NoError(t, db.QueryRowContext(ctx, "SHOW CREATE TABLE `"+table+"`").Scan(&name, &ddl))
		assert.Contains(t, strings.ToLower(ddl), "utf8mb4_bin",
			"table %s must carry the utf8mb4_bin collation; the cursor id tiebreak relies on byte-wise collation", table)
	}
}
