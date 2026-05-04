package sqlite

import (
	"context"
	"testing"

	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenDB_InMemory(t *testing.T) {
	sqlDB, err := OpenDB(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	defer func() { _ = sqlDB.Close() }()

	// Verify the connection works.
	err = sqlDB.Ping()
	require.NoError(t, err)

	// Verify foreign keys are enabled.
	var fkEnabled int
	err = sqlDB.QueryRow("PRAGMA foreign_keys").Scan(&fkEnabled)
	require.NoError(t, err)
	assert.Equal(t, 1, fkEnabled)
}

func TestMigrateDB(t *testing.T) {
	sqlDB, err := OpenDB(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	defer func() { _ = sqlDB.Close() }()

	m, err := newMigrator(sqlDB)
	require.NoError(t, err)
	require.NoError(t, m.Migrate(context.Background()))

	// Verify tables exist by querying each one.
	tables := []string{"orgs", "users", "user_sessions", "workers", "worker_registration_keys", "worker_access_grants"}
	for _, table := range tables {
		var count int64
		err := sqlDB.QueryRow("SELECT count(*) FROM " + table).Scan(&count)
		assert.NoError(t, err, "table %q does not exist or is not queryable", table)
	}
}

func TestMigrateDB_Idempotent(t *testing.T) {
	sqlDB, err := OpenDB(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	defer func() { _ = sqlDB.Close() }()

	// Run migrations twice -- second run should be a no-op.
	m, err := newMigrator(sqlDB)
	require.NoError(t, err)
	require.NoError(t, m.Migrate(context.Background()))

	m2, err := newMigrator(sqlDB)
	require.NoError(t, err)
	require.NoError(t, m2.Migrate(context.Background()))
}
