package db

import (
	"database/sql"

	"github.com/leapmux/leapmux/internal/util/sqlitedb"
)

// Open opens a SQLite database at the given path and configures it for
// concurrent use (WAL mode, foreign keys enabled).
// Use ":memory:" for an in-memory database (useful for testing).
func Open(path string, cfg sqlitedb.Config) (*sql.DB, error) {
	return sqlitedb.Open(path, cfg)
}
