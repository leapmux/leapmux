package mysql

import (
	"context"
	"database/sql"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
)

type sessionStore struct {
	q  *gendb.Queries
	db *sql.DB
}

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
	return sqlutil.MapSlice(rows, fromDBSession)
}

func (s *sessionStore) Create(ctx context.Context, p store.CreateSessionParams) error {
	return mapErr(s.q.CreateUserSession(ctx, gendb.CreateUserSessionParams{
		ID:        p.ID,
		UserID:    p.UserID,
		ExpiresAt: p.ExpiresAt,
		UserAgent: p.UserAgent,
		IpAddress: p.IPAddress,
	}))
}

func (s *sessionStore) GetByID(ctx context.Context, id string) (*store.UserSession, error) {
	sess, err := s.q.GetUserSessionByID(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBSession(sess)
	return &out, nil
}

func (s *sessionStore) Touch(ctx context.Context, p store.TouchSessionParams) error {
	return mapErr(s.q.TouchUserSession(ctx, gendb.TouchUserSessionParams{
		ExpiresAt:    p.ExpiresAt,
		ID:           p.ID,
		LastActiveAt: p.LastActiveAt,
	}))
}

func (s *sessionStore) Delete(ctx context.Context, id string) (int64, error) {
	return rowsAffected(s.q.DeleteUserSession(ctx, id))
}

func (s *sessionStore) DeleteByUser(ctx context.Context, userID string) error {
	return mapErr(s.q.DeleteUserSessionsByUser(ctx, userID))
}

func (s *sessionStore) DeleteOthers(ctx context.Context, p store.DeleteOtherSessionsParams) error {
	return mapErr(s.q.DeleteOtherUserSessions(ctx, gendb.DeleteOtherUserSessionsParams{
		UserID: p.UserID,
		ID:     p.KeepID,
	}))
}

func (s *sessionStore) ListByUserID(ctx context.Context, userID string) ([]store.UserSession, error) {
	rows, err := s.q.ListUserSessionsByUserID(ctx, userID)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBSessions(rows), nil
}

func (s *sessionStore) ListAllActive(ctx context.Context, p store.ListAllActiveSessionsParams) ([]store.ActiveSession, error) {
	// The MySQL query uses two positional params for the cursor (? IS NULL OR last_active_at < ?)
	// plus one for the limit.
	column1, lastActiveAt, err := parseMySQLCursor(p.Cursor)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListAllActiveSessions(ctx, gendb.ListAllActiveSessionsParams{
		Column1:      column1,
		LastActiveAt: lastActiveAt,
		Limit:        int32(p.Limit),
	})
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
	row, err := s.q.ValidateSessionWithUser(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	return &store.SessionWithUser{
		UserID:        row.ID,
		OrgID:         row.OrgID,
		Username:      row.Username,
		IsAdmin:       row.IsAdmin,
		EmailVerified: row.EmailVerified,
	}, nil
}
