package mysql

import (
	"database/sql"
	"testing"

	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
)

func newTransactionTestStore(db *sql.DB) store.Store {
	ks, err := keystore.NewRandom()
	if err != nil {
		panic(err)
	}
	return &mysqlStore{conn: &mysqlConn{shared: &mysqlShared{db: db}, ks: ks, exec: db, q: gendb.New(db)}}
}

func TestSQLTransactionCancellationOwnership(t *testing.T) {
	storetest.SQLTransactionCancellation(t, newTransactionTestStore)
}

func TestSQLTransactionCommitOutcomeOwnership(t *testing.T) {
	storetest.SQLTransactionCommitOutcome(t, newTransactionTestStore, func(tx store.Store) error {
		return tx.(*mysqlStore).conn.exec.(*sql.Tx).Rollback()
	})
}
