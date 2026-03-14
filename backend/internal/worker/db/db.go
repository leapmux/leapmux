package db

import (
	"database/sql"

	"github.com/leapmux/leapmux/internal/util/sqlitedb"
)

// DefaultMaxConns is the default maximum number of open database connections.
const DefaultMaxConns = sqlitedb.DefaultMaxConns

// Open opens a SQLite database at the given path and configures it for
// concurrent use (WAL mode, foreign keys enabled).
// Use ":memory:" for an in-memory database (useful for testing).
// If maxConns is provided, it sets the maximum number of open connections;
// otherwise DefaultMaxConns is used.
func Open(path string, maxConns ...int) (*sql.DB, error) {
	return sqlitedb.Open(path, maxConns...)
}
