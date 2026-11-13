package storetest

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/util/errwrap"
)

// SQLTransactionCancellation tests database/sql transaction ownership through
// each adapter. SQLite supplies the real connection pool; every query below
// reaches an already-rolled-back transaction before SQL dialect parsing.
func SQLTransactionCancellation(t *testing.T, build func(*sql.DB) store.Store) {
	t.Helper()
	for _, outcome := range []string{"commit", "query", "list", "page", "exec", "affected rows", "query and independent failure", "foreign callback", "independent callback", "nested foreign callback"} {
		t.Run(outcome, func(t *testing.T) {
			db, err := sqlitedb.Open(filepath.Join(t.TempDir(), "transaction.db"), sqlitedb.Config{})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			db.SetMaxOpenConns(1)
			live := testutil.DeadlineContext(t)
			foreign, err := db.BeginTx(live, nil)
			require.NoError(t, err)
			require.NoError(t, foreign.Rollback())
			_, foreignErr := foreign.ExecContext(live, "SELECT 1")
			require.ErrorIs(t, foreignErr, sql.ErrTxDone)
			ctx, cancel := context.WithCancel(live)
			defer cancel()
			independent := errors.New("independent transaction failure")
			st := build(db)
			err = st.RunInTransaction(ctx, func(tx store.Store) error {
				cancel()
				// With one connection, this acquisition proves automatic rollback
				// released the owned transaction before the callback returns.
				connection, err := db.Conn(live)
				require.NoError(t, err)
				require.NoError(t, connection.Close())
				switch outcome {
				case "commit":
					return nil
				case "foreign callback":
					return foreignErr
				case "independent callback":
					return independent
				case "nested foreign callback":
					return tx.RunInTransaction(ctx, func(store.Store) error { return foreignErr })
				default:
					var queryErr error
					switch outcome {
					case "list":
						_, queryErr = tx.Settings().GetAll(live)
					case "page":
						_, queryErr = tx.Users().ListAll(live, store.ListAllUsersParams{Limit: 1})
					case "exec":
						queryErr = tx.Settings().Delete(live, "cancellation-probe")
					case "affected rows":
						_, queryErr = tx.Workers().ForceDeregister(live, "cancellation-probe")
					default:
						_, queryErr = tx.Users().GetByID(live, "cancellation-probe")
					}
					require.ErrorIs(t, queryErr, sql.ErrTxDone)
					if outcome == "query and independent failure" {
						return errors.Join(queryErr, independent)
					}
					return queryErr
				}
			})
			if outcome == "commit" {
				assert.False(t, store.CommitOutcomeUnknown(err), "automatic rollback has a known outcome")
			}
			switch outcome {
			case "foreign callback", "nested foreign callback":
				require.ErrorIs(t, err, sql.ErrTxDone)
				assert.NotErrorIs(t, err, context.Canceled, "another transaction's error has no owned cancellation provenance")
				assert.False(t, errwrap.IsCancellation(err))
			case "independent callback":
				require.ErrorIs(t, err, independent)
				assert.NotErrorIs(t, err, context.Canceled)
				assert.False(t, errwrap.IsCancellation(err))
			default:
				require.ErrorIs(t, err, sql.ErrTxDone)
				require.ErrorIs(t, err, context.Canceled)
				if outcome == "query and independent failure" {
					require.ErrorIs(t, err, independent)
					assert.False(t, errwrap.IsCancellation(err))
				} else {
					assert.True(t, errwrap.IsCancellation(err))
				}
			}
		})
	}
}
