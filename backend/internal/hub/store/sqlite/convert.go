package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	sqlite3 "modernc.org/sqlite"
	sqlitelib "modernc.org/sqlite/lib"
)

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
// parameters SQLite's keyset queries bind: the cursor timestamp and the id
// tiebreaker. An empty cursor (first page) returns the zero values so the
// queries' "cursor_time IS NULL" branch selects every row (an invalid
// SQLiteNullTime binds NULL).
//
// The cursor timestamp stays sqltime.SQLiteNullTime -- its Value() emits the
// same canonical 24-char bytes SQLite stores on disk, so the raw-string keyset
// compare and IS-NULL first-page branch are byte-exact, including at
// trailing-zero milliseconds (pinned by TestKeysetCursorTrailingZeroMillisecondTie).
// The generated CursorTime params stay interface{}: the sqlite engine does not
// resolve the `narg IS NULL OR col < narg` OR-chain to a column type, so it
// cannot type these params. Binding a Valuer through the interface still routes
// through Value(), so the floor and canonical layout stay mechanical; only
// compile-time typing is missing here.
func decodeCursorParams(cursor string) (cursorTime sqltime.SQLiteNullTime, cursorID sql.NullString, err error) {
	c, err := store.ParseCursor(cursor)
	if err != nil {
		return sqltime.SQLiteNullTime{}, sql.NullString{}, err
	}
	if c == nil {
		return sqltime.SQLiteNullTime{}, sql.NullString{}, nil
	}
	return sqltime.SQLiteNullTimeOf(c.Time), sql.NullString{String: c.ID, Valid: true}, nil
}

// withCursor decodes the composite list cursor and applies the dialect's
// fetch-limit clamp, then hands the decoded values to fill, which assembles
// the dialect-specific sqlc params struct. Centralizing decodeCursorParams + the
// error short-circuit + store.FetchLimit here means a change to the cursor
// decode, the clamp rule, or the probe-row accounting edits ONE site per
// dialect instead of a copy in every list builder -- the prior
// shape let ListRegistrationKeysAdmin silently bypass ClampListLimit until this
// commit's pagination rebuild caught it.
func withCursor[T any](cursor string, limit int64, fill func(cursorTime sqltime.SQLiteNullTime, cursorID sql.NullString, fetchLimit int64) T) (T, error) {
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
	return withCursor(cursor, limit, func(ct sqltime.SQLiteNullTime, cid sql.NullString, fl int64) gendb.ListAllUsersParams {
		return gendb.ListAllUsersParams{CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func searchUsersParams(query *string, cursor string, limit int64) (gendb.SearchUsersParams, error) {
	return withCursor(cursor, limit, func(ct sqltime.SQLiteNullTime, cid sql.NullString, fl int64) gendb.SearchUsersParams {
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
	return withCursor(cursor, limit, func(ct sqltime.SQLiteNullTime, cid sql.NullString, fl int64) gendb.ListWorkersByUserIDParams {
		return gendb.ListWorkersByUserIDParams{RegisteredBy: registeredBy, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

// listWorkersAdminLiveParams builds the typed cursor for ListWorkersAdminLive.
func listWorkersAdminLiveParams(cursor string, limit int64) (gendb.ListWorkersAdminLiveParams, error) {
	return withCursor(cursor, limit, func(ct sqltime.SQLiteNullTime, cid sql.NullString, fl int64) gendb.ListWorkersAdminLiveParams {
		return gendb.ListWorkersAdminLiveParams{CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

// listWorkersAdminLiveByUserParams builds the typed cursor for ListWorkersAdminLiveByUser.
func listWorkersAdminLiveByUserParams(userID string, cursor string, limit int64) (gendb.ListWorkersAdminLiveByUserParams, error) {
	return withCursor(cursor, limit, func(ct sqltime.SQLiteNullTime, cid sql.NullString, fl int64) gendb.ListWorkersAdminLiveByUserParams {
		return gendb.ListWorkersAdminLiveByUserParams{UserID: userID, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

// listWorkersAdminActiveParams builds the typed cursor for ListWorkersAdminActive.
func listWorkersAdminActiveParams(cursor string, limit int64) (gendb.ListWorkersAdminActiveParams, error) {
	return withCursor(cursor, limit, func(ct sqltime.SQLiteNullTime, cid sql.NullString, fl int64) gendb.ListWorkersAdminActiveParams {
		return gendb.ListWorkersAdminActiveParams{CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

// listWorkersAdminActiveByUserParams builds the typed cursor for ListWorkersAdminActiveByUser.
func listWorkersAdminActiveByUserParams(userID string, cursor string, limit int64) (gendb.ListWorkersAdminActiveByUserParams, error) {
	return withCursor(cursor, limit, func(ct sqltime.SQLiteNullTime, cid sql.NullString, fl int64) gendb.ListWorkersAdminActiveByUserParams {
		return gendb.ListWorkersAdminActiveByUserParams{UserID: userID, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

// listWorkersAdminDeregisteringParams builds the typed cursor for ListWorkersAdminDeregistering.
func listWorkersAdminDeregisteringParams(cursor string, limit int64) (gendb.ListWorkersAdminDeregisteringParams, error) {
	return withCursor(cursor, limit, func(ct sqltime.SQLiteNullTime, cid sql.NullString, fl int64) gendb.ListWorkersAdminDeregisteringParams {
		return gendb.ListWorkersAdminDeregisteringParams{CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

// listWorkersAdminDeregisteringByUserParams builds the typed cursor for ListWorkersAdminDeregisteringByUser.
func listWorkersAdminDeregisteringByUserParams(userID string, cursor string, limit int64) (gendb.ListWorkersAdminDeregisteringByUserParams, error) {
	return withCursor(cursor, limit, func(ct sqltime.SQLiteNullTime, cid sql.NullString, fl int64) gendb.ListWorkersAdminDeregisteringByUserParams {
		return gendb.ListWorkersAdminDeregisteringByUserParams{UserID: userID, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

// listWorkersAdminDeletedParams builds the typed cursor for ListWorkersAdminDeleted.
func listWorkersAdminDeletedParams(cursor string, limit int64) (gendb.ListWorkersAdminDeletedParams, error) {
	return withCursor(cursor, limit, func(ct sqltime.SQLiteNullTime, cid sql.NullString, fl int64) gendb.ListWorkersAdminDeletedParams {
		return gendb.ListWorkersAdminDeletedParams{CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

// listWorkersAdminDeletedByUserParams builds the typed cursor for ListWorkersAdminDeletedByUser.
func listWorkersAdminDeletedByUserParams(userID string, cursor string, limit int64) (gendb.ListWorkersAdminDeletedByUserParams, error) {
	return withCursor(cursor, limit, func(ct sqltime.SQLiteNullTime, cid sql.NullString, fl int64) gendb.ListWorkersAdminDeletedByUserParams {
		return gendb.ListWorkersAdminDeletedByUserParams{UserID: userID, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllActiveSessionsParams(cursor string, limit int64, now time.Time) (gendb.ListAllActiveSessionsParams, error) {
	nt := sqltime.NewSQLiteTime(now)
	return withCursor(cursor, limit, func(ct sqltime.SQLiteNullTime, cid sql.NullString, fl int64) gendb.ListAllActiveSessionsParams {
		return gendb.ListAllActiveSessionsParams{Now: nt, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listUserSessionsParams(userID, cursor string, limit int64, now time.Time) (gendb.ListUserSessionsByUserIDParams, error) {
	nt := sqltime.NewSQLiteTime(now)
	return withCursor(cursor, limit, func(ct sqltime.SQLiteNullTime, cid sql.NullString, fl int64) gendb.ListUserSessionsByUserIDParams {
		return gendb.ListUserSessionsByUserIDParams{UserID: userID, Now: nt, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllAPITokensParams(clientID, cursor string, limit int64) (gendb.ListAllAPITokensParams, error) {
	return withCursor(cursor, limit, func(ct sqltime.SQLiteNullTime, cid sql.NullString, fl int64) gendb.ListAllAPITokensParams {
		return gendb.ListAllAPITokensParams{ClientID: clientID, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAPITokensByUserParams(userID, clientID, cursor string, limit int64) (gendb.ListAPITokensByUserParams, error) {
	return withCursor(cursor, limit, func(ct sqltime.SQLiteNullTime, cid sql.NullString, fl int64) gendb.ListAPITokensByUserParams {
		return gendb.ListAPITokensByUserParams{UserID: userID, ClientID: clientID, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllAPITokensByUserParams(userID, clientID, cursor string, limit int64) (gendb.ListAllAPITokensByUserParams, error) {
	return withCursor(cursor, limit, func(ct sqltime.SQLiteNullTime, cid sql.NullString, fl int64) gendb.ListAllAPITokensByUserParams {
		return gendb.ListAllAPITokensByUserParams{UserID: userID, ClientID: clientID, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllAPITokensIncludingRevokedParams(clientID, cursor string, limit int64) (gendb.ListAllAPITokensIncludingRevokedParams, error) {
	return withCursor(cursor, limit, func(ct sqltime.SQLiteNullTime, cid sql.NullString, fl int64) gendb.ListAllAPITokensIncludingRevokedParams {
		return gendb.ListAllAPITokensIncludingRevokedParams{ClientID: clientID, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllAPITokensByUserIncludingRevokedParams(userID, clientID, cursor string, limit int64) (gendb.ListAllAPITokensByUserIncludingRevokedParams, error) {
	return withCursor(cursor, limit, func(ct sqltime.SQLiteNullTime, cid sql.NullString, fl int64) gendb.ListAllAPITokensByUserIncludingRevokedParams {
		return gendb.ListAllAPITokensByUserIncludingRevokedParams{UserID: userID, ClientID: clientID, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllDelegationTokensParams(cursor string, limit int64) (gendb.ListAllDelegationTokensParams, error) {
	return withCursor(cursor, limit, func(ct sqltime.SQLiteNullTime, cid sql.NullString, fl int64) gendb.ListAllDelegationTokensParams {
		return gendb.ListAllDelegationTokensParams{CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllDelegationTokensByUserParams(userID, cursor string, limit int64) (gendb.ListAllDelegationTokensByUserParams, error) {
	return withCursor(cursor, limit, func(ct sqltime.SQLiteNullTime, cid sql.NullString, fl int64) gendb.ListAllDelegationTokensByUserParams {
		return gendb.ListAllDelegationTokensByUserParams{UserID: userID, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllDelegationTokensIncludingRevokedParams(cursor string, limit int64) (gendb.ListAllDelegationTokensIncludingRevokedParams, error) {
	return withCursor(cursor, limit, func(ct sqltime.SQLiteNullTime, cid sql.NullString, fl int64) gendb.ListAllDelegationTokensIncludingRevokedParams {
		return gendb.ListAllDelegationTokensIncludingRevokedParams{CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllDelegationTokensByUserIncludingRevokedParams(userID, cursor string, limit int64) (gendb.ListAllDelegationTokensByUserIncludingRevokedParams, error) {
	return withCursor(cursor, limit, func(ct sqltime.SQLiteNullTime, cid sql.NullString, fl int64) gendb.ListAllDelegationTokensByUserIncludingRevokedParams {
		return gendb.ListAllDelegationTokensByUserIncludingRevokedParams{UserID: userID, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

// listRegistrationKeysAdminParams mirrors the other list builders so the
// registration-key admin listing shares the cursor decode and limit clamp.
// now is the caller-computed expiry probe (invalid, binding NULL, when expired
// rows are included), passed in so the builder stays a pure function of its
// arguments. Typed SQLiteNullTime -- the generated Now field is interface{}
// (narg OR-chain), so the typed signature is what keeps a raw time.Time from
// reaching the bind.
func listRegistrationKeysAdminParams(cursor string, limit int64, now sqltime.SQLiteNullTime) (gendb.ListRegistrationKeysAdminParams, error) {
	return withCursor(cursor, limit, func(ct sqltime.SQLiteNullTime, cid sql.NullString, fl int64) gendb.ListRegistrationKeysAdminParams {
		return gendb.ListRegistrationKeysAdminParams{Now: now, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func rowsAffected(result sql.Result, err error) (int64, error) {
	return sqlutil.RowsAffected(result, err, mapErr)
}

func listOAuthClientsParams(userID string, p store.ListOAuthClientsParams) (gendb.ListOAuthClientsParams, error) {
	return withCursor(p.Cursor, p.Limit, func(ct sqltime.SQLiteNullTime, cid sql.NullString, fl int64) gendb.ListOAuthClientsParams {
		return gendb.ListOAuthClientsParams{UserID: sql.NullString{String: userID, Valid: true}, IncludeRevoked: p.IncludeRevoked, IncludeHubWide: p.IncludeHubWide, HubWideOnly: p.HubWideOnly, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}
