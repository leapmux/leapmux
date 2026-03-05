package db

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// DefaultMaxConns is the default maximum number of open database connections.
// SQLite in WAL mode supports concurrent readers alongside a single writer,
// so we allow multiple connections to avoid serializing all DB operations
// (reads included) through a single connection.
const DefaultMaxConns = 4

// Open opens a SQLite database at the given path and configures it for
// concurrent use (WAL mode, foreign keys enabled).
// Use ":memory:" for an in-memory database (useful for testing).
// If maxConns is provided, it sets the maximum number of open connections;
// otherwise DefaultMaxConns is used.
func Open(path string, maxConns ...int) (*sql.DB, error) {
	dsn := path
	if path != ":memory:" {
		dsn = path + "?_busy_timeout=5000"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Enable WAL mode for better concurrent read performance.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}

	// Enable foreign key enforcement.
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	// In-memory databases must use a single connection because each
	// connection gets its own isolated database instance.
	if path == ":memory:" {
		db.SetMaxOpenConns(1)
	} else {
		n := DefaultMaxConns
		if len(maxConns) > 0 && maxConns[0] > 0 {
			n = maxConns[0]
		}
		db.SetMaxOpenConns(n)
	}

	return db, nil
}
