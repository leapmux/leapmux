package postgres

import (
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
)

func tsToTime(ts pgtype.Timestamptz) time.Time {
	return ts.Time
}

func tsToTimePtr(ts pgtype.Timestamptz) *time.Time {
	if ts.Valid {
		return &ts.Time
	}
	return nil
}

func timeToTs(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

func timePtrToTs(t *time.Time) pgtype.Timestamptz {
	if t != nil {
		return pgtype.Timestamptz{Time: *t, Valid: true}
	}
	return pgtype.Timestamptz{}
}

func textToPtr(t pgtype.Text) *string {
	if t.Valid {
		return &t.String
	}
	return nil
}

func ptrToText(s *string) pgtype.Text {
	if s != nil {
		return pgtype.Text{String: *s, Valid: true}
	}
	return pgtype.Text{}
}

// parseCursorToTs parses the opaque cursor string into a pgtype.Timestamptz.
// An empty cursor returns the zero value (no cursor). A non-empty cursor must
// be an RFC3339Nano-formatted timestamp.
func parseCursorToTs(cursor string) (pgtype.Timestamptz, error) {
	t, ok, err := store.ParseCursorTime(cursor)
	if err != nil {
		return pgtype.Timestamptz{}, err
	}
	if !ok {
		return pgtype.Timestamptz{}, nil
	}
	return pgtype.Timestamptz{Time: t, Valid: true}, nil
}

func listAllOrgsParams(cursor string, limit int64) (gendb.ListAllOrgsParams, error) {
	parsedCursor, err := parseCursorToTs(cursor)
	if err != nil {
		return gendb.ListAllOrgsParams{}, err
	}
	return gendb.ListAllOrgsParams{
		Cursor: parsedCursor,
		Limit:  int32(limit),
	}, nil
}

func searchOrgsParams(query *string, cursor string, limit int64) (gendb.SearchOrgsParams, error) {
	parsedCursor, err := parseCursorToTs(cursor)
	if err != nil {
		return gendb.SearchOrgsParams{}, err
	}
	return gendb.SearchOrgsParams{
		Query:  ptrToText(query),
		Cursor: parsedCursor,
		Limit:  int32(limit),
	}, nil
}

func listAllUsersParams(cursor string, limit int64) (gendb.ListAllUsersParams, error) {
	parsedCursor, err := parseCursorToTs(cursor)
	if err != nil {
		return gendb.ListAllUsersParams{}, err
	}
	return gendb.ListAllUsersParams{
		Cursor: parsedCursor,
		Limit:  int32(limit),
	}, nil
}

func searchUsersParams(query *string, cursor string, limit int64) (gendb.SearchUsersParams, error) {
	parsedCursor, err := parseCursorToTs(cursor)
	if err != nil {
		return gendb.SearchUsersParams{}, err
	}
	return gendb.SearchUsersParams{
		Query:  ptrToText(query),
		Cursor: parsedCursor,
		Limit:  int32(limit),
	}, nil
}

func listWorkersByUserIDParams(registeredBy, cursor string, limit int64) (gendb.ListWorkersByUserIDParams, error) {
	parsedCursor, err := parseCursorToTs(cursor)
	if err != nil {
		return gendb.ListWorkersByUserIDParams{}, err
	}
	return gendb.ListWorkersByUserIDParams{
		RegisteredBy: registeredBy,
		Cursor:       parsedCursor,
		Limit:        int32(limit),
	}, nil
}

func listOwnedWorkersParams(userID, cursor string, limit int64) (gendb.ListOwnedWorkersParams, error) {
	parsedCursor, err := parseCursorToTs(cursor)
	if err != nil {
		return gendb.ListOwnedWorkersParams{}, err
	}
	return gendb.ListOwnedWorkersParams{
		UserID: userID,
		Cursor: parsedCursor,
		Limit:  int32(limit),
	}, nil
}

func listWorkersAdminAllParams(cursor string, limit int64) (gendb.ListWorkersAdminAllParams, error) {
	parsedCursor, err := parseCursorToTs(cursor)
	if err != nil {
		return gendb.ListWorkersAdminAllParams{}, err
	}
	return gendb.ListWorkersAdminAllParams{
		Cursor: parsedCursor,
		Limit:  int32(limit),
	}, nil
}

func listWorkersAdminByStatusParams(status leapmuxv1.WorkerStatus, cursor string, limit int64) (gendb.ListWorkersAdminByStatusParams, error) {
	parsedCursor, err := parseCursorToTs(cursor)
	if err != nil {
		return gendb.ListWorkersAdminByStatusParams{}, err
	}
	return gendb.ListWorkersAdminByStatusParams{
		Status: status,
		Cursor: parsedCursor,
		Limit:  int32(limit),
	}, nil
}

func listWorkersAdminByUserParams(userID, cursor string, limit int64) (gendb.ListWorkersAdminByUserParams, error) {
	parsedCursor, err := parseCursorToTs(cursor)
	if err != nil {
		return gendb.ListWorkersAdminByUserParams{}, err
	}
	return gendb.ListWorkersAdminByUserParams{
		UserID: userID,
		Cursor: parsedCursor,
		Limit:  int32(limit),
	}, nil
}

func listWorkersAdminByUserAndStatusParams(userID string, status leapmuxv1.WorkerStatus, cursor string, limit int64) (gendb.ListWorkersAdminByUserAndStatusParams, error) {
	parsedCursor, err := parseCursorToTs(cursor)
	if err != nil {
		return gendb.ListWorkersAdminByUserAndStatusParams{}, err
	}
	return gendb.ListWorkersAdminByUserAndStatusParams{
		UserID: userID,
		Status: status,
		Cursor: parsedCursor,
		Limit:  int32(limit),
	}, nil
}

func listAllActiveSessionsParams(cursor string, limit int64) (gendb.ListAllActiveSessionsParams, error) {
	parsedCursor, err := parseCursorToTs(cursor)
	if err != nil {
		return gendb.ListAllActiveSessionsParams{}, err
	}
	return gendb.ListAllActiveSessionsParams{
		Cursor: parsedCursor,
		Limit:  int32(limit),
	}, nil
}

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if pgErr.Code == pgerrcode.UniqueViolation {
			return fmt.Errorf("%w: %w", store.ErrConflict, err)
		}
	}
	return err
}

func rowsAffected(tag pgconn.CommandTag, err error) (int64, error) {
	if err != nil {
		return 0, mapErr(err)
	}
	return tag.RowsAffected(), nil
}
