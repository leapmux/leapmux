package mysql

import (
	"database/sql"
	"testing"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
)

func TestSQLTransactionCancellationOwnership(t *testing.T) {
	storetest.SQLTransactionCancellation(t, func(db *sql.DB) store.Store {
		return &mysqlStore{conn: &mysqlConn{shared: &mysqlShared{db: db}, exec: db, q: gendb.New(db)}}
	})
}
