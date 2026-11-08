package sqlite

import (
	"database/sql"
	"testing"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
)

func TestSQLTransactionCancellationOwnership(t *testing.T) {
	storetest.SQLTransactionCancellation(t, func(db *sql.DB) store.Store {
		return &sqliteStore{conn: &sqliteConn{shared: &sqliteShared{db: db}, exec: db, q: gendb.New(db)}}
	})
}
