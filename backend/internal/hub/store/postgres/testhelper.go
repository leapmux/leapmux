package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
)

// testablePGStore extends pgStore with test helper operations.
type testablePGStore struct {
	*pgStore
	borrowed bool
}

func (s *testablePGStore) Close() error {
	if s.borrowed {
		return nil
	}
	return s.pgStore.Close()
}

var _ store.TestableStore = (*testablePGStore)(nil)

// OpenTestable connects to a PostgreSQL database with a fresh random keystore
// and returns a TestableStore. The caller retains ownership; calling Close on
// the returned Store will close the pool.
func OpenTestable(ctx context.Context, cfg config.PostgresConfig) (store.TestableStore, error) {
	ks, err := keystore.NewRandom()
	if err != nil {
		return nil, err
	}
	st, err := Open(ctx, cfg, ks)
	if err != nil {
		return nil, err
	}
	return &testablePGStore{pgStore: st.(*pgStore)}, nil
}

// NewTestableFromPool creates a TestableStore from an existing pool and sql.DB
// with a fresh random keystore.
func NewTestableFromPool(pool *pgxpool.Pool, migrationDB *sql.DB) (store.TestableStore, error) {
	ks, err := keystore.NewRandom()
	if err != nil {
		return nil, err
	}
	st, err := newFromPool(pool, migrationDB, ks)
	if err != nil {
		return nil, err
	}
	return &testablePGStore{pgStore: st}, nil
}

func (s *testablePGStore) TestHelper() store.TestHelper {
	return &pgTestHelper{shared: s.conn.shared}
}

type pgTestHelper struct {
	shared *pgShared
}

func (h *pgTestHelper) exec(ctx context.Context, query string, args ...any) error {
	_, err := h.shared.pool.Exec(ctx, query, args...)
	return err
}

func (h *pgTestHelper) SetDeletedAt(ctx context.Context, entity store.TestEntity, id string, deletedAt time.Time) error {
	return sqlutil.SetDeletedAt(ctx, h.exec, sqlutil.DialectPostgres, entity, id, deletedAt)
}

func (h *pgTestHelper) SetCreatedAt(ctx context.Context, entity store.TestEntity, id string, createdAt time.Time) error {
	return sqlutil.SetCreatedAt(ctx, h.exec, sqlutil.DialectPostgres, entity, id, createdAt)
}

func (h *pgTestHelper) SetLastActiveAt(ctx context.Context, id string, lastActiveAt time.Time) error {
	return sqlutil.SetLastActiveAt(ctx, h.exec, sqlutil.DialectPostgres, id, lastActiveAt)
}

func (h *pgTestHelper) SetDeregisteringAt(ctx context.Context, workerID string, deregisteringAt time.Time) error {
	return sqlutil.SetDeregisteringAt(ctx, h.exec, sqlutil.DialectPostgres, workerID, deregisteringAt)
}

func (h *pgTestHelper) ClearWorkerDeregisteringAt(ctx context.Context, workerID string) error {
	return sqlutil.ClearWorkerDeregisteringAt(ctx, h.exec, sqlutil.DialectPostgres, workerID)
}

// ListAllOwnedTabs reads workspace_tab_owned without an owner predicate. See
// store.TestHelper.ListAllOwnedTabs for why this is test-only. The statement is
// shared with the database/sql dialects; only the driver differs, and tab_type
// scans as int32 because pgx maps INTEGER to Go's int32.
func (h *pgTestHelper) ListAllOwnedTabs(ctx context.Context) ([]store.WorkspaceTabRow, error) {
	rows, err := h.shared.pool.Query(ctx, sqlutil.ListAllOwnedTabsSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.WorkspaceTabRow
	for rows.Next() {
		var r store.WorkspaceTabRow
		var tabType int32
		if err := rows.Scan(&r.UserID, &r.WorkspaceID, &tabType, &r.TabID, &r.WorkerID, &r.TileID, &r.Position, &r.Rendered); err != nil {
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
		if _, err := h.shared.pool.Exec(ctx, sqlutil.TruncateStatement(t)); err != nil {
			return err
		}
	}
	if _, err := h.shared.pool.Exec(ctx, "INSERT INTO revocation_event_sequence (id, last_seq) VALUES (1, 0)"); err != nil {
		return err
	}
	return nil
}

func (h *pgTestHelper) TableHoldsText(ctx context.Context, table, text string) (bool, error) {
	return sqlutil.TableHoldsText(ctx, h.shared.migrationDB, table, text)
}

func (h *pgTestHelper) WithKeystore(ks *keystore.Keystore) store.TestableStore {
	if ks == nil {
		panic("postgres: WithKeystore requires a non-nil keystore")
	}
	return &testablePGStore{
		pgStore: &pgStore{
			conn: newPoolConn(h.shared, h.shared.pool, ks),
		},
		borrowed: true,
	}
}

func (h *pgTestHelper) CopyWorkspaceTitleCiphertext(ctx context.Context, srcWorkspaceID, dstWorkspaceID string) error {
	return sqlutil.CopyWorkspaceTitleCiphertext(ctx, h.shared.migrationDB, h.exec, sqlutil.DialectPostgres, srcWorkspaceID, dstWorkspaceID)
}

func (h *pgTestHelper) SetSettingSecretRaw(ctx context.Context, key string, raw []byte) error {
	return sqlutil.SetSettingSecretRaw(ctx, h.exec, sqlutil.DialectPostgres, key, raw)
}

func (h *pgTestHelper) CopySettingSecretCiphertext(ctx context.Context, srcKey, dstKey string) error {
	return sqlutil.CopySettingSecretCiphertext(ctx, h.shared.migrationDB, h.exec, sqlutil.DialectPostgres, srcKey, dstKey)
}
