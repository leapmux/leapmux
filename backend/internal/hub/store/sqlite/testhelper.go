package sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/sqltime"
)

var _ store.TestHelper = (*sqliteTestHelper)(nil)

// testableSQLiteStore extends sqliteStore with test helper operations.
type testableSQLiteStore struct {
	*sqliteStore
	borrowed bool
}

func (s *testableSQLiteStore) Close() error {
	if s.borrowed {
		return nil
	}
	return s.sqliteStore.Close()
}

var _ store.TestableStore = (*testableSQLiteStore)(nil)

// OpenTestable opens a SQLite store with a fresh random keystore that also
// implements store.TestableStore. This is intended for use in tests only.
func OpenTestable(path string) (store.TestableStore, error) {
	ks, err := keystore.NewRandom()
	if err != nil {
		return nil, err
	}
	return OpenTestableWithKeystore(path, ks)
}

// OpenTestableWithKeystore opens a SQLite store using ks that also implements
// store.TestableStore. This is intended for use in tests only.
func OpenTestableWithKeystore(path string, ks *keystore.Keystore) (store.TestableStore, error) {
	st, err := Open(path, sqlitedb.Config{}, ks)
	if err != nil {
		return nil, err
	}
	return &testableSQLiteStore{sqliteStore: st.(*sqliteStore)}, nil
}

func (s *testableSQLiteStore) TestHelper() store.TestHelper {
	return &sqliteTestHelper{shared: s.conn.shared}
}

type sqliteTestHelper struct {
	shared *sqliteShared
}

func (h *sqliteTestHelper) exec(ctx context.Context, query string, args ...any) error {
	_, err := h.shared.db.ExecContext(ctx, query, args...)
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

func (h *sqliteTestHelper) SetDeregisteringAt(ctx context.Context, workerID string, deregisteringAt time.Time) error {
	return h.setEntityTime(ctx, store.EntityWorkers, "deregistering_at", workerID, deregisteringAt)
}

func (h *sqliteTestHelper) ClearWorkerDeregisteringAt(ctx context.Context, workerID string) error {
	return sqlutil.ClearWorkerDeregisteringAt(ctx, h.exec, sqlutil.DialectSQLite, workerID)
}

// setEntityTime binds t as a SQLiteTime and writes it to a fixed column on a
// validated entity table. This matches every production write path
// byte-for-byte: Go-bound writes route through the same SQLiteTime.Value(), and
// the SQL-side strftime writes (column DEFAULTs, SoftDelete's strftime('now'))
// also store fixed 3-digit fractional seconds on disk -- see the SQLiteTime doc
// in the sqltime package, including the caution that modernc trims zeros only
// when a DATETIME column is scanned into a Go string.
func (h *sqliteTestHelper) setEntityTime(ctx context.Context, entity store.TestEntity, column, id string, t time.Time) error {
	return h.setEntityColumn(ctx, entity, column, id, sqltime.NewSQLiteTime(t))
}

// setEntityColumn writes a timestamp value to a fixed column on a validated
// entity table. The value is a SQLiteTime Valuer so the driver serializes the
// canonical strftime('%Y-%m-%dT%H:%M:%fZ') layout, matching production rows
// exactly -- a bound raw time.Time would serialize in the driver's own layout
// and break byte-sensitive comparisons (the created_at = cursor tiebreaker, the
// raw-string deleted_at cleanup cutoffs). The column is a hardcoded literal,
// never caller input.
func (h *sqliteTestHelper) setEntityColumn(ctx context.Context, entity store.TestEntity, column, id string, value any) error {
	return sqlutil.SetEntityColumnValue(ctx, h.exec, sqlutil.DialectSQLite, entity, column, id, value)
}

// CheckCanonicalTimestamps asserts every DATETIME column of every table
// currently holds only canonical 24-char 'T'-separated values on disk
// (see sqlitedb.FindNonCanonicalDatetimes for the discovery and probe
// mechanics). Intended to run as a test cleanup after store writes; it walks
// after each storetest subtest, so a NEW write path that binds a raw time.Time
// instead of a SQLiteTime fails the suite instead of shipping a #287-shaped
// silent-row-drop. All offending columns are reported at once.
func CheckCanonicalTimestamps(ctx context.Context, st store.TestableStore) error {
	ts, ok := st.(*testableSQLiteStore)
	if !ok {
		return fmt.Errorf("CheckCanonicalTimestamps: not a sqlite testable store: %T", st)
	}
	db := ts.conn.shared.db
	// goose_db_version is goose's own bookkeeping table (TIMESTAMP via
	// datetime('now')), not part of the store's canonical-layout contract.
	// Per-column coverage is deliberately NOT asserted here: no single
	// storetest subtest writes every table, so a non-vacuity check would fail
	// spuriously. That assertion lives in the dedicated
	// TestAllDatetimeColumnsStoreCanonicalLayout tests, whose fixtures do
	// populate every column.
	offenders, columns, err := sqlitedb.FindNonCanonicalDatetimes(ctx, db, "goose_db_version")
	if err != nil {
		return err
	}
	if len(columns) == 0 {
		// A migrator test may have rolled the schema back to zero; with no
		// application tables there is legitimately nothing to walk. With
		// tables present, zero DATETIME columns means the discovery query
		// broke and the walk would pass vacuously.
		var tables int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name != 'goose_db_version'`,
		).Scan(&tables); err != nil {
			return fmt.Errorf("canonical-timestamp table count: %w", err)
		}
		if tables == 0 {
			return nil
		}
		return fmt.Errorf("canonical-timestamp walk found no DATETIME columns across %d table(s); the discovery query is broken", tables)
	}
	if len(offenders) > 0 {
		return fmt.Errorf(
			"non-canonical timestamp value(s) on disk -- a write path bound a raw time.Time instead of a SQLiteTime, which silently corrupts raw-string compares:\n  %s",
			strings.Join(offenders, "\n  "))
	}
	return nil
}

// ListAllOwnedTabs reads workspace_tab_owned without an owner predicate. See
// store.TestHelper.ListAllOwnedTabs for why this is test-only.
func (h *sqliteTestHelper) ListAllOwnedTabs(ctx context.Context) ([]store.WorkspaceTabRow, error) {
	return sqlutil.ListAllOwnedTabs(ctx, h.shared.db)
}

func (h *sqliteTestHelper) TruncateAll(ctx context.Context) error {
	if _, err := h.shared.db.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		return err
	}
	defer func() { _, _ = h.shared.db.ExecContext(ctx, "PRAGMA foreign_keys = ON") }()
	for _, t := range sqlutil.SQLTruncateTableOrder {
		if _, err := h.shared.db.ExecContext(ctx, sqlutil.TruncateStatement(t)); err != nil {
			return fmt.Errorf("truncate %s: %w", t, err)
		}
	}
	if _, err := h.shared.db.ExecContext(ctx, "INSERT INTO revocation_event_sequence (id, last_seq) VALUES (1, 0)"); err != nil {
		return fmt.Errorf("reset revocation_event_sequence: %w", err)
	}
	return nil
}

func (h *sqliteTestHelper) TableHoldsText(ctx context.Context, table, text string) (bool, error) {
	return sqlutil.TableHoldsText(ctx, h.shared.db, table, text)
}

func (h *sqliteTestHelper) WithKeystore(ks *keystore.Keystore) store.TestableStore {
	if ks == nil {
		panic("sqlite: WithKeystore requires a non-nil keystore")
	}
	return &testableSQLiteStore{
		sqliteStore: &sqliteStore{
			conn: &sqliteConn{
				shared: h.shared,
				ks:     ks,
				exec:   h.shared.db,
				q:      gendb.New(h.shared.db),
			},
		},
		borrowed: true,
	}
}

func (h *sqliteTestHelper) CopyWorkspaceTitleCiphertext(ctx context.Context, srcWorkspaceID, dstWorkspaceID string) error {
	return sqlutil.CopyWorkspaceTitleCiphertext(ctx, h.shared.db, h.exec, sqlutil.DialectSQLite, srcWorkspaceID, dstWorkspaceID)
}

func (h *sqliteTestHelper) SetSettingSecretRaw(ctx context.Context, key string, raw []byte) error {
	return sqlutil.SetSettingSecretRaw(ctx, h.exec, sqlutil.DialectSQLite, key, raw)
}

func (h *sqliteTestHelper) CopySettingSecretCiphertext(ctx context.Context, srcKey, dstKey string) error {
	return sqlutil.CopySettingSecretCiphertext(ctx, h.shared.db, h.exec, sqlutil.DialectSQLite, srcKey, dstKey)
}
