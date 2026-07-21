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
)

type sessionStore struct{ conn *sqliteConn }

var _ store.SessionStore = (*sessionStore)(nil)

func fromDBSession(s gendb.UserSession) store.UserSession {
	return store.UserSession{
		ID:             s.ID,
		UserID:         s.UserID,
		ExpiresAt:      s.ExpiresAt.Time,
		CreatedAt:      s.CreatedAt.Time,
		LastActiveAt:   s.LastActiveAt.Time,
		AuthGeneration: s.AuthGeneration,
		UserAgent:      s.UserAgent,
		IPAddress:      s.IpAddress,
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
			UserID:    p.UserID,
			ExpiresAt: sqltime.NewSQLiteTime(p.ExpiresAt),
			UserAgent: p.UserAgent,
			IpAddress: p.IPAddress,
		}))
	})
}

func (s *sessionStore) GetByID(ctx context.Context, id string) (*store.UserSession, error) {
	sess, err := s.conn.q.GetUserSessionByID(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBSession(sess)
	return &out, nil
}

func (s *sessionStore) Touch(ctx context.Context, p store.TouchSessionParams) (int64, error) {
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
		 WHERE id = ? AND last_active_at < ?`,
		sqltime.NewSQLiteTime(p.ExpiresAt), p.ID, sqltime.NewSQLiteTime(p.LastActiveAt),
	)
	if err != nil {
		return 0, mapErr(err)
	}
	n, err := res.RowsAffected()
	return n, mapErr(err)
}

func (s *sessionStore) Delete(ctx context.Context, id string) (int64, error) {
	return store.RunCredentialMutation(ctx, s.conn.withTransaction, func(ctx context.Context, conn *sqliteConn) (*store.CredentialEvent, error) {
		row, err := conn.q.DeleteUserSession(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, mapErr(err)
		}
		return &store.CredentialEvent{Kind: store.RevocationEventKindSession, SubjectID: row.ID, UserID: row.UserID, At: time.Now().UTC()}, nil
	}, emitCredentialEvent)
}

func (s *sessionStore) DeleteByUser(ctx context.Context, userID string) error {
	return mapErr(s.conn.q.DeleteUserSessionsByUser(ctx, userID))
}

func (s *sessionStore) DeleteOthers(ctx context.Context, p store.DeleteOtherSessionsParams) error {
	return mapErr(s.conn.q.DeleteOtherUserSessions(ctx, gendb.DeleteOtherUserSessionsParams{
		UserID: p.UserID,
		ID:     p.KeepID,
	}))
}

func (s *sessionStore) RefreshAuthGeneration(ctx context.Context, p store.RefreshSessionAuthGenerationParams) (int64, error) {
	return rowsAffected(s.conn.q.RefreshUserSessionAuthGeneration(ctx, gendb.RefreshUserSessionAuthGenerationParams{
		SessionID: p.SessionID,
		UserID:    p.UserID,
	}))
}

func (s *sessionStore) ListByUserID(ctx context.Context, p store.ListUserSessionsParams) (store.Page[store.UserSession], error) {
	return queryPage(ctx, p.Limit,
		func() (gendb.ListUserSessionsByUserIDParams, error) {
			return listUserSessionsParams(p.UserID, p.Cursor, p.Limit)
		},
		s.conn.q.ListUserSessionsByUserID, fromDBSession)
}

func (s *sessionStore) ListAllActive(ctx context.Context, p store.ListAllActiveSessionsParams) (store.Page[store.ActiveSession], error) {
	return queryPage(ctx, p.Limit,
		func() (gendb.ListAllActiveSessionsParams, error) {
			return listAllActiveSessionsParams(p.Cursor, p.Limit)
		},
		s.conn.q.ListAllActiveSessions, fromDBActiveSessionRow)
}

func (s *sessionStore) ValidateWithUser(ctx context.Context, id string) (*store.SessionWithUser, error) {
	row, err := s.conn.q.ValidateSessionWithUser(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	return &store.SessionWithUser{
		UserID:         row.ID,
		OrgID:          row.OrgID,
		Username:       row.Username,
		IsAdmin:        ptrconv.Int64ToBool(row.IsAdmin),
		EmailVerified:  ptrconv.Int64ToBool(row.EmailVerified),
		Email:          row.Email,
		CreatedAt:      row.CreatedAt.UTC(),
		ExpiresAt:      row.ExpiresAt.UTC(),
		AuthGeneration: row.AuthGeneration,
	}, nil
}
