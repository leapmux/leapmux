package mysql

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
)

// mysqlErrDupEntry is the MySQL error number for duplicate-key violations.
const mysqlErrDupEntry = 1062

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	var mysqlErr *mysqldriver.MySQLError
	if errors.As(err, &mysqlErr) {
		if mysqlErr.Number == mysqlErrDupEntry {
			return fmt.Errorf("%w: %w", store.ErrConflict, err)
		}
	}
	return err
}

func rowsAffected(result sql.Result, err error) (int64, error) {
	return sqlutil.RowsAffected(result, err, mapErr)
}

// parseMySQLCursor parses the opaque cursor string into the two positional
// parameters needed by MySQL queries that use "(? IS NULL OR <col> < ?)".
// The first return is nil for the first page (making the IS NULL branch true),
// or the parsed time for subsequent pages. The second return is the same time
// value for binding to the "<col> < ?" parameter. The cursor column varies
// by query (created_at, last_active_at, etc.) — it is the caller's
// responsibility to bind the returns to the correct query parameters.
func parseMySQLCursor(cursor string) (any, time.Time, error) {
	t, ok, err := store.ParseCursorTime(cursor)
	if err != nil {
		return nil, time.Time{}, err
	}
	if !ok {
		return nil, time.Time{}, nil
	}
	return t, t, nil
}
