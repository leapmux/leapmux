package mysql

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
)

// abortErr builds the answer InnoDB or TiDB gives when it resolves a
// write-write conflict by killing this work.
func abortErr(number uint16) error {
	return &mysqldriver.MySQLError{Number: number, Message: "conflict"}
}

// scriptedDBTX answers a fixed sequence of errors, then succeeds.
type scriptedDBTX struct {
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

func (d *scriptedDBTX) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, d.next()
}

func (d *scriptedDBTX) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, d.next()
}

func (d *scriptedDBTX) QueryRowContext(context.Context, string, ...any) *sql.Row {
	d.calls++
	return nil
}

func (d *scriptedDBTX) PrepareContext(context.Context, string) (*sql.Stmt, error) {
	d.calls++
	return nil, nil
}

var _ gendb.DBTX = (*scriptedDBTX)(nil)

// TestIsRetryableConflict pins the exact set. A code left out returns a
// failure the user did not earn; a code let in re-runs work that may already
// have committed.
func TestIsRetryableConflict(t *testing.T) {
	t.Parallel()

	assert.True(t, isRetryableConflict(abortErr(mysqlErrLockDeadlock)),
		"1213 is InnoDB's deadlock victim, and TiDB's pessimistic mode reports it too")
	assert.True(t, isRetryableConflict(abortErr(mysqlErrLockWaitTimeout)),
		"1205 means the statement did nothing and the contention may have cleared")
	assert.True(t, isRetryableConflict(abortErr(tidbErrWriteConflict)),
		"9007 is TiDB's optimistic write conflict, whose own guidance is to retry")

	// Wrapping must not hide it: mapErr and the callers both wrap.
	assert.True(t, isRetryableConflict(
		errors.Join(errors.New("consume verification attempt"), abortErr(mysqlErrLockDeadlock))))

	// A duplicate key is the caller's DATA and fails identically for ever.
	assert.False(t, isRetryableConflict(abortErr(mysqlErrDupEntry)))
	assert.False(t, isRetryableConflict(errors.New("some other failure")))
	assert.False(t, isRetryableConflict(sql.ErrNoRows))
	assert.False(t, isRetryableConflict(nil))
}

func TestConflictRetryDBTXRetriesTheWrappedShapes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("exec", func(t *testing.T) {
		inner := &scriptedDBTX{errs: []error{abortErr(mysqlErrLockDeadlock)}}
		_, err := conflictRetryDBTX{inner: inner}.ExecContext(ctx, "UPDATE users SET x = 1")
		require.NoError(t, err)
		assert.Equal(t, 2, inner.calls)
	})

	t.Run("query", func(t *testing.T) {
		inner := &scriptedDBTX{errs: []error{abortErr(tidbErrWriteConflict)}}
		_, err := conflictRetryDBTX{inner: inner}.QueryContext(ctx, "SELECT 1")
		require.NoError(t, err)
		assert.Equal(t, 2, inner.calls)
	})

	t.Run("a non-conflict error is final", func(t *testing.T) {
		inner := &scriptedDBTX{errs: []error{abortErr(mysqlErrDupEntry)}}
		_, err := conflictRetryDBTX{inner: inner}.ExecContext(ctx, "INSERT INTO users ...")
		require.Error(t, err)
		assert.Equal(t, 1, inner.calls)
	})

	t.Run("the attempt count is bounded and the conflict still reaches the caller", func(t *testing.T) {
		errs := make([]error, store.ConflictRetryLimit+3)
		for i := range errs {
			errs[i] = abortErr(mysqlErrLockDeadlock)
		}
		inner := &scriptedDBTX{errs: errs}
		_, err := conflictRetryDBTX{inner: inner}.ExecContext(ctx, "UPDATE users SET x = 1")
		require.Error(t, err)
		assert.True(t, isRetryableConflict(err))
		assert.Equal(t, store.ConflictRetryLimit, inner.calls)
	})
}

// A cancelled request stops the retries at once, and the DATABASE error is
// what the caller gets: "context canceled" would hide which statement
// conflicted.
func TestConflictRetryDBTXStopsWhenTheCallerGoesAway(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	errs := make([]error, store.ConflictRetryLimit)
	for i := range errs {
		errs[i] = abortErr(mysqlErrLockDeadlock)
	}
	inner := &scriptedDBTX{errs: errs}

	start := time.Now()
	_, err := conflictRetryDBTX{inner: inner}.ExecContext(ctx, "UPDATE users SET x = 1")

	require.Error(t, err)
	assert.True(t, isRetryableConflict(err))
	assert.Equal(t, 1, inner.calls)
	assert.Less(t, time.Since(start), store.ConflictRetryBaseDelay)
}

// QueryRowContext CANNOT retry -- database/sql returns the concrete *sql.Row,
// which has no substitutable interface. This pins the limit so a reader does
// not assume coverage the wrapper does not give, and so the day sqlc or
// database/sql makes it wrappable, this case fails and points at the
// opportunity.
func TestQueryRowContextIsForwardedUnretried(t *testing.T) {
	t.Parallel()

	inner := &scriptedDBTX{errs: []error{abortErr(mysqlErrLockDeadlock)}}
	conflictRetryDBTX{inner: inner}.QueryRowContext(context.Background(), "SELECT 1")
	assert.Equal(t, 1, inner.calls,
		"a single-row read runs once; withTransaction is what covers it inside a transaction")
}

// TestPoolConnRetriesThroughItsQueries is the WIRING test. Every case above
// drives conflictRetryDBTX by hand, so all of them keep passing if the store
// stops building its Queries with it -- which is exactly how the fix would be
// lost.
func TestPoolConnRetriesThroughItsQueries(t *testing.T) {
	t.Parallel()

	pool := &scriptedDBTX{errs: []error{abortErr(mysqlErrLockDeadlock)}}
	conn := newPoolConn(&mysqlShared{}, pool)

	require.False(t, conn.inTx(), "a pool-backed conn is not a transaction")
	require.NoError(t, conn.q.SetRevocationEventSequence(context.Background(), 1))
	assert.Equal(t, 2, pool.calls,
		"the pool's Queries must carry conflictRetryDBTX; without it the abort reaches the caller")
}
