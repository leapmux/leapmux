package sqlite

import (
	"database/sql"
	"testing"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
)

func newTransactionTestStore(db *sql.DB) store.Store {
	return &sqliteStore{conn: &sqliteConn{shared: &sqliteShared{db: db}, exec: db, q: gendb.New(db)}}
}

func TestSQLTransactionCancellationOwnership(t *testing.T) {
	storetest.SQLTransactionCancellation(t, newTransactionTestStore)
}

func TestSQLTransactionCommitOutcomeOwnership(t *testing.T) {
	storetest.SQLTransactionCommitOutcome(t, newTransactionTestStore, func(tx store.Store) error {
		return tx.(*sqliteStore).conn.exec.(*sql.Tx).Rollback()
	})
}
