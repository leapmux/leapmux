package sqlitedb

import (
	"database/sql"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpen_InMemory(t *testing.T) {
	db, err := Open(":memory:", Config{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	require.NoError(t, db.Ping())

	var fk int
	require.NoError(t, db.QueryRow("PRAGMA foreign_keys").Scan(&fk))
	assert.Equal(t, 1, fk)
}

func TestOpen_File(t *testing.T) {
	path := t.TempDir() + "/test.db"
	db, err := Open(path, Config{})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	require.NoError(t, db.Ping())

	var fk int
	require.NoError(t, db.QueryRow("PRAGMA foreign_keys").Scan(&fk))
	assert.Equal(t, 1, fk)

	var journal string
	require.NoError(t, db.QueryRow("PRAGMA journal_mode").Scan(&journal))
	assert.Equal(t, "wal", journal)

	var timeout int
	require.NoError(t, db.QueryRow("PRAGMA busy_timeout").Scan(&timeout))
	assert.Equal(t, 60000, timeout)
}

func TestOpen_FileWithOptions(t *testing.T) {
	path := t.TempDir() + "/test.db"
	db, err := Open(path, Config{CacheSize: -8000, MmapSize: 268435456})
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	var cacheSize int
	require.NoError(t, db.QueryRow("PRAGMA cache_size").Scan(&cacheSize))
	assert.Equal(t, -8000, cacheSize)

	var mmapSize int
	require.NoError(t, db.QueryRow("PRAGMA mmap_size").Scan(&mmapSize))
	assert.Equal(t, 268435456, mmapSize)
}

// seedForeignStore writes a database that stands in for an agent CLI's own
// session store: WAL mode, a permissive file mode its owner chose, and a row
// that is still only in the -wal because nothing checkpointed it.
func seedForeignStore(t *testing.T) string {
	t.Helper()
	path := t.TempDir() + "/foreign.db"
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)")
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE session (id TEXT PRIMARY KEY, title TEXT)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO session VALUES ('ses_1', 'checkpointed')`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	// Re-open and insert WITHOUT closing, so the row stays in the -wal. A
	// reader that ignores the WAL (the `immutable=1` mistake) sees only the
	// first row.
	live, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = live.Close() })
	_, err = live.Exec(`INSERT INTO session VALUES ('ses_2', 'in the wal')`)
	require.NoError(t, err)

	require.NoError(t, os.Chmod(path, 0o644))
	return path
}

func TestOpenReadOnly_ReadsRowsStillInTheWAL(t *testing.T) {
	path := seedForeignStore(t)

	db, err := OpenReadOnly(path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	rows, err := db.Query(`SELECT id FROM session ORDER BY id`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"ses_1", "ses_2"}, ids,
		"a row still only in the -wal must be visible; `immutable=1` would drop it")
}

func TestOpenReadOnly_LeavesTheStoreAlone(t *testing.T) {
	path := seedForeignStore(t)
	before, err := os.Stat(path)
	require.NoError(t, err)

	db, err := OpenReadOnly(path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	var journal string
	require.NoError(t, db.QueryRow("PRAGMA journal_mode").Scan(&journal))
	assert.Equal(t, "wal", journal, "the owner's journal mode must survive the read")

	after, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, before.Mode(), after.Mode(),
		"OpenReadOnly must not chmod a database it does not own")
}

func TestOpenReadOnly_RefusesToWrite(t *testing.T) {
	path := seedForeignStore(t)

	db, err := OpenReadOnly(path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	_, err = db.Exec(`INSERT INTO session VALUES ('ses_3', 'nope')`)
	assert.Error(t, err, "query_only(1) plus mode=ro must refuse a write")

	var timeout int
	require.NoError(t, db.QueryRow("PRAGMA busy_timeout").Scan(&timeout))
	assert.Equal(t, readOnlyBusyTimeoutMs, timeout,
		"a dialog-path read must give up quickly, not wait out the owner's transaction")
}

func TestOpenReadOnly_MissingFileFails(t *testing.T) {
	_, err := OpenReadOnly(t.TempDir() + "/absent.db")
	assert.Error(t, err, "mode=ro must not create the file the way Open does")
}

func TestBuildReadOnlyDSN(t *testing.T) {
	dsn := buildReadOnlyDSN("/home/user/state_5.sqlite")
	assert.Equal(t,
		"file:/home/user/state_5.sqlite?mode=ro&_pragma=busy_timeout(2000)&_pragma=query_only(1)&_time_format=sqlite",
		dsn)
	// Both are absent on purpose: journal_mode cannot be set on a read-only
	// connection, and foreign_keys only governs writes this DSN forbids.
	assert.NotContains(t, dsn, "journal_mode")
	assert.NotContains(t, dsn, "foreign_keys")
	// The mistake this constructor exists to refuse.
	assert.NotContains(t, dsn, "immutable")
}

func TestBuildReadOnlyDSN_SpecialCharsInPath(t *testing.T) {
	dsn := buildReadOnlyDSN("/home/user/my?data&file.db")
	assert.Contains(t, dsn, "file:/home/user/my%3Fdata&file.db")
	assert.Contains(t, dsn, "mode=ro")
}

func TestBuildDSN_Memory(t *testing.T) {
	dsn := buildDSN(":memory:", Config{})
	assert.Equal(t, ":memory:?_pragma=foreign_keys(1)&_time_format=sqlite", dsn)
}

func TestBuildDSN_AbsolutePath(t *testing.T) {
	dsn := buildDSN("/home/user/data.db", Config{})
	assert.Equal(t, "file:/home/user/data.db?_pragma=busy_timeout(60000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_time_format=sqlite", dsn)
}

// TestBuildDSN_TimeFormatSqliteApplied is a focused regression test
// for the SQLite time-comparison bug: the modernc driver default
// stores time.Time using Go's String() output ("YYYY-MM-DD HH:MM:SS
// +HHMM TZN"), which sorts before strftime('YYYY-MM-DDTHH:MM:SS.SSSZ',
// 'now') for matching dates. `_time_format=sqlite` writes the
// canonical "YYYY-MM-DD HH:MM:SS.SSS+HH:MM" form that SQLite's
// datetime() function parses correctly. If a future refactor drops
// this query parameter, every same-day TTL comparison silently
// breaks — make the breakage loud here.
func TestBuildDSN_TimeFormatSqliteApplied(t *testing.T) {
	for _, path := range []string{":memory:", "/tmp/test.db"} {
		dsn := buildDSN(path, Config{})
		assert.Contains(t, dsn, "_time_format=sqlite", "path=%q must enable canonical time format", path)
	}
}

func TestBuildDSN_WithCacheAndMmap(t *testing.T) {
	dsn := buildDSN("/home/user/data.db", Config{CacheSize: -8000, MmapSize: 268435456})
	assert.Contains(t, dsn, "_pragma=cache_size(-8000)")
	assert.Contains(t, dsn, "_pragma=mmap_size(268435456)")
}

func TestBuildDSN_SpecialCharsInPath(t *testing.T) {
	dsn := buildDSN("/home/user/my?data&file.db", Config{})
	assert.Contains(t, dsn, "file:/home/user/my%3Fdata&file.db")
	assert.Contains(t, dsn, "_pragma=foreign_keys(1)")
}
