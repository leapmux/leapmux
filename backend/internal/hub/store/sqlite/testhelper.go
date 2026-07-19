package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
)

var _ store.TestHelper = (*sqliteTestHelper)(nil)

// testableSQLiteStore extends sqliteStore with test helper operations.
type testableSQLiteStore struct {
	*sqliteStore
}

var _ store.TestableStore = (*testableSQLiteStore)(nil)

// OpenTestable opens a SQLite store that also implements store.TestableStore.
// This is intended for use in tests only.
func OpenTestable(path string) (store.TestableStore, error) {
	st, err := Open(path, sqlitedb.Config{})
	if err != nil {
		return nil, err
	}
	return &testableSQLiteStore{sqliteStore: st.(*sqliteStore)}, nil
}

func (s *testableSQLiteStore) TestHelper() store.TestHelper {
	return &sqliteTestHelper{db: s.conn.shared.db}
}

type sqliteTestHelper struct {
	db *sql.DB
}

func (h *sqliteTestHelper) exec(ctx context.Context, query string, args ...any) error {
	_, err := h.db.ExecContext(ctx, query, args...)
	return err
}

func (h *sqliteTestHelper) SetDeletedAt(ctx context.Context, entity store.TestEntity, id string, deletedAt time.Time) error {
	return h.setEntityTime(ctx, entity, "deleted_at", id, deletedAt)
}

func (h *sqliteTestHelper) SetCreatedAt(ctx context.Context, entity store.TestEntity, id string, createdAt time.Time) error {
	return h.setEntityTime(ctx, entity, "created_at", id, createdAt)
}

func (h *sqliteTestHelper) SetLastActiveAt(ctx context.Context, id string, lastActiveAt time.Time) error {
	return h.setEntityTime(ctx, store.EntitySessions, "last_active_at", id, lastActiveAt)
}

// setEntityTime formats t in the canonical fixed-".000" layout and writes it
// to a fixed column on a validated entity table. This matches every production
// write path byte-for-byte: Go-bound writes use the same formatSQLiteTime, and
// the SQL-side strftime writes (column DEFAULTs, SoftDelete's strftime('now'))
// also store fixed 3-digit fractional seconds on disk -- see sqliteTimeFormat's
// doc in convert.go, including the caution that modernc trims zeros only when a
// DATETIME column is scanned into a Go string.
func (h *sqliteTestHelper) setEntityTime(ctx context.Context, entity store.TestEntity, column, id string, t time.Time) error {
	return h.setEntityColumn(ctx, entity, column, id, formatSQLiteTime(t))
}

// setEntityColumn writes a pre-formatted timestamp string to a fixed column on
// a validated entity table. SQLite stores timestamps as TEXT via strftime
// ('%Y-%m-%dT%H:%M:%fZ'), so the value is formatted with formatSQLiteTime to
// match production rows exactly -- a bound time.Time would serialize in the
// driver's own layout and break byte-sensitive comparisons (the created_at =
// cursor tiebreaker, the datetime()-normalized deleted_at cleanup cutoffs).
// The column is a hardcoded literal, never caller input.
func (h *sqliteTestHelper) setEntityColumn(ctx context.Context, entity store.TestEntity, column, id, value string) error {
	return sqlutil.SetEntityColumnValue(ctx, h.exec, sqlutil.ParameterStyleQuestionMark, entity, column, id, value)
}

func (h *sqliteTestHelper) SetRevocationEventRevokedAt(ctx context.Context, id string, revokedAt time.Time) error {
	return h.setTimestamp(ctx, sqlutil.TimestampColumnRevocationEventRevokedAt, id, formatSQLiteTime(revokedAt))
}

func (h *sqliteTestHelper) setTimestamp(ctx context.Context, column sqlutil.TimestampColumn, id string, at any) error {
	return sqlutil.SetTimestampColumn(ctx, h.exec, sqlutil.ParameterStyleQuestionMark, column, id, at)
}

// canonicalTimestampColumns lists every column the SQLite dialect compares as
// a RAW string (keyset ORDER BY columns, liveness filters, cleanup cutoffs).
// Each one's every write path MUST store the canonical
// strftime('%Y-%m-%dT%H:%M:%fZ') layout: a single raw time.Time bind stores
// modernc's driver layout (space at byte 10) and silently corrupts the
// raw-string compares. CheckCanonicalTimestamps walks this list after each
// storetest subtest, so a NEW write path that forgets the strftime wrap (or a
// new raw-compared column added here) fails the suite instead of shipping a
// #287-shaped silent-row-drop. Columns compared only under datetime() wraps
// (e.g. delegation_tokens.expires_at) are deliberately absent: their binds are
// driver-layout by design.
var canonicalTimestampColumns = map[string][]string{
	"users":                    {"created_at", "deleted_at", "pending_email_expires_at"},
	"user_sessions":            {"last_active_at", "expires_at"},
	"workers":                  {"created_at", "deleted_at"},
	"workspaces":               {"deleted_at"},
	"orgs":                     {"deleted_at"},
	"worker_registration_keys": {"created_at", "expires_at"},
	"api_tokens":               {"created_at", "revoked_at"},
	"delegation_tokens":        {"created_at", "revoked_at"},
	"oauth_tokens":             {"expires_at"},
	"revocation_events":        {"published_at"},
}

// CheckCanonicalTimestamps asserts every raw-string-compared timestamp column
// currently holds only canonical 24-char 'T'-separated values on disk. The
// probe runs SQL-side (length/substr over the stored TEXT) because modernc
// reformats DATETIME values on scan, which would hide a non-canonical layout.
// Intended to run as a test cleanup after store writes; see
// canonicalTimestampColumns for the invariant.
func CheckCanonicalTimestamps(ctx context.Context, st store.TestableStore) error {
	ts, ok := st.(*testableSQLiteStore)
	if !ok {
		return fmt.Errorf("CheckCanonicalTimestamps: not a sqlite testable store: %T", st)
	}
	db := ts.conn.shared.db
	for table, columns := range canonicalTimestampColumns {
		// A migrator test may have rolled the schema back below this table;
		// nothing to check then.
		var exists bool
		if err := db.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?)`, table,
		).Scan(&exists); err != nil {
			return fmt.Errorf("canonical-timestamp walk %s: %w", table, err)
		}
		if !exists {
			continue
		}
		for _, col := range columns {
			var count int
			var sample sql.NullString
			// The column is a hardcoded literal from the map above, never caller input.
			err := db.QueryRowContext(ctx,
				`SELECT COUNT(*), MIN(CAST(`+col+` AS TEXT)) FROM `+table+
					` WHERE `+col+` IS NOT NULL AND (length(CAST(`+col+` AS TEXT)) != 24 OR substr(CAST(`+col+` AS TEXT), 11, 1) != 'T')`,
			).Scan(&count, &sample)
			if err != nil {
				return fmt.Errorf("canonical-timestamp walk %s.%s: %w", table, col, err)
			}
			if count > 0 {
				return fmt.Errorf(
					"%s.%s holds %d non-canonical timestamp value(s) on disk (e.g. %q): a write path is missing its strftime('%%Y-%%m-%%dT%%H:%%M:%%fZ', ...) wrap, which silently corrupts this column's raw-string compares",
					table, col, count, sample.String)
			}
		}
	}
	return nil
}

func (h *sqliteTestHelper) TruncateAll(ctx context.Context) error {
	if _, err := h.db.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		return err
	}
	defer func() { _, _ = h.db.ExecContext(ctx, "PRAGMA foreign_keys = ON") }()
	for _, t := range sqlutil.SQLTruncateTableOrder {
		if _, err := h.db.ExecContext(ctx, "DELETE FROM "+t); err != nil {
			return fmt.Errorf("truncate %s: %w", t, err)
		}
	}
	if _, err := h.db.ExecContext(ctx, "INSERT INTO revocation_event_sequence (id, last_seq) VALUES (1, 0)"); err != nil {
		return fmt.Errorf("reset revocation_event_sequence: %w", err)
	}
	return nil
}
