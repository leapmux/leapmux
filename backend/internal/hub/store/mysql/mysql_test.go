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

// TestMySQLDatetimeColumnsLive asserts, against information_schema of the
// actual migrated database, that every `_at` column resolves to DATETIME with
// precision 3..6 and that no column of any name resolves to DATETIME or
// TIMESTAMP without being `_at`-named (TIMESTAMP is banned outright for its
// session-timezone semantics). This is the live twin of the static source scan
// in schema_internal_test.go (TestEveryTimestampColumnDeclaresDatetime): the
// static scan runs without Docker but reads only 00001_initial.sql with a
// line-shape-sensitive text parse, while this one is migration-count-agnostic
// and reads the server's resolved column types. Precision below 3 matters
// because MySQL ROUNDS a fractional second exceeding the column precision, so
// a DATETIME(0..2) column could store an instant after the ms-floored bound
// the sqltime valuers guarantee.
func TestMySQLDatetimeColumnsLive(t *testing.T) {
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

	rows, err := db.QueryContext(ctx, `
		SELECT table_name, column_name, data_type, COALESCE(datetime_precision, 0)
		FROM information_schema.columns
		WHERE table_schema = DATABASE()
		  AND table_name <> 'goose_db_version'
		  AND (column_name LIKE '%\_at' OR data_type IN ('datetime', 'timestamp'))
		ORDER BY table_name, ordinal_position`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()

	atColumns := 0
	for rows.Next() {
		var table, column, dataType string
		var precision int
		require.NoError(t, rows.Scan(&table, &column, &dataType, &precision))
		if strings.HasSuffix(column, "_at") {
			atColumns++
			assert.Equalf(t, "datetime", dataType,
				"%s.%s resolved to %s; `_at` columns must be DATETIME so the sqlc db_type override retypes them to the flooring valuer", table, column, dataType)
			assert.Truef(t, precision >= 3 && precision <= 6,
				"%s.%s resolved to DATETIME(%d); precision must be 3..6 so the column can hold the ms-floored instant (below 3, MySQL ROUNDS past the floor)", table, column, precision)
		} else {
			assert.Failf(t, "misnamed time column",
				"%s.%s resolved to %s; time columns must be named *_at (and TIMESTAMP is banned outright)", table, column, dataType)
		}
	}
	require.NoError(t, rows.Err())
	assert.Greater(t, atColumns, 20, "expected many _at columns in the migrated schema; got %d", atColumns)
}
