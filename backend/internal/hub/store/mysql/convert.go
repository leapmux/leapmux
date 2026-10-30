package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
	"github.com/leapmux/leapmux/internal/util/sqltime"
)

// mysqlErrDupEntry is the MySQL error number for duplicate-key violations.
const mysqlErrDupEntry = 1062

// isDupEntry reports whether err is a duplicate-key violation.
func isDupEntry(err error) bool {
	var mysqlErr *mysqldriver.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == mysqlErrDupEntry
}

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	if isDupEntry(err) {
		return fmt.Errorf("%w: %w", store.ErrConflict, err)
	}
	return err
}

func rowsAffected(result sql.Result, err error) (int64, error) {
	return sqlutil.RowsAffected(result, err, mapErr)
}

// decodeCursorParams decodes the composite list cursor into the two nullable sqlc
// parameters MySQL's keyset queries bind: the cursor timestamp and the id
// tiebreaker. An empty cursor (first page) returns the zero values so the
// queries' "cursor_time IS NULL" branch selects every row (an invalid
// MySQLNullTime binds NULL). The id comparison collates byte-wise via the
// table-level COLLATE=utf8mb4_bin declared in the migration.
//
// The timestamp is a MySQLNullTime, whose Value() floors (not rounds) to
// milliseconds to match the DATETIME(3) columns: a hand-crafted cursor carrying
// sub-millisecond digits would otherwise make the "=" tiebreak branch
// unsatisfiable while "<" still matched the boundary row, re-returning the
// previous page's tail as duplicates. EncodeCursor produces cursors from
// DB-scanned rows (already millisecond-quantized), so this is a no-op for real
// cursors.
func decodeCursorParams(cursor string) (sqltime.MySQLNullTime, sql.NullString, error) {
	c, err := store.ParseCursor(cursor)
	if err != nil {
		return sqltime.MySQLNullTime{}, sql.NullString{}, err
	}
	if c == nil {
		return sqltime.MySQLNullTime{}, sql.NullString{}, nil
	}
	return sqltime.MySQLNullTimeOf(c.Time), sql.NullString{String: c.ID, Valid: true}, nil
}

// withCursor decodes the composite list cursor and applies the dialect's
// fetch-limit clamp, then hands the decoded values to fill, which assembles
// the dialect-specific sqlc params struct. Centralizing decodeCursorParams + the
// error short-circuit + store.FetchLimit (with the int32 cast the MySQL LIMIT
// column requires) here means a change to the cursor decode, the clamp rule, or
// the probe-row accounting edits ONE site instead of a copy in every list
// builder.
func withCursor[T any](cursor string, limit int64, fill func(cursorTime sqltime.MySQLNullTime, cursorID sql.NullString, fetchLimit int32) T) (T, error) {
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
	return withCursor(cursor, limit, func(ct sqltime.MySQLNullTime, cid sql.NullString, fl int32) gendb.ListAllUsersParams {
		return gendb.ListAllUsersParams{CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func searchUsersParams(query *string, cursor string, limit int64) (gendb.SearchUsersParams, error) {
	return withCursor(cursor, limit, func(ct sqltime.MySQLNullTime, cid sql.NullString, fl int32) gendb.SearchUsersParams {
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
	return withCursor(cursor, limit, func(ct sqltime.MySQLNullTime, cid sql.NullString, fl int32) gendb.ListWorkersByUserIDParams {
		return gendb.ListWorkersByUserIDParams{RegisteredBy: registeredBy, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

// listWorkersAdminLiveParams builds the user_id=none live query
// (ListWorkersAdminLive): deleted_at IS NULL, with the live states the scope
// includes.
func listWorkersAdminLiveParams(scope store.WorkerAdminListScope, cursor string, limit int64) (gendb.ListWorkersAdminLiveParams, error) {
	return withCursor(cursor, limit, func(ct sqltime.MySQLNullTime, cid sql.NullString, fl int32) gendb.ListWorkersAdminLiveParams {
		return gendb.ListWorkersAdminLiveParams{
			IncludeActive: scope.IncludeActive, IncludeDeregistering: scope.IncludeDeregistering,
			CursorTime: ct, CursorID: cid, Limit: fl,
		}
	})
}

// listWorkersAdminLiveByUserParams builds the user_id=set live query
// (ListWorkersAdminLiveByUser). The user_id dimension is its own query rather
// than an opt-in `(narg IS NULL OR registered_by = narg)` probe; see
// workers.sql for the full 2x2 matrix rationale.
func listWorkersAdminLiveByUserParams(scope store.WorkerAdminListScope, userID string, cursor string, limit int64) (gendb.ListWorkersAdminLiveByUserParams, error) {
	return withCursor(cursor, limit, func(ct sqltime.MySQLNullTime, cid sql.NullString, fl int32) gendb.ListWorkersAdminLiveByUserParams {
		return gendb.ListWorkersAdminLiveByUserParams{
			IncludeActive: scope.IncludeActive, IncludeDeregistering: scope.IncludeDeregistering,
			UserID: userID, CursorTime: ct, CursorID: cid, Limit: fl,
		}
	})
}

// listWorkersAdminDeletedParams builds the user_id=none deleted query
// (ListWorkersAdminDeleted): deleted_at IS NOT NULL.
func listWorkersAdminDeletedParams(cursor string, limit int64) (gendb.ListWorkersAdminDeletedParams, error) {
	return withCursor(cursor, limit, func(ct sqltime.MySQLNullTime, cid sql.NullString, fl int32) gendb.ListWorkersAdminDeletedParams {
		return gendb.ListWorkersAdminDeletedParams{CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

// listWorkersAdminDeletedByUserParams builds the user_id=set deleted query
// (ListWorkersAdminDeletedByUser).
func listWorkersAdminDeletedByUserParams(userID string, cursor string, limit int64) (gendb.ListWorkersAdminDeletedByUserParams, error) {
	return withCursor(cursor, limit, func(ct sqltime.MySQLNullTime, cid sql.NullString, fl int32) gendb.ListWorkersAdminDeletedByUserParams {
		return gendb.ListWorkersAdminDeletedByUserParams{UserID: userID, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllActiveSessionsParams(cursor string, limit int64, now time.Time) (gendb.ListAllActiveSessionsParams, error) {
	nt := sqltime.NewMySQLTime(now)
	return withCursor(cursor, limit, func(ct sqltime.MySQLNullTime, cid sql.NullString, fl int32) gendb.ListAllActiveSessionsParams {
		return gendb.ListAllActiveSessionsParams{Now: nt, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listUserSessionsParams(userID, cursor string, limit int64, now time.Time) (gendb.ListUserSessionsByUserIDParams, error) {
	nt := sqltime.NewMySQLTime(now)
	return withCursor(cursor, limit, func(ct sqltime.MySQLNullTime, cid sql.NullString, fl int32) gendb.ListUserSessionsByUserIDParams {
		return gendb.ListUserSessionsByUserIDParams{UserID: userID, Now: nt, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllAPITokensParams(clientID, cursor string, limit int64) (gendb.ListAllAPITokensParams, error) {
	return withCursor(cursor, limit, func(ct sqltime.MySQLNullTime, cid sql.NullString, fl int32) gendb.ListAllAPITokensParams {
		return gendb.ListAllAPITokensParams{ClientID: clientID, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAPITokensByUserParams(userID, clientID, cursor string, limit int64) (gendb.ListAPITokensByUserParams, error) {
	return withCursor(cursor, limit, func(ct sqltime.MySQLNullTime, cid sql.NullString, fl int32) gendb.ListAPITokensByUserParams {
		return gendb.ListAPITokensByUserParams{UserID: userID, ClientID: clientID, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllAPITokensByUserParams(userID, clientID, cursor string, limit int64) (gendb.ListAllAPITokensByUserParams, error) {
	return withCursor(cursor, limit, func(ct sqltime.MySQLNullTime, cid sql.NullString, fl int32) gendb.ListAllAPITokensByUserParams {
		return gendb.ListAllAPITokensByUserParams{UserID: userID, ClientID: clientID, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllAPITokensIncludingRevokedParams(clientID, cursor string, limit int64) (gendb.ListAllAPITokensIncludingRevokedParams, error) {
	return withCursor(cursor, limit, func(ct sqltime.MySQLNullTime, cid sql.NullString, fl int32) gendb.ListAllAPITokensIncludingRevokedParams {
		return gendb.ListAllAPITokensIncludingRevokedParams{ClientID: clientID, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllAPITokensByUserIncludingRevokedParams(userID, clientID, cursor string, limit int64) (gendb.ListAllAPITokensByUserIncludingRevokedParams, error) {
	return withCursor(cursor, limit, func(ct sqltime.MySQLNullTime, cid sql.NullString, fl int32) gendb.ListAllAPITokensByUserIncludingRevokedParams {
		return gendb.ListAllAPITokensByUserIncludingRevokedParams{UserID: userID, ClientID: clientID, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllDelegationTokensParams(cursor string, limit int64) (gendb.ListAllDelegationTokensParams, error) {
	return withCursor(cursor, limit, func(ct sqltime.MySQLNullTime, cid sql.NullString, fl int32) gendb.ListAllDelegationTokensParams {
		return gendb.ListAllDelegationTokensParams{CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllDelegationTokensByUserParams(userID, cursor string, limit int64) (gendb.ListAllDelegationTokensByUserParams, error) {
	return withCursor(cursor, limit, func(ct sqltime.MySQLNullTime, cid sql.NullString, fl int32) gendb.ListAllDelegationTokensByUserParams {
		return gendb.ListAllDelegationTokensByUserParams{UserID: userID, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllDelegationTokensIncludingRevokedParams(cursor string, limit int64) (gendb.ListAllDelegationTokensIncludingRevokedParams, error) {
	return withCursor(cursor, limit, func(ct sqltime.MySQLNullTime, cid sql.NullString, fl int32) gendb.ListAllDelegationTokensIncludingRevokedParams {
		return gendb.ListAllDelegationTokensIncludingRevokedParams{CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllDelegationTokensByUserIncludingRevokedParams(userID, cursor string, limit int64) (gendb.ListAllDelegationTokensByUserIncludingRevokedParams, error) {
	return withCursor(cursor, limit, func(ct sqltime.MySQLNullTime, cid sql.NullString, fl int32) gendb.ListAllDelegationTokensByUserIncludingRevokedParams {
		return gendb.ListAllDelegationTokensByUserIncludingRevokedParams{UserID: userID, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

// listRegistrationKeysAdminParams mirrors the other list builders so the
// registration-key admin listing shares the cursor decode and limit clamp.
// now is the caller-computed expiry probe (invalid MySQLNullTime when expired
// rows are included), passed in so the builder stays a pure function of its arguments.
func listRegistrationKeysAdminParams(cursor string, limit int64, now sqltime.MySQLNullTime) (gendb.ListRegistrationKeysAdminParams, error) {
	return withCursor(cursor, limit, func(ct sqltime.MySQLNullTime, cid sql.NullString, fl int32) gendb.ListRegistrationKeysAdminParams {
		return gendb.ListRegistrationKeysAdminParams{Now: now, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listOAuthClientsParams(userID string, p store.ListOAuthClientsParams) (gendb.ListOAuthClientsParams, error) {
	return withCursor(p.Cursor, p.Limit, func(ct sqltime.MySQLNullTime, cid sql.NullString, fl int32) gendb.ListOAuthClientsParams {
		return gendb.ListOAuthClientsParams{UserID: sql.NullString{String: userID, Valid: true}, IncludeRevoked: p.IncludeRevoked, IncludeHubWide: p.IncludeHubWide, HubWideOnly: p.HubWideOnly, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}
