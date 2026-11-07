package sqlitedb

import (
	"context"
	"database/sql/driver"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"modernc.org/sqlite"
)

// SQLite's global function registry has no unregister operation. Repeated test
// runs need distinct identifiers rather than registering the same function again.
var interruptionFunctionSequence atomic.Uint64

func TestInterruptionClassificationPreservesDriverCancellation(t *testing.T) {
	function := fmt.Sprintf("leapmux_test_interruption_%d", interruptionFunctionSequence.Add(1))
	entered := make(chan struct{})
	release := make(chan struct{})
	require.NoError(t, sqlite.RegisterScalarFunction(function, 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
		close(entered)
		<-release
		return int64(1), nil
	}))
	db, err := Open(":memory:", Config{})
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		var result int
		done <- db.QueryRowContext(ctx, fmt.Sprintf("WITH RECURSIVE numbers(n) AS (SELECT %s() UNION ALL SELECT n+1 FROM numbers WHERE n<1000000000) SELECT sum(n) FROM numbers", function)).Scan(&result)
	}()
	<-entered
	cancel()
	close(release)
	err = <-done
	// The driver normalizes interruption to the canceled query context.
	// Its public API does not expose construction of a native interrupt error.
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, IsInterruption(err))
	require.False(t, IsInterruption(nil))
	require.False(t, IsInterruption((*sqlite.Error)(nil)))
	require.False(t, IsInterruption(context.Canceled))
	_, err = db.Exec("SELECT missing_table_column FROM missing_table")
	require.Error(t, err)
	require.False(t, IsInterruption(err))
}
