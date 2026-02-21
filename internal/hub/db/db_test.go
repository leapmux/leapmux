package db_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/db"
)

func TestOpen_InMemory(t *testing.T) {
	sqlDB, err := db.Open(":memory:")
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

func TestMigrate(t *testing.T) {
	sqlDB, err := db.Open(":memory:")
	require.NoError(t, err)
	defer func() { _ = sqlDB.Close() }()

	err = db.Migrate(sqlDB)
	require.NoError(t, err)

	// Verify tables exist by querying each one.
	tables := []string{"orgs", "users", "user_sessions", "workers", "worker_registrations", "workspaces", "messages"}
	for _, table := range tables {
		var count int64
		err := sqlDB.QueryRow("SELECT count(*) FROM " + table).Scan(&count)
		assert.NoError(t, err, "table %q does not exist or is not queryable", table)
	}
}

func TestMigrate_Idempotent(t *testing.T) {
	sqlDB, err := db.Open(":memory:")
	require.NoError(t, err)
	defer func() { _ = sqlDB.Close() }()

	// Run migrations twice — second run should be a no-op.
	err = db.Migrate(sqlDB)
	require.NoError(t, err)

	err = db.Migrate(sqlDB)
	require.NoError(t, err)
}
