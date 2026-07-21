//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/postgres"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

// startPostgresContainer boots a disposable PostgreSQL 17 container and
// returns its connection string. Shared by the store conformance suite and the
// live schema assertions.
func startPostgresContainer(t *testing.T) string {
	t.Helper()
	testutil.ConfigureDockerHost(t)

	ctx := context.Background()
	req := testcontainers.ContainerRequest{
		Image:        "postgres:17-alpine",
		ExposedPorts: []string{"5432/tcp"},
		Env: map[string]string{
			"POSTGRES_USER":     "test",
			"POSTGRES_PASSWORD": "test",
			"POSTGRES_DB":       "leapmux_test",
		},
		WaitingFor: wait.ForSQL("5432/tcp", "pgx", func(host string, port string) string {
			return fmt.Sprintf("postgres://test:test@%s:%s/leapmux_test?sslmode=disable", host, testutil.PortNumber(port))
		}),
	}
	pgContainer, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pgContainer.Terminate(ctx) })

	host, err := pgContainer.Host(ctx)
	require.NoError(t, err)
	port, err := pgContainer.MappedPort(ctx, "5432")
	require.NoError(t, err)

	return "postgres://test:test@" + host + ":" + port.Port() + "/leapmux_test?sslmode=disable"
}

func TestPostgresStore(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	connStr := startPostgresContainer(t)

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

// TestPostgresBinaryCollationLive pins the byte-wise-collation invariant
// against the ACTUAL migrated schema: every TEXT column that serves as a
// keyset id tiebreaker or an FK to one -- named "id", "*_id", or in the
// FK-author set (registered_by, created_by) -- must carry an explicit
// COLLATE "C", so `id < $cursor_id` compares byte-wise on every deployment
// instead of inheriting the database's locale collation. Unlike MySQL's
// table-level suffix, Postgres applies the clause per column, so a new table's
// id column can silently omit it; information_schema reports collation_name
// only when it differs from the database default, so asserting the literal
// 'C' catches an omitted clause even in a C-locale test database.
// Point-lookup secrets that never tiebreak a cursor (device_code, user_code,
// code, state, token, auth_token, provider_subject) also carry COLLATE "C"
// for byte-wise equality, but fall outside the name shape and are not
// enumerated here.
// Live twin of mysql's TestMySQLBinaryCollationLive; sqlite needs no
// counterpart because its TEXT comparison is BINARY unless a collation is
// declared. YugabyteDB inherits this pin via the shared migration.
func TestPostgresBinaryCollationLive(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	connStr := startPostgresContainer(t)

	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	sqlDB := stdlib.OpenDBFromPool(pool)
	st, err := postgres.NewTestableFromPool(pool, sqlDB)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Migrator().Migrate(ctx))

	// Beyond the id name shape, enumerate the columns whose byte-wise ORDERING
	// is behaviorally load-bearing despite falling outside it: the CRDT
	// journal's origin_client tiebreak and compaction watermark must order
	// exactly like Go's plain string compare (crdt/op.go), and the four
	// lexorank position columns must order exactly like the Go/TS comparators
	// for ANY future alphabet -- a dropped COLLATE "C" on these regresses
	// silently under a linguistic database locale.
	rows, err := pool.Query(ctx, `
		SELECT table_name, column_name, COALESCE(collation_name, '<database default>')
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND data_type = 'text'
		  AND table_name <> 'goose_db_version'
		  AND (column_name = 'id' OR column_name LIKE '%\_id' OR column_name IN ('registered_by', 'created_by')
		       OR (table_name, column_name) IN (
		           ('org_op_batches', 'origin_client'),
		           ('org_recent_batch_ids', 'canonical_client'),
		           ('workspace_sections', 'position'),
		           ('workspace_section_items', 'position'),
		           ('workspace_tab_owned', 'position'),
		           ('workspace_tab_rendered', 'position')))
		ORDER BY table_name, column_name`)
	require.NoError(t, err)
	defer rows.Close()
	checked := 0
	for rows.Next() {
		var table, column, collation string
		require.NoError(t, rows.Scan(&table, &column, &collation))
		checked++
		assert.Equalf(t, "C", collation,
			"%s.%s must carry an explicit COLLATE \"C\" so the cursor id tiebreak and FK joins compare byte-wise on every deployment", table, column)
	}
	require.NoError(t, rows.Err())
	// The migration declares 74 COLLATE "C" columns; a collapse of this count
	// means the name heuristic (or the schema) broke, not that fewer pins are
	// needed.
	assert.Greater(t, checked, 40, "expected many id/FK TEXT columns; the name heuristic may have broken (got %d)", checked)
}

// TestPostgresTimestamptzColumnsLive asserts, against information_schema of
// the actual migrated database, that every `_at` column resolves to
// timestamptz at the default microsecond precision (6) and that no column of
// any name resolves to a timezone-naive timestamp (banned outright: it
// silently reinterprets instants across session timezones). This is the live
// twin of the static source scan in schema_internal_test.go
// (TestEveryTimestampColumnDeclaresTimestamptz): the static scan runs without
// Docker but reads only 00001_initial.sql with a line-shape-sensitive text
// parse, while this one is migration-count-agnostic and reads the server's
// resolved column types. Precision below 6 matters because Postgres ROUNDS a
// fractional second exceeding the column precision, so a TIMESTAMPTZ(p<6)
// column could store an instant after the microsecond-floored bound the
// pgtime valuers guarantee. YugabyteDB inherits this pin via the shared
// migration.
func TestPostgresTimestamptzColumnsLive(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	connStr := startPostgresContainer(t)

	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	sqlDB := stdlib.OpenDBFromPool(pool)
	st, err := postgres.NewTestableFromPool(pool, sqlDB)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Migrator().Migrate(ctx))

	rows, err := pool.Query(ctx, `
		SELECT table_name, column_name, data_type, COALESCE(datetime_precision, 0)
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name <> 'goose_db_version'
		  AND (column_name LIKE '%\_at'
		       OR data_type IN ('timestamp with time zone', 'timestamp without time zone'))
		ORDER BY table_name, ordinal_position`)
	require.NoError(t, err)
	defer rows.Close()

	atColumns := 0
	for rows.Next() {
		var table, column, dataType string
		var precision int
		require.NoError(t, rows.Scan(&table, &column, &dataType, &precision))
		if strings.HasSuffix(column, "_at") {
			atColumns++
			assert.Equalf(t, "timestamp with time zone", dataType,
				"%s.%s resolved to %s; `_at` columns must be timestamptz so the sqlc db_type override retypes them to the flooring valuer", table, column, dataType)
			assert.Equalf(t, 6, precision,
				"%s.%s resolved to timestamptz(%d); precision must be the default 6 so the column can hold the microsecond-floored instant (below 6, Postgres ROUNDS past the floor)", table, column, precision)
		} else {
			assert.Failf(t, "misnamed time column",
				"%s.%s resolved to %s; time columns must be named *_at (and timezone-naive timestamp is banned outright)", table, column, dataType)
		}
	}
	require.NoError(t, rows.Err())
	assert.Greater(t, atColumns, 20, "expected many _at columns in the migrated schema; got %d", atColumns)
}
