package mysql

import (
	"context"
	"database/sql"
	"errors"

	mysqldriver "github.com/go-sql-driver/mysql"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
)

// A conflict abort is a NORMAL answer here too, and the client is the part
// that must handle it. This is the MySQL-family half of the same rule the
// postgres dialect states in its own conflict_retry.go -- LeapMux runs this
// dialect against MySQL and TiDB, and both resolve a write-write conflict by
// killing one transaction and expecting the application to run it again.
//
// Without a retry the store returned that abort to the caller as a failure,
// so two people who changed the same row at the same time got an error one of
// them did not earn.
const (
	// mysqlErrLockDeadlock (1213) is InnoDB's deadlock victim, and TiDB's
	// pessimistic mode reports the same number.
	mysqlErrLockDeadlock = 1213
	// mysqlErrLockWaitTimeout (1205) is a lock the row's holder never
	// released inside innodb_lock_wait_timeout. It is retryable for the same
	// reason: the statement did nothing, and the contention may have cleared.
	mysqlErrLockWaitTimeout = 1205
	// tidbErrWriteConflict (9007) is TiDB's optimistic-transaction conflict.
	// Its own client guidance is to retry the transaction.
	tidbErrWriteConflict = 9007
)

// isRetryableConflict reports whether the backend aborted this work for a
// conflict it expects the client to retry.
//
// A duplicate-key violation is deliberately absent: it is the caller's data
// and it will fail the same way for ever. A connection fault is absent too --
// the statement may have committed before the connection died, so retrying
// one could apply it twice.
func isRetryableConflict(err error) bool {
	var mysqlErr *mysqldriver.MySQLError
	if !errors.As(err, &mysqlErr) {
		return false
	}
	switch mysqlErr.Number {
	case mysqlErrLockDeadlock, mysqlErrLockWaitTimeout, tidbErrWriteConflict:
		return true
	}
	return false
}

// conflictRetryDBTX re-runs a statement the backend aborted for a retryable
// conflict. It wraps the POOL, never a transaction: a statement outside a
// transaction is its own implicit transaction, so an abort leaves nothing
// behind, while inside one the whole attempt is what must repeat (which
// withTransaction does).
//
// QueryRowContext IS NOT WRAPPED, and it cannot be. database/sql returns the
// concrete *sql.Row, whose only method reads unexported fields, so there is no
// way to return a Row that retries -- and no interface to substitute, because
// sqlc's generated DBTX specifies the concrete type. The postgres dialect
// wraps its equivalent because pgx returns the pgx.Row INTERFACE.
//
// What that costs is limited. In this dialect a single-row read is a SELECT:
// MySQL has no UPDATE ... RETURNING, so every mutation sqlc generates lands on
// ExecContext, which this does wrap. A SELECT can still take a lock-wait
// timeout inside a transaction, and there the transaction-level retry covers
// it. The gap is a bare single-row SELECT outside a transaction that loses a
// lock race, which needs a concurrent writer holding a row this reader wants
// and no surrounding transaction.
type conflictRetryDBTX struct {
	inner gendb.DBTX
}

var _ gendb.DBTX = conflictRetryDBTX{}

func (d conflictRetryDBTX) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	var res sql.Result
	err := store.RetryOnConflict(ctx, isRetryableConflict, func() error {
		var execErr error
		res, execErr = d.inner.ExecContext(ctx, query, args...)
		return execErr
	})
	return res, err
}

func (d conflictRetryDBTX) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	var rows *sql.Rows
	err := store.RetryOnConflict(ctx, isRetryableConflict, func() error {
		var queryErr error
		rows, queryErr = d.inner.QueryContext(ctx, query, args...)
		return queryErr
	})
	return rows, err
}

// QueryRowContext forwards untouched. See the type doc for why it cannot
// retry and what that leaves uncovered.
func (d conflictRetryDBTX) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return d.inner.QueryRowContext(ctx, query, args...)
}

// PrepareContext forwards untouched. A prepare takes no row locks, so it has
// no conflict to retry; the statement it returns runs through the caller's own
// path.
func (d conflictRetryDBTX) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return d.inner.PrepareContext(ctx, query)
}
