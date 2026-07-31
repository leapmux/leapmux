package db_test

import (
	"context"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/worker/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpen_InMemory(t *testing.T) {
	sqlDB, err := db.Open(":memory:", sqlitedb.Config{})
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
	sqlDB, err := db.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	defer func() { _ = sqlDB.Close() }()

	err = db.Migrate(context.Background(), sqlDB)
	require.NoError(t, err)

	// Verify tables exist by querying each one.
	tables := []string{
		"agents",
		"messages",
		"control_requests",
		"terminals",
		"worktrees",
		"worktree_tabs",
	}
	for _, table := range tables {
		var count int64
		err := sqlDB.QueryRow("SELECT count(*) FROM " + table).Scan(&count)
		assert.NoError(t, err, "table %q does not exist or is not queryable", table)
	}
}

func TestMigrate_Idempotent(t *testing.T) {
	sqlDB, err := db.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	defer func() { _ = sqlDB.Close() }()

	// Run migrations twice -- second run should be a no-op.
	err = db.Migrate(context.Background(), sqlDB)
	require.NoError(t, err)

	err = db.Migrate(context.Background(), sqlDB)
	require.NoError(t, err)
}

// TestMigrate_ConcurrentOnDistinctDBs is the regression for goose's
// package-level API.
//
// Migrate used to call goose.SetBaseFS + goose.SetDialect + goose.Up, which
// keep the base FS and the dialect in package GLOBALS. Two Migrate calls in
// flight at once therefore raced on them -- reported by -race the moment more
// than one worker DB is opened concurrently, which the service tests now do on
// every run. The provider API carries both per instance, so there is nothing
// global left to contend on.
//
// Distinct databases on purpose: the point is that concurrency in the MIGRATOR
// is safe, not that SQLite serializes writers.
func TestMigrate_ConcurrentOnDistinctDBs(t *testing.T) {
	t.Parallel()

	const concurrency = 8
	errs := make([]error, concurrency)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range concurrency {
		sqlDB, err := db.Open(":memory:", sqlitedb.Config{})
		require.NoError(t, err)
		t.Cleanup(func() { _ = sqlDB.Close() })

		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release together so the migrations actually overlap
			errs[i] = db.Migrate(context.Background(), sqlDB)
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		assert.NoError(t, err, "concurrent migration %d failed", i)
	}
}

// Migrating an already-migrated database is a no-op, not an error: the worker
// runs Migrate on every start, against a data dir that usually survives.
func TestMigrate_IsIdempotent(t *testing.T) {
	t.Parallel()

	sqlDB, err := db.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	defer func() { _ = sqlDB.Close() }()

	require.NoError(t, db.Migrate(context.Background(), sqlDB))
	require.NoError(t, db.Migrate(context.Background(), sqlDB), "a second Migrate on the same DB must be a no-op")

	var agents int
	require.NoError(t, sqlDB.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'agents'`).Scan(&agents))
	assert.Equal(t, 1, agents, "the schema must survive a repeat migration intact")
}
