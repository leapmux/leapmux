package sqlite

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

type sessionStore struct{ conn *sqliteConn }

var _ store.SessionStore = (*sessionStore)(nil)

func fromDBSession(s gendb.UserSession) store.UserSession {
	return store.UserSession{
		ID:           s.ID,
		UserID:       s.UserID,
		ExpiresAt:    s.ExpiresAt,
		CreatedAt:    s.CreatedAt,
		LastActiveAt: s.LastActiveAt,
		UserAgent:    s.UserAgent,
		IPAddress:    s.IpAddress,
	}
}

func fromDBSessions(rows []gendb.UserSession) []store.UserSession {
	return store.MapSlice(rows, fromDBSession)
}

func (s *sessionStore) Create(ctx context.Context, p store.CreateSessionParams) error {
	return mapErr(s.conn.q.CreateUserSession(ctx, gendb.CreateUserSessionParams{
		ID:        p.ID,
		UserID:    p.UserID,
		ExpiresAt: p.ExpiresAt.UTC(),
		UserAgent: p.UserAgent,
		IpAddress: p.IPAddress,
	}))
}

func (s *sessionStore) GetByID(ctx context.Context, id string) (*store.UserSession, error) {
	sess, err := s.conn.q.GetUserSessionByID(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBSession(sess)
	return &out, nil
}

func (s *sessionStore) Touch(ctx context.Context, p store.TouchSessionParams) error {
	// Format LastActiveAt in the same ISO 8601 format as strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
	// to ensure correct string comparison with the DB-stored value.
	lastActiveStr := p.LastActiveAt.UTC().Format(sqliteTimeFormat)
	_, err := s.conn.shared.db.ExecContext(ctx,
		`UPDATE user_sessions
		 SET last_active_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
		     expires_at = ?
		 WHERE id = ? AND last_active_at < ?`,
		p.ExpiresAt.UTC(), p.ID, lastActiveStr,
	)
	return mapErr(err)
}

func (s *sessionStore) Delete(ctx context.Context, id string) (int64, error) {
	return rowsAffected(s.conn.q.DeleteUserSession(ctx, id))
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

func (s *sessionStore) ListByUserID(ctx context.Context, userID string) ([]store.UserSession, error) {
	rows, err := s.conn.q.ListUserSessionsByUserID(ctx, userID)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBSessions(rows), nil
}

func (s *sessionStore) ListAllActive(ctx context.Context, p store.ListAllActiveSessionsParams) ([]store.ActiveSession, error) {
	params, err := listAllActiveSessionsParams(p.Cursor, p.Limit)
	if err != nil {
		return nil, err
	}
	rows, err := s.conn.q.ListAllActiveSessions(ctx, params)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]store.ActiveSession, len(rows))
	for i, r := range rows {
		out[i] = store.ActiveSession{
			ID:           r.ID,
			UserID:       r.UserID,
			Username:     r.Username,
			CreatedAt:    r.CreatedAt,
			LastActiveAt: r.LastActiveAt,
			ExpiresAt:    r.ExpiresAt,
			IPAddress:    r.IpAddress,
			UserAgent:    r.UserAgent,
		}
	}
	return out, nil
}

func (s *sessionStore) ValidateWithUser(ctx context.Context, id string) (*store.SessionWithUser, error) {
	row, err := s.conn.q.ValidateSessionWithUser(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	return &store.SessionWithUser{
		UserID:        row.ID,
		OrgID:         row.OrgID,
		Username:      row.Username,
		IsAdmin:       ptrconv.Int64ToBool(row.IsAdmin),
		EmailVerified: ptrconv.Int64ToBool(row.EmailVerified),
		Email:         row.Email,
	}, nil
}
