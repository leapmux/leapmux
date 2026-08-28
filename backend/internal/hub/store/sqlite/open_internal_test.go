package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/store"
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
	tables := []string{"users", "user_sessions", "user_state", "user_op_batches", "workers", "worker_registration_keys"}
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

// TestOpen_SecondBootReseedsTheBuiltIns pins the difference between a seed
// and a migration INSERT: the second boot on an existing database has nothing
// to migrate -- goose is at the latest version -- and must still reconcile the
// built-in registrations. Without it, rows that vanished between boots (a
// hand edit, a partial restore) stayed gone forever, which is the exact state
// the old migration-seeded rows could never recover from.
func TestOpen_SecondBootReseedsTheBuiltIns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.db")

	// First boot: migrates and seeds.
	first, err := Open(path, sqlitedb.Config{})
	require.NoError(t, err)
	require.NoError(t, first.Close())

	// Damage between boots, written the way it actually happens -- raw SQL.
	// No store verb can do it: every editing verb refuses a built-in, which
	// is the point of those refusals.
	raw, err := OpenDB(path, sqlitedb.Config{})
	require.NoError(t, err)
	_, err = raw.Exec("DELETE FROM oauth_clients WHERE registration_source = 'builtin'")
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	// Second boot: migration is a no-op, and the seed must restore the rows.
	second, err := Open(path, sqlitedb.Config{})
	require.NoError(t, err)
	defer func() { _ = second.Close() }()

	for _, clientID := range oauthapp.BuiltInClientIDs() {
		app, err := second.OAuthClients().Get(context.Background(), clientID)
		require.NoErrorf(t, err, "the second boot must reseed %s", clientID)
		assert.Equal(t, store.OAuthClientSourceBuiltin, app.RegistrationSource)
	}
}
