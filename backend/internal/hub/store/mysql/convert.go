package mysql

import (
	"context"
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

// decodeCursorParams decodes the composite list cursor into the two nullable sqlc
// parameters MySQL's keyset queries bind: the cursor timestamp (truncated to
// the DATETIME(3) columns' millisecond precision) and the id tiebreaker. An
// empty cursor (first page) returns the zero values so the queries'
// "cursor_time IS NULL" branch selects every row. The id comparison collates
// byte-wise via the table-level COLLATE=utf8mb4_bin declared in the migration.
//
// The timestamp is truncated (not rounded) to milliseconds to match the
// DATETIME(3) columns: the driver serializes a time.Time param with full
// sub-millisecond digits, and a hand-crafted cursor carrying any would make
// the "=" tiebreak branch unsatisfiable while "<" still matched the boundary
// row, re-returning the previous page's tail as duplicates. EncodeCursor
// produces cursors from DB-scanned rows (already millisecond-quantized), so
// this is a no-op for real cursors.
func decodeCursorParams(cursor string) (sql.NullTime, sql.NullString, error) {
	c, err := store.ParseCursor(cursor)
	if err != nil {
		return sql.NullTime{}, sql.NullString{}, err
	}
	if c == nil {
		return sql.NullTime{}, sql.NullString{}, nil
	}
	ms := c.Time.Truncate(time.Millisecond)
	return sql.NullTime{Time: ms, Valid: true}, sql.NullString{String: c.ID, Valid: true}, nil
}

// withCursor decodes the composite list cursor and applies the dialect's
// fetch-limit clamp, then hands the decoded values to fill, which assembles
// the dialect-specific sqlc params struct. Centralizing decodeCursorParams + the
// error short-circuit + store.FetchLimit (with the int32 cast the MySQL LIMIT
// column requires) here means a change to the cursor decode, the clamp rule, or
// the probe-row accounting edits ONE site instead of being copy-pasted across
// every list builder.
func withCursor[T any](cursor string, limit int64, fill func(cursorTime sql.NullTime, cursorID sql.NullString, fetchLimit int32) T) (T, error) {
	cursorTime, cursorID, err := decodeCursorParams(cursor)
	if err != nil {
		var zero T
		return zero, err
	}
	return fill(cursorTime, cursorID, int32(store.FetchLimit(limit))), nil
}

// queryPage forwards to store.QueryPage with this dialect's mapErr bound:
// every listing in the package routes through it, so the shared
// build -> query -> NewPage skeleton (and its error wrapping and probe-row
// accounting) lives once in store instead of drifting per dialect.
func queryPage[P any, R any, I store.PageCursorer](
	ctx context.Context,
	limit int64,
	build func() (P, error),
	query func(context.Context, P) ([]R, error),
	mapRow func(R) I,
) (store.Page[I], error) {
	return store.QueryPage(ctx, limit, build, query, mapRow, mapErr)
}

func listAllUsersParams(cursor string, limit int64) (gendb.ListAllUsersParams, error) {
	return withCursor(cursor, limit, func(ct sql.NullTime, cid sql.NullString, fl int32) gendb.ListAllUsersParams {
		return gendb.ListAllUsersParams{CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func searchUsersParams(query *string, cursor string, limit int64) (gendb.SearchUsersParams, error) {
	return withCursor(cursor, limit, func(ct sql.NullTime, cid sql.NullString, fl int32) gendb.SearchUsersParams {
		// Build the complete LIKE prefix pattern (fold + metachar escape + trailing
		// '%') at the one shared site, so the match is case-insensitive and literal
		// cross-dialect. A nil query stays nil (SearchUsers reads it as "no filter").
		return gendb.SearchUsersParams{
			Query:      ptrconv.PtrToNullString(store.SearchLikePattern(query)),
			CursorTime: ct,
			CursorID:   cid,
			Limit:      fl,
		}
	})
}

func listWorkersByUserIDParams(registeredBy, cursor string, limit int64) (gendb.ListWorkersByUserIDParams, error) {
	return withCursor(cursor, limit, func(ct sql.NullTime, cid sql.NullString, fl int32) gendb.ListWorkersByUserIDParams {
		return gendb.ListWorkersByUserIDParams{RegisteredBy: registeredBy, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

// listWorkersAdminParams builds the status=nil, user_id=nil query
// (ListWorkersAdmin): deleted_at IS NULL, no user filter.
func listWorkersAdminParams(cursor string, limit int64) (gendb.ListWorkersAdminParams, error) {
	return withCursor(cursor, limit, func(ct sql.NullTime, cid sql.NullString, fl int32) gendb.ListWorkersAdminParams {
		return gendb.ListWorkersAdminParams{CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

// listWorkersAdminByUserParams builds the status=nil, user_id=set query
// (ListWorkersAdminByUser): deleted_at IS NULL + required registered_by.
func listWorkersAdminByUserParams(userID string, cursor string, limit int64) (gendb.ListWorkersAdminByUserParams, error) {
	return withCursor(cursor, limit, func(ct sql.NullTime, cid sql.NullString, fl int32) gendb.ListWorkersAdminByUserParams {
		return gendb.ListWorkersAdminByUserParams{UserID: userID, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

// listWorkersAdminByStatusParams builds the status=set, user_id=nil query
// (ListWorkersAdminByStatus): no deleted_at filter (status=3 surfaces
// soft-deleted rows), no user filter.
func listWorkersAdminByStatusParams(status leapmuxv1.WorkerStatus, cursor string, limit int64) (gendb.ListWorkersAdminByStatusParams, error) {
	return withCursor(cursor, limit, func(ct sql.NullTime, cid sql.NullString, fl int32) gendb.ListWorkersAdminByStatusParams {
		return gendb.ListWorkersAdminByStatusParams{Status: status, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

// listWorkersAdminByUserAndStatusParams builds the status=set, user_id=set
// query (ListWorkersAdminByUserAndStatus): required registered_by + status.
func listWorkersAdminByUserAndStatusParams(status leapmuxv1.WorkerStatus, userID string, cursor string, limit int64) (gendb.ListWorkersAdminByUserAndStatusParams, error) {
	return withCursor(cursor, limit, func(ct sql.NullTime, cid sql.NullString, fl int32) gendb.ListWorkersAdminByUserAndStatusParams {
		return gendb.ListWorkersAdminByUserAndStatusParams{Status: status, UserID: userID, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllActiveSessionsParams(cursor string, limit int64) (gendb.ListAllActiveSessionsParams, error) {
	return withCursor(cursor, limit, func(ct sql.NullTime, cid sql.NullString, fl int32) gendb.ListAllActiveSessionsParams {
		return gendb.ListAllActiveSessionsParams{CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listUserSessionsParams(userID, cursor string, limit int64) (gendb.ListUserSessionsByUserIDParams, error) {
	return withCursor(cursor, limit, func(ct sql.NullTime, cid sql.NullString, fl int32) gendb.ListUserSessionsByUserIDParams {
		return gendb.ListUserSessionsByUserIDParams{UserID: userID, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllAPITokensParams(clientType, cursor string, limit int64) (gendb.ListAllAPITokensParams, error) {
	return withCursor(cursor, limit, func(ct sql.NullTime, cid sql.NullString, fl int32) gendb.ListAllAPITokensParams {
		return gendb.ListAllAPITokensParams{ClientType: clientType, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllAPITokensByUserParams(userID, clientType, cursor string, limit int64) (gendb.ListAllAPITokensByUserParams, error) {
	return withCursor(cursor, limit, func(ct sql.NullTime, cid sql.NullString, fl int32) gendb.ListAllAPITokensByUserParams {
		return gendb.ListAllAPITokensByUserParams{UserID: userID, ClientType: clientType, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllAPITokensIncludingRevokedParams(clientType, cursor string, limit int64) (gendb.ListAllAPITokensIncludingRevokedParams, error) {
	return withCursor(cursor, limit, func(ct sql.NullTime, cid sql.NullString, fl int32) gendb.ListAllAPITokensIncludingRevokedParams {
		return gendb.ListAllAPITokensIncludingRevokedParams{ClientType: clientType, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllAPITokensByUserIncludingRevokedParams(userID, clientType, cursor string, limit int64) (gendb.ListAllAPITokensByUserIncludingRevokedParams, error) {
	return withCursor(cursor, limit, func(ct sql.NullTime, cid sql.NullString, fl int32) gendb.ListAllAPITokensByUserIncludingRevokedParams {
		return gendb.ListAllAPITokensByUserIncludingRevokedParams{UserID: userID, ClientType: clientType, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllDelegationTokensParams(cursor string, limit int64) (gendb.ListAllDelegationTokensParams, error) {
	return withCursor(cursor, limit, func(ct sql.NullTime, cid sql.NullString, fl int32) gendb.ListAllDelegationTokensParams {
		return gendb.ListAllDelegationTokensParams{CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllDelegationTokensByUserParams(userID, cursor string, limit int64) (gendb.ListAllDelegationTokensByUserParams, error) {
	return withCursor(cursor, limit, func(ct sql.NullTime, cid sql.NullString, fl int32) gendb.ListAllDelegationTokensByUserParams {
		return gendb.ListAllDelegationTokensByUserParams{UserID: userID, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllDelegationTokensIncludingRevokedParams(cursor string, limit int64) (gendb.ListAllDelegationTokensIncludingRevokedParams, error) {
	return withCursor(cursor, limit, func(ct sql.NullTime, cid sql.NullString, fl int32) gendb.ListAllDelegationTokensIncludingRevokedParams {
		return gendb.ListAllDelegationTokensIncludingRevokedParams{CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllDelegationTokensByUserIncludingRevokedParams(userID, cursor string, limit int64) (gendb.ListAllDelegationTokensByUserIncludingRevokedParams, error) {
	return withCursor(cursor, limit, func(ct sql.NullTime, cid sql.NullString, fl int32) gendb.ListAllDelegationTokensByUserIncludingRevokedParams {
		return gendb.ListAllDelegationTokensByUserIncludingRevokedParams{UserID: userID, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

// listRegistrationKeysAdminParams mirrors the other list builders so the
// registration-key admin listing shares the cursor decode and limit clamp.
// now is the caller-computed expiry probe (zero NullTime when expired rows are
// included), passed in so the builder stays a pure function of its arguments.
func listRegistrationKeysAdminParams(cursor string, limit int64, now sql.NullTime) (gendb.ListRegistrationKeysAdminParams, error) {
	return withCursor(cursor, limit, func(ct sql.NullTime, cid sql.NullString, fl int32) gendb.ListRegistrationKeysAdminParams {
		return gendb.ListRegistrationKeysAdminParams{Now: now, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}
