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
	return &sqliteTestHelper{db: s.sqlDB}
}

type sqliteTestHelper struct {
	db *sql.DB
}

func (h *sqliteTestHelper) SetDeletedAt(ctx context.Context, entity store.TestEntity, id string, deletedAt time.Time) error {
	return sqlutil.SetDeletedAt(ctx, h.db, entity, id, deletedAt)
}

func (h *sqliteTestHelper) SetCreatedAt(ctx context.Context, entity store.TestEntity, id string, createdAt time.Time) error {
	return sqlutil.SetCreatedAt(ctx, h.db, entity, id, createdAt)
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
	return nil
}
