package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
	"github.com/leapmux/leapmux/internal/util/timefmt"
	sqlite3 "modernc.org/sqlite"
	sqlitelib "modernc.org/sqlite/lib"
)

// sqliteTimeFormat is the ISO 8601 format matching SQLite's
// strftime('%Y-%m-%dT%H:%M:%fZ', 'now') output.
const sqliteTimeFormat = timefmt.ISO8601

func formatSQLiteTime(t time.Time) string {
	return t.UTC().Format(sqliteTimeFormat)
}

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

func listAllUsersParams(cursor string, limit int64) (gendb.ListAllUsersParams, error) {
	parsedCursor, err := parseCursorToSQLiteTime(cursor)
	if err != nil {
		return gendb.ListAllUsersParams{}, err
	}
	return gendb.ListAllUsersParams{
		Cursor: parsedCursor,
		Limit:  store.ClampListLimit(limit),
	}, nil
}

func searchUsersParams(query *string, cursor string, limit int64) (gendb.SearchUsersParams, error) {
	parsedCursor, err := parseCursorToSQLiteTime(cursor)
	if err != nil {
		return gendb.SearchUsersParams{}, err
	}
	return gendb.SearchUsersParams{
		// Fold the search term the same way the write path folds display_name_folded,
		// so the plain-LIKE match is case-insensitive (and cross-dialect consistent) for
		// non-ASCII names. A nil query stays nil (SearchUsers reads it as "no filter").
		Query:  ptrconv.PtrToNullString(store.FoldSearchQuery(query)),
		Cursor: parsedCursor,
		Limit:  store.ClampListLimit(limit),
	}, nil
}

func listWorkersByUserIDParams(registeredBy, cursor string, limit int64) (gendb.ListWorkersByUserIDParams, error) {
	parsedCursor, err := parseCursorToSQLiteTime(cursor)
	if err != nil {
		return gendb.ListWorkersByUserIDParams{}, err
	}
	return gendb.ListWorkersByUserIDParams{
		RegisteredBy: registeredBy,
		Cursor:       parsedCursor,
		Limit:        store.ClampListLimit(limit),
	}, nil
}

func listWorkersAdminAllParams(cursor string, limit int64) (gendb.ListWorkersAdminAllParams, error) {
	parsedCursor, err := parseCursorToSQLiteTime(cursor)
	if err != nil {
		return gendb.ListWorkersAdminAllParams{}, err
	}
	return gendb.ListWorkersAdminAllParams{
		Cursor: parsedCursor,
		Limit:  store.ClampListLimit(limit),
	}, nil
}

func listWorkersAdminByStatusParams(status leapmuxv1.WorkerStatus, cursor string, limit int64) (gendb.ListWorkersAdminByStatusParams, error) {
	parsedCursor, err := parseCursorToSQLiteTime(cursor)
	if err != nil {
		return gendb.ListWorkersAdminByStatusParams{}, err
	}
	return gendb.ListWorkersAdminByStatusParams{
		Status: status,
		Cursor: parsedCursor,
		Limit:  store.ClampListLimit(limit),
	}, nil
}

func listWorkersAdminByUserParams(userID, cursor string, limit int64) (gendb.ListWorkersAdminByUserParams, error) {
	parsedCursor, err := parseCursorToSQLiteTime(cursor)
	if err != nil {
		return gendb.ListWorkersAdminByUserParams{}, err
	}
	return gendb.ListWorkersAdminByUserParams{
		UserID: userID,
		Cursor: parsedCursor,
		Limit:  store.ClampListLimit(limit),
	}, nil
}

func listWorkersAdminByUserAndStatusParams(userID string, status leapmuxv1.WorkerStatus, cursor string, limit int64) (gendb.ListWorkersAdminByUserAndStatusParams, error) {
	parsedCursor, err := parseCursorToSQLiteTime(cursor)
	if err != nil {
		return gendb.ListWorkersAdminByUserAndStatusParams{}, err
	}
	return gendb.ListWorkersAdminByUserAndStatusParams{
		UserID: userID,
		Status: status,
		Cursor: parsedCursor,
		Limit:  store.ClampListLimit(limit),
	}, nil
}

func listAllActiveSessionsParams(cursor string, limit int64) (gendb.ListAllActiveSessionsParams, error) {
	parsedCursor, err := parseCursorToSQLiteTime(cursor)
	if err != nil {
		return gendb.ListAllActiveSessionsParams{}, err
	}
	return gendb.ListAllActiveSessionsParams{
		Cursor: parsedCursor,
		Limit:  store.ClampListLimit(limit),
	}, nil
}

func rowsAffected(result sql.Result, err error) (int64, error) {
	return sqlutil.RowsAffected(result, err, mapErr)
}
