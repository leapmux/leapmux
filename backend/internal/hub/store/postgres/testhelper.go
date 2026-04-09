package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
)

// testablePGStore extends pgStore with test helper operations.
type testablePGStore struct {
	*pgStore
}

var _ store.TestableStore = (*testablePGStore)(nil)

// OpenTestable connects to a PostgreSQL database and returns a TestableStore.
// The caller retains ownership; calling Close on the returned Store will close the pool.
func OpenTestable(ctx context.Context, cfg config.PostgresConfig) (store.TestableStore, error) {
	st, err := Open(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &testablePGStore{pgStore: st.(*pgStore)}, nil
}

// NewTestableFromPool creates a TestableStore from an existing pool and sql.DB.
func NewTestableFromPool(pool *pgxpool.Pool, sqlDB *sql.DB) (store.TestableStore, error) {
	st, err := newFromPool(pool, sqlDB)
	if err != nil {
		return nil, err
	}
	return &testablePGStore{pgStore: st}, nil
}

func (s *testablePGStore) TestHelper() store.TestHelper {
	return &pgTestHelper{pool: s.pool}
}

type pgTestHelper struct {
	pool *pgxpool.Pool
}

func (h *pgTestHelper) SetDeletedAt(ctx context.Context, entity store.TestEntity, id string, deletedAt time.Time) error {
	if err := store.ValidateEntity(entity); err != nil {
		return err
	}
	query := fmt.Sprintf("UPDATE %s SET deleted_at = $1 WHERE id = $2", entity)
	_, err := h.pool.Exec(ctx, query, deletedAt, id)
	return err
}

func (h *pgTestHelper) SetCreatedAt(ctx context.Context, entity store.TestEntity, id string, createdAt time.Time) error {
	if err := store.ValidateEntity(entity); err != nil {
		return err
	}
	query := fmt.Sprintf("UPDATE %s SET created_at = $1 WHERE id = $2", entity)
	_, err := h.pool.Exec(ctx, query, createdAt, id)
	return err
}

func (h *pgTestHelper) TruncateAll(ctx context.Context) error {
	// Use DELETE instead of TRUNCATE for compatibility with YugabyteDB,
	// where TRUNCATE is a slow distributed operation. For small test
	// datasets DELETE is fast on all PostgreSQL-compatible backends.
	for _, t := range sqlutil.SQLTruncateTableOrder {
		if _, err := h.pool.Exec(ctx, "DELETE FROM "+t); err != nil {
			return err
		}
	}
	return nil
}
