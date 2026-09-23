package sqlite

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sqlite3 "modernc.org/sqlite"
)

// A deferred transaction that READS and then WRITES loses its snapshot whenever
// another connection commits in between. SQLite refuses the write outright --
// the `busy_timeout` handler is never invoked for it -- so only a restart of the
// whole transaction clears it.
//
// Before the retry, this surfaced as a failed RPC: a workspace delete racing any
// other writer answered `delete workspace: database is locked`, which the hub
// reported as a 500.
func TestRunInTransaction_RetriesAfterAConcurrentWriterInvalidatesTheSnapshot(t *testing.T) {
	t.Parallel()

	// A FILE, not `:memory:`: an in-memory database is one connection with one
	// isolated instance, so two writers cannot contend inside it at all.
	path := filepath.Join(t.TempDir(), "hub.db")
	st, err := Open(path, sqlitedb.Config{})
	require.NoError(t, err)
	defer func() { _ = st.Close() }()

	other, err := OpenDB(path, sqlitedb.Config{})
	require.NoError(t, err)
	defer func() { _ = other.Close() }()

	ctx := t.Context()
	// The row this contends over. `hub_settings` is a plain key-value table, so
	// the test needs no fixture beyond the migration that created it.
	_, err = other.ExecContext(ctx, `INSERT INTO hub_settings (key, value) VALUES ('busy-probe', '{}')`)
	require.NoError(t, err)

	var once sync.Once
	attempts := 0
	err = st.RunInTransaction(ctx, func(tx store.Store) error {
		attempts++
		// READ first, which is what takes the snapshot this test invalidates.
		var value string
		row := tx.(*sqliteStore).conn.exec.QueryRowContext(ctx, `SELECT value FROM hub_settings WHERE key = 'busy-probe'`)
		if err := row.Scan(&value); err != nil {
			return err
		}
		// Commit from the OTHER connection, once, between this read and the
		// write below. The second attempt reads a fresh snapshot and wins.
		once.Do(func() {
			_, writeErr := other.ExecContext(ctx, `UPDATE hub_settings SET value = '{"n":1}' WHERE key = 'busy-probe'`)
			assert.NoError(t, writeErr)
		})
		_, err := tx.(*sqliteStore).conn.exec.ExecContext(ctx, `UPDATE hub_settings SET value = '{"n":2}' WHERE key = 'busy-probe'`)
		return err
	})

	require.NoError(t, err, "the transaction must start over rather than surface the lock")
	assert.Greater(t, attempts, 1, "the first attempt must have lost its snapshot, or this test proves nothing")
}

// The TRUE case is covered by the retry test above, which cannot pass unless
// this predicate recognised the refusal. `sqlite3.Error` carries its code in an
// unexported field with no constructor, so a unit test cannot build a busy one.
// What it can pin is that nothing ELSE reads as contention -- a retry on the
// wrong error would repeat a transaction that failed for a real reason.
func TestIsSQLiteBusy_RefusesEveryErrorThatIsNotContention(t *testing.T) {
	t.Parallel()

	assert.False(t, isSQLiteBusy(nil))
	assert.False(t, isSQLiteBusy(store.ErrNotFound))
	assert.False(t, isSQLiteBusy(store.ErrConflict))
	assert.False(t, isSQLiteBusy(errors.New("delete workspace: some other failure")))
	// A SQLite error that is not a busy one, which is the case worth pinning:
	// the predicate reads the CODE and must not match on the type alone.
	assert.False(t, isSQLiteBusy(&sqlite3.Error{}))
}
