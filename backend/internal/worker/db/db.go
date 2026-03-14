package db

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"

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
	// Use _pragma DSN parameters so they are applied to every new
	// connection from the pool (not just the first one).
	dsn := path
	if path == ":memory:" {
		dsn = path + "?_pragma=foreign_keys(1)"
	} else {
		dsn = path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Restrict file permissions to owner-only (0600).
	if path != ":memory:" {
		if err := os.Chmod(path, 0o600); err != nil {
			slog.Warn("failed to chmod database file", "path", path, "error", err)
		}
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
