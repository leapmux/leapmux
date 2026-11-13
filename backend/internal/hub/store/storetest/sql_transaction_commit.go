package storetest

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SQLTransactionCommitOutcome forces a commit-stage failure through each real
// adapter. Completing the native transaction outside its owner makes Commit
// fail without cancelling the owner's context.
func SQLTransactionCommitOutcome(t *testing.T, build func(*sql.DB) store.Store, complete func(store.Store) error) {
	t.Helper()
	db, err := sqlitedb.Open(filepath.Join(t.TempDir(), "commit.db"), sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	st := build(db)
	err = st.RunInTransaction(t.Context(), func(tx store.Store) error {
		return complete(tx)
	})
	require.ErrorIs(t, err, sql.ErrTxDone)
	assert.True(t, store.CommitOutcomeUnknown(err))
	assert.NotErrorIs(t, err, context.Canceled)
	require.NoError(t, st.RunInTransaction(t.Context(), func(store.Store) error { return nil }))
	rollback := errors.New("confirmed callback rollback")
	err = st.RunInTransaction(t.Context(), func(store.Store) error { return rollback })
	require.ErrorIs(t, err, rollback)
	assert.False(t, store.CommitOutcomeUnknown(err), "a callback failure never attempted commit")
}
