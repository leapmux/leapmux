package sqlitedb

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/url"
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
	db, err := sql.Open("sqlite", buildDSN(path))
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
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

	// Force a connection to ensure the file is created before chmod.
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	// Restrict file permissions to owner-only (0600).
	if path != ":memory:" {
		if err := os.Chmod(path, 0o600); err != nil {
			slog.Warn("failed to chmod database file", "path", path, "error", err)
		}
	}

	return db, nil
}

// buildDSN constructs a SQLite DSN with pragma parameters applied via the
// connection string so they take effect on every pooled connection.
// It uses the file: URI scheme to safely separate the path from query
// parameters, avoiding issues if the path contains special characters.
func buildDSN(path string) string {
	// 60s busy_timeout: high enough to never trigger during normal
	// operation, but still acts as a safety net against stuck transactions.
	// Request-scoped contexts provide the real timeout boundary.
	const filePragmas = "_pragma=busy_timeout(60000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	const memoryPragmas = "_pragma=foreign_keys(1)"

	if path == ":memory:" {
		return ":memory:?" + memoryPragmas
	}

	u := &url.URL{
		Scheme:   "file",
		OmitHost: true,
		Path:     path,
		RawQuery: filePragmas,
	}
	return u.String()
}
