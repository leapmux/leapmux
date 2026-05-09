package sqlitedb

import (
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
