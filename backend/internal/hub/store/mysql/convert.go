package mysql

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
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

func listAllOrgsParams(cursor string, limit int64) (gendb.ListAllOrgsParams, error) {
	column1, createdAt, err := parseMySQLCursor(cursor)
	if err != nil {
		return gendb.ListAllOrgsParams{}, err
	}
	return gendb.ListAllOrgsParams{
		Column1:   column1,
		CreatedAt: createdAt,
		Limit:     int32(limit),
	}, nil
}

func searchOrgsParams(query *string, cursor string, limit int64) (gendb.SearchOrgsParams, error) {
	column3, createdAt, err := parseMySQLCursor(cursor)
	if err != nil {
		return gendb.SearchOrgsParams{}, err
	}
	return gendb.SearchOrgsParams{
		Query:     ptrconv.PtrToNullString(query),
		Column3:   column3,
		CreatedAt: createdAt,
		Limit:     int32(limit),
	}, nil
}

func listAllUsersParams(cursor string, limit int64) (gendb.ListAllUsersParams, error) {
	column1, createdAt, err := parseMySQLCursor(cursor)
	if err != nil {
		return gendb.ListAllUsersParams{}, err
	}
	return gendb.ListAllUsersParams{
		Column1:   column1,
		CreatedAt: createdAt,
		Limit:     int32(limit),
	}, nil
}

func searchUsersParams(query *string, cursor string, limit int64) (gendb.SearchUsersParams, error) {
	column5, createdAt, err := parseMySQLCursor(cursor)
	if err != nil {
		return gendb.SearchUsersParams{}, err
	}
	return gendb.SearchUsersParams{
		Query:     ptrconv.PtrToNullString(query),
		Column5:   column5,
		CreatedAt: createdAt,
		Limit:     int32(limit),
	}, nil
}

func listWorkersByUserIDParams(registeredBy, cursor string, limit int64) (gendb.ListWorkersByUserIDParams, error) {
	column2, createdAt, err := parseMySQLCursor(cursor)
	if err != nil {
		return gendb.ListWorkersByUserIDParams{}, err
	}
	return gendb.ListWorkersByUserIDParams{
		RegisteredBy: registeredBy,
		Column2:      column2,
		CreatedAt:    createdAt,
		Limit:        int32(limit),
	}, nil
}

func listOwnedWorkersParams(userID, cursor string, limit int64) (gendb.ListOwnedWorkersParams, error) {
	column2, createdAt, err := parseMySQLCursor(cursor)
	if err != nil {
		return gendb.ListOwnedWorkersParams{}, err
	}
	return gendb.ListOwnedWorkersParams{
		UserID:      userID,
		Column2:     column2,
		CreatedAt:   createdAt,
		Column5:     column2,
		CreatedAt_2: createdAt,
		Limit:       int32(limit),
	}, nil
}

func listWorkersAdminAllParams(cursor string, limit int64) (gendb.ListWorkersAdminAllParams, error) {
	column1, createdAt, err := parseMySQLCursor(cursor)
	if err != nil {
		return gendb.ListWorkersAdminAllParams{}, err
	}
	return gendb.ListWorkersAdminAllParams{
		Column1:   column1,
		CreatedAt: createdAt,
		Limit:     int32(limit),
	}, nil
}

func listWorkersAdminByStatusParams(status leapmuxv1.WorkerStatus, cursor string, limit int64) (gendb.ListWorkersAdminByStatusParams, error) {
	column2, createdAt, err := parseMySQLCursor(cursor)
	if err != nil {
		return gendb.ListWorkersAdminByStatusParams{}, err
	}
	return gendb.ListWorkersAdminByStatusParams{
		Status:    status,
		Column2:   column2,
		CreatedAt: createdAt,
		Limit:     int32(limit),
	}, nil
}

func listWorkersAdminByUserParams(userID, cursor string, limit int64) (gendb.ListWorkersAdminByUserParams, error) {
	column2, createdAt, err := parseMySQLCursor(cursor)
	if err != nil {
		return gendb.ListWorkersAdminByUserParams{}, err
	}
	return gendb.ListWorkersAdminByUserParams{
		UserID:    userID,
		Column2:   column2,
		CreatedAt: createdAt,
		Limit:     int32(limit),
	}, nil
}

func listWorkersAdminByUserAndStatusParams(userID string, status leapmuxv1.WorkerStatus, cursor string, limit int64) (gendb.ListWorkersAdminByUserAndStatusParams, error) {
	column3, createdAt, err := parseMySQLCursor(cursor)
	if err != nil {
		return gendb.ListWorkersAdminByUserAndStatusParams{}, err
	}
	return gendb.ListWorkersAdminByUserAndStatusParams{
		UserID:    userID,
		Status:    status,
		Column3:   column3,
		CreatedAt: createdAt,
		Limit:     int32(limit),
	}, nil
}

func listAllActiveSessionsParams(cursor string, limit int64) (gendb.ListAllActiveSessionsParams, error) {
	column1, lastActiveAt, err := parseMySQLCursor(cursor)
	if err != nil {
		return gendb.ListAllActiveSessionsParams{}, err
	}
	return gendb.ListAllActiveSessionsParams{
		Column1:      column1,
		LastActiveAt: lastActiveAt,
		Limit:        int32(limit),
	}, nil
}
