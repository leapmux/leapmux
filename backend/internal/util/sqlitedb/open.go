package sqlitedb

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"

	_ "modernc.org/sqlite"
)

// DefaultMaxConns is the default maximum number of open database connections.
// SQLite in WAL mode supports concurrent readers alongside a single writer,
// so we allow multiple connections to avoid serializing all DB operations
// (reads included) through a single connection.
const DefaultMaxConns = 4

// Config holds tuning options for a SQLite database.
type Config struct {
	MaxConns  int // Maximum open connections. 0 = DefaultMaxConns.
	CacheSize int // Page cache size (negative = KiB, positive = pages). 0 = SQLite default (-2000 = 2 MiB).
	MmapSize  int // Memory-mapped I/O size in bytes. 0 = disabled.
}

// Open opens a SQLite database at the given path and configures it for
// concurrent use (WAL mode, foreign keys enabled).
// Use ":memory:" for an in-memory database (useful for testing).
func Open(path string, cfg Config) (*sql.DB, error) {
	db, err := sql.Open("sqlite", buildDSN(path, cfg))
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// In-memory databases must use a single connection because each
	// connection gets its own isolated database instance.
	if path == ":memory:" {
		db.SetMaxOpenConns(1)
	} else {
		n := cfg.MaxConns
		if n <= 0 {
			n = DefaultMaxConns
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
//
// `_time_format=sqlite` is critical: it instructs the modernc driver to
// serialize time.Time values as `YYYY-MM-DD HH:MM:SS.SSS[+-]HH:MM` (a
// canonical format SQLite's date/time functions parse), instead of
// the driver default which writes Go's time.Time.String() output and
// breaks SQL `>`/`<` comparisons against `strftime('now')` whenever
// the two values fall on the same calendar day.
func buildDSN(path string, cfg Config) string {
	if path == ":memory:" {
		return ":memory:?_pragma=foreign_keys(1)&_time_format=sqlite"
	}

	// 60s busy_timeout: high enough to never trigger during normal
	// operation, but still acts as a safety net against stuck transactions.
	// Request-scoped contexts provide the real timeout boundary.
	pragmas := "_pragma=busy_timeout(60000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_time_format=sqlite"
	if cfg.CacheSize != 0 {
		pragmas += "&_pragma=cache_size(" + strconv.Itoa(cfg.CacheSize) + ")"
	}
	if cfg.MmapSize > 0 {
		pragmas += "&_pragma=mmap_size(" + strconv.Itoa(cfg.MmapSize) + ")"
	}

	u := &url.URL{
		Scheme:   "file",
		OmitHost: true,
		Path:     path,
		RawQuery: pragmas,
	}
	return u.String()
}
