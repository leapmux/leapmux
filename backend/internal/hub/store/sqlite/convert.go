package sqlite

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
	"github.com/leapmux/leapmux/internal/util/timefmt"
	sqlite3 "modernc.org/sqlite"
	sqlitelib "modernc.org/sqlite/lib"
)

// sqliteTimeFormat is the ISO 8601 format matching SQLite's
// strftime('%Y-%m-%dT%H:%M:%fZ', 'now') output.
const sqliteTimeFormat = timefmt.ISO8601

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	var sqliteErr *sqlite3.Error
	if errors.As(err, &sqliteErr) {
		code := sqliteErr.Code()
		if code == sqlitelib.SQLITE_CONSTRAINT_UNIQUE || code == sqlitelib.SQLITE_CONSTRAINT_PRIMARYKEY {
			return fmt.Errorf("%w: %w", store.ErrConflict, err)
		}
	}
	return err
}

// parseCursorToSQLiteTime converts an RFC3339Nano cursor string to the
// ISO 8601 format that SQLite stores via strftime('%Y-%m-%dT%H:%M:%fZ', 'now').
// Returns nil when cursor is empty (first page).
func parseCursorToSQLiteTime(cursor string) (any, error) {
	t, ok, err := store.ParseCursorTime(cursor)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return t.UTC().Format(sqliteTimeFormat), nil
}

func rowsAffected(result sql.Result, err error) (int64, error) {
	return sqlutil.RowsAffected(result, err, mapErr)
}
