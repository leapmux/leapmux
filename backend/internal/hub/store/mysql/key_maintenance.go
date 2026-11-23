package mysql

import (
	"context"
	"database/sql"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
)

type keyMaintenanceStore struct {
	conn *mysqlConn
}

func (s *keyMaintenanceStore) CountKeyVersionRefs(ctx context.Context, version uint32) ([]store.KeyVersionRefCount, error) {
	if tx, ok := s.conn.exec.(*sql.Tx); ok {
		return sqlutil.CountKeyVersionRefs(ctx, sqlutil.NewSQLQueryExec(tx), sqlutil.DialectMySQL, version, nil)
	}
	return sqlutil.CountKeyVersionRefs(ctx, sqlutil.NewSQLQueryExec(s.conn.shared.db), sqlutil.DialectMySQL, version, isRetryableConflict)
}

func (s *keyMaintenanceStore) VerifyKeyring(ctx context.Context) error {
	if tx, ok := s.conn.exec.(*sql.Tx); ok {
		return sqlutil.VerifyKeyring(ctx, sqlutil.NewSQLQueryExec(tx), sqlutil.DialectMySQL, s.conn.ks, nil)
	}
	return sqlutil.VerifyKeyring(ctx, sqlutil.NewSQLQueryExec(s.conn.shared.db), sqlutil.DialectMySQL, s.conn.ks, isRetryableConflict)
}

func (s *keyMaintenanceStore) ReencryptAll(ctx context.Context) (store.ReencryptResult, error) {
	if tx, ok := s.conn.exec.(*sql.Tx); ok {
		qe := sqlutil.NewSQLQueryExec(tx)
		return sqlutil.ReencryptAll(ctx, qe, sqlutil.InTxRunner(qe), sqlutil.DialectMySQL, s.conn.ks, nil)
	}
	qe := sqlutil.NewSQLQueryExec(s.conn.shared.db)
	return sqlutil.ReencryptAll(ctx, qe, sqlutil.NewSQLTxRunner(s.conn.shared.db, isRetryableConflict), sqlutil.DialectMySQL, s.conn.ks, isRetryableConflict)
}
