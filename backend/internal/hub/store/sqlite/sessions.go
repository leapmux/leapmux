package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type sessionStore struct{ conn *sqliteConn }

var _ store.SessionStore = (*sessionStore)(nil)

func fromDBSession(s gendb.UserSession) store.UserSession {
	return store.UserSession{
		ID:                 s.ID,
		UserID:             s.UserID,
		ExpiresAt:          s.ExpiresAt.Time,
		CreatedAt:          s.CreatedAt.Time,
		LastActiveAt:       s.LastActiveAt.Time,
		AuthGeneration:     s.AuthGeneration,
		ElevationProvenAt:  s.ElevationProvenAt.Ptr(),
		ElevationExpiresAt: s.ElevationExpiresAt.Ptr(),
		UserAgent:          s.UserAgent,
		IPAddress:          s.IpAddress,
	}
}

func fromDBActiveSessionRow(r gendb.ListAllActiveSessionsRow) store.ActiveSession {
	return store.ActiveSession{
		ID:           r.ID,
		UserID:       r.UserID,
		Username:     r.Username,
		UserDeleted:  r.UserDeleted,
		CreatedAt:    r.CreatedAt.Time,
		LastActiveAt: r.LastActiveAt.Time,
		ExpiresAt:    r.ExpiresAt.Time,
		IPAddress:    r.IpAddress,
		UserAgent:    r.UserAgent,
	}
}

func (s *sessionStore) Create(ctx context.Context, p store.CreateSessionParams) error {
	return (&sqliteStore{conn: s.conn}).RunInUserAuthTransaction(ctx, p.UserID, func(tx store.Store) error {
		return mapErr(tx.(*sqliteStore).conn.q.CreateUserSession(ctx, gendb.CreateUserSessionParams{
			ID:        p.ID,
			UserID:    p.UserID.String(),
			ExpiresAt: sqltime.NewSQLiteTime(p.ExpiresAt),
			UserAgent: p.UserAgent,
			IpAddress: p.IPAddress,
		}))
	})
}

func (s *sessionStore) GetByID(ctx context.Context, id string, now time.Time) (*store.UserSession, error) {
	sess, err := s.conn.q.GetUserSessionByID(ctx, gendb.GetUserSessionByIDParams{
		ID:  id,
		Now: sqltime.NewSQLiteTime(now),
	})
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBSession(sess)
	return &out, nil
}

func (s *sessionStore) Touch(ctx context.Context, p store.TouchSessionParams, now time.Time) (int64, error) {
	// sqlite Touch is an inline query rather than a generated one (unlike
	// postgres/mysql). last_active_at is written SQL-side by strftime('now'); the
	// expires_at write and the last_active_at WHERE comparand are bound as
	// SQLiteTime values, whose Value() emits the same canonical strftime layout
	// the read-side liveness/cursor filters in user_sessions.sql compare as raw
	// strings. See the ListAllActiveSessions comment there for the full storage
	// invariant and the modernc driver-layout hazard that makes binding a raw
	// time.Time unsafe -- the SQLiteTime type mechanizes the canonical layout so
	// a raw bind is a compile error.
	res, err := s.conn.exec.ExecContext(ctx,
		`UPDATE user_sessions
		 SET last_active_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
		     expires_at = ?
		 WHERE id = ? AND last_active_at < ?
		   AND expires_at > ?`,
		sqltime.NewSQLiteTime(p.ExpiresAt), p.ID, sqltime.NewSQLiteTime(p.LastActiveAt), sqltime.NewSQLiteTime(now),
	)
	if err != nil {
		return 0, mapErr(err)
	}
	n, err := res.RowsAffected()
	return n, mapErr(err)
}

func (s *sessionStore) Delete(ctx context.Context, id string) (int64, error) {
	return s.deleteEmitting(ctx, id, store.RevocationEventKindSession)
}

func (s *sessionStore) Revoke(ctx context.Context, id string) (int64, error) {
	return s.deleteEmitting(ctx, id, store.RevocationEventKindSessionRevoked)
}

// deleteEmitting removes one session row and records who ended it. The two
// callers differ in the event kind ALONE, so the delete stays one
// implementation and cannot drift between a sign-out and a revoke.
func (s *sessionStore) deleteEmitting(ctx context.Context, id, kind string) (int64, error) {
	return store.RunCredentialMutation(ctx, s.conn.withTransaction, func(ctx context.Context, conn *sqliteConn) (*store.CredentialEvent, error) {
		row, err := conn.q.DeleteUserSession(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, mapErr(err)
		}
		return &store.CredentialEvent{Kind: kind, SubjectID: row.ID, UserID: row.UserID, At: time.Now().UTC()}, nil
	}, emitCredentialEvent)
}

func (s *sessionStore) DeleteByUser(ctx context.Context, userID userid.UserID) error {
	owner, ok := userid.OwnerFilter(userID)
	if !ok {
		// An unminted caller names no user, so a bulk mutation must refuse
		// rather than address every blank-owner row -- or report success
		// having changed nothing. See userid.OwnerFilter.
		return store.ErrInvalidArgument
	}
	return mapErr(s.conn.q.DeleteUserSessionsByUser(ctx, owner))
}

func (s *sessionStore) DeleteOthers(ctx context.Context, p store.DeleteOtherSessionsParams) error {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. This method reports only an error,
		// so returning nil would tell the caller the mutation SUCCEEDED while
		// addressing no row -- the shape a revocation must never have. See
		// userid.OwnerFilter.
		return store.ErrInvalidArgument
	}
	return mapErr(s.conn.q.DeleteOtherUserSessions(ctx, gendb.DeleteOtherUserSessionsParams{
		UserID: owner,
		ID:     p.KeepID,
	}))
}

func (s *sessionStore) RefreshAuthGeneration(ctx context.Context, p store.RefreshSessionAuthGenerationParams) (int64, error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return 0, nil
	}
	return rowsAffected(s.conn.q.RefreshUserSessionAuthGeneration(ctx, gendb.RefreshUserSessionAuthGenerationParams{
		SessionID: p.SessionID,
		UserID:    owner,
	}))
}

func (s *sessionStore) ListByUserID(ctx context.Context, p store.ListUserSessionsParams, now time.Time) (store.Page[store.UserSession], error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return store.Page[store.UserSession]{}, nil
	}
	return queryPage(ctx, p.Limit,
		func() (gendb.ListUserSessionsByUserIDParams, error) {
			return listUserSessionsParams(owner, p.Cursor, p.Limit, now)
		},
		s.conn.q.ListUserSessionsByUserID, fromDBSession)
}

func (s *sessionStore) ListAllActive(ctx context.Context, p store.ListAllActiveSessionsParams, now time.Time) (store.Page[store.ActiveSession], error) {
	return queryPage(ctx, p.Limit,
		func() (gendb.ListAllActiveSessionsParams, error) {
			return listAllActiveSessionsParams(p.Cursor, p.Limit, now)
		},
		s.conn.q.ListAllActiveSessions, fromDBActiveSessionRow)
}

func (s *sessionStore) ValidateWithUser(ctx context.Context, id string, now time.Time) (*store.SessionWithUser, error) {
	row, err := s.conn.q.ValidateSessionWithUser(ctx, gendb.ValidateSessionWithUserParams{
		ID:  id,
		Now: sqltime.NewSQLiteTime(now),
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return &store.SessionWithUser{
		UserID:             row.ID,
		Username:           row.Username,
		IsAdmin:            ptrconv.Int64ToBool(row.IsAdmin),
		EmailVerified:      ptrconv.Int64ToBool(row.EmailVerified),
		Email:              row.Email,
		CreatedAt:          row.CreatedAt.UTC(),
		ExpiresAt:          row.ExpiresAt.UTC(),
		AuthGeneration:     row.AuthGeneration,
		ElevationProvenAt:  row.ElevationProvenAt.Ptr(),
		ElevationExpiresAt: row.ElevationExpiresAt.Ptr(),
	}, nil
}

// Elevate, SlideElevation and DropElevation are the only writers of
// elevation_proven_at / elevation_expires_at. Elevate and DropElevation emit a user_info
// event so a cross-process UserInfo cache re-reads the deadline; the slide
// does not, because a stale SHORTER deadline fails closed.
func (s *sessionStore) Elevate(ctx context.Context, p store.ElevateSessionParams, now time.Time) (int64, error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return 0, store.ErrInvalidArgument
	}
	return store.RunCredentialMutation(ctx, s.conn.withTransaction, func(ctx context.Context, conn *sqliteConn) (*store.CredentialEvent, error) {
		n, err := rowsAffected(conn.q.ElevateUserSession(ctx, gendb.ElevateUserSessionParams{
			ElevationProvenAt:  sqltime.SQLiteNullTimeOf(p.ElevationProvenAt),
			ElevationExpiresAt: sqltime.SQLiteNullTimeOf(p.ElevationExpiresAt),
			ID:                 p.SessionID,
			UserID:             owner,
			Now:                sqltime.NewSQLiteTime(now),
		}))
		if err != nil || n == 0 {
			return nil, err
		}
		return store.UserInfoEvent(owner, now.UTC()), nil
	}, emitCredentialEvent)
}

func (s *sessionStore) SlideElevation(ctx context.Context, p store.SlideSessionElevationParams, now time.Time) (int64, error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		return 0, store.ErrInvalidArgument
	}
	// WindowDeadline is an untyped sqlc parameter (it sits inside min(), which
	// carries no column type), so the canonical layout is this bind's
	// responsibility: a raw time.Time would store modernc's driver layout and
	// break every raw-string comparison on elevation_expires_at. See the
	// ListAllActiveSessions comment in user_sessions.sql, and the fixture in
	// TestAllDatetimeColumnsStoreCanonicalLayout that fails on a raw bind.
	return rowsAffected(s.conn.q.SlideUserSessionElevation(ctx, gendb.SlideUserSessionElevationParams{
		WindowDeadline: sqltime.NewSQLiteTime(p.WindowDeadline),
		MaxTotalMicros: p.MaxTotal.Microseconds(),
		ID:             p.SessionID,
		UserID:         owner,
		Now:            sqltime.SQLiteNullTimeOf(now),
	}))
}

func (s *sessionStore) DropElevation(ctx context.Context, p store.DropSessionElevationParams, now time.Time) (int64, error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		return 0, store.ErrInvalidArgument
	}
	return store.RunCredentialMutation(ctx, s.conn.withTransaction, func(ctx context.Context, conn *sqliteConn) (*store.CredentialEvent, error) {
		n, err := rowsAffected(conn.q.DropUserSessionElevation(ctx, gendb.DropUserSessionElevationParams{
			ID:     p.SessionID,
			UserID: owner,
		}))
		if err != nil || n == 0 {
			return nil, err
		}
		return store.UserInfoEvent(owner, now.UTC()), nil
	}, emitCredentialEvent)
}
