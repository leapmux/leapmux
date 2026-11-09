package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

// closeTestGuard limits each wait for close. It is a deadlock guard, not a
// timing assumption: a close that works returns within its checkpoint limit.
const closeTestGuard = 30 * time.Second

// openStoreWithSetting opens a store on a file and writes one setting, so the
// WAL holds frames for the checkpoint in close to copy.
func openStoreWithSetting(t *testing.T) (st *sqliteStore, path string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "hub.db")
	opened, err := Open(path, sqlitedb.Config{})
	require.NoError(t, err)
	value := `true`
	require.NoError(t, opened.Settings().Upsert(context.Background(), store.UpsertSettingParams{
		Key: "signup_enabled", Value: &value,
	}))
	return opened.(*sqliteStore), path
}

// closeWithGuard runs close with checkpointLimit on its own goroutine and
// fails the test if it does not return.
func closeWithGuard(t *testing.T, st *sqliteStore, checkpointLimit time.Duration) {
	t.Helper()
	closed := make(chan error, 1)
	go func() { closed <- st.close(checkpointLimit) }()
	select {
	case err := <-closed:
		require.NoError(t, err)
	case <-time.After(closeTestGuard):
		t.Fatal("close did not return")
	}
}

// walSize returns the size of the database's WAL file, or -1 when the file
// does not exist.
func walSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path + "-wal")
	if os.IsNotExist(err) {
		return -1
	}
	require.NoError(t, err)
	return info.Size()
}

// The busy timeout of this database has no practical limit, so a straggler
// that holds the write lock at shutdown used to hold the checkpoint in Close
// for about 24.8 days. The checkpoint now gives up at its limit, and the close
// still runs. The WAL survives, and the next open recovers every write.
func TestCloseGivesUpTheCheckpointWhenAWriterHoldsTheLock(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)
	st, path := openStoreWithSetting(t)

	// _txlock=immediate makes BEGIN take the write lock, which the TRUNCATE
	// checkpoint needs.
	straggler, err := sqlitedb.Open(path, sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = straggler.Close() })
	tx, err := straggler.Begin()
	require.NoError(t, err)

	closeWithGuard(t, st, 100*time.Millisecond)

	assert.Contains(t, logs.String(), "WAL checkpoint failed", "close must report the checkpoint it gave up")
	assert.Contains(t, logs.String(), context.DeadlineExceeded.Error(),
		"the checkpoint must end at its limit, not fail for another reason")
	assert.Positive(t, walSize(t, path), "the WAL must survive a checkpoint that gave up")
	_, err = st.conn.shared.db.Exec("SELECT 1")
	assert.ErrorContains(t, err, "database is closed", "close must close the database after the checkpoint gave up")

	require.NoError(t, tx.Rollback())
	require.NoError(t, straggler.Close())

	reopened, err := Open(path, sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	row, err := reopened.Settings().Get(context.Background(), "signup_enabled")
	require.NoError(t, err, "the next open must recover the write that the WAL held")
	require.NotNil(t, row.Value)
	assert.Equal(t, "true", *row.Value)
}

// With no writer in the way, the limit changes nothing: the checkpoint copies
// the WAL into the database and truncates it.
func TestCloseCheckpointsTheWAL(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)
	st, path := openStoreWithSetting(t)
	require.Positive(t, walSize(t, path), "the write must reach the WAL before close")

	closeWithGuard(t, st, closeCheckpointTimeout)

	assert.NotContains(t, logs.String(), "WAL checkpoint failed")
	assert.LessOrEqual(t, walSize(t, path), int64(0), "the checkpoint must truncate the WAL, or the close must remove it")
}
