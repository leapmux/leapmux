package sqlite

import (
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sqlite3 "modernc.org/sqlite"
	sqlitelib "modernc.org/sqlite/lib"
)

// RunInTransaction must own the write lock before `fn` reads anything.
//
// A deferred transaction reads under a snapshot and upgrades it at its first
// write. SQLite refuses that upgrade at once whenever another connection holds or
// took the write lock, and the busy handler never runs for the refusal. A
// workspace delete that raced any other writer then failed the RPC with
// `delete workspace: database is locked`, a 500. Five immediate restarts did not
// help, because each one lost the same race again within microseconds.
func TestRunInTransaction_HoldsWriteIntentBeforeItsReads(t *testing.T) {
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

	// A competing writer that refuses a taken lock at once rather than waiting
	// for it, so the test reads the lock state instead of waiting it out.
	competing, err := other.Conn(ctx)
	require.NoError(t, err)
	defer func() { _ = competing.Close() }()
	_, err = competing.ExecContext(ctx, `PRAGMA busy_timeout=0`)
	require.NoError(t, err)

	attempts := 0
	var competingErrors []error
	err = st.RunInTransaction(ctx, func(tx store.Store) error {
		attempts++
		// READ first, which is what took the snapshot that a deferred
		// transaction then lost.
		var value string
		row := tx.(*sqliteStore).conn.exec.QueryRowContext(ctx, `SELECT value FROM hub_settings WHERE key = 'busy-probe'`)
		if err := row.Scan(&value); err != nil {
			return err
		}
		// Between this read and the write below, another writer must find the
		// lock taken.
		_, competingErr := competing.ExecContext(ctx, `UPDATE hub_settings SET value = '{"n":1}' WHERE key = 'busy-probe'`)
		competingErrors = append(competingErrors, competingErr)
		_, err := tx.(*sqliteStore).conn.exec.ExecContext(ctx, `UPDATE hub_settings SET value = '{"n":2}' WHERE key = 'busy-probe'`)
		return err
	})

	require.NoError(t, err, "a transaction that holds the lock cannot lose it to another writer")
	assert.Equal(t, 1, attempts, "the transaction must not start over")
	require.Len(t, competingErrors, 1)
	var sqliteErr *sqlite3.Error
	require.ErrorAs(t, competingErrors[0], &sqliteErr, "a second writer must find the lock taken while the transaction runs")
	assert.Equal(t, sqlitelib.SQLITE_BUSY, sqliteErr.Code())

	var value string
	require.NoError(t, other.QueryRowContext(ctx, `SELECT value FROM hub_settings WHERE key = 'busy-probe'`).Scan(&value))
	assert.JSONEq(t, `{"n":2}`, value, "only the transaction's own write may land")
}
