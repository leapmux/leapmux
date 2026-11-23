package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
)

type keyMaintenanceStore struct {
	conn *pgConn
}

type pgxRowsAdapter struct {
	pgx.Rows
}

func (r pgxRowsAdapter) Close() error {
	r.Rows.Close()
	return nil
}

type pgxQueryExec struct {
	db gendb.DBTX
}

func (e pgxQueryExec) QueryRows(ctx context.Context, query string, args ...any) (sqlutil.QueryRows, error) {
	rows, err := e.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return pgxRowsAdapter{Rows: rows}, nil
}

func (e pgxQueryExec) ScanRow(ctx context.Context, query string, args []any, dest ...any) error {
	err := e.db.QueryRow(ctx, query, args...).Scan(dest...)
	if errors.Is(err, pgx.ErrNoRows) {
		return sql.ErrNoRows
	}
	return err
}

func (e pgxQueryExec) Exec(ctx context.Context, query string, args ...any) error {
	_, err := e.db.Exec(ctx, query, args...)
	return err
}

func (s *keyMaintenanceStore) CountKeyVersionRefs(ctx context.Context, version uint32) ([]store.KeyVersionRefCount, error) {
	if pgxTx, ok := s.conn.exec.(pgx.Tx); ok {
		return sqlutil.CountKeyVersionRefs(ctx, pgxQueryExec{db: pgxTx}, sqlutil.DialectPostgres, version, nil)
	}
	return sqlutil.CountKeyVersionRefs(ctx, pgxQueryExec{db: s.conn.shared.pool}, sqlutil.DialectPostgres, version, isRetryableConflict)
}

func (s *keyMaintenanceStore) VerifyKeyring(ctx context.Context) error {
	if pgxTx, ok := s.conn.exec.(pgx.Tx); ok {
		return sqlutil.VerifyKeyring(ctx, pgxQueryExec{db: pgxTx}, sqlutil.DialectPostgres, s.conn.ks, nil)
	}
	return sqlutil.VerifyKeyring(ctx, pgxQueryExec{db: s.conn.shared.pool}, sqlutil.DialectPostgres, s.conn.ks, isRetryableConflict)
}

func (s *keyMaintenanceStore) ReencryptAll(ctx context.Context) (store.ReencryptResult, error) {
	if pgxTx, ok := s.conn.exec.(pgx.Tx); ok {
		qe := pgxQueryExec{db: pgxTx}
		return sqlutil.ReencryptAll(ctx, qe, sqlutil.InTxRunner(qe), sqlutil.DialectPostgres, s.conn.ks, nil)
	}
	runTx := func(ctx context.Context, fn func(sqlutil.QueryExec) error) error {
		return store.RetryOnConflict(ctx, isRetryableConflict, func() error {
			tx, err := s.conn.shared.pool.Begin(ctx)
			if err != nil {
				return fmt.Errorf("begin reencrypt transaction: %w", err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if err := fn(pgxQueryExec{db: tx}); err != nil {
				return err
			}
			return tx.Commit(ctx)
		})
	}
	return sqlutil.ReencryptAll(ctx, pgxQueryExec{db: s.conn.shared.pool}, runTx, sqlutil.DialectPostgres, s.conn.ks, isRetryableConflict)
}
