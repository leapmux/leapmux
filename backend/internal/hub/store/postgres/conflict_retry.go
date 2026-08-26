package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
)

// A conflict abort is a NORMAL answer from a distributed SQL backend, and the
// client is the part that must handle it.
//
// LeapMux runs this dialect against PostgreSQL, YugabyteDB and CockroachDB.
// The two distributed backends resolve a write-write conflict by aborting one
// of the transactions with a retryable SQLSTATE, and both document that the
// application retries it -- there is no server-side wait that makes the error
// go away. PostgreSQL itself raises the same two codes: 40001 under REPEATABLE
// READ or SERIALIZABLE, and 40P01 whenever two transactions take row locks in
// opposite orders.
//
// Without a retry the store returned that abort to the caller as a failure, so
// two people who changed the same row at the same time got an error one of
// them did not earn. The storetest case that caught it charges 24 concurrent
// verification attempts against ONE users row -- exactly the shape a rate
// limit produces -- and YugabyteDB answered one of them "deadlock detected".
//
// The retry sits at the statement seam rather than at each call site, so no
// store method can forget it. It applies ONLY outside an explicit transaction:
// see conflictRetryDBTX. The attempt count and the backoff are
// store.RetryOnConflict's; only isRetryableConflict below is this driver's.

// isRetryableConflict reports whether the backend aborted this statement for a
// conflict it expects the client to retry.
//
// The two codes are the whole set, deliberately. A connection fault is NOT in
// it: the statement may have committed before the connection died, so retrying
// one could apply it twice. A unique violation is not in it either -- it is
// the caller's data, and it will fail the same way for ever.
func isRetryableConflict(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == pgerrcode.SerializationFailure || pgErr.Code == pgerrcode.DeadlockDetected
}

// conflictRetryDBTX re-runs a statement the backend aborted for a retryable
// conflict. It wraps the POOL, never a transaction, and that distinction is
// the whole safety argument.
//
// A statement outside a transaction is its own implicit transaction: an abort
// leaves nothing behind, so running it again is exactly what the backend asks
// for. A statement inside an explicit transaction is not -- the abort killed
// the whole transaction, every later statement in it answers
// "current transaction is aborted", and re-running one statement would rejoin
// a dead transaction rather than repeat the unit of work. So pgConn.q carries
// this wrapper and q.WithTx(tx) does not, which makes the wrong retry
// unreachable rather than merely avoided.
//
// Retrying a whole EXPLICIT transaction is the other half of the contract and
// is not implemented here: it would re-run a caller's callback, and a callback
// that appends to a slice or sends on a channel is not safe to run twice. See
// the note in RunInTransaction.
type conflictRetryDBTX struct {
	inner gendb.DBTX
}

var _ gendb.DBTX = conflictRetryDBTX{}

func (d conflictRetryDBTX) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	var tag pgconn.CommandTag
	err := store.RetryOnConflict(ctx, isRetryableConflict, func() error {
		var execErr error
		tag, execErr = d.inner.Exec(ctx, sql, args...)
		return execErr
	})
	return tag, err
}

// Query FORWARDS UNTOUCHED, and it cannot retry. pgx reports a statement the
// server aborted on the ROWS, not from this call: Conn.Query ends
// `return rows, rows.err`, where rows.err carries only the local failures
// (a failed describe, an argument-count mismatch). The server's
// SerializationFailure or DeadlockDetected lands on the result reader and
// reaches the caller through rows.Err() after Next() returns false. A wrapper
// here therefore always sees a nil error, and a retry that ran anyway would
// have to be decided at a point where nothing is known yet.
//
// Retrying LATER is not available either: by the time the error is knowable
// the caller already holds the Rows and may have consumed part of it, so
// re-running the statement would replay rows it already scanned. Covering
// this would mean buffering every result set in memory and replaying it,
// which is a cost every read in the hub would pay for a case only the
// distributed backends raise.
//
// What that leaves uncovered is a MULTI-ROW read outside a transaction that
// loses a conflict. Inside a transaction the whole attempt is what repeats,
// and withTransaction does that. It is the mirror of the gap the mysql
// dialect documents on QueryRowContext, and the two dialects cover opposite
// halves for the same reason: each retries exactly the shapes whose driver
// reports the error eagerly.
func (d conflictRetryDBTX) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return d.inner.Query(ctx, sql, args...)
}

// QueryRow defers the retry to Scan, because that is where pgx reports the
// error: QueryRow itself returns a Row and never an error, so a wrapper that
// tried to decide here would have nothing to read.
//
// A retried Scan writes the SAME destinations again. That is safe for the one
// error class this retries: the backend aborted the statement, so it returned
// no row and pgx wrote nothing into them.
func (d conflictRetryDBTX) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return conflictRetryRow{ctx: ctx, query: func() pgx.Row { return d.inner.QueryRow(ctx, sql, args...) }}
}

type conflictRetryRow struct {
	ctx   context.Context
	query func() pgx.Row
}

var _ pgx.Row = conflictRetryRow{}

func (r conflictRetryRow) Scan(dest ...any) error {
	return store.RetryOnConflict(r.ctx, isRetryableConflict, func() error { return r.query().Scan(dest...) })
}
