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
	return sqlutil.SetDeletedAt(ctx, h.exec, sqlutil.ParameterStyleQuestionMark, entity, id, deletedAt)
}

func (h *sqliteTestHelper) SetCreatedAt(ctx context.Context, entity store.TestEntity, id string, createdAt time.Time) error {
	return sqlutil.SetCreatedAt(ctx, h.exec, sqlutil.ParameterStyleQuestionMark, entity, id, createdAt)
}

func (h *sqliteTestHelper) SetRevocationEventRevokedAt(ctx context.Context, id string, revokedAt time.Time) error {
	return h.setTimestamp(ctx, sqlutil.TimestampColumnRevocationEventRevokedAt, id, formatSQLiteTime(revokedAt))
}

func (h *sqliteTestHelper) setTimestamp(ctx context.Context, column sqlutil.TimestampColumn, id string, at any) error {
	return sqlutil.SetTimestampColumn(ctx, h.exec, sqlutil.ParameterStyleQuestionMark, column, id, at)
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
