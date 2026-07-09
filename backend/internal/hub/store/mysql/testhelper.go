package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
)

// testableMySQLStore extends mysqlStore with test helper operations.
type testableMySQLStore struct {
	*mysqlStore
}

var _ store.TestableStore = (*testableMySQLStore)(nil)

// OpenTestable opens a MySQL store that also implements store.TestableStore.
// This is intended for use in tests only.
func OpenTestable(cfg config.MySQLConfig) (store.TestableStore, error) {
	st, err := Open(cfg)
	if err != nil {
		return nil, err
	}
	return &testableMySQLStore{mysqlStore: st.(*mysqlStore)}, nil
}

func (s *testableMySQLStore) TestHelper() store.TestHelper {
	return &mysqlTestHelper{db: s.conn.shared.db}
}

type mysqlTestHelper struct {
	db *sql.DB
}

func (h *mysqlTestHelper) exec(ctx context.Context, query string, args ...any) error {
	_, err := h.db.ExecContext(ctx, query, args...)
	return err
}

func (h *mysqlTestHelper) SetDeletedAt(ctx context.Context, entity store.TestEntity, id string, deletedAt time.Time) error {
	return sqlutil.SetDeletedAt(ctx, h.exec, sqlutil.ParameterStyleQuestionMark, entity, id, deletedAt)
}

func (h *mysqlTestHelper) SetCreatedAt(ctx context.Context, entity store.TestEntity, id string, createdAt time.Time) error {
	return sqlutil.SetCreatedAt(ctx, h.exec, sqlutil.ParameterStyleQuestionMark, entity, id, createdAt)
}

func (h *mysqlTestHelper) SetRevocationEventRevokedAt(ctx context.Context, id string, revokedAt time.Time) error {
	return h.setTimestamp(ctx, sqlutil.TimestampColumnRevocationEventRevokedAt, id, revokedAt)
}

func (h *mysqlTestHelper) setTimestamp(ctx context.Context, column sqlutil.TimestampColumn, id string, at any) error {
	return sqlutil.SetTimestampColumn(ctx, h.exec, sqlutil.ParameterStyleQuestionMark, column, id, at)
}

func (h *mysqlTestHelper) TruncateAll(ctx context.Context) error {
	if _, err := h.db.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 0"); err != nil {
		return err
	}
	defer func() { _, _ = h.db.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 1") }()
	for _, t := range sqlutil.SQLTruncateTableOrder {
		if _, err := h.db.ExecContext(ctx, "TRUNCATE TABLE "+t); err != nil {
			return fmt.Errorf("truncate %s: %w", t, err)
		}
	}
	if _, err := h.db.ExecContext(ctx, "INSERT INTO revocation_event_sequence (id, last_seq) VALUES (1, 0)"); err != nil {
		return fmt.Errorf("reset revocation_event_sequence: %w", err)
	}
	return nil
}
