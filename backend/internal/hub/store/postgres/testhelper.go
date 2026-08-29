package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
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
func NewTestableFromPool(pool *pgxpool.Pool, migrationDB *sql.DB) (store.TestableStore, error) {
	st, err := newFromPool(pool, migrationDB)
	if err != nil {
		return nil, err
	}
	return &testablePGStore{pgStore: st}, nil
}

func (s *testablePGStore) TestHelper() store.TestHelper {
	return &pgTestHelper{pool: s.conn.shared.pool}
}

type pgTestHelper struct {
	pool *pgxpool.Pool
}

func (h *pgTestHelper) exec(ctx context.Context, query string, args ...any) error {
	_, err := h.pool.Exec(ctx, query, args...)
	return err
}

func (h *pgTestHelper) SetDeletedAt(ctx context.Context, entity store.TestEntity, id string, deletedAt time.Time) error {
	return sqlutil.SetDeletedAt(ctx, h.exec, sqlutil.ParameterStyleDollar, entity, id, deletedAt)
}

func (h *pgTestHelper) SetCreatedAt(ctx context.Context, entity store.TestEntity, id string, createdAt time.Time) error {
	return sqlutil.SetCreatedAt(ctx, h.exec, sqlutil.ParameterStyleDollar, entity, id, createdAt)
}

func (h *pgTestHelper) SetLastActiveAt(ctx context.Context, id string, lastActiveAt time.Time) error {
	return sqlutil.SetLastActiveAt(ctx, h.exec, sqlutil.ParameterStyleDollar, id, lastActiveAt)
}

func (h *pgTestHelper) SetRevocationEventRevokedAt(ctx context.Context, id string, revokedAt time.Time) error {
	return h.setTimestamp(ctx, sqlutil.TimestampColumnRevocationEventRevokedAt, id, revokedAt)
}

func (h *pgTestHelper) setTimestamp(ctx context.Context, column sqlutil.TimestampColumn, id string, at any) error {
	return sqlutil.SetTimestampColumn(ctx, h.exec, sqlutil.ParameterStyleDollar, column, id, at)
}

// ListAllOwnedTabs reads workspace_tab_owned without an owner predicate. See
// store.TestHelper.ListAllOwnedTabs for why this is test-only. The statement is
// shared with the database/sql dialects; only the driver differs, and tab_type
// scans as int32 because pgx maps INTEGER to Go's int32.
func (h *pgTestHelper) ListAllOwnedTabs(ctx context.Context) ([]store.WorkspaceTabRow, error) {
	rows, err := h.pool.Query(ctx, sqlutil.ListAllOwnedTabsSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.WorkspaceTabRow
	for rows.Next() {
		var r store.WorkspaceTabRow
		var tabType int32
		if err := rows.Scan(&r.UserID, &r.WorkspaceID, &tabType, &r.TabID, &r.WorkerID, &r.TileID, &r.Position); err != nil {
			return nil, err
		}
		r.TabType = leapmuxv1.TabType(tabType)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (h *pgTestHelper) TruncateAll(ctx context.Context) error {
	// Use DELETE instead of TRUNCATE for compatibility with YugabyteDB,
	// where TRUNCATE is a slow distributed operation. For small test
	// datasets DELETE is fast on all PostgreSQL-compatible backends.
	for _, t := range sqlutil.SQLTruncateTableOrder {
		if _, err := h.pool.Exec(ctx, sqlutil.TruncateStatement(t)); err != nil {
			return err
		}
	}
	if _, err := h.pool.Exec(ctx, "INSERT INTO revocation_event_sequence (id, last_seq) VALUES (1, 0)"); err != nil {
		return err
	}
	return nil
}
