package db_test

import (
	"context"
	"strings"
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
		"agent_turn_ends",
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

func TestMigrate_SeedsActiveArchiveState(t *testing.T) {
	t.Parallel()

	sqlDB, err := db.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.Migrate(context.Background(), sqlDB))
	require.NoError(t, func() error {
		_, err := sqlDB.Exec(`INSERT INTO agents (id) VALUES ('agent-1')`)
		return err
	}())
	require.NoError(t, func() error {
		_, err := sqlDB.Exec(`INSERT INTO terminals (id) VALUES ('terminal-1')`)
		return err
	}())

	var agentArchived, terminalArchived int64
	require.NoError(t, sqlDB.QueryRow(`SELECT workspace_archived FROM agents WHERE id = 'agent-1'`).Scan(&agentArchived))
	require.NoError(t, sqlDB.QueryRow(`SELECT workspace_archived FROM terminals WHERE id = 'terminal-1'`).Scan(&terminalArchived))
	assert.Zero(t, agentArchived)
	assert.Zero(t, terminalArchived)
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

// TestInputQueueStateRefusesHalfAPause pins the table constraint that makes
// pause_reason the paused flag. A pause states its reason and its owner
// together, so a row with one and not the other is a state no reader can
// interpret, and the database refuses it.
func TestInputQueueStateRefusesHalfAPause(t *testing.T) {
	t.Parallel()

	sqlDB, err := db.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	ctx := context.Background()
	require.NoError(t, db.Migrate(ctx, sqlDB))

	for name, c := range map[string]struct {
		reason, owner any
		refused       bool
	}{
		"a reason without an owner": {reason: 1, owner: nil, refused: true},
		"an owner without a reason": {reason: nil, owner: 1, refused: true},
		"a pause with both":         {reason: 1, owner: 1},
		"no pause":                  {reason: nil, owner: nil},
	} {
		t.Run(name, func(t *testing.T) {
			agentID := "agent-" + strings.ReplaceAll(name, " ", "-")
			_, err := sqlDB.ExecContext(ctx, `INSERT INTO agents (id) VALUES (?)`, agentID)
			require.NoError(t, err)
			_, err = sqlDB.ExecContext(ctx,
				`INSERT INTO agent_input_queue_state (agent_id, pause_reason, pause_owner) VALUES (?, ?, ?)`,
				agentID, c.reason, c.owner)
			if c.refused {
				assert.ErrorContains(t, err, "CHECK constraint failed")
				return
			}
			assert.NoError(t, err)
		})
	}
}

// TestAttachmentSizeIsTheDataLength pins the generated size column. The
// database derives it from the bytes, so it always agrees with them, and a
// writer cannot state it.
func TestAttachmentSizeIsTheDataLength(t *testing.T) {
	t.Parallel()

	sqlDB, err := db.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	ctx := context.Background()
	require.NoError(t, db.Migrate(ctx, sqlDB))
	for _, statement := range []string{
		`INSERT INTO agents (id) VALUES ('agent-1')`,
		`INSERT INTO agent_input_queue_items (id, agent_id, order_index, kind, state) VALUES ('item-1', 'agent-1', 1, 1, 1)`,
	} {
		_, err := sqlDB.ExecContext(ctx, statement)
		require.NoError(t, err)
	}

	_, err = sqlDB.ExecContext(ctx,
		`INSERT INTO agent_input_queue_attachments (item_id, position, filename, mime_type, data) VALUES ('item-1', 0, 'a.bin', 'application/octet-stream', ?)`,
		[]byte("hello"))
	require.NoError(t, err)
	var size int64
	require.NoError(t, sqlDB.QueryRowContext(ctx,
		`SELECT size FROM agent_input_queue_attachments WHERE item_id = 'item-1' AND position = 0`).Scan(&size))
	assert.Equal(t, int64(5), size)

	_, err = sqlDB.ExecContext(ctx,
		`INSERT INTO agent_input_queue_attachments (item_id, position, filename, mime_type, data, size) VALUES ('item-1', 1, 'b.bin', 'application/octet-stream', ?, 5)`,
		[]byte("hello"))
	assert.Error(t, err, "a writer cannot state a generated column")
}
