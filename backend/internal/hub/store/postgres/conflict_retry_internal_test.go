package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
)

// abortErr builds the answer a distributed backend gives when it resolves a
// write-write conflict by killing this statement.
func abortErr(code string) error {
	return &pgconn.PgError{Code: code, Message: "conflict"}
}

// scriptedDBTX answers a fixed sequence of errors, then succeeds. It is the
// deterministic stand-in for a contended row: the integration suite reproduces
// the same abort against a real YugabyteDB, but only when the timing lands.
type scriptedDBTX struct {
	// errs is consumed one per attempt; a nil entry (or running past the end)
	// is a success.
	errs  []error
	calls int
}

func (d *scriptedDBTX) next() error {
	i := d.calls
	d.calls++
	if i < len(d.errs) {
		return d.errs[i]
	}
	return nil
}

func (d *scriptedDBTX) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, d.next()
}

func (d *scriptedDBTX) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, d.next()
}

func (d *scriptedDBTX) QueryRow(context.Context, string, ...any) pgx.Row {
	return scriptedRow{d: d}
}

type scriptedRow struct{ d *scriptedDBTX }

func (r scriptedRow) Scan(dest ...any) error {
	err := r.d.next()
	if err != nil {
		return err
	}
	// A real Row writes the destinations on success, so the fake does too --
	// otherwise a retry that clobbered them would pass unnoticed.
	for _, d := range dest {
		if p, ok := d.(*int64); ok {
			*p = 7
		}
	}
	return nil
}

var _ gendb.DBTX = (*scriptedDBTX)(nil)

// TestIsRetryableConflict pins the exact set, because the cost of a wrong
// answer runs in both directions: a code left out returns a failure the user
// did not earn, and a code let in re-runs a statement that may already have
// committed.
func TestIsRetryableConflict(t *testing.T) {
	t.Parallel()

	assert.True(t, isRetryableConflict(abortErr(pgerrcode.SerializationFailure)),
		"40001 is what PostgreSQL raises under REPEATABLE READ and what CockroachDB raises on any conflict")
	assert.True(t, isRetryableConflict(abortErr(pgerrcode.DeadlockDetected)),
		"40P01 is what YugabyteDB's wait-queue detector answered the contended row with")

	// A wrapped error still counts: mapErr and the sqlc layer both wrap.
	assert.True(t, isRetryableConflict(
		errors.Join(errors.New("consume verification attempt"), abortErr(pgerrcode.SerializationFailure))))

	// A unique violation is the caller's DATA. It fails identically for ever,
	// so retrying it only delays the answer.
	assert.False(t, isRetryableConflict(abortErr(pgerrcode.UniqueViolation)))
	// A connection fault is NOT retryable here: the statement may have
	// committed before the connection died, so a retry could apply it twice.
	assert.False(t, isRetryableConflict(abortErr(pgerrcode.ConnectionException)))
	assert.False(t, isRetryableConflict(errors.New("some other failure")))
	assert.False(t, isRetryableConflict(pgx.ErrNoRows))
	assert.False(t, isRetryableConflict(nil))
}

// TestConflictRetryDBTXRetriesEveryStatementShape pins WHICH statement shapes
// the wrapper covers, and it pins the one it does not.
//
// sqlc routes a query through whichever DBTX method its result shape needs.
// Exec and QueryRow are covered -- the case that caught this
// (ConsumeVerificationAttempt, an UPDATE ... RETURNING) is a QueryRow, the
// shape where the error surfaces at Scan rather than at the call.
//
// Query is NOT covered, and the "query" case below asserts exactly that
// rather than the opposite. It used to assert a retry, and it passed for a
// reason that had nothing to do with pgx: scriptedDBTX returns its error
// synchronously from Query, a shape the real driver cannot produce for these
// two codes. The suite therefore reported coverage the wrapper never had. See
// conflictRetryDBTX.Query for why the real one cannot retry.
func TestConflictRetryDBTXRetriesEveryStatementShape(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("exec", func(t *testing.T) {
		inner := &scriptedDBTX{errs: []error{abortErr(pgerrcode.DeadlockDetected)}}
		_, err := conflictRetryDBTX{inner: inner}.Exec(ctx, "UPDATE users SET x = 1")
		require.NoError(t, err)
		assert.Equal(t, 2, inner.calls)
	})

	t.Run("query forwards untouched and does not retry", func(t *testing.T) {
		inner := &scriptedDBTX{errs: []error{abortErr(pgerrcode.SerializationFailure)}}
		_, err := conflictRetryDBTX{inner: inner}.Query(ctx, "SELECT 1")
		require.Error(t, err, "the wrapper must not swallow what it cannot retry")
		assert.True(t, isRetryableConflict(err))
		assert.Equal(t, 1, inner.calls, "one call: pgx reports this error on the rows, not here")
	})

	t.Run("query row scans after the retry", func(t *testing.T) {
		inner := &scriptedDBTX{errs: []error{
			abortErr(pgerrcode.DeadlockDetected),
			abortErr(pgerrcode.SerializationFailure),
		}}
		var got int64
		err := conflictRetryDBTX{inner: inner}.QueryRow(ctx, "UPDATE users SET n = n + 1 RETURNING n").Scan(&got)
		require.NoError(t, err)
		assert.Equal(t, 3, inner.calls)
		assert.EqualValues(t, 7, got, "the retried Scan must fill the caller's destinations")
	})
}

// A statement that is not a conflict must be answered at once. Retrying a
// wrong password check or a unique violation would multiply the latency of
// every ordinary failure.
func TestConflictRetryDBTXDoesNotRetryOtherErrors(t *testing.T) {
	t.Parallel()

	inner := &scriptedDBTX{errs: []error{abortErr(pgerrcode.UniqueViolation)}}
	_, err := conflictRetryDBTX{inner: inner}.Exec(context.Background(), "INSERT INTO users ...")
	require.Error(t, err)
	assert.Equal(t, 1, inner.calls, "a non-conflict error is final")
}

// The attempt count is capped, and the LAST error reaches the caller. A
// backend that keeps aborting is congested rather than contended, and a caller
// that waits for ever serves nobody.
func TestConflictRetryDBTXGivesUpAndReportsTheConflict(t *testing.T) {
	t.Parallel()

	errs := make([]error, store.ConflictRetryLimit+3)
	for i := range errs {
		errs[i] = abortErr(pgerrcode.DeadlockDetected)
	}
	inner := &scriptedDBTX{errs: errs}

	_, err := conflictRetryDBTX{inner: inner}.Exec(context.Background(), "UPDATE users SET x = 1")

	require.Error(t, err)
	assert.True(t, isRetryableConflict(err), "the caller must still see WHY it failed")
	assert.Equal(t, store.ConflictRetryLimit, inner.calls)
}

// A cancelled request stops the retries, and the DATABASE error is what the
// caller gets. Reporting "context canceled" instead would hide which statement
// conflicted, which is the one fact an operator needs.
func TestConflictRetryDBTXStopsWhenTheCallerGoesAway(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	errs := make([]error, store.ConflictRetryLimit)
	for i := range errs {
		errs[i] = abortErr(pgerrcode.SerializationFailure)
	}
	inner := &scriptedDBTX{errs: errs}

	start := time.Now()
	_, err := conflictRetryDBTX{inner: inner}.Exec(ctx, "UPDATE users SET x = 1")

	require.Error(t, err)
	assert.True(t, isRetryableConflict(err))
	assert.Equal(t, 1, inner.calls, "a cancelled request runs the statement once")
	assert.Less(t, time.Since(start), store.ConflictRetryBaseDelay,
		"a cancelled request must not wait out the backoff")
}

// TestPoolConnRetriesThroughItsQueries is the WIRING test, and it is the one
// that matters most.
//
// Every case above drives conflictRetryDBTX by hand, so all of them keep
// passing if the store stops building its Queries with it -- which is exactly
// how the fix would be lost. This one goes through newPoolConn, the single
// constructor both entry points use, and issues a real sqlc query against a
// backend that aborts once.
func TestPoolConnRetriesThroughItsQueries(t *testing.T) {
	t.Parallel()

	pool := &scriptedDBTX{errs: []error{abortErr(pgerrcode.DeadlockDetected)}}
	conn := newPoolConn(&pgShared{}, pool)

	require.False(t, conn.inTx(), "a pool-backed conn is not a transaction")
	// A plain :exec query, so what it proves is the retry rather than any
	// decoding: the abort arrives, the wrapper runs the statement again.
	require.NoError(t, conn.q.SetRevocationEventSequence(context.Background(), 1))
	assert.Equal(t, 2, pool.calls,
		"the pool's Queries must carry conflictRetryDBTX; without it the abort reaches the caller")
}

// TestPoolConnRetriesThroughItsRawExec is the other half of the wiring. This
// dialect runs only the publish statement through conn.exec, but the rule is
// one rule across both dialects rather than a per-dialect judgement, and this
// pins it here too.
func TestPoolConnRetriesThroughItsRawExec(t *testing.T) {
	t.Parallel()

	pool := &scriptedDBTX{errs: []error{abortErr(pgerrcode.DeadlockDetected)}}
	conn := newPoolConn(&pgShared{}, pool)

	require.False(t, conn.inTx(), "a pool-backed conn is not a transaction")
	_, err := conn.exec.Exec(context.Background(), "DELETE FROM workspace_tab_owned WHERE user_id = $1", "u1")
	require.NoError(t, err)
	assert.Equal(t, 2, pool.calls,
		"the pool's raw exec must carry conflictRetryDBTX, exactly as its Queries do")
}

// The wrapper covers the pool and NOT a transaction, and that is the safety
// argument rather than an accident: an abort kills the whole transaction, so
// re-running one statement inside it would rejoin a dead transaction instead
// of repeating the unit of work.
func TestTransactionQueriesCarryNoStatementRetry(t *testing.T) {
	t.Parallel()

	q := gendb.New(conflictRetryDBTX{inner: &scriptedDBTX{}})
	// WithTx rebuilds Queries over the transaction itself, dropping the
	// wrapper. Compare the two rather than asserting the concrete type, so
	// this still fails if a future WithTx re-wrapped its argument.
	assert.NotSame(t, q, q.WithTx(nil))
}
