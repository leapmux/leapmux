package sqlite

import (
	"context"
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

// sqliteTimeFormat is the ISO 8601 layout SQLite writes via
// strftime('%Y-%m-%dT%H:%M:%fZ', ...): fixed 3-digit fractional seconds, the
// same instant grid as this Go layout's ".000". Every SQL-side write path
// (column DEFAULTs, the Create/Touch strftime wraps, SoftDelete's
// strftime('now')) stores canonical 24-char values ON DISK, so the raw-string
// keyset predicates and cleanup cutoff compares -- which run SQL-side against
// the stored bytes -- are byte-exact against formatSQLiteTime-formatted
// params, including at trailing-zero milliseconds (pinned by
// TestKeysetCursorTrailingZeroMillisecondTie in sessions_internal_test.go).
// CAUTION for tests and tooling: modernc TRIMS trailing fractional zeros when
// a DATETIME column is scanned into a Go string (a stored ".130Z" arrives as
// ".13Z") -- a driver presentation artifact only; production reads scan into
// time.Time and are unaffected. Do not mistake a short scanned string for
// on-disk variability.
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

// decodeCursorParams decodes the composite list cursor into the two nullable sqlc
// parameters SQLite's keyset queries bind: the cursor timestamp, formatted as
// the ISO 8601 string SQLite stores via strftime('%Y-%m-%dT%H:%M:%fZ'), and the
// id tiebreaker. An empty cursor (first page) returns the zero values so the
// queries' "cursor_time IS NULL" branch selects every row.
func decodeCursorParams(cursor string) (cursorTime any, cursorID sql.NullString, err error) {
	c, err := store.ParseCursor(cursor)
	if err != nil {
		return nil, sql.NullString{}, err
	}
	if c == nil {
		return nil, sql.NullString{}, nil
	}
	return formatSQLiteTime(c.Time), sql.NullString{String: c.ID, Valid: true}, nil
}

// withCursor decodes the composite list cursor and applies the dialect's
// fetch-limit clamp, then hands the decoded values to fill, which assembles
// the dialect-specific sqlc params struct. Centralizing decodeCursorParams + the
// error short-circuit + store.FetchLimit here means a change to the cursor
// decode, the clamp rule, or the probe-row accounting edits ONE site per
// dialect instead of being copy-pasted across every list builder -- the prior
// shape let ListRegistrationKeysAdmin silently bypass ClampListLimit until this
// commit's pagination rebuild caught it.
func withCursor[T any](cursor string, limit int64, fill func(cursorTime any, cursorID sql.NullString, fetchLimit int64) T) (T, error) {
	cursorTime, cursorID, err := decodeCursorParams(cursor)
	if err != nil {
		var zero T
		return zero, err
	}
	return fill(cursorTime, cursorID, store.FetchLimit(limit)), nil
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
	return withCursor(cursor, limit, func(ct any, cid sql.NullString, fl int64) gendb.ListAllUsersParams {
		return gendb.ListAllUsersParams{CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func searchUsersParams(query *string, cursor string, limit int64) (gendb.SearchUsersParams, error) {
	return withCursor(cursor, limit, func(ct any, cid sql.NullString, fl int64) gendb.SearchUsersParams {
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
	return withCursor(cursor, limit, func(ct any, cid sql.NullString, fl int64) gendb.ListWorkersByUserIDParams {
		return gendb.ListWorkersByUserIDParams{RegisteredBy: registeredBy, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

// listWorkersAdminParams builds the status=nil, user_id=nil query
// (ListWorkersAdmin): deleted_at IS NULL, no user filter.
func listWorkersAdminParams(cursor string, limit int64) (gendb.ListWorkersAdminParams, error) {
	return withCursor(cursor, limit, func(ct any, cid sql.NullString, fl int64) gendb.ListWorkersAdminParams {
		return gendb.ListWorkersAdminParams{CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

// listWorkersAdminByUserParams builds the status=nil, user_id=set query
// (ListWorkersAdminByUser): deleted_at IS NULL + required registered_by. The
// user_id dimension is its own query rather than an opt-in `(narg IS NULL OR
// registered_by = narg)` probe: that probe defeats SQLite's index seek for the
// user_id-set case (EXPLAIN: full partial-index scan + sort) and emits an
// untyped interface{} param that breaks Postgres (SQLSTATE 42P08). See
// workers.sql for the full 2x2 matrix rationale.
func listWorkersAdminByUserParams(userID string, cursor string, limit int64) (gendb.ListWorkersAdminByUserParams, error) {
	return withCursor(cursor, limit, func(ct any, cid sql.NullString, fl int64) gendb.ListWorkersAdminByUserParams {
		return gendb.ListWorkersAdminByUserParams{UserID: userID, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

// listWorkersAdminByStatusParams builds the status=set, user_id=nil query
// (ListWorkersAdminByStatus): no deleted_at filter (status=3 surfaces
// soft-deleted rows), no user filter.
func listWorkersAdminByStatusParams(status leapmuxv1.WorkerStatus, cursor string, limit int64) (gendb.ListWorkersAdminByStatusParams, error) {
	return withCursor(cursor, limit, func(ct any, cid sql.NullString, fl int64) gendb.ListWorkersAdminByStatusParams {
		return gendb.ListWorkersAdminByStatusParams{Status: status, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

// listWorkersAdminByUserAndStatusParams builds the status=set, user_id=set
// query (ListWorkersAdminByUserAndStatus): required registered_by + status,
// riding the (registered_by, status, created_at, id) composite seek.
func listWorkersAdminByUserAndStatusParams(status leapmuxv1.WorkerStatus, userID string, cursor string, limit int64) (gendb.ListWorkersAdminByUserAndStatusParams, error) {
	return withCursor(cursor, limit, func(ct any, cid sql.NullString, fl int64) gendb.ListWorkersAdminByUserAndStatusParams {
		return gendb.ListWorkersAdminByUserAndStatusParams{Status: status, UserID: userID, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllActiveSessionsParams(cursor string, limit int64) (gendb.ListAllActiveSessionsParams, error) {
	return withCursor(cursor, limit, func(ct any, cid sql.NullString, fl int64) gendb.ListAllActiveSessionsParams {
		return gendb.ListAllActiveSessionsParams{CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listUserSessionsParams(userID, cursor string, limit int64) (gendb.ListUserSessionsByUserIDParams, error) {
	return withCursor(cursor, limit, func(ct any, cid sql.NullString, fl int64) gendb.ListUserSessionsByUserIDParams {
		return gendb.ListUserSessionsByUserIDParams{UserID: userID, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllAPITokensParams(clientType, cursor string, limit int64) (gendb.ListAllAPITokensParams, error) {
	return withCursor(cursor, limit, func(ct any, cid sql.NullString, fl int64) gendb.ListAllAPITokensParams {
		return gendb.ListAllAPITokensParams{ClientType: clientType, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllAPITokensByUserParams(userID, clientType, cursor string, limit int64) (gendb.ListAllAPITokensByUserParams, error) {
	return withCursor(cursor, limit, func(ct any, cid sql.NullString, fl int64) gendb.ListAllAPITokensByUserParams {
		return gendb.ListAllAPITokensByUserParams{UserID: userID, ClientType: clientType, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllAPITokensIncludingRevokedParams(clientType, cursor string, limit int64) (gendb.ListAllAPITokensIncludingRevokedParams, error) {
	return withCursor(cursor, limit, func(ct any, cid sql.NullString, fl int64) gendb.ListAllAPITokensIncludingRevokedParams {
		return gendb.ListAllAPITokensIncludingRevokedParams{ClientType: clientType, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllAPITokensByUserIncludingRevokedParams(userID, clientType, cursor string, limit int64) (gendb.ListAllAPITokensByUserIncludingRevokedParams, error) {
	return withCursor(cursor, limit, func(ct any, cid sql.NullString, fl int64) gendb.ListAllAPITokensByUserIncludingRevokedParams {
		return gendb.ListAllAPITokensByUserIncludingRevokedParams{UserID: userID, ClientType: clientType, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllDelegationTokensParams(cursor string, limit int64) (gendb.ListAllDelegationTokensParams, error) {
	return withCursor(cursor, limit, func(ct any, cid sql.NullString, fl int64) gendb.ListAllDelegationTokensParams {
		return gendb.ListAllDelegationTokensParams{CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllDelegationTokensByUserParams(userID, cursor string, limit int64) (gendb.ListAllDelegationTokensByUserParams, error) {
	return withCursor(cursor, limit, func(ct any, cid sql.NullString, fl int64) gendb.ListAllDelegationTokensByUserParams {
		return gendb.ListAllDelegationTokensByUserParams{UserID: userID, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllDelegationTokensIncludingRevokedParams(cursor string, limit int64) (gendb.ListAllDelegationTokensIncludingRevokedParams, error) {
	return withCursor(cursor, limit, func(ct any, cid sql.NullString, fl int64) gendb.ListAllDelegationTokensIncludingRevokedParams {
		return gendb.ListAllDelegationTokensIncludingRevokedParams{CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllDelegationTokensByUserIncludingRevokedParams(userID, cursor string, limit int64) (gendb.ListAllDelegationTokensByUserIncludingRevokedParams, error) {
	return withCursor(cursor, limit, func(ct any, cid sql.NullString, fl int64) gendb.ListAllDelegationTokensByUserIncludingRevokedParams {
		return gendb.ListAllDelegationTokensByUserIncludingRevokedParams{UserID: userID, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

// listRegistrationKeysAdminParams mirrors the other list builders so the
// registration-key admin listing shares the cursor decode and limit clamp.
// now is the caller-computed expiry probe (nil when expired rows are
// included), passed in so the builder stays a pure function of its arguments.
func listRegistrationKeysAdminParams(cursor string, limit int64, now any) (gendb.ListRegistrationKeysAdminParams, error) {
	return withCursor(cursor, limit, func(ct any, cid sql.NullString, fl int64) gendb.ListRegistrationKeysAdminParams {
		return gendb.ListRegistrationKeysAdminParams{Now: now, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func rowsAffected(result sql.Result, err error) (int64, error) {
	return sqlutil.RowsAffected(result, err, mapErr)
}
