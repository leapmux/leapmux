package mysql

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
)

type keyMaintenanceStore struct {
	conn *mysqlConn
}

func (s *keyMaintenanceStore) CountKeyVersionRefs(ctx context.Context, version uint32) ([]store.KeyVersionRefCount, error) {
	return sqlutil.CountKeyVersionRefs(ctx, s.conn.shared.db, sqlutil.DialectMySQL, version)
}

func (s *keyMaintenanceStore) ReencryptAll(ctx context.Context) (store.ReencryptResult, error) {
	runTx := func(ctx context.Context, fn func(*sql.Tx) error) error {
		return store.RetryOnConflict(ctx, isRetryableConflict, func() error {
			tx, err := s.conn.shared.db.BeginTx(ctx, nil)
			if err != nil {
				return fmt.Errorf("begin reencrypt transaction: %w", err)
			}
			defer func() { _ = tx.Rollback() }()
			if err := fn(tx); err != nil {
				return err
			}
			return tx.Commit()
		})
	}
	return sqlutil.ReencryptAll(ctx, s.conn.shared.db, runTx, sqlutil.DialectMySQL, s.conn.ks)
}
