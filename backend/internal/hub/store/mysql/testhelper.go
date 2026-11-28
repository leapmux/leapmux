package mysql

import (
	"context"
	"fmt"
	"time"

	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
)

// testableMySQLStore extends mysqlStore with test helper operations.
type testableMySQLStore struct {
	*mysqlStore
	borrowed bool
}

func (s *testableMySQLStore) Close() error {
	if s.borrowed {
		return nil
	}
	return s.mysqlStore.Close()
}

var _ store.TestableStore = (*testableMySQLStore)(nil)

// OpenTestable opens a MySQL store with a fresh random keystore that also
// implements store.TestableStore. This is intended for use in tests only.
func OpenTestable(cfg config.MySQLConfig) (store.TestableStore, error) {
	ks, err := keystore.NewRandom()
	if err != nil {
		return nil, err
	}
	st, err := Open(cfg, ks)
	if err != nil {
		return nil, err
	}
	return &testableMySQLStore{mysqlStore: st.(*mysqlStore)}, nil
}

func (s *testableMySQLStore) TestHelper() store.TestHelper {
	return &mysqlTestHelper{shared: s.conn.shared}
}

type mysqlTestHelper struct {
	shared *mysqlShared
}

func (h *mysqlTestHelper) exec(ctx context.Context, query string, args ...any) error {
	_, err := h.shared.db.ExecContext(ctx, query, args...)
	return err
}

func (h *mysqlTestHelper) SetDeletedAt(ctx context.Context, entity store.TestEntity, id string, deletedAt time.Time) error {
	return sqlutil.SetDeletedAt(ctx, h.exec, sqlutil.DialectMySQL, entity, id, deletedAt)
}

func (h *mysqlTestHelper) SetCreatedAt(ctx context.Context, entity store.TestEntity, id string, createdAt time.Time) error {
	return sqlutil.SetCreatedAt(ctx, h.exec, sqlutil.DialectMySQL, entity, id, createdAt)
}

func (h *mysqlTestHelper) SetLastActiveAt(ctx context.Context, id string, lastActiveAt time.Time) error {
	return sqlutil.SetLastActiveAt(ctx, h.exec, sqlutil.DialectMySQL, id, lastActiveAt)
}

// ListAllOwnedTabs reads workspace_tab_owned without an owner predicate. See
// store.TestHelper.ListAllOwnedTabs for why this is test-only.
func (h *mysqlTestHelper) ListAllOwnedTabs(ctx context.Context) ([]store.WorkspaceTabRow, error) {
	return sqlutil.ListAllOwnedTabs(ctx, h.shared.db)
}

func (h *mysqlTestHelper) TruncateAll(ctx context.Context) error {
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

func (h *mysqlTestHelper) TableHoldsText(ctx context.Context, table, text string) (bool, error) {
	return sqlutil.TableHoldsText(ctx, h.shared.db, table, text)
}

func (h *mysqlTestHelper) WithKeystore(ks *keystore.Keystore) store.TestableStore {
	if ks == nil {
		panic("mysql: WithKeystore requires a non-nil keystore")
	}
	return &testableMySQLStore{
		mysqlStore: &mysqlStore{
			conn: newPoolConn(h.shared, h.shared.db, ks),
		},
		borrowed: true,
	}
}

func (h *mysqlTestHelper) CopyWorkspaceTitleCiphertext(ctx context.Context, srcWorkspaceID, dstWorkspaceID string) error {
	return sqlutil.CopyWorkspaceTitleCiphertext(ctx, h.shared.db, h.exec, sqlutil.DialectMySQL, srcWorkspaceID, dstWorkspaceID)
}

func (h *mysqlTestHelper) SetSettingSecretRaw(ctx context.Context, key string, raw []byte) error {
	return sqlutil.SetSettingSecretRaw(ctx, h.exec, sqlutil.DialectMySQL, key, raw)
}

func (h *mysqlTestHelper) CopySettingSecretCiphertext(ctx context.Context, srcKey, dstKey string) error {
	return sqlutil.CopySettingSecretCiphertext(ctx, h.shared.db, h.exec, sqlutil.DialectMySQL, srcKey, dstKey)
}
