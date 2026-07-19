package postgres

import (
	"context"
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

// decodeCursorParams decodes the composite list cursor into the two nullable sqlc
// parameters the keyset queries bind: the cursor timestamp and the id
// tiebreaker. An empty cursor (first page) returns the zero values so the
// queries' "cursor_time IS NULL" branch selects every row.
//
// The timestamp is truncated to microsecond precision to match the timestamptz
// columns: pgx sends pgtype.Timestamptz over the binary protocol floored to
// microseconds, but a hand-crafted cursor parsed from text could carry
// sub-microsecond digits, and a future switch to text binding would let the
// `::timestamptz` cast round (half-to-even) at the .5us boundary -- making the
// `=` tiebreak branch unsatisfiable and re-returning the previous page's tail
// as duplicates. Truncating in Go mirrors MySQL's defensive
// Truncate(time.Millisecond) and makes the "=" branch mechanically satisfiable
// regardless of how the value is transported. EncodeCursor produces cursors
// from DB-scanned rows (already microsecond-quantized), so this is a no-op for
// real cursors.
func decodeCursorParams(cursor string) (pgtype.Timestamptz, pgtype.Text, error) {
	c, err := store.ParseCursor(cursor)
	if err != nil {
		return pgtype.Timestamptz{}, pgtype.Text{}, err
	}
	if c == nil {
		return pgtype.Timestamptz{}, pgtype.Text{}, nil
	}
	return pgtype.Timestamptz{Time: c.Time.Truncate(time.Microsecond), Valid: true}, pgtype.Text{String: c.ID, Valid: true}, nil
}

// withCursor decodes the composite list cursor and applies the dialect's
// fetch-limit clamp, then hands the decoded values to fill, which assembles
// the dialect-specific sqlc params struct. Centralizing decodeCursorParams + the
// error short-circuit + store.FetchLimit (with the int32 cast the Postgres
// LIMIT column requires) here means a change to the cursor decode, the clamp
// rule, or the probe-row accounting edits ONE site per dialect instead of
// being copy-pasted across every list builder.
func withCursor[T any](cursor string, limit int64, fill func(cursorTime pgtype.Timestamptz, cursorID pgtype.Text, fetchLimit int32) T) (T, error) {
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
	return withCursor(cursor, limit, func(ct pgtype.Timestamptz, cid pgtype.Text, fl int32) gendb.ListAllUsersParams {
		return gendb.ListAllUsersParams{CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func searchUsersParams(query *string, cursor string, limit int64) (gendb.SearchUsersParams, error) {
	return withCursor(cursor, limit, func(ct pgtype.Timestamptz, cid pgtype.Text, fl int32) gendb.SearchUsersParams {
		// Build the complete LIKE prefix pattern (fold + metachar escape + trailing
		// '%') at the one shared site, so the match is case-insensitive and literal
		// cross-dialect. A nil query stays nil (SearchUsers reads it as "no filter").
		return gendb.SearchUsersParams{
			Query:      ptrToText(store.SearchLikePattern(query)),
			CursorTime: ct,
			CursorID:   cid,
			Limit:      fl,
		}
	})
}

func listWorkersByUserIDParams(registeredBy, cursor string, limit int64) (gendb.ListWorkersByUserIDParams, error) {
	return withCursor(cursor, limit, func(ct pgtype.Timestamptz, cid pgtype.Text, fl int32) gendb.ListWorkersByUserIDParams {
		return gendb.ListWorkersByUserIDParams{RegisteredBy: registeredBy, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

// listWorkersAdminParams builds the status=nil, user_id=nil query
// (ListWorkersAdmin): deleted_at IS NULL, no user filter.
func listWorkersAdminParams(cursor string, limit int64) (gendb.ListWorkersAdminParams, error) {
	return withCursor(cursor, limit, func(ct pgtype.Timestamptz, cid pgtype.Text, fl int32) gendb.ListWorkersAdminParams {
		return gendb.ListWorkersAdminParams{CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

// listWorkersAdminByUserParams builds the status=nil, user_id=set query
// (ListWorkersAdminByUser): deleted_at IS NULL + required registered_by. The
// user_id dimension is its own query rather than an opt-in `(narg IS NULL OR
// registered_by = narg)` probe: that probe made sqlc emit UserID as an untyped
// interface{}, and binding NULL raised SQLSTATE 42P08 (YugabyteDB inherits the
// break via this store). See workers.sql for the full 2x2 matrix rationale.
func listWorkersAdminByUserParams(userID string, cursor string, limit int64) (gendb.ListWorkersAdminByUserParams, error) {
	return withCursor(cursor, limit, func(ct pgtype.Timestamptz, cid pgtype.Text, fl int32) gendb.ListWorkersAdminByUserParams {
		return gendb.ListWorkersAdminByUserParams{UserID: userID, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

// listWorkersAdminByStatusParams builds the status=set, user_id=nil query
// (ListWorkersAdminByStatus): no deleted_at filter (status=3 surfaces
// soft-deleted rows), no user filter.
func listWorkersAdminByStatusParams(status leapmuxv1.WorkerStatus, cursor string, limit int64) (gendb.ListWorkersAdminByStatusParams, error) {
	return withCursor(cursor, limit, func(ct pgtype.Timestamptz, cid pgtype.Text, fl int32) gendb.ListWorkersAdminByStatusParams {
		return gendb.ListWorkersAdminByStatusParams{Status: status, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

// listWorkersAdminByUserAndStatusParams builds the status=set, user_id=set
// query (ListWorkersAdminByUserAndStatus): required registered_by + status.
func listWorkersAdminByUserAndStatusParams(status leapmuxv1.WorkerStatus, userID string, cursor string, limit int64) (gendb.ListWorkersAdminByUserAndStatusParams, error) {
	return withCursor(cursor, limit, func(ct pgtype.Timestamptz, cid pgtype.Text, fl int32) gendb.ListWorkersAdminByUserAndStatusParams {
		return gendb.ListWorkersAdminByUserAndStatusParams{Status: status, UserID: userID, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllActiveSessionsParams(cursor string, limit int64) (gendb.ListAllActiveSessionsParams, error) {
	return withCursor(cursor, limit, func(ct pgtype.Timestamptz, cid pgtype.Text, fl int32) gendb.ListAllActiveSessionsParams {
		return gendb.ListAllActiveSessionsParams{CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listUserSessionsParams(userID, cursor string, limit int64) (gendb.ListUserSessionsByUserIDParams, error) {
	return withCursor(cursor, limit, func(ct pgtype.Timestamptz, cid pgtype.Text, fl int32) gendb.ListUserSessionsByUserIDParams {
		return gendb.ListUserSessionsByUserIDParams{UserID: userID, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllAPITokensParams(clientType, cursor string, limit int64) (gendb.ListAllAPITokensParams, error) {
	return withCursor(cursor, limit, func(ct pgtype.Timestamptz, cid pgtype.Text, fl int32) gendb.ListAllAPITokensParams {
		return gendb.ListAllAPITokensParams{ClientType: clientType, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllAPITokensByUserParams(userID, clientType, cursor string, limit int64) (gendb.ListAllAPITokensByUserParams, error) {
	return withCursor(cursor, limit, func(ct pgtype.Timestamptz, cid pgtype.Text, fl int32) gendb.ListAllAPITokensByUserParams {
		return gendb.ListAllAPITokensByUserParams{UserID: userID, ClientType: clientType, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllAPITokensIncludingRevokedParams(clientType, cursor string, limit int64) (gendb.ListAllAPITokensIncludingRevokedParams, error) {
	return withCursor(cursor, limit, func(ct pgtype.Timestamptz, cid pgtype.Text, fl int32) gendb.ListAllAPITokensIncludingRevokedParams {
		return gendb.ListAllAPITokensIncludingRevokedParams{ClientType: clientType, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllAPITokensByUserIncludingRevokedParams(userID, clientType, cursor string, limit int64) (gendb.ListAllAPITokensByUserIncludingRevokedParams, error) {
	return withCursor(cursor, limit, func(ct pgtype.Timestamptz, cid pgtype.Text, fl int32) gendb.ListAllAPITokensByUserIncludingRevokedParams {
		return gendb.ListAllAPITokensByUserIncludingRevokedParams{UserID: userID, ClientType: clientType, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllDelegationTokensParams(cursor string, limit int64) (gendb.ListAllDelegationTokensParams, error) {
	return withCursor(cursor, limit, func(ct pgtype.Timestamptz, cid pgtype.Text, fl int32) gendb.ListAllDelegationTokensParams {
		return gendb.ListAllDelegationTokensParams{CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllDelegationTokensByUserParams(userID, cursor string, limit int64) (gendb.ListAllDelegationTokensByUserParams, error) {
	return withCursor(cursor, limit, func(ct pgtype.Timestamptz, cid pgtype.Text, fl int32) gendb.ListAllDelegationTokensByUserParams {
		return gendb.ListAllDelegationTokensByUserParams{UserID: userID, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllDelegationTokensIncludingRevokedParams(cursor string, limit int64) (gendb.ListAllDelegationTokensIncludingRevokedParams, error) {
	return withCursor(cursor, limit, func(ct pgtype.Timestamptz, cid pgtype.Text, fl int32) gendb.ListAllDelegationTokensIncludingRevokedParams {
		return gendb.ListAllDelegationTokensIncludingRevokedParams{CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

func listAllDelegationTokensByUserIncludingRevokedParams(userID, cursor string, limit int64) (gendb.ListAllDelegationTokensByUserIncludingRevokedParams, error) {
	return withCursor(cursor, limit, func(ct pgtype.Timestamptz, cid pgtype.Text, fl int32) gendb.ListAllDelegationTokensByUserIncludingRevokedParams {
		return gendb.ListAllDelegationTokensByUserIncludingRevokedParams{UserID: userID, CursorTime: ct, CursorID: cid, Limit: fl}
	})
}

// listRegistrationKeysAdminParams mirrors the other list builders so the
// registration-key admin listing shares the cursor decode and limit clamp.
// now is the caller-computed expiry probe (Valid=false when expired rows are
// included), passed in so the builder stays a pure function of its arguments.
func listRegistrationKeysAdminParams(cursor string, limit int64, now pgtype.Timestamptz) (gendb.ListRegistrationKeysAdminParams, error) {
	return withCursor(cursor, limit, func(ct pgtype.Timestamptz, cid pgtype.Text, fl int32) gendb.ListRegistrationKeysAdminParams {
		return gendb.ListRegistrationKeysAdminParams{Now: now, CursorTime: ct, CursorID: cid, Limit: fl}
	})
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
