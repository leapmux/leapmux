package sqlitedb

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"math"
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

// Open opens a SQLite database that this program owns and configures it for
// concurrent use: WAL mode, foreign keys, write transactions that take the
// write lock at BEGIN, and a lock wait without a practical limit (see buildDSN).
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

// readOnlyBusyTimeoutMs is how long a read-only connection waits for a lock.
//
// Open waits for a lock without a practical limit (ownBusyTimeoutMs), and this
// one must not, because the databases OpenReadOnly serves are FOREIGN: a
// coding-agent CLI owns them and can hold a write transaction for as long as it
// likes. This connection is on the path of an interactive dialog, so it must
// give up quickly and report no sessions rather than hold the dialog open
// behind another program's transaction.
const readOnlyBusyTimeoutMs = 2000

// OpenReadOnly opens a SQLite database that this program does NOT own, for
// reading only. The agent-session stores of Codex, OpenCode, Kilo, MiMo Code,
// Goose, ZCode and Cursor are read through it.
//
// Separate from Open because Open MUTATES the file it opens: it chmods the
// file to 0600 and sets journal_mode(WAL). Applied to a user's own Codex
// database, the first takes away a permission its owner chose and the second
// rewrites the journal mode of a store that another program runs on.
//
// `immutable=1` is not used, although it is the obvious way to promise SQLite
// that nothing else writes. It makes SQLite ignore the -wal file, and every one
// of these stores runs in WAL mode with live writers -- so the newest sessions,
// which are the ones a session picker exists to show, would be missing with no
// error to say so. `mode=ro` reads the WAL correctly; it needs write permission
// on the -shm file, which the worker has because it runs as the user who owns
// the store.
//
// The context bounds the OPEN, not only the queries that follow it. sql.Open
// itself connects to nothing, so the first real work is the ping, and the ping
// is what waits out the busy timeout against a store whose owner holds a lock.
// A caller that opens one database per session pays that wait once per
// database, so a deadline the ping ignores is a deadline the whole scan
// ignores.
//
// The caller must Close the returned handle.
func OpenReadOnly(ctx context.Context, path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", buildReadOnlyDSN(path))
	if err != nil {
		return nil, fmt.Errorf("open database read-only: %w", err)
	}
	// One connection: these handles are opened for a single short query and
	// closed, so a pool buys nothing and would multiply the -shm contention
	// with the owning program.
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping database read-only: %w", err)
	}
	return db, nil
}

// buildReadOnlyDSN is buildDSN's counterpart for a database this program does
// not own. It deliberately omits `journal_mode`, which cannot be set on a
// read-only connection, and `foreign_keys`, which only affects writes.
// `query_only` makes the read-only intent enforced by SQLite rather than by
// convention, so a query that tries to write fails here instead of at the file.
func buildReadOnlyDSN(path string) string {
	u := &url.URL{
		Scheme:   "file",
		OmitHost: true,
		Path:     path,
		RawQuery: "mode=ro&_pragma=busy_timeout(" + strconv.Itoa(readOnlyBusyTimeoutMs) + ")&_pragma=query_only(1)&_time_format=sqlite",
	}
	return u.String()
}

// ownBusyTimeoutMs is how long a connection to a database this program owns
// waits for a lock: the largest value SQLite accepts, about 24.8 days, because
// SQLite has no setting for a wait without a limit.
//
// Every writer of such a database is this program, and each one holds the lock
// for one short local transaction. A limit therefore only turns a slow writer
// into a failed operation, and no caller can do better than wait. The cost is a
// self-deadlock: code that holds a write transaction and waits for a write on a
// second pooled connection now hangs rather than failing after a limit. Such
// code is a defect either way.
const ownBusyTimeoutMs = math.MaxInt32

// buildDSN constructs a SQLite DSN with pragma parameters applied via the
// connection string so they take effect on every pooled connection.
// It uses the file: URI scheme to safely separate the path from query
// parameters, avoiding issues if the path contains special characters.
//
// `_txlock=immediate` makes every write transaction take the write lock at its
// BEGIN, and the busy timeout above then covers the wait for it. The driver
// leaves a read-only transaction (`sql.TxOptions{ReadOnly: true}`) deferred, so
// readers stay concurrent with the writer. A deferred write transaction is the
// mode to avoid. It reads under a snapshot and upgrades the snapshot at its
// first write, and SQLite refuses that upgrade at once (SQLITE_BUSY, or
// SQLITE_BUSY_SNAPSHOT when another writer committed in between) whenever
// another connection holds or took the lock. SQLite never calls the busy
// handler for that refusal, so no timeout and no immediate restart can wait it
// out.
//
// `_time_format=sqlite` is critical: it instructs the modernc driver to
// serialize time.Time values as `YYYY-MM-DD HH:MM:SS.SSS[+-]HH:MM` (a
// canonical format SQLite's date/time functions parse), instead of
// the driver default which writes Go's time.Time.String() output and
// breaks SQL `>`/`<` comparisons against `strftime('now')` whenever
// the two values fall on the same calendar day.
func buildDSN(path string, cfg Config) string {
	if path == ":memory:" {
		// One connection serves an in-memory database, so nothing contends for
		// its lock. It still takes the same transaction mode, so a test on it
		// runs the transactions that production runs.
		return ":memory:?_pragma=foreign_keys(1)&_txlock=immediate&_time_format=sqlite"
	}

	pragmas := "_pragma=busy_timeout(" + strconv.Itoa(ownBusyTimeoutMs) + ")&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_txlock=immediate&_time_format=sqlite"
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
