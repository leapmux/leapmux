package sqlitedb

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpen_InMemory(t *testing.T) {
	db, err := Open(":memory:")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	require.NoError(t, db.Ping())

	var fk int
	require.NoError(t, db.QueryRow("PRAGMA foreign_keys").Scan(&fk))
	assert.Equal(t, 1, fk)
}

func TestOpen_File(t *testing.T) {
	path := t.TempDir() + "/test.db"
	db, err := Open(path)
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

func TestBuildDSN_Memory(t *testing.T) {
	dsn := buildDSN(":memory:")
	assert.Equal(t, ":memory:?_pragma=foreign_keys(1)", dsn)
}

func TestBuildDSN_AbsolutePath(t *testing.T) {
	dsn := buildDSN("/home/user/data.db")
	assert.Equal(t, "file:/home/user/data.db?_pragma=busy_timeout(60000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", dsn)
}

func TestBuildDSN_SpecialCharsInPath(t *testing.T) {
	dsn := buildDSN("/home/user/my?data&file.db")
	assert.Contains(t, dsn, "file:/home/user/my%3Fdata&file.db")
	assert.Contains(t, dsn, "_pragma=foreign_keys(1)")
}
